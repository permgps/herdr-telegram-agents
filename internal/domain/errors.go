package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors shared by the application layer and the adapters. Adapters
// translate their transport-specific failures into these so the application
// can branch with errors.Is without importing any library.
var (
	// ErrNotConfigured means the plugin has no usable configuration yet.
	ErrNotConfigured = errors.New("plugin is not configured")
	// ErrAgentGone means Herdr no longer knows the targeted agent or pane.
	ErrAgentGone = errors.New("agent is gone")
	// ErrTopicGone means the Telegram topic was deleted.
	ErrTopicGone = errors.New("topic is gone")
	// ErrTopicClosed means the Telegram topic is closed and rejects writes.
	ErrTopicClosed = errors.New("topic is closed")
	// ErrForbidden means the bot was removed from the chat or lost rights.
	ErrForbidden = errors.New("bot is forbidden in chat")
	// ErrBotUnauthorized means the bot token is invalid or revoked.
	ErrBotUnauthorized = errors.New("bot token is unauthorized")
	// ErrPollerConflict means another process is polling the same bot.
	ErrPollerConflict = errors.New("another poller owns this bot")
	// ErrChatMigrated means the chat id changed; see ChatMigratedError.
	ErrChatMigrated = errors.New("chat migrated to a new id")
	// ErrDisconnected means the Herdr socket connection was lost.
	ErrDisconnected = errors.New("disconnected from herdr")
)

// ChatMigratedError carries the replacement chat id when Telegram reports a
// migration. It matches ErrChatMigrated under errors.Is.
type ChatMigratedError struct {
	NewChatID int64
}

func (e *ChatMigratedError) Error() string {
	return fmt.Sprintf("chat migrated to %d", e.NewChatID)
}

// Is makes errors.Is(err, ErrChatMigrated) true for any ChatMigratedError.
func (e *ChatMigratedError) Is(target error) bool {
	return target == ErrChatMigrated
}
