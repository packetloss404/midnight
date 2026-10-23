package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestWriteErrorUsesProblemDetailsWithV1AlphaEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "bad token", "req-1", nil)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}

	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.AuthTokenHeaderInvalid || got.Error.RequestID != "req-1" || got.Error.DocsURL == "" {
		t.Fatalf("unexpected envelope: %#v", got.Error)
	}
	if got.Type != got.Error.DocsURL || got.Status != http.StatusUnauthorized ||
		got.Code != got.Error.Code || got.RequestID != got.Error.RequestID || got.Detail != "bad token" {
		t.Fatalf("unexpected problem details: %#v", got)
	}
}

func TestWriteErrorFallsBackToRegisteredCode(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusInternalServerError, failurecodes.Code("UNKNOWN"), "boom", "req-1", nil)

	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.InternalError {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.InternalError)
	}
}

func TestWriteErrorRejectsCodeStatusOutsideRuntimeProblemRegistry(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusConflict, failurecodes.ResourceNotFound, "impossible pair", "req-1", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want fail-closed 500", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != failurecodes.InternalError || got.Status != http.StatusInternalServerError {
		t.Fatalf("problem = %#v, want INTERNAL_ERROR/500", got)
	}
}

func TestMarshalErrorResponseFailClosedStatusMatchesStoredStatusHelper(t *testing.T) {
	body, err := marshalErrorResponse(http.StatusConflict, failurecodes.ResourceNotFound, "impossible pair", "req-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := responseStatus(body, http.StatusConflict); got != http.StatusInternalServerError {
		t.Fatalf("stored status = %d, want fail-closed 500", got)
	}
}

func TestMarshalErrorResponseMatchesLiveProblemShape(t *testing.T) {
	body, err := marshalErrorResponse(
		http.StatusConflict,
		failurecodes.ResourceConflict,
		"version changed",
		"req-replay",
		map[string]any{"expected_version": 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	var got ErrorResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type == "" || got.Title == "" || got.Status != http.StatusConflict ||
		got.Detail != "version changed" || got.Code != failurecodes.ResourceConflict ||
		got.Error.Code != got.Code || got.Error.RequestID != "req-replay" {
		t.Fatalf("stored problem response = %#v", got)
	}
}

func TestEveryRuntimeProblemDefinitionCanBeEmittedAtItsClosedStatuses(t *testing.T) {
	for _, definition := range failurecodes.APIProblemTypes() {
		for _, status := range definition.Statuses {
			response := newErrorResponse(status, definition.Code, "detail", "req-registry", nil)
			if response.Code != definition.Code || response.Status != status ||
				response.Type != definition.Type || response.Title != definition.Title {
				t.Errorf("problem %s/%d = %#v", definition.Code, status, response)
			}
		}
	}
}
