package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// PrivateOutput captures once per agent after settling, then renders per
// mirror. Only ExactReplies is wired here; the cwd-selected Claude reader is
// never reachable from private delivery.
type PrivateOutput struct {
	Control      *PrivateControl
	Capture      *Capture
	ExactReplies domain.ReplySource
	Automatic    func() bool
	pending      map[domain.Key]privateOutputPending
	last         map[string]string
	working      map[domain.Key]bool
}

type privateOutputPending struct {
	captured bool
	screen   domain.Screen
	reply    domain.Reply
	silent   bool
	agent    domain.Agent
	due      time.Time
	grants   map[string]uint64
	// attempts counts the waits for a reply the source still reports as
	// pending (domain.ErrReplyPending).
	attempts int
}

// privateReplyFresh reports whether reply may stand in for the screen of g's
// mirror: a reply written before the grant was active belongs to the
// owner's private history.
func privateReplyFresh(g domain.ShareGrant, reply domain.Reply) bool {
	return reply.Text != "" && reply.Written.After(g.ActivatedAt)
}

func (p *PrivateOutput) Observe(e AgentEvent) {
	if p.pending == nil {
		p.pending = map[domain.Key]privateOutputPending{}
		p.last = map[string]string{}
		p.working = map[domain.Key]bool{}
	}
	if e.Kind == AgentGone {
		delete(p.pending, e.Agent.Key)
		delete(p.working, e.Agent.Key)
		return
	}
	if e.Agent.Status == domain.StatusWorking {
		p.working[e.Agent.Key] = true
		delete(p.pending, e.Agent.Key)
		return
	}
	if e.Kind == AgentAppeared {
		return
	}
	a := e.Agent
	if a.Status == domain.StatusIdle && p.working[a.Key] {
		a.Status = domain.StatusDone
	}
	if a.Status != domain.StatusBlocked && a.Status != domain.StatusDone {
		return
	}
	delete(p.working, a.Key)
	st, ok := p.Control.Sharing.Snapshot()
	if !ok {
		return
	}
	grants := map[string]uint64{}
	for id, g := range st.Grants {
		if g.Key == a.Key && g.State == domain.GrantActive {
			grants[id] = g.Revision
		}
	}
	if len(grants) == 0 {
		return
	}
	p.pending[a.Key] = privateOutputPending{agent: a, due: p.Control.Now().Add(2 * time.Second), grants: grants}
}

func (p *PrivateOutput) Tick(ctx context.Context) error {
	for key, job := range p.pending {
		if p.Control.Now().Before(job.due) {
			continue
		}
		delete(p.pending, key)
		if p.Automatic != nil && !p.Automatic() {
			continue
		}
		if err := p.deliver(ctx, job); err != nil {
			return err
		}
		return nil // Bound work per bridge tick; remaining agents keep their jobs.
	}
	return nil
}

