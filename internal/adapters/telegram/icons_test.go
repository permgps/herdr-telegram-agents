package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func sticker(emoji, id string) *models.Sticker {
	return &models.Sticker{Emoji: emoji, CustomEmojiID: id}
}

func TestIconSetFor(t *testing.T) {
	set := NewIconSet([]*models.Sticker{
		sticker("🔥", "fire1"),
		sticker("⚡", "bolt1"),
		sticker("⚡", "bolt2"),  // duplicate emoji: first wins
		sticker("💤", ""),       // no id: ignored
		sticker("", "noemoji"), // no emoji: ignored
		nil,
		sticker("🏁", "flag1"),
		sticker("❔", "q1"),
		sticker("☕️", "coffee"), // with U+FE0F, as the live pack sends it
		sticker("👀", "eyes"),
		sticker("🏆", "cup"),
	})

	tests := []struct {
		status domain.Status
		emoji  string
		color  int
	}{
		{domain.StatusWorking, "bolt1", 7322096},
		{domain.StatusIdle, "coffee", 16766590},
		{domain.StatusBlocked, "", 16478047},
		{domain.StatusDone, "cup", 9367192},
		{domain.StatusUnknown, "eyes", 13338331},
		{domain.StatusExited, "flag1", 13338331},
		{domain.Status("weird"), "", 13338331},
	}
	for _, tc := range tests {
		got := set.For(tc.status)
		if got.EmojiID != tc.emoji || got.Color != tc.color {
			t.Errorf("For(%s) = %+v, want emoji %q color %d", tc.status, got, tc.emoji, tc.color)
		}
	}
}

func TestEveryStatusHasColor(t *testing.T) {
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if (IconSet{}).For(st).Color == 0 {
			t.Errorf("%s has no color", st)
		}
		if len(preferredEmoji[st]) == 0 {
			t.Errorf("%s has no preferred emoji", st)
		}
	}
}

type fakeIconSource struct {
	stickers []*models.Sticker
	err      error
	calls    int
}

func (f *fakeIconSource) GetForumTopicIconStickers(context.Context) ([]*models.Sticker, error) {
	f.calls++
	return f.stickers, f.err
}

func TestLoadIcons(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src := &fakeIconSource{stickers: []*models.Sticker{sticker("🏆", "ok1"), sticker("🚀", "rocket")}}

	set, err := LoadIcons(context.Background(), src, log)
	if err != nil {
		t.Fatal(err)
	}
	if src.calls != 1 {
		t.Errorf("calls = %d, want 1", src.calls)
	}
	if got := set.For(domain.StatusDone).EmojiID; got != "ok1" {
		t.Errorf("done icon = %q", got)
	}
	if got := set.For(domain.StatusWorking).EmojiID; got != "rocket" {
		t.Errorf("working icon = %q", got)
	}
	out := buf.String()
	if !strings.Contains(out, "telegram topic icons loaded") || !strings.Contains(out, "stickers=2") {
		t.Errorf("missing info log: %s", out)
	}
	if !strings.Contains(out, "with_icon=\"[working done]\"") || !strings.Contains(out, "without_icon=\"[idle blocked unknown exited]\"") {
		t.Errorf("status lists not logged: %s", out)
	}

	boom := errors.New("error do request for method getForumTopicIconStickers, boom")
	src = &fakeIconSource{err: boom}
	if _, err := LoadIcons(context.Background(), src, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}
