package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const defaultOperationLogsLimit = 100

type LogQueryStore interface {
	Query(filter logrecords.Filter, cursor logrecords.Cursor, pageSize int) (logrecords.QueryResult, error)
}

type operationLogsResponse struct {
	OperationID     orailtypes.ID                   `json:"operation_id"`
	OperationEvents operationEventsRef              `json:"operation_events"`
	DeployLogs      operationEventsRef              `json:"deploy_logs"`
	AppLogs         []appLogSurface                 `json:"app_logs"`
	PlatformLogs    []appLogSurface                 `json:"platform_logs"`
	CollectorHealth []observability.CollectorHealth `json:"collector_health"`
	Message         string                          `json:"message"`
}

type operationEventsRef struct {
	Available bool   `json:"available"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Count     int    `json:"count"`
}

type appLogSurface struct {
	Surface      string             `json:"surface"`
	Available    bool               `json:"available"`
	Reason       string             `json:"reason,omitempty"`
	ReasonCode   failurecodes.Code  `json:"reason_code,omitempty"`
	RecoveryHint string             `json:"recovery_hint,omitempty"`
	Count        int                `json:"count"`
	Entries      []logrecords.Entry `json:"entries,omitempty"`
	NextCursor   *opaqueLogCursor   `json:"next_cursor,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
}

type opaqueLogCursor struct {
	Token string `json:"token"`
}

type operationLogsQuery struct {
	Limit    int
	FreeText string
	Surface  string
	Cursor   logrecords.Cursor
}

func writeOperationLogs(w http.ResponseWriter, r *http.Request, operationStore OperationStore, logStore LogQueryStore, cursorCodec CursorCodec, operationID orailtypes.ID) {
	query, ok := parseOperationLogsQuery(w, r, cursorCodec, operationID)
	if !ok {
		return
	}
	if _, err := operationStore.Get(r.Context(), operationID); errors.Is(err, ErrOperationNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
		return
	} else if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation lookup failed", map[string]any{"operation_id": operationID})
		return
	}

	events, err := operationStore.ListEvents(r.Context(), operationID)
	if errors.Is(err, ErrOperationNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation events lookup failed", map[string]any{"operation_id": operationID})
		return
	}
	operationEvents := operationEventsRef{
		Available: true,
		Kind:      "operation_events",
		URL:       v1alphaPrefix + "/operations/" + string(operationID) + "/events",
		Count:     len(events),
	}
	var deploymentIDs []string
	if principal, human := productPrincipalFromContext(r.Context()); human &&
		principal.Kind == productPrincipalHuman {
		scope, scoped := tenantOperationScopeFromContext(r.Context())
		if !scoped || scope.OperationID != operationID {
			writeRequestError(
				w,
				r,
				http.StatusNotFound,
				failurecodes.OperationNotFound,
				"operation not found",
				map[string]any{"operation_id": operationID},
			)
			return
		}
		// Human app-log expansion is pinned to the canonical deployment scope
		// proven by tenant authorization. Operation event messages are
		// free-form telemetry and must never widen this filter.
		deploymentIDs = []string{scope.DeploymentID}
	} else {
		deploymentIDs = deploymentIDsFromOperationEvents(events)
	}
	appLogs := queryAppLogSurfaces(logStore, cursorCodec, operationID, deploymentIDs, query)
	platformLogs := queryPlatformLogSurfaces(logStore, cursorCodec, operationID, query)
	expectedCollectors := []string{observability.CollectorKindDocker, observability.CollectorKindTraefik}
	collectorHealth, healthErr := readCollectorHealth(
		r.Context(), logStore,
		expectedCollectors,
	)
	if healthErr != nil {
		collectorHealth = collectorHeartbeatGaps(expectedCollectors, "collector heartbeat state is unavailable")
	}

	WriteJSON(w, http.StatusOK, operationLogsResponse{
		OperationID:     operationID,
		OperationEvents: operationEvents,
		DeployLogs:      operationEvents,
		AppLogs:         appLogs,
		PlatformLogs:    platformLogs,
		CollectorHealth: collectorHealth,
		Message:         operationLogsMessage(logStore),
	})
}

func unavailableAppLogSurface(surface string) appLogSurface {
	return appLogSurface{
		Surface:      surface,
		Available:    false,
		Reason:       "unavailable: no backed app log store is wired for this surface",
		ReasonCode:   failurecodes.LogStoreUnavailable,
		RecoveryHint: failurecodes.RecoveryHint(failurecodes.LogStoreUnavailable),
	}
}

func unavailablePlatformLogSurface(surface string) appLogSurface {
	return appLogSurface{
		Surface:      surface,
		Available:    false,
		Reason:       "unavailable: no backed platform log store is wired for this surface",
		ReasonCode:   failurecodes.LogStoreUnavailable,
		RecoveryHint: failurecodes.RecoveryHint(failurecodes.LogStoreUnavailable),
	}
}

