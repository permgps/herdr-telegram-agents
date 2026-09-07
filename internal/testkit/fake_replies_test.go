package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// TestFakeRepliesWrittenFollowsClock guards the reply's default Written
// against the wall clock: a fixture that starts turns on a fake clock must
// see replies written on that same clock, or a stale check would compare
// two unrelated times.
func TestFakeRepliesWrittenFollowsClock(t *testing.T) {
	future := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	f := NewFakeReplies()
	f.SetNow(func() time.Time { return future })
	key := domain.Key{PaneID: "p1", TerminalID: "t1"}
	f.Set(key, "hi")
	r, err := f.LastReply(context.Background(), domain.Agent{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Written.Equal(future.Add(-time.Second)) || r.Age != time.Second || !r.Meta.Started.IsZero() {
		t.Errorf("reply = %+v", r)
	}
	// SetMeta pins Written and the meta regardless of the clock.
	f.SetMeta(key, domain.TurnMeta{Model: "m"}, future.Add(time.Hour))
	r, _ = f.LastReply(context.Background(), domain.Agent{Key: key})
	if !r.Written.Equal(future.Add(time.Hour)) || r.Meta.Model != "m" {
		t.Errorf("reply after SetMeta = %+v", r)
	}
}
