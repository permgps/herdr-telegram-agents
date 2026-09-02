package herdr

import "fmt"

// codeNotFound is the server error code for a missing agent, pane or
// workspace target; the gateway maps it to domain.ErrAgentGone.
const codeNotFound = "not_found"

// APIError is an error line returned by the Herdr server for a request.
// Codes are Herdr's snake_case identifiers such as "not_found".
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("herdr api %s: %s", e.Code, e.Message)
}
