package domain

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestDefaultOptionsMatchStatusEmoji(t *testing.T) {
	icons := DefaultStatusIcons()
	want := StatusIcons{Working: "⚡", Idle: "✅", Blocked: "❓", Done: "🏆", Unknown: "👀", Exited: "🏁"}
	if icons != want {
		t.Fatalf("defaults = %+v, want %+v", icons, want)
	}
	for _, st := range iconStatuses {
		if got := icons.For(st); got != st.Emoji() {
			t.Errorf("For(%s) = %q, want Status.Emoji %q", st, got, st.Emoji())
		}
	}
	if !DefaultOptions().SyncEnabled() {
		t.Error("sync is off by default")
	}
}

func TestOptionsWith(t *testing.T) {
	base := DefaultOptions()
	next, err := base.With(OptionSyncEnabled, "false")
	if err != nil {
		t.Fatal(err)
	}
	if next.SyncEnabled() {
		t.Error("sync still on after With")
	}
	if !base.SyncEnabled() {
		t.Error("With mutated the receiver")
	}
	if base.IsDefault(OptionSyncEnabled) != true || next.IsDefault(OptionSyncEnabled) != false {
		t.Error("IsDefault wrong")
	}
	if _, err := base.With("nope", "x"); !errors.Is(err, ErrUnknownOption) {
		t.Errorf("unknown key err = %v", err)
	}
	if _, err := base.With(OptionSyncEnabled, "yes"); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("bad bool err = %v", err)
	}
	if got := base.String("nope"); got != "" {
		t.Errorf("String(unknown) = %q", got)
	}
	vals := next.Values()
	if len(vals) != len(OptionSpecs) || vals[OptionSyncEnabled] != "false" || vals[IconKey(StatusIdle)] != "✅" {
		t.Errorf("Values = %v", vals)
	}
}

func TestStatusIconsRoundTripAndKeys(t *testing.T) {
	o, err := DefaultOptions().With(IconKey(StatusWorking), "🔥")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.StatusIcons().For(StatusWorking); got != "🔥" {
		t.Errorf("working icon = %q", got)
	}
	if got := o.StatusIcons().For("weird"); got != "👀" {
		t.Errorf("unknown status icon = %q", got)
	}
	for _, st := range iconStatuses {
		back, ok := StatusOfIconKey(IconKey(st))
		if !ok || back != st {
			t.Errorf("StatusOfIconKey(IconKey(%s)) = %s, %v", st, back, ok)
		}
	}
	for _, key := range []string{"icons.", "icons.nope", OptionSyncEnabled} {
		if _, ok := StatusOfIconKey(key); ok {
			t.Errorf("StatusOfIconKey(%q) accepted", key)
		}
	}
}

func TestDuplicateIcons(t *testing.T) {
	if a, b, dup := DefaultStatusIcons().Duplicate(); dup {
		t.Fatalf("defaults report duplicate %s/%s", a, b)
	}
	o, _ := DefaultOptions().With(IconKey(StatusDone), "⚡️") // with variation selector
	a, b, dup := o.StatusIcons().Duplicate()
	if !dup || a != StatusWorking || b != StatusDone {
		t.Errorf("Duplicate = %s, %s, %v; want working, done, true", a, b, dup)
	}
	if err := ValidateOptions(o, nil); !errors.Is(err, ErrDuplicateIcon) {
		t.Errorf("validate err = %v", err)
	}
}

func TestValidateOptionsAgainstSource(t *testing.T) {
	pack := func(name string) []string {
		if name == ChoiceSourceIcons {
			return []string{"⚡️", "✅", "❓", "🏆", "👀", "🏁", "🔥"}
		}
		return nil
	}
	if err := ValidateOptions(DefaultOptions(), pack); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	ok, _ := DefaultOptions().With(IconKey(StatusWorking), "🔥")
	if err := ValidateOptions(ok, pack); err != nil {
		t.Errorf("🔥 rejected: %v", err)
	}
	bad, _ := DefaultOptions().With(IconKey(StatusWorking), "🚀")
	if err := ValidateOptions(bad, pack); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("🚀 accepted: %v", err)
	}
	empty := func(string) []string { return nil }
	if err := ValidateOptions(bad, empty); err != nil {
		t.Errorf("unknown list should accept: %v", err)
	}
	blank, _ := DefaultOptions().With(IconKey(StatusWorking), "")
	if err := ValidateOptions(blank, nil); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("empty icon accepted: %v", err)
	}
}

