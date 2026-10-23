// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const EnvironmentResourceSummarySchema = "midnight.environment-resources/v1alpha1"

// EnvironmentDeploymentBatchStore is the optional canonical read capability
// used by the environment canvas. PostgresDeploymentStore implements it with
// one environment-scoped query, avoiding one latest-deployment query per
// service.
type EnvironmentDeploymentBatchStore interface {
	ListDeploymentsByEnvironment(ctx context.Context, projectID, environmentID string) ([]DeploymentRecord, error)
}

// PersistedEndpointEvidence is deliberately separate from deploy intent and
// service metadata. Implementations return only endpoints whose publication
// has been durably observed; an intended hostname is not serving evidence.
type PersistedEndpointEvidence struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	DeploymentID  string
	Host          string
	URL           string
	TLS           bool
	EvidenceAt    time.Time
	Persisted     bool
}

type EnvironmentEndpointEvidenceStore interface {
	ListPersistedEndpointsByEnvironment(ctx context.Context, projectID, environmentID string) ([]PersistedEndpointEvidence, error)
}

// PersistedReplicaEvidence is the durable per-deployment runtime projection
// used by the environment canvas. Counts come from deployment_instances; they
// are never inferred from deployment state or requested service configuration.
type PersistedReplicaEvidence struct {
	ProjectID       string
	EnvironmentID   string
	ServiceID       string
	DeploymentID    string
	DesiredReplicas int
	ReadyReplicas   int
	RunningReplicas int
	EvidenceAt      time.Time
}

type EnvironmentReplicaEvidenceStore interface {
	ListPersistedReplicasByEnvironment(ctx context.Context, projectID, environmentID string) ([]PersistedReplicaEvidence, error)
}

// PersistedSourceEvidence is canonical configured source truth. The latest
// deployment still carries the exact release revision; this record answers
// which source is currently connected even before the first deployment.
type PersistedSourceEvidence struct {
	ProjectID      string
	ServiceID      string
	Type           string
	Repository     string
	Ref            string
	WatchPaths     []string
	RootDir        string
	ConfigPath     string
	WaitForCI      bool
	RequiredChecks []string
	AutoDeploy     bool
	VerifiedAt     time.Time
	UpdatedAt      time.Time
	TruthSource    string
}

type EnvironmentSourceEvidenceStore interface {
	ListPersistedSourcesByProject(ctx context.Context, workspaceID, projectID string) ([]PersistedSourceEvidence, error)
}

type EnvironmentResourceSummaryDependencies struct {
	Projects     ProjectStore
	Environments EnvironmentStore
	Services     ServiceStore
	Deployments  DeploymentStore
	Operations   OperationStore
	Endpoints    EnvironmentEndpointEvidenceStore
	Replicas     EnvironmentReplicaEvidenceStore
	Sources      EnvironmentSourceEvidenceStore
}

type EnvironmentResourceSummary struct {
	SchemaVersion string                         `json:"schema_version"`
	Project       EnvironmentResourceProject     `json:"project"`
	Environment   EnvironmentResourceEnvironment `json:"environment"`
	Resources     []EnvironmentServiceResource   `json:"resources"`
}

type EnvironmentResourceProject struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type EnvironmentResourceEnvironment struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Tier        string `json:"tier"`
	IsProtected bool   `json:"is_protected"`
}

type EnvironmentServiceResource struct {
	Service          EnvironmentResourceService    `json:"service"`
	LatestDeployment *EnvironmentDeploymentSummary `json:"latest_deployment,omitempty"`
	ActiveDeployment *EnvironmentDeploymentSummary `json:"active_deployment,omitempty"`
	Source           *EnvironmentSourceSummary     `json:"source,omitempty"`
	Endpoint         *EnvironmentEndpointSummary   `json:"endpoint,omitempty"`
	PrivateEndpoint  EnvironmentPrivateEndpoint    `json:"private_endpoint"`
}

