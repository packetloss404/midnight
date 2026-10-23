// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/observability"
)

const (
	defaultObservabilityLogWindow = time.Hour
	defaultSSEReplayWindow        = 15 * time.Minute
	maxObservabilitySearchBytes   = 1024
	maxObservabilityFilterValues  = 32
)

type observabilityLogsResponse struct {
	Scope           observability.Scope             `json:"scope"`
	Records         []observability.LogRecord       `json:"records"`
	NextCursor      *opaqueLogCursor                `json:"next_cursor,omitempty"`
	Truncated       bool                            `json:"truncated"`
	DroppedAtIngest int64                           `json:"dropped_at_ingest"`
	CollectorHealth []observability.CollectorHealth `json:"collector_health"`
}

func observabilityLogsHandler(store observability.Store, cursorCodec CursorCodec, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.LogStoreUnavailable, "durable observability store is not configured", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		query, rawCursor, ok := parseObservabilityLogQuery(w, r, clock, defaultObservabilityLogWindow)
		if !ok {
			return
		}
		binding := observabilityQueryBinding(query)
		if rawCursor != "" {
			cursor, err := cursorCodec.DecodeLogCursor(rawCursor, binding, "observability", binding)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid observability log cursor", nil)
				return
			}
			query.AfterID = int64(cursor.Offset)
		}
		page, err := store.QueryLogs(r.Context(), query)
		if err != nil {
			writeObservabilityError(w, r, err)
			return
		}
		collectorHealth, err := readCollectorHealth(
			r.Context(), store, collectorKindsForSurfaces(query.Surfaces),
		)
		if err != nil {
			writeObservabilityError(w, r, err)
			return
		}
		response := observabilityLogsResponse{
			Scope: query.Scope, Records: page.Records, Truncated: page.Truncated,
			DroppedAtIngest: page.DropCounts, CollectorHealth: collectorHealth,
		}
		if page.Truncated {
			token, err := cursorCodec.EncodeLogCursor(binding, "observability", binding, logrecords.Cursor{
				File: "postgres", Offset: int(page.NextID),
			})
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "observability cursor generation failed", nil)
				return
			}
			response.NextCursor = &opaqueLogCursor{Token: token}
		}
		WriteJSON(w, http.StatusOK, response)
	}
}

func humanObservabilityProjectAuthorizationMiddleware(access identity.AccessStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if !ok || principal.Kind != productPrincipalHuman {
			next.ServeHTTP(w, r)
			return
		}
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			// Query validation owns the missing-scope response.
			next.ServeHTTP(w, r)
			return
		}
		if access == nil || principal.Human.UserID == "" {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "observability tenant authorization is unavailable", nil)
			return
		}
		_, _, err := identity.Authorize(
			r.Context(), access, principal.Human.UserID,
			identity.AccessTarget{ProjectID: projectID}, identity.CapabilityProjectRead, false,
		)
		if err == nil {
			next.ServeHTTP(w, r)
			return
		}
		if tenantReadDenied(err) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "observability scope not found", nil)
			return
		}
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "observability tenant authorization is unavailable", nil)
	})
}

func collectorKindsForSurfaces(surfaces []string) []string {
	if len(surfaces) == 0 {
		return []string{observability.CollectorKindDocker, observability.CollectorKindTraefik}
	}
	wantsDocker, wantsTraefik := false, false
	for _, surface := range surfaces {
		switch strings.ToLower(strings.TrimSpace(surface)) {
		case "runtime":
			wantsDocker = true
		case "http":
			wantsTraefik = true
		}
	}
	kinds := make([]string, 0, 2)
	if wantsDocker {
		kinds = append(kinds, observability.CollectorKindDocker)
	}
	if wantsTraefik {
		kinds = append(kinds, observability.CollectorKindTraefik)
	}
	return kinds
}

func readCollectorHealth(ctx context.Context, store any, expectedKinds []string) ([]observability.CollectorHealth, error) {
	provider, ok := store.(observability.CollectorHealthStore)
	if !ok || len(expectedKinds) == 0 {
		return []observability.CollectorHealth{}, nil
	}
	return provider.CollectorHealth(ctx, expectedKinds, observability.DefaultHeartbeatAge)
}

func collectorHeartbeatGaps(kinds []string, reason string) []observability.CollectorHealth {
	health := make([]observability.CollectorHealth, 0, len(kinds))
	for _, kind := range kinds {
		health = append(health, observability.CollectorHealth{
			Kind: kind, Status: "unavailable", HeartbeatGap: true, Reason: reason,
		})
	}
	return health
}

