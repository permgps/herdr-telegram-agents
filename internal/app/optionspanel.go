package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
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
	opts   *Options
	tg     domain.TelegramGateway
	log    *slog.Logger
	lastID int
}

func newPanel(opts *Options, tg domain.TelegramGateway, log *slog.Logger) *panel {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &panel{opts: opts, tg: tg, log: log}
}

// panelAction is one decoded callback. Kinds: h home, g group, t toggle a
// bool, c open a choice grid page, v pick a choice by index, r reset a
// group, e a text option (no editor yet), x close, n no-op.
type panelAction struct {
	kind  byte
	group int
	key   string
	page  int
	index int
}

func dataHome() string             { return panelPrefix + "h" }
func dataClose() string            { return panelPrefix + "x" }
func dataNoop() string             { return panelPrefix + "n" }
func dataGroup(gi int) string      { return fmt.Sprintf("%sg:%d", panelPrefix, gi) }
func dataReset(gi int) string      { return fmt.Sprintf("%sr:%d", panelPrefix, gi) }
func dataToggle(key string) string { return panelPrefix + "t:" + key }
func dataText(key string) string   { return panelPrefix + "e:" + key }
func dataGrid(key string, page int) string {
	return fmt.Sprintf("%sc:%s:%d", panelPrefix, key, page)
}
func dataPick(key string, index int) string {
	return fmt.Sprintf("%sv:%s:%d", panelPrefix, key, index)
}

var errPanelData = errors.New("malformed panel data")

// parsePanelData decodes callback data written by the data* helpers.
func parsePanelData(data string) (panelAction, error) {
	if !strings.HasPrefix(data, panelPrefix) {
		return panelAction{}, errPanelData
	}
	parts := strings.Split(strings.TrimPrefix(data, panelPrefix), ":")
	if len(parts) == 0 || len(parts[0]) != 1 {
		return panelAction{}, errPanelData
	}
	a := panelAction{kind: parts[0][0]}
	num := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, errPanelData
		}
		return n, nil
	}
	var err error
	switch a.kind {
	case 'h', 'x', 'n':
		if len(parts) != 1 {
			return panelAction{}, errPanelData
		}
	case 'g', 'r':
		if len(parts) != 2 {
			return panelAction{}, errPanelData
		}
		a.group, err = num(parts[1])
	case 't', 'e':
		if len(parts) != 2 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
	case 'c':
		if len(parts) != 3 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
		a.page, err = num(parts[2])
	case 'v':
		if len(parts) != 3 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
		a.index, err = num(parts[2])
	default:
		return panelAction{}, errPanelData
	}
	if err != nil {
		return panelAction{}, err
	}
	return a, nil
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
		return p.show(ctx, ev.MessageID, renderGrid(spec, a.page, p.opts.Get(), p.opts.Choices(spec.Choices)))
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

// show edits the panel message; when the edit fails for a non-fatal reason
// (the message is gone) a fresh panel is sent instead.
func (p *panel) show(ctx context.Context, messageID int, v view) error {
	if err := p.edit(ctx, messageID, v.text, v.buttons); err != nil {
		return err
	}
	if p.lastID == 0 {
		p.lastID = messageID
	}
	return nil
}

func (p *panel) edit(ctx context.Context, messageID int, text string, buttons []domain.Button) error {
	err := p.tg.EditText(ctx, messageID, text, true, buttons)
	if err == nil {
		return nil
	}
	if isFatal(err) {
		return err
	}
	p.log.Warn("options panel edit failed, sending a new panel", slog.Int("message_id", messageID), slog.String("err", err.Error()))
	id, err := p.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: text, HTML: true, Buttons: buttons})
	if err != nil {
		if isFatal(err) {
			return err
		}
		p.log.Warn("options panel re-send failed", slog.String("err", err.Error()))
		return nil
	}
	p.lastID = id
	p.log.Info("options panel re-sent", slog.Int("message_id", id))
	return nil
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

func groupAt(i int) (domain.OptionGroup, bool) {
	groups := domain.OptionGroups()
	if i < 0 || i >= len(groups) {
		return domain.OptionGroup{}, false
	}
	return groups[i], true
}

func groupIndex(name string) int {
	for i, g := range domain.OptionGroups() {
		if g.Name == name {
			return i
		}
	}
	return 0
}

// renderHome lists the groups with their descriptions.
func renderHome() view {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n%s\n", panelTitle, panelPickGroup)
	groups := domain.OptionGroups()
	buttons := make([]domain.Button, 0, len(groups)+1)
	for i, g := range groups {
		fmt.Fprintf(&b, "\n<b>%s</b>: %s", html.EscapeString(g.Title), html.EscapeString(g.Description))
		buttons = append(buttons, domain.Button{Text: g.Title, Data: dataGroup(i)})
	}
	buttons = append(buttons, domain.Button{Text: panelClose, Data: dataClose()})
	return view{b.String(), buttons}
}

