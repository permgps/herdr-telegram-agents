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
	// ListWorkspaces returns every workspace Herdr shows, with its label.
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	// CreateTab creates an unfocused tab in the workspace with Herdr's
	// default cwd and label and returns it with its root pane.
	CreateTab(ctx context.Context, workspaceID string) (Tab, error)
	// StartAgent starts an agent of kind in a pane sitting at its shell
	// prompt and waits up to timeout (clamped into StartAgentMinTimeout ..
	// StartAgentMaxTimeout) for it to be ready; the returned Agent carries
	// the pane and tab ids.
	StartAgent(ctx context.Context, name, kind, paneID string, timeout time.Duration) (Agent, error)
	// ClosePane closes the pane; a pane that is already gone is
	// ErrAgentGone.
	ClosePane(ctx context.Context, paneID string) error
}

// Bounds of the StartAgent timeout; Herdr's agent.start accepts
// timeout_ms between 3001 and 300000.
const (
	StartAgentMinTimeout = 3001 * time.Millisecond
	StartAgentMaxTimeout = 300 * time.Second
)

// Reply is an agent's last message taken from its own transcript. Text is
// the raw Markdown as the agent wrote it. Source names the file it came
// from, for logs only. Age is how long ago that file was last written.
type Reply struct {
	Text   string
	Source string
	Age    time.Duration
}

// ReplySource finds the last reply of an agent outside Herdr, in the
// agent's own session transcript. It returns ErrNoReply, wrapped with the
// reason, whenever nothing usable exists; the caller then falls back to
// the screen.
type ReplySource interface {
	LastReply(ctx context.Context, agent Agent) (Reply, error)
}

// Button is one inline button under a bot message. Text is what the
// operator sees; Data comes back verbatim in ButtonPressed and must stay
// within Telegram's 64-byte limit. Buttons with the same non-zero Row share
// one keyboard row; Row 0 means "a row of its own", which keeps the
// one-button-per-row callers unchanged.
type Button struct {
	Text string
	Data string
	Row  int
}

// Outgoing is one message for the forum group. ThreadID 0 addresses the
// General topic. Code renders the text as a code block; HTML sends it as
// ready HTML the caller has escaped (used for links); Markdown is raw
// Markdown the adapter splits on fence boundaries and renders to Telegram
// HTML. The three are exclusive; when several are set Code wins, then
// HTML. MaxParts caps how many message parts a long text becomes (0 =
// unlimited); the last kept part ends with a trailer naming the dropped
// characters. ReplyTo quotes the operator message with that id when
// non-zero. Notify sends the message with a sound; everything else is
// silent. Buttons, when set, are attached to the last message part as an
// inline keyboard, one button per row.
type Outgoing struct {
	ThreadID int
	Text     string
	Code     bool
	HTML     bool
	Markdown bool
	MaxParts int
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
	// DeleteTopic deletes the topic with all its messages; needs the
	// can_delete_messages right. A topic that is already gone is
	// ErrTopicGone.
	DeleteTopic(ctx context.Context, threadID int) error
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
	// EditText replaces the text and the inline keyboard of a bot message
	// in one call; an empty buttons slice removes the keyboard. Editing to
	// the same content is a success.
	EditText(ctx context.Context, messageID int, text string, html bool, buttons []Button) error
	// SetStatusIcons replaces the emoji used for topic icons from now on;
	// safe to call from any goroutine. The default table is in force until
	// the first call.
	SetStatusIcons(icons StatusIcons)
	// IconPack lists the emoji of the free topic-icon pack in Telegram's
	// order, cached at connect; nil when the pack is unknown.
	IconPack() []string
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

// OptionsStore persists the operator-editable options. A missing file
// yields DefaultOptions, not an error.
type OptionsStore interface {
	Load(ctx context.Context) (Options, error)
	Save(ctx context.Context, o Options) error
}

// MappingStore persists the agent-to-topic mapping. A missing file yields
// an empty mapping, not an error.
type MappingStore interface {
	Load(ctx context.Context) (*Mapping, error)
	Save(ctx context.Context, m *Mapping) error
}

// GroupInfo is what the doctor learns about the configured group.
type GroupInfo struct {
	Title  string
	Rights Rights
}

// TelegramInspector is the light Telegram client of the doctor and
// send-test actions: single calls with no polling and no webhook change,
// so it never disturbs a running daemon.
type TelegramInspector interface {
	// Identity is getMe.
	Identity(ctx context.Context) (BotIdentity, error)
	// Group reads the chat title, whether it is a forum and the bot's
	// rights in it.
	Group(ctx context.Context) (GroupInfo, error)
	// SendTest posts text into General with a notification and returns
	// the message id.
	SendTest(ctx context.Context, text string) (int, error)
}

// HerdrInfo is what the Herdr socket answers to a ping.
type HerdrInfo struct {
	Version  string
	Protocol int
}

// HerdrProber pings the Herdr socket without starting an event stream.
type HerdrProber interface {
	Ping(ctx context.Context) (HerdrInfo, error)
}

// PaneOpener opens one of the plugin's manifest panes through Herdr.
type PaneOpener interface {
	OpenPane(ctx context.Context, pluginID, entrypoint string) error
}

// IdleSource reports how long the machine's keyboard and mouse have been
// untouched; presence tracking calls it every few seconds. A platform
// without a source returns ErrIdleUnsupported on every call.
type IdleSource interface {
	Idle(ctx context.Context) (time.Duration, error)
}

// Clock abstracts time so the application layer is testable without sleeping.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}
