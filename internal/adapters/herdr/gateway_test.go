package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func pingHandler(id string, params json.RawMessage) (any, *testkit.APIError) {
	return map[string]any{"type": "pong", "version": "0.7.5", "protocol": 17}, nil
}

// ackHandler records nothing and replies with an empty result.
func ackHandler(id string, params json.RawMessage) (any, *testkit.APIError) {
	return map[string]any{"type": "ack"}, nil
}

func newGateway(t *testing.T, s *testkit.NDJSONServer) *Gateway {
	t.Helper()
	s.Handle("ping", pingHandler)
	g := NewGateway(s.Path(), testLogger(t), fastBackoff)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	if got := s.WaitRequests("events.subscribe", 1, 2*time.Second); len(got) < 1 {
		t.Fatal("stream did not subscribe")
	}
	return g
}

// lastParams decodes the params of the most recent request for method.
func lastParams(t *testing.T, s *testkit.NDJSONServer, method string) map[string]any {
	t.Helper()
	reqs := s.WaitRequests(method, 1, 2*time.Second)
	if len(reqs) == 0 {
		t.Fatalf("no %s request", method)
	}
	var got map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].Params, &got); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	return got
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestGatewayPing(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", pingHandler)
	g := NewGateway(s.Path(), testLogger(t), fastBackoff)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := g.Ping(ctx)
	if err != nil || info.Version != "0.7.5" || info.Protocol != 17 {
		t.Fatalf("Ping = %+v, %v", info, err)
	}
	if got := s.WaitRequests("events.subscribe", 1, 50*time.Millisecond); len(got) != 0 {
		t.Fatal("Ping must not start the stream")
	}
	dead := NewGateway("/nonexistent/herdr.sock", testLogger(t), fastBackoff)
	if _, err := dead.Ping(ctx); err == nil {
		t.Fatal("Ping on a missing socket should fail")
	}
}

func TestGatewayStartFailsWithoutServer(t *testing.T) {
	g := NewGateway("/nonexistent/herdr.sock", testLogger(t), fastBackoff)
	if err := g.Start(ctxT(t)); err == nil {
		t.Fatal("Start succeeded without a server")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close after failed Start: %v", err)
	}
}

func TestGatewayListAgents(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return json.RawMessage(agentListSample), nil
	})
	s.Handle("workspace.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "workspace_list", "workspaces": []map[string]any{
			{"workspace_id": "w3", "label": " V3Jobs ", "number": 1}, {"workspace_id": "w9", "label": "Other"},
		}}, nil
	})
	s.Handle("tab.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "tab_list", "tabs": []map[string]any{
			{"tab_id": "w3:t2", "workspace_id": "w3", "label": "claude"}, {"tab_id": "w3:tA", "workspace_id": "w3", "label": "term"},
		}}, nil
	})
	g := newGateway(t, s)

	agents, err := g.ListAgents(ctxT(t))
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("no agents decoded")
	}
	for _, m := range []string{"agent.list", "workspace.list", "tab.list"} {
		if got := lastParams(t, s, m); len(got) != 0 {
			t.Fatalf("%s params = %v, want {}", m, got)
		}
	}
	a := agents[0]
	if a.PaneID == "" || a.Status == "" {
		t.Fatalf("agent not translated: %+v", a)
	}
	if a.WorkspaceLabel != "V3Jobs" || a.TabLabel != "claude" || a.Label() != "V3Jobs · claude" {
		t.Fatalf("labels not resolved: %+v", a)
	}

	// A failed label lookup fails the listing instead of yielding bare ids.
	s.Handle("tab.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "internal", Message: "boom"}
	})
	if _, err := g.ListAgents(ctxT(t)); err == nil {
		t.Fatal("ListAgents should fail when tab.list fails")
	}

	// No agents: the label calls are skipped.
	s.Handle("agent.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "agent_list", "agents": []any{}}, nil
	})
	before := len(s.Requests())
	if agents, err := g.ListAgents(ctxT(t)); err != nil || len(agents) != 0 {
		t.Fatalf("empty ListAgents = %v, %v", agents, err)
	}
	if n := len(s.Requests()) - before; n != 1 {
		t.Fatalf("requests for an empty list = %d, want 1", n)
	}
}

