package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Grid geometry of the icon picker: iconGridCols emoji per row,
// iconGridRows rows per page (56 per page, two pages for the 112-emoji
// pack). Telegram documents no hard cap on inline buttons; 8 per row is
// the widest that stays tappable on a phone (checked in the manual run).
const (
	iconGridCols = 8
	iconGridRows = 7
	// panelPrefix marks callback data that belongs to the options panel;
	// the bridge routes such presses here, everything else to outbound.
	panelPrefix = "o:"
)

// Every string an operator sees on the panel. English only, by decision
// (2026-09-03); option titles and descriptions come from the registry.
const (
	panelTitle           = "Options"
	panelPickGroup       = "Pick a group."
	panelCurrent         = "Current"
	panelOn              = "on"
	panelOff             = "off"
	panelIconFor         = "Icon for"
	panelOnlyPackIcons   = "Telegram allows only these icons for topics."
	panelPackUnavailable = "icon pack unavailable, restart the daemon"
	panelBack            = "‹ Back"
	panelClose           = "✖ Close"
	panelReset           = "↺ Reset to defaults"
	panelToastSaved      = "saved"
	panelToastUsedBy     = "used by %s"
	panelToastNotEdit    = "not editable yet"
	panelToastUnknown    = "unknown button"
	panelToastDefaults   = "already at defaults"
	panelInGeneral       = "options live in General"
)

// view is one rendered state of the panel: HTML text plus keyboard.
type view struct {
	text    string
	buttons []domain.Button
}

// panel is the /options message: one per chat, edited in place. It runs on
// the bridge goroutine; lastID is the message that carries the live
// keyboard so a new /options can retire it.
type panel struct {
	opts      *Options
	tg        domain.TelegramGateway
	log       *slog.Logger
	lastID    int
	chatID    int64
	operators []int64
	updates   *UpdateManager
	jobs      domain.UpdateJobStore
	launch    func(context.Context, string) (int, error)
	running   func() bool
	async     func(func(context.Context) any)
}

type updateCheckResult struct {
	panelID int
	check   domain.UpdateCheck
	err     error
}
type updateStartResult struct {
	panelID int
	job     domain.UpdateJob
	err     error
}

func newPanel(opts *Options, tg domain.TelegramGateway, log *slog.Logger) *panel {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &panel{opts: opts, tg: tg, log: log}
}

// Open answers /options with a fresh panel and retires the previous one.
func (p *panel) Open(ctx context.Context, cmd domain.GeneralCommand) error {
	if p.lastID != 0 {
		if err := p.tg.EditButtons(ctx, p.lastID, nil); err != nil {
			if isFatal(err) {
				return err
			}
			p.log.Debug("previous options panel not retired", slog.Int("message_id", p.lastID), slog.String("err", err.Error()))
		}
	}
	home := renderHome()
	id, err := p.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: home.text, HTML: true, ReplyTo: cmd.MessageID, Buttons: home.buttons})
	if err != nil {
		if isFatal(err) {
			return err
		}
		p.log.Warn("options panel send failed", slog.String("err", err.Error()))
		return nil
	}
	p.lastID = id
	p.log.Info("options panel opened", slog.Int("message_id", id), slog.Int64("by", cmd.FromID))
	return nil
}

