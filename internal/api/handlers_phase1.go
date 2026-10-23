// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/timeline"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// AuditFilter is the API-side audit list query shape.
//
// Lane 4's panel-locked audit.Query type narrows to the chain-cursor
// fields (SinceID + Limit) needed by the JSONL exporter and the
// AuditChain.Read seam. The /api/v1alpha/audit handler exposes the
// fuller server-side filter set (resource/actor/request scoping +
// after_id cursor) so the API stays read-friendly without the chain
// math caring about it. Implementations translate this filter onto
// whatever underlying store they have (in-memory MemoryChain, future
// Postgres-backed AuditChain, etc.).
type AuditFilter struct {
	ResourceKind string
	ResourceID   string
	ActorID      string
	RequestID    string
	AfterID      int64
	Limit        int
}

// AuditQueryStore is the read seam used by the audit list handler.
type AuditQueryStore interface {
	ListAudit(ctx context.Context, f AuditFilter) ([]audit.Row, error)
}

// TimelineQueryStore is the read seam used by the timeline list
// handler. It matches both the in-memory and Postgres timeline stores.
type TimelineQueryStore interface {
	Query(filter timeline.Filter, cursor timeline.QueryCursor, pageSize int) (timeline.QueryResult, error)
}

// MigrationsAdminStore wraps the migration runner so the admin
// handlers in router.go can invoke up/down without depending on
// internal/db/pg directly.
type MigrationsAdminStore interface {
	List(ctx context.Context) ([]pg.AppliedMigration, error)
	Up(ctx context.Context) ([]pg.MigrationFile, error)
	Down(ctx context.Context) (*pg.MigrationFile, error)
	Manifest(ctx context.Context) ([]pg.MigrationFile, db.MigrationManifest, error)
}

// MigrationsRunnerAdmin adapts a *pg.MigrationRunner to MigrationsAdminStore.
type MigrationsRunnerAdmin struct {
	Runner *pg.MigrationRunner
}

// List returns the schema_migrations rows.
func (m *MigrationsRunnerAdmin) List(ctx context.Context) ([]pg.AppliedMigration, error) {
	return pg.ListApplied(ctx, m.Runner.Pool)
}

// Up applies pending migrations.
func (m *MigrationsRunnerAdmin) Up(ctx context.Context) ([]pg.MigrationFile, error) {
	return m.Runner.Up(ctx)
}

// Down rolls back the latest applied migration.
func (m *MigrationsRunnerAdmin) Down(ctx context.Context) (*pg.MigrationFile, error) {
	return m.Runner.Down(ctx)
}

// Manifest returns the on-disk manifest with computed checksums.
func (m *MigrationsRunnerAdmin) Manifest(ctx context.Context) ([]pg.MigrationFile, db.MigrationManifest, error) {
	manifestPath := m.Runner.ManifestPath
	if manifestPath == "" {
		manifestPath = "manifest.yaml"
	}
	files, manifest, err := pg.LoadOrderedMigrations(m.Runner.FS, manifestPath)
	if err != nil {
		return nil, db.MigrationManifest{}, err
	}
	return files, manifest, nil
}

// auditListHandler exposes GET /api/v1alpha/audit. Lane 4 will own the
// signed-export and CLI-side verify wiring; this handler is a basic
// cursor-paginated read.
func auditListHandler(store AuditQueryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		filter := AuditFilter{
			ResourceKind: r.URL.Query().Get("resource_kind"),
			ResourceID:   r.URL.Query().Get("resource_id"),
			ActorID:      r.URL.Query().Get("actor_id"),
			RequestID:    r.URL.Query().Get("request_id"),
		}
		if after := r.URL.Query().Get("after_id"); after != "" {
			n, err := strconv.ParseInt(after, 10, 64)
			if err != nil || n < 0 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid after_id cursor", nil)
				return
			}
			filter.AfterID = n
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
		filter.Limit = limit
		rows, err := store.ListAudit(r.Context(), filter)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit list failed", nil)
			return
		}
		view := make([]auditRowView, 0, len(rows))
		for _, row := range rows {
			view = append(view, toAuditRowView(row))
		}
		var nextCursor *string
		if len(view) == limit && len(view) > 0 {
			cur := strconv.FormatInt(view[len(view)-1].ID, 10)
			nextCursor = &cur
		}
		WriteJSON(w, http.StatusOK, orailtypes.CursorPageResponse[auditRowView]{
			Data: view,
			Page: orailtypes.CursorPageDTO{NextCursor: nextCursor},
		})
	}
}

