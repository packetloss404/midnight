// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/midnight/midnight/internal/varrefs"
)

type memoryEnvelopeStore struct {
	mu      sync.RWMutex
	records map[VariableID]PersistedVariable
}

func newMemoryEnvelopeStore() *memoryEnvelopeStore {
	return &memoryEnvelopeStore{records: map[VariableID]PersistedVariable{}}
}

func (s *memoryEnvelopeStore) Put(_ context.Context, variable PersistedVariable) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.records {
		if existing.Meta.Project == variable.Meta.Project &&
			existing.Meta.Environment == variable.Meta.Environment &&
			existing.Meta.Service == variable.Meta.Service &&
			existing.Meta.Name == variable.Meta.Name &&
			id != variable.Meta.ID {
			delete(s.records, id)
		}
	}
	s.records[variable.Meta.ID] = clonePersistedVariable(variable)
	return nil
}

func (s *memoryEnvelopeStore) Get(_ context.Context, id VariableID) (PersistedVariable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	variable, ok := s.records[id]
	if !ok {
		return PersistedVariable{}, ErrVariableNotFound
	}
	return clonePersistedVariable(variable), nil
}

func (s *memoryEnvelopeStore) Find(_ context.Context, projectID, environmentID, serviceID, name string) (PersistedVariable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, variable := range s.records {
		meta := variable.Meta
		if meta.Project == projectID && meta.Environment == environmentID && meta.Service == serviceID && meta.Name == name {
			return clonePersistedVariable(variable), nil
		}
	}
	return PersistedVariable{}, ErrVariableNotFound
}

func (s *memoryEnvelopeStore) List(context.Context) ([]PersistedVariable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PersistedVariable, 0, len(s.records))
	for _, variable := range s.records {
		out = append(out, clonePersistedVariable(variable))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.ID < out[j].Meta.ID })
	return out, nil
}

func clonePersistedVariable(in PersistedVariable) PersistedVariable {
	out := in
	out.Envelope.Ciphertext = append([]byte(nil), in.Envelope.Ciphertext...)
	out.Envelope.WrappedDEK = append([]byte(nil), in.Envelope.WrappedDEK...)
	out.Meta.References = append([]VariableReference(nil), in.Meta.References...)
	return out
}

