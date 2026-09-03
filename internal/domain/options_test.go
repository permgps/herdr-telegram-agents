package domain

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
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
	if len(groups) != 4 || groups[0].Name != GroupSync || groups[1].Name != GroupAppearance || groups[2].Name != GroupPrivacy || groups[3].Name != GroupTopics {
		t.Fatalf("groups = %+v", groups)
	}
	if got := OptionsInGroup(GroupPrivacy); len(got) != 1 || got[0].Key != OptionRedact || got[0].Kind != KindBool || got[0].Default != "true" {
		t.Errorf("privacy options = %+v", got)
	}
	if got := OptionsInGroup(GroupTopics); len(got) != 1 || got[0].Key != OptionDeleteAfterDays || got[0].Kind != KindChoice || got[0].Default != "30" || got[0].Choices != ChoiceSourceDays {
		t.Errorf("topics options = %+v", got)
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

func TestDeleteAfterDaysValidation(t *testing.T) {
	o := DefaultOptions()
	for _, v := range []string{"0", "7", "45", "3650"} {
		next, err := o.With(OptionDeleteAfterDays, v)
		if err != nil {
			t.Fatalf("With(%q): %v", v, err)
		}
		if err := ValidateOptions(next, nil); err != nil {
			t.Errorf("ValidateOptions(%q): %v", v, err)
		}
	}
	for _, v := range []string{"-1", "x", "", "3651", "7 days"} {
		next, err := o.With(OptionDeleteAfterDays, v)
		if err != nil {
			t.Fatalf("With(%q): %v", v, err)
		}
		if err := ValidateOptions(next, nil); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("ValidateOptions(%q) = %v, want ErrInvalidOption", v, err)
		}
	}
	// A hand-edited value outside the panel's list survives sanitising.
	dirty, _ := o.With(OptionDeleteAfterDays, "45")
	clean, dropped := SanitizeOptions(dirty, nil)
	if len(dropped) != 0 || clean.String(OptionDeleteAfterDays) != "45" {
		t.Errorf("sanitize kept %q, dropped %v", clean.String(OptionDeleteAfterDays), dropped)
	}
}

func TestDeleteAfterAndLabels(t *testing.T) {
	spec, _ := LookupOption(OptionDeleteAfterDays)
	cases := []struct {
		value  string
		want   time.Duration
		label  string
		button string
	}{
		{"0", 0, "Off", "Off"},
		{"1", 24 * time.Hour, "1 day", "1d"},
		{"30", 30 * 24 * time.Hour, "30 days", "30d"},
		{"garbage", 0, "garbage", "garbage"},
	}
	for _, tc := range cases {
		o, _ := DefaultOptions().With(OptionDeleteAfterDays, tc.value)
		if got := o.DeleteAfter(); got != tc.want {
			t.Errorf("DeleteAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
		if got := ChoiceLabel(spec, tc.value); got != tc.label {
			t.Errorf("ChoiceLabel(%q) = %q, want %q", tc.value, got, tc.label)
		}
		if got := ChoiceButton(spec, tc.value); got != tc.button {
			t.Errorf("ChoiceButton(%q) = %q, want %q", tc.value, got, tc.button)
		}
	}
	if !DefaultOptions().RedactEnabled() {
		t.Error("redaction should default to on")
	}
	off, _ := DefaultOptions().With(OptionRedact, "false")
	if off.RedactEnabled() {
		t.Error("redaction should be off")
	}
	icons, _ := LookupOption(IconKey(StatusWorking))
	if ChoiceLabel(icons, "⚡") != "⚡" || ChoiceButton(icons, "⚡") != "⚡" {
		t.Error("icon labels must pass through")
	}
	list, ok := StaticChoices(ChoiceSourceDays)
	if !ok || len(list) != 6 || list[0] != "0" || list[5] != "90" {
		t.Errorf("StaticChoices(days) = %v, %v", list, ok)
	}
	if _, ok := StaticChoices(ChoiceSourceIcons); ok {
		t.Error("icons are not a static source")
	}
}
