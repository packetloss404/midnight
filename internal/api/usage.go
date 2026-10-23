// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/metering"
)

func usageSummaryHandler(store metering.Store, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable usage evidence is unavailable", nil)
			return
		}
		workspaceID, ok := singleUsageQuery(r, "workspace_id")
		if !ok || strings.TrimSpace(workspaceID) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "workspace_id is required exactly once", nil)
			return
		}
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified || principal.Kind != productPrincipalHuman || principal.Human.UserID == "" {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "usage is available only to an authenticated workspace member", nil)
			return
		}
		now := time.Now().UTC()
		if clock != nil {
			now = clock().UTC()
		}
		since, until := metering.DefaultWindow(now)
		var err error
		if raw, present, valid := singleOptionalUsageQuery(r, "since"); present {
			if !valid {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "since is required exactly once when supplied", nil)
				return
			}
			since, err = time.Parse(time.RFC3339, raw)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "since must be RFC3339", nil)
				return
			}
		}
		if raw, present, valid := singleOptionalUsageQuery(r, "until"); present {
			if !valid {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "until is required exactly once when supplied", nil)
				return
			}
			until, err = time.Parse(time.RFC3339, raw)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "until must be RFC3339", nil)
				return
			}
		}
		if !until.After(since) || until.Sub(since) > 366*24*time.Hour {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "usage window must be positive and at most 366 days", nil)
			return
		}
		summary, err := store.Summary(r.Context(), metering.SummaryQuery{WorkspaceID: workspaceID,
			ActorUserID: principal.Human.UserID, WindowStart: since.UTC(), WindowEnd: until.UTC()})
		if err != nil {
			if errors.Is(err, metering.ErrNotAuthorized) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "workspace not found", nil)
				return
			}
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "usage evidence could not be reconciled", nil)
			return
		}
		WriteJSON(w, http.StatusOK, summary)
	}
}

func singleUsageQuery(r *http.Request, name string) (string, bool) {
	values, exists := r.URL.Query()[name]
	if !exists || len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func singleOptionalUsageQuery(r *http.Request, name string) (value string, present, valid bool) {
	values, exists := r.URL.Query()[name]
	if !exists {
		return "", false, true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", true, false
	}
	return values[0], true, true
}
