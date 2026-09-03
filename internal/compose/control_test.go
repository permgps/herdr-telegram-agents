package compose

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// TestStartControlRoundTrip covers the seam internal/cli uses: the daemon
// starts the channel with its handlers, an action reaches them, and the
// stop function closes the channel again.
func TestStartControlRoundTrip(t *testing.T) {
	env := PluginEnv{StateDir: testkit.ShortTempDir(t)}
	var mu sync.Mutex
	stops, resyncs := 0, 0
	stop, err := StartControl(context.Background(), env, ControlHandlers{
		Stop: func() {
			mu.Lock()
			stops++
			mu.Unlock()
		},
		Resync: func() {
			mu.Lock()
			resyncs++
			mu.Unlock()
		},
		Status: func() string { return "version=test pid=1 uptime=0s agents=0 dropped=0 herdr=ok" },
	}, nil)
	if err != nil {
		t.Fatalf("StartControl: %v", err)
	}

	ctx := context.Background()
	if _, err := system.SendControl(ctx, env.StateDir, system.ControlStop); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := system.SendControl(ctx, env.StateDir, system.ControlResync); err != nil {
		t.Fatalf("resync: %v", err)
	}
	line, err := system.SendControl(ctx, env.StateDir, system.ControlStatus)
	if err != nil || line != "version=test pid=1 uptime=0s agents=0 dropped=0 herdr=ok" {
		t.Fatalf("status: line = %q, err = %v", line, err)
	}
	mu.Lock()
	gotStops, gotResyncs := stops, resyncs
	mu.Unlock()
	if gotStops != 1 || gotResyncs != 1 {
		t.Fatalf("handlers called stop=%d resync=%d", gotStops, gotResyncs)
	}

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return")
	}
	if _, err := system.SendControl(ctx, env.StateDir, system.ControlStatus); !errors.Is(err, domain.ErrControlUnavailable) {
		t.Fatalf("after stop: err = %v, want ErrControlUnavailable", err)
	}
}

func TestStatsLinePassesThrough(t *testing.T) {
	now := time.Now()
	s := Stats{Version: "v0.1.0", PID: 7, Since: now.Add(-time.Minute), Agents: 2, HerdrOK: true}
	if got, want := StatsLine(s, now), "version=v0.1.0 pid=7 uptime=1m0s agents=2 dropped=0 herdr=ok"; got != want {
		t.Fatalf("StatsLine = %q, want %q", got, want)
	}
}
