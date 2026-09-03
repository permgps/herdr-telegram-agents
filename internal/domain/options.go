package domain

import (
	"fmt"
	"strings"
)

// OptionKind says how an option is edited and validated.
type OptionKind int

const (
	// KindBool is a checkbox: "true" or "false".
	KindBool OptionKind = iota + 1
	// KindChoice is one value out of a list named by OptionSpec.Choices;
	// the list comes from outside the domain (the topic-icon pack).
	KindChoice
	// KindText is a free string; the panel has no editor for it yet, the
	// registry and the store understand it so a future option needs no
	// format change.
	KindText
)

// OptionGroup is a heading in the options panel. Name is the stable id
// used in callback data and in OptionSpec.Group; Title and Description are
// the English strings the operator sees.
type OptionGroup struct {
	Name        string
	Title       string
	Description string
}

// OptionSpec describes one option: the registry row that drives the panel,
// options.json, validation and /help. Title is the short label on the
// button, Description one English sentence shown in the panel text.
// Default is the value in string form. Choices names a ChoiceSource list
// for KindChoice and is empty otherwise.
type OptionSpec struct {
	Key         string
	Group       string
	Title       string
	Description string
	Kind        OptionKind
	Default     string
	Choices     string
}

// Option keys and group names referenced from the application layer.
const (
	OptionSyncEnabled = "sync.enabled"
	// ChoiceSourceIcons is the choice list of the free topic-icon pack.
	ChoiceSourceIcons = "icons"
	// GroupSync and GroupAppearance are the group names of the first
	// options.
	GroupSync       = "sync"
	GroupAppearance = "appearance"

	iconKeyPrefix = "icons."
)

// OptionGroupSpecs lists the groups in display order. Every OptionSpec.Group
// must name one of them (TestOptionSpecsReferenceKnownGroups guards it).
var OptionGroupSpecs = []OptionGroup{
	{Name: GroupSync, Title: "Sync", Description: "What the mirror writes to Telegram."},
	{Name: GroupAppearance, Title: "Appearance", Description: "How topics and status lines look."},
}

// iconStatuses is the order of the status-icon options and of StatusIcons.
var iconStatuses = [...]Status{StatusWorking, StatusIdle, StatusBlocked, StatusDone, StatusUnknown, StatusExited}

// OptionSpecs is the registry of every option in display order. Adding an
// option is one row here plus the place that reads its value.
var OptionSpecs = buildOptionSpecs()

func buildOptionSpecs() []OptionSpec {
	specs := []OptionSpec{
		{
			Key:         OptionSyncEnabled,
			Group:       GroupSync,
			Title:       "Herdr → Telegram sync",
			Description: "Mirror Herdr agents into Telegram topics. Off: no topic edits and no screen posts until it is on again.",
			Kind:        KindBool,
			Default:     "true",
		},
	}
	descriptions := map[Status]string{
		StatusWorking: "Topic icon while the agent is working.",
		StatusIdle:    "Topic icon while the agent waits at its prompt.",
		StatusBlocked: "Topic icon while the agent asks a question or waits for approval.",
		StatusDone:    "Topic icon after the agent finished its turn.",
		StatusUnknown: "Topic icon when Herdr cannot tell the state.",
		StatusExited:  "Topic icon after the agent's pane closed.",
	}
	for _, st := range iconStatuses {
		specs = append(specs, OptionSpec{
			Key:         IconKey(st),
			Group:       GroupAppearance,
			Title:       string(st),
			Description: descriptions[st],
			Kind:        KindChoice,
			Default:     st.Emoji(),
			Choices:     ChoiceSourceIcons,
		})
	}
	return specs
}

// OptionGroups returns the groups in display order.
func OptionGroups() []OptionGroup {
	out := make([]OptionGroup, len(OptionGroupSpecs))
	copy(out, OptionGroupSpecs)
	return out
}

// OptionsInGroup returns the specs of one group in display order.
func OptionsInGroup(group string) []OptionSpec {
	var out []OptionSpec
	for _, s := range OptionSpecs {
		if s.Group == group {
			out = append(out, s)
		}
	}
	return out
}

