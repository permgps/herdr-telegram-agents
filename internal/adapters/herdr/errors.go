package herdr

import "fmt"

// APIError is an error line returned by the Herdr server for a request.
// Codes are Herdr's snake_case identifiers such as "not_found".
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("herdr api %s: %s", e.Code, e.Message)
}