type auditRowView struct {
	ID                   int64           `json:"id"`
	PrevHash             string          `json:"prev_hash"`
	RowHash              string          `json:"row_hash"`
	CanonicalFormVersion string          `json:"canonical_form_version"`
	RequestID            string          `json:"request_id"`
	OccurredAt           string          `json:"occurred_at"`
	RecordedAt           string          `json:"recorded_at"`
	ActorID              string          `json:"actor_id,omitempty"`
	ActorType            string          `json:"actor_type"`
	ResourceKind         string          `json:"resource_kind"`
	ResourceID           string          `json:"resource_id,omitempty"`
	Action               string          `json:"action"`
	Before               json.RawMessage `json:"before,omitempty"`
	After                json.RawMessage `json:"after,omitempty"`
}

func toAuditRowView(r audit.Row) auditRowView {
	return auditRowView{
		ID:                   r.Sequence,
		PrevHash:             hex.EncodeToString(r.PrevHash),
		RowHash:              hex.EncodeToString(r.RowHash),
		CanonicalFormVersion: r.CanonicalFormVersion,
		RequestID:            r.RequestID,
		OccurredAt:           r.OccurredAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		RecordedAt:           r.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		ActorID:              r.ActorID,
		ActorType:            r.ActorType,
		ResourceKind:         r.ResourceKind,
		ResourceID:           r.ResourceID,
		Action:               r.Action,
		Before:               marshalAuditPayload(r.Before),
		After:                marshalAuditPayload(r.After),
	}
}

// marshalAuditPayload serializes Lane 4's map[string]any audit payload
// fields back into the json.RawMessage shape the API view exposes. A
// nil/empty map renders as omitempty (the JSON tag handles it).
func marshalAuditPayload(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

// timelineListHandler exposes GET /api/v1alpha/timeline.
func timelineListHandler(store TimelineQueryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		filter := timeline.Filter{
			DeploymentID: r.URL.Query().Get("deployment_id"),
			FreeText:     r.URL.Query().Get("text"),
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

		cursor := timeline.QueryCursor{}
		if rawCursor := r.URL.Query().Get("cursor"); rawCursor != "" {
			parts := strings.SplitN(rawCursor, ":", 2)
			if len(parts) != 2 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid timeline cursor", nil)
				return
			}
			seq, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || seq < 0 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid timeline cursor", nil)
				return
			}
			cursor = timeline.QueryCursor{DeploymentID: parts[0], AfterSeq: seq}
		}

		result, err := store.Query(filter, cursor, limit)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "timeline query failed", nil)
			return
		}
		var nextCursor *string
		if result.Truncated && result.NextCursor.DeploymentID != "" {
			c := result.NextCursor.DeploymentID + ":" + strconv.FormatInt(result.NextCursor.AfterSeq, 10)
			nextCursor = &c
		}
		WriteJSON(w, http.StatusOK, orailtypes.CursorPageResponse[timeline.Event]{
			Data: result.Events,
			Page: orailtypes.CursorPageDTO{NextCursor: nextCursor},
		})
	}
}

type EnvironmentMutationRequest struct {
	ProjectID       string `json:"project_id,omitempty"`
	Slug            string `json:"slug,omitempty"`
	Name            string `json:"name,omitempty"`
	Tier            string `json:"tier,omitempty"`
	IsProtected     *bool  `json:"is_protected,omitempty"`
	SourceID        string `json:"source_id,omitempty"`
	CopyConfig      bool   `json:"copy_config,omitempty"`
	CopyDomains     bool   `json:"copy_domains,omitempty"`
	CopyVolumes     bool   `json:"copy_volumes,omitempty"`
	CopyDeployments bool   `json:"copy_deployments,omitempty"`
}

