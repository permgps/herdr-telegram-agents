package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// PrivateControl is confined to the shared bridge worker, so owner and guest
// Herdr mutations serialize. Downloads and git run asynchronously; their
// completion returns to that worker carrying the original grant revision.
type PrivateControl struct {
	dashboardAfter  int64
	Dashboard       *PrivateDashboard
	Output          *PrivateOutput
	followups       []privateFollowup
	Sharing         *Sharing
	Telegram        domain.DestinationTelegram
	Transport       domain.TelegramGateway
	Herdr           domain.HerdrGateway
	Git             domain.GitRunner
	Inbox           domain.InboxStore
	Agent           func(domain.Key) (domain.Agent, bool)
	Now             func() time.Time
	Async           func(func(context.Context) func(context.Context) error) bool
	Read            func(context.Context, domain.ShareOrigin, domain.Command) error
	Overview        func(context.Context, domain.PrivateMessage) error
	InvalidateOwner func(context.Context, domain.Key) error
	// InboxEnabled and InboxMaxBytes read the owner's Inbox options; nil
	// means on and 20 MiB, the defaults of domain.OptionInboxEnabled and
	// domain.OptionInboxMaxMB.
	InboxEnabled  func() bool
	InboxMaxBytes func() int64
	// InboxSharedMax is the room all recipients' files share in the inbox
	// (see state.Inbox.SharedQuota); nil means no limit beyond
	// InboxMaxBytes.
	InboxSharedMax func() int64
	// HoldPicker and ReleasePicker share the owner's picker hold (see
	// inbound.holdForPicker): a plain message after a kept picker is held
	// back once, whoever sends it. Nil means no hold.
	HoldPicker    func(domain.Key, domain.Agent) (string, bool)
	ReleasePicker func(domain.Key, string)
	// Log receives the control path's diagnostics; nil discards them.
	Log           *slog.Logger
	callbacks     map[string]privateButton
	mints         uint64
	typing        map[domain.Key]domain.ShareOrigin
	albums        map[string]*privateAlbum
	denialNotices map[string]uint64
}

type privateButton struct {
	surface domain.TopicAddress
	origin  domain.ShareOrigin
	message int
	keys    []string
	kind    string
	expires time.Time
	// seq is the agent's StateChangeSeq when a dialog button was drawn: a
	// press acts only while the agent still waits at that same dialog.
	seq int64
	// minted orders buttons that expire together, oldest first.
	minted uint64
}

// log returns the configured logger or a discarding one.
func (p *PrivateControl) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

// isDialog reports whether the button answers the agent's open dialog.
func (b privateButton) isDialog() bool {
	return b.kind == "dialog" || b.kind == "text"
}

// dropDialogButtons forgets the dialog buttons drawn for origin's mirror:
// a newer post replaces that keyboard.
func (p *PrivateControl) dropDialogButtons(o domain.ShareOrigin) {
	for ref, b := range p.callbacks {
		if b.isDialog() && b.origin.GrantID == o.GrantID && b.origin.Key == o.Key {
			delete(p.callbacks, ref)
		}
	}
}

// dropButtons forgets actor's buttons of the given kinds drawn on surface
// (the button's own surface, or its mirror when it has none), except the
// ones in keep: a refreshed keyboard replaces them once it is shown. It
// returns how many went.
func (p *PrivateControl) dropButtons(actor int64, surface domain.TopicAddress, keep []domain.Button, kinds ...string) int {
	removed := 0
	for ref, b := range p.callbacks {
		if b.origin.ActorID != actor || b.where() != surface || !slices.Contains(kinds, b.kind) {
			continue
		}
		if slices.ContainsFunc(keep, func(k domain.Button) bool { return k.Data == ref }) {
			continue
		}
		delete(p.callbacks, ref)
		removed++
	}
	return removed
}

