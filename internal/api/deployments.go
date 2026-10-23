// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	defaultDeploymentPageLimit = 50
	maxDeploymentPageLimit     = 100
	deploymentTruthCanonical   = "canonical"
	deploymentTruthLegacy      = "legacy"
)

type DeploymentResource struct {
	ID                   string                    `json:"id"`
	DeploymentID         string                    `json:"deployment_id,omitempty"`
	ServiceID            string                    `json:"service_id"`
	ProjectID            string                    `json:"project_id,omitempty"`
	EnvironmentID        string                    `json:"environment_id,omitempty"`
	InstanceID           string                    `json:"instance_id,omitempty"`
	Image                string                    `json:"image,omitempty"`
	Action               string                    `json:"action,omitempty"`
	SourceType           string                    `json:"source_type,omitempty"`
	SourceRepository     string                    `json:"source_repository,omitempty"`
	SourceRef            string                    `json:"source_ref,omitempty"`
	CommitSHA            string                    `json:"commit_sha,omitempty"`
	TriggerActorType     string                    `json:"trigger_actor_type,omitempty"`
	TriggerActorLabel    string                    `json:"trigger_actor_label,omitempty"`
	RouteIntent          string                    `json:"route_intent,omitempty"`
	Route                string                    `json:"route,omitempty"`
	RouteApplyState      router.RouteApplyState    `json:"route_apply_state,omitempty"`
	RouteReachable       bool                      `json:"route_reachable,omitempty"`
	OperationID          string                    `json:"operation_id"`
	OperationState       orailtypes.OperationState `json:"operation_state"`
	FailureCode          orailtypes.FailureCode    `json:"failure_code,omitempty"`
	FailureReason        string                    `json:"failure_reason,omitempty"`
	Status               string                    `json:"status"`
	Active               bool                      `json:"active"`
	RuntimeDesiredState  string                    `json:"runtime_desired_state,omitempty"`
	RuntimeObservedState string                    `json:"runtime_observed_state,omitempty"`
	RuntimeGeneration    int64                     `json:"runtime_generation,omitempty"`
	LastEvent            string                    `json:"last_event,omitempty"`
	LastEventAt          time.Time                 `json:"last_event_at,omitempty"`
	CreatedAt            *time.Time                `json:"created_at,omitempty"`
	StartedAt            *time.Time                `json:"started_at,omitempty"`
	CompletedAt          *time.Time                `json:"completed_at,omitempty"`
}