// EnvironmentMetadataUpdateRequest is the strict human-session PATCH shape.
// Pointer fields preserve partial-update intent; raw-field presence validation
// below distinguishes omission from an explicit JSON null.
type EnvironmentMetadataUpdateRequest struct {
	ExpectedVersion *int64  `json:"expected_version"`
	Slug            *string `json:"slug,omitempty"`
	Name            *string `json:"name,omitempty"`
	Tier            *string `json:"tier,omitempty"`
}

const environmentCreateBodyLimitBytes = 16 << 10

// environmentsHandler exposes the durable environment lifecycle. Metadata-only
// duplication works in every store. The optional Postgres duplicator adds one
// atomic service-config + encrypted-variable copy while still naming runtime
// resources that were deliberately omitted.
func environmentsHandler(store EnvironmentStore, duplicators ...EnvironmentDuplicator) http.HandlerFunc {
	var duplicator EnvironmentDuplicator
	if len(duplicators) > 0 {
		duplicator = duplicators[0]
	}
	return environmentsHandlerWithTenantMutations(store, duplicator, nil, nil)
}

// environmentsHandlerWithTenantMutations preserves the operator lifecycle
// while admitting only collection POST through the explicit human
// coordinator. Item mutation and duplication remain behind the recovery
// boundary.
func environmentsHandlerWithTenantMutations(
	store EnvironmentStore,
	duplicator EnvironmentDuplicator,
	createCoordinator TenantMutationCoordinator,
	updateCoordinator TenantEnvironmentUpdateCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == "/environments" {
			switch r.Method {
			case http.MethodGet:
				filter := ResourceListFilter{ParentID: orailtypes.ID(r.URL.Query().Get("project_id"))}
				envs, err := store.ListEnvironments(r.Context(), filter)
				if err != nil {
					writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "environment list failed", nil)
					return
				}
				WriteJSON(w, http.StatusOK, envs)
				return
			case http.MethodPost:
				createEnvironment(w, r, store, createCoordinator)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}
		rest := strings.TrimPrefix(path, "/environments/")
		id, action, hasAction := strings.Cut(rest, "/")
		if id == "" || (hasAction && action != "duplicate") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", nil)
			return
		}
		if hasAction {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"environment_id": id})
				return
			}
			duplicateEnvironment(w, r, store, duplicator, orailtypes.ID(id))
			return
		}
		switch r.Method {
		case http.MethodGet:
			env, err := store.GetEnvironment(r.Context(), orailtypes.ID(id))
			if errors.Is(err, ErrResourceNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", map[string]any{"environment_id": id})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "environment lookup failed", map[string]any{"environment_id": id})
				return
			}
			w.Header().Set("ETag", strongVersionETag(env.ResourceVersion))
			WriteJSON(w, http.StatusOK, env)
			return
		case http.MethodPatch:
			updateEnvironment(w, r, store, orailtypes.ID(id), updateCoordinator)
			return
		case http.MethodDelete:
			expectedVersion, ok := requireIfMatchVersion(w, r)
			if !ok {
				return
			}
			conditional, supported := store.(ConditionalEnvironmentStore)
			if !supported {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "conditional environment deletion is unavailable", nil)
				return
			}
			if err := conditional.DeleteEnvironmentCAS(r.Context(), orailtypes.ID(id), expectedVersion); err != nil {
				if errors.Is(err, ErrResourcePreconditionFailed) {
					writeIfMatchFailed(w, r, "environment version changed", map[string]any{"expected_version": expectedVersion})
					return
				}
				writeEnvironmentStoreError(w, r, err, id)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"environment_id": id})
			return
		}
	}
}

func decodeEnvironmentMutation(w http.ResponseWriter, r *http.Request) (EnvironmentMutationRequest, bool) {
	var request EnvironmentMutationRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid environment request body") {
		return request, false
	}
	request.ProjectID, request.Slug, request.Name, request.Tier, request.SourceID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.Slug), strings.TrimSpace(request.Name), strings.TrimSpace(request.Tier), strings.TrimSpace(request.SourceID)
	return request, true
}