// bindButtons records the message that carries buttons. A ref that is gone
// (evicted, or the dead "expired" ref) stays gone.
func (p *PrivateControl) bindButtons(message int, buttons []domain.Button) {
	for _, button := range buttons {
		if b, ok := p.callbacks[button.Data]; ok {
			b.message = message
			p.callbacks[button.Data] = b
		}
	}
}

// forgetButtons drops the refs of buttons that never reached a message.
func (p *PrivateControl) forgetButtons(buttons []domain.Button) {
	for _, button := range buttons {
		delete(p.callbacks, button.Data)
	}
}

// where is the address a press of the button must come from.
func (b privateButton) where() domain.TopicAddress {
	if b.surface.ChatID == 0 {
		return b.origin.Address
	}
	return b.surface
}

// evictActor makes room for one more button of actor: at
// privateButtonsPerActor live entries, that recipient's buttons that expire
// first (the earliest minted among equals) go, so only its own oldest
// keyboards answer "expired".
func (p *PrivateControl) evictActor(actor int64) {
	var refs []string
	for ref, b := range p.callbacks {
		if b.origin.ActorID == actor {
			refs = append(refs, ref)
		}
	}
	excess := len(refs) - privateButtonsPerActor + 1
	if excess <= 0 {
		return
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := p.callbacks[refs[i]], p.callbacks[refs[j]]
		if !a.expires.Equal(b.expires) {
			return a.expires.Before(b.expires)
		}
		return a.minted < b.minted
	})
	for _, ref := range refs[:excess] {
		delete(p.callbacks, ref)
	}
	p.log().Debug("private buttons evicted", slog.Int64("actor", actor), slog.Int("removed", excess), slog.String("reason", "actor_cap"))
}

type privateAlbum struct {
	origin      domain.ShareOrigin
	attachments []domain.TopicAttachment
	caption     string
	due         time.Time
}

const privateHelp = "Shared agent commands: /help, /status, /agents, /screen [N|all]. Control also permits prompts, attachments, /keys, /stop, /interrupt, /clear, /compact, /usage and /model. Repository, close and focus permissions are checked separately. /pause, /resume and /alias affect only this mirror."