func (p *PrivateOutput) deliver(ctx context.Context, job privateOutputPending) error {
	c := p.Control
	st, ok := c.Sharing.Snapshot()
	if !ok {
		return nil
	}
	var origins []domain.ShareOrigin
	for id, rev := range job.grants {
		g, ok := st.Grants[id]
		m := st.Mirrors[id]
		if !ok || g.Revision != rev || m.Preferences.Paused || st.Recipients[g.RecipientID].Unavailable {
			continue
		}
		o, _ := c.Sharing.Origin(id)
		o.Revision, o.Key = g.Revision, g.Key
		callCtx, done, d := c.Sharing.Begin(ctx, o, domain.ShareOutput)
		if !d.Allowed {
			continue
		}
		done()
		_ = callCtx
		origins = append(origins, o)
	}
	if len(origins) == 0 {
		return nil
	}
	a, live := c.Agent(job.agent.Key)
	if !live {
		return nil
	}
	if !job.captured {
		if a.Kind == "opencode" && a.SessionDigest != "" && p.ExactReplies != nil && job.agent.Status == domain.StatusDone {
			var err error
			job.reply, err = p.ExactReplies.LastReply(ctx, a)
			if errors.Is(err, domain.ErrReplyPending) {
				if origins = p.waitForReply(st, a.Key, job, origins); len(origins) == 0 {
					return nil
				}
			}
		}
		var err error
		job.screen, err = c.Herdr.ReadScreen(ctx, a.PaneID, domain.ScreenDetection, domain.MaxScreenLines)
		if err != nil {
			return err
		}
		job.captured = true
	}
	screen, reply := job.screen, job.reply
	clean, _ := domain.CutChrome(screen.Text)
	remaining := map[string]uint64{}
	for _, o := range origins[min(len(origins), 4):] {
		remaining[o.GrantID] = o.Revision
	}
	if len(remaining) > 0 {
		job.grants = remaining
		job.due = c.Now().Add(time.Second)
		p.pending[a.Key] = job
	}
	for _, o := range origins[:min(len(origins), 4)] {
		g := st.Grants[o.GrantID]
		m := st.Mirrors[o.GrantID]
		text := clean
		formatted := false
		footer := ""
		// Reject replies from before activation even with a verified source.
		if m.Preferences.Display != "screen" && privateReplyFresh(g, reply) {
			text = reply.Text
			formatted = m.Preferences.Display == "formatted"
			if m.Preferences.Metadata {
				footer = reply.Meta.Line()
			}
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:]) + ":" + strconv.FormatUint(o.Revision, 10)
		if text == "" || (p.last[o.GrantID] == hash || m.LastOutput == hash) {
			continue
		}
		c.dropDialogButtons(o)
		buttons := p.dialogButtons(o, screen.Text, job.agent)
		id, err := c.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: text, Code: !formatted, Markdown: formatted, Footer: footer, Fold: m.Preferences.Fold, Notify: job.agent.Status == domain.StatusBlocked && !m.Preferences.Silent && !job.silent, Buttons: buttons, MaxParts: 4}, c.Sharing.Guard(o, domain.ShareOutput))
		if err != nil {
			if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrRecipientUnavailable) {
				_ = c.Sharing.Reachability(ctx, g.RecipientID, true, c.Now())
			}
			continue
		}
		p.last[o.GrantID] = hash
		keyboard := 0
		if len(buttons) > 0 {
			keyboard = id
		}
		c.Sharing.RecordOutput(o, hash, keyboard)
		c.bindButtons(id, buttons)
	}
	return nil
}

// waitForReply handles a reply source that says the turn is still running
// (OpenCode reports done at every step of a turn). The mirrors that show
// replies wait for it: their grants go back to the queue up to
// replyPendingRetries times, replyPendingDelay apart, and a newer event for
// the key replaces the wait as on the owner's topics. Screen mirrors never
// wait. It returns the origins to deliver now; once the attempts are used
// up, that is all of them and they get the screen.
func (p *PrivateOutput) waitForReply(st domain.SharingState, key domain.Key, job privateOutputPending, origins []domain.ShareOrigin) []domain.ShareOrigin {
	var now, wait []domain.ShareOrigin
	for _, o := range origins {
		if st.Mirrors[o.GrantID].Preferences.Display == "screen" {
			now = append(now, o)
		} else {
			wait = append(wait, o)
		}
	}
	if len(wait) == 0 {
		return origins
	}
	if job.attempts >= replyPendingRetries {
		p.Control.log().Info("private reply still pending, screen posted", slog.String("key", key.String()), slog.Int("attempts", job.attempts))
		return origins
	}
	// Screen mirrors past this tick's batch ride along with the retry, which
	// delivers them like any queued grant.
	retry := job
	retry.attempts++
	retry.due = p.Control.Now().Add(replyPendingDelay)
	retry.grants = map[string]uint64{}
	for _, o := range append(wait, now[min(len(now), 4):]...) {
		retry.grants[o.GrantID] = o.Revision
	}
	p.pending[key] = retry
	p.Control.log().Debug("private reply pending, retry scheduled", slog.String("key", key.String()),
		slog.Int("attempt", retry.attempts), slog.Int("max", replyPendingRetries))
	return now[:min(len(now), 4)]
}

