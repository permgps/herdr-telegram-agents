package domain

import (
	"context"
	"time"
)

// ScreenSource selects which text `agent.read` returns.
type ScreenSource string

const (
	// ScreenVisible is what the user sees on the terminal right now.
	ScreenVisible ScreenSource = "visible"
	// ScreenRecent is the visible screen plus recent scrollback.
	ScreenRecent ScreenSource = "recent"
	// ScreenDetection is the region Herdr used to detect the agent status.
	ScreenDetection ScreenSource = "detection"
)

// Screen is a snapshot of an agent's terminal text.
type Screen struct {
	Text      string
	Revision  int64
	Truncated bool
}

// NotifySound selects the sound for a Herdr desktop notification.
type NotifySound string

const (
	NotifySoundNone    NotifySound = "none"
	NotifySoundDefault NotifySound = "default"
)

// HerdrGateway is the port to the Herdr socket API. Every call takes a
// context and returns ErrAgentGone when the target no longer exists.
type HerdrGateway interface {
	// ListAgents returns every agent Herdr currently tracks.
	ListAgents(ctx context.Context) ([]Agent, error)
	// ReadScreen returns up to lines of terminal text for the agent.
	ReadScreen(ctx context.Context, target string, source ScreenSource, lines int) (Screen, error)
	// Prompt types text into the agent and submits it.
	Prompt(ctx context.Context, target, text string) error
	// SendKeys sends raw key names such as "enter" or "escape".
	SendKeys(ctx context.Context, target string, keys []string) error
	// Rename sets the agent name; nil clears it back to the default.
	Rename(ctx context.Context, target string, name *string) error
	// Notify shows a desktop notification through Herdr.
	Notify(ctx context.Context, title, body string, sound NotifySound) error
	// Events streams HerdrEvent values until the gateway is closed.
	Events() <-chan Event
	// WatchPanes replaces the set of panes whose status changes are streamed.
	WatchPanes(ctx context.Context, paneIDs []string) error
}

// TelegramGateway is the port to the Telegram forum group.
type TelegramGateway interface {
	// CreateTopic creates a topic named for the label with the status icon.
	CreateTopic(ctx context.Context, name string, status Status) (Topic, error)
	// EditTopic applies a partial update; an empty patch is a no-op.
	EditTopic(ctx context.Context, threadID int, patch TopicPatch) error
	// CloseTopic closes the topic so it stays readable but rejects writes.
	CloseTopic(ctx context.Context, threadID int) error
	// ReopenTopic reopens a closed topic.
	ReopenTopic(ctx context.Context, threadID int) error
	// SendText posts text into the topic; code renders it as a code block.
	SendText(ctx context.Context, threadID int, text string, code bool) error
	// Rights reports whether the chat is a forum and the bot may manage
	// its topics and delete messages; the daemon checks it on start and
	// after RightsChanged.
	Rights(ctx context.Context) (Rights, error)
	// Events streams TopicMessage, TopicRenamed, TopicClosed, TopicReopened
	// and RightsChanged values until the gateway is closed.
	Events() <-chan Event
}

// Rights is the bot's standing in the configured chat.
type Rights struct {
	IsForum         bool
	IsAdmin         bool
	CanManageTopics bool
	// CanDeleteMessages lets the gateway remove the "changed the topic
	// icon" notices its own edits cause; without it they stay, nothing
	// else is affected.
	CanDeleteMessages bool
}

// ConfigStore persists the plugin configuration. Load returns
// ErrNotConfigured when nothing has been saved yet.
type ConfigStore interface {
	Load(ctx context.Context) (Config, error)
	Save(ctx context.Context, cfg Config) error
}

// MappingStore persists the agent-to-topic mapping. A missing file yields
// an empty mapping, not an error.
type MappingStore interface {
	Load(ctx context.Context) (*Mapping, error)
	Save(ctx context.Context, m *Mapping) error
}

// PaneOpener opens one of the plugin's manifest panes through Herdr.
type PaneOpener interface {
	OpenPane(ctx context.Context, pluginID, entrypoint string) error
}

// Clock abstracts time so the application layer is testable without sleeping.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}