func (p *PrivateControl) Handle(ctx context.Context, e domain.PrivateMessage) error {
	if e.FirstContact {
		_, err := p.Telegram.SendAt(ctx, e.Address, domain.Outgoing{Text: "You are registered. The owner can now share an agent with you. This message has not been sent to an agent."}, nil)
		return err
	}
	if e.Text == "" && e.Attachment == nil && e.CallbackID == "" {
		return p.notice(ctx, e, "This media is not supported for agent input. Your contact is registered.")
	}
	if p.Dashboard != nil {
		if handled, err := p.Dashboard.Local(ctx, e); handled {
			return err
		}
	}
	if e.CallbackID != "" {
		return p.press(ctx, e)
	}
	st, _ := p.Sharing.Snapshot()
	service := st.ServiceTopics[e.Contact.ActorID] == e.Address
	if p.Overview != nil && (service || e.Address.ThreadID == 0 || strings.HasPrefix(e.Text, "/agents") || strings.HasPrefix(e.Text, "/start")) {
		return p.Overview(ctx, e)
	}
	o, ok := p.resolve(e)
	if !ok {
		return p.notice(ctx, e, "No active shared agent in this topic. Use /agents.")
	}
	a, ok := p.Agent(o.Key)
	if !ok {
		return p.notice(ctx, e, "The shared agent is unavailable.")
	}
	cmd := domain.Route(e.Text, "", a.Status)
	if waiting, exists := p.typing[o.Key]; exists && !strings.HasPrefix(strings.TrimSpace(e.Text), "/") && e.Attachment == nil {
		if waiting.ActorID != o.ActorID || waiting.GrantID != o.GrantID || waiting.Revision != o.Revision {
			return p.notice(ctx, e, "Another controller is entering a dialog answer.")
		}
		cmd = domain.Command{Kind: domain.CmdPrompt, Text: e.Text}
	}
	action, allowed := domain.ShareCommandAction(cmd)
	if e.Attachment != nil {
		action = domain.ShareAttachment
		allowed = true
	}
	if !allowed {
		return p.notice(ctx, e, privateHelp)
	}
	if e.Stale && action != domain.ShareScreen && action != domain.ShareHistory && action != domain.ShareOverview {
		return p.notice(ctx, e, "This message predates startup. Please resend it.")
	}
	callCtx, done, decision := p.Sharing.Begin(ctx, o, action)
	if !decision.Allowed {
		return p.notice(ctx, e, "This action is not permitted by your current access.")
	}
	done() // Receipt admission only. Every effect below starts a fresh dispatch.
	_ = callCtx
	if e.Attachment != nil {
		return p.attachment(ctx, o, *e.Attachment)
	}
	switch cmd.Kind {
	case domain.CmdHelp:
		return p.send(ctx, o, privateHelp)
	case domain.CmdStatus:
		return p.send(ctx, o, a.Name+" · "+string(a.Status))
	case domain.CmdScreen:
		if p.Read != nil {
			return p.Read(ctx, o, cmd)
		}
		return p.screen(ctx, o, cmd.Lines)
	case domain.CmdPrompt:
		waiting, typed := p.typing[o.Key]
		if typed {
			if waiting.ActorID != o.ActorID || waiting.GrantID != o.GrantID || waiting.Revision != o.Revision {
				return p.notice(ctx, e, "Another controller is entering a dialog answer. Retry after it finishes.")
			}
			delete(p.typing, o.Key)
		}
		// As on the owner's path, the free text of a ✏️ answer is not held
		// and leaves the hold alone: it answers the dialog, not the picker.
		if !typed {
			if word, held := p.holdPicker(o.Key, a); held {
				p.log().Info("private prompt held for picker", slog.String("key", o.Key.String()), slog.String("word", word), slog.Int64("actor", o.ActorID))
				return p.send(ctx, o, fmt.Sprintf(pickerRefusedFmt, "/"+word))
			}
		}
		return p.effect(ctx, o, action, func(ctx context.Context) error { return p.Herdr.Prompt(ctx, o.Key.PaneID, cmd.Text) })
	case domain.CmdKeys:
		p.releasePicker(o.Key, "keys")
		return p.keys(ctx, o, cmd.Keys)
	case domain.CmdStop:
		p.releasePicker(o.Key, "stop")
		return p.keys(ctx, o, []string{domain.KeyEscape})
	case domain.CmdInterrupt:
		p.releasePicker(o.Key, "interrupt")
		return p.keys(ctx, o, []string{domain.KeyInterrupt})
	case domain.CmdForward:
		if a.Kind != domain.ClaudeKind || !cmd.Forward.Fits(a.Kind) {
			return p.send(ctx, o, "This command is supported only for Claude Code.")
		}
		// As on the owner's path: typed into a running turn or an open
		// dialog, its Enter would confirm the highlighted option and the
		// follow-up esc could interrupt the tool that just started.
		if hint, refused := forwardRefusal(a.Status); refused {
			p.log().Info("[FIX] private command refused", slog.String("key", o.Key.String()), slog.String("grant", o.GrantID),
				slog.String("word", forwardWord(cmd.Text)), slog.String("status", string(a.Status)))
			return p.send(ctx, o, "Not sent: "+hint+".")
		}
		err := p.effect(ctx, o, domain.ShareForward, func(ctx context.Context) error { return p.Herdr.Prompt(ctx, o.Key.PaneID, cmd.Text) })
		if err == nil && cmd.Forward.Post != domain.ForwardPostNone {
			p.followups = append(p.followups, privateFollowup{origin: o, due: p.Now().Add(2 * time.Second), dismiss: cmd.Forward.Dismiss})
		}
		return err
	case domain.CmdFocus:
		return p.effect(ctx, o, domain.ShareFocus, func(ctx context.Context) error { return p.Herdr.Focus(ctx, o.Key.PaneID) })
	case domain.CmdClose:
		return p.confirmClose(ctx, o)
	case domain.CmdGit:
		return p.git(ctx, o, a, cmd)
	}
	return nil
}

