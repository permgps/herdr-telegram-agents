package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
// for KindChoice and is empty otherwise. Validate, when set, replaces the
// list check of a KindChoice: the list is then a set of suggestions the
// panel offers and any value Validate accepts is stored (a number typed
// into options.json by hand, for example).
type OptionSpec struct {
	Key         string
	Group       string
	Title       string
	Description string
	Kind        OptionKind
	Default     string
	Choices     string
	Validate    func(value string) error
}

// Option keys and group names referenced from the application layer.
const (
	OptionSyncEnabled = "sync.enabled"
	// OptionRedact switches the secret redaction of every post on or off.
	OptionRedact = "privacy.redact"
	// OptionDeleteAfterDays is how long a closed topic of an exited agent
	// stays before the sweep deletes it; "0" keeps every topic.
	OptionDeleteAfterDays = "topics.delete_after_days"
	// OptionQuietEnabled is the master switch of quiet mode: while the
	// operator is at the desk, topic writes wait and posts are silent.
	OptionQuietEnabled = "quiet.enabled"
	// OptionQuietIdleMinutes is how many minutes without keyboard or mouse
	// input count as "away".
	OptionQuietIdleMinutes = "quiet.idle_minutes"
	// OptionQuietTopics holds every topic write (create, icon, name, close,
	// reopen) while at the desk; each of them is a Telegram service message
	// that rings the phone.
	OptionQuietTopics = "quiet.topics"
	// OptionQuietPosts says what happens to screen posts while at the desk:
	// a PostsMode value.
	OptionQuietPosts = "quiet.posts"
	// OptionQuietReannounce re-posts, with sound, the screen of every agent
	// still blocked when the operator leaves, once per question.
	OptionQuietReannounce = "quiet.reannounce"
	// ChoiceSourceIcons is the choice list of the free topic-icon pack.
	ChoiceSourceIcons = "icons"
	// ChoiceSourceDays is the static list of day counts offered by the
	// panel for OptionDeleteAfterDays (see DaysChoices).
	ChoiceSourceDays = "days"
	// ChoiceSourceMinutes is the static list of idle thresholds offered by
	// the panel for OptionQuietIdleMinutes (see MinutesChoices).
	ChoiceSourceMinutes = "minutes"
	// ChoiceSourcePosts is the static list of PostsMode values.
	ChoiceSourcePosts = "posts"
	// OptionPostsDone says what a topic receives when its agent finishes:
	// a DoneMode value.
	OptionPostsDone = "posts.done"
	// ChoiceSourceDone is the static list of DoneMode values.
	ChoiceSourceDone = "done"
	// Group names, in the panel's display order.
	GroupSync       = "sync"
	GroupQuiet      = "quiet"
	GroupPosts      = "posts"
	GroupAppearance = "appearance"
	GroupPrivacy    = "privacy"
	GroupTopics     = "topics"

	iconKeyPrefix = "icons."
)

// DoneMode is what the topic receives when its agent turns done.
type DoneMode string

const (
	// DoneScreen posts the tail of the terminal screen as a code block.
	DoneScreen DoneMode = "screen"
	// DoneReply posts the agent's last reply from its transcript as a code
	// block.
	DoneReply DoneMode = "reply"
	// DoneFormatted posts the agent's last reply rendered from Markdown.
	DoneFormatted DoneMode = "formatted"
)

// PostsMode is what quiet mode does with screen posts while the operator
// is at the desk.
type PostsMode string

const (
	// PostsSilent sends the post without a sound (Telegram still shows a
	// silent banner).
	PostsSilent PostsMode = "silent"
	// PostsHeld does not send the post at all; the catch-up on leaving
	// posts agents that are still blocked.
	PostsHeld PostsMode = "held"
	// PostsNormal posts as if quiet mode were off.
	PostsNormal PostsMode = "normal"
)

