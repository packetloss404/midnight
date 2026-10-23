// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/templates"
	volumeModel "github.com/midnight/midnight/internal/volume"
)

// Re-export for the resolver layer so call sites only import internal/graphql.
type volumeRow = volumeModel.Volume

// gqlProject is the GraphQL-side projection of an api.Project. Field
// names mirror the schema (Railway-compatible).
type gqlProject struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	src api.Project // back-reference so child resolvers can read project_id without a second lookup
}

func newGQLProject(p api.Project) *gqlProject {
	desc := p.DisplayName
	return &gqlProject{
		ID:          string(p.ID),
		Name:        p.Slug,
		Description: &desc,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		src:         p,
	}
}

type gqlService struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ProjectID string    `json:"projectId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	src api.Service
}

func newGQLService(s api.Service) *gqlService {
	return &gqlService{
		ID:        string(s.ID),
		Name:      s.Slug,
		ProjectID: string(s.ProjectID),
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		src:       s,
	}
}

type gqlEnvironment struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ProjectID   string    `json:"projectId"`
	Tier        *string   `json:"tier"`
	IsProtected bool      `json:"isProtected"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func newGQLEnvironment(e api.Environment) *gqlEnvironment {
	tier := mapEnvTier(e.Tier)
	return &gqlEnvironment{
		ID:          string(e.ID),
		Name:        e.Name,
		ProjectID:   string(e.ProjectID),
		Tier:        tier,
		IsProtected: e.IsProtected,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}
}

func mapEnvTier(t string) *string {
	if t == "" {
		return nil
	}
	var v string
	switch t {
	case "prod", "production":
		v = "PROD"
	case "staging":
		v = "STAGING"
	case "preview":
		v = "PREVIEW"
	case "dev", "development":
		v = "DEV"
	default:
		v = "DEV"
	}
	return &v
}

