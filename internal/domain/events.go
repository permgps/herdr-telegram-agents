package domain

// Event is a marker for everything that flows through the gateways' event
// channels. The unexported method keeps the set closed to this package.
type Event interface {
	isEvent()
}

// HerdrEventKind names the low-level Herdr socket events the plugin tracks.
type HerdrEventKind string

const (
	PaneAgentDetected      HerdrEventKind = "pane.agent_detected"
	PaneAgentStatusChanged HerdrEventKind = "pane.agent_status_changed"
	PaneClosed             HerdrEventKind = "pane.closed"
	PaneExited             HerdrEventKind = "pane.exited"
	PaneUpdated            HerdrEventKind = "pane.updated"
	TabRenamed             HerdrEventKind = "tab.renamed"
	// StreamReset is synthesised by the adapter when the subscription
	// connection drops and is re-established; consumers should reconcile.
	StreamReset HerdrEventKind = "stream.reset"
)

// HerdrEvent is one event from the Herdr socket, translated to domain types
// but otherwise kept close to the wire: the application layer decides what a
// sequence of them means.
//
// Which fields are set depends on Kind:
//   - PaneAgentDetected, PaneAgentStatusChanged, PaneUpdated: Agent (and
//     PaneID, WorkspaceID, TabID copied from it).
//   - PaneClosed: PaneID, WorkspaceID, TabID.
//   - PaneExited: PaneID, WorkspaceID, TabID, FinalStatus, Released.
//   - TabRenamed: TabID, Label.
//   - StreamReset: nothing.
type HerdrEvent struct {
	Kind        HerdrEventKind
	PaneID      string
	WorkspaceID string
	TabID       string
	Agent       *Agent
	Label       string
	FinalStatus *Status
	Released    bool
}

// TopicMessage is a text message written by an operator inside a topic.
type TopicMessage struct {
	ThreadID int
	FromID   int64
	Text     string
}

// TopicRenamed is emitted when someone renames a topic in Telegram.
type TopicRenamed struct {
	ThreadID int
	Name     string
}

// TopicClosed is emitted when someone closes a topic in Telegram.
type TopicClosed struct {
	ThreadID int
}

// TopicReopened is emitted when someone reopens a topic in Telegram.
type TopicReopened struct {
	ThreadID int
}

// RightsChanged is emitted when the bot's membership in the group changes.
type RightsChanged struct {
	CanManageTopics bool
}

func (HerdrEvent) isEvent()    {}
func (TopicMessage) isEvent()  {}
func (TopicRenamed) isEvent()  {}
func (TopicClosed) isEvent()   {}
func (TopicReopened) isEvent() {}
func (RightsChanged) isEvent() {}
