// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/pkg/orailtypes"
	"github.com/vektah/gqlparser/v2/ast"
)

// resolver is the per-request dispatcher that owns the variable bag and
// schema cursor. Every typed resolver method returns the resolved value
// and a slice of GraphQL errors -- the executor unwraps them and folds
// them into the response envelope.
type resolver struct {
	services        *api.ServicesAPI
	deployments     api.DeploymentStore
	actions         api.DeploymentActionIntake
	schema          *ast.Schema
	vars            map[string]interface{}
	requestIdentity requestIdentity
}

// resolveQuery handles every field on the Query root type. The schema
// validation guarantees no other field name is reachable, so the default
// branch is a defensive "unknown field" only.
func (r *resolver) resolveQuery(ctx context.Context, field *ast.Field, _ interface{}) (interface{}, []ResponseError) {
	switch field.Name {
	case "serverInfo":
		return r.resolveServerInfo(ctx, field)
	case "project":
		return r.resolveProject(ctx, field)
	case "projects":
		return r.resolveProjects(ctx, field)
	case "service":
		return r.resolveService(ctx, field)
	case "serviceInstance":
		return r.resolveServiceInstance(ctx, field)
	case "deployment":
		return r.resolveDeployment(ctx, field)
	case "deployments":
		return r.resolveDeployments(ctx, field)
	case "variables":
		return r.resolveVariables(ctx, field)
	case "environment":
		return r.resolveEnvironment(ctx, field)
	case "environments":
		return r.resolveEnvironments(ctx, field)
	case "volume":
		return r.resolveVolume(ctx, field)
	case "volumes":
		return r.resolveVolumes(ctx, field)
	case "templates":
		return r.resolveTemplates(ctx, field)
	case "template":
		return r.resolveTemplate(ctx, field)
	case "githubInstallations":
		return r.resolveGithubInstallations(ctx, field)
	case "githubInstallationRepos":
		return r.resolveGithubInstallationRepos(ctx, field)
	}
	return nil, []ResponseError{{Message: "unknown query field: " + field.Name}}
}

// resolveMutation handles every field on the Mutation root type.
func (r *resolver) resolveMutation(ctx context.Context, field *ast.Field, _ interface{}) (interface{}, []ResponseError) {
	switch field.Name {
	case "projectCreate":
		return r.mutateProjectCreate(ctx, field)
	case "serviceCreate":
		return r.mutateServiceCreate(ctx, field)
	case "serviceInstanceUpdate":
		return r.mutateServiceInstanceUpdate(ctx, field)
	case "serviceInstanceDeployV2":
		return r.mutateServiceInstanceDeploy(ctx, field)
	case "deploymentRedeploy":
		return r.mutateDeploymentRedeploy(ctx, field)
	case "deploymentRollback":
		return r.mutateDeploymentRollback(ctx, field)
	case "deploymentStop":
		return r.mutateDeploymentStop(ctx, field)
	case "deploymentRestart":
		return r.mutateDeploymentRestart(ctx, field)
	case "deploymentRemove":
		return r.mutateDeploymentRemove(ctx, field)
	case "variableUpsert":
		return r.mutateVariableUpsert(ctx, field)
	case "variableCollectionUpsert":
		return r.mutateVariableCollectionUpsert(ctx, field)
	case "environmentCreate":
		return r.mutateEnvironmentCreate(ctx, field)
	case "customDomainCreate":
		return r.mutateCustomDomainCreate(ctx, field)
	case "templateDeploy":
		return r.mutateTemplateDeploy(ctx, field)
	case "volumeCreate":
		return r.mutateVolumeCreate(ctx, field)
	case "volumeResize":
		return r.mutateVolumeResize(ctx, field)
	case "volumeDelete":
		return r.mutateVolumeDelete(ctx, field)
	case "serviceGithubLink":
		return r.mutateServiceGithubLink(ctx, field)
	case "serviceGithubUnlink":
		return r.mutateServiceGithubUnlink(ctx, field)
	}
	return nil, []ResponseError{{Message: "unknown mutation field: " + field.Name}}
}