// holdPicker asks the owner's picker hold whether a plain message to key
// must be held back; the hold is one-shot, so a refusal releases it.
func (p *PrivateControl) holdPicker(key domain.Key, a domain.Agent) (string, bool) {
	if p.HoldPicker == nil {
		return "", false
	}
	return p.HoldPicker(key, a)
}

// releasePicker drops the owner's picker hold of key, if any.
func (p *PrivateControl) releasePicker(key domain.Key, reason string) {
	if p.ReleasePicker != nil {
		p.ReleasePicker(key, reason)
	}
}

// inboxEnabled is the owner's Inbox switch; on without a registry.
func (p *PrivateControl) inboxEnabled() bool {
	return p.InboxEnabled == nil || p.InboxEnabled()
}

// inboxMax is the owner's per-file Inbox limit; 20 MiB without a registry.
func (p *PrivateControl) inboxMax() int64 {
	if p.InboxMaxBytes == nil {
		return 20 << 20
	}
	return p.InboxMaxBytes()
}

// attachmentLimits returns the most one recipient file and one album may
// hold: the owner's per-file limit and twice that, both within the room
// the recipients share, so a file is refused before its download and an
// album never evicts its own first files.
func (p *PrivateControl) attachmentLimits() (file, album int64) {
	file, album = p.inboxMax(), 2*p.inboxMax()
	if p.InboxSharedMax != nil {
		shared := p.InboxSharedMax()
		file, album = min(file, shared), min(album, shared)
	}
	return file, album
}

func (p *PrivateControl) resolve(e domain.PrivateMessage) (domain.ShareOrigin, bool) {
	st, ok := p.Sharing.Snapshot()
	if !ok {
		return domain.ShareOrigin{}, false
	}
	for id, m := range st.Mirrors {
		if m.Address == e.Address {
			o, ok := p.Sharing.Origin(id)
			o.ActorID = e.Contact.ActorID
			o.MessageID = e.MessageID
			return o, ok
		}
	}
	return domain.ShareOrigin{}, false
}

func (p *PrivateControl) notice(ctx context.Context, e domain.PrivateMessage, text string) error {
	_, err := p.Telegram.SendAt(ctx, e.Address, domain.Outgoing{Text: text, ReplyTo: e.MessageID, MaxParts: 1}, nil)
	return err
}
func (p *PrivateControl) send(ctx context.Context, o domain.ShareOrigin, text string) error {
	_, err := p.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: text, ReplyTo: o.MessageID, MaxParts: 4}, p.Sharing.Guard(o, domain.ShareOutput))
	return err
}

func (p *PrivateControl) effect(ctx context.Context, o domain.ShareOrigin, action domain.ShareAction, run func(context.Context) error) error {
	_, release, decision := p.Sharing.Begin(ctx, o, action)
	if !decision.Allowed {
		return errors.New("private action authorization expired")
	}
	release()
	if err := p.Invalidate(ctx, o.Key); err != nil {
		return err
	}
	callCtx, done, d := p.Sharing.Begin(ctx, o, action)
	if !d.Allowed {
		return errors.New("private action authorization expired")
	}
	defer done()
	err := run(callCtx)
	p.Sharing.log.Info("private agent action", "actor_id", o.ActorID, "grant_id", o.GrantID, "action", string(action), "success", err == nil)
	if err != nil {
		_ = p.send(ctx, o, "The agent action failed. It was not retried.")
		return errors.New("private agent action failed")
	}
	return p.Telegram.ReactAt(ctx, domain.MessageAddress{ChatID: o.Address.ChatID, MessageID: o.MessageID}, "👍", p.Sharing.Guard(o, domain.ShareOutput))
}

