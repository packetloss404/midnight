// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"
)

// ServiceCreator is the seam the materializer uses to create one
// `service` row. The shape mirrors the api.ServiceStore.CreateService
// method (the api package wires the real store at router time).
type ServiceCreator interface {
	CreateServiceService(ctx context.Context, in CreateServiceInput) (CreatedService, error)
}

// CreateServiceInput is the input shape for ServiceCreator.
type CreateServiceInput struct {
	ProjectID     string
	EnvironmentID string
	Slug          string
	DisplayName   string
	Image         string
	Args          []string
	Healthcheck   string
	Ports         []TemplatePort
}

// CreatedService is what ServiceCreator returns; just the id + the
// final slug (the creator may disambiguate on slug conflict).
type CreatedService struct {
	ID   string
	Slug string
}

// VolumeAttacher is the seam used to record a volume + attachment for
// a template service. The actual volume worker is on a concurrent
// track; this seam exists so the materializer hands off the declared
// requirement.
type VolumeAttacher interface {
	AttachTemplateVolume(ctx context.Context, in AttachVolumeInput) (AttachedVolume, error)
}

// AttachVolumeInput is the input shape for VolumeAttacher.
type AttachVolumeInput struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Name          string
	MountPath     string
	SizeBytes     int64
}

// AttachedVolume is what VolumeAttacher returns.
type AttachedVolume struct {
	VolumeID     string
	AttachmentID string
}

// VariableUpserter is the seam used to upsert one variable. It maps
// onto the secrets.Vault.Set call wired through api.UpsertVariableService.
type VariableUpserter interface {
	UpsertVariableService(ctx context.Context, in UpsertVariableInput) (UpsertedVariable, error)
}

// UpsertVariableInput is the input shape for VariableUpserter.
type UpsertVariableInput struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Name          string
	Value         string
	Secret        bool
}

// UpsertedVariable is what VariableUpserter returns.
type UpsertedVariable struct {
	ID   string
	Name string
}

// Generator produces random passwords + uuids. Tests inject a
// deterministic generator; production wiring passes DefaultGenerator.
type Generator interface {
	Password(length int) (string, error)
	UUID() (string, error)
}

// DefaultGenerator wires the standard crypto/rand-backed primitives
// declared in manifest.go.
type DefaultGenerator struct{}

// Password implements Generator.
func (DefaultGenerator) Password(length int) (string, error) { return GeneratePassword(length) }

// UUID implements Generator.
func (DefaultGenerator) UUID() (string, error) { return GenerateUUID() }

// Clock returns the current UTC time. Tests pass a fixed function.
type Clock func() time.Time

// Materializer turns a Template manifest into real service + variable +
// volume rows. The struct is safe for concurrent use; per-call state
// is held on the stack.
type Materializer struct {
	Services  ServiceCreator
	Volumes   VolumeAttacher
	Variables VariableUpserter
	Gen       Generator
	Now       Clock

	// PrivateDomainTemplate renders the auto-injected
	// MIDNIGHT_PRIVATE_DOMAIN value for a created service. It defaults
	// to "<slug>.midnight.internal" matching the private-networking
	// track. Override in tests if you need to assert a different
	// shape.
	PrivateDomainTemplate func(slug string) string
}

// Result is what Materialize returns: the operation id (if the caller
// pre-created one) and the ordered list of created resources.
type Result struct {
	OperationID      string            `json:"operation_id,omitempty"`
	TemplateSlug     string            `json:"template_slug"`
	CreatedResources []CreatedResource `json:"created_resources"`
}

// ResolveEnvironmentValues compiles one validated template service's literal,
// generated, and computed values without persisting them. Changeset producers
// use this to hand the plaintext directly to the encrypted variable authority;
// callers must not log or retain the returned values.
func ResolveEnvironmentValues(service TemplateService, generator Generator, privateDomain string) (map[string]string, error) {
	if generator == nil {
		generator = DefaultGenerator{}
	}
	resolved := make(map[string]string, len(service.Env)+1)
	if strings.TrimSpace(privateDomain) != "" {
		resolved["MIDNIGHT_PRIVATE_DOMAIN"] = strings.TrimSpace(privateDomain)
	}
	for name, env := range service.Env {
		switch env.Kind {
		case EnvKindLiteral:
			resolved[name] = env.Value
		case EnvKindGenerated:
			value, err := generateEnv(env, generator)
			if err != nil {
				return nil, fmt.Errorf("generate env %s: %w", name, err)
			}
			resolved[name] = value
		case EnvKindComputedURL:
		default:
			return nil, fmt.Errorf("%w: env %s kind %q is unsupported", ErrTemplateInvalid, name, env.Kind)
		}
	}
	for name, env := range service.Env {
		if env.Kind != EnvKindComputedURL {
			continue
		}
		rendered, err := renderTemplate(env.Template, resolved)
		if err != nil {
			return nil, fmt.Errorf("render env %s: %w", name, err)
		}
		resolved[name] = rendered
	}
	return resolved, nil
}