// -----------------------------------------------------------------------
// Query resolvers
// -----------------------------------------------------------------------

func (r *resolver) resolveServerInfo(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	info := map[string]interface{}{
		"apiVersion":          "v2",
		"midnightVersion":     version.Version,
		"graphqlSchemaSource": "schemas/graphql/schema.graphql",
	}
	return r.subSelectMap(ctx, field, info), nil
}

func (r *resolver) resolveProject(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "project(id) requires id"}}
	}
	p, srvErr := r.services.GetProjectService(ctx, id)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandProject(ctx, field, newGQLProject(p)), nil
}

func (r *resolver) resolveProjects(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	projects, srvErr := r.services.ListProjectsService(ctx)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	nodes := make([]*gqlProject, 0, len(projects))
	for _, p := range projects {
		nodes = append(nodes, newGQLProject(p))
	}
	return r.connectionResponse(ctx, field, len(nodes), func(i int) interface{} {
		return r.expandProject(ctx, edgeNodeField(field), nodes[i])
	}, func(i int) string {
		return nodes[i].ID
	}), nil
}

func (r *resolver) resolveService(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "service(id) requires id"}}
	}
	s, srvErr := r.services.GetServiceService(ctx, id)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandService(ctx, field, newGQLService(s)), nil
}

func (r *resolver) resolveServiceInstance(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	svcID, _ := argString(field, "serviceId", r.vars)
	envID, _ := argString(field, "environmentId", r.vars)
	if svcID == "" || envID == "" {
		return nil, []ResponseError{{Message: "serviceInstance(serviceId, environmentId) requires both arguments"}}
	}
	svc, srvErr := r.services.GetServiceService(ctx, svcID)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	var env *api.Environment
	if envObj, envErr := r.services.GetEnvironmentService(ctx, envID); envErr == nil {
		env = &envObj
	}
	si := newGQLServiceInstance(svc, env)
	return r.expandServiceInstance(ctx, field, si), nil
}

func (r *resolver) resolveDeployment(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "deployment(id) requires id"}}
	}
	d, srvErr := r.services.GetDeploymentService(ctx, id)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandDeployment(ctx, field, newGQLDeployment(d)), nil
}

func (r *resolver) resolveDeployments(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	var serviceID, envID, projectID string
	if input, ok := argObject(field, "input", r.vars); ok {
		if v, ok := input["serviceId"].(string); ok {
			serviceID = v
		}
		if v, ok := input["environmentId"].(string); ok {
			envID = v
		}
		if v, ok := input["projectId"].(string); ok {
			projectID = v
		}
	}

	var deployments []api.DeploymentResource
	if serviceID != "" {
		out, srvErr := r.services.ListDeploymentsForServiceService(ctx, serviceID)
		if srvErr != nil {
			return nil, []ResponseError{wrapServiceError(srvErr)}
		}
		deployments = out
	} else {
		// scan all services in scope
		services, srvErr := r.services.ListServicesService(ctx, projectID)
		if srvErr != nil {
			return nil, []ResponseError{wrapServiceError(srvErr)}
		}
		for _, svc := range services {
			out, srvErr := r.services.ListDeploymentsForServiceService(ctx, string(svc.ID))
			if srvErr != nil {
				continue
			}
			deployments = append(deployments, out...)
		}
	}

	// optional environment filter
	if envID != "" {
		filtered := make([]api.DeploymentResource, 0, len(deployments))
		for _, d := range deployments {
			if d.EnvironmentID == envID {
				filtered = append(filtered, d)
			}
		}
		deployments = filtered
	}

	nodes := make([]*gqlDeployment, 0, len(deployments))
	for _, d := range deployments {
		nodes = append(nodes, newGQLDeployment(d))
	}
	return r.connectionResponse(ctx, field, len(nodes), func(i int) interface{} {
		return r.expandDeployment(ctx, edgeNodeField(field), nodes[i])
	}, func(i int) string {
		return nodes[i].ID
	}), nil
}