func TestGatewayReadScreen(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.read", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{
			"type": "pane_read",
			"read": map[string]any{
				"pane_id": "w3:p2", "source": "visible", "format": "text",
				"text": "hello\nworld", "revision": 42, "truncated": true,
			},
		}, nil
	})
	g := newGateway(t, s)

	scr, err := g.ReadScreen(ctxT(t), "w3:p2", domain.ScreenVisible, 50)
	if err != nil {
		t.Fatalf("ReadScreen: %v", err)
	}
	want := domain.Screen{Text: "hello\nworld", Revision: 42, Truncated: true}
	if scr != want {
		t.Fatalf("screen = %+v, want %+v", scr, want)
	}
	wantParams := map[string]any{
		"target": "w3:p2", "source": "visible", "lines": float64(50),
		"format": "text", "strip_ansi": true,
	}
	if got := lastParams(t, s, "agent.read"); !reflect.DeepEqual(got, wantParams) {
		t.Fatalf("params = %v, want %v", got, wantParams)
	}
}

func TestGatewayReadScreenOmitsZeroLines(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.read", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "pane_read", "read": map[string]any{"text": ""}}, nil
	})
	g := newGateway(t, s)
	if _, err := g.ReadScreen(ctxT(t), "w3:p2", domain.ScreenRecent, 0); err != nil {
		t.Fatalf("ReadScreen: %v", err)
	}
	if got := lastParams(t, s, "agent.read"); got["lines"] != nil {
		t.Fatalf("lines should be omitted, params = %v", got)
	}
}

func TestGatewayPromptAndSendKeys(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.prompt", ackHandler)
	s.Handle("agent.send_keys", ackHandler)
	g := newGateway(t, s)

	if err := g.Prompt(ctxT(t), "reviewer", "fix the tests"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	want := map[string]any{"target": "reviewer", "text": "fix the tests"}
	if got := lastParams(t, s, "agent.prompt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt params = %v, want %v", got, want)
	}

	if err := g.SendKeys(ctxT(t), "w1:p1", []string{"y", "enter"}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	want = map[string]any{"target": "w1:p1", "keys": []any{"y", "enter"}}
	if got := lastParams(t, s, "agent.send_keys"); !reflect.DeepEqual(got, want) {
		t.Fatalf("send_keys params = %v, want %v", got, want)
	}
}

func TestGatewayFocus(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.focus", ackHandler)
	g := newGateway(t, s)

	if err := g.Focus(ctxT(t), "w1:p1"); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	want := map[string]any{"target": "w1:p1"}
	if got := lastParams(t, s, "agent.focus"); !reflect.DeepEqual(got, want) {
		t.Fatalf("focus params = %v, want %v", got, want)
	}
}

func TestGatewayFocusNotFoundIsAgentGone(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.focus", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "not_found", Message: "no such pane"}
	})
	g := newGateway(t, s)
	if err := g.Focus(ctxT(t), "w9:p9"); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("err = %v, want ErrAgentGone", err)
	}
}

func TestGatewayRenameTab(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("tab.rename", ackHandler)
	g := newGateway(t, s)
	if err := g.RenameTab(ctxT(t), "w1:t2", "review"); err != nil {
		t.Fatalf("RenameTab: %v", err)
	}
	if got := lastParams(t, s, "tab.rename"); got["tab_id"] != "w1:t2" || got["label"] != "review" {
		t.Fatalf("tab.rename params = %v", got)
	}
}

