package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func iconsOf(pack ...string) domain.ChoiceSource {
	return func(name string) []string {
		if name == domain.ChoiceSourceIcons {
			return pack
		}
		return nil
	}
}

func TestOptionsSetSavesThenRunsHooks(t *testing.T) {
	store := testkit.NewMemOptionsStore()
	o := NewOptions(domain.DefaultOptions(), store, iconsOf("⚡", "✅", "❓", "🏆", "👀", "🏁", "🔥"), nil)
	var order []string
	o.OnChange("sync.", func(key string, cur domain.Options) {
		order = append(order, "sync:"+key+"="+cur.String(key)+" saved="+itoa(store.Saved()))
	})
	o.OnChange("icons.", func(key string, cur domain.Options) { order = append(order, "icons:"+key) })
	o.OnChange("", func(key string, cur domain.Options) { order = append(order, "all:"+key) })

	ctx := context.Background()
	if err := o.Set(ctx, domain.OptionSyncEnabled, "false", 7); err != nil {
		t.Fatal(err)
	}
	if o.SyncEnabled() || store.Stored().SyncEnabled() {
		t.Error("sync still on in memory or store")
	}
	if err := o.Set(ctx, domain.OptionSyncEnabled, "false", 7); err != nil {
		t.Fatal(err)
	}
	if store.Saved() != 1 {
		t.Errorf("no-op Set saved again: %d", store.Saved())
	}
	if err := o.Set(ctx, domain.IconKey(domain.StatusWorking), "🔥", 7); err != nil {
		t.Fatal(err)
	}
	want := []string{"sync:sync.enabled=false saved=1", "all:sync.enabled", "icons:icons.working", "all:icons.working"}
	if len(order) != len(want) {
		t.Fatalf("hooks = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("hook %d = %q, want %q", i, order[i], want[i])
		}
	}
	if o.StatusIcons().Working != "🔥" {
		t.Errorf("icons = %+v", o.StatusIcons())
	}
}

func TestOptionsRejectedValuesTouchNothing(t *testing.T) {
	store := testkit.NewMemOptionsStore()
	o := NewOptions(domain.DefaultOptions(), store, iconsOf("⚡", "✅", "❓", "🏆", "👀", "🏁"), nil)
	hooks := 0
	o.OnChange("", func(string, domain.Options) { hooks++ })
	ctx := context.Background()

	cases := []struct {
		key, value string
		want       error
	}{
		{"nope", "x", domain.ErrUnknownOption},
		{domain.OptionSyncEnabled, "maybe", domain.ErrInvalidOption},
		{domain.IconKey(domain.StatusWorking), "🚀", domain.ErrInvalidOption}, // not in the pack
		{domain.IconKey(domain.StatusWorking), "✅", domain.ErrDuplicateIcon}, // idle's icon
	}
	for _, c := range cases {
		if err := o.Set(ctx, c.key, c.value, 1); !errors.Is(err, c.want) {
			t.Errorf("Set(%s, %s) = %v, want %v", c.key, c.value, err, c.want)
		}
	}
	if store.Saved() != 0 || hooks != 0 || o.StatusIcons() != domain.DefaultStatusIcons() || !o.SyncEnabled() {
		t.Errorf("rejected values leaked: saves=%d hooks=%d icons=%+v", store.Saved(), hooks, o.StatusIcons())
	}

	boom := errors.New("disk full")
	store.FailNext(boom)
	if err := o.Set(ctx, domain.OptionSyncEnabled, "false", 1); !errors.Is(err, boom) {
		t.Fatalf("save failure = %v", err)
	}
	if !o.SyncEnabled() || hooks != 0 {
		t.Error("failed save changed memory or ran hooks")
	}
}

func TestOptionsResetGroup(t *testing.T) {
	store := testkit.NewMemOptionsStore()
	o := NewOptions(domain.DefaultOptions(), store, nil, nil)
	var keys []string
	o.OnChange("", func(key string, _ domain.Options) { keys = append(keys, key) })
	ctx := context.Background()
	_ = o.Set(ctx, domain.IconKey(domain.StatusWorking), "🔥", 1)
	_ = o.Set(ctx, domain.IconKey(domain.StatusDone), "🧠", 1)
	keys = nil

	changed, err := o.Reset(ctx, domain.GroupAppearance, 1)
	if err != nil || !changed {
		t.Fatalf("Reset = %v, %v", changed, err)
	}
	if store.Saved() != 3 || o.StatusIcons() != domain.DefaultStatusIcons() {
		t.Errorf("after reset: saves=%d icons=%+v", store.Saved(), o.StatusIcons())
	}
	if len(keys) != 2 || keys[0] != "icons.working" || keys[1] != "icons.done" {
		t.Errorf("hooks after reset = %v", keys)
	}
	changed, err = o.Reset(ctx, domain.GroupAppearance, 1)
	if err != nil || changed || store.Saved() != 3 {
		t.Errorf("second reset: changed=%v err=%v saves=%d", changed, err, store.Saved())
	}
}

func TestOptionsConcurrentReads(t *testing.T) {
	o := NewOptions(domain.DefaultOptions(), nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.SyncEnabled()
				_ = o.StatusIcons()
			}
		}()
	}
	for j := 0; j < 50; j++ {
		v := "false"
		if j%2 == 1 {
			v = "true"
		}
		if err := o.Set(context.Background(), domain.OptionSyncEnabled, v, 1); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func itoa(n int) string { return string(rune('0' + n)) }