func parseOperationLogsQuery(w http.ResponseWriter, r *http.Request, cursorCodec CursorCodec, operationID orailtypes.ID) (operationLogsQuery, bool) {
	query := operationLogsQuery{Limit: defaultOperationLogsLimit}
	values := r.URL.Query()
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > orailtypes.MaxPageLimit {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid log limit", map[string]any{"limit": raw, "max": orailtypes.MaxPageLimit})
			return operationLogsQuery{}, false
		}
		query.Limit = limit
	}
	query.FreeText = strings.TrimSpace(values.Get("q"))
	query.Surface = strings.TrimSpace(values.Get("surface"))
	if query.Surface != "" && !isKnownLogSurface(query.Surface) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "invalid log cursor surface", map[string]any{"surface": query.Surface})
		return operationLogsQuery{}, false
	}
	if values.Has("cursor_file") || values.Has("cursor_offset") {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "file-backed log cursors are not accepted; use the opaque cursor returned by the API", nil)
		return operationLogsQuery{}, false
	}
	rawCursor := strings.TrimSpace(values.Get("cursor"))
	if rawCursor != "" && query.Surface == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.LogQueryInvalid, "log cursor surface is required when cursor is set", nil)
		return operationLogsQuery{}, false
	}
	if rawCursor != "" {
		cursor, err := cursorCodec.DecodeLogCursor(rawCursor, string(operationID), query.Surface, query.FreeText)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid log cursor", nil)
			return operationLogsQuery{}, false
		}
		query.Cursor = cursor
	}
	return query, true
}

func queryAppLogSurfaces(store LogQueryStore, cursorCodec CursorCodec, operationID orailtypes.ID, deploymentIDs []string, query operationLogsQuery) []appLogSurface {
	surfaces := []string{"build", "runtime", "http"}
	out := make([]appLogSurface, 0, len(surfaces))
	for _, surface := range surfaces {
		if store == nil {
			out = append(out, unavailableAppLogSurface(surface))
			continue
		}
		filter := logrecords.Filter{
			Components: appLogComponents(surface),
			FreeText:   query.FreeText,
		}
		if len(deploymentIDs) > 0 {
			filter.Deployments = deploymentIDs
		} else {
			filter.Operations = []string{string(operationID)}
		}
		out = append(out, queryLogSurface(store, cursorCodec, operationID, surface, filter, query))
	}
	return out
}

func queryPlatformLogSurfaces(store LogQueryStore, cursorCodec CursorCodec, operationID orailtypes.ID, query operationLogsQuery) []appLogSurface {
	const surface = "platform"
	if store == nil {
		return []appLogSurface{unavailablePlatformLogSurface(surface)}
	}
	filter := logrecords.Filter{
		Components: platformLogComponents(),
		Operations: []string{string(operationID)},
		FreeText:   query.FreeText,
	}
	return []appLogSurface{queryLogSurface(store, cursorCodec, operationID, surface, filter, query)}
}

func queryLogSurface(store LogQueryStore, cursorCodec CursorCodec, operationID orailtypes.ID, surface string, filter logrecords.Filter, query operationLogsQuery) appLogSurface {
	cursor := logrecords.Cursor{}
	if query.Surface == "" || query.Surface == surface {
		cursor = query.Cursor
	}
	result, err := store.Query(filter, cursor, query.Limit)
	if err != nil {
		return appLogSurface{
			Surface:      surface,
			Available:    false,
			Reason:       "unavailable: log store query failed: " + err.Error(),
			ReasonCode:   failurecodes.LogStoreUnavailable,
			RecoveryHint: failurecodes.RecoveryHint(failurecodes.LogStoreUnavailable),
		}
	}
	out := appLogSurface{
		Surface:   surface,
		Available: true,
		Count:     len(result.Entries),
		Entries:   result.Entries,
		Truncated: result.Truncated,
	}
	if result.Truncated {
		token, err := cursorCodec.EncodeLogCursor(string(operationID), surface, query.FreeText, result.NextCursor)
		if err != nil {
			return appLogSurface{
				Surface:      surface,
				Available:    false,
				Reason:       "unavailable: log cursor generation failed",
				ReasonCode:   failurecodes.LogStoreUnavailable,
				RecoveryHint: failurecodes.RecoveryHint(failurecodes.LogStoreUnavailable),
			}
		}
		out.NextCursor = &opaqueLogCursor{Token: token}
	}
	return out
}

func isKnownLogSurface(surface string) bool {
	switch surface {
	case "build", "runtime", "http", "platform":
		return true
	default:
		return false
	}
}

func appLogComponents(surface string) []string {
	switch surface {
	case "build":
		return []string{"build", "buildkit", "app.build"}
	case "runtime":
		return []string{"runtime", "app", "container", "docker", "app.runtime"}
	case "http":
		return []string{"http", "router", "traefik", "ingress", "app.http"}
	default:
		return []string{surface}
	}
}

func platformLogComponents() []string {
	return []string{
		"platform",
		"server",
		"worker",
		"agent",
		"registry",
		"buildkit",
		"midnight-server",
		"midnight-worker",
		"midnight-agent",
		"midnight-registry",
		"midnight-buildkit",
	}
}

func deploymentIDsFromOperationEvents(events []OperationEvent) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1)
	for _, event := range events {
		fields := fieldsFromDashboardEvent(event.Message)
		if value := strings.TrimSpace(fields["deployment_id"]); value != "" {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				out = append(out, value)
			}
		}
	}
	return out
}

func operationLogsMessage(store LogQueryStore) string {
	if store == nil {
		return "Operation events are available. Build, runtime, and HTTP app logs are not wired to a backing log store in this API surface yet."
	}
	return "Operation events and configured app/platform log surfaces are returned from backed stores. Empty surfaces mean no matching real log entries were found."
}