type EnvironmentResourceService struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type EnvironmentDeploymentSummary struct {
	ID                   string                       `json:"id"`
	Status               string                       `json:"status"`
	Active               bool                         `json:"active"`
	Image                string                       `json:"image,omitempty"`
	Action               string                       `json:"action,omitempty"`
	SourceType           string                       `json:"source_type,omitempty"`
	Repository           string                       `json:"source_repository,omitempty"`
	SourceRef            string                       `json:"source_ref,omitempty"`
	Revision             string                       `json:"commit_sha,omitempty"`
	RootDir              string                       `json:"root_directory,omitempty"`
	DockerfilePath       string                       `json:"dockerfile_path,omitempty"`
	Builder              string                       `json:"builder,omitempty"`
	RuntimeDesiredState  string                       `json:"runtime_desired_state,omitempty"`
	RuntimeObservedState string                       `json:"runtime_observed_state,omitempty"`
	RuntimeGeneration    int64                        `json:"runtime_generation,omitempty"`
	Replicas             *EnvironmentReplicaSummary   `json:"replicas,omitempty"`
	Operation            *EnvironmentOperationSummary `json:"operation,omitempty"`
	CreatedAt            time.Time                    `json:"created_at,omitempty"`
	UpdatedAt            time.Time                    `json:"updated_at,omitempty"`
	StartedAt            *time.Time                   `json:"started_at,omitempty"`
	CompletedAt          *time.Time                   `json:"completed_at,omitempty"`
	TruthSource          string                       `json:"truth_source"`
}

type EnvironmentReplicaSummary struct {
	Desired     int       `json:"desired"`
	Ready       int       `json:"ready"`
	Running     int       `json:"running"`
	Health      string    `json:"health"`
	EvidenceAt  time.Time `json:"evidence_at"`
	TruthSource string    `json:"truth_source"`
}

