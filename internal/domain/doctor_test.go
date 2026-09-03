package domain_test

import (
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestCheckMarksAndSummary(t *testing.T) {
	checks := []domain.Check{
		{Name: "config", Level: domain.CheckOK, Detail: "fine"},
		{Name: "group", Level: domain.CheckWarn, Detail: "no delete right"},
		{Name: "herdr", Level: domain.CheckFail, Detail: "socket gone"},
		{Name: "mapping", Level: domain.CheckOK, Detail: "3 entries"},
	}
	marks := []string{"✓", "!", "✗", "✓"}
	for i, c := range checks {
		if c.Mark() != marks[i] {
			t.Errorf("check %s mark = %q, want %q", c.Name, c.Mark(), marks[i])
		}
	}
	ok, warn, fail := domain.Summarize(checks)
	if ok != 2 || warn != 1 || fail != 1 {
		t.Fatalf("Summarize = %d %d %d", ok, warn, fail)
	}
	if ok, warn, fail := domain.Summarize(nil); ok+warn+fail != 0 {
		t.Fatal("empty summary should be zero")
	}
}
