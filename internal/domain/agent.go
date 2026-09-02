package domain

// Key identifies an agent for the lifetime of the plugin state.
//
// PaneID alone is not enough: Herdr never reuses a closed pane id, but a new
// agent can start in the same pane after the previous one exits. TerminalID
// changes in that case, so the pair distinguishes the two agents.
type Key struct {
	PaneID     string
	TerminalID string
}

// String renders the key as "<pane>/<terminal>" for logs and state files.
func (k Key) String() string {
	return k.PaneID + "/" + k.TerminalID
}

// Agent is the plugin's view of one Herdr agent.
//
// Title is the pane title as reported by Herdr with the spinner already
// stripped by the adapter, so the domain treats it as clean text.
type Agent struct {
	Key
	WorkspaceID    string
	TabID          string
	Kind           string
	Name           string
	Title          string
	Status         Status
	Revision       int64
	StateChangeSeq int64
	Focused        bool
}

// Label is the human-readable name used for the topic. The explicit agent
// name wins, then the pane title, then a synthetic "<kind>@<workspace>" so a
// topic always has something recognisable in it.
func (a Agent) Label() string {
	if a.Name != "" {
		return a.Name
	}
	if a.Title != "" {
		return a.Title
	}
	return a.Kind + "@" + a.WorkspaceID
}
