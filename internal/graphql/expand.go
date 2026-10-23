// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/midnight/midnight/internal/api"
	"github.com/vektah/gqlparser/v2/ast"
)

// The expand* family fans an already-resolved domain object out to the
// caller-selected fields by walking the field's SelectionSet. Child
// fields that need their own resolver (services on a Project, latest
// deployment on a ServiceInstance, ...) recursively call back into the
// service layer.

// expandProject expands the requested field selection on a Project.
func (r *resolver) expandProject(ctx context.Context, parent *ast.Field, p *gqlProject) map[string]interface{} {
	if p == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Project"
		case "id":
			out[key] = p.ID
		case "name":
			out[key] = p.Name
		case "description":
			out[key] = p.Description
		case "createdAt":
			out[key] = p.CreatedAt
		case "updatedAt":
			out[key] = p.UpdatedAt
		case "services":
			services, srvErr := r.services.ListServicesService(ctx, p.ID)
			if srvErr != nil {
				out[key] = nil
				continue
			}
			nodes := make([]*gqlService, 0, len(services))
			for _, s := range services {
				nodes = append(nodes, newGQLService(s))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandService(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		case "environments":
			envs, srvErr := r.services.ListEnvironmentsService(ctx, p.ID)
			if srvErr != nil {
				out[key] = nil
				continue
			}
			nodes := make([]*gqlEnvironment, 0, len(envs))
			for _, e := range envs {
				nodes = append(nodes, newGQLEnvironment(e))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandEnvironment(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		case "volumes":
			results, srvErr := r.services.ListVolumesService(ctx, p.ID, "", "")
			if srvErr != nil {
				out[key] = r.connectionResponse(ctx, f, 0, func(int) interface{} { return nil }, func(int) string { return "" })
				continue
			}
			nodes := make([]*gqlVolume, 0, len(results))
			for _, res := range results {
				nodes = append(nodes, newGQLVolume(res.Volume, res.MountPath))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandVolume(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		}
	}
	return out
}

func (r *resolver) expandService(ctx context.Context, parent *ast.Field, s *gqlService) map[string]interface{} {
	if s == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Service"
		case "id":
			out[key] = s.ID
		case "name":
			out[key] = s.Name
		case "projectId":
			out[key] = s.ProjectID
		case "createdAt":
			out[key] = s.CreatedAt
		case "updatedAt":
			out[key] = s.UpdatedAt
		case "serviceInstances":
			// Each environment in the service's project produces one
			// ServiceInstance.
			envs, _ := r.services.ListEnvironmentsService(ctx, s.ProjectID)
			nodes := make([]*gqlServiceInstance, 0, len(envs))
			for _, e := range envs {
				envCopy := e
				nodes = append(nodes, newGQLServiceInstance(s.src, &envCopy))
			}
			if len(nodes) == 0 {
				// Surface a synthetic instance for the empty-env case
				// so the GraphQL connection never silently hides the
				// service from a Railway client expecting >=1 instance.
				nodes = append(nodes, newGQLServiceInstance(s.src, nil))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandServiceInstance(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		case "deployments":
			envFilter, _ := argString(f, "environmentId", r.vars)
			deployments, srvErr := r.services.ListDeploymentsForServiceService(ctx, s.ID)
			if srvErr != nil {
				out[key] = nil
				continue
			}
			if envFilter != "" {
				filtered := make([]api.DeploymentResource, 0, len(deployments))
				for _, d := range deployments {
					if d.EnvironmentID == envFilter {
						filtered = append(filtered, d)
					}
				}
				deployments = filtered
			}
			nodes := make([]*gqlDeployment, 0, len(deployments))
			for _, d := range deployments {
				nodes = append(nodes, newGQLDeployment(d))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandDeployment(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		case "github":
			link, ok, srvErr := r.services.GithubLinkSpecForService(ctx, s.ID)
			if srvErr != nil || !ok {
				out[key] = nil
				continue
			}
			out[key] = r.expandGithubLink(ctx, f, newGQLGithubLink(link))
		}
	}
	return out
}

func (r *resolver) expandServiceInstance(ctx context.Context, parent *ast.Field, si *gqlServiceInstance) map[string]interface{} {
	if si == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "ServiceInstance"
		case "id":
			out[key] = si.ID
		case "serviceId":
			out[key] = si.ServiceID
		case "environmentId":
			out[key] = si.EnvironmentID
		case "serviceName":
			out[key] = si.ServiceName
		case "environmentName":
			out[key] = si.EnvironmentName
		case "region":
			out[key] = si.Region
		case "numReplicas":
			out[key] = si.NumReplicas
		case "startCommand":
			out[key] = si.StartCommand
		case "buildCommand":
			out[key] = si.BuildCommand
		case "rootDirectory":
			out[key] = si.RootDirectory
		case "source":
			out[key] = nil
		case "createdAt":
			out[key] = si.CreatedAt
		case "updatedAt":
			out[key] = si.UpdatedAt
		case "domains":
			values, srvErr := r.services.ListDomainsService(ctx, string(si.service.ProjectID), si.EnvironmentID, si.ServiceID)
			if srvErr != nil {
				out[key] = []interface{}{}
				continue
			}
			domains := make([]interface{}, 0, len(values))
			for _, value := range values {
				if value.Domain.Kind == "custom" {
					domains = append(domains, r.expandCustomDomain(f, newGQLCustomDomain(value)))
				}
			}
			out[key] = domains
		case "serviceDomains":
			values, srvErr := r.services.ListDomainsService(ctx, string(si.service.ProjectID), si.EnvironmentID, si.ServiceID)
			if srvErr != nil {
				out[key] = []interface{}{}
				continue
			}
			domains := make([]interface{}, 0, len(values))
			for _, value := range values {
				if value.Domain.Kind == "generated" {
					domains = append(domains, r.expandServiceDomain(f, newGQLServiceDomain(value)))
				}
			}
			out[key] = domains
		case "variables":
			vars, _ := r.services.ListVariablesService(ctx, "", si.EnvironmentID, si.ServiceID)
			m := map[string]interface{}{}
			for _, v := range vars {
				// Only PLAIN variables surface a value through this
				// shortcut map (Railway's behavior). SECRET/SEALED
				// values are placeholders only.
				if v.Scope == "plain" {
					m[v.Name] = ""
				} else {
					m[v.Name] = "${{redacted}}"
				}
			}
			out[key] = m
		case "latestDeployment":
			d, srvErr := r.services.LatestActiveDeploymentForService(ctx, si.ServiceID)
			if srvErr != nil {
				out[key] = nil
				continue
			}
			out[key] = r.expandDeployment(ctx, f, newGQLDeployment(d))
		case "deployments":
			deployments, srvErr := r.services.ListDeploymentsForServiceService(ctx, si.ServiceID)
			if srvErr != nil {
				out[key] = nil
				continue
			}
			// filter by environment id if set
			if si.EnvironmentID != "" {
				filtered := make([]api.DeploymentResource, 0, len(deployments))
				for _, d := range deployments {
					if d.EnvironmentID == "" || d.EnvironmentID == si.EnvironmentID {
						filtered = append(filtered, d)
					}
				}
				deployments = filtered
			}
			nodes := make([]*gqlDeployment, 0, len(deployments))
			for _, d := range deployments {
				nodes = append(nodes, newGQLDeployment(d))
			}
			out[key] = r.connectionResponse(ctx, f, len(nodes), func(i int) interface{} {
				return r.expandDeployment(ctx, edgeNodeField(f), nodes[i])
			}, func(i int) string {
				return nodes[i].ID
			})
		}
	}
	return out
}

func (r *resolver) expandCustomDomain(parent *ast.Field, value *gqlCustomDomain) map[string]interface{} {
	return selectSimpleFields(parent, map[string]interface{}{
		"__typename": "CustomDomain", "id": value.ID, "domain": value.Domain,
		"serviceId": value.ServiceID, "environmentId": value.EnvironmentID,
		"status": value.Status, "createdAt": value.CreatedAt,
	})
}

func (r *resolver) expandServiceDomain(parent *ast.Field, value *gqlServiceDomain) map[string]interface{} {
	return selectSimpleFields(parent, map[string]interface{}{
		"__typename": "ServiceDomain", "id": value.ID, "domain": value.Domain,
		"serviceId": value.ServiceID, "environmentId": value.EnvironmentID,
		"suffix": value.Suffix, "createdAt": value.CreatedAt,
	})
}

func selectSimpleFields(parent *ast.Field, values map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, selection := range parent.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if value, ok := values[field.Name]; ok {
			out[fieldKey(field)] = value
		}
	}
	return out
}

func (r *resolver) expandEnvironment(_ context.Context, parent *ast.Field, e *gqlEnvironment) map[string]interface{} {
	if e == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Environment"
		case "id":
			out[key] = e.ID
		case "name":
			out[key] = e.Name
		case "projectId":
			out[key] = e.ProjectID
		case "tier":
			out[key] = e.Tier
		case "isProtected":
			out[key] = e.IsProtected
		case "createdAt":
			out[key] = e.CreatedAt
		case "updatedAt":
			out[key] = e.UpdatedAt
		}
	}
	return out
}

func (r *resolver) expandDeployment(ctx context.Context, parent *ast.Field, d *gqlDeployment) map[string]interface{} {
	return r.expandDeploymentSnapshot(ctx, parent, d, nil)
}

func (r *resolver) expandDeploymentSnapshot(ctx context.Context, parent *ast.Field, d *gqlDeployment, snapshotEvents []api.OperationEvent) map[string]interface{} {
	if d == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Deployment"
		case "id":
			out[key] = d.ID
		case "serviceId":
			out[key] = d.ServiceID
		case "environmentId":
			out[key] = d.EnvironmentID
		case "projectId":
			out[key] = d.ProjectID
		case "status":
			out[key] = d.Status
		case "operationId":
			out[key] = d.OperationID
		case "operationState":
			out[key] = d.OperationState
		case "image":
			out[key] = d.Image
		case "staticUrl":
			out[key] = d.StaticURL
		case "meta":
			out[key] = d.Meta
		case "createdAt":
			out[key] = d.CreatedAt
		case "updatedAt":
			out[key] = d.UpdatedAt
		case "logs":
			limit, _ := argInt(f, "limit", r.vars)
			if limit <= 0 {
				limit = 50
			}
			events := snapshotEvents
			if events == nil {
				var srvErr *api.ServiceError
				events, srvErr = r.services.ListOperationEventsService(ctx, d.OperationID)
				if srvErr != nil {
					out[key] = []interface{}{}
					continue
				}
			}
			lines := make([]interface{}, 0, len(events))
			start := 0
			if len(events) > limit {
				start = len(events) - limit
			}
			for _, ev := range events[start:] {
				lines = append(lines, map[string]interface{}{
					"timestamp": ev.CreatedAt,
					"message":   ev.Message,
					"severity":  string(ev.State),
				})
			}
			out[key] = lines
		}
	}
	return out
}