func observabilityMetricsHandler(store observability.Store, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.LogStoreUnavailable, "durable metrics store is not configured", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		query, ok := parseObservabilityMetricQuery(w, r, clock)
		if !ok {
			return
		}
		series, err := store.QueryMetrics(r.Context(), query)
		if err != nil {
			writeObservabilityError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, series)
	}
}

func observabilityLogStreamHandler(store observability.Store, cursorCodec CursorCodec, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.LogStoreUnavailable, "durable observability store is not configured", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		query, rawCursor, ok := parseObservabilityLogQuery(w, r, clock, defaultSSEReplayWindow)
		if !ok {
			return
		}
		// A live tail has no upper bound. Since remains the durable replay
		// floor; AfterID advances monotonically after each delivered record.
		query.Until = time.Time{}
		if headerCursor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); headerCursor != "" {
			rawCursor = headerCursor
		}
		binding := observabilityQueryBinding(query)
		if rawCursor != "" {
			cursor, err := cursorCodec.DecodeLogCursor(rawCursor, binding, "observability", binding)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid observability stream cursor", nil)
				return
			}
			query.AfterID = int64(cursor.Offset)
			// The durable id is the replay boundary. Do not retain the
			// first-connect 15-minute floor: a client disconnected for longer
			// than that must still receive every committed row after its
			// Last-Event-ID (subject only to bounded retention).
			query.Since = time.Time{}
		}
		collectorKinds := collectorKindsForSurfaces(query.Surfaces)
		collectorHealth, err := readCollectorHealth(r.Context(), store, collectorKinds)
		if err != nil {
			writeObservabilityError(w, r, err)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "streaming is unavailable", nil)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		if err := writeSSECollectorHealth(w, collectorHealth); err != nil {
			return
		}
		flusher.Flush()

		poll := time.NewTicker(time.Second)
		defer poll.Stop()
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		for {
			page, err := store.QueryLogs(r.Context(), query)
			if err != nil {
				writeSSEError(w, failurecodes.LogStoreUnavailable, "durable log replay failed")
				flusher.Flush()
				return
			}
			for _, record := range page.Records {
				query.AfterID = record.ID
				cursor, err := cursorCodec.EncodeLogCursor(binding, "observability", binding, logrecords.Cursor{
					File: "postgres", Offset: int(record.ID),
				})
				if err != nil {
					return
				}
				data, err := json.Marshal(record)
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "id: %s\nevent: log\ndata: %s\n\n", cursor, data); err != nil {
					return
				}
			}
			if len(page.Records) > 0 {
				flusher.Flush()
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-poll.C:
			case <-keepalive.C:
				collectorHealth, err = readCollectorHealth(r.Context(), store, collectorKinds)
				if err != nil {
					writeSSEError(w, failurecodes.LogStoreUnavailable, "collector heartbeat query failed")
					flusher.Flush()
					return
				}
				if err := writeSSECollectorHealth(w, collectorHealth); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func writeSSECollectorHealth(w io.Writer, health []observability.CollectorHealth) error {
	data, err := json.Marshal(health)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: collector-health\ndata: %s\n\n", data)
	return err
}

func parseObservabilityLogQuery(w http.ResponseWriter, r *http.Request, clock func() time.Time, defaultWindow time.Duration) (observability.LogQuery, string, bool) {
	values := r.URL.Query()
	now := time.Now().UTC()
	if clock != nil {
		now = clock().UTC()
	}
	scope := observability.Scope{
		ProjectID:     strings.TrimSpace(values.Get("project_id")),
		EnvironmentID: strings.TrimSpace(values.Get("environment_id")),
		ServiceID:     strings.TrimSpace(values.Get("service_id")),
		DeploymentID:  strings.TrimSpace(values.Get("deployment_id")),
		OperationID:   strings.TrimSpace(values.Get("operation_id")),
		ReplicaID:     strings.TrimSpace(values.Get("replica_id")),
	}
	query := observability.LogQuery{
		Scope: scope, Search: strings.TrimSpace(values.Get("q")),
		Surfaces:   splitQueryValues(values["surface"]),
		Streams:    splitQueryValues(values["stream"]),
		Levels:     splitQueryValues(values["level"]),
		Components: splitQueryValues(values["component"]),
		Since:      now.Add(-defaultWindow), Until: now, Limit: observability.DefaultQueryLimit,
	}
	if len([]byte(query.Search)) > maxObservabilitySearchBytes ||
		len(query.Surfaces) > maxObservabilityFilterValues ||
		len(query.Streams) > maxObservabilityFilterValues ||
		len(query.Levels) > maxObservabilityFilterValues ||
		len(query.Components) > maxObservabilityFilterValues {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid,
			"observability log filters exceed bounded size", map[string]any{
				"max_search_bytes":  maxObservabilitySearchBytes,
				"max_filter_values": maxObservabilityFilterValues,
			})
		return observability.LogQuery{}, "", false
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > observability.MaxQueryLimit {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid observability log limit", map[string]any{"max": observability.MaxQueryLimit})
			return observability.LogQuery{}, "", false
		}
		query.Limit = limit
	}
	var err error
	if raw := strings.TrimSpace(values.Get("since")); raw != "" {
		query.Since, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid observability since timestamp", nil)
			return observability.LogQuery{}, "", false
		}
	}
	if raw := strings.TrimSpace(values.Get("until")); raw != "" {
		query.Until, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid observability until timestamp", nil)
			return observability.LogQuery{}, "", false
		}
	}
	if query.Until.Before(query.Since) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "observability until precedes since", nil)
		return observability.LogQuery{}, "", false
	}
	if values.Has("cursor_file") || values.Has("cursor_offset") {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "storage cursor details are not accepted; use the opaque cursor", nil)
		return observability.LogQuery{}, "", false
	}
	return query, strings.TrimSpace(values.Get("cursor")), true
}