// OptionGroupSpecs lists the groups in display order. Every OptionSpec.Group
// must name one of them (TestOptionSpecsReferenceKnownGroups guards it).
var OptionGroupSpecs = []OptionGroup{
	{Name: GroupSync, Title: "Sync", Description: "What the mirror writes to Telegram."},
	{Name: GroupQuiet, Title: "Quiet", Description: "Less noise while you are at the machine."},
	{Name: GroupPosts, Title: "Posts", Description: "What a topic receives when its agent finishes."},
	{Name: GroupAppearance, Title: "Appearance", Description: "How topics and status lines look."},
	{Name: GroupPrivacy, Title: "Privacy", Description: "What never leaves this machine."},
	{Name: GroupTopics, Title: "Topics", Description: "Lifecycle of the forum topics."},
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
		{
			Key:         OptionQuietEnabled,
			Group:       GroupQuiet,
			Title:       "Quiet while at the desk",
			Description: "While you are typing on this machine, topic edits wait and screen posts are silent; everything catches up when you leave.",
			Kind:        KindBool,
			Default:     "true",
		},
		{
			Key:         OptionQuietIdleMinutes,
			Group:       GroupQuiet,
			Title:       "Away after",
			Description: "Minutes without keyboard or mouse input before you count as away.",
			Kind:        KindChoice,
			Default:     "3",
			Choices:     ChoiceSourceMinutes,
			Validate:    validateMinutes,
		},
		{
			Key:         OptionQuietTopics,
			Group:       GroupQuiet,
			Title:       "Hold topic edits",
			Description: "While at the desk, no topic is created, renamed, closed or given a new icon; each of those rings the phone.",
			Kind:        KindBool,
			Default:     "true",
		},
		{
			Key:         OptionQuietPosts,
			Group:       GroupQuiet,
			Title:       "Screen posts",
			Description: "While at the desk: Silent posts without sound, Held waits until you leave, Normal posts as usual.",
			Kind:        KindChoice,
			Default:     string(PostsSilent),
			Choices:     ChoiceSourcePosts,
		},
		{
			Key:         OptionQuietReannounce,
			Group:       GroupQuiet,
			Title:       "Re-announce on leaving",
			Description: "When you leave, post the screen of every agent still waiting, with sound, once per question.",
			Kind:        KindBool,
			Default:     "true",
		},
		{
			Key:         OptionPostsDone,
			Group:       GroupPosts,
			Title:       "Done post",
			Description: "What is posted when an agent finishes: Screen is the last 12 lines of the terminal, Reply is the agent's last message from its transcript, Formatted renders that message with bold, lists, links and code.",
			Kind:        KindChoice,
			Default:     string(DoneScreen),
			Choices:     ChoiceSourceDone,
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
	specs = append(specs,
		OptionSpec{
			Key:         OptionRedact,
			Group:       GroupPrivacy,
			Title:       "Redact secrets",
			Description: "Mask API keys, tokens, passwords and private keys in every screen posted to Telegram.",
			Kind:        KindBool,
			Default:     "true",
		},
		OptionSpec{
			Key:         OptionDeleteAfterDays,
			Group:       GroupTopics,
			Title:       "Delete closed topics after",
			Description: "Delete the topic of an exited agent once it has been closed for this long, and forget it. Off keeps every topic.",
			Kind:        KindChoice,
			Default:     "30",
			Choices:     ChoiceSourceDays,
			Validate:    validateDays,
		},
	)
	return specs
}

// daysChoices is the list the panel offers for OptionDeleteAfterDays.
var daysChoices = []string{"0", "7", "14", "30", "60", "90"}

// maxDeleteAfterDays bounds a hand-edited value (ten years).
const maxDeleteAfterDays = 3650

// DaysChoices returns the day counts the panel offers: Off, 7, 14, 30, 60
// and 90 days.
func DaysChoices() []string { return append([]string(nil), daysChoices...) }

// minutesChoices is the list the panel offers for OptionQuietIdleMinutes.
var minutesChoices = []string{"1", "2", "3", "5", "10", "15"}

// maxIdleMinutes bounds a hand-edited idle threshold (one day).
const maxIdleMinutes = 1440

// defaultQuietIdle is the threshold in force when the stored value cannot
// be parsed.
const defaultQuietIdle = 3 * time.Minute

// MinutesChoices returns the idle thresholds the panel offers: 1, 2, 3, 5,
// 10 and 15 minutes.
func MinutesChoices() []string { return append([]string(nil), minutesChoices...) }

// postsChoices is the list the panel offers for OptionQuietPosts.
var postsChoices = []string{string(PostsSilent), string(PostsHeld), string(PostsNormal)}

// PostsChoices returns the PostsMode values the panel offers.
func PostsChoices() []string { return append([]string(nil), postsChoices...) }

// doneChoices is the list the panel offers for OptionPostsDone.
var doneChoices = []string{string(DoneScreen), string(DoneReply), string(DoneFormatted)}

// DoneChoices returns the DoneMode values the panel offers.
func DoneChoices() []string { return append([]string(nil), doneChoices...) }

// StaticChoices answers the choice lists the domain owns itself; the
// application layer asks it before the external ChoiceSource.
func StaticChoices(name string) ([]string, bool) {
	switch name {
	case ChoiceSourceDays:
		return DaysChoices(), true
	case ChoiceSourceMinutes:
		return MinutesChoices(), true
	case ChoiceSourcePosts:
		return PostsChoices(), true
	case ChoiceSourceDone:
		return DoneChoices(), true
	}
	return nil, false
}

func validateDays(value string) error {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 || n > maxDeleteAfterDays {
		return fmt.Errorf("%q is not a day count between 0 and %d: %w", value, maxDeleteAfterDays, ErrInvalidOption)
	}
	return nil
}

func validateMinutes(value string) error {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > maxIdleMinutes {
		return fmt.Errorf("%q is not a minute count between 1 and %d: %w", value, maxIdleMinutes, ErrInvalidOption)
	}
	return nil
}

// ChoiceLabel is the human form of a choice value in panel text: days
// become "Off", "1 day" or "30 days", minutes "1 min" or "3 min", posts
// modes "Silent", "Held", "Normal"; other sources show the value itself.
func ChoiceLabel(spec OptionSpec, value string) string {
	switch spec.Choices {
	case ChoiceSourceDays:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		switch {
		case err != nil:
			return value
		case n == 0:
			return "Off"
		case n == 1:
			return "1 day"
		default:
			return fmt.Sprintf("%d days", n)
		}
	case ChoiceSourceMinutes:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return value
		}
		return fmt.Sprintf("%d min", n)
	case ChoiceSourcePosts:
		return postsWord(value)
	case ChoiceSourceDone:
		return doneWord(value)
	}
	return value
}