func TestDurableVaultSurvivesRestartWithoutPlaintextMetadata(t *testing.T) {
	ctx := context.Background()
	key := []byte("durable-variable-test-kek-000000")
	store := newMemoryEnvelopeStore()
	provider1, err := NewMemoryKeyProvider(key, 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	vault1 := NewVaultWithStore(provider1, AllowAll{}, store)
	const plaintext = "postgres://user:restart-secret@db/app"
	created, err := vault1.Set(ctx, VariableInput{
		Name:        "DATABASE_URL",
		Project:     "prj_one",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSealed,
		Plaintext:   []byte(plaintext),
		ValueKind:   "literal",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	raw := store.records[created.meta.ID]
	if strings.Contains(string(raw.Envelope.Ciphertext), plaintext) || strings.Contains(string(raw.Envelope.WrappedDEK), plaintext) {
		t.Fatal("durable envelope leaked plaintext")
	}

	provider2, err := NewMemoryKeyProvider(key, 1)
	if err != nil {
		t.Fatalf("restart NewMemoryKeyProvider: %v", err)
	}
	vault2 := NewVaultWithStore(provider2, AllowAll{}, store)
	metas, err := vault2.List(ctx)
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(metas) != 1 || metas[0].Name != "DATABASE_URL" || metas[0].Scope != ScopeSealed {
		t.Fatalf("metadata after restart = %#v", metas)
	}
	if strings.Contains(metas[0].Name+metas[0].LengthBucket+metas[0].KEKFingerprint, plaintext) {
		t.Fatal("metadata leaked plaintext")
	}
	decrypted, record, err := vault2.Decrypt(ctx, metas[0].ID, ScopeRuntimeInject)
	if err != nil {
		t.Fatalf("Decrypt after restart: %v", err)
	}
	defer zero(decrypted)
	if string(decrypted) != plaintext {
		t.Fatalf("decrypted value = %q, want restart value", decrypted)
	}
	if record.VariableID != metas[0].ID || strings.Contains(record.KEKFingerprint+record.Bucket, plaintext) {
		t.Fatalf("decrypt audit record is not redaction-safe: %#v", record)
	}
}

func TestRuntimeLookupResolvesEncryptedTemplatesAndReturnsTypedFailures(t *testing.T) {
	ctx := context.Background()
	provider, err := NewMemoryKeyProvider([]byte("runtime-variable-test-kek-000000"), 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	vault := NewVault(provider, AllowAll{})
	set := func(name, value string) {
		t.Helper()
		parsed, parseErr := varrefs.Parse(value)
		if parseErr != nil {
			t.Fatalf("Parse(%s): %v", name, parseErr)
		}
		refs := make([]VariableReference, 0, len(parsed.Refs))
		for _, ref := range parsed.Refs {
			refs = append(refs, VariableReference{Kind: string(ref.Kind), Service: ref.Service, Name: ref.Name})
		}
		kind := "literal"
		if len(refs) > 0 {
			kind = "reference"
		}
		if _, setErr := vault.Set(ctx, VariableInput{
			Name:        name,
			Project:     "prj_one",
			Environment: "env_prod",
			Service:     "svc_api",
			Scope:       ScopeSecret,
			Plaintext:   []byte(value),
			ValueKind:   kind,
			References:  refs,
		}); setErr != nil {
			t.Fatalf("Set(%s): %v", name, setErr)
		}
	}
	set("HOST", "db.internal")
	set("DATABASE_URL", "postgres://${{HOST}}/app")

	resolver := varrefs.NewResolver(NewRuntimeLookup(vault, nil))
	env, err := resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env["DATABASE_URL"] != "postgres://db.internal/app" {
		t.Fatalf("DATABASE_URL = %q", env["DATABASE_URL"])
	}

	set("BROKEN", "${{MISSING}}")
	_, err = resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api")
	var unknown varrefs.ErrUnknownReference
	if !errors.As(err, &unknown) {
		t.Fatalf("Resolve missing error = %T %v, want ErrUnknownReference", err, err)
	}
	if strings.Contains(err.Error(), "db.internal") {
		t.Fatalf("unresolved error leaked resolved plaintext: %v", err)
	}

	set("A", "${{B}}")
	set("B", "${{A}}")
	_, err = resolver.Render(ctx, "prj_one", "env_prod", "svc_api", "A", "${{B}}")
	var cycle varrefs.ErrReferenceCycle
	if !errors.As(err, &cycle) {
		t.Fatalf("Resolve cycle error = %T %v, want ErrReferenceCycle", err, err)
	}
}

func TestBuildLookupUsesBuildInjectionPurpose(t *testing.T) {
	provider, err := NewMemoryKeyProvider([]byte("build-variable-purpose-kek-00000"), 1)
	if err != nil {
		t.Fatal(err)
	}
	lookup := NewBuildLookup(NewVault(provider, AllowAll{}), nil)
	if lookup.purpose != ScopeBuildInject {
		t.Fatalf("NewBuildLookup purpose = %q, want %q", lookup.purpose, ScopeBuildInject)
	}
	if runtimeLookup := NewRuntimeLookup(lookup.Vault, nil); runtimeLookup.purpose != ScopeRuntimeInject {
		t.Fatalf("NewRuntimeLookup purpose = %q, want %q", runtimeLookup.purpose, ScopeRuntimeInject)
	}
}

type liveServiceLookupProbe struct {
	calls int
}

func (p *liveServiceLookupProbe) LookupServiceID(context.Context, string, string, string) (string, bool, error) {
	p.calls++
	return "svc_reused_after_snapshot", true, nil
}

func TestRuntimeLookupPinsServiceAliasesWithVariableVersions(t *testing.T) {
	ctx := context.Background()
	provider, err := NewMemoryKeyProvider([]byte("runtime-alias-snapshot-kek-00000"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(provider, AllowAll{})
	set := func(serviceID, name, value string) VariableMeta {
		t.Helper()
		variable, setErr := vault.Set(ctx, VariableInput{
			Name:        name,
			Project:     "prj_one",
			Environment: "env_prod",
			Service:     serviceID,
			Scope:       ScopeSecret,
			Plaintext:   []byte(value),
			ValueKind:   "reference",
		})
		if setErr != nil {
			t.Fatalf("Set(%s.%s): %v", serviceID, name, setErr)
		}
		return variable.Meta()
	}
	api := set("svc_api", "DATABASE_URL", "postgres://${{database.PASSWORD}}/app")
	database := set("svc_database", "PASSWORD", "snapshot-password")
	live := &liveServiceLookupProbe{}
	resolver := varrefs.NewResolver(NewRuntimeLookup(vault, live))
	refs := []varrefs.VariableVersionRef{
		{
			ID:                 string(api.ID),
			ServiceID:          api.Service,
			ServiceSlug:        "api",
			ServiceDisplayName: "API",
			Name:               api.Name,
			Version:            api.Version,
		},
		{
			ID:                 string(database.ID),
			ServiceID:          database.Service,
			ServiceSlug:        "database",
			ServiceDisplayName: "Database",
			Name:               database.Name,
			Version:            database.Version,
		},
	}

	env, err := resolver.ResolveAtVersions(ctx, "prj_one", "env_prod", "svc_api", refs)
	if err != nil {
		t.Fatalf("ResolveAtVersions: %v", err)
	}
	if got := env["DATABASE_URL"]; got != "postgres://snapshot-password/app" {
		t.Fatalf("DATABASE_URL=%q", got)
	}
	if live.calls != 0 {
		t.Fatalf("immutable resolution consulted live service aliases %d times", live.calls)
	}

	for i := range refs {
		refs[i].ServiceSlug = ""
		refs[i].ServiceDisplayName = ""
	}
	if _, err := resolver.ResolveAtVersions(ctx, "prj_one", "env_prod", "svc_api", refs); err == nil {
		t.Fatal("legacy snapshot without service alias bindings resolved a cross-service reference")
	}
	if live.calls != 0 {
		t.Fatalf("legacy snapshot fell back to live service aliases %d times", live.calls)
	}
}
