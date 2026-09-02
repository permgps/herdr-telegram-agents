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
			name:  "workspace label and custom name",
			agent: domain.Agent{Name: "reviewer", Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs", TabID: "w1:t1", TabLabel: "claude"},
			want:  "V3Jobs · reviewer",
		},
		{
			name:  "tab label when no name",
			agent: domain.Agent{Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs", TabLabel: "1"},
			want:  "V3Jobs · 1",
		},
		{
			name:  "kind when no name or tab label",
			agent: domain.Agent{Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs"},
			want:  "V3Jobs · claude",
		},
		{
			name:  "workspace id when label unknown",
			agent: domain.Agent{Kind: "codex", WorkspaceID: "ws1"},
			want:  "ws1 · codex",
		},
		{
			name:  "title is never used",
			agent: domain.Agent{Title: "Подтверждение", Kind: "claude", WorkspaceID: "wE", WorkspaceLabel: "herdr_tg", TabLabel: "1"},
			want:  "herdr_tg · 1",
		},
		{
			name:  "bare name without workspace",
			agent: domain.Agent{Name: "reviewer", Kind: "claude"},
			want:  "reviewer",
		},
		{
			name:  "bare workspace",
			agent: domain.Agent{WorkspaceLabel: "V3Jobs"},
			want:  "V3Jobs",
		},
		{
			name:  "empty everything",
			agent: domain.Agent{},
			want:  "",
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
