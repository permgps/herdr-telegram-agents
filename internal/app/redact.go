package app

import (
	"context"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// redactingGateway wraps a TelegramGateway so every text that leaves the
// daemon (screen posts, documents, button labels, panel edits, notices)
// passes the domain.Redactor first. It is the single insertion point for
// the privacy.redact option: the five call sites that build posts never
// need to know about it. Everything else is passed through untouched.
type redactingGateway struct {
	domain.TelegramGateway
	red     *domain.Redactor
	enabled func() bool
	log     *slog.Logger
}

// newRedactingGateway returns tg wrapped; enabled is read on every call so
// a change of the option applies to the next post. A nil enabled means
// always on.
func newRedactingGateway(tg domain.TelegramGateway, red *domain.Redactor, enabled func() bool, log *slog.Logger) domain.TelegramGateway {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if enabled == nil {
		enabled = func() bool { return true }
	}
	if red == nil {
		red = domain.NewRedactor()
	}
	return &redactingGateway{TelegramGateway: tg, red: red, enabled: enabled, log: log}
}

func (g *redactingGateway) Send(ctx context.Context, out domain.Outgoing) (int, error) {
	if !g.enabled() {
		return g.TelegramGateway.Send(ctx, out)
	}
	stats := domain.RedactionStats{}
	out.Text = g.redact(out.Text, stats)
	out.Buttons = g.redactButtons(out.Buttons, stats)
	g.report(out.ThreadID, "send", stats)
	return g.TelegramGateway.Send(ctx, out)
}

func (g *redactingGateway) SendDocument(ctx context.Context, doc domain.Document) error {
	if !g.enabled() {
		return g.TelegramGateway.SendDocument(ctx, doc)
	}
	stats := domain.RedactionStats{}
	doc.Data = []byte(g.redact(string(doc.Data), stats))
	doc.Caption = g.redact(doc.Caption, stats)
	g.report(doc.ThreadID, "document", stats)
	return g.TelegramGateway.SendDocument(ctx, doc)
}

func (g *redactingGateway) EditText(ctx context.Context, messageID int, text string, html bool, buttons []domain.Button) error {
	if !g.enabled() {
		return g.TelegramGateway.EditText(ctx, messageID, text, html, buttons)
	}
	stats := domain.RedactionStats{}
	text = g.redact(text, stats)
	buttons = g.redactButtons(buttons, stats)
	g.report(0, "edittext", stats)
	return g.TelegramGateway.EditText(ctx, messageID, text, html, buttons)
}

func (g *redactingGateway) EditButtons(ctx context.Context, messageID int, buttons []domain.Button) error {
	if !g.enabled() {
		return g.TelegramGateway.EditButtons(ctx, messageID, buttons)
	}
	stats := domain.RedactionStats{}
	buttons = g.redactButtons(buttons, stats)
	g.report(0, "buttons", stats)
	return g.TelegramGateway.EditButtons(ctx, messageID, buttons)
}

func (g *redactingGateway) redact(text string, stats domain.RedactionStats) string {
	out, s := g.red.Redact(text)
	for k, n := range s {
		stats[k] += n
	}
	return out
}

// redactButtons copies the slice so the caller's keyboard (kept by
// outbound for later edits) is never rewritten in place.
func (g *redactingGateway) redactButtons(buttons []domain.Button, stats domain.RedactionStats) []domain.Button {
	if len(buttons) == 0 {
		return buttons
	}
	out := make([]domain.Button, len(buttons))
	copy(out, buttons)
	for i := range out {
		out[i].Text = g.redact(out[i].Text, stats)
	}
	return out
}

// report logs the kinds and counts of what was masked; the values never
// reach the log.
func (g *redactingGateway) report(threadID int, via string, stats domain.RedactionStats) {
	if stats.Total() == 0 {
		return
	}
	g.log.Info("secrets redacted", slog.Int("thread_id", threadID), slog.String("via", via), slog.String("kinds", stats.String()))
}