func (p *PrivateControl) keys(ctx context.Context, o domain.ShareOrigin, keys []string) error {
	return p.effect(ctx, o, domain.ShareKeys, func(ctx context.Context) error { return p.Herdr.SendKeys(ctx, o.Key.PaneID, keys) })
}

func (p *PrivateControl) screen(ctx context.Context, o domain.ShareOrigin, lines int) error {
	callCtx, done, d := p.Sharing.Begin(ctx, o, domain.ShareScreen)
	if !d.Allowed {
		return errors.New("screen access denied")
	}
	defer done()
	if lines == 0 {
		lines = domain.MaxScreenLines
	}
	screen, err := p.Herdr.ReadScreen(callCtx, o.Key.PaneID, domain.ScreenVisible, lines)
	if err != nil {
		return p.send(ctx, o, "Screen unavailable.")
	}
	clean, _ := domain.CutChrome(screen.Text)
	_, err = p.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: clean, Code: true, ReplyTo: o.MessageID, MaxParts: 4}, p.Sharing.Guard(o, domain.ShareScreen))
	return err
}

func (p *PrivateControl) confirmClose(ctx context.Context, o domain.ShareOrigin) error {
	ref := p.button(privateButton{origin: o, kind: "close", expires: p.Now().Add(time.Minute)})
	id, err := p.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: "Close this agent's pane for everyone?", Buttons: []domain.Button{{Text: "Confirm close", Data: ref}}}, p.Sharing.Guard(o, domain.ShareClose))
	p.bindButtons(id, []domain.Button{{Data: ref}})
	return err
}

func (p *PrivateControl) button(b privateButton) string {
	if p.callbacks == nil {
		p.callbacks = map[string]privateButton{}
	}
	now := p.Now()
	for id, old := range p.callbacks {
		if !now.Before(old.expires) {
			delete(p.callbacks, id)
		}
	}
	p.evictActor(b.origin.ActorID)
	// The global cap is a backstop only; the dead ref is answered as stale.
	if len(p.callbacks) >= 4096 {
		held := 0
		for _, old := range p.callbacks {
			if old.origin.ActorID == b.origin.ActorID {
				held++
			}
		}
		p.log().Warn("private button table full", slog.Int("size", len(p.callbacks)), slog.Int("actor_entries", held))
		return "expired"
	}
	p.mints++
	b.minted = p.mints
	id := "pm:" + rand.Text()
	p.callbacks[id] = b
	return id
}

func (p *PrivateControl) press(ctx context.Context, e domain.PrivateMessage) error {
	b, ok := p.callbacks[e.CallbackData]
	if !ok || e.Stale || e.Contact.ActorID != b.origin.ActorID || e.Address != b.where() || e.MessageID != b.message || !p.Now().Before(b.expires) {
		return p.Transport.AnswerButton(ctx, e.CallbackID, "This button is stale or unavailable.")
	}
	_ = p.Transport.AnswerButton(ctx, e.CallbackID, "")
	delete(p.callbacks, e.CallbackData)
	o := b.origin
	o.MessageID = e.MessageID
	switch b.kind {
	case "status":
		a, _ := p.Agent(o.Key)
		return p.send(ctx, o, a.Name+" · "+string(a.Status))
	case "screen":
		if p.Read != nil {
			return p.Read(ctx, o, domain.Command{Kind: domain.CmdScreen})
		}
		return p.screen(ctx, o, 0)
	case "pause":
		if p.Dashboard != nil {
			e.Address = o.Address
			e.Text = "/pause"
			_, err := p.Dashboard.Local(ctx, e)
			return err
		}
	}
	if b.kind == "close" {
		return p.effect(ctx, o, domain.ShareClose, func(ctx context.Context) error { return p.Herdr.ClosePane(ctx, o.Key.PaneID) })
	}
	// The dialog may have been answered elsewhere and replaced by another:
	// the old "1" would then answer a question the recipient never saw.
	if a, live := p.Agent(o.Key); !live || a.Status != domain.StatusBlocked || a.StateChangeSeq != b.seq {
		p.log().Info("[FIX] private dialog button stale", slog.String("key", o.Key.String()), slog.String("grant", o.GrantID),
			slog.Bool("live", live), slog.String("status", string(a.Status)), slog.Int64("seq", a.StateChangeSeq), slog.Int64("button_seq", b.seq))
		return p.send(ctx, o, "That question is no longer open.")
	}
	err := p.effect(ctx, o, domain.ShareDialog, func(ctx context.Context) error { return p.Herdr.SendKeys(ctx, o.Key.PaneID, b.keys) })
	if err == nil && b.kind == "text" {
		if p.typing == nil {
			p.typing = map[domain.Key]domain.ShareOrigin{}
		}
		p.typing[o.Key] = o
	}
	if err == nil && p.Output != nil {
		p.Output.Refresh(o.Key)
	}
	return err
}