func createEnvironment(
	w http.ResponseWriter,
	r *http.Request,
	store EnvironmentStore,
	coordinators ...TenantMutationCoordinator,
) {
	limited := http.MaxBytesReader(w, r.Body, environmentCreateBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "environment request body exceeds the configured limit", map[string]any{"limit": environmentCreateBodyLimitBytes})
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return
	}
	var request EnvironmentMutationRequest
	if len(body) == 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment request body is required", nil)
		return
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid environment request body", map[string]any{"reason": "json"})
		return
	}
	request.ProjectID, request.Slug, request.Name, request.Tier, request.SourceID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.Slug), strings.TrimSpace(request.Name), strings.TrimSpace(request.Tier), strings.TrimSpace(request.SourceID)
	if request.ProjectID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return
	}
	if request.Slug == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "slug is required", map[string]any{"field": "slug"})
		return
	}
	if !serviceSlugPattern.MatchString(request.Slug) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment slug must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters", map[string]any{"field": "slug"})
		return
	}
	if request.SourceID != "" || request.CopyConfig || request.CopyDomains || request.CopyVolumes || request.CopyDeployments {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment creation does not accept duplication fields", nil)
		return
	}
	if request.Name == "" {
		request.Name = request.Slug
	}
	if request.Tier == "" {
		request.Tier = "development"
	}
	if !validEnvironmentTier(request.Tier) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid environment tier", map[string]any{"tier": request.Tier})
		return
	}
	protected := false
	if request.IsProtected != nil {
		protected = *request.IsProtected
	}
	principal, principalOK := productPrincipalFromContext(r.Context())
	if principalOK && principal.Kind == productPrincipalHuman {
		var coordinator TenantMutationCoordinator
		if len(coordinators) > 0 {
			coordinator = coordinators[0]
		}
		createTenantEnvironment(w, r, coordinator, request, body)
		return
	}
	env, err := store.CreateEnvironment(r.Context(), Environment{ProjectID: orailtypes.ID(request.ProjectID), Slug: request.Slug, Name: request.Name, Tier: request.Tier, IsProtected: protected})
	if err != nil {
		writeEnvironmentStoreError(w, r, err, "")
		return
	}
	WriteJSON(w, http.StatusCreated, env)
}

