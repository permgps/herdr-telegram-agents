package herdr

import "fmt"

// codeNotFound is the server error code for a missing agent, pane or
// workspace target; the gateway maps it to domain.ErrAgentGone.
const codeNotFound = "not_found"

// codeNotIdle is returned when alternate-screen history is requested while
// an agent is working.
const codeNotIdle = "agent_not_idle"

// codeBlocked is Herdr's refusal of agent.prompt for an agent waiting at a
// dialog (0.9.3: "If the agent is already blocked, submission is rejected
// with agent_blocked before any input is sent"); the gateway maps it to
// domain.ErrAgentBlocked.
const codeBlocked = "agent_blocked"

// codeNotDriven is Herdr's refusal of the agent input methods for an agent
// it does not drive itself: one that reports its own state instead of
// using an official integration, such as Crush (0.9.3: "agent w1:p1 is not
// an active named agent"). Reads and waits work there, so the pane is alive
// and takes input; the gateway retires the refusal through pane.send_text
// and pane.send_keys. Nothing has reached the pane when it arrives.
const codeNotDriven = "agent_not_ready"

// APIError is an error line returned by the Herdr server for a request.
// Codes are Herdr's snake_case identifiers such as "not_found".
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("herdr api %s: %s", e.Code, e.Message)
}
