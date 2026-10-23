// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type fakeCommunityDiscoverer func(context.Context, string, string) (templates.CommunityDiscovery, error)

func (f fakeCommunityDiscoverer) Discover(ctx context.Context, source, ref string) (templates.CommunityDiscovery, error) {
	return f(ctx, source, ref)
}

type fakeCommunityForkStore struct {
	fork *templates.CommunityFork
}

func (s *fakeCommunityForkStore) CreateFork(_ context.Context, fork templates.CommunityFork) (templates.CommunityFork, error) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	fork.ResourceVersion = 1
	fork.CreatedAt = now
	fork.UpdatedAt = now
	s.fork = &fork
	return fork, nil
}

func (s *fakeCommunityForkStore) ListForks(_ context.Context, projectID string) ([]templates.CommunityFork, error) {
	if s.fork == nil || s.fork.ProjectID != projectID {
		return []templates.CommunityFork{}, nil
	}
	return []templates.CommunityFork{*s.fork}, nil
}

func (s *fakeCommunityForkStore) GetFork(_ context.Context, projectID, id string) (templates.CommunityFork, error) {
	if s.fork == nil || s.fork.ProjectID != projectID || s.fork.ID != id {
		return templates.CommunityFork{}, templates.ErrCommunityForkNotFound
	}
	return *s.fork, nil
}

func (s *fakeCommunityForkStore) UpdateFork(_ context.Context, projectID, id string, expected int64, draft templates.CommunityManifest, findings []templates.ValidationFinding) (templates.CommunityFork, error) {
	if s.fork == nil || s.fork.ProjectID != projectID || s.fork.ID != id {
		return templates.CommunityFork{}, templates.ErrCommunityForkNotFound
	}
	if s.fork.ResourceVersion != expected {
		return templates.CommunityFork{}, templates.ErrCommunityForkConflict
	}
	s.fork.Draft = draft
	s.fork.Findings = findings
	s.fork.Valid = len(findings) == 0
	s.fork.ResourceVersion++
	s.fork.UpdatedAt = s.fork.UpdatedAt.Add(time.Minute)
	return *s.fork, nil
}

func TestCommunityTemplateDiscoverRouteReturnsServerTrust(t *testing.T) {
	discovery := validAPIDiscovery()
	router := NewRouterWithDataStack(DataStack{
		CommunityTemplateDiscoverer: fakeCommunityDiscoverer(func(_ context.Context, source, ref string) (templates.CommunityDiscovery, error) {
			if source != "https://github.com/acme/queue" || ref != discovery.Provenance.Ref {
				t.Fatalf("discover input = %q %q", source, ref)
			}
			return discovery, nil
		}),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/community/discover", strings.NewReader(`{"source":"https://github.com/acme/queue","ref":"`+discovery.Provenance.Ref+`"}`))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"trust_class":"community"`) || !strings.Contains(rr.Body.String(), discovery.Provenance.ManifestSHA256) {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestCommunityTemplateForkCreateAndConditionalEdit(t *testing.T) {
	discovery := validAPIDiscovery()
	store := &fakeCommunityForkStore{}
	router := NewRouterWithDataStack(DataStack{
		CommunityTemplateDiscoverer: fakeCommunityDiscoverer(func(context.Context, string, string) (templates.CommunityDiscovery, error) { return discovery, nil }),
		CommunityTemplateForks:      store,
	})
	createBody := `{"project_id":"prj_demo","source":"https://github.com/acme/queue","ref":"` + discovery.Provenance.Ref + `","expected_manifest_sha256":"` + discovery.Provenance.ManifestSHA256 + `"}`
	create := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/community/forks", strings.NewReader(createBody))
	create.Header.Set("Idempotency-Key", "community-fork-create")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	if created.Header().Get("ETag") != `"1"` || store.fork == nil || store.fork.TrustClass != "local" {
		t.Fatalf("create headers=%v fork=%+v", created.Header(), store.fork)
	}

	draft := discovery.Manifest
	draft.Template.Services[0].Image = "acme/queue:latest"
	encodedDraft, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	updateBody := `{"project_id":"prj_demo","draft":` + string(encodedDraft) + `}`
	update := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/templates/community/forks/"+store.fork.ID, strings.NewReader(updateBody))
	update.Header.Set("If-Match", `"1"`)
	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updated.Code, updated.Body.String())
	}
	if updated.Header().Get("ETag") != `"2"` || store.fork.Valid || len(store.fork.Findings) == 0 {
		t.Fatalf("update headers=%v fork=%+v", updated.Header(), store.fork)
	}

	stale := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/templates/community/forks/"+store.fork.ID, strings.NewReader(updateBody))
	stale.Header.Set("If-Match", `"1"`)
	staleResponse := httptest.NewRecorder()
	router.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale status = %d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestCommunityTemplateForkRequiresDiscoveryDigestMatch(t *testing.T) {
	discovery := validAPIDiscovery()
	store := &fakeCommunityForkStore{}
	router := NewRouterWithDataStack(DataStack{
		CommunityTemplateDiscoverer: fakeCommunityDiscoverer(func(context.Context, string, string) (templates.CommunityDiscovery, error) { return discovery, nil }),
		CommunityTemplateForks:      store,
	})
	body := `{"project_id":"prj_demo","source":"https://github.com/acme/queue","ref":"` + discovery.Provenance.Ref + `","expected_manifest_sha256":"` + strings.Repeat("b", 64) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/community/forks", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "community-fork-digest")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusPreconditionFailed || store.fork != nil {
		t.Fatalf("status = %d body=%s fork=%+v", rr.Code, rr.Body.String(), store.fork)
	}
}