type gqlDeployment struct {
	ID             string         `json:"id"`
	ServiceID      string         `json:"serviceId"`
	EnvironmentID  *string        `json:"environmentId"`
	ProjectID      *string        `json:"projectId"`
	Status         string         `json:"status"`
	OperationID    string         `json:"operationId"`
	OperationState string         `json:"operationState"`
	Image          *string        `json:"image"`
	StaticURL      *string        `json:"staticUrl"`
	Meta           map[string]any `json:"meta"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

func newGQLDeployment(d api.DeploymentResource) *gqlDeployment {
	var env, proj, image, url *string
	if d.EnvironmentID != "" {
		v := d.EnvironmentID
		env = &v
	}
	if d.ProjectID != "" {
		v := d.ProjectID
		proj = &v
	}
	if d.Image != "" {
		v := d.Image
		image = &v
	}
	if d.Route != "" {
		v := d.Route
		url = &v
	}
	meta := map[string]any{}
	if d.LastEvent != "" {
		meta["last_event"] = d.LastEvent
	}
	if d.RouteIntent != "" {
		meta["route_intent"] = d.RouteIntent
	}
	if d.InstanceID != "" {
		meta["instance_id"] = d.InstanceID
	}
	if d.FailureCode != "" {
		meta["failure_code"] = string(d.FailureCode)
	}
	if d.FailureReason != "" {
		meta["failure_reason"] = d.FailureReason
	}
	return &gqlDeployment{
		ID:             d.ID,
		ServiceID:      d.ServiceID,
		EnvironmentID:  env,
		ProjectID:      proj,
		Status:         mapDeploymentStatus(d.Status),
		OperationID:    d.OperationID,
		OperationState: string(d.OperationState),
		Image:          image,
		StaticURL:      url,
		Meta:           meta,
		CreatedAt:      d.LastEventAt,
		UpdatedAt:      d.LastEventAt,
	}
}

func mapDeploymentStatus(s string) string {
	switch s {
	case "queued":
		return "QUEUED"
	case "deploying":
		return "DEPLOYING"
	case "succeeded", "active":
		return "SUCCESS"
	case "failed":
		return "FAILED"
	case "canceled":
		return "CANCELED"
	default:
		return "QUEUED"
	}
}

type gqlVariable struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Scope         string                  `json:"scope"`
	ProjectID     *string                 `json:"projectId"`
	EnvironmentID *string                 `json:"environmentId"`
	ServiceID     *string                 `json:"serviceId"`
	WorkspaceID   *string                 `json:"workspaceId"`
	Value         *string                 `json:"value"`
	ValueKind     string                  `json:"valueKind"`
	References    []*gqlVariableReference `json:"references"`
	Version       int                     `json:"version"`
	LengthBucket  string                  `json:"lengthBucket"`
	LastRotatedAt time.Time               `json:"lastRotatedAt"`
}

// gqlVariableReference is the redaction-safe projection of one parsed
// `${{...}}` reference attached to a variable.
type gqlVariableReference struct {
	Kind    string  `json:"kind"`
	Service *string `json:"service"`
	Name    string  `json:"name"`
}

func newGQLVariable(m secrets.VariableMeta) *gqlVariable {
	scope := "SECRET"
	switch m.Scope {
	case secrets.ScopePlain:
		scope = "PLAIN"
	case secrets.ScopeSecret:
		scope = "SECRET"
	case secrets.ScopeSealed:
		scope = "SEALED"
	}
	var proj, env, svc, ws *string
	if m.Project != "" {
		v := m.Project
		proj = &v
	}
	if m.Environment != "" {
		v := m.Environment
		env = &v
	}
	if m.Service != "" {
		v := m.Service
		svc = &v
	}
	if m.Workspace != "" {
		v := m.Workspace
		ws = &v
	}
	valueKind := "LITERAL"
	if strings.EqualFold(m.ValueKind, "reference") {
		valueKind = "REFERENCE"
	}
	refs := make([]*gqlVariableReference, 0, len(m.References))
	for _, r := range m.References {
		var s *string
		if r.Service != "" {
			v := r.Service
			s = &v
		}
		refs = append(refs, &gqlVariableReference{
			Kind:    mapVariableReferenceKind(r.Kind),
			Service: s,
			Name:    r.Name,
		})
	}
	return &gqlVariable{
		ID:            string(m.ID),
		Name:          m.Name,
		Scope:         scope,
		ProjectID:     proj,
		EnvironmentID: env,
		ServiceID:     svc,
		WorkspaceID:   ws,
		Value:         nil, // never expose plaintext through GraphQL reads
		ValueKind:     valueKind,
		References:    refs,
		Version:       m.Version,
		LengthBucket:  m.LengthBucket,
		LastRotatedAt: m.LastRotatedAt,
	}
}

// mapVariableReferenceKind translates the varrefs-shape reference kind
// (snake_case) into the GraphQL enum value declared on the schema.
func mapVariableReferenceKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "service_var":
		return "ServiceVar"
	case "shared":
		return "Shared"
	case "local":
		return "LocalVar"
	case "builtin":
		return "Builtin"
	default:
		return "LocalVar"
	}
}

type gqlVolume struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	ProjectID     string    `json:"projectId"`
	ServiceID     *string   `json:"serviceId"`
	EnvironmentID *string   `json:"environmentId"`
	MountPath     string    `json:"mountPath"`
	SizeMB        *int      `json:"sizeMb"`
	SizeBytes     int64     `json:"sizeBytes"`
	State         *string   `json:"state"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// newGQLVolume maps a runtime volume row into the schema shape. The
// volume package's Volume type is the source-of-truth. The mountPath
// is sourced from the api.VolumeMountStore by the resolver.
func newGQLVolume(v volumeRow, mountPath string) *gqlVolume {
	var svc, env, state *string
	if v.ServiceID != "" {
		s := v.ServiceID
		svc = &s
	}
	if v.EnvironmentID != "" {
		s := v.EnvironmentID
		env = &s
	}
	if v.Status != "" {
		s := string(v.Status)
		state = &s
	}
	var size *int
	if v.SizeBytes > 0 {
		s := int(v.SizeBytes / (1024 * 1024))
		size = &s
	}
	return &gqlVolume{
		ID:            v.ID,
		Name:          v.Name,
		ProjectID:     v.ProjectID,
		ServiceID:     svc,
		EnvironmentID: env,
		MountPath:     mountPath,
		SizeMB:        size,
		SizeBytes:     v.SizeBytes,
		State:         state,
		Status:        string(v.Status),
		CreatedAt:     v.CreatedAt,
		UpdatedAt:     v.UpdatedAt,
	}
}

// gqlServiceInstance is the Railway compound (serviceId, environmentId)
// shape. It carries enough context for child resolvers to fetch
// deployments + variables without re-querying the indexes.
type gqlServiceInstance struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"serviceId"`
	EnvironmentID   string    `json:"environmentId"`
	ServiceName     string    `json:"serviceName"`
	EnvironmentName *string   `json:"environmentName"`
	Region          *string   `json:"region"`
	NumReplicas     *int      `json:"numReplicas"`
	StartCommand    *string   `json:"startCommand"`
	BuildCommand    *string   `json:"buildCommand"`
	RootDirectory   *string   `json:"rootDirectory"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`

	service     api.Service
	environment *api.Environment
}

type gqlCustomDomain struct {
	ID            string    `json:"id"`
	Domain        string    `json:"domain"`
	ServiceID     string    `json:"serviceId"`
	EnvironmentID *string   `json:"environmentId"`
	Status        *string   `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

type gqlServiceDomain struct {
	ID            string    `json:"id"`
	Domain        string    `json:"domain"`
	ServiceID     string    `json:"serviceId"`
	EnvironmentID *string   `json:"environmentId"`
	Suffix        *string   `json:"suffix"`
	CreatedAt     time.Time `json:"createdAt"`
}

func domainEnvironmentID(value router.DomainRouteState) *string {
	if value.Domain.EnvironmentID == "" {
		return nil
	}
	environmentID := value.Domain.EnvironmentID
	return &environmentID
}

func newGQLCustomDomain(value router.DomainRouteState) *gqlCustomDomain {
	status := string(value.ApplyState)
	return &gqlCustomDomain{
		ID: value.Domain.ID, Domain: value.Domain.NormalizedHostname,
		ServiceID: value.Domain.ServiceID, EnvironmentID: domainEnvironmentID(value),
		Status: &status, CreatedAt: value.Domain.CreatedAt,
	}
}

func newGQLServiceDomain(value router.DomainRouteState) *gqlServiceDomain {
	suffix := value.Domain.AppsDomain
	return &gqlServiceDomain{
		ID: value.Domain.ID, Domain: value.Domain.NormalizedHostname,
		ServiceID: value.Domain.ServiceID, EnvironmentID: domainEnvironmentID(value),
		Suffix: &suffix, CreatedAt: value.Domain.CreatedAt,
	}
}

func newGQLServiceInstance(svc api.Service, env *api.Environment) *gqlServiceInstance {
	envID := ""
	var envName *string
	if env != nil {
		envID = string(env.ID)
		n := env.Name
		envName = &n
	}
	id := string(svc.ID) + ":" + envID
	return &gqlServiceInstance{
		ID:              id,
		ServiceID:       string(svc.ID),
		EnvironmentID:   envID,
		ServiceName:     svc.Slug,
		EnvironmentName: envName,
		CreatedAt:       svc.CreatedAt,
		UpdatedAt:       svc.UpdatedAt,
		service:         svc,
		environment:     env,
	}
}

// connectionFromSlice builds a Relay-style pagination response from a
// slice that has already been ordered. cursor encoding here is the
// stable offset-into-list form -- if the underlying store grows a
// stricter ordering it can swap in signed cursors without changing the
// resolver shape.
type relayConnection[T any] struct {
	Edges    []relayEdge[T] `json:"edges"`
	PageInfo relayPageInfo  `json:"pageInfo"`
}

type relayEdge[T any] struct {
	Cursor string `json:"cursor"`
	Node   T      `json:"node"`
}

type relayPageInfo struct {
	HasNextPage     bool    `json:"hasNextPage"`
	HasPreviousPage bool    `json:"hasPreviousPage"`
	StartCursor     *string `json:"startCursor"`
	EndCursor       *string `json:"endCursor"`
}

// gqlTemplate is the projection of a one-click templates.Template
// manifest into the GraphQL schema shape.
type gqlTemplate struct {
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Summary     *string           `json:"summary"`
	Description *string           `json:"description"`
	Services    []*gqlTemplateSvc `json:"services"`
}

type gqlTemplateSvc struct {
	Slug   string             `json:"slug"`
	Image  string             `json:"image"`
	Env    []*gqlTemplateEnv  `json:"env"`
	Volume *gqlTemplateVolume `json:"volume"`
}

type gqlTemplateEnv struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Value         *string `json:"value"`
	GeneratedKind *string `json:"generatedKind"`
	Length        *int    `json:"length"`
	Template      *string `json:"template"`
	Secret        *bool   `json:"secret"`
}

