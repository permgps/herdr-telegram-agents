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
	Version     int
	BotToken    string
	BotUsername string
	ChatID      int64
	ChatTitle   string
	OperatorIDs []int64
	// ObserverIDs may read the group and use /status and /help in General
	// but never drive an agent; managed with /observers.
	ObserverIDs  []int64
	LogLevel     string
	ConfiguredAt time.Time
}

// Role is what a Telegram user may do with the bot.
type Role int

const (
	// RoleStranger is anyone not listed: every update is dropped.
	RoleStranger Role = iota
	// RoleObserver sees the group and may use /status and /help in
	// General; topic messages, button presses and other commands are
	// dropped.
	RoleObserver
	// RoleOperator drives the agents.
	RoleOperator
)

// String names the role for logs and replies.
func (r Role) String() string {
	switch r {
	case RoleOperator:
		return "operator"
	case RoleObserver:
		return "observer"
	}
	return "stranger"
}

// Role classifies a Telegram user id; an id listed as both is an operator.
func (c Config) Role(id int64) Role {
	switch {
	case c.IsOperator(id):
		return RoleOperator
	case c.IsObserver(id):
		return RoleObserver
	}
	return RoleStranger
}

// IsObserver reports whether the id is in ObserverIDs.
func (c Config) IsObserver(id int64) bool {
	for _, ob := range c.ObserverIDs {
		if ob == id {
			return true
		}
	}
	return false
}

// WithObserver returns a copy with id added to ObserverIDs. An id that is
// not positive, an operator or already an observer is ErrInvalidObserver;
// the receiver is never changed.
func (c Config) WithObserver(id int64) (Config, error) {
	switch {
	case id <= 0:
		return c, fmt.Errorf("%w: id must be a positive number", ErrInvalidObserver)
	case c.IsOperator(id):
		return c, fmt.Errorf("%w: %d is an operator", ErrInvalidObserver, id)
	case c.IsObserver(id):
		return c, fmt.Errorf("%w: %d is already an observer", ErrInvalidObserver, id)
	}
	next := c
	next.ObserverIDs = append(append([]int64(nil), c.ObserverIDs...), id)
	return next, nil
}

// WithoutObserver returns a copy with id removed from ObserverIDs; an id
// that is not an observer is ErrInvalidObserver. The receiver is never
// changed.
func (c Config) WithoutObserver(id int64) (Config, error) {
	if !c.IsObserver(id) {
		return c, fmt.Errorf("%w: %d is not an observer", ErrInvalidObserver, id)
	}
	next := c
	next.ObserverIDs = make([]int64, 0, len(c.ObserverIDs))
	for _, ob := range c.ObserverIDs {
		if ob != id {
			next.ObserverIDs = append(next.ObserverIDs, ob)
		}
	}
	if len(next.ObserverIDs) == 0 {
		next.ObserverIDs = nil
	}
	return next, nil
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
