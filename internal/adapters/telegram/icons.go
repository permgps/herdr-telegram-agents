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
// topic icon (empty when the icon pack lacks the configured emoji, in which
// case an edit leaves the icon unchanged) and the icon_color fallback
// Telegram only honours at creation time.
type Icon struct {
	EmojiID string
	Color   int
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

// IconSet resolves emoji to topic-icon ids from one
// getForumTopicIconStickers result and remembers the pack order for the
// options panel. The zero value has no emoji ids and only colors.
type IconSet struct {
	byEmoji map[string]string
	order   []string
}

// NewIconSet indexes stickers by emoji; the first sticker per emoji wins
// and Emojis keeps the pack order, each emoji spelled as the pack spells it.
func NewIconSet(stickers []*models.Sticker) IconSet {
	m := make(map[string]string, len(stickers))
	order := make([]string, 0, len(stickers))
	for _, s := range stickers {
		if s == nil || s.CustomEmojiID == "" || s.Emoji == "" {
			continue
		}
		key := emojiKey(s.Emoji)
		if _, dup := m[key]; !dup {
			m[key] = s.CustomEmojiID
			order = append(order, s.Emoji)
		}
	}
	return IconSet{byEmoji: m, order: order}
}

// Emojis lists the pack's emoji in Telegram's order; nil for the zero set.
func (s IconSet) Emojis() []string {
	if len(s.order) == 0 {
		return nil
	}
	return append([]string(nil), s.order...)
}

// ID resolves one emoji (variation selector ignored) to its sticker id.
func (s IconSet) ID(emoji string) (string, bool) {
	id, ok := s.byEmoji[emojiKey(emoji)]
	return id, ok
}

// ForEmoji returns the icon for an emoji: its sticker id when the pack has
// it, else only the colour of the status (used at creation, ignored by an
// edit). Unknown statuses get the unknown colour.
func (s IconSet) ForEmoji(emoji string, status domain.Status) Icon {
	color, ok := statusColor[status]
	if !ok {
		color = statusColor[domain.StatusUnknown]
	}
	icon := Icon{Color: color}
	if id, ok := s.ID(emoji); ok {
		icon.EmojiID = id
	}
	return icon
}

// iconSource is the one Bot API method LoadIcons needs; *bot.Bot satisfies
// it and tests can supply a fake without HTTP.
type iconSource interface {
	GetForumTopicIconStickers(ctx context.Context) ([]*models.Sticker, error)
}

// LoadIcons fetches the topic-icon pack once and logs which of the default
// status icons the pack has. The gateway caches the result for the life of
// the process; the operator's own choice is applied later through
// SetStatusIcons.
func LoadIcons(ctx context.Context, api iconSource, log *slog.Logger) (IconSet, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	stickers, err := api.GetForumTopicIconStickers(ctx)
	if err != nil {
		return IconSet{}, fmt.Errorf("getForumTopicIconStickers: %w", translate(err))
	}
	set := NewIconSet(stickers)
	defaults := domain.DefaultStatusIcons()
	var with, without []string
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if _, ok := set.ID(defaults.For(st)); ok {
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