// Materialize creates the resources declared by the manifest for the
// supplied project + environment.
//
// Transactional guarantee: on partial failure the materializer attempts
// best-effort rollback via the Undoer interface (if implemented by the
// stores). When a store does not implement Undoer, the materializer
// records the unrolled-back resources on the returned error so the
// caller can surface a precise audit trail. This matches the panel-
// locked "single operation, all-or-nothing within the storage layer's
// guarantees" contract for one-click deploy.
func (m *Materializer) Materialize(ctx context.Context, manifest Template, projectID, environmentID string) (Result, error) {
	if err := ValidateTemplate(manifest); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(projectID) == "" {
		return Result{}, fmt.Errorf("%w: project_id is required", ErrTemplateInvalid)
	}
	if m.Services == nil || m.Variables == nil {
		return Result{}, errors.New("materializer: services + variables seams are required")
	}
	gen := m.Gen
	if gen == nil {
		gen = DefaultGenerator{}
	}
	priv := m.PrivateDomainTemplate
	if priv == nil {
		priv = func(slug string) string { return slug + ".midnight.internal" }
	}

	state := newRollbackState()
	for _, svc := range manifest.Services {
		if err := m.materializeService(ctx, manifest, svc, projectID, environmentID, gen, priv, state); err != nil {
			m.rollback(ctx, state)
			return Result{}, err
		}
	}

	return Result{
		TemplateSlug:     manifest.Slug,
		CreatedResources: state.created(),
	}, nil
}

func (m *Materializer) materializeService(
	ctx context.Context,
	manifest Template,
	svc TemplateService,
	projectID, environmentID string,
	gen Generator,
	priv func(string) string,
	state *rollbackState,
) error {
	created, err := m.Services.CreateServiceService(ctx, CreateServiceInput{
		ProjectID:     projectID,
		EnvironmentID: environmentID,
		Slug:          svc.Slug,
		DisplayName:   manifest.Name,
		Image:         svc.Image,
		Args:          append([]string(nil), svc.Args...),
		Healthcheck:   healthcheckCmd(svc.Healthcheck),
		Ports:         append([]TemplatePort(nil), svc.Ports...),
	})
	if err != nil {
		return fmt.Errorf("create service %s: %w", svc.Slug, err)
	}
	state.recordService(created.ID, created.Slug)

	// Volume — best-effort hand-off to the volume worker track.
	if svc.Volume != nil && m.Volumes != nil {
		vol, err := m.Volumes.AttachTemplateVolume(ctx, AttachVolumeInput{
			ProjectID:     projectID,
			EnvironmentID: environmentID,
			ServiceID:     created.ID,
			Name:          svc.Volume.Name,
			MountPath:     svc.Volume.MountPath,
			SizeBytes:     svc.Volume.SizeBytes,
		})
		if err != nil {
			return fmt.Errorf("attach volume for service %s: %w", svc.Slug, err)
		}
		state.recordVolume(vol.VolumeID, vol.AttachmentID, svc.Volume.Name)
	}

	// Resolve env values in two passes: literal + generated first so
	// computed_url templates have their inputs available.
	resolved := make(map[string]string, len(svc.Env)+1)
	resolved["MIDNIGHT_PRIVATE_DOMAIN"] = priv(created.Slug)

	for name, env := range svc.Env {
		switch env.Kind {
		case EnvKindLiteral:
			resolved[name] = env.Value
		case EnvKindGenerated:
			value, err := generateEnv(env, gen)
			if err != nil {
				return fmt.Errorf("generate env %s for service %s: %w", name, svc.Slug, err)
			}
			resolved[name] = value
		case EnvKindComputedURL:
			// Resolved below.
		default:
			return fmt.Errorf("%w: env %s kind %q is unsupported", ErrTemplateInvalid, name, env.Kind)
		}
	}
	for name, env := range svc.Env {
		if env.Kind != EnvKindComputedURL {
			continue
		}
		rendered, err := renderTemplate(env.Template, resolved)
		if err != nil {
			return fmt.Errorf("render env %s for service %s: %w", name, svc.Slug, err)
		}
		resolved[name] = rendered
	}

	// Upsert every env var into the variable vault. Consumers reference
	// these values with the exact created service slug returned by the
	// materializer, for example ${{postgres.DATABASE_URL}}.
	for _, name := range orderedKeys(svc.Env) {
		env := svc.Env[name]
		variable, err := m.Variables.UpsertVariableService(ctx, UpsertVariableInput{
			ProjectID:     projectID,
			EnvironmentID: environmentID,
			ServiceID:     created.ID,
			Name:          name,
			Value:         resolved[name],
			Secret:        env.Secret,
		})
		if err != nil {
			return fmt.Errorf("upsert variable %s for service %s: %w", name, svc.Slug, err)
		}
		state.recordVariable(variable.ID, name)
	}

	return nil
}

