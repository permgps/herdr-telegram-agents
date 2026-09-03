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
	// RenameTab sets the label of the tab holding the agent; a tab always
	// has a label, so there is no clearing.
	RenameTab(ctx context.Context, tabID, label string) error
	// Focus brings the agent's pane to the front in Herdr.
	Focus(ctx context.Context, target string) error
	// Notify shows a desktop notification through Herdr.
	Notify(ctx context.Context, title, body string, sound NotifySound) error
	// Events streams HerdrEvent values until the gateway is closed.
	Events() <-chan Event
	// WatchPanes replaces the set of panes whose status changes are streamed.
	WatchPanes(ctx context.Context, paneIDs []string) error
}

// Button is one inline button under a bot message. Text is what the
// operator sees; Data comes back verbatim in ButtonPressed and must stay
// within Telegram's 64-byte limit.
type Button struct {
	Text string
	Data string
}

// Outgoing is one message for the forum group. ThreadID 0 addresses the
// General topic. Code renders the text as a code block; HTML sends it as
// ready HTML the caller has escaped (used for links). ReplyTo quotes the
// operator message with that id when non-zero. Notify sends the message
// with a sound; everything else is silent. Buttons, when set, are attached
// to the last message part as an inline keyboard, one button per row.
type Outgoing struct {
	ThreadID int
	Text     string
	Code     bool
	HTML     bool
	ReplyTo  int
	Notify   bool
	Buttons  []Button
}

// Document is one file for the forum group, sent silently as a single
// message. ThreadID 0 addresses the General topic. Name is the file name
// the operator sees; Caption is plain text under the file. ReplyTo quotes
// the operator message with that id when non-zero.
type Document struct {
	ThreadID int
	Name     string
	Data     []byte
	Caption  string
	ReplyTo  int
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
	// Send posts a message into a topic, or into General when ThreadID is
	// 0; long text is split into several messages. It returns the id of
	// the last message sent (the one carrying Buttons), 0 on error.
	Send(ctx context.Context, out Outgoing) (int, error)
	// EditButtons replaces the inline keyboard of a bot message; an empty
	// slice removes it.
	EditButtons(ctx context.Context, messageID int, buttons []Button) error
	// AnswerButton closes the spinner of a pressed button with a short
	// toast; callbackID comes from ButtonPressed.
	AnswerButton(ctx context.Context, callbackID, text string) error
	// SendDocument uploads one file into a topic, or into General when
	// ThreadID is 0, as a single silent message.
	SendDocument(ctx context.Context, doc Document) error
	// React puts one emoji reaction on the operator's message; threadID is
	// informational, Telegram addresses reactions by message id.
	React(ctx context.Context, threadID, messageID int, emoji string) error
	// Rights reports whether the chat is a forum and the bot may manage
	// its topics and delete messages; the daemon checks it on start and
	// after RightsChanged.
	Rights(ctx context.Context) (Rights, error)
	// Events streams TopicMessage, ButtonPressed, GeneralCommand,
	// TopicRenamed, TopicClosed, TopicReopened and RightsChanged values
	// until the gateway is closed.
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