// Invalidate removes every mirror's callbacks before a controller acts.
func (p *PrivateControl) InvalidatePrivate(ctx context.Context, key domain.Key) error {
	delete(p.typing, key)
	messages := map[domain.MessageAddress]domain.ShareOrigin{}
	for ref, b := range p.callbacks {
		if b.origin.Key == key {
			delete(p.callbacks, ref)
			if b.message > 0 {
				messages[domain.MessageAddress{ChatID: b.origin.Address.ChatID, MessageID: b.message}] = b.origin
			}
		}
	}
	for m, o := range messages {
		_ = p.Telegram.EditButtonsAt(ctx, m, nil, p.Sharing.Guard(o, domain.ShareOutput))
	}
	return nil
}

func (p *PrivateControl) git(ctx context.Context, o domain.ShareOrigin, a domain.Agent, cmd domain.Command) error {
	if p.Git == nil || p.Async == nil {
		return p.send(ctx, o, "Repository commands unavailable.")
	}
	if cmd.Git.Sub == "" {
		return p.send(ctx, o, domain.GitUsage)
	}
	accepted := p.Async(func(ctx context.Context) func(context.Context) error {
		callCtx, done, d := p.Sharing.Begin(ctx, o, domain.ShareGit)
		if !d.Allowed {
			return nil
		}
		defer done()
		result, err := p.Git.Run(callCtx, a.Cwd, cmd.Git.Args)
		return func(ctx context.Context) error {
			if err != nil {
				return p.send(ctx, o, "Repository command failed.")
			}
			if len(result.Output) > 256<<10 {
				result.Output = result.Output[:256<<10]
			}
			return p.Telegram.DocumentAt(ctx, o.Address, domain.Document{Name: "git-" + cmd.Git.Sub + ".txt", Data: []byte(result.Output), ReplyTo: o.MessageID}, p.Sharing.Guard(o, domain.ShareGit))
		}
	})
	if !accepted {
		return p.send(ctx, o, "Private transfers are busy. Retry shortly.")
	}
	return nil
}