func (r *resolver) expandVariable(ctx context.Context, parent *ast.Field, v *gqlVariable) map[string]interface{} {
	if v == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Variable"
		case "id":
			out[key] = v.ID
		case "name":
			out[key] = v.Name
		case "scope":
			out[key] = v.Scope
		case "projectId":
			out[key] = v.ProjectID
		case "environmentId":
			out[key] = v.EnvironmentID
		case "serviceId":
			out[key] = v.ServiceID
		case "workspaceId":
			out[key] = v.WorkspaceID
		case "value":
			out[key] = v.Value
		case "valueKind":
			out[key] = v.ValueKind
		case "references":
			refs := make([]interface{}, 0, len(v.References))
			for _, ref := range v.References {
				refs = append(refs, r.expandVariableReference(ctx, f, ref))
			}
			out[key] = refs
		case "version":
			out[key] = v.Version
		case "lengthBucket":
			out[key] = v.LengthBucket
		case "lastRotatedAt":
			out[key] = v.LastRotatedAt
		}
	}
	return out
}

func (r *resolver) expandVariableReference(_ context.Context, parent *ast.Field, ref *gqlVariableReference) map[string]interface{} {
	if ref == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "VariableReference"
		case "kind":
			out[key] = ref.Kind
		case "service":
			out[key] = ref.Service
		case "name":
			out[key] = ref.Name
		}
	}
	return out
}

