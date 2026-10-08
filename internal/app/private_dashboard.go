package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// PrivateDashboard publishes only the recipient's active grants. Its service
// topic uses ordinary private-chat pinning and never General-topic methods.
type PrivateDashboard struct {
	Control     *PrivateControl
	Reconciler  *PrivateReconciler
	BotUsername string
	menus       domain.PrivateCommandRegistrar
	last        map[int64]string
	refreshed   map[int64]time.Time
	// menu is the command list last published per recipient; setMyCommands
	// runs only when it changes.
	menu map[int64]string
}

func NewPrivateDashboard(c *PrivateControl, r *PrivateReconciler, username string, menus domain.PrivateCommandRegistrar) *PrivateDashboard {
	return &PrivateDashboard{Control: c, Reconciler: r, BotUsername: username, menus: menus, last: map[int64]string{}, refreshed: map[int64]time.Time{}, menu: map[int64]string{}}
}

func (d *PrivateDashboard) Handle(ctx context.Context, e domain.PrivateMessage) error {
	if strings.HasPrefix(e.Text, "/start mirror_") {
		return d.navigate(ctx, e, strings.TrimPrefix(e.Text, "/start mirror_"))
	}
	if strings.HasPrefix(e.Text, "/help") {
		return d.Control.notice(ctx, e, privateHelp)
	}
	return d.Refresh(ctx, e.Contact.ActorID, true)
}

