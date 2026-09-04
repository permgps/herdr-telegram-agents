package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		bot  string
		want domain.Command
	}{
		{"plain text is a prompt", "fix the tests", "herdr_bot", domain.Command{Kind: domain.CmdPrompt, Text: "fix the tests"}},
		{"prompt keeps text as typed", "  run it  ", "herdr_bot", domain.Command{Kind: domain.CmdPrompt, Text: "  run it  "}},
		{"screen full", "/screen", "herdr_bot", domain.Command{Kind: domain.CmdScreen}},
		{"screen lines", "/screen 10", "herdr_bot", domain.Command{Kind: domain.CmdScreen, Lines: 10}},
		{"screen clamps high", "/screen 999", "herdr_bot", domain.Command{Kind: domain.CmdScreen, Lines: 200}},
		{"screen clamps low", "/screen 0", "herdr_bot", domain.Command{Kind: domain.CmdScreen, Lines: 1}},
		{"screen non-numeric", "/screen abc", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/screen abc"}},
		{"screen all", "/screen all", "herdr_bot", domain.Command{Kind: domain.CmdScreen, All: true}},
		{"screen all upper case", "/screen ALL", "herdr_bot", domain.Command{Kind: domain.CmdScreen, All: true}},
		{"screen all with extra arg", "/screen all 5", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/screen all 5"}},
		{"screen all with bot suffix", "/screen@herdr_bot all", "herdr_bot", domain.Command{Kind: domain.CmdScreen, All: true}},
		{"keys", "/keys esc enter", "herdr_bot", domain.Command{Kind: domain.CmdKeys, Keys: []string{"esc", "enter"}}},
		{"keys without list", "/keys", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/keys"}},
		{"focus", "/focus", "herdr_bot", domain.Command{Kind: domain.CmdFocus}},
		{"status", "/status", "herdr_bot", domain.Command{Kind: domain.CmdStatus}},
		{"help", "/help", "herdr_bot", domain.Command{Kind: domain.CmdHelp}},
		{"options", "/options", "herdr_bot", domain.Command{Kind: domain.CmdOptions}},
		{"options with suffix", "/Options@herdr_bot extra", "herdr_bot", domain.Command{Kind: domain.CmdOptions}},
		{"upper case word", "/HELP", "herdr_bot", domain.Command{Kind: domain.CmdHelp}},
		{"bot suffix stripped", "/status@Herdr_Bot", "herdr_bot", domain.Command{Kind: domain.CmdStatus}},
		{"bot suffix with args", "/screen@herdr_bot 5", "herdr_bot", domain.Command{Kind: domain.CmdScreen, Lines: 5}},
		{"any suffix when username unknown", "/help@whatever", "", domain.Command{Kind: domain.CmdHelp}},
		{"other bot", "/help@other_bot", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/help@other_bot"}},
		{"unknown word", "/restart now", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/restart"}},
		{"leading whitespace before slash", "  /focus", "herdr_bot", domain.Command{Kind: domain.CmdFocus}},
		{"forward clear", "/clear", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/clear", Forward: domain.ForwardRule{Post: domain.ForwardPostTail}}},
		{"forward clear upper case with bot suffix", "/CLEAR@herdr_bot", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/clear", Forward: domain.ForwardRule{Post: domain.ForwardPostTail}}},
		{"forward clear for another bot", "/clear@other_bot", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/clear@other_bot"}},
		{"forward compact bare", "/compact", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/compact", Forward: domain.ForwardRule{Post: domain.ForwardPostNone}}},
		{"forward compact keeps inner spacing", "/compact focus on the  API ", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/compact focus on the  API", Forward: domain.ForwardRule{Post: domain.ForwardPostNone}}},
		{"forward usage", "/usage", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/usage", Forward: domain.ForwardRule{Post: domain.ForwardPostScreen, Dismiss: true}}},
		{"forward model bare opens the picker", "/model", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/model", Forward: domain.ForwardRule{Post: domain.ForwardPostScreen, Dismiss: true}}},
		{"forward model with name", "/model sonnet", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/model sonnet", Forward: domain.ForwardRule{Post: domain.ForwardPostTail}}},
		{"forward model with suffix and name", "/model@herdr_bot opus", "herdr_bot", domain.Command{Kind: domain.CmdForward, Text: "/model opus", Forward: domain.ForwardRule{Post: domain.ForwardPostTail}}},
		{"near miss stays unknown", "/models", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/models"}},
		{"away until here", "/away", "herdr_bot", domain.Command{Kind: domain.CmdAway}},
		{"away hours", "/away 2h", "herdr_bot", domain.Command{Kind: domain.CmdAway, Away: 2 * time.Hour}},
		{"away minutes upper case", "/AWAY 30M", "herdr_bot", domain.Command{Kind: domain.CmdAway, Away: 30 * time.Minute}},
		{"away mixed", "/away@herdr_bot 1h30m", "herdr_bot", domain.Command{Kind: domain.CmdAway, Away: 90 * time.Minute}},
		{"away too short", "/away 30s", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/away 30s"}},
		{"away too long", "/away 200h", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/away 200h"}},
		{"away garbage", "/away tomorrow", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/away tomorrow"}},
		{"away two args", "/away 2 h", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/away 2 h"}},
		{"here", "/here", "herdr_bot", domain.Command{Kind: domain.CmdHere}},
		{"here with suffix", "/Here@herdr_bot", "herdr_bot", domain.Command{Kind: domain.CmdHere}},
		{"here with arg", "/here now", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/here now"}},
		{"stop", "/stop", "herdr_bot", domain.Command{Kind: domain.CmdStop}},
		{"stop with suffix", "/Stop@herdr_bot", "herdr_bot", domain.Command{Kind: domain.CmdStop}},
		{"stop with arg", "/stop now", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/stop now"}},
		{"interrupt", "/interrupt", "herdr_bot", domain.Command{Kind: domain.CmdInterrupt}},
		{"interrupt with arg", "/interrupt hard", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/interrupt hard"}},
		{"close", "/close", "herdr_bot", domain.Command{Kind: domain.CmdClose}},
		{"close with arg", "/close p1", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/close p1"}},
		{"new bare", "/new", "herdr_bot", domain.Command{Kind: domain.CmdNew}},
		{"new workspace with spaces", "/new My Project", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "My Project", AgentKind: "claude"}},
		{"new workspace and kind", "/new my project codex", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "my project", AgentKind: "codex"}},
		{"new kind upper case", "/new Work CODEX", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "Work", AgentKind: "codex"}},
		{"new kind only", "/new codex", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "", AgentKind: "codex"}},
		{"new with suffix", "/new@herdr_bot Work", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "Work", AgentKind: "claude"}},
		{"new collapses inner spacing", "/new  Big   Site  ", "herdr_bot", domain.Command{Kind: domain.CmdNew, Workspace: "Big Site", AgentKind: "claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.ParseCommand(tt.in, tt.bot)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseCommand(%q, %q) = %+v, want %+v", tt.in, tt.bot, got, tt.want)
			}
		})
	}
}

func TestShortReply(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"y", []string{"y"}, true},
		{"Y", []string{"y"}, true},
		{"yes", []string{"y"}, true},
		{"n", []string{"n"}, true},
		{"No", []string{"n"}, true},
		{"1", []string{"1"}, true},
		{"9", []string{"9"}, true},
		{"0", nil, false},
		{"10", nil, false},
		{"enter", []string{"enter"}, true},
		{"ok", []string{"enter"}, true},
		{"esc", []string{"esc"}, true},
		{"ESCAPE", []string{"esc"}, true},
		{" ok ", []string{"enter"}, true},
		{"okay", nil, false},
		{"yes please", nil, false},
		{"", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := domain.ShortReply(tt.in)
			if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ShortReply(%q) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRoute(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		status domain.Status
		want   domain.Command
	}{
		{"short reply while blocked", "y", domain.StatusBlocked, domain.Command{Kind: domain.CmdKeys, Keys: []string{"y"}}},
		{"digit while blocked", "2", domain.StatusBlocked, domain.Command{Kind: domain.CmdKeys, Keys: []string{"2"}}},
		{"short reply while idle is a prompt", "y", domain.StatusIdle, domain.Command{Kind: domain.CmdPrompt, Text: "y"}},
		{"short reply while working is a prompt", "ok", domain.StatusWorking, domain.Command{Kind: domain.CmdPrompt, Text: "ok"}},
		{"long text while blocked is a prompt", "yes, but keep the tests", domain.StatusBlocked, domain.Command{Kind: domain.CmdPrompt, Text: "yes, but keep the tests"}},
		{"slash while blocked", "/keys esc", domain.StatusBlocked, domain.Command{Kind: domain.CmdKeys, Keys: []string{"esc"}}},
		{"slash while idle", "/status", domain.StatusIdle, domain.Command{Kind: domain.CmdStatus}},
		{"forwarded command while blocked is not a key press", "/clear", domain.StatusBlocked, domain.Command{Kind: domain.CmdForward, Text: "/clear", Forward: domain.ForwardRule{Post: domain.ForwardPostTail}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.Route(tt.in, "herdr_bot", tt.status)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Route(%q, %s) = %+v, want %+v", tt.in, tt.status, got, tt.want)
			}
		})
	}
}

func TestForwardWords(t *testing.T) {
	want := []string{"clear", "compact", "model", "usage"}
	if got := domain.ForwardWords(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ForwardWords = %v, want %v", got, want)
	}
}

func TestCutOverlay(t *testing.T) {
	rule := "▔▔▔▔▔▔▔▔▔▔▔▔"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lines after the rule", "transcript\nmore\n" + rule + "\n   Settings  Usage\n   10% used", "   Settings  Usage\n   10% used"},
		{"last rule wins", rule + "\nold\n" + rule + "\nnew", "new"},
		{"indented rule", "a\n  " + rule + "  \nb", "b"},
		{"short rule ignored", "a\n▔▔▔\nb", "a\n▔▔▔\nb"},
		{"mixed line is not a rule", "a\n" + rule + "x\nb", "a\n" + rule + "x\nb"},
		{"no rule", "a\nb", "a\nb"},
		{"rule as last line", "a\n" + rule, ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.CutOverlay(tt.in); got != tt.want {
				t.Fatalf("CutOverlay(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsAgentKind(t *testing.T) {
	for _, k := range []string{"claude", "Claude", "CODEX", "pi", "maki"} {
		if !domain.IsAgentKind(k) {
			t.Errorf("IsAgentKind(%q) = false", k)
		}
	}
	for _, k := range []string{"", "claud", "claude-2", "Work", "gpt"} {
		if domain.IsAgentKind(k) {
			t.Errorf("IsAgentKind(%q) = true", k)
		}
	}
	if len(domain.AgentKinds) != 21 || domain.DefaultAgentKind != "claude" || !domain.IsAgentKind(domain.DefaultAgentKind) {
		t.Errorf("AgentKinds = %v, default %q", domain.AgentKinds, domain.DefaultAgentKind)
	}
}

func TestMatchWorkspace(t *testing.T) {
	list := []domain.Workspace{
		{ID: "w1", Label: "Work"},
		{ID: "w2", Label: "Web"},
		{ID: "w3", Label: "Workshop"},
		{ID: "w4", Label: " My Project "},
		{ID: "ws-5", Label: ""},
	}
	tests := []struct {
		name   string
		label  string
		wantID string
		want   domain.MatchResult
	}{
		{"exact beats prefix", "work", "w1", domain.MatchOne},
		{"exact upper case", "WORKSHOP", "w3", domain.MatchOne},
		{"unique prefix", "we", "w2", domain.MatchOne},
		{"trimmed label and text", "  my project", "w4", domain.MatchOne},
		{"unlabelled workspace by id", "WS-5", "ws-5", domain.MatchOne},
		{"ambiguous prefix", "w", "", domain.MatchMany},
		{"none", "zzz", "", domain.MatchNone},
		{"empty", "", "", domain.MatchNone},
		{"spaces only", "   ", "", domain.MatchNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, got := domain.MatchWorkspace(tt.label, list)
			if got != tt.want || ws.ID != tt.wantID {
				t.Fatalf("MatchWorkspace(%q) = %+v, %d; want %q, %d", tt.label, ws, got, tt.wantID, tt.want)
			}
		})
	}
	if got := domain.MatchWorkspaces("w", list); len(got) != 4 || got[0].ID != "w1" || got[1].ID != "w2" || got[2].ID != "w3" || got[3].ID != "ws-5" {
		t.Errorf("MatchWorkspaces(w) = %+v", got)
	}
	if got := domain.MatchWorkspaces("x", nil); got != nil {
		t.Errorf("MatchWorkspaces on nil = %+v", got)
	}
}
