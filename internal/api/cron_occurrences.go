// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
)

// CronOccurrenceStore is deliberately read-only. Cron invocations are driven
// by the durable worker cursor, not by an API action or an Operation resource.
type CronOccurrenceStore interface {
	ListOccurrences(context.Context, string, string, string, string, int) ([]deploy.CronOccurrence, error)
	GetOccurrence(context.Context, string, string, string, string) (deploy.CronOccurrence, error)
}

type CronOccurrenceView struct {
	ID               string     `json:"id"`
	DeploymentID     string     `json:"deployment_id"`
	ProjectID        string     `json:"project_id"`
	ServiceID        string     `json:"service_id"`
	EnvironmentID    string     `json:"environment_id"`
	DueAt            time.Time  `json:"due_at"`
	SnapshotSHA256   string     `json:"snapshot_sha256"`
	Image            string     `json:"image"`
	State            string     `json:"state"`
	Attempt          int        `json:"attempt"`
	MaxAttempts      int        `json:"max_attempts"`
	TimeoutSeconds   int64      `json:"timeout_seconds"`
	RetryAt          *time.Time `json:"retry_at,omitempty"`
	AttemptStartedAt *time.Time `json:"attempt_started_at,omitempty"`
	ContainerName    string     `json:"container_name,omitempty"`
	ContainerID      string     `json:"container_id,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	FailureCode      string     `json:"failure_code,omitempty"`
	FailureMessage   string     `json:"failure_message,omitempty"`
	// OperationID is intentionally absent: a cron occurrence is an automatic,
	// durable scheduler execution rather than a user-created Operation. The
	// terminal/retry evidence above is the authoritative outcome surface.
}

func cronOccurrencesHandler(store CronOccurrenceStore, scopeResolver MutationScopeResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil || scopeResolver == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable cron occurrence storage is not configured", map[string]any{"capability": "durable-cron-occurrences", "state": "unavailable"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		serviceID, environmentID := strings.TrimSpace(r.PathValue("service_id")), strings.TrimSpace(r.PathValue("environment_id"))
		scope, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{ServiceID: serviceID, EnvironmentID: environmentID, RequireProject: true, RequireService: true, RequireEnvironment: true})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		if state != "" && !validCronOccurrenceState(state) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid cron occurrence state", map[string]any{"field": "state"})
			return
		}
		if id := strings.TrimSpace(r.PathValue("occurrence_id")); id != "" {
			item, err := store.GetOccurrence(r.Context(), scope.ProjectID, environmentID, serviceID, id)
			if errors.Is(err, pgx.ErrNoRows) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "cron occurrence not found", map[string]any{"occurrence_id": id})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "cron occurrence lookup failed", nil)
				return
			}
			WriteJSON(w, http.StatusOK, cronOccurrenceView(item))
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 200 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "limit must be between 1 and 200", map[string]any{"field": "limit"})
				return
			}
		}
		items, err := store.ListOccurrences(r.Context(), scope.ProjectID, environmentID, serviceID, state, limit)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "cron occurrence list failed", nil)
			return
		}
		out := make([]CronOccurrenceView, 0, len(items))
		for _, item := range items {
			out = append(out, cronOccurrenceView(item))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

func validCronOccurrenceState(value string) bool {
	switch value {
	case deploy.CronRunPending, deploy.CronRunRunning, deploy.CronRunRetryWait, deploy.CronRunSucceeded, deploy.CronRunFailed:
		return true
	}
	return false
}

func cronOccurrenceView(item deploy.CronOccurrence) CronOccurrenceView {
	return CronOccurrenceView{ID: item.ID, DeploymentID: item.DeploymentID, ProjectID: item.ProjectID, ServiceID: item.ServiceID, EnvironmentID: item.EnvironmentID, DueAt: item.DueAt, SnapshotSHA256: item.SnapshotSHA256, Image: item.Image, State: item.State, Attempt: item.Attempt, MaxAttempts: item.MaxAttempts, TimeoutSeconds: int64(item.Timeout / time.Second), RetryAt: item.RetryAt, AttemptStartedAt: item.AttemptStartedAt, ContainerName: item.ContainerName, ContainerID: item.ContainerID, ExitCode: item.ExitCode, FailureCode: string(item.FailureCode), FailureMessage: item.FailureMessage}
}