// LookupOption finds a spec by key.
func LookupOption(key string) (OptionSpec, bool) {
	for _, s := range OptionSpecs {
		if s.Key == key {
			return s, true
		}
	}
	return OptionSpec{}, false
}

// LookupOptionGroup finds a group by name.
func LookupOptionGroup(name string) (OptionGroup, bool) {
	for _, g := range OptionGroupSpecs {
		if g.Name == name {
			return g, true
		}
	}
	return OptionGroup{}, false
}

// IconKey is the option key holding the icon of a status ("icons.working").
func IconKey(st Status) string { return iconKeyPrefix + string(st) }

// StatusOfIconKey is the inverse of IconKey; false for any other key.
func StatusOfIconKey(key string) (Status, bool) {
	if !strings.HasPrefix(key, iconKeyPrefix) {
		return "", false
	}
	st := Status(strings.TrimPrefix(key, iconKeyPrefix))
	for _, known := range iconStatuses {
		if st == known {
			return st, true
		}
	}
	return "", false
}

// Options is the value bag: every known key in string form. The zero value
// and DefaultOptions both answer with the defaults. Values are immutable;
// With returns a modified copy.
type Options struct {
	values map[string]string
}

// DefaultOptions returns every option at its default.
func DefaultOptions() Options { return Options{} }

// String returns the value of key, or its default when unset. Unknown keys
// yield "".
func (o Options) String(key string) string {
	if v, ok := o.values[key]; ok {
		return v
	}
	spec, ok := LookupOption(key)
	if !ok {
		return ""
	}
	return spec.Default
}

// Bool returns a KindBool option; anything but "true" is false.
func (o Options) Bool(key string) bool { return o.String(key) == "true" }

// With returns a copy with key set to value. The key must exist and a bool
// must be "true" or "false"; choice and text values are checked by
// ValidateOptions because the choice lists live outside the domain.
func (o Options) With(key, value string) (Options, error) {
	spec, ok := LookupOption(key)
	if !ok {
		return o, fmt.Errorf("option %q: %w", key, ErrUnknownOption)
	}
	if spec.Kind == KindBool && value != "true" && value != "false" {
		return o, fmt.Errorf("option %q: %q is not a bool: %w", key, value, ErrInvalidOption)
	}
	next := Options{values: make(map[string]string, len(o.values)+1)}
	for k, v := range o.values {
		next.values[k] = v
	}
	next.values[key] = value
	return next, nil
}

// Values returns every known key with its effective value (a copy).
func (o Options) Values() map[string]string {
	out := make(map[string]string, len(OptionSpecs))
	for _, s := range OptionSpecs {
		out[s.Key] = o.String(s.Key)
	}
	return out
}

// IsDefault reports whether key holds its default value.
func (o Options) IsDefault(key string) bool {
	spec, ok := LookupOption(key)
	return ok && o.String(key) == spec.Default
}

// SyncEnabled is the Herdr → Telegram mirror switch.
func (o Options) SyncEnabled() bool { return o.Bool(OptionSyncEnabled) }

// StatusIcons collects the six icon options.
func (o Options) StatusIcons() StatusIcons {
	return StatusIcons{
		Working: o.String(IconKey(StatusWorking)),
		Idle:    o.String(IconKey(StatusIdle)),
		Blocked: o.String(IconKey(StatusBlocked)),
		Done:    o.String(IconKey(StatusDone)),
		Unknown: o.String(IconKey(StatusUnknown)),
		Exited:  o.String(IconKey(StatusExited)),
	}
}

// StatusIcons is the emoji shown for each status: the topic icon and the
// glyph in bot text such as /status.
type StatusIcons struct {
	Working, Idle, Blocked, Done, Unknown, Exited string
}

// DefaultStatusIcons is the built-in table (⚡ ✅ ❓ 🏆 👀 🏁).
func DefaultStatusIcons() StatusIcons { return DefaultOptions().StatusIcons() }