func (r *resolver) resolveVariables(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	project, _ := argString(field, "projectId", r.vars)
	environment, _ := argString(field, "environmentId", r.vars)
	service, _ := argString(field, "serviceId", r.vars)
	vars, srvErr := r.services.ListVariablesService(ctx, project, environment, service)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(vars))
	for _, v := range vars {
		out = append(out, r.expandVariable(ctx, field, newGQLVariable(v)))
	}
	return out, nil
}

func (r *resolver) resolveEnvironment(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "environment(id) requires id"}}
	}
	env, srvErr := r.services.GetEnvironmentService(ctx, id)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandEnvironment(ctx, field, newGQLEnvironment(env)), nil
}

func (r *resolver) resolveEnvironments(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	projectID, _ := argString(field, "projectId", r.vars)
	envs, srvErr := r.services.ListEnvironmentsService(ctx, projectID)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(envs))
	for _, e := range envs {
		out = append(out, r.expandEnvironment(ctx, field, newGQLEnvironment(e)))
	}
	return out, nil
}

func (r *resolver) resolveVolume(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "volume(id) requires id"}}
	}
	res, srvErr := r.services.GetVolumeService(ctx, id)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandVolume(ctx, field, newGQLVolume(res.Volume, res.MountPath)), nil
}

func (r *resolver) resolveVolumes(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	projectID, _ := argString(field, "projectId", r.vars)
	serviceID, _ := argString(field, "serviceId", r.vars)
	envID, _ := argString(field, "environmentId", r.vars)
	results, srvErr := r.services.ListVolumesService(ctx, projectID, serviceID, envID)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(results))
	for _, res := range results {
		out = append(out, r.expandVolume(ctx, field, newGQLVolume(res.Volume, res.MountPath)))
	}
	return out, nil
}

func (r *resolver) resolveTemplates(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	tmpls, srvErr := r.services.ListTemplatesService(ctx)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(tmpls))
	for _, t := range tmpls {
		out = append(out, r.expandTemplate(ctx, field, newGQLTemplate(t)))
	}
	return out, nil
}

func (r *resolver) resolveTemplate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	slug, ok := argString(field, "slug", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "template(slug) requires slug"}}
	}
	t, srvErr := r.services.GetTemplateService(ctx, slug)
	if srvErr != nil {
		if srvErr.Status == 404 {
			return nil, nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandTemplate(ctx, field, newGQLTemplate(t)), nil
}

func (r *resolver) resolveGithubInstallations(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	insts, srvErr := r.services.ListGithubInstallationsService(ctx)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(insts))
	for _, in := range insts {
		out = append(out, r.expandGithubInstallation(ctx, field, &gqlGithubInstallation{ID: in.ID, Account: in.Account}))
	}
	return out, nil
}

func (r *resolver) resolveGithubInstallationRepos(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	idF, _ := argInt(field, "installationId", r.vars)
	if idF == 0 {
		return nil, []ResponseError{{Message: "githubInstallationRepos(installationId) requires installationId"}}
	}
	repos, srvErr := r.services.ListGithubInstallationReposService(ctx, int64(idF))
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	out := make([]interface{}, 0, len(repos))
	for _, repo := range repos {
		out = append(out, r.expandGithubRepo(ctx, field, &gqlGithubRepo{FullName: repo.FullName, Private: repo.Private}))
	}
	return out, nil
}

// -----------------------------------------------------------------------
// Mutations
// -----------------------------------------------------------------------

func (r *resolver) mutateProjectCreate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "projectCreate(input) requires input"}}
	}
	req := api.CreateProjectRequest{
		WorkspaceID: stringOrEmpty(input["workspaceId"]),
		Slug:        stringOrEmpty(input["name"]),
		DisplayName: stringOrEmpty(input["description"]),
	}
	if req.WorkspaceID == "" {
		// GraphQL is still an operator-token compatibility surface. Human
		// sessions remain disabled here until resolver-wide tenant scoping is
		// installed, so legacy callers explicitly target the migration tenant.
		req.WorkspaceID = api.DefaultWorkspaceID
	}
	p, srvErr := r.services.CreateProjectService(ctx, req)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandProject(ctx, field, newGQLProject(p)), nil
}

