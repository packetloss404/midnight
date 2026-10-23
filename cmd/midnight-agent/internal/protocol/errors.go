package protocol

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HTTPError is returned for non-success protocol responses.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	DocsURL    string
	Details    map[string]any
}

func (e HTTPError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("http %d: %s: %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("http %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

type errorResponse struct {
	Error errorEnvelope `json:"error"`
}

type errorEnvelope struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	DocsURL   string         `json:"docs_url"`
	Details   map[string]any `json:"details,omitempty"`
}

func decodeHTTPError(resp *http.Response) error {
	httpErr := HTTPError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	var body errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return httpErr
	}
	if body.Error.Code != "" {
		httpErr.Code = body.Error.Code
	}
	if body.Error.Message != "" {
		httpErr.Message = body.Error.Message
	}
	httpErr.RequestID = body.Error.RequestID
	httpErr.DocsURL = body.Error.DocsURL
	httpErr.Details = body.Error.Details
	return httpErr
}