func TestGatewayRename(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.rename", ackHandler)
	g := newGateway(t, s)

	name := "reviewer"
	if err := g.Rename(ctxT(t), "w1:p1", &name); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := lastParams(t, s, "agent.rename"); got["name"] != "reviewer" {
		t.Fatalf("rename params = %v", got)
	}

	if err := g.Rename(ctxT(t), "w1:p1", nil); err != nil {
		t.Fatalf("Rename(nil): %v", err)
	}
	reqs := s.WaitRequests("agent.rename", 2, 2*time.Second)
	if len(reqs) != 2 {
		t.Fatalf("rename requests = %d", len(reqs))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(reqs[1].Params, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["name"]) != "null" {
		t.Fatalf("name = %s, want null (raw params %s)", raw["name"], reqs[1].Params)
	}
}

func TestGatewayNotify(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("notification.show", ackHandler)
	g := newGateway(t, s)

	if err := g.Notify(ctxT(t), "Telegram Agents", "daemon started", domain.NotifySoundNone); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	want := map[string]any{"title": "Telegram Agents", "body": "daemon started", "sound": "none"}
	if got := lastParams(t, s, "notification.show"); !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	if err := g.Notify(ctxT(t), "done", "", domain.NotifySoundDefault); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	reqs := s.WaitRequests("notification.show", 2, 2*time.Second)
	var got map[string]any
	_ = json.Unmarshal(reqs[1].Params, &got)
	want = map[string]any{"title": "done", "sound": "done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
}

func TestGatewayNotFoundIsAgentGone(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.prompt", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "not_found", Message: "no agent w9:p9"}
	})
	s.Handle("agent.read", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "invalid_params", Message: "bad source"}
	})
	g := newGateway(t, s)

	err := g.Prompt(ctxT(t), "w9:p9", "hi")
	if !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("err = %v, want ErrAgentGone", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("not_found should not surface as APIError: %v", err)
	}

	_, err = g.ReadScreen(ctxT(t), "w1:p1", domain.ScreenSource("bogus"), 0)
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_params" {
		t.Fatalf("err = %v, want APIError invalid_params", err)
	}
	if errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("other codes must not map to ErrAgentGone: %v", err)
	}
}

func TestGatewayRetriesDialOnce(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.prompt", ackHandler)
	g := newGateway(t, s)

	var fails atomic.Int32
	fails.Store(1)
	g.dial = func(ctx context.Context, path string) (net.Conn, error) {
		if fails.Add(-1) >= 0 {
			return nil, errors.New("socket busy")
		}
		return dial(ctx, path)
	}
	if err := g.Prompt(ctxT(t), "a", "one"); err != nil {
		t.Fatalf("prompt after transient dial failure: %v", err)
	}
	if got := s.WaitRequests("agent.prompt", 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("prompt requests = %d, want exactly 1", len(got))
	}
}

func TestGatewayGivesUpWhenServerGone(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.prompt", ackHandler)
	g := newGateway(t, s)
	s.Close()

	err := g.Prompt(ctxT(t), "a", "x")
	if !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("err = %v, want ErrDisconnected", err)
	}
}

func TestGatewayEachCallUsesOwnConnection(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.prompt", ackHandler)
	g := newGateway(t, s)
	for i := 0; i < 3; i++ {
		if err := g.Prompt(ctxT(t), "a", "x"); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}
	reqs := s.WaitRequests("agent.prompt", 3, 2*time.Second)
	seen := map[int]bool{}
	for _, r := range reqs {
		seen[r.Conn] = true
	}
	if len(seen) != 3 {
		t.Fatalf("prompts spread over %d connections, want 3: %+v", len(seen), reqs)
	}
}

func TestGatewayEventsAndWatchPanes(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	g := newGateway(t, s)

	s.Push("pane_closed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"})
	ev := next(t, g.Events())
	if ev.Kind != domain.PaneClosed || ev.PaneID != "w1:p1" {
		t.Fatalf("event = %+v", ev)
	}

	if err := g.WatchPanes(ctxT(t), []string{"w2:p2", "w1:p1"}); err != nil {
		t.Fatalf("WatchPanes: %v", err)
	}
	reqs := s.WaitRequests("events.subscribe", 2, 2*time.Second)
	if len(reqs) != 2 {
		t.Fatalf("subscribe requests = %d, want 2", len(reqs))
	}
	var panes []string
	for _, sub := range subscribedTypes(t, reqs[1]) {
		if sub.Type == "pane.agent_status_changed" {
			panes = append(panes, sub.PaneID)
		}
	}
	if want := []string{"w1:p1", "w2:p2"}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("watched panes = %v, want %v", panes, want)
	}

	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := <-g.Events(); ok {
		t.Fatal("events channel still open after Close")
	}
}

