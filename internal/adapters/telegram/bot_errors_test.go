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

func TestErrorsHandlerConflictStaysFatal(t *testing.T) {
	log, buf := newHandlerLog()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	calls := 0
	h := errorsHandler("tok", log, func() { calls++ }, func() time.Time { return now })

	h(errors.New("transient")) // a prior warning must not mute the fatal path
	h(fmt.Errorf("getUpdates: %w", bot.ErrorConflict))
	if calls != 1 {
		t.Fatalf("fatal calls = %d, want 1", calls)
	}
	errs := buf.lines("ERROR")
	if len(errs) != 1 || !strings.Contains(errs[0], "another poller owns this bot") {
		t.Errorf("error lines = %v", errs)
	}
}