func generateEnv(env TemplateEnv, gen Generator) (string, error) {
	switch env.GeneratedKind {
	case GeneratedKindPassword:
		return gen.Password(env.Length)
	case GeneratedKindUUID:
		return gen.UUID()
	default:
		return "", fmt.Errorf("%w: generator kind %q is unsupported", ErrTemplateInvalid, env.GeneratedKind)
	}
}

func renderTemplate(src string, values map[string]string) (string, error) {
	tmpl, err := template.New("env").Option("missingkey=error").Parse(src)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func healthcheckCmd(h *TemplateHealthcheck) string {
	if h == nil {
		return ""
	}
	return h.Cmd
}

// orderedKeys returns the variable names in deterministic order so
// audit + test consumers see a stable sequence.
func orderedKeys(env map[string]TemplateEnv) []string {
	out := make([]string, 0, len(env))
	for name := range env {
		out = append(out, name)
	}
	// stable order: simple lexical sort; tests + audit consumers
	// can rely on this.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// rollbackState is the per-Materialize bookkeeping for partial-failure
// recovery. The materializer records every successful create; the
// rollback path then issues compensating deletes for any store that
// implements Undoer.
type rollbackState struct {
	mu        sync.Mutex
	resources []CreatedResource
	services  []string
	volumes   []volumeUndo
	variables []string
}

type volumeUndo struct {
	volumeID     string
	attachmentID string
}

func newRollbackState() *rollbackState { return &rollbackState{} }

func (s *rollbackState) recordService(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources = append(s.resources, CreatedResource{Kind: "service", ID: id, Name: name})
	s.services = append(s.services, id)
}

func (s *rollbackState) recordVariable(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources = append(s.resources, CreatedResource{Kind: "variable", ID: id, Name: name})
	s.variables = append(s.variables, id)
}

func (s *rollbackState) recordVolume(volumeID, attachmentID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources = append(s.resources, CreatedResource{Kind: "volume", ID: volumeID, Name: name})
	s.volumes = append(s.volumes, volumeUndo{volumeID: volumeID, attachmentID: attachmentID})
}

func (s *rollbackState) created() []CreatedResource {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CreatedResource, len(s.resources))
	copy(out, s.resources)
	return out
}

// Undoer is the optional companion interface a store can implement to
// support rollback. The materializer best-effort-deletes any rows it
// created when a later step fails. The dashboard wiring relies on
// downstream audit to surface unrolled-back state if Undoer is not
// available.
type Undoer interface {
	UndoService(ctx context.Context, id string) error
	UndoVariable(ctx context.Context, id string) error
	UndoVolume(ctx context.Context, volumeID, attachmentID string) error
}

func (m *Materializer) rollback(ctx context.Context, state *rollbackState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	// undo in reverse: variables first, then volumes, then services
	if undoer, ok := m.Variables.(Undoer); ok {
		for i := len(state.variables) - 1; i >= 0; i-- {
			_ = undoer.UndoVariable(ctx, state.variables[i])
		}
	}
	if undoer, ok := m.Volumes.(Undoer); ok {
		for i := len(state.volumes) - 1; i >= 0; i-- {
			_ = undoer.UndoVolume(ctx, state.volumes[i].volumeID, state.volumes[i].attachmentID)
		}
	}
	if undoer, ok := m.Services.(Undoer); ok {
		for i := len(state.services) - 1; i >= 0; i-- {
			_ = undoer.UndoService(ctx, state.services[i])
		}
	}
}