type EnvironmentSourceSummary struct {
	Type           string    `json:"type"`
	Repository     string    `json:"repository,omitempty"`
	Ref            string    `json:"ref,omitempty"`
	Revision       string    `json:"revision,omitempty"`
	WatchPaths     []string  `json:"watch_paths,omitempty"`
	RootDir        string    `json:"root_directory,omitempty"`
	ConfigPath     string    `json:"config_path,omitempty"`
	WaitForCI      *bool     `json:"wait_for_ci,omitempty"`
	RequiredChecks []string  `json:"required_checks,omitempty"`
	AutoDeploy     *bool     `json:"auto_deploy,omitempty"`
	VerifiedAt     time.Time `json:"verified_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	TruthSource    string    `json:"truth_source"`
}

type EnvironmentOperationSummary struct {
	ID            string                    `json:"id"`
	State         orailtypes.OperationState `json:"state"`
	FailureCode   orailtypes.FailureCode    `json:"failure_code,omitempty"`
	FailureReason string                    `json:"failure_reason,omitempty"`
}

type EnvironmentEndpointSummary struct {
	DeploymentID string    `json:"deployment_id"`
	Host         string    `json:"host,omitempty"`
	URL          string    `json:"url,omitempty"`
	TLS          bool      `json:"tls"`
	EvidenceAt   time.Time `json:"evidence_at"`
}

// EnvironmentPrivateEndpoint is deterministic service-discovery identity,
// not a reachability assertion. Runtime readiness remains in Replicas.
type EnvironmentPrivateEndpoint struct {
	Host        string `json:"host"`
	TruthSource string `json:"truth_source"`
}

// BuildEnvironmentResourceSummary returns one durable, environment-scoped read
// model for all services on the canvas. Canonical deployment rows win per
// service; operation-event reconstruction is used only for services with no
// canonical row in this environment.
func BuildEnvironmentResourceSummary(
	ctx context.Context,
	deps EnvironmentResourceSummaryDependencies,
	environmentID orailtypes.ID,
) (EnvironmentResourceSummary, error) {
	if deps.Projects == nil || deps.Environments == nil || deps.Services == nil {
		return EnvironmentResourceSummary{}, errors.New("environment resource summary stores are not configured")
	}
	if environmentID == "" {
		return EnvironmentResourceSummary{}, fmt.Errorf("%w: environment id is required", ErrResourceInvalid)
	}

	environment, err := deps.Environments.GetEnvironment(ctx, environmentID)
	if err != nil {
		return EnvironmentResourceSummary{}, err
	}
	project, err := deps.Projects.GetProject(ctx, environment.ProjectID)
	if err != nil {
		return EnvironmentResourceSummary{}, err
	}
	services, err := deps.Services.ListServices(ctx, ResourceListFilter{ParentID: project.ID})
	if err != nil {
		return EnvironmentResourceSummary{}, err
	}
	sort.SliceStable(services, func(i, j int) bool {
		if services[i].Slug == services[j].Slug {
			return services[i].ID < services[j].ID
		}
		return services[i].Slug < services[j].Slug
	})

	latest := make(map[string]EnvironmentDeploymentSummary, len(services))
	active := make(map[string]EnvironmentDeploymentSummary, len(services))
	canonicalByID := make(map[string]EnvironmentDeploymentSummary)
	activeDeployments := make(map[string]map[string]struct{}, len(services))
	if batch, ok := deps.Deployments.(EnvironmentDeploymentBatchStore); ok && batch != nil {
		records, err := batch.ListDeploymentsByEnvironment(ctx, string(project.ID), string(environment.ID))
		if err != nil {
			return EnvironmentResourceSummary{}, err
		}
		for _, record := range records {
			if record.ProjectID != string(project.ID) || record.EnvironmentID != string(environment.ID) {
				continue
			}
			if record.Active {
				addActiveDeployment(activeDeployments, record.ServiceID, record.ID)
			}
			candidate := canonicalEnvironmentDeployment(record)
			canonicalByID[record.ID] = candidate
			current, exists := latest[record.ServiceID]
			if !exists || environmentDeploymentNewer(candidate, current) {
				latest[record.ServiceID] = candidate
			}
			if record.Active {
				currentActive, activeExists := active[record.ServiceID]
				if !activeExists || environmentDeploymentNewer(candidate, currentActive) {
					active[record.ServiceID] = candidate
				}
			}
		}
	}

	missing := make(map[string]Service)
	for _, service := range services {
		if _, ok := latest[string(service.ID)]; !ok {
			missing[string(service.ID)] = service
		}
	}
	if len(missing) > 0 && deps.Operations != nil {
		legacy, legacyActive, err := legacyEnvironmentDeployments(
			ctx,
			deps.Operations,
			string(project.ID),
			string(environment.ID),
			missing,
		)
		if err != nil {
			return EnvironmentResourceSummary{}, err
		}
		for serviceID, deployment := range legacy {
			latest[serviceID] = deployment
			if deployment.Active {
				active[serviceID] = deployment
			}
		}
		for serviceID, deploymentIDs := range legacyActive {
			for deploymentID := range deploymentIDs {
				addActiveDeployment(activeDeployments, serviceID, deploymentID)
			}
		}
	}

	replicaEvidence := map[string]EnvironmentReplicaSummary{}
	if deps.Replicas != nil {
		items, err := deps.Replicas.ListPersistedReplicasByEnvironment(
			ctx,
			string(project.ID),
			string(environment.ID),
		)
		if err != nil {
			return EnvironmentResourceSummary{}, err
		}
		for _, item := range items {
			if item.ProjectID != string(project.ID) ||
				item.EnvironmentID != string(environment.ID) ||
				item.ServiceID == "" ||
				item.DeploymentID == "" ||
				item.DesiredReplicas < 1 ||
				item.ReadyReplicas < 0 ||
				item.RunningReplicas < 0 ||
				item.EvidenceAt.IsZero() {
				continue
			}
			deployment, exists := canonicalByID[item.DeploymentID]
			if !exists {
				continue
			}
			replicaEvidence[item.DeploymentID] = EnvironmentReplicaSummary{
				Desired:     item.DesiredReplicas,
				Ready:       item.ReadyReplicas,
				Running:     item.RunningReplicas,
				Health:      environmentReplicaHealth(deployment, item),
				EvidenceAt:  item.EvidenceAt.UTC(),
				TruthSource: "deployment-instances",
			}
		}
	}
	for serviceID, deployment := range latest {
		if replicas, ok := replicaEvidence[deployment.ID]; ok {
			copy := replicas
			deployment.Replicas = &copy
			latest[serviceID] = deployment
		}
	}
	for serviceID, deployment := range active {
		if replicas, ok := replicaEvidence[deployment.ID]; ok {
			copy := replicas
			deployment.Replicas = &copy
			active[serviceID] = deployment
		}
	}

	sources := map[string]EnvironmentSourceSummary{}
	if deps.Sources != nil {
		items, err := deps.Sources.ListPersistedSourcesByProject(
			ctx,
			project.WorkspaceID,
			string(project.ID),
		)
		if err != nil {
			return EnvironmentResourceSummary{}, err
		}
		for _, item := range items {
			if item.ProjectID != string(project.ID) ||
				item.ServiceID == "" ||
				strings.TrimSpace(item.Type) == "" {
				continue
			}
			autoDeploy := item.AutoDeploy
			waitForCI := item.WaitForCI
			sources[item.ServiceID] = EnvironmentSourceSummary{
				Type:           strings.TrimSpace(item.Type),
				Repository:     strings.TrimSpace(item.Repository),
				Ref:            strings.TrimSpace(item.Ref),
				WatchPaths:     append([]string(nil), item.WatchPaths...),
				RootDir:        strings.TrimSpace(item.RootDir),
				ConfigPath:     strings.TrimSpace(item.ConfigPath),
				WaitForCI:      &waitForCI,
				RequiredChecks: append([]string(nil), item.RequiredChecks...),
				AutoDeploy:     &autoDeploy,
				VerifiedAt:     item.VerifiedAt.UTC(),
				UpdatedAt:      item.UpdatedAt.UTC(),
				TruthSource:    firstNonEmpty(strings.TrimSpace(item.TruthSource), "canonical-source-link"),
			}
		}
	}
	for serviceID, deployment := range latest {
		source, exists := sources[serviceID]
		if !exists {
			source = sourceSummaryFromDeployment(deployment)
			if source.Type == "" {
				continue
			}
		}
		if source.Revision == "" &&
			strings.EqualFold(strings.TrimSpace(source.Repository), strings.TrimSpace(deployment.Repository)) &&
			(source.Ref == "" || deployment.SourceRef == "" || source.Ref == deployment.SourceRef) {
			source.Revision = deployment.Revision
		}
		sources[serviceID] = source
	}

	endpoints := map[string]EnvironmentEndpointSummary{}
	if deps.Endpoints != nil {
		evidence, err := deps.Endpoints.ListPersistedEndpointsByEnvironment(
			ctx,
			string(project.ID),
			string(environment.ID),
		)
		if err != nil {
			return EnvironmentResourceSummary{}, err
		}
		endpoints = selectPersistedEndpoints(
			evidence,
			string(project.ID),
			string(environment.ID),
			activeDeployments,
		)
	}

	resources := make([]EnvironmentServiceResource, 0, len(services))
	for _, service := range services {
		serviceID := string(service.ID)
		resource := EnvironmentServiceResource{
			Service: EnvironmentResourceService{
				ID:          serviceID,
				ProjectID:   string(service.ProjectID),
				Slug:        service.Slug,
				DisplayName: service.DisplayName,
			},
			PrivateEndpoint: EnvironmentPrivateEndpoint{
				Host:        service.Slug + ".midnight.internal",
				TruthSource: "derived-service-dns",
			},
		}
		if deployment, ok := latest[serviceID]; ok {
			copy := deployment
			resource.LatestDeployment = &copy
		}
		if deployment, ok := active[serviceID]; ok {
			copy := deployment
			resource.ActiveDeployment = &copy
		}
		if source, ok := sources[serviceID]; ok {
			copy := source
			resource.Source = &copy
		}
		if endpoint, ok := endpoints[serviceID]; ok {
			copy := endpoint
			resource.Endpoint = &copy
		}
		resources = append(resources, resource)
	}

	return EnvironmentResourceSummary{
		SchemaVersion: EnvironmentResourceSummarySchema,
		Project: EnvironmentResourceProject{
			ID:          string(project.ID),
			Slug:        project.Slug,
			DisplayName: project.DisplayName,
		},
		Environment: EnvironmentResourceEnvironment{
			ID:          string(environment.ID),
			ProjectID:   string(environment.ProjectID),
			Slug:        environment.Slug,
			Name:        environment.Name,
			Tier:        environment.Tier,
			IsProtected: environment.IsProtected,
		},
		Resources: resources,
	}, nil
}

// EnvironmentResourceSummaryHandler is ready to wire at
// GET /api/v1alpha/environments/{environment_id}/resources.
func EnvironmentResourceSummaryHandler(deps EnvironmentResourceSummaryDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		if environmentID == "" {
			environmentID = environmentResourceIDFromPath(r.URL.Path)
		}
		if environmentID == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", nil)
			return
		}
		summary, err := BuildEnvironmentResourceSummary(r.Context(), deps, orailtypes.ID(environmentID))
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", map[string]any{"environment_id": environmentID})
			return
		}
		if errors.Is(err, ErrResourceInvalid) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment resource summary request is invalid", map[string]any{"environment_id": environmentID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "environment resource summary failed", map[string]any{"environment_id": environmentID})
			return
		}
		WriteJSON(w, http.StatusOK, summary)
	}
}

func (s *PostgresDeploymentStore) ListDeploymentsByEnvironment(
	ctx context.Context,
	projectID, environmentID string,
) ([]DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("deployments by environment: project and environment are required")
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, project_id, service_id, environment_id, action,
		        source_type, repo, ref, sha, root_directory, dockerfile_path, builder,
		        image_ref, image_digest, state, active, COALESCE(operation_id, ''),
		        failure_code, failure_message,
		        runtime_desired_state, runtime_observed_state, runtime_generation,
		        created_at, updated_at, started_at, completed_at
		   FROM deployments
		  WHERE project_id = $1 AND environment_id = $2
		  ORDER BY service_id, created_at DESC, id DESC`,
		projectID,
		environmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("deployments by environment: %w", err)
	}
	defer rows.Close()
	out := make([]DeploymentRecord, 0)
	for rows.Next() {
		var record DeploymentRecord
		var action, state string
		if err := rows.Scan(
			&record.ID,
			&record.ProjectID,
			&record.ServiceID,
			&record.EnvironmentID,
			&action,
			&record.SourceType,
			&record.Repo,
			&record.Ref,
			&record.SHA,
			&record.RootDirectory,
			&record.DockerfilePath,
			&record.Builder,
			&record.ImageRef,
			&record.ImageDigest,
			&state,
			&record.Active,
			&record.OperationID,
			&record.FailureCode,
			&record.FailureMessage,
			&record.RuntimeDesiredState,
			&record.RuntimeObservedState,
			&record.RuntimeGeneration,
			&record.CreatedAt,
			&record.UpdatedAt,
			&record.StartedAt,
			&record.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("deployments by environment scan: %w", err)
		}
		record.Action = deploy.DeploymentAction(action)
		record.State = deploy.DeploymentState(state)
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deployments by environment rows: %w", err)
	}
	return out, nil
}