func (r *resolver) mutateServiceCreate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "serviceCreate(input) requires input"}}
	}
	req := api.CreateServiceRequest{
		ProjectID:   stringOrEmpty(input["projectId"]),
		Slug:        stringOrEmpty(input["name"]),
		DisplayName: stringOrEmpty(input["name"]),
	}
	s, srvErr := r.services.CreateServiceService(ctx, req)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandService(ctx, field, newGQLService(s)), nil
}

func (r *resolver) mutateServiceInstanceUpdate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return unsupportedMutation(
		"service-instance-configuration",
		"Service instance updates require durable per-environment configuration storage.",
	)
}

func (r *resolver) mutateServiceInstanceDeploy(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "serviceInstanceDeployV2(input) requires input"}}
	}
	idemKey, _ := argString(field, "idempotencyKey", r.vars)
	req := api.DeployRequest{
		ServiceID:     stringOrEmpty(input["serviceId"]),
		EnvironmentID: stringOrEmpty(input["environmentId"]),
		Image:         stringOrEmpty(input["image"]),
	}
	if r.services.GraphQLDeployIntake != nil {
		result, srvErr := r.services.EnqueueGraphQLDeployService(ctx, api.GraphQLDeployRequest{
			ServiceID: orailtypes.ID(req.ServiceID),
			Intent:    api.DeployIntakeRequest{EnvironmentID: req.EnvironmentID, Image: req.Image},
			Key:       idempotency.Key(strings.TrimSpace(idemKey)), Fingerprint: r.requestIdentity.Fingerprint,
			RequestID: r.requestIdentity.RequestID, SourceIP: r.requestIdentity.SourceIP, UserAgent: r.requestIdentity.UserAgent,
			EncodeResult: func(accepted api.GraphQLDeployAccepted) ([]byte, error) {
				selected := r.expandDeploymentSnapshot(ctx, field, newGQLDeployment(accepted.Deployment), []api.OperationEvent{accepted.InitialEvent})
				return json.Marshal(selected)
			},
		})
		if srvErr != nil {
			return nil, []ResponseError{wrapServiceError(srvErr)}
		}
		var selected map[string]interface{}
		if err := json.Unmarshal(result.Body, &selected); err != nil {
			return nil, []ResponseError{{Message: "stored GraphQL deployment result is invalid", Extensions: map[string]interface{}{"code": string(failurecodes.InternalError)}}}
		}
		return selected, nil
	}
	op, srvErr := r.services.EnqueueDeployService(ctx, req, idemKey)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	// Surface only the canonical deployment created by intake. A deployment
	// mutation must never substitute an operation id for deployment identity.
	deployments, dErr := r.services.ListDeploymentsForServiceService(ctx, req.ServiceID)
	if dErr != nil {
		return nil, []ResponseError{wrapServiceError(dErr)}
	}
	for _, d := range deployments {
		if d.OperationID == string(op.ID) {
			return r.expandDeployment(ctx, field, newGQLDeployment(d)), nil
		}
	}
	return nil, []ResponseError{{
		Message: "canonical deployment was not observable after intake",
		Extensions: map[string]interface{}{
			"code":         string(failurecodes.InternalError),
			"operation_id": string(op.ID),
		},
	}}
}

func (r *resolver) mutateDeploymentRedeploy(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return r.commitImageReplay(ctx, field, deploy.ActionRedeployImage)
}

func (r *resolver) mutateDeploymentRollback(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return r.commitImageReplay(ctx, field, deploy.ActionRollbackDigest)
}

func (r *resolver) mutateDeploymentRestart(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return r.commitRuntimeAction(ctx, field, deploy.RuntimeActionRestart)
}

