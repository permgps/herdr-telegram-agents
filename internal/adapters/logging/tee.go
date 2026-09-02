package logging

import (
	"context"
	"log/slog"
)

// teeHandler fans one record out to several handlers; a record is passed
// to every handler whose level admits it. Errors from later handlers do
// not stop earlier ones and the first error is returned.
type teeHandler struct {
	handlers []slog.Handler
}

// Tee returns a logger writing to every given logger's handler. Nil
// loggers are skipped; with fewer than two left the single one (or a
// discard logger) is returned as is.
func Tee(loggers ...*slog.Logger) *slog.Logger {
	var hs []slog.Handler
	for _, l := range loggers {
		if l != nil {
			hs = append(hs, l.Handler())
		}
	}
	switch len(hs) {
	case 0:
		return slog.New(slog.DiscardHandler)
	case 1:
		return slog.New(hs[0])
	}
	return slog.New(&teeHandler{handlers: hs})
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range t.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &teeHandler{handlers: out}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		out[i] = h.WithGroup(name)
	}
	return &teeHandler{handlers: out}
}
