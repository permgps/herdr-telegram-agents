package domain_test

import (
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestKeyString(t *testing.T) {
	k := domain.Key{PaneID: "p1", TerminalID: "t9"}
	if got := k.String(); got != "p1/t9" {
		t.Fatalf("String() = %q, want %q", got, "p1/t9")
	}
}

func TestAgentLabel(t *testing.T) {
	tests := []struct {
		name  string
		agent domain.Agent
		want  string
	}{
		{
			name:  "name wins over title",
			agent: domain.Agent{Name: "reviewer", Title: "claude — fix tests", Kind: "claude", WorkspaceID: "ws1"},
			want:  "reviewer",
		},
		{
			name:  "title when name empty",
			agent: domain.Agent{Title: "claude — fix tests", Kind: "claude", WorkspaceID: "ws1"},
			want:  "claude — fix tests",
		},
		{
			name:  "kind at workspace as last resort",
			agent: domain.Agent{Kind: "codex", WorkspaceID: "ws1"},
			want:  "codex@ws1",
		},
		{
			name:  "empty everything still yields a separator",
			agent: domain.Agent{},
			want:  "@",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.agent.Label(); got != tt.want {
				t.Fatalf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}
