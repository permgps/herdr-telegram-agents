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

func TestIconSetForEmoji(t *testing.T) {
	set := NewIconSet([]*models.Sticker{
		sticker("🔥", "fire1"),
		sticker("⚡", "bolt1"),
		sticker("⚡", "bolt2"),  // duplicate emoji: first wins
		sticker("💤", ""),       // no id: ignored
		sticker("", "noemoji"), // no emoji: ignored
		nil,
		sticker("🏁", "flag1"),
		sticker("☕️", "coffee"), // with U+FE0F, as the live pack sends it
		sticker("👀", "eyes"),
	})

	tests := []struct {
		emoji  string
		status domain.Status
		id     string
		color  int
	}{
		{"⚡", domain.StatusWorking, "bolt1", 7322096},
		{"⚡️", domain.StatusWorking, "bolt1", 7322096}, // selector ignored on lookup
		{"☕", domain.StatusIdle, "coffee", 16766590},   // selector ignored in the pack
		{"❓", domain.StatusBlocked, "", 16478047},      // not in the pack: colour only
		{"👀", domain.StatusUnknown, "eyes", 13338331},
		{"🏁", domain.StatusExited, "flag1", 13338331},
		{"🔥", domain.Status("weird"), "fire1", 13338331},
	}
	for _, tc := range tests {
		got := set.ForEmoji(tc.emoji, tc.status)
		if got.EmojiID != tc.id || got.Color != tc.color {
			t.Errorf("ForEmoji(%s, %s) = %+v, want id %q color %d", tc.emoji, tc.status, got, tc.id, tc.color)
		}
	}
	if want := []string{"🔥", "⚡", "🏁", "☕️", "👀"}; strings.Join(set.Emojis(), "") != strings.Join(want, "") {
		t.Errorf("Emojis = %q, want pack order %q", set.Emojis(), want)
	}
	if (IconSet{}).Emojis() != nil {
		t.Error("zero set lists emoji")
	}
}

func TestEveryStatusHasColorAndDefaultIcon(t *testing.T) {
	defaults := domain.DefaultStatusIcons()
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if (IconSet{}).ForEmoji(defaults.For(st), st).Color == 0 {
			t.Errorf("%s has no color", st)
		}
		if defaults.For(st) == "" {
			t.Errorf("%s has no default emoji", st)
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
	if got := set.ForEmoji("🏆", domain.StatusDone).EmojiID; got != "ok1" {
		t.Errorf("done icon = %q", got)
	}
	if got := set.ForEmoji("⚡", domain.StatusWorking).EmojiID; got != "" {
		t.Errorf("working icon = %q, want none (no fallback to 🚀 any more)", got)
	}
	out := buf.String()
	if !strings.Contains(out, "telegram topic icons loaded") || !strings.Contains(out, "stickers=2") {
		t.Errorf("missing info log: %s", out)
	}
	if !strings.Contains(out, "with_icon=[done]") || !strings.Contains(out, "without_icon=\"[working idle blocked unknown exited]\"") {
		t.Errorf("status lists not logged: %s", out)
	}

	boom := errors.New("error do request for method getForumTopicIconStickers, boom")
	src = &fakeIconSource{err: boom}
	if _, err := LoadIcons(context.Background(), src, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}
