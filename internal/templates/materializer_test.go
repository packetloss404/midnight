// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore implements ServiceCreator, VolumeAttacher, VariableUpserter,
// and (optionally) Undoer so the tests can assert both the happy path
// and the rollback path.
type fakeStore struct {
	mu sync.Mutex

	services  []CreateServiceInput
	volumes   []AttachVolumeInput
	variables []UpsertVariableInput

	servicesByID  map[string]CreateServiceInput
	variablesByID map[string]UpsertVariableInput
	volumesByID   map[string]AttachVolumeInput

	failOn string // simulate failure when a service slug or variable name matches

	undoServiceCalls  []string
	undoVariableCalls []string
	undoVolumeCalls   []string

	nextID int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		servicesByID:  map[string]CreateServiceInput{},
		variablesByID: map[string]UpsertVariableInput{},
		volumesByID:   map[string]AttachVolumeInput{},
	}
}

func (s *fakeStore) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%04d", prefix, s.nextID)
}

func (s *fakeStore) CreateServiceService(_ context.Context, in CreateServiceInput) (CreatedService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Slug == s.failOn {
		return CreatedService{}, errors.New("service create boom")
	}
	id := s.id("svc")
	s.servicesByID[id] = in
	s.services = append(s.services, in)
	return CreatedService{ID: id, Slug: in.Slug}, nil
}

func (s *fakeStore) AttachTemplateVolume(_ context.Context, in AttachVolumeInput) (AttachedVolume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Name == s.failOn {
		return AttachedVolume{}, errors.New("volume attach boom")
	}
	id := s.id("vol")
	attachment := s.id("vat")
	s.volumesByID[id] = in
	s.volumes = append(s.volumes, in)
	return AttachedVolume{VolumeID: id, AttachmentID: attachment}, nil
}

func (s *fakeStore) UpsertVariableService(_ context.Context, in UpsertVariableInput) (UpsertedVariable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Name == s.failOn {
		return UpsertedVariable{}, errors.New("variable upsert boom")
	}
	id := s.id("var")
	s.variablesByID[id] = in
	s.variables = append(s.variables, in)
	return UpsertedVariable{ID: id, Name: in.Name}, nil
}

func (s *fakeStore) UndoService(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.undoServiceCalls = append(s.undoServiceCalls, id)
	delete(s.servicesByID, id)
	return nil
}

func (s *fakeStore) UndoVariable(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.undoVariableCalls = append(s.undoVariableCalls, id)
	delete(s.variablesByID, id)
	return nil
}

func (s *fakeStore) UndoVolume(_ context.Context, volumeID, attachmentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.undoVolumeCalls = append(s.undoVolumeCalls, volumeID)
	delete(s.volumesByID, volumeID)
	_ = attachmentID
	return nil
}

func newTestMaterializer(store *fakeStore) *Materializer {
	return &Materializer{
		Services:  store,
		Volumes:   store,
		Variables: store,
		Gen:       DefaultGenerator{},
		Now:       func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) },
	}
}