func (p *PrivateOutput) dialogButtons(o domain.ShareOrigin, screen string, agent domain.Agent) []domain.Button {
	st, _ := p.Control.Sharing.Snapshot()
	g := st.Grants[o.GrantID]
	if agent.Status != domain.StatusBlocked || !domain.SharePermits(g, domain.ShareDialog) {
		return nil
	}
	d := domain.ParseDialog(screen)
	var buttons []domain.Button
	add := func(label, kind string, keys []string) {
		ref := p.Control.button(privateButton{origin: o, kind: kind, keys: keys, expires: p.Control.Now().Add(10 * time.Minute), seq: agent.StateChangeSeq})
		buttons = append(buttons, domain.Button{Text: label, Data: ref})
	}
	for _, choice := range d.Choices {
		add(choice.Label, "dialog", []string{strconv.Itoa(choice.Number)})
	}
	if d.Multi {
		add("Submit", "dialog", d.SubmitKeys())
	}
	if d.TextEntry > 0 {
		// The dialog rides along so a plain message can answer it through
		// the same entry once the screen proves it is still this dialog.
		ref := p.Control.button(privateButton{origin: o, kind: "text", keys: []string{strconv.Itoa(d.TextEntry)},
			expires: p.Control.Now().Add(10 * time.Minute), seq: agent.StateChangeSeq, dialog: d})
		buttons = append(buttons, domain.Button{Text: d.TextLabel, Data: ref})
	}
	return buttons
}

func (p *PrivateOutput) Read(ctx context.Context, o domain.ShareOrigin, cmd domain.Command) error {
	if !cmd.All {
		c := p.Control
		st, _ := c.Sharing.Snapshot()
		m := st.Mirrors[o.GrantID]
		a, live := c.Agent(o.Key)
		if live && cmd.Lines == 0 && m.Preferences.Display != "screen" && a.Kind == "opencode" && a.SessionDigest != "" && p.ExactReplies != nil {
			callCtx, done, decision := c.Sharing.Begin(ctx, o, domain.ShareScreen)
			if !decision.Allowed {
				return errors.New("screen access denied")
			}
			reply, err := p.ExactReplies.LastReply(callCtx, a)
			done()
			g, granted := st.Grants[o.GrantID]
			fresh := err == nil && granted && privateReplyFresh(g, reply)
			if !fresh {
				c.log().Debug("private reply skipped", slog.String("grant", o.GrantID), slog.String("reason", privateSkipReason(err, granted, reply)))
			}
			if fresh {
				footer := ""
				if m.Preferences.Metadata {
					footer = reply.Meta.Line()
				}
				formatted := m.Preferences.Display == "formatted"
				_, err = c.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: reply.Text, Code: !formatted, Markdown: formatted, Fold: m.Preferences.Fold, Footer: footer, ReplyTo: o.MessageID, MaxParts: 4}, c.Sharing.Guard(o, domain.ShareScreen))
				return err
			}
		}
		return c.screen(ctx, o, cmd.Lines)
	}
	st, ok := p.Control.Sharing.Snapshot()
	if !ok {
		return ErrSharingUnavailable
	}
	g, ok := st.Grants[o.GrantID]
	if !ok {
		return ErrSharingUnavailable
	}
	text := ""
	if p.Capture != nil {
		text = p.Capture.PrivateSince(o.Key, g.ActivatedAt)
	}
	if strings.TrimSpace(text) == "" {
		text = "No screen history captured since this grant was activated."
	}
	return p.Control.Telegram.DocumentAt(ctx, o.Address, domain.Document{Name: "shared-screen.txt", Data: []byte(text), ReplyTo: o.MessageID}, p.Control.Sharing.Guard(o, domain.ShareHistory))
}

// privateSkipReason names why /screen fell back from the reply to the
// screen, for the debug log.
func privateSkipReason(err error, granted bool, reply domain.Reply) string {
	switch {
	case err != nil || !granted:
		return "error"
	case reply.Text == "":
		return "empty"
	}
	return "before_grant"
}

func (p *PrivateOutput) Refresh(key domain.Key) {
	a, ok := p.Control.Agent(key)
	if !ok {
		return
	}
	p.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	job, ok := p.pending[key]
	if ok {
		for id := range job.grants {
			o, _ := p.Control.Sharing.Origin(id)
			p.Control.Sharing.RecordOutput(o, "", 0)
			delete(p.last, id)
		}
		job.silent = true
		p.pending[key] = job
	}
}
