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
	// ErrTopicMuted means an operator closed the topic by hand, so the
	// mirror skips it; the reconciler uses it to short-circuit and never
	// returns it to the daemon.
	ErrTopicMuted = errors.New("topic is muted")
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
	// ErrAlreadyRunning means another daemon holds the pid file.
	ErrAlreadyRunning = errors.New("daemon is already running")
	// ErrNotRunning means no daemon is running.
	ErrNotRunning = errors.New("daemon is not running")
	// ErrUnsupportedPlatform means the operation has no implementation on
	// this OS.
	ErrUnsupportedPlatform = errors.New("not supported on this platform")
	// ErrControlUnavailable means the daemon is not listening on its
	// control channel: it is not running, or it is an older build that
	// only understands signals.
	ErrControlUnavailable = errors.New("daemon control channel unavailable")
	// ErrUnknownOption means the option key is not in the registry.
	ErrUnknownOption = errors.New("unknown option")
	// ErrInvalidOption means the value does not fit the option's kind or
	// choice list.
	ErrInvalidOption = errors.New("invalid option value")
	// ErrDuplicateIcon means two statuses would share the same topic icon.
	ErrDuplicateIcon = errors.New("two statuses share an icon")
	// ErrIdleUnsupported means this platform has no source for the input
	// idle time, so presence cannot be measured automatically.
	ErrIdleUnsupported = errors.New("input idle time not available on this platform")
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