func (r *resolver) mutateDeploymentStop(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return r.commitRuntimeAction(ctx, field, deploy.RuntimeActionStop)
}

func (r *resolver) mutateDeploymentRemove(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return r.commitRuntimeAction(ctx, field, deploy.RuntimeActionRemove)
}

// commitImageReplay creates a new canonical deployment from the selected
// deployment's immutable digest and config/variable snapshot.  It deliberately
// does not call the normal deploy intake: that path accepts mutable source
// inputs, while this operation must remain a no-build replay.
func (r *resolver) commitImageReplay(ctx context.Context, field *ast.Field, action deploy.DeploymentAction) (interface{}, []ResponseError) {
	if r.actions == nil {
		capability := "deployment-redeploy"
		if action == deploy.ActionRollbackDigest {
			capability = "deployment-rollback"
		}
		return unsupportedMutation(capability, "Deployment actions require the durable Postgres action intake.")
	}
	targetID, ok := argString(field, "id", r.vars)
	if !ok || strings.TrimSpace(targetID) == "" {
		return nil, []ResponseError{{Message: field.Name + "(id) requires id"}}
	}
	key, ok := argString(field, "idempotencyKey", r.vars)
	if !ok || strings.TrimSpace(key) == "" {
		return nil, []ResponseError{{Message: field.Name + " requires idempotencyKey", Extensions: map[string]interface{}{"code": "IDEMPOTENCY_KEY_REQUIRED"}}}
	}
	// Load first so service ownership is derived server-side rather than being
	// accepted from the client. The action store verifies it again under lock.
	target, err := r.deploymentByID(ctx, targetID)
	if err != nil {
		return nil, deploymentActionError(err)
	}
	result, err := r.actions.CommitImageReplay(ctx, api.ImageReplayRequest{
		ServiceID:  target.ServiceID,
		SourceID:   targetID,
		Action:     action,
		TriggerKey: graphqlActionTrigger(field.Name, targetID, key),
	})
	if err != nil {
		return nil, deploymentActionError(err)
	}
	return r.expandDeployment(ctx, field, actionResultDeployment(result)), nil
}

// commitRuntimeAction records a stop/restart/remove action separately from the
// immutable deployment FSM. Its response is a projection of the target
// deployment, but carries the newly-created operation id so a client follows
// the actual runtime-action operation rather than the deployment's old deploy
// operation.
func (r *resolver) commitRuntimeAction(ctx context.Context, field *ast.Field, action deploy.RuntimeAction) (interface{}, []ResponseError) {
	if r.actions == nil {
		capability := "deployment-" + string(action)
		return unsupportedMutation(capability, "Deployment actions require the durable Postgres action intake.")
	}
	targetID, ok := argString(field, "id", r.vars)
	if !ok || strings.TrimSpace(targetID) == "" {
		return nil, []ResponseError{{Message: field.Name + "(id) requires id"}}
	}
	key, ok := argString(field, "idempotencyKey", r.vars)
	if !ok || strings.TrimSpace(key) == "" {
		return nil, []ResponseError{{Message: field.Name + " requires idempotencyKey", Extensions: map[string]interface{}{"code": "IDEMPOTENCY_KEY_REQUIRED"}}}
	}
	target, err := r.deploymentByID(ctx, targetID)
	if err != nil {
		return nil, deploymentActionError(err)
	}
	result, err := r.actions.CommitRuntimeAction(ctx, api.RuntimeActionRequest{
		ServiceID:  target.ServiceID,
		TargetID:   targetID,
		Action:     action,
		TriggerKey: graphqlActionTrigger(field.Name, targetID, key),
	})
	if err != nil {
		return nil, deploymentActionError(err)
	}
	return r.expandDeployment(ctx, field, actionResultDeployment(result)), nil
}

