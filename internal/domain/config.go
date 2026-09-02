package domain

import (
	"fmt"
	"time"
)

// ConfigVersion is the schema version written to config.json. A file with
// a different version is refused so a newer or older binary never
// misreads fields silently.
const ConfigVersion = 1

// Config is everything the daemon needs to talk to one Telegram forum group.
// It is written once by the setup wizard and read on every start.
type Config struct {
	Version      int
	BotToken     string
	BotUsername  string
	ChatID       int64
	ChatTitle    string
	OperatorIDs  []int64
	LogLevel     string
	ConfiguredAt time.Time
}

// Validate reports the first field that makes the config unusable. Every
// failure wraps ErrNotConfigured so callers can treat "missing" and
// "broken" the same way: run the setup wizard.
func (c Config) Validate() error {
	switch {
	case c.Version != ConfigVersion:
		return fmt.Errorf("%w: version %d, want %d", ErrNotConfigured, c.Version, ConfigVersion)
	case c.BotToken == "":
		return fmt.Errorf("%w: bot_token is empty", ErrNotConfigured)
	case c.ChatID >= 0:
		return fmt.Errorf("%w: chat_id %d is not a supergroup id", ErrNotConfigured, c.ChatID)
	case len(c.OperatorIDs) == 0:
		return fmt.Errorf("%w: operator_ids is empty", ErrNotConfigured)
	}
	return nil
}

// IsOperator reports whether the Telegram user id may drive the bot.
func (c Config) IsOperator(id int64) bool {
	for _, op := range c.OperatorIDs {
		if op == id {
			return true
		}
	}
	return false
}
