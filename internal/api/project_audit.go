// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	projectAuditCursorSignedPrefix   = "pa1"
	projectAuditCursorUnsignedPrefix = "pau1"
	projectAuditCursorMaxSize        = 2048
)

type projectAuditCursorPayload struct {
	ProjectID string `json:"project_id"`
	BeforeID  int64  `json:"before_id"`
}

type projectAuditEvent struct {
	ID                   int64     `json:"id"`
	OccurredAt           time.Time `json:"occurred_at"`
	Actor                string    `json:"actor"`
	ActorType            string    `json:"actor_type"`
	Action               string    `json:"action"`
	ResourceKind         string    `json:"resource_kind"`
	ResourceID           string    `json:"resource_id,omitempty"`
	Outcome              string    `json:"outcome"`
	RequestID            string    `json:"request_id"`
	ReasonCode           string    `json:"reason_code,omitempty"`
	PolicyDecision       string    `json:"policy_decision,omitempty"`
	RowHash              string    `json:"row_hash"`
	CanonicalFormVersion string    `json:"canonical_form_version"`
}

type projectAuditEventPage struct {
	ProjectID  string              `json:"project_id"`
	Events     []projectAuditEvent `json:"events"`
	NextCursor *string             `json:"next_cursor,omitempty"`
	Truncated  bool                `json:"truncated"`
}

func projectAuditEventsHandler(reader audit.ProjectReader, access identity.AccessStore, cursorCodec CursorCodec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		if projectID == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project audit scope not found", nil)
			return
		}
		if !authorizeHumanProjectAuditRead(w, r, access, projectID) {
			return
		}
		if reader == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project audit store is unavailable", nil)
			return
		}
		limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
		if errors.Is(err, orailtypes.ErrPageLimitExceeded) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.PaginationLimitExceeded, "page limit exceeds locked maximum", map[string]any{"max": orailtypes.MaxPageLimit})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid page limit", nil)
			return
		}
		beforeID := int64(0)
		if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
			beforeID, err = cursorCodec.decodeProjectAuditCursor(raw, projectID)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid project audit cursor", nil)
				return
			}
		}
		rows, err := reader.ReadProject(r.Context(), audit.ProjectQuery{
			ProjectID: projectID,
			BeforeID:  beforeID,
			Limit:     limit + 1,
		})
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project audit read failed", nil)
			return
		}
		truncated := len(rows) > limit
		if truncated {
			rows = rows[:limit]
		}
		events := make([]projectAuditEvent, 0, len(rows))
		for _, row := range rows {
			events = append(events, projectAuditEventFromRow(row))
		}
		var nextCursor *string
		if truncated && len(rows) > 0 {
			token, encodeErr := cursorCodec.encodeProjectAuditCursor(projectID, rows[len(rows)-1].Sequence)
			if encodeErr != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project audit cursor is unavailable", nil)
				return
			}
			nextCursor = &token
		}
		WriteJSON(w, http.StatusOK, projectAuditEventPage{
			ProjectID: projectID, Events: events, NextCursor: nextCursor, Truncated: truncated,
		})
	}
}

func authorizeHumanProjectAuditRead(w http.ResponseWriter, r *http.Request, access identity.AccessStore, projectID string) bool {
	principal, ok := productPrincipalFromContext(r.Context())
	if !ok || principal.Kind != productPrincipalHuman {
		return true
	}
	if access == nil || principal.Human.UserID == "" {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project audit authorization is unavailable", nil)
		return false
	}
	_, _, err := identity.Authorize(
		r.Context(), access, principal.Human.UserID,
		identity.AccessTarget{ProjectID: projectID}, identity.CapabilityProjectRead, false,
	)
	if err == nil {
		return true
	}
	if tenantReadDenied(err) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project audit scope not found", nil)
		return false
	}
	writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project audit authorization is unavailable", nil)
	return false
}

func projectAuditEventFromRow(row audit.Row) projectAuditEvent {
	outcome := "recorded"
	if decision := strings.ToLower(strings.TrimSpace(row.PolicyDecision)); decision != "" {
		outcome = decision
	} else if strings.TrimSpace(row.ReasonCode) != "" {
		outcome = "failed"
	}
	actorType := firstNonEmpty(row.ActorType, "system")
	return projectAuditEvent{
		ID: row.Sequence, OccurredAt: row.OccurredAt.UTC(),
		Actor: firstNonEmpty(row.ActorID, actorType), ActorType: actorType,
		Action: row.Action, ResourceKind: row.ResourceKind, ResourceID: row.ResourceID,
		Outcome: outcome, RequestID: row.RequestID, ReasonCode: row.ReasonCode,
		PolicyDecision: row.PolicyDecision, RowHash: auditRowHash(row.RowHash),
		CanonicalFormVersion: row.CanonicalFormVersion,
	}
}

func (c CursorCodec) encodeProjectAuditCursor(projectID string, beforeID int64) (string, error) {
	payload := projectAuditCursorPayload{ProjectID: strings.TrimSpace(projectID), BeforeID: beforeID}
	if payload.ProjectID == "" || payload.BeforeID <= 0 {
		return "", orailtypes.ErrInvalidCursorPage
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(c.secret) == 0 {
		return projectAuditCursorUnsignedPrefix + "." + encoded, nil
	}
	return projectAuditCursorSignedPrefix + "." + encoded + "." + c.projectAuditCursorSignature(encoded), nil
}

func (c CursorCodec) decodeProjectAuditCursor(raw, projectID string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > projectAuditCursorMaxSize {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	parts := strings.Split(raw, ".")
	var encoded string
	switch {
	case len(parts) == 2 && parts[0] == projectAuditCursorUnsignedPrefix && len(c.secret) == 0:
		encoded = parts[1]
	case len(parts) == 3 && parts[0] == projectAuditCursorSignedPrefix && len(c.secret) != 0 &&
		hmac.Equal([]byte(parts[2]), []byte(c.projectAuditCursorSignature(parts[1]))):
		encoded = parts[1]
	default:
		return 0, orailtypes.ErrInvalidCursorPage
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	var decoded projectAuditCursorPayload
	if err := decodeStrictCursorJSON(payload, &decoded); err != nil ||
		decoded.ProjectID != strings.TrimSpace(projectID) || decoded.BeforeID <= 0 {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	return decoded.BeforeID, nil
}

func (c CursorCodec) projectAuditCursorSignature(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte("midnight:project-audit-cursor:v1:"))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