func TestCommunityTemplateForkRoutesFailHonestlyWithoutDurableStore(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates/community/forks?project_id=prj_demo", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "CAPABILITY_UNAVAILABLE") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCommunityTemplateProjectAuthorizationSeparatesReadAndEdit(t *testing.T) {
	accepted := time.Now().UTC()
	access := identity.NewMemoryAccessStore()
	access.SeedWorkspace(identity.Workspace{ID: "wrk_demo", Slug: "demo"})
	access.SeedProject("prj_demo", "wrk_demo",
		identity.ProjectMember{ProjectID: "prj_demo", User: identity.User{ID: "usr_viewer"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_demo", User: identity.User{ID: "usr_editor"}, Role: identity.ProjectRoleEditor, AcceptedAt: &accepted},
	)
	requestFor := func(userID string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates/community/forks?project_id=prj_demo", nil)
		ctx := withProductPrincipal(req.Context(), productPrincipal{Kind: productPrincipalHuman, Human: identity.Principal{UserID: userID}})
		return req.WithContext(ctx)
	}
	if rr := httptest.NewRecorder(); !authorizeCommunityProject(rr, requestFor("usr_viewer"), access, "prj_demo", false) {
		t.Fatalf("viewer read denied: %d %s", rr.Code, rr.Body.String())
	}
	if rr := httptest.NewRecorder(); authorizeCommunityProject(rr, requestFor("usr_viewer"), access, "prj_demo", true) || rr.Code != http.StatusNotFound {
		t.Fatalf("viewer edit status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := httptest.NewRecorder(); !authorizeCommunityProject(rr, requestFor("usr_editor"), access, "prj_demo", true) {
		t.Fatalf("editor edit denied: %d %s", rr.Code, rr.Body.String())
	}
}

type missingCommunityServiceConfigs struct{}

func (missingCommunityServiceConfigs) Get(context.Context, string, string, string) (serviceconfig.Config, error) {
	return serviceconfig.Config{}, serviceconfig.ErrNotFound
}

func (missingCommunityServiceConfigs) Put(context.Context, serviceconfig.Config, int64) (serviceconfig.Config, error) {
	return serviceconfig.Config{}, errors.New("unexpected service config write")
}

func TestCompileCommunityTemplateChangesProducesReviewableDesiredState(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{ID: "prj_demo", WorkspaceID: "wrk_demo", Slug: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ID: "svc_demo", ProjectID: project.ID, Slug: "worker", DisplayName: "Worker"})
	if err != nil {
		t.Fatal(err)
	}
	fork := templates.CommunityFork{ProjectID: string(project.ID), Draft: templates.CommunityManifest{Template: templates.Template{Services: []templates.TemplateService{{
		Slug:  "worker",
		Image: "ghcr.io/acme/worker@sha256:" + strings.Repeat("a", 64),
		Ports: []templates.TemplatePort{{Port: 9123, Protocol: "tcp"}},
		Env: map[string]templates.TemplateEnv{
			"MODE":  {Kind: templates.EnvKindLiteral, Value: "queue"},
			"TOKEN": {Kind: templates.EnvKindGenerated, GeneratedKind: templates.GeneratedKindPassword, Length: 24, Secret: true},
		},
		Volume: &templates.TemplateVolume{Name: "data", MountPath: "/var/lib/worker", SizeBytes: 1024},
	}}}}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	changes, err := compileCommunityTemplateChanges(req, templateDeps{serviceStore: resources, serviceConfigs: missingCommunityServiceConfigs{}}, fork, "env_demo", map[string]string{"worker": string(service.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 5 {
		t.Fatalf("changes = %d, want runtime + private domain + 2 variables + storage", len(changes))
	}
	if runtime := changes[0]; runtime.Kind != "runtime" || runtime.Operation != "add" || runtime.Runtime == nil || runtime.Runtime.TargetPort != 9123 {
		t.Fatalf("runtime change = %#v", runtime)
	}
	byName := map[string]EnvironmentChangesetInput{}
	for _, change := range changes {
		byName[change.Name] = change
	}
	if mode := byName["MODE"]; mode.Scope == nil || *mode.Scope != "plain" || mode.Value == nil || *mode.Value != "queue" {
		t.Fatalf("MODE change = %#v", mode)
	}
	if privateDomain := byName["MIDNIGHT_PRIVATE_DOMAIN"]; privateDomain.Value == nil || *privateDomain.Value != "worker.midnight.internal" {
		t.Fatalf("private-domain change = %#v", privateDomain)
	}
	if token := byName["TOKEN"]; token.Scope == nil || *token.Scope != "sealed" || token.Value == nil || len(*token.Value) != 24 {
		t.Fatal("generated sealed TOKEN change was not compiled")
	}
	if storage := byName["data"]; storage.Kind != "storage" || storage.Storage == nil || storage.Storage.MountPath != "/var/lib/worker" {
		t.Fatalf("storage change = %#v", storage)
	}
	if _, err := resources.GetService(context.Background(), orailtypes.ID(service.ID)); err != nil {
		t.Fatalf("compile mutated service identity: %v", err)
	}
}

func validAPIDiscovery() templates.CommunityDiscovery {
	manifest := templates.CommunityManifest{
		SchemaVersion: 1,
		Template: templates.Template{
			Slug: "queue-worker", Name: "Queue worker", Version: "1.0.0", Summary: "Pinned queue worker",
			Services: []templates.TemplateService{{Slug: "worker", Image: "ghcr.io/acme/worker@sha256:" + strings.Repeat("a", 64)}},
		},
		Metadata: templates.CommunityMetadata{
			Maintainer: "Acme", License: "Apache-2.0", SecurityReview: "unreviewed", RiskClass: "moderate",
			UpdatePolicy: "Monthly", DeprecationPolicy: "Six months", ReadmePath: "README.md",
			SBOM: templates.CommunitySBOM{Path: "sbom.json", SHA256: strings.Repeat("c", 64)},
		},
	}
	return templates.CommunityDiscovery{
		TrustClass: "community", Manifest: manifest, Readme: "# Queue", Valid: true, Findings: []templates.ValidationFinding{},
		Provenance: templates.CommunityProvenance{
			Source: "https://github.com/acme/queue", Ref: strings.Repeat("d", 40), ManifestPath: "template.yaml",
			ManifestSHA256: strings.Repeat("a", 64), ReadmePath: "README.md", ReadmeSHA256: strings.Repeat("b", 64),
			SBOMPath: "sbom.json", SBOMSHA256: strings.Repeat("c", 64),
		},
	}
}