// renderGroup lists one group's options with descriptions and values; the
// buttons toggle bools, open grids for choices, and navigate.
func renderGroup(gi int, opts domain.Options) view {
	g, ok := groupAt(gi)
	if !ok {
		return renderHome()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n<i>%s</i>\n", html.EscapeString(g.Title), html.EscapeString(g.Description))
	specs := domain.OptionsInGroup(g.Name)
	buttons := make([]domain.Button, 0, len(specs)+3)
	for _, spec := range specs {
		value := opts.String(spec.Key)
		fmt.Fprintf(&b, "\n<b>%s</b>: %s\n%s: %s", html.EscapeString(spec.Title), html.EscapeString(spec.Description),
			panelCurrent, html.EscapeString(displayValue(spec, value)))
		switch spec.Kind {
		case domain.KindBool:
			box := "☐"
			if value == "true" {
				box = "☑"
			}
			buttons = append(buttons, domain.Button{Text: box + " " + spec.Title, Data: dataToggle(spec.Key)})
		case domain.KindChoice:
			buttons = append(buttons, domain.Button{Text: value + " " + spec.Title, Data: dataGrid(spec.Key, 0)})
		default:
			buttons = append(buttons, domain.Button{Text: spec.Title + ": " + value, Data: dataText(spec.Key)})
		}
	}
	buttons = append(buttons,
		domain.Button{Text: panelReset, Data: dataReset(gi)},
		domain.Button{Text: panelBack, Data: dataHome()},
		domain.Button{Text: panelClose, Data: dataClose()})
	return view{b.String(), buttons}
}

// renderGrid draws one page of the choice list for spec, the current value
// bracketed, with a navigation row and Back.
func renderGrid(spec domain.OptionSpec, page int, opts domain.Options, pack []string) view {
	current := opts.String(spec.Key)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s %s</b>\n%s\n%s\n%s: %s", panelIconFor, html.EscapeString(spec.Title),
		html.EscapeString(spec.Description), panelOnlyPackIcons, panelCurrent, html.EscapeString(current))
	back := domain.Button{Text: panelBack, Data: dataGroup(groupIndex(spec.Group))}
	if len(pack) == 0 {
		b.WriteString("\n\n" + panelPackUnavailable)
		return view{b.String(), []domain.Button{back}}
	}
	perPage := iconGridCols * iconGridRows
	pages := (len(pack) + perPage - 1) / perPage
	if page >= pages {
		page = pages - 1
	}
	start := page * perPage
	end := start + perPage
	if end > len(pack) {
		end = len(pack)
	}
	buttons := make([]domain.Button, 0, end-start+4)
	for i := start; i < end; i++ {
		label := pack[i]
		if sameEmoji(label, current) {
			label = "[" + label + "]"
		}
		buttons = append(buttons, domain.Button{Text: label, Data: dataPick(spec.Key, i), Row: 1 + (i-start)/iconGridCols})
	}
	if pages > 1 {
		nav := iconGridRows + 1
		prev := domain.Button{Text: "·", Data: dataNoop(), Row: nav}
		if page > 0 {
			prev = domain.Button{Text: "‹", Data: dataGrid(spec.Key, page-1), Row: nav}
		}
		next := domain.Button{Text: "·", Data: dataNoop(), Row: nav}
		if page < pages-1 {
			next = domain.Button{Text: "›", Data: dataGrid(spec.Key, page+1), Row: nav}
		}
		buttons = append(buttons, prev, domain.Button{Text: fmt.Sprintf("%d/%d", page+1, pages), Data: dataNoop(), Row: nav}, next)
	}
	return view{b.String(), append(buttons, back)}
}

// renderSummary is the text left behind when the panel is closed.
func renderSummary(opts domain.Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>", panelTitle)
	for _, g := range domain.OptionGroups() {
		fmt.Fprintf(&b, "\n\n<b>%s</b>", html.EscapeString(g.Title))
		for _, spec := range domain.OptionsInGroup(g.Name) {
			fmt.Fprintf(&b, "\n%s: %s", html.EscapeString(spec.Title), html.EscapeString(displayValue(spec, opts.String(spec.Key))))
		}
	}
	return b.String()
}

func displayValue(spec domain.OptionSpec, value string) string {
	if spec.Kind != domain.KindBool {
		return value
	}
	if value == "true" {
		return panelOn
	}
	return panelOff
}

func sameEmoji(a, b string) bool {
	return strings.ReplaceAll(a, "️", "") == strings.ReplaceAll(b, "️", "")
}