func (d *PrivateDashboard) active(recipient int64) (domain.SharingState, []domain.ShareGrant) {
	st, ok := d.Control.Sharing.Snapshot()
	if !ok {
		return st, nil
	}
	var grants []domain.ShareGrant
	for id, g := range st.Grants {
		if g.RecipientID != recipient {
			continue
		}
		o, _ := d.Control.Sharing.Origin(id)
		ctx, done, decision := d.Control.Sharing.Begin(context.Background(), o, domain.ShareOverview)
		if !decision.Allowed {
			continue
		}
		done()
		_ = ctx
		grants = append(grants, g)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	return st, grants
}

func (d *PrivateDashboard) Refresh(ctx context.Context, recipient int64, explicit bool) error {
	c := d.Control
	st, grants := d.active(recipient)
	guard := d.guard(grants)
	text := "Your shared agents\n"
	for _, g := range grants {
		a, _ := c.Agent(g.Key)
		m := st.Mirrors[g.ID]
		label := a.Label()
		if m.Preferences.Alias != "" {
			label = m.Preferences.Alias
		}
		text += fmt.Sprintf("\n%s · %s · %s", html.EscapeString(label), g.Role, a.Status)
		if m.Preferences.Paused {
			text += " · paused"
		}
		if d.BotUsername != "" {
			text += fmt.Sprintf(" · <a href=\"https://t.me/%s?start=mirror_%s\">Open</a>", html.EscapeString(d.BotUsername), html.EscapeString(m.NavigationID))
		}
	}
	if len(grants) == 0 {
		text += "No active grants. The owner can share an agent after you contact this bot."
	}
	if !explicit && d.last[recipient] == text && c.Now().Sub(d.refreshed[recipient]) < 5*time.Minute {
		return nil
	}
	address, exists := st.ServiceTopics[recipient]
	if !exists && len(grants) > 0 {
		address = domain.TopicAddress{ChatID: recipient}
		if err := c.Sharing.ServiceTopic(ctx, recipient, address, c.Now()); err != nil {
			return err
		}
		topic, err := c.Telegram.CreateTopicAt(ctx, recipient, "Shared agents", domain.StatusIdle, nil)
		if err != nil {
			return errors.New("service topic creation needs owner repair")
		}
		address.ThreadID = topic.ThreadID
		if err := c.Sharing.ServiceTopic(ctx, recipient, address, c.Now()); err != nil {
			return err
		}
	}
	if address.ThreadID == 0 {
		if !explicit {
			return nil
		}
		_, err := c.Telegram.SendAt(ctx, domain.TopicAddress{ChatID: recipient}, domain.Outgoing{Text: text, HTML: true, MaxParts: 1}, guard)
		return err
	}
	message := st.Dashboards[recipient]
	var buttons []domain.Button
	for _, g := range grants[:min(len(grants), 10)] {
		o, _ := c.Sharing.Origin(g.ID)
		o.Revision, o.Key = g.Revision, g.Key
		a, _ := c.Agent(g.Key)
		for _, kind := range []string{"status", "screen", "pause"} {
			ref := c.button(privateButton{origin: o, surface: address, kind: kind, expires: c.Now().Add(10 * time.Minute)})
			buttons = append(buttons, domain.Button{Text: kind + " · " + a.Label(), Data: ref})
		}
	}
	message, err := d.show(ctx, recipient, address, message, text, buttons, guard)
	if err != nil {
		// The phone still shows the last keyboard: it keeps working, and
		// the buttons minted for the failed one go.
		c.forgetButtons(buttons)
		c.log().Info("[FIX] private dashboard not refreshed, last buttons kept", slog.Int64("actor", recipient),
			slog.Int("forgotten", len(buttons)), slog.String("err", err.Error()))
		return err
	}
	c.bindButtons(message.MessageID, buttons)
	// The keyboard just shown replaces the last one: without this every
	// refresh would leave three live buttons per grant behind until they
	// expire.
	if removed := c.dropButtons(recipient, address, buttons, "status", "screen", "pause"); removed > 0 {
		c.log().Debug("private buttons pruned", slog.Int64("actor", recipient), slog.String("kinds", "status,screen,pause"),
			slog.Int("removed", removed), slog.String("reason", "refresh"))
	}
	if d.menus != nil {
		commands := []string{"start", "help", "agents", "status"}
		if len(grants) > 0 {
			commands = append(commands, "screen", "pause", "resume", "alias", "silent", "display", "fold", "metadata")
			control := false
			for _, g := range grants {
				control = control || g.Role == domain.ShareControl
			}
			if control {
				commands = append(commands, "keys", "stop", "interrupt", "clear", "compact", "usage", "model", "git")
			}
			for _, pair := range []struct {
				action  domain.ShareAction
				command string
			}{{domain.ShareGit, "git"}, {domain.ShareClose, "close"}, {domain.ShareFocus, "focus"}} {
				for _, g := range grants {
					if domain.SharePermits(g, pair.action) {
						commands = append(commands, pair.command)
						break
					}
				}
			}
		}
		if key := strings.Join(commands, " "); d.menu[recipient] != key {
			if err := d.menus.RegisterPrivateCommands(ctx, recipient, commands); err == nil {
				d.menu[recipient] = key
			}
		}
	}
	d.last[recipient] = text
	d.refreshed[recipient] = c.Now()
	return nil
}

// show edits recipient's dashboard message in place, or sends, records and
// pins a new one when there is none or it is gone, and returns the message
// that now carries buttons.
func (d *PrivateDashboard) show(ctx context.Context, recipient int64, address domain.TopicAddress, message domain.MessageAddress,
	text string, buttons []domain.Button, guard domain.DispatchGuard) (domain.MessageAddress, error) {
	c := d.Control
	if message.MessageID > 0 {
		err := c.Telegram.EditTextAt(ctx, message, text, true, buttons, guard)
		if err == nil {
			return message, nil
		}
		if !errors.Is(err, domain.ErrMessageGone) {
			return message, err
		}
	}
	id, err := c.Telegram.SendAt(ctx, address, domain.Outgoing{Text: text, HTML: true, Buttons: buttons, MaxParts: 1}, guard)
	if err != nil {
		return message, err
	}
	message = domain.MessageAddress{ChatID: recipient, MessageID: id}
	if err := c.Sharing.Dashboard(ctx, recipient, message, c.Now()); err != nil {
		return message, err
	}
	_ = c.Telegram.PinAt(ctx, message, nil)
	return message, nil
}

func (d *PrivateDashboard) navigate(ctx context.Context, e domain.PrivateMessage, reference string) error {
	st, grants := d.active(e.Contact.ActorID)
	for _, g := range grants {
		if reference != "" && st.Mirrors[g.ID].NavigationID == reference {
			o, _ := d.Control.Sharing.Origin(g.ID)
			return d.Control.send(ctx, o, "This is your shared agent topic. Telegram may require opening it manually from the topic list.")
		}
	}
	return d.Control.notice(ctx, e, "This navigation link is unavailable.")
}

func (d *PrivateDashboard) Local(ctx context.Context, e domain.PrivateMessage) (bool, error) {
	fields := strings.Fields(e.Text)
	if len(fields) == 0 {
		return false, nil
	}
	word := strings.ToLower(fields[0])
	switch word {
	case "/pause", "/resume", "/alias", "/silent", "/display", "/fold", "/metadata":
	default:
		return false, nil
	}
	if e.Stale {
		return true, d.Control.notice(ctx, e, "This message predates startup. Please resend it.")
	}
	o, ok := d.Control.resolve(e)
	if !ok {
		return true, d.Control.notice(ctx, e, "No shared agent in this topic.")
	}
	callCtx, done, decision := d.Control.Sharing.Begin(ctx, o, domain.SharePreferences)
	if !decision.Allowed {
		return true, d.Control.notice(ctx, e, "Access unavailable.")
	}
	done()
	_ = callCtx
	st, _ := d.Control.Sharing.Snapshot()
	prefs := st.Mirrors[o.GrantID].Preferences
	arg := strings.TrimSpace(strings.TrimPrefix(e.Text, fields[0]))
	switch word {
	case "/pause":
		prefs.Paused = true
	case "/resume":
		prefs.Paused = false
	case "/alias":
		prefs.Alias = arg
	case "/silent":
		prefs.Silent = !prefs.Silent
	case "/metadata":
		prefs.Metadata = !prefs.Metadata
	case "/display":
		prefs.Display = arg
	case "/fold":
		n, err := strconv.Atoi(arg)
		if err != nil {
			return true, d.Control.send(ctx, o, "Use /fold 0..200.")
		}
		prefs.Fold = n
	}
	if err := d.Control.Sharing.Preferences(ctx, o, prefs, d.Control.Now()); err != nil {
		return true, err
	}
	if !prefs.Paused {
		a, _ := d.Control.Agent(o.Key)
		_ = d.Reconciler.Observe(ctx, AgentEvent{Kind: AgentChanged, Agent: a})
		if word == "/resume" && d.Control.Output != nil {
			d.Control.Output.Refresh(o.Key)
		}
	}
	return true, d.Control.send(ctx, o, "Mirror settings saved.")
}

func (d *PrivateDashboard) guard(grants []domain.ShareGrant) domain.DispatchGuard {
	origins := make([]domain.ShareOrigin, 0, len(grants))
	for _, g := range grants {
		o, _ := d.Control.Sharing.Origin(g.ID)
		o.Revision, o.Key = g.Revision, g.Key
		origins = append(origins, o)
	}
	return func(ctx context.Context) (context.Context, context.CancelFunc, error) {
		callCtx, cancel := context.WithCancel(ctx)
		var releases []context.CancelFunc
		var stops []func() bool
		done := func() {
			cancel()
			for _, stop := range stops {
				stop()
			}
			for _, release := range releases {
				release()
			}
		}
		for _, o := range origins {
			admitted, release, decision := d.Control.Sharing.Begin(callCtx, o, domain.ShareOverview)
			if !decision.Allowed {
				done()
				return nil, nil, errors.New("private overview changed")
			}
			releases = append(releases, release)
			stops = append(stops, context.AfterFunc(admitted, cancel))
		}
		return callCtx, done, nil
	}
}
