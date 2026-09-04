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
	WorkspaceLabel string
	TabID          string
	TabLabel       string
	Kind           string
	Name           string
	Title          string
	Status         Status
	Revision       int64
	StateChangeSeq int64
	Focused        bool
	// Cwd is the working directory Herdr reports for the pane; the reply
	// source uses it to find the agent's own transcript. Empty when Herdr
	// does not know it.
	Cwd string
}

// labelSeparator joins the workspace and the agent part of a label, the
// way Herdr's Agents panel does.
const labelSeparator = " · "

// Label is the topic name for the agent, mirroring the row Herdr shows in
// its Agents panel: "<workspace> · <agent>". The workspace part is the
// workspace label, else its id. The agent part is the custom agent name,
// else the tab label, else the agent kind. Missing parts are dropped, so a
// bare agent name or a bare workspace is still a usable label. The terminal
// title is deliberately not used: agents rewrite it for every task and a
// topic name should stay put.
func (a Agent) Label() string {
	ws := a.WorkspaceLabel
	if ws == "" {
		ws = a.WorkspaceID
	}
	who := a.Name
	if who == "" {
		who = a.TabLabel
	}
	if who == "" {
		who = a.Kind
	}
	switch {
	case ws == "":
		return who
	case who == "":
		return ws
	}
	return ws + labelSeparator + who
}

// Workspace is one Herdr workspace as /new sees it: the id the socket
// wants and the label the operator types.
type Workspace struct {
	ID    string
	Label string
}

// Tab is one Herdr tab as CreateTab returns it: the ids the socket wants,
// the label Herdr chose and the root pane an agent may be started in.
type Tab struct {
	ID          string
	WorkspaceID string
	Label       string
	RootPaneID  string
}