type DeploymentResourcePage struct {
	Items      []DeploymentResource `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

func serviceDeploymentsHandler(
	serviceStore ServiceStore,
	operationStore OperationStore,
	deploymentStore DeploymentStore,
	routeStore router.DomainWorkflowStore,
	cursorCodec CursorCodec,
) func(http.ResponseWriter, *http.Request, orailtypes.ID, string, string) {
	return func(w http.ResponseWriter, r *http.Request, serviceID orailtypes.ID, action string, deploymentID string) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"service_id": serviceID})
			return
		}
		query, ok := parseDeploymentReadQuery(w, r, action == "")
		if !ok {
			return
		}
		if query.Cursor != "" {
			beforeCreatedAt, beforeID, truthSource, err := cursorCodec.decodeDeploymentCursor(
				query.Cursor,
				string(serviceID),
				query.EnvironmentID,
			)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid deployment cursor", nil)
				return
			}
			query.BeforeCreatedAt = beforeCreatedAt
			query.BeforeID = beforeID
			query.CursorTruthSource = truthSource
		}
		service, err := serviceStore.GetService(r.Context(), serviceID)
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service lookup failed", map[string]any{"service_id": serviceID})
			return
		}
		switch action {
		case "":
			page, err := readDeploymentResourcePage(
				r,
				operationStore,
				deploymentStore,
				service,
				query,
			)
			if err == nil {
				err = enrichDeploymentGeneratedRoutes(r, routeStore, service, page.Resources)
			}
			if err != nil {
				if errors.Is(err, orailtypes.ErrInvalidCursorPage) {
					writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "deployment cursor no longer matches canonical truth", nil)
					return
				}
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment list failed", map[string]any{"service_id": serviceID})
				return
			}
			if page.HasMore && len(page.Resources) > 0 {
				page.NextCursor, err = cursorCodec.encodeDeploymentCursor(
					string(service.ID),
					query.EnvironmentID,
					page.TruthSource,
					page.LastCreatedAt,
					page.LastID,
				)
				if err != nil {
					writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment cursor encode failed", nil)
					return
				}
			}
			WriteJSON(w, http.StatusOK, DeploymentResourcePage{
				Items:      page.Resources,
				NextCursor: page.NextCursor,
			})
		case "latest-active":
			record, found, err := readExactDeploymentResource(
				r,
				operationStore,
				deploymentStore,
				service,
				query.EnvironmentID,
				"",
				true,
			)
			if err == nil && found {
				enriched := []DeploymentResource{record}
				err = enrichDeploymentGeneratedRoutes(r, routeStore, service, enriched)
				record = enriched[0]
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment lookup failed", map[string]any{"service_id": serviceID})
				return
			}
			if !found {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "active deployment not found", map[string]any{"service_id": serviceID})
				return
			}
			WriteJSON(w, http.StatusOK, record)
		case "get":
			record, found, err := readExactDeploymentResource(
				r,
				operationStore,
				deploymentStore,
				service,
				query.EnvironmentID,
				deploymentID,
				false,
			)
			if err == nil && found {
				enriched := []DeploymentResource{record}
				err = enrichDeploymentGeneratedRoutes(r, routeStore, service, enriched)
				record = enriched[0]
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment lookup failed", map[string]any{"service_id": serviceID, "deployment_id": deploymentID})
				return
			}
			if found {
				WriteJSON(w, http.StatusOK, record)
				return
			}
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment not found", map[string]any{"service_id": serviceID, "deployment_id": deploymentID})
		default:
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment not found", map[string]any{"service_id": serviceID})
		}
	}
}

func enrichDeploymentGeneratedRoutes(
	r *http.Request,
	store router.DomainWorkflowStore,
	service Service,
	resources []DeploymentResource,
) error {
	if store == nil || len(resources) == 0 {
		return nil
	}
	reader, ok := store.(router.GeneratedRouteProjectionStore)
	if !ok {
		return nil
	}
	deploymentIDs := make([]string, 0, len(resources))
	for _, resource := range resources {
		deploymentIDs = append(deploymentIDs, resource.DeploymentID)
	}
	values, err := reader.ListGeneratedRouteProjections(
		r.Context(),
		string(service.ProjectID),
		string(service.ID),
		deploymentIDs,
	)
	if err != nil {
		return err
	}
	byDeployment := make(map[string]router.GeneratedRouteProjection, len(values))
	for _, value := range values {
		byDeployment[value.DeploymentID] = value
	}
	for index := range resources {
		value, found := byDeployment[resources[index].DeploymentID]
		if !found {
			continue
		}
		resources[index].RouteIntent = value.Hostname
		resources[index].RouteApplyState = value.ApplyState
		resources[index].RouteReachable = value.Reachable
		if value.Reachable {
			resources[index].Route = "http://" + value.Hostname + value.Path
		} else {
			resources[index].Route = ""
		}
	}
	return nil
}

type deploymentReadQuery struct {
	EnvironmentID     string
	Limit             int
	Cursor            string
	BeforeCreatedAt   time.Time
	BeforeID          string
	CursorTruthSource string
}

func parseDeploymentReadQuery(
	w http.ResponseWriter,
	r *http.Request,
	allowPagination bool,
) (deploymentReadQuery, bool) {
	values := r.URL.Query()
	query := deploymentReadQuery{Limit: defaultDeploymentPageLimit}
	if environmentValues, present := values["environment_id"]; present {
		if len(environmentValues) != 1 ||
			strings.TrimSpace(environmentValues[0]) == "" {
			writeRequestError(
				w,
				r,
				http.StatusBadRequest,
				failurecodes.ResourceValidationFailed,
				"environment_id must be provided exactly once",
				map[string]any{"field": "environment_id"},
			)
			return deploymentReadQuery{}, false
		}
		query.EnvironmentID = strings.TrimSpace(environmentValues[0])
	}
	for _, key := range []string{"limit", "cursor"} {
		entries := values[key]
		if len(entries) > 1 {
			writeRequestError(
				w,
				r,
				http.StatusBadRequest,
				failurecodes.ResourceValidationFailed,
				key+" must be provided at most once",
				map[string]any{"field": key},
			)
			return deploymentReadQuery{}, false
		}
		if !allowPagination && len(entries) > 0 {
			writeRequestError(
				w,
				r,
				http.StatusBadRequest,
				failurecodes.ResourceValidationFailed,
				key+" is only supported on the deployment collection",
				map[string]any{"field": key},
			)
			return deploymentReadQuery{}, false
		}
	}
	if rawLimit := strings.TrimSpace(values.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > maxDeploymentPageLimit {
			writeRequestError(
				w,
				r,
				http.StatusBadRequest,
				failurecodes.ResourceValidationFailed,
				"limit must be an integer between 1 and "+strconv.Itoa(maxDeploymentPageLimit),
				map[string]any{"field": "limit"},
			)
			return deploymentReadQuery{}, false
		}
		query.Limit = limit
	} else if _, present := values["limit"]; present {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "limit must not be empty", map[string]any{"field": "limit"})
		return deploymentReadQuery{}, false
	}
	if rawCursor, present := values["cursor"]; present {
		query.Cursor = strings.TrimSpace(rawCursor[0])
		if query.Cursor == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "deployment cursor must not be empty", map[string]any{"field": "cursor"})
			return deploymentReadQuery{}, false
		}
	}
	return query, true
}

type deploymentResourceRecordPage struct {
	Resources     []DeploymentResource
	HasMore       bool
	NextCursor    string
	LastCreatedAt time.Time
	LastID        string
	TruthSource   string
}

func readDeploymentResourcePage(
	r *http.Request,
	operationStore OperationStore,
	deploymentStore DeploymentStore,
	service Service,
	query deploymentReadQuery,
) (deploymentResourceRecordPage, error) {
	if query.CursorTruthSource == deploymentTruthLegacy {
		if deploymentStore != nil {
			canonical, err := canonicalDeploymentTruthPresent(
				r,
				deploymentStore,
				string(service.ID),
			)
			if err != nil {
				return deploymentResourceRecordPage{}, err
			}
			if canonical {
				return deploymentResourceRecordPage{}, orailtypes.ErrInvalidCursorPage
			}
		}
		return readLegacyDeploymentResourcePage(r, operationStore, service, query)
	}
	filter := DeploymentReadFilter{
		ServiceID:       string(service.ID),
		EnvironmentID:   query.EnvironmentID,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        query.BeforeID,
		Limit:           query.Limit,
	}
	if deploymentStore != nil {
		var page DeploymentRecordPage
		var err error
		if reader, ok := deploymentStore.(DeploymentReadStore); ok {
			page, err = reader.ReadDeploymentPage(r.Context(), filter)
		} else {
			page, err = readDeploymentPageFromList(r.Context(), deploymentStore, filter)
		}
		if err != nil {
			return deploymentResourceRecordPage{}, err
		}
		if humanProductPrincipal(r.Context()) || len(page.Records) > 0 || query.Cursor != "" {
			result := deploymentResourceRecordPage{
				Resources:   deploymentResourcesFromRecords(service, page.Records),
				HasMore:     page.HasMore,
				TruthSource: deploymentTruthCanonical,
			}
			if len(page.Records) > 0 {
				last := page.Records[len(page.Records)-1]
				result.LastCreatedAt = last.CreatedAt
				result.LastID = last.ID
			}
			return result, nil
		}
		canonical, err := canonicalDeploymentTruthPresent(
			r,
			deploymentStore,
			string(service.ID),
		)
		if err != nil {
			return deploymentResourceRecordPage{}, err
		}
		if canonical {
			return deploymentResourceRecordPage{
				Resources:   make([]DeploymentResource, 0),
				TruthSource: deploymentTruthCanonical,
			}, nil
		}
	}
	if query.CursorTruthSource == deploymentTruthCanonical {
		return deploymentResourceRecordPage{
			Resources:   make([]DeploymentResource, 0),
			TruthSource: deploymentTruthCanonical,
		}, nil
	}
	return readLegacyDeploymentResourcePage(r, operationStore, service, query)
}

func readLegacyDeploymentResourcePage(
	r *http.Request,
	operationStore OperationStore,
	service Service,
	query deploymentReadQuery,
) (deploymentResourceRecordPage, error) {
	legacy, err := legacyDeploymentResourcesForService(r, operationStore, service)
	if err != nil {
		return deploymentResourceRecordPage{}, err
	}
	legacy = filterDeploymentsByEnvironment(legacy, query.EnvironmentID)
	sort.SliceStable(legacy, func(i, j int) bool {
		if legacy[i].LastEventAt.Equal(legacy[j].LastEventAt) {
			return legacy[i].ID > legacy[j].ID
		}
		return legacy[i].LastEventAt.After(legacy[j].LastEventAt)
	})
	resources := make([]DeploymentResource, 0, min(len(legacy), query.Limit+1))
	for _, deployment := range legacy {
		if !query.BeforeCreatedAt.IsZero() &&
			(deployment.LastEventAt.After(query.BeforeCreatedAt) ||
				(deployment.LastEventAt.Equal(query.BeforeCreatedAt) && deployment.ID >= query.BeforeID)) {
			continue
		}
		resources = append(resources, deployment)
		if len(resources) == query.Limit+1 {
			break
		}
	}
	hasMore := len(resources) > query.Limit
	if hasMore {
		resources = resources[:query.Limit]
	}
	result := deploymentResourceRecordPage{
		Resources:   resources,
		HasMore:     hasMore,
		TruthSource: deploymentTruthLegacy,
	}
	if len(resources) > 0 {
		last := resources[len(resources)-1]
		result.LastCreatedAt = last.LastEventAt
		result.LastID = last.ID
	}
	return result, nil
}

func readExactDeploymentResource(
	r *http.Request,
	operationStore OperationStore,
	deploymentStore DeploymentStore,
	service Service,
	environmentID string,
	deploymentID string,
	activeOnly bool,
) (DeploymentResource, bool, error) {
	filter := DeploymentReadFilter{
		ServiceID:     string(service.ID),
		EnvironmentID: environmentID,
		DeploymentID:  deploymentID,
		ActiveOnly:    activeOnly,
		Limit:         1,
	}
	if deploymentStore != nil {
		var page DeploymentRecordPage
		var err error
		if reader, ok := deploymentStore.(DeploymentReadStore); ok {
			page, err = reader.ReadDeploymentPage(r.Context(), filter)
		} else {
			page, err = readDeploymentPageFromList(r.Context(), deploymentStore, filter)
		}
		if err != nil {
			return DeploymentResource{}, false, err
		}
		if len(page.Records) > 0 {
			return deploymentResourceFromRecord(service, page.Records[0]), true, nil
		}
		if humanProductPrincipal(r.Context()) {
			return DeploymentResource{}, false, nil
		}
		canonical, err := canonicalDeploymentTruthPresent(
			r,
			deploymentStore,
			string(service.ID),
		)
		if err != nil {
			return DeploymentResource{}, false, err
		}
		if canonical {
			return DeploymentResource{}, false, nil
		}
	}
	legacy, err := legacyDeploymentResourcesForService(r, operationStore, service)
	if err != nil {
		return DeploymentResource{}, false, err
	}
	legacy = filterDeploymentsByEnvironment(legacy, environmentID)
	if activeOnly {
		deployment, found := latestActiveDeployment(legacy)
		return deployment, found, nil
	}
	for _, deployment := range legacy {
		if deployment.ID == deploymentID || deployment.DeploymentID == deploymentID {
			return deployment, true, nil
		}
	}
	return DeploymentResource{}, false, nil
}

func canonicalDeploymentTruthPresent(
	r *http.Request,
	deploymentStore DeploymentStore,
	serviceID string,
) (bool, error) {
	filter := DeploymentReadFilter{
		ServiceID: serviceID,
		Limit:     1,
	}
	var page DeploymentRecordPage
	var err error
	if reader, ok := deploymentStore.(DeploymentReadStore); ok {
		page, err = reader.ReadDeploymentPage(r.Context(), filter)
	} else {
		page, err = readDeploymentPageFromList(r.Context(), deploymentStore, filter)
	}
	if err != nil {
		return false, err
	}
	return len(page.Records) > 0, nil
}

func filterDeploymentsByEnvironment(
	deployments []DeploymentResource,
	environmentID string,
) []DeploymentResource {
	if environmentID == "" {
		return deployments
	}
	filtered := make([]DeploymentResource, 0, len(deployments))
	for _, deployment := range deployments {
		if deployment.EnvironmentID == environmentID {
			filtered = append(filtered, deployment)
		}
	}
	return filtered
}

func legacyDeploymentResourcesForService(
	r *http.Request,
	operationStore OperationStore,
	service Service,
) ([]DeploymentResource, error) {
	operations, err := operationStore.List(r.Context(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		return nil, err
	}
	deployments := make([]DeploymentResource, 0)
	for _, operation := range operations {
		events, err := operationStore.ListEvents(r.Context(), operation.ID)
		if err != nil {
			return nil, err
		}
		deployment, ok := deploymentResourceFromOperation(service, operation, events)
		if ok {
			deployments = append(deployments, deployment)
		}
	}
	return deployments, nil
}

func deploymentResourcesFromRecords(
	service Service,
	records []DeploymentRecord,
) []DeploymentResource {
	resources := make([]DeploymentResource, 0, len(records))
	for _, record := range records {
		resources = append(resources, deploymentResourceFromRecord(service, record))
	}
	return resources
}

func deploymentResourceFromOperation(service Service, operation orailtypes.Operation, events []OperationEvent) (DeploymentResource, bool) {
	var fields map[string]string
	var route string
	var lastEvent string
	var lastEventAt time.Time
	for _, event := range events {
		if strings.TrimSpace(event.Message) != "" {
			lastEvent = event.Message
			lastEventAt = event.CreatedAt
		}
		if parsedServiceID, parsedFields, ok := parseDashboardDeployIntakeEventMessage(event.Message); ok && parsedServiceID == string(service.ID) {
			fields = parsedFields
		}
		if promotedRoute, ok := parseDashboardPromoteRouteEventMessage(event.Message); ok {
			route = promotedRoute
		}
	}
	if fields == nil {
		return DeploymentResource{}, false
	}
	deploymentID := fields["deployment_id"]
	id := firstNonEmpty(deploymentID, string(operation.ID))
	active := route != ""
	return DeploymentResource{
		ID:             id,
		DeploymentID:   deploymentID,
		ServiceID:      string(service.ID),
		ProjectID:      firstNonEmpty(fields["project_id"], string(service.ProjectID)),
		EnvironmentID:  fields["environment_id"],
		InstanceID:     fields["instance_id"],
		Image:          fields["image"],
		RouteIntent:    fields["route_intent"],
		Route:          route,
		OperationID:    string(operation.ID),
		OperationState: operation.State,
		FailureCode:    operation.Code,
		FailureReason:  operation.Reason,
		Status:         deploymentStatus(operation.State, active),
		Active:         active,
		LastEvent:      lastEvent,
		LastEventAt:    lastEventAt,
	}, true
}

// deploymentResourceFromRecord maps a canonical deployment row to the API
// resource shape. Product truth (state/active) comes straight from the table —
// no event-string parsing.
func deploymentResourceFromRecord(service Service, rec DeploymentRecord) DeploymentResource {
	sourceRepository, sourceRef := deploymentSourceEvidence(rec)
	actorType, actorLabel := deploymentTriggerActor(rec, sourceRepository)
	return DeploymentResource{
		ID:                   rec.ID,
		DeploymentID:         rec.ID,
		ServiceID:            firstNonEmpty(rec.ServiceID, string(service.ID)),
		ProjectID:            firstNonEmpty(rec.ProjectID, string(service.ProjectID)),
		EnvironmentID:        rec.EnvironmentID,
		Image:                firstNonEmpty(rec.ImageRef, rec.ImageDigest),
		Action:               string(rec.Action),
		SourceType:           rec.SourceType,
		SourceRepository:     sourceRepository,
		SourceRef:            sourceRef,
		CommitSHA:            deploymentCommitSHA(rec),
		TriggerActorType:     actorType,
		TriggerActorLabel:    actorLabel,
		OperationID:          rec.OperationID,
		OperationState:       canonicalOperationState(rec.State),
		FailureCode:          orailtypes.FailureCode(rec.FailureCode),
		FailureReason:        rec.FailureMessage,
		Status:               canonicalDeploymentStatus(rec.State),
		Active:               rec.Active,
		RuntimeDesiredState:  rec.RuntimeDesiredState,
		RuntimeObservedState: rec.RuntimeObservedState,
		RuntimeGeneration:    rec.RuntimeGeneration,
		LastEventAt:          rec.UpdatedAt,
		CreatedAt:            deploymentEvidenceTime(rec.CreatedAt),
		StartedAt:            deploymentOptionalEvidenceTime(rec.StartedAt),
		CompletedAt:          deploymentOptionalEvidenceTime(rec.CompletedAt),
	}
}

func deploymentTriggerActor(rec DeploymentRecord, sourceRepository string) (string, string) {
	if rec.Action == deploy.ActionDeploySource &&
		strings.EqualFold(strings.TrimSpace(rec.SourceType), "github") &&
		sourceRepository != "" && exactCommitSHA(rec.Ref) != "" {
		return "provider_automation", "GitHub App"
	}
	return "", ""
}

// deploymentSourceEvidence projects only support-safe durable source
// identity. Current intake rejects credential-bearing URLs and reserves
// internal artifact/local schemes, but historical or directly seeded rows may
// predate those boundaries. Reads must not turn such rows into a disclosure.
func deploymentSourceEvidence(rec DeploymentRecord) (string, string) {
	repository := strings.TrimSpace(rec.Repo)
	ref := strings.TrimSpace(rec.Ref)
	if repository == "" || repository != rec.Repo || len(repository) > 2048 ||
		containsDeploymentEvidenceControl(repository) ||
		reservedDeploySourceScheme(repository) != "" {
		return "", ""
	}
	parsed, err := url.Parse(repository)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ""
	}
	if parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, "https") {
		return "", ""
	}
	if ref == "" || ref != rec.Ref || len(ref) > 255 || containsDeploymentEvidenceControl(ref) {
		ref = ""
	}
	return repository, ref
}

func containsDeploymentEvidenceControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return true
		}
	}
	return false
}

func deploymentEvidenceTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func deploymentOptionalEvidenceTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return deploymentEvidenceTime(*value)
}

func deploymentCommitSHA(rec DeploymentRecord) string {
	if value := exactCommitSHA(rec.SHA); value != "" {
		return value
	}
	if strings.EqualFold(strings.TrimSpace(rec.SourceType), "github") {
		return exactCommitSHA(rec.Ref)
	}
	return ""
}

func exactCommitSHA(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) != 40 && len(value) != 64 {
		return ""
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return ""
		}
	}
	return value
}

// canonicalDeploymentStatus renders a canonical deployment state as the API's
// uppercase status string.
func canonicalDeploymentStatus(state deploy.DeploymentState) string {
	switch state {
	case deploy.DeploymentQueued:
		return "QUEUED"
	case deploy.DeploymentBuilding:
		return "BUILDING"
	case deploy.DeploymentDeploying:
		return "DEPLOYING"
	case deploy.DeploymentActive:
		return "ACTIVE"
	case deploy.DeploymentSuperseded:
		return "SUPERSEDED"
	case deploy.DeploymentFailed:
		return "FAILED"
	case deploy.DeploymentCanceled:
		return "CANCELED"
	default:
		return strings.ToUpper(string(state))
	}
}

// canonicalOperationState maps a canonical deployment state to the operation
// state enum for backward-compatible response fields.
func canonicalOperationState(state deploy.DeploymentState) orailtypes.OperationState {
	switch state {
	case deploy.DeploymentActive, deploy.DeploymentSuperseded:
		return orailtypes.OperationSucceeded
	case deploy.DeploymentFailed:
		return orailtypes.OperationFailed
	case deploy.DeploymentCanceled:
		return orailtypes.OperationCanceled
	case deploy.DeploymentBuilding, deploy.DeploymentDeploying:
		return orailtypes.OperationRunning
	default:
		return orailtypes.OperationPending
	}
}

func latestActiveDeployment(deployments []DeploymentResource) (DeploymentResource, bool) {
	var latest DeploymentResource
	for _, deployment := range deployments {
		if !deployment.Active {
			continue
		}
		if latest.ID == "" || deployment.LastEventAt.After(latest.LastEventAt) {
			latest = deployment
		}
	}
	return latest, latest.ID != ""
}

func deploymentStatus(state orailtypes.OperationState, active bool) string {
	if active {
		return "active"
	}
	switch state {
	case orailtypes.OperationQueued:
		return "queued"
	case orailtypes.OperationRunning:
		return "deploying"
	case orailtypes.OperationSucceeded:
		return "succeeded"
	case orailtypes.OperationFailed:
		return "failed"
	case orailtypes.OperationCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}