// expandVolume projects a gqlVolume onto the field's selection set.
func (r *resolver) expandVolume(_ context.Context, parent *ast.Field, v *gqlVolume) map[string]interface{} {
	if v == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Volume"
		case "id":
			out[key] = v.ID
		case "name":
			out[key] = v.Name
		case "projectId":
			out[key] = v.ProjectID
		case "serviceId":
			out[key] = v.ServiceID
		case "environmentId":
			out[key] = v.EnvironmentID
		case "mountPath":
			out[key] = v.MountPath
		case "sizeMb":
			out[key] = v.SizeMB
		case "sizeBytes":
			out[key] = v.SizeBytes
		case "state":
			out[key] = v.State
		case "status":
			out[key] = v.Status
		case "createdAt":
			out[key] = v.CreatedAt
		case "updatedAt":
			out[key] = v.UpdatedAt
		}
	}
	return out
}

func (r *resolver) expandTemplate(ctx context.Context, parent *ast.Field, t *gqlTemplate) map[string]interface{} {
	if t == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "Template"
		case "slug":
			out[key] = t.Slug
		case "name":
			out[key] = t.Name
		case "version":
			out[key] = t.Version
		case "summary":
			out[key] = t.Summary
		case "description":
			out[key] = t.Description
		case "services":
			services := make([]interface{}, 0, len(t.Services))
			for _, s := range t.Services {
				services = append(services, r.expandTemplateService(ctx, f, s))
			}
			out[key] = services
		}
	}
	return out
}

