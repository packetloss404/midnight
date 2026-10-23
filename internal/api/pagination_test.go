// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestParseStrictPageLimitRejectsNegative(t *testing.T) {
	if _, err := parseStrictPageLimit("-1"); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("err = %v, want ErrInvalidCursorPage", err)
	}
}

func TestParseStrictPageLimitRejectsAboveCap(t *testing.T) {
	if _, err := parseStrictPageLimit("500"); !errors.Is(err, orailtypes.ErrPageLimitExceeded) {
		t.Fatalf("err = %v, want ErrPageLimitExceeded", err)
	}
}

func TestParseStrictPageLimitDefaultsAndAcceptsCap(t *testing.T) {
	limit, err := parseStrictPageLimit("")
	if err != nil || limit != orailtypes.DefaultPageLimit {
		t.Fatalf("default err = %v, limit = %d", err, limit)
	}
	limit, err = parseStrictPageLimit("200")
	if err != nil || limit != 200 {
		t.Fatalf("cap err = %v, limit = %d", err, limit)
	}
}

func TestRouterListOperationsRejectsAboveCapWithPaginationLimitExceeded(t *testing.T) {
	router := NewRouterWithOperationStore(NewMemoryOperationStore())
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?limit=999", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.PaginationLimitExceeded {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.PaginationLimitExceeded)
	}
	if got.Error.Details["max"] == nil {
		t.Fatalf("expected details.max in envelope, got %#v", got.Error.Details)
	}
}