func createTenantEnvironment(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantMutationCoordinator,
	request EnvironmentMutationRequest,
	body []byte,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant environment creation is unavailable", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	key, keyFailure := singletonIdempotencyKey(r.Header)
	if keyFailure == failurecodes.IdempotencyKeyRequired {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "exactly one Idempotency-Key header is required", nil)
		return
	}
	if keyFailure == failurecodes.IdempotencyKeyInvalid {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "invalid idempotency key", nil)
		return
	}
	protected := request.IsProtected != nil && *request.IsProtected
	result, err := coordinator.CreateEnvironment(r.Context(), TenantEnvironmentCreateRequest{
		Principal: principal,
		Session:   session,
		Environment: Environment{
			ProjectID:   orailtypes.ID(request.ProjectID),
			Slug:        request.Slug,
			Name:        request.Name,
			Tier:        request.Tier,
			IsProtected: protected,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     r.Method,
			Path:       idempotencyRouteTemplate(r),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to create this environment", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, ErrResourceConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"slug": request.Slug})
		return
	case errors.Is(err, ErrResourceInvalid), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant environment creation is unavailable", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func updateEnvironment(
	w http.ResponseWriter,
	r *http.Request,
	store EnvironmentStore,
	id orailtypes.ID,
	coordinators ...TenantEnvironmentUpdateCoordinator,
) {
	if principal, ok := productPrincipalFromContext(r.Context()); ok &&
		principal.Kind == productPrincipalHuman {
		var coordinator TenantEnvironmentUpdateCoordinator
		if len(coordinators) > 0 {
			coordinator = coordinators[0]
		}
		updateTenantEnvironment(w, r, coordinator, string(id))
		return
	}
	request, ok := decodeEnvironmentMutation(w, r)
	if !ok {
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	current, err := store.GetEnvironment(r.Context(), id)
	if err != nil {
		writeEnvironmentStoreError(w, r, err, string(id))
		return
	}
	if request.ProjectID != "" && request.ProjectID != string(current.ProjectID) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is immutable", nil)
		return
	}
	if request.Slug != "" {
		current.Slug = request.Slug
	}
	if request.Name != "" {
		current.Name = request.Name
	}
	if request.Tier != "" {
		current.Tier = request.Tier
	}
	if request.IsProtected != nil {
		current.IsProtected = *request.IsProtected
	}
	conditional, supported := store.(ConditionalEnvironmentStore)
	if !supported {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "conditional environment update is unavailable", nil)
		return
	}
	env, err := conditional.UpdateEnvironmentCAS(r.Context(), current, ifMatch)
	if errors.Is(err, ErrResourcePreconditionFailed) {
		writeIfMatchFailed(w, r, "environment version changed", map[string]any{"expected_version": ifMatch})
		return
	}
	if err != nil {
		writeEnvironmentStoreError(w, r, err, string(id))
		return
	}
	w.Header().Set("ETag", strongVersionETag(env.ResourceVersion))
	WriteJSON(w, http.StatusOK, env)
}

func updateTenantEnvironment(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantEnvironmentUpdateCoordinator,
	environmentID string,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant environment update is unavailable", nil)
		return
	}
	body, request, ok := readEnvironmentMetadataUpdateRequest(w, r)
	if !ok {
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	if !expectedVersionMatchesIfMatch(w, r, *request.ExpectedVersion, ifMatch) {
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	key, keyFailure := singletonIdempotencyKey(r.Header)
	if keyFailure == failurecodes.IdempotencyKeyRequired {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "exactly one Idempotency-Key header is required", nil)
		return
	}
	if keyFailure == failurecodes.IdempotencyKeyInvalid {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "invalid idempotency key", nil)
		return
	}
	result, err := coordinator.UpdateEnvironment(r.Context(), TenantEnvironmentUpdateRequest{
		Principal:     principal,
		Session:       session,
		EnvironmentID: environmentID,
		Update: TenantEnvironmentUpdate{
			ExpectedVersion: *request.ExpectedVersion,
			Slug:            request.Slug,
			Name:            request.Name,
			Tier:            request.Tier,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     r.Method,
			Path:       tenantEnvironmentUpdateIdempotencyPath(environmentID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", map[string]any{"environment_id": environmentID})
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to update this environment", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, ErrResourceConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"environment_id": environmentID})
		return
	case errors.Is(err, ErrResourceInvalid), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant environment update is unavailable", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func readEnvironmentMetadataUpdateRequest(
	w http.ResponseWriter,
	r *http.Request,
) ([]byte, EnvironmentMetadataUpdateRequest, bool) {
	limited := http.MaxBytesReader(w, r.Body, environmentCreateBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "environment update request body exceeds the configured limit", map[string]any{"limit": environmentCreateBodyLimitBytes})
			return nil, EnvironmentMetadataUpdateRequest{}, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment update body must be one JSON object", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	var request EnvironmentMetadataUpdateRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid environment update request", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment update body must contain exactly one JSON object", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment update body must be one JSON object", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	expectedRaw, hasExpected := fields["expected_version"]
	if !hasExpected || bytes.Equal(bytes.TrimSpace(expectedRaw), []byte("null")) ||
		request.ExpectedVersion == nil || *request.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be positive", map[string]any{"field": "expected_version"})
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	metadataCount := 0
	for _, field := range []string{"slug", "name", "tier"} {
		if raw, present := fields[field]; present {
			metadataCount++
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment metadata fields cannot be null", map[string]any{"field": field})
				return nil, EnvironmentMetadataUpdateRequest{}, false
			}
		}
	}
	if metadataCount == 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "at least one of slug, name, or tier is required", nil)
		return nil, EnvironmentMetadataUpdateRequest{}, false
	}
	if request.Slug != nil {
		value := strings.TrimSpace(*request.Slug)
		request.Slug = &value
	}
	if request.Name != nil {
		value := strings.TrimSpace(*request.Name)
		request.Name = &value
	}
	if request.Tier != nil {
		value := strings.TrimSpace(*request.Tier)
		request.Tier = &value
	}
	return body, request, true
}

func duplicateEnvironment(w http.ResponseWriter, r *http.Request, store EnvironmentStore, duplicator EnvironmentDuplicator, sourceID orailtypes.ID) {
	request, ok := decodeEnvironmentMutation(w, r)
	if !ok {
		return
	}
	if request.CopyDomains || request.CopyVolumes || request.CopyDeployments {
		requested := make([]string, 0, 3)
		if request.CopyDomains {
			requested = append(requested, "domains")
		}
		if request.CopyVolumes {
			requested = append(requested, "volumes")
		}
		if request.CopyDeployments {
			requested = append(requested, "deployments")
		}
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed,
			"domains, persistent volumes, and deployment/runtime history cannot be cloned safely",
			map[string]any{"unsupported_features": requested})
		return
	}
	if request.CopyConfig {
		if duplicator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "config-bearing environment duplication requires the durable Postgres copier", map[string]any{"capability": "environment-duplicate-config", "state": "unavailable"})
			return
		}
		idempotencyKey := idempotency.Key(strings.TrimSpace(r.Header.Get("Idempotency-Key")))
		if idempotencyKey == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required for config-bearing duplication", nil)
			return
		}
		if err := idempotency.ValidateKey(idempotencyKey); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid Idempotency-Key header", nil)
			return
		}
		result, err := duplicator.DuplicateEnvironment(r.Context(), EnvironmentDuplicateRequest{
			SourceID:       sourceID,
			ProjectID:      orailtypes.ID(request.ProjectID),
			Slug:           request.Slug,
			Name:           request.Name,
			Tier:           request.Tier,
			IsProtected:    request.IsProtected,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			writeEnvironmentDuplicateError(w, r, err, string(sourceID))
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		WriteJSON(w, http.StatusCreated, result)
		return
	}
	source, err := store.GetEnvironment(r.Context(), sourceID)
	if err != nil {
		writeEnvironmentStoreError(w, r, err, string(sourceID))
		return
	}
	if request.Slug == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "slug is required", nil)
		return
	}
	if request.ProjectID != "" && request.ProjectID != string(source.ProjectID) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "duplicate must stay in the source project", nil)
		return
	}
	name := request.Name
	if name == "" {
		name = source.Name + " copy"
	}
	tier := request.Tier
	if tier == "" {
		tier = source.Tier
	}
	protected := false
	if request.IsProtected != nil {
		protected = *request.IsProtected
	}
	env, err := store.CreateEnvironment(r.Context(), Environment{ProjectID: source.ProjectID, Slug: request.Slug, Name: name, Tier: tier, IsProtected: protected})
	if err != nil {
		writeEnvironmentStoreError(w, r, err, string(sourceID))
		return
	}
	WriteJSON(w, http.StatusCreated, env)
}

