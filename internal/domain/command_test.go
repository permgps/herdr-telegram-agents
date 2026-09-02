package domain_test

import (
	"reflect"
	"testing"

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
		{"upper case word", "/HELP", "herdr_bot", domain.Command{Kind: domain.CmdHelp}},
		{"bot suffix stripped", "/status@Herdr_Bot", "herdr_bot", domain.Command{Kind: domain.CmdStatus}},
		{"bot suffix with args", "/screen@herdr_bot 5", "herdr_bot", domain.Command{Kind: domain.CmdScreen, Lines: 5}},
		{"any suffix when username unknown", "/help@whatever", "", domain.Command{Kind: domain.CmdHelp}},
		{"other bot", "/help@other_bot", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/help@other_bot"}},
		{"unknown word", "/restart now", "herdr_bot", domain.Command{Kind: domain.CmdUnknown, Text: "/restart"}},
		{"leading whitespace before slash", "  /focus", "herdr_bot", domain.Command{Kind: domain.CmdFocus}},
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