func (r *resolver) expandTemplateService(ctx context.Context, parent *ast.Field, s *gqlTemplateSvc) map[string]interface{} {
	if s == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "TemplateService"
		case "slug":
			out[key] = s.Slug
		case "image":
			out[key] = s.Image
		case "env":
			env := make([]interface{}, 0, len(s.Env))
			for _, e := range s.Env {
				env = append(env, r.expandTemplateEnv(ctx, f, e))
			}
			out[key] = env
		case "volume":
			if s.Volume == nil {
				out[key] = nil
				continue
			}
			out[key] = r.expandTemplateVolume(ctx, f, s.Volume)
		}
	}
	return out
}

func (r *resolver) expandTemplateEnv(_ context.Context, parent *ast.Field, e *gqlTemplateEnv) map[string]interface{} {
	if e == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "TemplateEnv"
		case "name":
			out[key] = e.Name
		case "kind":
			out[key] = e.Kind
		case "value":
			out[key] = e.Value
		case "generatedKind":
			out[key] = e.GeneratedKind
		case "length":
			out[key] = e.Length
		case "template":
			out[key] = e.Template
		case "secret":
			out[key] = e.Secret
		}
	}
	return out
}

func (r *resolver) expandTemplateVolume(_ context.Context, parent *ast.Field, v *gqlTemplateVolume) map[string]interface{} {
	if v == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "TemplateVolume"
		case "mountPath":
			out[key] = v.MountPath
		case "sizeBytes":
			out[key] = v.SizeBytes
		}
	}
	return out
}

func (r *resolver) expandGithubLink(_ context.Context, parent *ast.Field, l *gqlGithubLink) map[string]interface{} {
	if l == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "GitHubLink"
		case "installationId":
			out[key] = l.InstallationID
		case "repo":
			out[key] = l.Repo
		case "branch":
			out[key] = l.Branch
		case "watchPaths":
			out[key] = stringSliceAsAny(l.WatchPaths)
		case "cloneUrl":
			out[key] = l.CloneURL
		case "waitForCi":
			out[key] = l.WaitForCI
		case "requiredChecks":
			out[key] = stringSliceAsAny(l.RequiredChecks)
		}
	}
	return out
}

func (r *resolver) expandGithubInstallation(_ context.Context, parent *ast.Field, i *gqlGithubInstallation) map[string]interface{} {
	if i == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "GitHubInstallation"
		case "id":
			out[key] = i.ID
		case "account":
			out[key] = i.Account
		}
	}
	return out
}

func (r *resolver) expandGithubRepo(_ context.Context, parent *ast.Field, repo *gqlGithubRepo) map[string]interface{} {
	if repo == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, sel := range parent.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			out[key] = "GitHubRepo"
		case "fullName":
			out[key] = repo.FullName
		case "private":
			out[key] = repo.Private
		}
	}
	return out
}