func TestGatewayListWorkspaces(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("workspace.list", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "workspace_list", "workspaces": []map[string]any{
			{"workspace_id": "w1", "label": " Work ", "number": 1, "focused": true},
			{"workspace_id": "w2", "label": "Web", "number": 2},
		}}, nil
	})
	g := newGateway(t, s)
	got, err := g.ListWorkspaces(ctxT(t))
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	want := []domain.Workspace{{ID: "w1", Label: "Work"}, {ID: "w2", Label: "Web"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListWorkspaces = %+v, want %+v", got, want)
	}
	if p := lastParams(t, s, "workspace.list"); len(p) != 0 {
		t.Fatalf("workspace.list params = %v, want {}", p)
	}
}

func TestGatewayCreateTab(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("tab.create", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{
			"type":      "tab_created",
			"tab":       map[string]any{"tab_id": "w1:t7", "workspace_id": "w1", "label": " Tab 7 "},
			"root_pane": map[string]any{"pane_id": "w1:p7", "workspace_id": "w1", "tab_id": "w1:t7"},
		}, nil
	})
	g := newGateway(t, s)
	tab, err := g.CreateTab(ctxT(t), "w1")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	want := domain.Tab{ID: "w1:t7", WorkspaceID: "w1", Label: "Tab 7", RootPaneID: "w1:p7"}
	if tab != want {
		t.Fatalf("CreateTab = %+v, want %+v", tab, want)
	}
	got := lastParams(t, s, "tab.create")
	if got["workspace_id"] != "w1" || got["focus"] != false || len(got) != 2 {
		t.Fatalf("tab.create params = %v, want {workspace_id: w1, focus: false}", got)
	}
}

func TestGatewayStartAgent(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.start", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{
			"type": "agent_started",
			"agent": map[string]any{
				"pane_id": "w1:p7", "workspace_id": "w1", "tab_id": "w1:t7", "terminal_id": "term-7",
				"agent": "codex", "agent_status": "working", "name": "codex-2", "state_change_seq": 1,
			},
			"argv": []string{"codex"},
		}, nil
	})
	g := newGateway(t, s)
	agent, err := g.StartAgent(ctxT(t), "codex-2", "codex", "w1:p7", time.Second)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if agent.PaneID != "w1:p7" || agent.TerminalID != "term-7" || agent.TabID != "w1:t7" || agent.Kind != "codex" || agent.Name != "codex-2" || agent.Status != domain.StatusWorking {
		t.Fatalf("StartAgent = %+v", agent)
	}
	got := lastParams(t, s, "agent.start")
	if got["name"] != "codex-2" || got["kind"] != "codex" || got["pane_id"] != "w1:p7" || got["timeout_ms"] != float64(3001) {
		t.Fatalf("agent.start params = %v", got)
	}
	// The upper bound is clamped too.
	if _, err := g.StartAgent(ctxT(t), "codex", "codex", "w1:p7", time.Hour); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if reqs := s.WaitRequests("agent.start", 2, 2*time.Second); len(reqs) != 2 {
		t.Fatalf("agent.start requests = %d", len(reqs))
	}
	if got := lastParams(t, s, "agent.start"); got["timeout_ms"] != float64(300000) {
		t.Fatalf("clamped timeout_ms = %v", got["timeout_ms"])
	}
}

func TestGatewayStartAgentErrorIsTagged(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.start", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "timeout", Message: "agent not detected"}
	})
	g := newGateway(t, s)
	_, err := g.StartAgent(ctxT(t), "claude", "claude", "w1:p7", 30*time.Second)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "timeout" {
		t.Fatalf("err = %v, want APIError timeout", err)
	}
}

func TestGatewayClosePane(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("pane.close", ackHandler)
	g := newGateway(t, s)
	if err := g.ClosePane(ctxT(t), "w1:p7"); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if got := lastParams(t, s, "pane.close"); got["pane_id"] != "w1:p7" || len(got) != 1 {
		t.Fatalf("pane.close params = %v", got)
	}
}

func TestGatewayClosePaneNotFoundIsAgentGone(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("pane.close", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "not_found", Message: "no such pane"}
	})
	g := newGateway(t, s)
	if err := g.ClosePane(ctxT(t), "w9:p9"); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("err = %v, want ErrAgentGone", err)
	}
}