// ChoiceButton is the short form of a choice value on a button: "Off",
// "7d", "3m", "Silent"; other sources show the value itself.
func ChoiceButton(spec OptionSpec, value string) string {
	switch spec.Choices {
	case ChoiceSourceDays:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		switch {
		case err != nil:
			return value
		case n == 0:
			return "Off"
		default:
			return fmt.Sprintf("%dd", n)
		}
	case ChoiceSourceMinutes:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return value
		}
		return fmt.Sprintf("%dm", n)
	case ChoiceSourcePosts:
		return postsWord(value)
	case ChoiceSourceDone:
		return doneWord(value)
	}
	return value
}

// postsWord capitalises a known PostsMode value ("silent" → "Silent") and
// returns anything else as is.
func postsWord(value string) string {
	switch PostsMode(strings.TrimSpace(value)) {
	case PostsSilent:
		return "Silent"
	case PostsHeld:
		return "Held"
	case PostsNormal:
		return "Normal"
	}
	return value
}

// doneWord capitalises a known DoneMode value ("reply" → "Reply") and
// returns anything else as is.
func doneWord(value string) string {
	switch DoneMode(strings.TrimSpace(value)) {
	case DoneScreen:
		return "Screen"
	case DoneReply:
		return "Reply"
	case DoneFormatted:
		return "Formatted"
	}
	return value
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

// RedactEnabled is the secret redaction switch.
func (o Options) RedactEnabled() bool { return o.Bool(OptionRedact) }

// DeleteAfter is the age at which the sweep deletes a closed topic of an
// exited agent; zero means the sweep is off (also for an unparsable value).
func (o Options) DeleteAfter() time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(o.String(OptionDeleteAfterDays)))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * 24 * time.Hour
}

// QuietEnabled is the quiet-mode master switch.
func (o Options) QuietEnabled() bool { return o.Bool(OptionQuietEnabled) }

// QuietIdle is how long the machine's input must be idle before the
// operator counts as away; three minutes for an unparsable value.
func (o Options) QuietIdle() time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(o.String(OptionQuietIdleMinutes)))
	if err != nil || n < 1 {
		return defaultQuietIdle
	}
	return time.Duration(n) * time.Minute
}

// QuietTopics reports whether topic writes wait while at the desk.
func (o Options) QuietTopics() bool { return o.Bool(OptionQuietTopics) }

// QuietPosts is what quiet mode does with screen posts; PostsSilent for an
// unknown value.
func (o Options) QuietPosts() PostsMode {
	switch m := PostsMode(strings.TrimSpace(o.String(OptionQuietPosts))); m {
	case PostsSilent, PostsHeld, PostsNormal:
		return m
	}
	return PostsSilent
}

// PostsDone is what the topic receives when an agent turns done;
// DoneScreen for an unknown value.
func (o Options) PostsDone() DoneMode {
	switch m := DoneMode(strings.TrimSpace(o.String(OptionPostsDone))); m {
	case DoneScreen, DoneReply, DoneFormatted:
		return m
	}
	return DoneScreen
}

// QuietReannounce reports whether leaving re-posts still-blocked agents
// with sound.
func (o Options) QuietReannounce() bool { return o.Bool(OptionQuietReannounce) }

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
// Lists the domain owns (StaticChoices) are answered before the source is
// asked; a spec with its own Validate never consults the list.
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
			if spec.Validate != nil {
				if err := spec.Validate(v); err != nil {
					return fmt.Errorf("option %q: %w", spec.Key, err)
				}
				continue
			}
			list, known := StaticChoices(spec.Choices)
			if !known && choices != nil {
				list = choices(spec.Choices)
			}
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