func (r *resolver) deploymentByID(ctx context.Context, deploymentID string) (api.DeploymentResource, error) {
	if r.deployments == nil {
		return api.DeploymentResource{}, api.ErrDeploymentNotFound
	}
	// DeploymentStore intentionally only exposes service-scoped listing. Get
	// through ServicesAPI preserves its read-flip and avoids a new unscoped
	// store method merely for this facade.
	resource, serviceErr := r.services.GetDeploymentService(ctx, deploymentID)
	if serviceErr != nil {
		return api.DeploymentResource{}, serviceErr
	}
	return resource, nil
}

func graphqlActionTrigger(name, targetID, key string) string {
	return "graphql:" + strings.TrimSpace(name) + ":" + strings.TrimSpace(targetID) + ":" + strings.TrimSpace(key)
}

func actionResultDeployment(result api.DeploymentActionResult) *gqlDeployment {
	rec := result.Deployment
	image := rec.ImageRef
	if image == "" {
		image = rec.ImageDigest
	}
	resource := api.DeploymentResource{
		ID: rec.ID, DeploymentID: rec.ID, ServiceID: rec.ServiceID, ProjectID: rec.ProjectID,
		EnvironmentID: rec.EnvironmentID, Image: image,
		OperationID: string(result.Operation.ID), OperationState: result.Operation.State,
		Status: "queued", Active: rec.Active, RuntimeDesiredState: rec.RuntimeDesiredState,
		RuntimeObservedState: rec.RuntimeObservedState, RuntimeGeneration: rec.RuntimeGeneration,
		LastEventAt: rec.UpdatedAt,
	}
	return newGQLDeployment(resource)
}

func deploymentActionError(err error) []ResponseError {
	code := string(failurecodes.InternalError)
	var serviceErr *api.ServiceError
	if errors.As(err, &serviceErr) && serviceErr.Code != "" {
		code = serviceErr.Code
	} else if errors.Is(err, api.ErrDeploymentNotFound) {
		code = string(failurecodes.ResourceNotFound)
	} else if errors.Is(err, api.ErrDeploymentActionConflict) || errors.Is(err, api.ErrDeploymentSnapshotAbsent) {
		code = string(failurecodes.ResourceConflict)
	}
	return []ResponseError{{Message: err.Error(), Extensions: map[string]interface{}{"code": code}}}
}

func (r *resolver) mutateVariableUpsert(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "variableUpsert(input) requires input"}}
	}
	req := api.CreateVariableRequest{
		Name:          stringOrEmpty(input["name"]),
		WorkspaceID:   stringOrEmpty(input["workspaceId"]),
		ProjectID:     stringOrEmpty(input["projectId"]),
		EnvironmentID: stringOrEmpty(input["environmentId"]),
		ServiceID:     stringOrEmpty(input["serviceId"]),
		Scope:         mapScope(stringOrEmpty(input["scope"])),
		Value:         stringOrEmpty(input["value"]),
	}
	meta, srvErr := r.services.UpsertVariableService(ctx, req)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandVariable(ctx, field, newGQLVariable(meta)), nil
}

func (r *resolver) mutateVariableCollectionUpsert(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "variableCollectionUpsert(input) requires input"}}
	}
	scope := mapScope(stringOrEmpty(input["scope"]))
	common := api.CreateVariableRequest{
		WorkspaceID:   stringOrEmpty(input["workspaceId"]),
		ProjectID:     stringOrEmpty(input["projectId"]),
		EnvironmentID: stringOrEmpty(input["environmentId"]),
		ServiceID:     stringOrEmpty(input["serviceId"]),
		Scope:         scope,
	}
	vars, ok := input["variables"].(map[string]interface{})
	if !ok {
		return []interface{}{}, nil
	}
	out := make([]interface{}, 0, len(vars))
	for name, raw := range vars {
		req := common
		req.Name = name
		switch v := raw.(type) {
		case string:
			req.Value = v
		default:
			req.Value = stringOrEmpty(v)
		}
		meta, srvErr := r.services.UpsertVariableService(ctx, req)
		if srvErr != nil {
			return out, []ResponseError{wrapServiceError(srvErr)}
		}
		out = append(out, r.expandVariable(ctx, field, newGQLVariable(meta)))
	}
	return out, nil
}