func (s *MemoryDeploymentStore) ListDeploymentsByEnvironment(
	ctx context.Context,
	projectID, environmentID string,
) ([]DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("MemoryDeploymentStore: not initialized")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("deployments by environment: project and environment are required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeploymentRecord, 0)
	for _, record := range s.rows {
		if record.ProjectID == projectID && record.EnvironmentID == environmentID {
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ServiceID != out[j].ServiceID {
			return out[i].ServiceID < out[j].ServiceID
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func canonicalEnvironmentDeployment(record DeploymentRecord) EnvironmentDeploymentSummary {
	summary := EnvironmentDeploymentSummary{
		ID:                   record.ID,
		Status:               canonicalDeploymentStatus(record.State),
		Active:               record.Active,
		Image:                firstNonEmpty(record.ImageRef, record.ImageDigest),
		Action:               string(record.Action),
		SourceType:           record.SourceType,
		Repository:           record.Repo,
		SourceRef:            record.Ref,
		Revision:             record.SHA,
		RootDir:              record.RootDirectory,
		DockerfilePath:       record.DockerfilePath,
		Builder:              record.Builder,
		RuntimeDesiredState:  record.RuntimeDesiredState,
		RuntimeObservedState: record.RuntimeObservedState,
		RuntimeGeneration:    record.RuntimeGeneration,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
		StartedAt:            record.StartedAt,
		CompletedAt:          record.CompletedAt,
		TruthSource:          "canonical",
	}
	if record.OperationID != "" {
		summary.Operation = &EnvironmentOperationSummary{
			ID:            record.OperationID,
			State:         canonicalOperationState(record.State),
			FailureCode:   orailtypes.FailureCode(record.FailureCode),
			FailureReason: record.FailureMessage,
		}
	}
	return summary
}

func sourceSummaryFromDeployment(deployment EnvironmentDeploymentSummary) EnvironmentSourceSummary {
	sourceType := strings.TrimSpace(deployment.SourceType)
	if sourceType == "" && strings.TrimSpace(deployment.Repository) != "" {
		sourceType = "repository"
	}
	if sourceType == "" && strings.TrimSpace(deployment.Image) != "" {
		sourceType = "image"
	}
	return EnvironmentSourceSummary{
		Type:        sourceType,
		Repository:  strings.TrimSpace(deployment.Repository),
		Ref:         strings.TrimSpace(deployment.SourceRef),
		Revision:    strings.TrimSpace(deployment.Revision),
		RootDir:     strings.TrimSpace(deployment.RootDir),
		UpdatedAt:   deployment.UpdatedAt,
		TruthSource: "canonical-deployment",
	}
}

func environmentReplicaHealth(
	deployment EnvironmentDeploymentSummary,
	evidence PersistedReplicaEvidence,
) string {
	if deployment.RuntimeDesiredState == "stopped" || deployment.RuntimeObservedState == "stopped" {
		return "stopped"
	}
	if deployment.RuntimeDesiredState == "removed" || deployment.RuntimeObservedState == "removed" {
		return "removed"
	}
	if strings.EqualFold(deployment.Status, "failed") ||
		(deployment.Operation != nil && deployment.Operation.State == orailtypes.OperationFailed) {
		return "unhealthy"
	}
	if evidence.ReadyReplicas >= evidence.DesiredReplicas {
		return "healthy"
	}
	if evidence.ReadyReplicas > 0 {
		return "degraded"
	}
	state := strings.ToLower(deployment.Status)
	if state == "queued" || state == "building" || state == "deploying" {
		return "progressing"
	}
	return "unhealthy"
}

func legacyEnvironmentDeployments(
	ctx context.Context,
	operations OperationStore,
	projectID, environmentID string,
	services map[string]Service,
) (
	map[string]EnvironmentDeploymentSummary,
	map[string]map[string]struct{},
	error,
) {
	result := make(map[string]EnvironmentDeploymentSummary)
	active := make(map[string]map[string]struct{})
	items, err := operations.List(ctx, OperationListFilter{Kind: "deploy"})
	if err != nil {
		return nil, nil, err
	}
	for _, operation := range items {
		events, err := operations.ListEvents(ctx, operation.ID)
		if err != nil {
			return nil, nil, err
		}
		var serviceID string
		var fields map[string]string
		for _, event := range events {
			if parsedServiceID, parsedFields, ok := parseDashboardDeployIntakeEventMessage(event.Message); ok {
				serviceID, fields = parsedServiceID, parsedFields
			}
		}
		service, ok := services[serviceID]
		if !ok || fields["project_id"] != projectID || fields["environment_id"] != environmentID {
			continue
		}
		resource, ok := deploymentResourceFromOperation(service, operation, events)
		if !ok {
			continue
		}
		summary := EnvironmentDeploymentSummary{
			ID:          resource.ID,
			Status:      resource.Status,
			Active:      resource.Active,
			Image:       resource.Image,
			CreatedAt:   resource.LastEventAt,
			UpdatedAt:   resource.LastEventAt,
			TruthSource: "legacy-operation",
			Operation: &EnvironmentOperationSummary{
				ID:            resource.OperationID,
				State:         resource.OperationState,
				FailureCode:   resource.FailureCode,
				FailureReason: resource.FailureReason,
			},
		}
		current, exists := result[serviceID]
		if !exists || environmentDeploymentNewer(summary, current) {
			result[serviceID] = summary
		}
		if resource.Active {
			addActiveDeployment(active, serviceID, resource.ID)
		}
	}
	return result, active, nil
}

func selectPersistedEndpoints(
	evidence []PersistedEndpointEvidence,
	projectID, environmentID string,
	active map[string]map[string]struct{},
) map[string]EnvironmentEndpointSummary {
	result := make(map[string]EnvironmentEndpointSummary)
	for _, item := range evidence {
		if !item.Persisted ||
			item.ProjectID != projectID ||
			item.EnvironmentID != environmentID ||
			item.ServiceID == "" ||
			item.DeploymentID == "" ||
			item.EvidenceAt.IsZero() ||
			(strings.TrimSpace(item.Host) == "" && strings.TrimSpace(item.URL) == "") {
			continue
		}
		if _, ok := active[item.ServiceID][item.DeploymentID]; !ok {
			continue
		}
		candidate := EnvironmentEndpointSummary{
			DeploymentID: item.DeploymentID,
			Host:         strings.TrimSpace(item.Host),
			URL:          strings.TrimSpace(item.URL),
			TLS:          item.TLS,
			EvidenceAt:   item.EvidenceAt.UTC(),
		}
		current, exists := result[item.ServiceID]
		if !exists ||
			candidate.EvidenceAt.After(current.EvidenceAt) ||
			(candidate.EvidenceAt.Equal(current.EvidenceAt) && candidate.Host < current.Host) {
			result[item.ServiceID] = candidate
		}
	}
	return result
}

func environmentDeploymentNewer(candidate, current EnvironmentDeploymentSummary) bool {
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	return candidate.ID > current.ID
}

func addActiveDeployment(active map[string]map[string]struct{}, serviceID, deploymentID string) {
	if serviceID == "" || deploymentID == "" {
		return
	}
	if active[serviceID] == nil {
		active[serviceID] = make(map[string]struct{})
	}
	active[serviceID][deploymentID] = struct{}{}
}

func environmentResourceIDFromPath(path string) string {
	path = strings.TrimPrefix(path, v1alphaPrefix+"/environments/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "resources" {
		return ""
	}
	return parts[0]
}
