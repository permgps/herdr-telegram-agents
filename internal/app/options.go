package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Options holds the operator-editable options in force. Readers (the
// reconciler, outbound, inbound) consult it on every action, so a change
// applies to the next event without any goroutine touching another's
// state. Set validates, saves, swaps and then runs the change hooks on the
// caller's goroutine; the hooks only signal (a resync request, a gateway
// setter) and must not block.
type Options struct {
	mu      sync.RWMutex
	cur     domain.Options
	store   domain.OptionsStore
	choices domain.ChoiceSource
	hooks   []optionHook
	log     *slog.Logger
}

// optionHook runs after every change of a key with the prefix.
type optionHook struct {
	prefix string
	fn     func(key string, cur domain.Options)
}

// NewOptions wraps loaded options. store may be nil (tests): changes then
// live in memory only. choices resolves KindChoice lists; nil accepts any
// value.
func NewOptions(cur domain.Options, store domain.OptionsStore, choices domain.ChoiceSource, log *slog.Logger) *Options {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Options{cur: cur, store: store, choices: choices, log: log}
}

// Get returns the options in force (a value; safe to keep).
func (o *Options) Get() domain.Options {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.cur
}

// SyncEnabled is the Herdr → Telegram mirror switch.
func (o *Options) SyncEnabled() bool { return o.Get().SyncEnabled() }

// StatusIcons is the icon table in force.
func (o *Options) StatusIcons() domain.StatusIcons { return o.Get().StatusIcons() }

// RedactEnabled is the secret redaction switch.
func (o *Options) RedactEnabled() bool { return o.Get().RedactEnabled() }

// DeleteAfter is the stale-topic age in force; zero means the sweep is off.
func (o *Options) DeleteAfter() time.Duration { return o.Get().DeleteAfter() }

// QuietEnabled is the quiet-mode master switch.
func (o *Options) QuietEnabled() bool { return o.Get().QuietEnabled() }

// QuietIdle is the idle threshold after which the operator counts as away.
func (o *Options) QuietIdle() time.Duration { return o.Get().QuietIdle() }

// QuietTopics reports whether topic writes wait while at the desk.
func (o *Options) QuietTopics() bool { return o.Get().QuietTopics() }

// QuietPosts is what quiet mode does with screen posts.
func (o *Options) QuietPosts() domain.PostsMode { return o.Get().QuietPosts() }

// PostsDone is what the topic receives when an agent turns done.
func (o *Options) PostsDone() domain.DoneMode { return o.Get().PostsDone() }

// QuietReannounce reports whether leaving re-posts still-blocked agents.
func (o *Options) QuietReannounce() bool { return o.Get().QuietReannounce() }

// PostsReactions reports whether prompts get 👀 / ✅ reactions.
func (o *Options) PostsReactions() bool { return o.Get().PostsReactions() }

// BlockedDelay is the extra wait before a blocked post; zero means none.
func (o *Options) BlockedDelay() time.Duration { return o.Get().BlockedDelay() }

// MinTurn is the shortest turn whose done screen is posted; zero posts all.
func (o *Options) MinTurn() time.Duration { return o.Get().MinTurn() }

// Choices lists the allowed values of a choice source; nil when unknown.
// Lists the domain owns (the day counts) are answered without asking the
// external source.
func (o *Options) Choices(name string) []string {
	if list, ok := domain.StaticChoices(name); ok {
		return list
	}
	if o.choices == nil {
		return nil
	}
	return o.choices(name)
}

// OnChange registers fn for every change of a key starting with prefix
// ("" matches all). Hooks run in registration order after the save.
func (o *Options) OnChange(prefix string, fn func(key string, cur domain.Options)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hooks = append(o.hooks, optionHook{prefix: prefix, fn: fn})
}

// Set changes one option. by is the operator id, for the log. A value equal
// to the current one saves nothing and runs no hook; a rejected value or a
// failed save leaves memory and file untouched.
func (o *Options) Set(ctx context.Context, key, value string, by int64) error {
	o.mu.Lock()
	if o.cur.String(key) == value {
		if _, ok := domain.LookupOption(key); ok {
			o.mu.Unlock()
			o.log.Debug("option unchanged", slog.String("key", key), slog.String("value", value))
			return nil
		}
	}
	next, err := o.cur.With(key, value)
	if err == nil {
		err = domain.ValidateOptions(next, o.choices)
	}
	if err != nil {
		o.mu.Unlock()
		o.log.Warn("option rejected", slog.String("key", key), slog.String("value", value), slog.Int64("by", by), slog.String("err", err.Error()))
		return err
	}
	if err := o.save(ctx, next); err != nil {
		o.mu.Unlock()
		return err
	}
	o.cur = next
	hooks := o.hooksFor(key)
	o.mu.Unlock()
	o.log.Info("option set", slog.String("key", key), slog.String("value", value), slog.Int64("by", by))
	o.runHooks(hooks, []string{key}, next)
	return nil
}

// Reset restores every option of a group to its default in one save and
// runs the hooks of each key that changed. changed is false when the
// group was already at its defaults.
func (o *Options) Reset(ctx context.Context, group string, by int64) (changed bool, err error) {
	o.mu.Lock()
	next := o.cur
	var keys []string
	for _, spec := range domain.OptionsInGroup(group) {
		if next.String(spec.Key) == spec.Default {
			continue
		}
		if next, err = next.With(spec.Key, spec.Default); err != nil {
			o.mu.Unlock()
			return false, err
		}
		keys = append(keys, spec.Key)
	}
	if len(keys) == 0 {
		o.mu.Unlock()
		o.log.Debug("options already at defaults", slog.String("group", group))
		return false, nil
	}
	if err := domain.ValidateOptions(next, o.choices); err != nil {
		o.mu.Unlock()
		o.log.Warn("options reset rejected", slog.String("group", group), slog.String("err", err.Error()))
		return false, err
	}
	if err := o.save(ctx, next); err != nil {
		o.mu.Unlock()
		return false, err
	}
	o.cur = next
	hooks := append([]optionHook(nil), o.hooks...)
	o.mu.Unlock()
	o.log.Info("options reset", slog.String("group", group), slog.Any("keys", keys), slog.Int64("by", by))
	o.runHooks(hooks, keys, next)
	return true, nil
}

// save persists next; the caller holds the lock.
func (o *Options) save(ctx context.Context, next domain.Options) error {
	if o.store == nil {
		return nil
	}
	if err := o.store.Save(ctx, next); err != nil {
		o.log.Error("options save failed", slog.String("err", err.Error()))
		return fmt.Errorf("save options: %w", err)
	}
	return nil
}

// hooksFor copies the hooks matching key; the caller holds the lock.
func (o *Options) hooksFor(key string) []optionHook {
	var out []optionHook
	for _, h := range o.hooks {
		if hasPrefix(key, h.prefix) {
			out = append(out, h)
		}
	}
	return out
}

func (o *Options) runHooks(hooks []optionHook, keys []string, cur domain.Options) {
	for _, key := range keys {
		for i, h := range hooks {
			if !hasPrefix(key, h.prefix) {
				continue
			}
			o.log.Debug("option hook", slog.String("key", key), slog.Int("hook", i))
			h.fn(key, cur)
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