func (p *PrivateControl) attachment(ctx context.Context, o domain.ShareOrigin, a domain.TopicAttachment) error {
	if p.Inbox == nil || p.Async == nil {
		return p.send(ctx, o, "Attachments unavailable.")
	}
	// The owner's Inbox options bind recipients too, before any download.
	if !p.inboxEnabled() {
		p.log().Info("private attachment refused", slog.String("grant", o.GrantID), slog.String("reason", "inbox_off"), slog.Int64("bytes", a.Size))
		return p.send(ctx, o, inboxOff)
	}
	if max, _ := p.attachmentLimits(); a.Size > max {
		reason := "too_big"
		if a.Size <= p.inboxMax() {
			reason = "over_shared_quota"
		}
		p.log().Info("[FIX] private attachment refused", slog.String("grant", o.GrantID), slog.String("reason", reason), slog.Int64("bytes", a.Size),
			slog.Int64("limit", max))
		return p.send(ctx, o, fmt.Sprintf(inboxTooBigFmt, humanBytes(a.Size), humanBytes(max)))
	}
	if a.GroupID == "" {
		if !p.download(o, []domain.TopicAttachment{a}, a.Caption) {
			return p.send(ctx, o, "Private transfers are busy. Retry shortly.")
		}
		return nil
	}
	if p.albums == nil {
		p.albums = map[string]*privateAlbum{}
	}
	key := o.GrantID + ":" + strconv.FormatUint(o.Revision, 10) + ":" + strconv.FormatInt(o.ActorID, 10) + ":" + a.GroupID
	album := p.albums[key]
	if album == nil {
		if len(p.albums) >= 64 {
			return p.send(ctx, o, "Too many pending albums. Retry shortly.")
		}
		album = &privateAlbum{origin: o}
		p.albums[key] = album
	}
	if len(album.attachments) >= 10 {
		return p.send(ctx, o, "Album limit is 10 files.")
	}
	album.attachments = append(album.attachments, a)
	if a.Caption != "" {
		album.caption = a.Caption
	}
	album.due = p.Now().Add(time.Second)
	return nil
}

type privateFollowup struct {
	origin  domain.ShareOrigin
	due     time.Time
	dismiss bool
}

