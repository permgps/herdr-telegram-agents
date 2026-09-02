package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Icon is what the adapter sends for a status: the custom emoji id of a
// topic icon (empty when the icon pack has none of the preferred emoji, in
// which case an edit leaves the icon unchanged) and the icon_color fallback
// Telegram only honours at creation time.
type Icon struct {
	EmojiID string
	Color   int
}

// preferredEmoji lists, per status, the sticker emoji to look for in the
// free topic-icon pack; the first one present wins. Checked against the
// live pack on 2026-09-02 (112 stickers): the first choice of every status
// exists there. Emoji are compared without the U+FE0F presentation
// selector, which the pack uses inconsistently ("⚡️" but "✅").
var preferredEmoji = map[domain.Status][]string{
	domain.StatusWorking: {"⚡", "🔥", "🚀"},
	domain.StatusIdle:    {"✅", "☕", "⛅"}, // the green check Herdr shows for idle
	domain.StatusBlocked: {"❓", "❗", "⁉"},
	domain.StatusDone:    {"🏆", "🎉", "🎖"},
	domain.StatusUnknown: {"👀", "🔮", "🤖"},
	domain.StatusExited:  {"🏁", "⛔", "🔚"},
}

// emojiKey normalises an emoji for lookups by dropping variation selectors.
func emojiKey(e string) string {
	return strings.ReplaceAll(e, "\uFE0F", "")
}

// statusColor is the icon_color per status, from the six values Telegram
// accepts for createForumTopic.
var statusColor = map[domain.Status]int{
	domain.StatusWorking: 7322096,  // 0x6FB9F0 blue
	domain.StatusIdle:    16766590, // 0xFFD67E yellow
	domain.StatusBlocked: 16478047, // 0xFB6F5F red
	domain.StatusDone:    9367192,  // 0x8EEE98 green
	domain.StatusUnknown: 13338331, // 0xCB86DB purple
	domain.StatusExited:  13338331, // 0xCB86DB purple
}

// IconSet resolves statuses to icons from one getForumTopicIconStickers
// result. The zero value has no emoji ids and only colors.
type IconSet struct {
	byEmoji map[string]string
}

// NewIconSet indexes stickers by emoji; the first sticker per emoji wins.
func NewIconSet(stickers []*models.Sticker) IconSet {
	m := make(map[string]string, len(stickers))
	for _, s := range stickers {
		if s == nil || s.CustomEmojiID == "" || s.Emoji == "" {
			continue
		}
		key := emojiKey(s.Emoji)
		if _, dup := m[key]; !dup {
			m[key] = s.CustomEmojiID
		}
	}
	return IconSet{byEmoji: m}
}

// For returns the icon for a status. Unknown statuses get the unknown color.
func (s IconSet) For(status domain.Status) Icon {
	color, ok := statusColor[status]
	if !ok {
		color = statusColor[domain.StatusUnknown]
	}
	icon := Icon{Color: color}
	for _, e := range preferredEmoji[status] {
		if id, ok := s.byEmoji[emojiKey(e)]; ok {
			icon.EmojiID = id
			break
		}
	}
	return icon
}

// iconSource is the one Bot API method LoadIcons needs; *bot.Bot satisfies
// it and tests can supply a fake without HTTP.
type iconSource interface {
	GetForumTopicIconStickers(ctx context.Context) ([]*models.Sticker, error)
}

// LoadIcons fetches the topic-icon pack once and logs which statuses got an
// icon id. The gateway caches the result for the life of the process.
func LoadIcons(ctx context.Context, api iconSource, log *slog.Logger) (IconSet, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	stickers, err := api.GetForumTopicIconStickers(ctx)
	if err != nil {
		return IconSet{}, fmt.Errorf("getForumTopicIconStickers: %w", translate(err))
	}
	set := NewIconSet(stickers)
	var with, without []string
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if set.For(st).EmojiID != "" {
			with = append(with, string(st))
		} else {
			without = append(without, string(st))
		}
	}
	log.Info("telegram topic icons loaded",
		slog.Int("stickers", len(stickers)),
		slog.Int("emoji", len(set.byEmoji)),
		slog.Any("with_icon", with),
		slog.Any("without_icon", without))
	return set, nil
}