func stringSliceAsAny(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// connectionResponse builds a Relay-style edges+pageInfo map from an
// indexable slice. `first`/`after` arguments on the field are honored
// when present.
func (r *resolver) connectionResponse(ctx context.Context, field *ast.Field, total int, nodeAt func(int) interface{}, idAt func(int) string) map[string]interface{} {
	_ = ctx
	first, hasFirst := argInt(field, "first", r.vars)
	after, _ := argString(field, "after", r.vars)

	start := 0
	if after != "" {
		// cursor is base64("c:" + index)
		if i, ok := decodeCursor(after); ok {
			start = i + 1
		}
	}
	end := total
	if hasFirst && first > 0 && start+first < end {
		end = start + first
	}
	if start > total {
		start = total
	}
	if end < start {
		end = start
	}

	edges := make([]map[string]interface{}, 0, end-start)
	// inner edge subselection
	edgeFields := edgeFieldSelection(field)
	for i := start; i < end; i++ {
		edge := map[string]interface{}{}
		for _, ef := range edgeFields {
			switch ef.Name {
			case "__typename":
				edge[fieldKey(ef)] = "edge"
			case "cursor":
				edge[fieldKey(ef)] = encodeCursor(i)
			case "node":
				edge[fieldKey(ef)] = nodeAt(i)
			}
		}
		edges = append(edges, edge)
	}

	pageInfo := map[string]interface{}{
		"hasNextPage":     end < total,
		"hasPreviousPage": start > 0,
		"startCursor":     nilIfEmpty(encodeCursorOrNil(start, end > start)),
		"endCursor":       nilIfEmpty(encodeCursorOrNil(end-1, end > start)),
	}

	// What did the caller select on the connection?
	connection := map[string]interface{}{}
	for _, sel := range field.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		key := fieldKey(f)
		switch f.Name {
		case "__typename":
			connection[key] = "Connection"
		case "edges":
			generic := make([]interface{}, len(edges))
			for i, e := range edges {
				generic[i] = e
			}
			connection[key] = generic
		case "pageInfo":
			pi := map[string]interface{}{}
			for _, pf := range f.SelectionSet {
				pff, ok := pf.(*ast.Field)
				if !ok {
					continue
				}
				pi[fieldKey(pff)] = pageInfo[pff.Name]
			}
			connection[key] = pi
		}
	}
	return connection
}

func edgeFieldSelection(connectionField *ast.Field) []*ast.Field {
	for _, sel := range connectionField.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		if f.Name == "edges" {
			out := make([]*ast.Field, 0, len(f.SelectionSet))
			for _, esel := range f.SelectionSet {
				ef, ok := esel.(*ast.Field)
				if ok {
					out = append(out, ef)
				}
			}
			return out
		}
	}
	return nil
}

// edgeNodeField returns the synthetic "node" subselection (if any) that
// the caller declared on an edges{node{...}} traversal. The returned
// field carries the SelectionSet for the inner node type.
func edgeNodeField(connectionField *ast.Field) *ast.Field {
	for _, sel := range connectionField.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok || f.Name != "edges" {
			continue
		}
		for _, esel := range f.SelectionSet {
			ef, ok := esel.(*ast.Field)
			if !ok {
				continue
			}
			if ef.Name == "node" {
				return ef
			}
		}
	}
	// Caller didn't declare a node selection -- return an empty
	// surrogate so child resolvers don't panic on a nil SelectionSet.
	return &ast.Field{Name: "node"}
}

// subSelectMap shallow-projects a precomputed map onto the field's
// selection set. Used by serverInfo / customDomainCreate stubs.
func (r *resolver) subSelectMap(_ context.Context, parent *ast.Field, src map[string]interface{}) map[string]interface{} {
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
		if v, ok := src[f.Name]; ok {
			out[key] = v
		}
	}
	return out
}

func fieldKey(f *ast.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

func encodeCursor(i int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("c:%d", i)))
}

func encodeCursorOrNil(i int, hasEdges bool) string {
	if !hasEdges {
		return ""
	}
	return encodeCursor(i)
}

func decodeCursor(s string) (int, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	var i int
	if _, err := fmt.Sscanf(string(b), "c:%d", &i); err != nil {
		return 0, false
	}
	return i, true
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