func writeEnvironmentDuplicateError(w http.ResponseWriter, r *http.Request, err error, sourceID string) {
	if errors.Is(err, ErrEnvironmentDuplicateIdempotencyConflict) ||
		errors.Is(err, idempotency.ErrKeyReusedDifferentBody) ||
		errors.Is(err, idempotency.ErrRequestInProgress) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "Idempotency-Key is already bound to a different or incomplete request", map[string]any{"environment_id": sourceID})
		return
	}
	writeEnvironmentStoreError(w, r, err, sourceID)
}

func writeEnvironmentStoreError(w http.ResponseWriter, r *http.Request, err error, id string) {
	if errors.Is(err, ErrResourceNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", map[string]any{"environment_id": id})
		return
	}
	if errors.Is(err, ErrResourceConflict) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"environment_id": id})
		return
	}
	if errors.Is(err, ErrResourceInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), map[string]any{"environment_id": id})
		return
	}
	writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "environment mutation failed", map[string]any{"environment_id": id})
}

// migrationsHandler exposes GET/POST/DELETE /api/v1alpha/migrations.
//   - GET: list applied + pending migrations (with real SHA-256s).
//   - POST {"action":"up"}: apply pending migrations.
//   - POST {"action":"down"}: roll back the latest applied migration.
func migrationsHandler(store MigrationsAdminStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			migrationsListHandler(w, r, store)
		case http.MethodPost:
			migrationsRunHandler(w, r, store)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

type migrationView struct {
	Version    int    `json:"version"`
	Name       string `json:"name"`
	Up         string `json:"up"`
	Down       string `json:"down"`
	UpSHA256   string `json:"up_sha256"`
	DownSHA256 string `json:"down_sha256"`
	Applied    bool   `json:"applied"`
}

type migrationsListResponse struct {
	SchemaVersion int             `json:"schema_version"`
	LatestVersion int             `json:"latest_version"`
	AppliedCount  int             `json:"applied_count"`
	PendingCount  int             `json:"pending_count"`
	Migrations    []migrationView `json:"migrations"`
}

func migrationsListHandler(w http.ResponseWriter, r *http.Request, store MigrationsAdminStore) {
	files, manifest, err := store.Manifest(r.Context())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "migration manifest not found", nil)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "manifest load failed", nil)
		return
	}
	applied, err := store.List(r.Context())
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "applied list failed", nil)
		return
	}
	appliedSet := make(map[int]struct{}, len(applied))
	for _, row := range applied {
		appliedSet[row.Version] = struct{}{}
	}
	views := make([]migrationView, 0, len(files))
	pending := 0
	for _, m := range files {
		_, isApplied := appliedSet[m.Version]
		if !isApplied {
			pending++
		}
		views = append(views, migrationView{
			Version:    m.Version,
			Name:       m.Name,
			Up:         m.Up,
			Down:       m.Down,
			UpSHA256:   m.UpSHA256,
			DownSHA256: m.DownSHA256,
			Applied:    isApplied,
		})
	}
	WriteJSON(w, http.StatusOK, migrationsListResponse{
		SchemaVersion: manifest.SchemaVersion,
		LatestVersion: manifest.SchemaVersion,
		AppliedCount:  len(applied),
		PendingCount:  pending,
		Migrations:    views,
	})
}