func (p *PrivateControl) Tick(ctx context.Context) error {
	if p.Dashboard != nil && p.Dashboard.Reconciler != nil {
		_ = p.Dashboard.Reconciler.Flush(ctx)
	}
	st, _ := p.Sharing.Snapshot()
	if p.denialNotices == nil {
		p.denialNotices = map[string]uint64{}
	}
	notices := 0
	for id, g := range st.Grants {
		if notices >= 2 {
			break
		}
		if g.State != domain.GrantRevoked && g.State != domain.GrantExpired && g.State != domain.GrantSuspended {
			continue
		}
		if p.denialNotices[id] == g.Revision {
			continue
		}
		notices++
		p.denialNotices[id] = g.Revision
		for ref, b := range p.callbacks {
			if b.origin.GrantID == id {
				delete(p.callbacks, ref)
			}
		}
		m := st.Mirrors[id]
		if m.Address.ThreadID == 0 {
			continue
		}
		guard := p.Sharing.BindingGuard(id, g.Revision)
		if m.KeyboardMessageID > 0 {
			_ = p.Telegram.EditButtonsAt(ctx, domain.MessageAddress{ChatID: m.Address.ChatID, MessageID: m.KeyboardMessageID}, nil, guard)
		}
		_, _ = p.Telegram.SendAt(ctx, m.Address, domain.Outgoing{Text: "Shared access is " + string(g.State) + ". Existing history is retained.", MaxParts: 1}, guard)
	}
	for key, o := range p.typing {
		_, release, decision := p.Sharing.Begin(ctx, o, domain.ShareDialog)
		if !decision.Allowed {
			delete(p.typing, key)
		} else {
			release()
		}
	}
	if p.Output != nil {
		_ = p.Output.Tick(ctx)
	}
	if p.Dashboard != nil {
		st, _ := p.Sharing.Snapshot()
		recipients := map[int64]bool{}
		for id := range st.Dashboards {
			recipients[id] = true
		}
		for _, g := range st.Grants {
			if g.State == domain.GrantActive {
				recipients[g.RecipientID] = true
			}
		}
		ids := make([]int64, 0, len(recipients))
		for id := range recipients {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		start := sort.Search(len(ids), func(i int) bool { return ids[i] > p.dashboardAfter })
		if start == len(ids) {
			start = 0
		}
		for n := 0; n < min(len(ids), 2); n++ {
			id := ids[(start+n)%len(ids)]
			p.dashboardAfter = id
			if p.Output != nil && p.Output.Automatic != nil && !p.Output.Automatic() {
				active := false
				for _, g := range st.Grants {
					if g.RecipientID == id && g.State == domain.GrantActive {
						active = true
						break
					}
				}
				if active {
					continue
				}
			}
			_ = p.Dashboard.Refresh(ctx, id, false)
		}
	}
	pending := p.followups[:0]
	for _, f := range p.followups {
		if p.Now().Before(f.due) {
			pending = append(pending, f)
			continue
		}
		_ = p.screen(ctx, f.origin, domain.MaxScreenLines)
		if f.dismiss {
			_ = p.keys(ctx, f.origin, []string{domain.KeyEscape})
		}
	}
	p.followups = pending
	for key, a := range p.albums {
		if !p.Now().Before(a.due) {
			delete(p.albums, key)
			if !p.download(a.origin, a.attachments, a.caption) {
				_ = p.send(ctx, a.origin, "Private transfers are busy. Please resend the album.")
			}
		}
	}
	return nil
}

// download fetches files off the bridge goroutine within attachmentLimits
// and saves them to the shared part of the inbox. Its completion types the paths unless the owner's
// picker hold takes them.
func (p *PrivateControl) download(o domain.ShareOrigin, files []domain.TopicAttachment, caption string) bool {
	max, albumMax := p.attachmentLimits()
	return p.Async(func(ctx context.Context) func(context.Context) error {
		callCtx, done, d := p.Sharing.Begin(ctx, o, domain.ShareAttachment)
		if !d.Allowed {
			return nil
		}
		defer done()
		var paths []string
		var total int
		for _, a := range files {
			data, err := p.Transport.Download(callCtx, a.FileID, max)
			if err != nil {
				return func(ctx context.Context) error { return p.send(ctx, o, "Attachment download failed.") }
			}
			total += len(data)
			if int64(total) > albumMax {
				p.log().Info("[FIX] private attachment refused", slog.String("grant", o.GrantID), slog.String("reason", "album_too_big"), slog.Int("bytes", total),
					slog.Int64("limit", albumMax))
				return func(ctx context.Context) error { return p.send(ctx, o, "Album exceeds "+humanBytes(albumMax)+".") }
			}
			if callCtx.Err() != nil {
				return nil
			}
			name := domain.SafeFileName(a.Name, domain.DefaultAttachmentName(a.Kind, a.MIME))
			path, err := p.Inbox.SaveShared(callCtx, fmt.Sprintf("%d-%s-%s", o.ActorID, rand.Text(), name), data)
			if errors.Is(err, domain.ErrFileTooBig) {
				return func(ctx context.Context) error {
					return p.send(ctx, o, "Attachment not saved: the shared inbox is full.")
				}
			}
			if err != nil {
				return func(ctx context.Context) error { return p.send(ctx, o, "Attachment save failed.") }
			}
			paths = append(paths, path)
		}
		return func(ctx context.Context) error {
			// The prompt ends with an Enter, so a kept picker would take it
			// as a choice: the file stays saved and is not typed, as on the
			// owner's path. A revoked grant leaves the hold alone.
			if _, release, d := p.Sharing.Begin(ctx, o, domain.ShareAttachment); d.Allowed {
				release()
				if a, live := p.Agent(o.Key); live {
					if word, held := p.holdPicker(o.Key, a); held {
						p.log().Info("[FIX] private attachment held for picker", slog.String("key", o.Key.String()), slog.String("word", word),
							slog.Int64("actor", o.ActorID), slog.Int("saved", len(paths)))
						return p.send(ctx, o, fmt.Sprintf(pickerRefusedFmt, "/"+word))
					}
				}
			}
			return p.effect(ctx, o, domain.ShareAttachment, func(ctx context.Context) error {
				return p.Herdr.Prompt(ctx, o.Key.PaneID, domain.AttachmentPrompt(caption, paths))
			})
		}
	})
}

func (p *PrivateControl) Invalidate(ctx context.Context, key domain.Key) error {
	_ = p.InvalidatePrivate(ctx, key)
	if p.InvalidateOwner != nil {
		return p.InvalidateOwner(ctx, key)
	}
	return nil
}