// For returns the icon of a status; unknown values map to Unknown.
func (s StatusIcons) For(st Status) string {
	switch st {
	case StatusWorking:
		return s.Working
	case StatusIdle:
		return s.Idle
	case StatusBlocked:
		return s.Blocked
	case StatusDone:
		return s.Done
	case StatusExited:
		return s.Exited
	default:
		return s.Unknown
	}
}

// Duplicate reports two statuses that share an icon (variation selectors
// ignored), in registry order. Two statuses with one icon would be
// indistinguishable in Telegram.
func (s StatusIcons) Duplicate() (Status, Status, bool) {
	seen := make(map[string]Status, len(iconStatuses))
	for _, st := range iconStatuses {
		key := emojiKey(s.For(st))
		if key == "" {
			continue
		}
		if prev, ok := seen[key]; ok {
			return prev, st, true
		}
		seen[key] = st
	}
	return "", "", false
}

// UsedBy reports which status shows emoji (variation selector ignored).
func (s StatusIcons) UsedBy(emoji string) (Status, bool) {
	want := emojiKey(emoji)
	if want == "" {
		return "", false
	}
	for _, st := range iconStatuses {
		if emojiKey(s.For(st)) == want {
			return st, true
		}
	}
	return "", false
}

// emojiKey strips the U+FE0F variation selector so "⚡️" and "⚡" compare
// equal; Telegram's pack lists some emoji with it, keyboards send without.
func emojiKey(e string) string { return strings.ReplaceAll(e, "️", "") }

// ChoiceSource returns the allowed values of a choice list by name. An
// empty result means the list is not known yet and any value is accepted.
type ChoiceSource func(name string) []string

// ValidateOptions checks every value against its spec: bools parse, a
// choice is in its list when the list is known, a text is non-empty, and
// no two statuses share an icon.
func ValidateOptions(o Options, choices ChoiceSource) error {
	for _, spec := range OptionSpecs {
		v := o.String(spec.Key)
		switch spec.Kind {
		case KindBool:
			if v != "true" && v != "false" {
				return fmt.Errorf("option %q: %q is not a bool: %w", spec.Key, v, ErrInvalidOption)
			}
		case KindChoice:
			if v == "" {
				return fmt.Errorf("option %q: empty: %w", spec.Key, ErrInvalidOption)
			}
			if choices == nil {
				continue
			}
			list := choices(spec.Choices)
			if len(list) > 0 && !containsEmoji(list, v) {
				return fmt.Errorf("option %q: %q is not in the %s list: %w", spec.Key, v, spec.Choices, ErrInvalidOption)
			}
		case KindText:
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("option %q: empty: %w", spec.Key, ErrInvalidOption)
			}
		}
	}
	if a, b, dup := o.StatusIcons().Duplicate(); dup {
		return fmt.Errorf("%w: %s and %s share %s", ErrDuplicateIcon, a, b, o.StatusIcons().For(a))
	}
	return nil
}

// SanitizeOptions applies the values of o one key at a time on top of the
// defaults, in registry order, and drops every key whose value fails
// ValidateOptions against the rest; dropped keys are returned so the caller
// can log them. It is meant for a file edited by hand: a duplicate icon
// keeps the earlier status, an emoji outside the pack falls back to the
// default, and the other options survive untouched.
func SanitizeOptions(o Options, choices ChoiceSource) (Options, []string) {
	clean := DefaultOptions()
	var dropped []string
	for _, spec := range OptionSpecs {
		value := o.String(spec.Key)
		if value == spec.Default {
			continue
		}
		next, err := clean.With(spec.Key, value)
		if err == nil {
			err = ValidateOptions(next, choices)
		}
		if err != nil {
			dropped = append(dropped, spec.Key)
			continue
		}
		clean = next
	}
	return clean, dropped
}

func containsEmoji(list []string, v string) bool {
	want := emojiKey(v)
	for _, e := range list {
		if emojiKey(e) == want {
			return true
		}
	}
	return false
}