func TestMaterializePostgresCreatesServiceVolumeAndVariables(t *testing.T) {
	store := newFakeStore()
	m := newTestMaterializer(store)
	result, err := m.Materialize(context.Background(), Postgres16(), "prj_test", "env_dev")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if result.TemplateSlug != "postgres" {
		t.Fatalf("template slug = %q", result.TemplateSlug)
	}

	if len(store.services) != 1 {
		t.Fatalf("services created = %d, want 1", len(store.services))
	}
	svc := store.services[0]
	if svc.Slug != "postgres" || svc.Image != "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20" {
		t.Fatalf("service = %+v", svc)
	}
	if svc.Healthcheck != "pg_isready -U $PGUSER" {
		t.Fatalf("healthcheck = %q", svc.Healthcheck)
	}

	if len(store.volumes) != 1 {
		t.Fatalf("volumes created = %d, want 1", len(store.volumes))
	}
	if store.volumes[0].MountPath != "/var/lib/postgresql/data" || store.volumes[0].SizeBytes != 1<<30 {
		t.Fatalf("volume = %+v", store.volumes[0])
	}

	// We expect exactly one variable per env entry on the Postgres
	// service.
	expected := map[string]struct {
		secret bool
		assert func(t *testing.T, value string)
	}{
		"POSTGRES_PASSWORD": {secret: true, assert: func(t *testing.T, value string) {
			if len(value) != 32 {
				t.Fatalf("password length = %d, want 32", len(value))
			}
		}},
		"POSTGRES_USER": {assert: func(t *testing.T, value string) {
			if value != "postgres" {
				t.Fatalf("POSTGRES_USER = %q", value)
			}
		}},
		"POSTGRES_DB": {assert: func(t *testing.T, value string) {
			if value != "midnight" {
				t.Fatalf("POSTGRES_DB = %q", value)
			}
		}},
		"PGUSER": {assert: func(t *testing.T, value string) {
			if value != "postgres" {
				t.Fatalf("PGUSER = %q", value)
			}
		}},
		"PGDATABASE": {assert: func(t *testing.T, value string) {
			if value != "midnight" {
				t.Fatalf("PGDATABASE = %q", value)
			}
		}},
		"PGPORT": {assert: func(t *testing.T, value string) {
			if value != "5432" {
				t.Fatalf("PGPORT = %q", value)
			}
		}},
		"PGHOST": {assert: func(t *testing.T, value string) {
			if value != "postgres.midnight.internal" {
				t.Fatalf("PGHOST = %q", value)
			}
		}},
		"PGPASSWORD": {secret: true, assert: func(t *testing.T, value string) {
			if len(value) != 32 {
				t.Fatalf("PGPASSWORD length = %d", len(value))
			}
		}},
		"DATABASE_URL": {secret: true, assert: func(t *testing.T, value string) {
			if !strings.HasPrefix(value, "postgres://postgres:") {
				t.Fatalf("DATABASE_URL = %q", value)
			}
			if !strings.Contains(value, "@postgres.midnight.internal:5432/midnight") {
				t.Fatalf("DATABASE_URL = %q does not contain host/port/db", value)
			}
		}},
	}
	if len(store.variables) != len(expected) {
		t.Fatalf("variables created = %d, want %d", len(store.variables), len(expected))
	}
	got := map[string]UpsertVariableInput{}
	for _, v := range store.variables {
		got[v.Name] = v
	}
	for name, expect := range expected {
		variable, ok := got[name]
		if !ok {
			t.Fatalf("variable %q missing", name)
		}
		if variable.Secret != expect.secret {
			t.Fatalf("variable %q secret = %v, want %v", name, variable.Secret, expect.secret)
		}
		expect.assert(t, variable.Value)
	}
}

func TestMaterializePostgresPasswordIsUniqueAcrossRuns(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		store := newFakeStore()
		m := newTestMaterializer(store)
		if _, err := m.Materialize(context.Background(), Postgres16(), "prj_test", "env_dev"); err != nil {
			t.Fatalf("Materialize run %d: %v", i, err)
		}
		for _, v := range store.variables {
			if v.Name != "POSTGRES_PASSWORD" {
				continue
			}
			if seen[v.Value] {
				t.Fatalf("duplicate password observed across runs: %q", v.Value)
			}
			seen[v.Value] = true
		}
	}
}