func (r *resolver) mutateEnvironmentCreate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "environmentCreate(input) requires input"}}
	}
	env := api.Environment{
		ProjectID: orailtypes.ID(stringOrEmpty(input["projectId"])),
		Slug:      strings.ToLower(stringOrEmpty(input["name"])),
		Name:      stringOrEmpty(input["name"]),
		Tier:      strings.ToLower(stringOrEmpty(input["tier"])),
	}
	out, srvErr := r.services.CreateEnvironmentService(ctx, env)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandEnvironment(ctx, field, newGQLEnvironment(out)), nil
}

func (r *resolver) mutateCustomDomainCreate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "customDomainCreate(input) requires input"}}
	}
	value, srvErr := r.services.CreateCustomDomainService(
		ctx,
		stringOrEmpty(input["environmentId"]),
		stringOrEmpty(input["serviceId"]),
		stringOrEmpty(input["domain"]),
	)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandCustomDomain(field, newGQLCustomDomain(value)), nil
}

// -----------------------------------------------------------------------
// Templates / Volumes / GitHub mutations
// -----------------------------------------------------------------------

func (r *resolver) mutateTemplateDeploy(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	return unsupportedMutation(
		"template-runtime-deploy",
		"Template deploy requires runtime, volume, connectivity, and health verification.",
	)
}

func unsupportedMutation(capability, message string) (interface{}, []ResponseError) {
	return nil, []ResponseError{{
		Message: message,
		Extensions: map[string]interface{}{
			"code":       string(failurecodes.NotImplemented),
			"capability": capability,
			"state":      "unavailable",
		},
	}}
}

func (r *resolver) mutateVolumeCreate(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "volumeCreate(input) requires input"}}
	}
	req := api.VolumeCreateServiceRequest{
		ServiceID:     stringOrEmpty(input["serviceId"]),
		EnvironmentID: stringOrEmpty(input["environmentId"]),
		Name:          stringOrEmpty(input["name"]),
		MountPath:     stringOrEmpty(input["mountPath"]),
	}
	if v, ok := input["sizeBytes"]; ok {
		req.SizeBytes = toInt64(v)
	}
	idem := stringOrEmpty(input["idempotencyKey"])
	result, srvErr := r.services.CreateVolumeService(ctx, req, idem)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	volumeNode := r.expandVolume(ctx, fieldByName(field, "volume"), newGQLVolume(result.Volume, result.MountPath))
	payload := map[string]interface{}{
		"volume":      volumeNode,
		"operationId": nil,
	}
	return r.subSelectPayload(ctx, field, payload, map[string]func(*ast.Field) interface{}{
		"volume": func(f *ast.Field) interface{} {
			return r.expandVolume(ctx, f, newGQLVolume(result.Volume, result.MountPath))
		},
	}), nil
}

func (r *resolver) mutateVolumeResize(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "volumeResize(id) requires id"}}
	}
	sizeI, ok := argInt(field, "sizeBytes", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "volumeResize(sizeBytes) requires sizeBytes"}}
	}
	result, srvErr := r.services.ResizeVolumeService(ctx, id, int64(sizeI))
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandVolume(ctx, field, newGQLVolume(result.Volume, result.MountPath)), nil
}

func (r *resolver) mutateVolumeDelete(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	id, ok := argString(field, "id", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "volumeDelete(id) requires id"}}
	}
	if srvErr := r.services.DeleteVolumeService(ctx, id); srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return true, nil
}