// Press serves one button of the panel: apply, toast, re-render.
func (p *panel) Press(ctx context.Context, ev domain.ButtonPressed) error {
	if strings.HasPrefix(ev.Data, "o:u:") {
		return p.pressUpdate(ctx, ev)
	}
	p.log.Debug("options panel press", slog.String("data", ev.Data), slog.Int("message_id", ev.MessageID), slog.Int64("by", ev.FromID))
	a, err := parsePanelData(ev.Data)
	if err != nil {
		p.log.Warn("options panel data unknown", slog.String("data", ev.Data), slog.Int("message_id", ev.MessageID))
		return p.answer(ctx, ev.CallbackID, panelToastUnknown)
	}
	switch a.kind {
	case 'n':
		return p.answer(ctx, ev.CallbackID, "")
	case 'h':
		p.answer0(ctx, ev.CallbackID)
		return p.show(ctx, ev.MessageID, renderHome())
	case 'x':
		p.log.Debug("options panel closed", slog.Int("message_id", ev.MessageID))
		p.answer0(ctx, ev.CallbackID)
		if p.lastID == ev.MessageID {
			p.lastID = 0
		}
		return p.edit(ctx, ev.MessageID, renderSummary(p.opts.Get()), nil)
	case 'g':
		if _, ok := groupAt(a.group); !ok {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		p.answer0(ctx, ev.CallbackID)
		return p.show(ctx, ev.MessageID, renderGroup(a.group, p.opts.Get()))
	case 'r':
		g, ok := groupAt(a.group)
		if !ok {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		changed, err := p.opts.Reset(ctx, g.Name, ev.FromID)
		switch {
		case err != nil:
			p.answer0(ctx, ev.CallbackID, "⚠️ "+failureReason(err))
		case !changed:
			p.answer0(ctx, ev.CallbackID, panelToastDefaults)
		default:
			p.answer0(ctx, ev.CallbackID, panelToastSaved)
		}
		return p.show(ctx, ev.MessageID, renderGroup(a.group, p.opts.Get()))
	case 't':
		spec, ok := domain.LookupOption(a.key)
		if !ok || spec.Kind != domain.KindBool {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		next := "true"
		if p.opts.Get().Bool(a.key) {
			next = "false"
		}
		if err := p.opts.Set(ctx, a.key, next, ev.FromID); err != nil {
			p.answer0(ctx, ev.CallbackID, "⚠️ "+failureReason(err))
		} else {
			p.answer0(ctx, ev.CallbackID, panelToastSaved)
		}
		return p.show(ctx, ev.MessageID, renderGroup(groupIndex(spec.Group), p.opts.Get()))
	case 'e':
		if _, ok := domain.LookupOption(a.key); !ok {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		return p.answer(ctx, ev.CallbackID, panelToastNotEdit)
	case 'c':
		spec, ok := domain.LookupOption(a.key)
		if !ok || spec.Kind != domain.KindChoice {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		p.answer0(ctx, ev.CallbackID)
		choices := p.opts.Choices(spec.Choices)
		p.log.Debug("grid layout", slog.String("source", spec.Choices), slog.Int("entries", len(choices)), slog.Int("page", a.page))
		return p.show(ctx, ev.MessageID, renderGrid(spec, a.page, p.opts.Get(), choices))
	case 'v':
		spec, ok := domain.LookupOption(a.key)
		if !ok || spec.Kind != domain.KindChoice {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		pack := p.opts.Choices(spec.Choices)
		if a.index >= len(pack) {
			return p.answer(ctx, ev.CallbackID, panelToastUnknown)
		}
		value := pack[a.index]
		cur := p.opts.Get()
		if st, ok := domain.StatusOfIconKey(a.key); ok {
			if other, used := cur.StatusIcons().UsedBy(value); used && other != st {
				p.answer0(ctx, ev.CallbackID, fmt.Sprintf(panelToastUsedBy, other))
				return p.show(ctx, ev.MessageID, renderGrid(spec, a.index/(iconGridCols*iconGridRows), cur, pack))
			}
		}
		if err := p.opts.Set(ctx, a.key, value, ev.FromID); err != nil {
			p.answer0(ctx, ev.CallbackID, "⚠️ "+failureReason(err))
			return p.show(ctx, ev.MessageID, renderGrid(spec, a.index/(iconGridCols*iconGridRows), cur, pack))
		}
		p.answer0(ctx, ev.CallbackID, panelToastSaved)
		return p.show(ctx, ev.MessageID, renderGroup(groupIndex(spec.Group), p.opts.Get()))
	}
	return p.answer(ctx, ev.CallbackID, panelToastUnknown)
}

// answer closes the button spinner with a toast; failures are only logged.
func (p *panel) answer(ctx context.Context, callbackID, text string) error {
	p.answer0(ctx, callbackID, text)
	return nil
}

func (p *panel) answer0(ctx context.Context, callbackID string, text ...string) {
	toast := ""
	if len(text) > 0 {
		toast = text[0]
	}
	if err := p.tg.AnswerButton(ctx, callbackID, toast); err != nil {
		p.log.Warn("options panel answer failed", slog.String("callback_id", callbackID), slog.String("err", err.Error()))
	}
}
