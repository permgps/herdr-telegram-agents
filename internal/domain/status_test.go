package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		in   string
		want domain.Status
	}{
		{"working", domain.StatusWorking},
		{"idle", domain.StatusIdle},
		{"blocked", domain.StatusBlocked},
		{"done", domain.StatusDone},
		{"unknown", domain.StatusUnknown},
		{"exited", domain.StatusUnknown},
		{"", domain.StatusUnknown},
		{"WORKING", domain.StatusUnknown},
		{"garbage", domain.StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := domain.ParseStatus(tt.in); got != tt.want {
				t.Fatalf("ParseStatus(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStatusLive(t *testing.T) {
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown,
	} {
		if !st.Live() {
			t.Errorf("%s.Live() = false, want true", st)
		}
	}
	if domain.StatusExited.Live() {
		t.Errorf("exited.Live() = true, want false")
	}
}

func TestStatusReadyForInput(t *testing.T) {
	tests := []struct {
		st   domain.Status
		want bool
	}{
		{domain.StatusWorking, false},
		{domain.StatusIdle, true},
		{domain.StatusBlocked, false},
		{domain.StatusDone, true},
		{domain.StatusUnknown, false},
		{domain.StatusExited, false},
	}
	for _, tt := range tests {
		if got := tt.st.ReadyForInput(); got != tt.want {
			t.Errorf("%s.ReadyForInput() = %v, want %v", tt.st, got, tt.want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	t.Run("short label is untouched", func(t *testing.T) {
		if got := domain.DisplayName("V3Jobs · claude"); got != "V3Jobs · claude" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("clamp counts runes", func(t *testing.T) {
		label := strings.Repeat("ж", 200)
		got := domain.DisplayName(label)
		if n := utf8.RuneCountInString(got); n != 128 {
			t.Fatalf("rune count = %d, want 128", n)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("clamp produced invalid UTF-8")
		}
	})
	t.Run("exactly at limit is untouched", func(t *testing.T) {
		label := strings.Repeat("a", 128)
		if got := domain.DisplayName(label); got != label {
			t.Fatalf("name changed: %q", got)
		}
	})
}

func TestStripPrefix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"⚙️ reviewer", "reviewer"},
		{"💤 reviewer", "reviewer"},
		{"❓ reviewer", "reviewer"},
		{"✅ reviewer", "reviewer"},
		{"❔ reviewer", "reviewer"},
		{"🏁 reviewer", "reviewer"},
		{"🏁   spaced", "spaced"},
		{"🏁reviewer", "reviewer"},
		{"reviewer", "reviewer"},
		{"V3Jobs · claude", "V3Jobs · claude"},
		{"🚀 reviewer", "🚀 reviewer"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := domain.StripPrefix(tt.in); got != tt.want {
				t.Fatalf("StripPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
