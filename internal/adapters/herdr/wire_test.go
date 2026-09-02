package herdr

import (
	"encoding/json"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Verified output of `herdr agent list` on Herdr 0.7.5 (protocol 17).
const agentListSample = `{
  "type": "agent_list",
  "agents": [{
    "agent": "claude",
    "agent_status": "idle",
    "cwd": "/Users/alex/Projects/Work/v3laravel",
    "focused": false,
    "foreground_cwd": "/Users/alex/Projects/Work/v3laravel",
    "pane_id": "w3:p2",
    "revision": 172,
    "state_change_seq": 9,
    "tab_id": "w3:t2",
    "terminal_id": "term_65a77760e3b0a1",
    "terminal_title": "✳ Объяснение первого пункта",
    "terminal_title_stripped": "Объяснение первого пункта",
    "workspace_id": "w3"
  }]
}`

func TestDecodeAgentListSample(t *testing.T) {
	var res agentListResult
	if err := json.Unmarshal([]byte(agentListSample), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Type != "agent_list" || len(res.Agents) != 1 {
		t.Fatalf("decoded %+v", res)
	}
	a := toDomainAgent(res.Agents[0])
	want := domain.Agent{
		Key:            domain.Key{PaneID: "w3:p2", TerminalID: "term_65a77760e3b0a1"},
		WorkspaceID:    "w3",
		TabID:          "w3:t2",
		Kind:           "claude",
		Title:          "Объяснение первого пункта",
		Status:         domain.StatusIdle,
		Revision:       172,
		StateChangeSeq: 9,
	}
	if a != want {
		t.Fatalf("toDomainAgent =\n %+v\nwant\n %+v", a, want)
	}
	if a.Label() != "w3 · claude" {
		t.Fatalf("Label() = %q", a.Label())
	}
}

func TestToDomainAgentLabelPriority(t *testing.T) {
	name := "  reviewer "
	tests := []struct {
		name string
		in   agentInfo
		want string
	}{
		{"custom name wins", agentInfo{Name: &name, Title: "meta", TerminalTitle: "✳ term", Agent: "claude", WorkspaceID: "w1"}, "w1 · reviewer"},
		{"titles are ignored", agentInfo{Title: "meta", TerminalTitle: "✳ term", TerminalTitleStripped: "term", Agent: "claude", WorkspaceID: "w1"}, "w1 · claude"},
		{"kind at workspace", agentInfo{Agent: "codex", WorkspaceID: "w7"}, "w7 · codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toDomainAgent(tt.in).Label(); got != tt.want {
				t.Fatalf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"✳ Объяснение", "Объяснение"},
		{"⠋ working", "working"},
		{"⚙️ gear", "gear"},
		{"✳✳  double", "double"},
		{"  plain  ", "plain"},
		{"", ""},
		{"✳", ""},
		{"fix ✳ inside", "fix ✳ inside"},
	}
	for _, tt := range tests {
		if got := cleanTitle(tt.in); got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseStatusFromWire(t *testing.T) {
	var a agentInfo
	if err := json.Unmarshal([]byte(`{"agent_status":"blocked","name":null}`), &a); err != nil {
		t.Fatal(err)
	}
	if got := toDomainAgent(a).Status; got != domain.StatusBlocked {
		t.Fatalf("status = %q", got)
	}
	if a.Name != nil {
		t.Fatalf("null name decoded as %q", *a.Name)
	}
}

func TestSubscriptionOmitsEmptyPaneID(t *testing.T) {
	b, err := json.Marshal(subscribeParams{Subscriptions: []subscription{
		{Type: "pane.closed"},
		{Type: "pane.agent_status_changed", PaneID: "w1:p1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"subscriptions":[{"type":"pane.closed"},{"type":"pane.agent_status_changed","pane_id":"w1:p1"}]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}