func (r *resolver) mutateServiceGithubLink(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	input, ok := argObject(field, "input", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "serviceGithubLink(input) requires input"}}
	}
	req := api.GithubLinkServiceRequest{
		ServiceID:     stringOrEmpty(input["serviceId"]),
		Repo:          stringOrEmpty(input["repo"]),
		Branch:        stringOrEmpty(input["branch"]),
		RootDirectory: stringOrEmpty(input["rootDirectory"]),
		ConfigPath:    stringOrEmpty(input["configPath"]),
		CloneURL:      stringOrEmpty(input["cloneUrl"]),
	}
	if v, ok := input["installationId"]; ok {
		req.InstallationID = toInt64(v)
	}
	if v, ok := input["watchPaths"].([]interface{}); ok {
		for _, e := range v {
			if s, ok := e.(string); ok {
				req.WatchPaths = append(req.WatchPaths, s)
			}
		}
	}
	if v, ok := input["requiredChecks"].([]interface{}); ok {
		for _, e := range v {
			if s, ok := e.(string); ok {
				req.RequiredChecks = append(req.RequiredChecks, s)
			}
		}
	}
	if v, ok := input["waitForCi"].(bool); ok {
		req.WaitForCI = v
	}
	svc, srvErr := r.services.LinkGithubService(ctx, req)
	if srvErr != nil {
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandService(ctx, field, newGQLService(svc)), nil
}

func (r *resolver) mutateServiceGithubUnlink(ctx context.Context, field *ast.Field) (interface{}, []ResponseError) {
	serviceID, ok := argString(field, "serviceId", r.vars)
	if !ok {
		return nil, []ResponseError{{Message: "serviceGithubUnlink(serviceId) requires serviceId"}}
	}
	svc, srvErr := r.services.UnlinkGithubService(ctx, serviceID)
	if srvErr != nil {
		// When the link was absent we still want to return the service
		// (idempotent unlink). Surface other errors verbatim.
		if srvErr.Status == 404 && svc.ID != "" {
			return r.expandService(ctx, field, newGQLService(svc)), nil
		}
		return nil, []ResponseError{wrapServiceError(srvErr)}
	}
	return r.expandService(ctx, field, newGQLService(svc)), nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func stringOrEmpty(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return ""
	}
}

func mapScope(v string) secrets.Scope {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "PLAIN":
		return secrets.ScopePlain
	case "SEALED":
		return secrets.ScopeSealed
	case "SECRET", "":
		return secrets.ScopeSecret
	default:
		return secrets.ScopeSecret
	}
}

// toInt64 coerces an evalValue result (which leaves numbers as the raw
// string form or float64) into int64. Returns 0 on unparseable input.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case string:
		var i int64
		_, _ = fmt.Sscanf(n, "%d", &i)
		return i
	default:
		return 0
	}
}

func nilIfEmptyString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// fieldByName returns the (alias-or-name)-matched child field on `parent`,
// or an empty surrogate if not selected. Used to drive child expand calls
// when a parent payload is a non-trivial object.
func fieldByName(parent *ast.Field, name string) *ast.Field {
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		if f.Name == name {
			return f
		}
	}
	return &ast.Field{Name: name}
}

// edgeNodeFieldNamed is fieldByName, but tailored for cases where the
// payload nested list field carries direct object selections (no Relay
// connection). Returns the matched child field's SelectionSet wrapped
// in a synthetic *ast.Field so the expand* method can iterate it the
// same way it iterates a regular field.
func edgeNodeFieldNamed(parent *ast.Field, name string) *ast.Field {
	return fieldByName(parent, name)
}

// subSelectPayload projects a non-Relay object payload (template
// deploy, volume create) onto the selection set of `parent`.
// `staticValues` are used when the selected field does not match one of
// the special-cased keys in `lazy`. The lazy map lets the caller defer
// child expansion to the requested field so unused branches do not pay
// the cost.
func (r *resolver) subSelectPayload(_ context.Context, parent *ast.Field, staticValues map[string]interface{}, lazy map[string]func(*ast.Field) interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		if f.Name == "__typename" {
			out[key] = ""
			continue
		}
		if fn, ok := lazy[f.Name]; ok {
			out[key] = fn(f)
			continue
		}
		if v, ok := staticValues[f.Name]; ok {
			out[key] = v
		}
	}
	return out
}