func TestOptionGroupsAndSpecs(t *testing.T) {
	groups := OptionGroups()
	if len(groups) != 2 || groups[0].Name != GroupSync || groups[1].Name != GroupAppearance {
		t.Fatalf("groups = %+v", groups)
	}
	if got := OptionsInGroup(GroupAppearance); len(got) != 6 || got[0].Key != "icons.working" || got[5].Key != "icons.exited" {
		t.Errorf("appearance options = %+v", got)
	}
	if got := OptionsInGroup(GroupSync); len(got) != 1 || got[0].Kind != KindBool {
		t.Errorf("sync options = %+v", got)
	}
	seen := map[string]bool{}
	for _, s := range OptionSpecs {
		if seen[s.Key] {
			t.Errorf("duplicate key %q", s.Key)
		}
		seen[s.Key] = true
		if _, ok := LookupOptionGroup(s.Group); !ok {
			t.Errorf("option %q references unknown group %q", s.Key, s.Group)
		}
		if s.Kind == KindChoice && s.Choices == "" {
			t.Errorf("choice option %q has no choice source", s.Key)
		}
		if _, ok := LookupOption(s.Key); !ok {
			t.Errorf("LookupOption(%q) failed", s.Key)
		}
	}
}

// TestOptionStringsAreEnglish guards the decision that everything an
// operator sees is English: titles and descriptions are present and carry
// no Cyrillic.
func TestOptionStringsAreEnglish(t *testing.T) {
	cyrillic := regexp.MustCompile(`\p{Cyrillic}`)
	check := func(what, s string) {
		t.Helper()
		if s == "" {
			t.Errorf("%s is empty", what)
		}
		if s != strings.TrimSpace(s) {
			t.Errorf("%s has surrounding whitespace: %q", what, s)
		}
		if cyrillic.MatchString(s) {
			t.Errorf("%s contains Cyrillic: %q", what, s)
		}
	}
	for _, g := range OptionGroupSpecs {
		check("group "+g.Name+" title", g.Title)
		check("group "+g.Name+" description", g.Description)
	}
	for _, s := range OptionSpecs {
		check("option "+s.Key+" title", s.Title)
		check("option "+s.Key+" description", s.Description)
	}
}

func TestSanitizeOptionsDropsOnlyBadKeys(t *testing.T) {
	pack := func(name string) []string { return []string{"⚡", "✅", "❓", "🏆", "👀", "🏁", "🔥"} }
	o := DefaultOptions()
	o, _ = o.With(OptionSyncEnabled, "false")
	o, _ = o.With(IconKey(StatusWorking), "🔥")
	o, _ = o.With(IconKey(StatusDone), "🔥")   // duplicate of working: dropped
	o, _ = o.With(IconKey(StatusIdle), "🚀")   // not in the pack: dropped
	o, _ = o.With(IconKey(StatusExited), "❓") // duplicate of blocked (earlier in registry order): dropped

	clean, dropped := SanitizeOptions(o, pack)
	if clean.SyncEnabled() {
		t.Error("sync flag lost")
	}
	icons := clean.StatusIcons()
	if icons.Working != "🔥" || icons.Done != "🏆" || icons.Idle != "✅" || icons.Exited != "🏁" || icons.Blocked != "❓" {
		t.Errorf("icons = %+v", icons)
	}
	want := []string{"icons.idle", "icons.done", "icons.exited"}
	if strings.Join(dropped, ",") != strings.Join(want, ",") {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
	if err := ValidateOptions(clean, pack); err != nil {
		t.Errorf("sanitized options invalid: %v", err)
	}
	if _, d := SanitizeOptions(DefaultOptions(), pack); len(d) != 0 {
		t.Errorf("defaults dropped %v", d)
	}
}