type gqlTemplateVolume struct {
	MountPath string `json:"mountPath"`
	SizeBytes int64  `json:"sizeBytes"`
}

func newGQLTemplate(t templates.Template) *gqlTemplate {
	out := &gqlTemplate{
		Slug:    t.Slug,
		Name:    t.Name,
		Version: t.Version,
	}
	if t.Summary != "" {
		s := t.Summary
		out.Summary = &s
	}
	if t.ConsumerDocs != "" {
		s := t.ConsumerDocs
		out.Description = &s
	}
	for _, svc := range t.Services {
		gs := &gqlTemplateSvc{Slug: svc.Slug, Image: svc.Image}
		// Sort by name for stable output.
		names := make([]string, 0, len(svc.Env))
		for name := range svc.Env {
			names = append(names, name)
		}
		// stable order
		for i := 1; i < len(names); i++ {
			for j := i; j > 0 && names[j-1] > names[j]; j-- {
				names[j-1], names[j] = names[j], names[j-1]
			}
		}
		for _, name := range names {
			env := svc.Env[name]
			ge := &gqlTemplateEnv{Name: name, Kind: mapTemplateEnvKind(env.Kind)}
			if env.Value != "" {
				v := env.Value
				ge.Value = &v
			}
			if env.GeneratedKind != "" {
				v := string(env.GeneratedKind)
				ge.GeneratedKind = &v
			}
			if env.Length > 0 {
				v := env.Length
				ge.Length = &v
			}
			if env.Template != "" {
				v := env.Template
				ge.Template = &v
			}
			if env.Secret {
				v := env.Secret
				ge.Secret = &v
			}
			gs.Env = append(gs.Env, ge)
		}
		if svc.Volume != nil {
			gs.Volume = &gqlTemplateVolume{MountPath: svc.Volume.MountPath, SizeBytes: svc.Volume.SizeBytes}
		}
		out.Services = append(out.Services, gs)
	}
	return out
}

