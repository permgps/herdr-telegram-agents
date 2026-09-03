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
	if len(groups) != 5 || groups[0].Name != GroupSync || groups[1].Name != GroupQuiet || groups[2].Name != GroupAppearance || groups[3].Name != GroupPrivacy || groups[4].Name != GroupTopics {
		t.Fatalf("groups = %+v", groups)
	}
	quiet := OptionsInGroup(GroupQuiet)
	wantQuiet := []struct {
		key     string
		kind    OptionKind
		def     string
		choices string
	}{
		{OptionQuietEnabled, KindBool, "true", ""},
		{OptionQuietIdleMinutes, KindChoice, "3", ChoiceSourceMinutes},
		{OptionQuietTopics, KindBool, "true", ""},
		{OptionQuietPosts, KindChoice, "silent", ChoiceSourcePosts},
		{OptionQuietReannounce, KindBool, "true", ""},
	}
	if len(quiet) != len(wantQuiet) {
		t.Fatalf("quiet options = %+v", quiet)
	}
	for i, w := range wantQuiet {
		got := quiet[i]
		if got.Key != w.key || got.Kind != w.kind || got.Default != w.def || got.Choices != w.choices {
			t.Errorf("quiet option %d = %+v, want %+v", i, got, w)
		}
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

func TestQuietOptionsDefaultsAndAccessors(t *testing.T) {
	o := DefaultOptions()
	if !o.QuietEnabled() || !o.QuietTopics() || !o.QuietReannounce() {
		t.Error("quiet switches should default to on")
	}
	if got := o.QuietIdle(); got != 3*time.Minute {
		t.Errorf("QuietIdle default = %v", got)
	}
	if got := o.QuietPosts(); got != PostsSilent {
		t.Errorf("QuietPosts default = %q", got)
	}
	off, _ := o.With(OptionQuietEnabled, "false")
	if off.QuietEnabled() || !o.QuietEnabled() {
		t.Error("With did not switch quiet off, or mutated the receiver")
	}
	for value, want := range map[string]PostsMode{"silent": PostsSilent, "held": PostsHeld, "normal": PostsNormal, "loud": PostsSilent, " held ": PostsHeld} {
		next, _ := o.With(OptionQuietPosts, value)
		if got := next.QuietPosts(); got != want {
			t.Errorf("QuietPosts(%q) = %q, want %q", value, got, want)
		}
	}
	for value, want := range map[string]time.Duration{"1": time.Minute, "45": 45 * time.Minute, "x": 3 * time.Minute, "0": 3 * time.Minute} {
		next, _ := o.With(OptionQuietIdleMinutes, value)
		if got := next.QuietIdle(); got != want {
			t.Errorf("QuietIdle(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestQuietOptionsValidation(t *testing.T) {
	o := DefaultOptions()
	for _, v := range []string{"1", "3", "45", "1440"} {
		next, _ := o.With(OptionQuietIdleMinutes, v)
		if err := ValidateOptions(next, nil); err != nil {
			t.Errorf("ValidateOptions(minutes %q): %v", v, err)
		}
	}
	for _, v := range []string{"0", "-1", "x", "", "1441", "3 min"} {
		next, _ := o.With(OptionQuietIdleMinutes, v)
		if err := ValidateOptions(next, nil); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("ValidateOptions(minutes %q) = %v, want ErrInvalidOption", v, err)
		}
	}
	for _, v := range []string{"silent", "held", "normal"} {
		next, _ := o.With(OptionQuietPosts, v)
		if err := ValidateOptions(next, nil); err != nil {
			t.Errorf("ValidateOptions(posts %q): %v", v, err)
		}
	}
	// The posts list is static, so it is enforced even without an external source.
	loud, _ := o.With(OptionQuietPosts, "loud")
	if err := ValidateOptions(loud, nil); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("ValidateOptions(posts loud) = %v, want ErrInvalidOption", err)
	}
	// A hand-edited threshold outside the panel's list survives sanitising; a bad mode does not.
	dirty, _ := o.With(OptionQuietIdleMinutes, "45")
	dirty, _ = dirty.With(OptionQuietPosts, "loud")
	clean, dropped := SanitizeOptions(dirty, nil)
	if clean.String(OptionQuietIdleMinutes) != "45" || strings.Join(dropped, ",") != OptionQuietPosts || clean.QuietPosts() != PostsSilent {
		t.Errorf("sanitize kept %q / %q, dropped %v", clean.String(OptionQuietIdleMinutes), clean.String(OptionQuietPosts), dropped)
	}
	for name, want := range map[string][]string{ChoiceSourceMinutes: MinutesChoices(), ChoiceSourcePosts: PostsChoices()} {
		list, ok := StaticChoices(name)
		if !ok || strings.Join(list, ",") != strings.Join(want, ",") {
			t.Errorf("StaticChoices(%q) = %v, %v", name, list, ok)
		}
	}
}

func TestQuietChoiceLabels(t *testing.T) {
	minutes, _ := LookupOption(OptionQuietIdleMinutes)
	posts, _ := LookupOption(OptionQuietPosts)
	cases := []struct {
		spec          OptionSpec
		value         string
		label, button string
	}{
		{minutes, "1", "1 min", "1m"},
		{minutes, "15", "15 min", "15m"},
		{minutes, "garbage", "garbage", "garbage"},
		{posts, "silent", "Silent", "Silent"},
		{posts, "held", "Held", "Held"},
		{posts, "normal", "Normal", "Normal"},
		{posts, "loud", "loud", "loud"},
	}
	for _, tc := range cases {
		if got := ChoiceLabel(tc.spec, tc.value); got != tc.label {
			t.Errorf("ChoiceLabel(%s, %q) = %q, want %q", tc.spec.Key, tc.value, got, tc.label)
		}
		if got := ChoiceButton(tc.spec, tc.value); got != tc.button {
			t.Errorf("ChoiceButton(%s, %q) = %q, want %q", tc.spec.Key, tc.value, got, tc.button)
		}
	}
}
