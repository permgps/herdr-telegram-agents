package domain_test

import (
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const botToken = "1234567890:" + "AAHf3kJd9sLq2mN8pR4tV6wX0yZ1bC3dE5f" // built from parts so secret scanners ignore it

func TestRedactPatterns(t *testing.T) {
	r := domain.NewRedactor(botToken)
	cases := []struct {
		name  string
		in    string
		want  string
		stats string
	}{
		{"openai", "key sk-" + "proj-abcdefghijklmnopqrstuvwxyz0123 ok", "key sk-…0123 ok", "openai=1"},
		{"anthropic", "sk-" + "ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz012345", "sk-…2345", "openai=1"},
		{"github classic", "ghp_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij", "ghp_…ghij", "github=1"},
		{"github fine", "github_pat_" + "11ABCDEFG0abcdefghijklmnopqrstuvwxyz", "github_pat_…wxyz", "github=1"},
		{"aws", "AKIA" + "IOSFODNN7EXAMPLE", "AKIA…MPLE", "aws=1"},
		{"slack", "xoxb-" + "123456789012-abcdefghijklmnop", "xoxb-…mnop", "slack=1"},
		{"gitlab", "glpat-" + "abcdefghijklmnopqrstuv", "glpat-…stuv", "gitlab=1"},
		{"google", "AIza" + "SyA1234567890abcdefghijklmnopqrstuv", "AIza…stuv", "google=1"},
		{"jwt", "eyJ" + "hbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abcdefghijklmnop", "eyJ…mnop", "jwt=1"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz", "Authorization: Bearer …wxyz", "bearer=1"},
		{"password", "password=hunter2secret", "password=[redacted]", "keyvalue=1"},
		{"api key colon", `API_KEY: "abcdefghijkl"`, `API_KEY: "[redacted]"`, "keyvalue=1"},
		{"telegram shape", "token 9876543210:" + "BBGf3kJd9sLq2mN8pR4tV6wX0yZ1bC3dE5g", "token [redacted]", "telegram=1"},
		{"bot token exact", "curl https://api.telegram.org/bot" + botToken + "/getMe", "curl https://api.telegram.org/bot[redacted]/getMe", "exact=1"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----\nrest", "[redacted]\nrest", "privatekey=1"},
		{"private key cut", "text\n-----BEGIN PRIVATE KEY-----\nMIIEow\nMIIB", "text\n[redacted]", "privatekey=1"},
		{"two kinds", "sk-abcdefghijklmnopqrstuvwx and Bearer 0123456789abcdefghij", "sk-…uvwx and Bearer …ghij", "bearer=1 openai=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stats := r.Redact(tc.in)
			if got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if stats.String() != tc.stats {
				t.Errorf("stats = %q, want %q", stats.String(), tc.stats)
			}
		})
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	r := domain.NewRedactor(botToken, "short")
	inputs := []string{
		"token: none",
		"password reset email sent",
		"sk- alone and sk-short",
		"12345:abc",
		"Bearer of bad news",
		"the ghost_of a key",
		"AKIA is a prefix",
		"eyJ.eyJ.eyJ",
		"plain terminal output with numbers 1234567890",
		"",
	}
	for _, in := range inputs {
		got, stats := r.Redact(in)
		if got != in || stats.Total() != 0 {
			t.Errorf("Redact(%q) = %q, stats %v; want unchanged", in, got, stats)
		}
	}
}

func TestRedactKeepsEndsAtMinimalLength(t *testing.T) {
	r := domain.NewRedactor()
	// The shortest slack body (10 characters) still leaves prefix, ellipsis
	// and a four-character tail.
	got, _ := r.Redact("xoxb-" + "abcdefghij")
	if got != "xoxb-…ghij" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	r := domain.NewRedactor(botToken)
	in := "sk-abcdefghijklmnopqrstuvwx Bearer 0123456789abcdefghij password=hunter2secret " + botToken
	once, stats := r.Redact(in)
	if stats.Total() != 4 {
		t.Fatalf("first pass stats %v", stats)
	}
	twice, again := r.Redact(once)
	if twice != once || again.Total() != 0 {
		t.Fatalf("second pass changed text: %q → %q, stats %v", once, twice, again)
	}
}

func TestRedactLargeInput(t *testing.T) {
	r := domain.NewRedactor(botToken)
	line := "some ordinary output line with a token sk-abcdefghijklmnopqrstuvwx in it\n"
	in := strings.Repeat(line, 256*1024/len(line))
	got, stats := r.Redact(in)
	if stats["openai"] == 0 || strings.Contains(got, "sk-abcdef") {
		t.Fatalf("large input not redacted: stats %v", stats)
	}
}

func TestRedactionStatsString(t *testing.T) {
	s := domain.RedactionStats{"openai": 2, "bearer": 1}
	if s.String() != "bearer=1 openai=2" || s.Total() != 3 {
		t.Fatalf("String() = %q, Total() = %d", s.String(), s.Total())
	}
	if (domain.RedactionStats{}).String() != "" {
		t.Fatal("empty stats should render empty")
	}
}