func mapTemplateEnvKind(k templates.EnvKind) string {
	switch k {
	case templates.EnvKindLiteral:
		return "LITERAL"
	case templates.EnvKindGenerated:
		return "GENERATED"
	case templates.EnvKindComputedURL:
		return "COMPUTED_URL"
	default:
		return "LITERAL"
	}
}

// gqlGithubLink is the redaction-safe projection of a github.ServiceLink
// surfaced as the `Service.github` field and the link mutation
// responses.
type gqlGithubLink struct {
	InstallationID int64    `json:"installationId"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	WatchPaths     []string `json:"watchPaths"`
	RootDirectory  *string  `json:"rootDirectory"`
	ConfigPath     *string  `json:"configPath"`
	CloneURL       *string  `json:"cloneUrl"`
	WaitForCI      *bool    `json:"waitForCi"`
	RequiredChecks []string `json:"requiredChecks"`
}

func newGQLGithubLink(l github.ServiceLink) *gqlGithubLink {
	out := &gqlGithubLink{
		InstallationID: l.InstallationID,
		Repo:           l.Repo,
		Branch:         l.Branch,
		WatchPaths:     append([]string{}, l.WatchPaths...),
		RequiredChecks: append([]string{}, l.RequiredChecks...),
	}
	if l.CloneURL != "" {
		v := l.CloneURL
		out.CloneURL = &v
	}
	if l.RootDirectory != "" {
		v := l.RootDirectory
		out.RootDirectory = &v
	}
	if l.ConfigPath != "" {
		v := l.ConfigPath
		out.ConfigPath = &v
	}
	if l.WaitForCI {
		v := l.WaitForCI
		out.WaitForCI = &v
	}
	return out
}

type gqlGithubInstallation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

type gqlGithubRepo struct {
	FullName string `json:"fullName"`
	Private  bool   `json:"private"`
}