type migrationsActionRequest struct {
	Action string `json:"action"`
}

type migrationsActionResponse struct {
	Action string          `json:"action"`
	Files  []migrationView `json:"files"`
}

func migrationsRunHandler(w http.ResponseWriter, r *http.Request, store MigrationsAdminStore) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid migrations action body")
	if !ok {
		return
	}
	var req migrationsActionRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &req, "invalid migrations action body") {
		return
	}
	switch strings.ToLower(req.Action) {
	case "", "up":
		applied, err := store.Up(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migrations up failed: "+err.Error(), nil)
			return
		}
		WriteJSON(w, http.StatusOK, migrationsActionResponse{
			Action: "up",
			Files:  toMigrationViews(applied),
		})
	case "down":
		rolled, err := store.Down(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migrations down failed: "+err.Error(), nil)
			return
		}
		var files []migrationView
		if rolled != nil {
			files = toMigrationViews([]pg.MigrationFile{*rolled})
		}
		WriteJSON(w, http.StatusOK, migrationsActionResponse{
			Action: "down",
			Files:  files,
		})
	default:
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "unsupported migrations action", map[string]any{"action": req.Action})
	}
}

func toMigrationViews(files []pg.MigrationFile) []migrationView {
	out := make([]migrationView, 0, len(files))
	for _, m := range files {
		out = append(out, migrationView{
			Version:    m.Version,
			Name:       m.Name,
			Up:         m.Up,
			Down:       m.Down,
			UpSHA256:   m.UpSHA256,
			DownSHA256: m.DownSHA256,
			Applied:    true,
		})
	}
	return out
}
