package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

const devAgentList = `{"type":"agent_list","agents":[
 {"agent":"claude","agent_status":"idle","pane_id":"w3:p2","tab_id":"w3:t2","terminal_id":"term_1","terminal_title":"✳ Review","terminal_title_stripped":"Review","workspace_id":"w3","revision":1,"state_change_seq":1},
 {"agent":"codex","agent_status":"working","name":"builder","pane_id":"w1:p1","tab_id":"w1:t1","terminal_id":"term_2","workspace_id":"w1","revision":2,"state_change_seq":3}
]}`

// syncBuffer is a bytes.Buffer safe for a writer goroutine and a polling test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newDevServer(t *testing.T) *testkit.NDJSONServer {
	t.Helper()
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "pong", "version": "0.7.5", "protocol": 17}, nil
	})
	s.Handle("agent.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return json.RawMessage(devAgentList), nil
	})
	socketPathOverride = s.Path()
	t.Cleanup(func() { socketPathOverride = "" })
	return s
}

func TestDevRefusesOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"dev", "agents"}, "x", &stdout, &stderr); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(stderr.String(), "HERDR_ENV") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDevUsage(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	socketPathOverride = "/dev/null"
	t.Cleanup(func() { socketPathOverride = "" })
	for _, args := range [][]string{{"dev"}, {"dev", "bogus"}} {
		var stdout, stderr bytes.Buffer
		if got := Run(args, "x", &stdout, &stderr); got != exitUsage {
			t.Fatalf("Run(%v) = %d, want %d", args, got, exitUsage)
		}
	}
}

func TestDevAgents(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	newDevServer(t)
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"dev", "agents"}, "x", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr = %s", got, stderr.String())
	}
	want := "w3:p2 idle Review (claude, term term_1)\n" +
		"w1:p1 working builder (codex, term term_2)\n"
	if stdout.String() != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}
}

func TestDevAgentsConnectFailure(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	socketPathOverride = "/nonexistent/herdr.sock"
	t.Cleanup(func() { socketPathOverride = "" })
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"dev", "agents"}, "x", &stdout, &stderr); got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "connect /nonexistent/herdr.sock") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDevWatchPrintsEventsAndRewatches(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	s := newDevServer(t)
	var stdout syncBuffer
	var stderr syncBuffer
	rc := &runContext{version: "x", stdout: &stdout, stderr: &stderr, log: newLogger(&stderr)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- devWatch(ctx, rc, s.Path()) }()

	// The stream subscribes once at Start (global kinds only) and again once
	// WatchPanes adds both agent panes.
	reqs := s.WaitRequests("events.subscribe", 2, 2*time.Second)
	if len(reqs) < 2 {
		t.Fatalf("subscribe requests = %d, want 2", len(reqs))
	}
	if p := string(reqs[1].Params); !strings.Contains(p, `"w1:p1"`) || !strings.Contains(p, `"w3:p2"`) {
		t.Fatalf("subscribe params = %s", p)
	}

	s.Push("pane_agent_status_changed", map[string]any{
		"pane_id": "w1:p1", "workspace_id": "w1", "agent": "codex", "agent_status": "blocked", "title": "Question",
	})
	waitOutput(t, &stdout, "pane.agent_status_changed pane=w1:p1 ws=w1 agent=codex status=blocked title=Question")

	// A closed pane triggers agent.list again and a fresh subscription.
	s.Push("pane_closed", map[string]any{"pane_id": "w3:p2", "workspace_id": "w3"})
	waitOutput(t, &stdout, "pane.closed pane=w3:p2 ws=w3")
	if got := s.WaitRequests("agent.list", 2, 2*time.Second); len(got) < 2 {
		t.Fatalf("agent.list calls = %d, want 2", len(got))
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not stop after cancel")
	}
}

func waitOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output lacks %q:\n%s", want, out.String())
}

func TestFormatEvent(t *testing.T) {
	st := domain.StatusDone
	tests := []struct {
		name string
		ev   domain.Event
		want string
	}{
		{"detected", domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: "w1:p1", WorkspaceID: "w1",
			Agent: &domain.Agent{Kind: "claude", Status: domain.StatusUnknown}}, "pane.agent_detected pane=w1:p1 ws=w1 agent=claude status=unknown"},
		{"released", domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: "w1:p1", Released: true, FinalStatus: &st},
			"pane.agent_detected pane=w1:p1 released=true final=done"},
		{"tab renamed", domain.HerdrEvent{Kind: domain.TabRenamed, TabID: "w1:t1", Label: "api"}, "tab.renamed tab=w1:t1 label=api"},
		{"reset", domain.HerdrEvent{Kind: domain.StreamReset}, "stream.reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatEvent(tt.ev); got != tt.want {
				t.Fatalf("formatEvent = %q, want %q", got, tt.want)
			}
		})
	}
}