func TestMaterializeRedisAssemblesURL(t *testing.T) {
	store := newFakeStore()
	m := newTestMaterializer(store)
	if _, err := m.Materialize(context.Background(), Redis7(), "prj_test", "env_dev"); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(store.services) != 1 || store.services[0].Image != "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e" {
		t.Fatalf("redis service = %+v", store.services)
	}
	if len(store.volumes) != 1 || store.volumes[0].MountPath != "/data" || store.volumes[0].SizeBytes != 256<<20 {
		t.Fatalf("redis volume = %+v", store.volumes)
	}
	gotURL := ""
	gotPassword := ""
	for _, v := range store.variables {
		switch v.Name {
		case "REDIS_URL":
			gotURL = v.Value
			if !v.Secret {
				t.Fatalf("REDIS_URL should be marked secret because it embeds the password")
			}
		case "REDIS_PASSWORD":
			gotPassword = v.Value
		}
	}
	if gotPassword == "" || len(gotPassword) != 32 {
		t.Fatalf("REDIS_PASSWORD = %q", gotPassword)
	}
	wantURL := "redis://:" + gotPassword + "@redis.midnight.internal:6379/0"
	if gotURL != wantURL {
		t.Fatalf("REDIS_URL = %q, want %q", gotURL, wantURL)
	}
}

func TestMaterializeRollsBackOnFailure(t *testing.T) {
	store := newFakeStore()
	// Fail when the materializer tries to upsert the PGPORT
	// variable so we exercise rollback after a partial commit (the
	// service + volume + earlier variables are already created).
	store.failOn = "PGPORT"
	m := newTestMaterializer(store)
	_, err := m.Materialize(context.Background(), Postgres16(), "prj_test", "env_dev")
	if err == nil {
		t.Fatal("expected materialize failure")
	}
	if len(store.undoServiceCalls) != 1 {
		t.Fatalf("UndoService calls = %v", store.undoServiceCalls)
	}
	if len(store.undoVolumeCalls) != 1 {
		t.Fatalf("UndoVolume calls = %v", store.undoVolumeCalls)
	}
	if len(store.undoVariableCalls) == 0 {
		t.Fatalf("UndoVariable calls = %v", store.undoVariableCalls)
	}
}

func TestBuiltinTemplatesValidate(t *testing.T) {
	for _, tmpl := range Builtins() {
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate(%s): %v", tmpl.Slug, err)
		}
	}
}

func TestPostgresConsumerDocsRequireReturnedServiceSlug(t *testing.T) {
	docs := Postgres16().ConsumerDocs
	if strings.Contains(docs, "${{Postgres.DATABASE_URL}}") {
		t.Fatalf("consumer docs advertise a service name that provisioning does not create: %q", docs)
	}
	for _, want := range []string{"created_services[].slug", "${{orders-database-abcd1234.DATABASE_URL}}"} {
		if !strings.Contains(docs, want) {
			t.Fatalf("consumer docs %q do not contain %q", docs, want)
		}
	}
}

func TestValidateTemplateRejectsUndeclaredReference(t *testing.T) {
	tmpl := Template{
		Slug:    "broken",
		Version: "1",
		Services: []TemplateService{{
			Slug:  "broken",
			Image: "busybox",
			Env: map[string]TemplateEnv{
				"BAD": {Kind: EnvKindComputedURL, Template: "{{.DOES_NOT_EXIST}}"},
			},
		}},
	}
	if err := ValidateTemplate(tmpl); err == nil {
		t.Fatal("expected validation error for undeclared reference")
	}
}

func TestLookupRejectsUnknown(t *testing.T) {
	if _, err := Lookup("nope"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("Lookup(nope) err = %v, want ErrTemplateNotFound", err)
	}
}

func TestLookupFindsBuiltins(t *testing.T) {
	pg, err := Lookup("postgres")
	if err != nil {
		t.Fatalf("Lookup(postgres): %v", err)
	}
	if pg.Slug != "postgres" {
		t.Fatalf("Lookup(postgres) slug = %q", pg.Slug)
	}
	redis, err := Lookup("Redis")
	if err != nil {
		t.Fatalf("Lookup(Redis): %v", err)
	}
	if redis.Slug != "redis" {
		t.Fatalf("Lookup(Redis) slug = %q", redis.Slug)
	}
}
