// Package api contains shared v1alpha HTTP response contracts and handlers.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
)

type ErrorEnvelope struct {
	Code      failurecodes.Code `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	DocsURL   string            `json:"docs_url"`
	Details   map[string]any    `json:"details,omitempty"`
}

type ErrorResponse struct {
	// RFC 9457 problem details. The nested error member remains during
	// v1alpha as a compatibility extension for existing dashboard and CLI
	// clients; /api/v1 clients consume the top-level fields.
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail"`
	Code      failurecodes.Code `json:"code"`
	RequestID string            `json:"request_id"`
	DocsURL   string            `json:"docs_url"`
	Details   map[string]any    `json:"details,omitempty"`
	Error     ErrorEnvelope     `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newErrorResponse(status int, code failurecodes.Code, message, requestID string, details map[string]any) ErrorResponse {
	definition, ok := failurecodes.LookupProblemType(code)
	if !ok || !failurecodes.IsPermittedProblemStatus(code, status) {
		code = failurecodes.InternalError
		status = http.StatusInternalServerError
		definition, _ = failurecodes.LookupProblemType(code)
	}
	docsURL := definition.Type
	envelope := ErrorEnvelope{
		Code:      code,
		Message:   message,
		RequestID: requestID,
		DocsURL:   docsURL,
		Details:   details,
	}
	return ErrorResponse{
		Type:      docsURL,
		Title:     definition.Title,
		Status:    status,
		Detail:    message,
		Code:      code,
		RequestID: requestID,
		DocsURL:   docsURL,
		Details:   details,
		Error:     envelope,
	}
}

func marshalErrorResponse(status int, code failurecodes.Code, message, requestID string, details map[string]any) ([]byte, error) {
	body, err := json.Marshal(newErrorResponse(status, code, message, requestID, details))
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func responseStatus(body []byte, fallback int) int {
	var problem struct {
		Status int `json:"status"`
	}
	if json.Unmarshal(body, &problem) == nil && problem.Status >= 400 && problem.Status <= 599 {
		return problem.Status
	}
	return fallback
}

func WriteError(w http.ResponseWriter, status int, code failurecodes.Code, message string, requestID string, details map[string]any) {
	response := newErrorResponse(status, code, message, requestID, details)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(response.Status)
	_ = json.NewEncoder(w).Encode(response)
}
