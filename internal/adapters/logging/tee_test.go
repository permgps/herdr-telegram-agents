package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestTee(t *testing.T) {
	var a, b bytes.Buffer
	la := slog.New(slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo}))
	lb := slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))
	log := Tee(la, nil, lb).With(slog.String("component", "setup"))
	log.Debug("quiet")
	log.Info("hello", slog.Int("n", 1))
	if strings.Contains(a.String(), "quiet") || !strings.Contains(a.String(), "hello") || !strings.Contains(a.String(), "component=setup") {
		t.Fatalf("text = %q", a.String())
	}
	if !strings.Contains(b.String(), "quiet") || !strings.Contains(b.String(), `"n":1`) {
		t.Fatalf("json = %q", b.String())
	}
	if !log.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be enabled through the json handler")
	}
	if Tee(nil, nil).Enabled(context.Background(), slog.LevelError) {
		t.Fatal("empty tee should discard")
	}
	if Tee(la).Handler() != la.Handler() {
		t.Fatal("single tee should return the handler as is")
	}
}
