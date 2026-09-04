// Package herdr adapts the Herdr socket API (newline-delimited JSON,
// protocol 17) to the domain.HerdrGateway port.
package herdr

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// protocolVersion is the socket protocol this adapter was written against.
const protocolVersion = 17

// ProtocolVersion is protocolVersion for the doctor, which reports a
// mismatch as a warning.
const ProtocolVersion = protocolVersion

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type eventEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// agentInfo mirrors the AgentInfo schema of protocol 17. Optional fields
// are pointers or zero values; the adapter never relies on their presence.
type agentInfo struct {
	PaneID                 string  `json:"pane_id"`
	WorkspaceID            string  `json:"workspace_id"`
	TabID                  string  `json:"tab_id"`
	TerminalID             string  `json:"terminal_id"`
	Agent                  string  `json:"agent"`
	AgentStatus            string  `json:"agent_status"`
	Name                   *string `json:"name"`
	DisplayAgent           string  `json:"display_agent"`
	Title                  string  `json:"title"`
	TerminalTitle          string  `json:"terminal_title"`
	TerminalTitleStripped  string  `json:"terminal_title_stripped"`
	Cwd                    string  `json:"cwd"`
	ForegroundCwd          string  `json:"foreground_cwd"`
	Focused                bool    `json:"focused"`
	Revision               int64   `json:"revision"`
	StateChangeSeq         int64   `json:"state_change_seq"`
	InteractiveReady       bool    `json:"interactive_ready"`
	LaunchPending          bool    `json:"launch_pending"`
	ScreenDetectionSkipped bool    `json:"screen_detection_skipped"`
}

type pongResult struct {
	Type     string `json:"type"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

type agentListResult struct {
	Type   string      `json:"type"`
	Agents []agentInfo `json:"agents"`
}

// workspaceInfo is the slice of WorkspaceInfo the plugin reads: the label
// shown in Herdr's sidebar.
type workspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type workspaceListResult struct {
	Type       string          `json:"type"`
	Workspaces []workspaceInfo `json:"workspaces"`
}

// tabInfo is the slice of TabInfo the plugin reads.
type tabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type tabListResult struct {
	Type string    `json:"type"`
	Tabs []tabInfo `json:"tabs"`
}

type workspaceRenamedData struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

// paneReadResult is the `pane_read` result of agent.read.
type paneReadResult struct {
	Type string `json:"type"`
	Read struct {
		PaneID      string `json:"pane_id"`
		WorkspaceID string `json:"workspace_id"`
		TabID       string `json:"tab_id"`
		Source      string `json:"source"`
		Format      string `json:"format"`
		Text        string `json:"text"`
		Revision    int64  `json:"revision"`
		Truncated   bool   `json:"truncated"`
	} `json:"read"`
}

// Request params for the agent and notification methods.
type readParams struct {
	Target    string `json:"target"`
	Source    string `json:"source"`
	Lines     int    `json:"lines,omitempty"`
	Format    string `json:"format"`
	StripANSI bool   `json:"strip_ansi"`
}

type promptParams struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

type sendKeysParams struct {
	Target string   `json:"target"`
	Keys   []string `json:"keys"`
}

type focusParams struct {
	Target string `json:"target"`
}

// renameParams always carries name so a nil pointer serialises as null,
// which is how Herdr clears a custom name.
type renameParams struct {
	Target string  `json:"target"`
	Name   *string `json:"name"`
}

type tabRenameParams struct {
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

// tabCreateParams is tab.create with Herdr's defaults for cwd and label;
// focus is always sent so a tab opened from the phone never steals the
// desktop's focus.
type tabCreateParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Focus       bool   `json:"focus"`
}

// paneInfo is the slice of PaneInfo the plugin reads from tab_created.
type paneInfo struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
}

type tabCreatedResult struct {
	Type     string   `json:"type"`
	Tab      tabInfo  `json:"tab"`
	RootPane paneInfo `json:"root_pane"`
}

type agentStartParams struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	PaneID    string `json:"pane_id"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type agentStartedResult struct {
	Type  string    `json:"type"`
	Agent agentInfo `json:"agent"`
	Argv  []string  `json:"argv"`
}

type paneCloseParams struct {
	PaneID string `json:"pane_id"`
}

type notifyParams struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

// subscription is one entry of events.subscribe params.
type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id,omitempty"`
}

type subscribeParams struct {
	Subscriptions []subscription `json:"subscriptions"`
}

// Event data shapes. The envelope's `event` field uses snake_case kinds
// (`pane_agent_detected`) even though subscriptions use dotted ones.
type paneAgentDetectedData struct {
	PaneID      string  `json:"pane_id"`
	WorkspaceID string  `json:"workspace_id"`
	Agent       *string `json:"agent"`
	Released    bool    `json:"released"`
	FinalStatus *string `json:"final_status"`
}

type paneAgentStatusChangedData struct {
	PaneID       string `json:"pane_id"`
	WorkspaceID  string `json:"workspace_id"`
	Agent        string `json:"agent"`
	AgentStatus  string `json:"agent_status"`
	DisplayAgent string `json:"display_agent"`
	Title        string `json:"title"`
}

type paneClosedData struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
}

type paneExitedData struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
}

type paneUpdatedData struct {
	Pane agentInfo `json:"pane"`
}

type tabRenamedData struct {
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

// toDomainAgent converts wire agent info to the domain model. Title is
// cleaned here so the domain can treat it as plain text.
func toDomainAgent(a agentInfo) domain.Agent {
	name := ""
	if a.Name != nil {
		name = strings.TrimSpace(*a.Name)
	}
	return domain.Agent{
		Key:            domain.Key{PaneID: a.PaneID, TerminalID: a.TerminalID},
		WorkspaceID:    a.WorkspaceID,
		TabID:          a.TabID,
		Kind:           a.Agent,
		Name:           name,
		Title:          pickTitle(a),
		Status:         domain.ParseStatus(a.AgentStatus),
		Revision:       a.Revision,
		StateChangeSeq: a.StateChangeSeq,
		Focused:        a.Focused,
		Cwd:            a.Cwd,
	}
}

// pickTitle prefers the metadata title, then Herdr's own stripped terminal
// title, then the raw terminal title with the spinner glyph removed.
func pickTitle(a agentInfo) string {
	if t := cleanTitle(a.Title); t != "" {
		return t
	}
	if t := cleanTitle(a.TerminalTitleStripped); t != "" {
		return t
	}
	return cleanTitle(a.TerminalTitle)
}

// cleanTitle drops leading symbol runes (the spinner glyphs agents put in
// the terminal title, such as "✳" or "⠋") and surrounding whitespace.
func cleanTitle(s string) string {
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.So, r) || unicode.Is(unicode.Sm, r) ||
			unicode.Is(unicode.Mn, r) || r == '️'
	})
	return strings.TrimSpace(s)
}