func parseObservabilityMetricQuery(w http.ResponseWriter, r *http.Request, clock func() time.Time) (observability.MetricQuery, bool) {
	values := r.URL.Query()
	now := time.Now().UTC()
	if clock != nil {
		now = clock().UTC()
	}
	query := observability.MetricQuery{
		Scope: observability.Scope{
			ProjectID:     strings.TrimSpace(values.Get("project_id")),
			EnvironmentID: strings.TrimSpace(values.Get("environment_id")),
			ServiceID:     strings.TrimSpace(values.Get("service_id")),
			DeploymentID:  strings.TrimSpace(values.Get("deployment_id")),
			ReplicaID:     strings.TrimSpace(values.Get("replica_id")),
		},
		Since: now.Add(-time.Hour), Until: now, Mode: strings.TrimSpace(values.Get("mode")),
	}
	var err error
	if raw := strings.TrimSpace(values.Get("since")); raw != "" {
		query.Since, err = time.Parse(time.RFC3339Nano, raw)
	}
	if err == nil {
		if raw := strings.TrimSpace(values.Get("until")); raw != "" {
			query.Until, err = time.Parse(time.RFC3339Nano, raw)
		}
	}
	if err == nil {
		if raw := strings.TrimSpace(values.Get("step")); raw != "" {
			query.Step, err = time.ParseDuration(raw)
		}
	}
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid observability metrics time query", nil)
		return observability.MetricQuery{}, false
	}
	return query, true
}

func observabilityQueryBinding(query observability.LogQuery) string {
	return strings.Join([]string{
		query.Scope.ProjectID, query.Scope.EnvironmentID, query.Scope.ServiceID,
		query.Scope.DeploymentID, query.Scope.OperationID, query.Scope.ReplicaID,
		strings.Join(query.Surfaces, ","), strings.Join(query.Streams, ","),
		strings.Join(query.Levels, ","), strings.Join(query.Components, ","), query.Search,
	}, "\x00")
}

func splitQueryValues(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func writeObservabilityError(w http.ResponseWriter, r *http.Request, err error) {
	message := err.Error()
	if errors.Is(err, contextCanceledError(r)) {
		return
	}
	if strings.Contains(message, "store is unavailable") {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.LogStoreUnavailable,
			"durable observability storage is unavailable", nil)
		return
	}
	if observabilityClientError(message) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, message, nil)
		return
	}
	// Database/collector details can contain topology or driver data. Do not
	// return them; the request id in the standard envelope is the correlation
	// handle for server-side diagnostics.
	writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError,
		"observability query failed", nil)
}

func observabilityClientError(message string) bool {
	for _, exact := range []string{
		"project scope is required",
		"deployment scope requires service scope",
		"replica scope requires service scope",
		"observability scope does not resolve to one project graph",
		"metric query requires service scope",
		"metric mode must be sum or replica",
		"metric until must be after since",
	} {
		if message == exact {
			return true
		}
	}
	return strings.HasPrefix(message, "log limit exceeds ")
}

func contextCanceledError(r *http.Request) error {
	if r == nil {
		return nil
	}
	return r.Context().Err()
}

func writeSSEError(w http.ResponseWriter, code failurecodes.Code, message string) {
	payload, _ := json.Marshal(map[string]any{"code": code, "message": message})
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
}
