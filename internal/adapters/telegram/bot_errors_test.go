package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// lockedBuffer is a log sink safe for the handler's concurrent use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines(level string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, "level="+level) {
			out = append(out, l)
		}
	}
	return out
}

func newHandlerLog() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func TestErrorsHandlerRateLimitsPollingWarnings(t *testing.T) {
	const token = "123:secret"
	log, buf := newHandlerLog()
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := start
	fatal := func() { t.Error("fatal called for a transient error") }
	h := errorsHandler(token, log, fatal, func() time.Time { return now })

	pollErr := fmt.Errorf("error do request: dial tcp: lookup api.telegram.org/bot%s: no such host", token)
	for i := range 100 {
		now = start.Add(time.Duration(i) * 5 * time.Second) // 100 errors within 9 minutes
		h(pollErr)
	}
	if got := buf.lines("WARN"); len(got) != 1 {
		t.Fatalf("warnings within 9 minutes = %d, want 1: %v", len(got), got)
	}
	if got := buf.lines("DEBUG"); len(got) != 99 {
		t.Errorf("debug lines = %d, want 99", len(got))
	}

	now = start.Add(pollWarnEvery)
	h(pollErr)
	warns := buf.lines("WARN")
	if len(warns) != 2 {
		t.Fatalf("warnings after 10 minutes = %d, want 2", len(warns))
	}
	if !strings.Contains(warns[1], "suppressed=99") || !strings.Contains(warns[1], "since_s=600") {
		t.Errorf("second warning = %q, want suppressed=99 since_s=600", warns[1])
	}
	if strings.Contains(warns[1], "secret") {
		t.Errorf("token leaked: %q", warns[1])
	}

	// The count restarts after a warning.
	now = now.Add(time.Second)
	h(pollErr)
	if got := buf.lines("DEBUG"); !strings.Contains(got[len(got)-1], "suppressed=1") {
		t.Errorf("debug after second warning = %q, want suppressed=1", got[len(got)-1])
	}
}

func conflictErr() error { return fmt.Errorf("getUpdates: %w", bot.ErrorConflict) }

func TestErrorsHandlerToleratesBriefConflict(t *testing.T) {
	log, buf := newHandlerLog()
	start := time.Date(2026, 11, 4, 11, 40, 0, 0, time.UTC)
	now := start
	h := errorsHandler("tok", log, func() { t.Error("fatal called for a brief conflict") }, func() time.Time { return now })

	// The self-collision after a network drop: a few 409s inside one hold.
	for _, d := range []time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond, 5 * time.Second} {
		now = start.Add(d)
		h(conflictErr())
	}
	if got := buf.lines("WARN"); len(got) != 1 || !strings.Contains(got[0], "getUpdates conflict, retrying") {
		t.Fatalf("warnings = %v, want one conflict warning", got)
	}

	// A later conflict after a quiet gap starts a new streak, so it is not
	// measured from the first one even though conflictGrace has passed.
	now = start.Add(conflictGrace + time.Minute)
	h(conflictErr())
	if got := buf.lines("WARN"); len(got) != 2 {
		t.Errorf("warnings after a new streak = %d, want 2", len(got))
	}
	if got := buf.lines("ERROR"); len(got) != 0 {
		t.Errorf("error lines = %v, want none", got)
	}
}

func TestErrorsHandlerPersistentConflictIsFatal(t *testing.T) {
	log, buf := newHandlerLog()
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := start
	calls := 0
	h := errorsHandler("tok", log, func() { calls++ }, func() time.Time { return now })

	h(errors.New("transient")) // a prior warning must not mute the fatal path
	// A competing poller: the library retries at most five seconds apart.
	for now = start; now.Sub(start) < conflictGrace; now = now.Add(5 * time.Second) {
		h(conflictErr())
	}
	if calls != 0 {
		t.Fatalf("fatal calls before conflictGrace = %d, want 0", calls)
	}
	h(conflictErr())
	if calls != 1 {
		t.Fatalf("fatal calls = %d, want 1", calls)
	}
	errs := buf.lines("ERROR")
	if len(errs) != 1 || !strings.Contains(errs[0], "another poller owns this bot") || !strings.Contains(errs[0], "since_s=100") {
		t.Errorf("error lines = %v", errs)
	}
}
