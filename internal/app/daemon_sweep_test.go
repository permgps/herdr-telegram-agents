package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestDaemonContinuesStaleSweepBacklog(t *testing.T) {
	f := newDaemon(t)
	for n := 0; n < 51; n++ {
		a := agent(fmt.Sprintf("pane-%02d", n), "term", "agent", domain.StatusWorking)
		topic, err := f.tg.CreateTopic(context.Background(), a.Label(), a.Status)
		if err != nil {
			t.Fatal(err)
		}
		f.rec.Mapping().Link(a.Key, topic, a, t0)
		f.rec.Mapping().MarkExited(a.Key, t0)
		f.rec.Mapping().MarkClosed(a.Key, t0)
	}
	f.tg.Reset()
	f.clock.Advance(40 * 24 * time.Hour)
	f.start(t)
	deleted := func() int {
		count := 0
		for _, call := range f.tg.Calls() {
			if strings.HasPrefix(call, "delete:") {
				count++
			}
		}
		return count
	}
	waitFor(t, "first cleanup batch", func() bool { return deleted() == 50 })
	f.clock.Advance(time.Minute)
	waitFor(t, "cleanup continuation", func() bool { return deleted() == 51 })
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}
