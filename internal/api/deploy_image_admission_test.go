// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/registry"
)

const admissionTestDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

// fakeImageResolver stands in for a registry. Known maps a caller reference to
// the digest the registry holds; anything else is reported missing unless
// Unavailable is set.
type fakeImageResolver struct {
	known       map[string]string
	unavailable bool
	calls       []string
}

func (f *fakeImageResolver) Resolve(_ context.Context, reference string) (registry.Resolution, error) {
	f.calls = append(f.calls, reference)
	if f.unavailable {
		return registry.Resolution{}, fmt.Errorf("%w: dial tcp: connection refused", registry.ErrRegistryUnavailable)
	}
	parsed, err := registry.ParseImageReference(reference)
	if err != nil {
		return registry.Resolution{}, fmt.Errorf("%w: %s", registry.ErrReferenceInvalid, err)
	}
	digest, ok := f.known[reference]
	if !ok {
		return registry.Resolution{}, registry.ErrManifestNotFound
	}
	name := parsed.Repository
	if parsed.Registry != "" {
		name = parsed.Registry + "/" + parsed.Repository
	}
	if parsed.Tag != "" {
		name += ":" + parsed.Tag
	}
	return registry.Resolution{
		Reference:  name + "@" + digest,
		Registry:   parsed.Registry,
		Repository: parsed.Repository,
		Tag:        parsed.Tag,
		Digest:     digest,
		Pinned:     parsed.Digest == "",
	}, nil
}

type admissionHarness struct {
	router     http.Handler
	service    Service
	project    Project
	queueStore *recordingQueueStore
	operations *MemoryOperationStore
	deploys    *MemoryDeploymentStore
	resources  *MemoryResourceStore
}

func newAdmissionHarness(t *testing.T, spec map[string]any) *admissionHarness {
	t.Helper()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web", Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	operations := NewMemoryOperationStore()
	deployments := NewMemoryDeploymentStore(func() time.Time { return now })
	queueStore := &recordingQueueStore{jobID: "job_admission"}
	handler := NewRouterWithDataStack(DataStack{
		OperationStore:   operations,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		DeploymentStore:  deployments,
		AuditChain:       audit.NewMemoryChain(),
		Clock:            func() time.Time { return now },
	})
	return &admissionHarness{
		router:     handler,
		service:    service,
		project:    project,
		queueStore: queueStore,
		operations: operations,
		deploys:    deployments,
		resources:  resources,
	}
}

func (h *admissionHarness) post(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/services/"+string(h.service.ID)+"/deployments",
		strings.NewReader(body),
	)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem body %q: %v", recorder.Body.String(), err)
	}
	return problem.Code
}

func TestDeployIntakePinsMutableTagToResolvedDigest(t *testing.T) {
	defer SetDeployImageResolver(&fakeImageResolver{
		known: map[string]string{"nginx:1.27": admissionTestDigest},
	})()

	harness := newAdmissionHarness(t, nil)
	recorder := harness.post(t, "tag-deploy-1", `{"environment_id":"env_prod","image":"nginx:1.27"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}

	records, err := harness.deploys.ListDeploymentsByService(context.Background(), string(harness.service.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("deployment records = %d, want 1", len(records))
	}
	if want := "nginx:1.27@" + admissionTestDigest; records[0].ImageRef != want {
		t.Fatalf("image ref = %q, want the tag preserved alongside the resolved digest %q", records[0].ImageRef, want)
	}
	if records[0].ImageDigest != admissionTestDigest {
		t.Fatalf("image digest = %q, want %q", records[0].ImageDigest, admissionTestDigest)
	}
	if len(harness.queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(harness.queueStore.requests))
	}
	var payload struct {
		Candidate struct {
			Image string `json:"image"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(harness.queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(payload.Candidate.Image, "@"+admissionTestDigest) {
		t.Fatalf("queued candidate image = %q, want the pinned digest", payload.Candidate.Image)
	}
}

func TestDeployIntakeRejectsDigestTheRegistryDoesNotHave(t *testing.T) {
	resolver := &fakeImageResolver{known: map[string]string{}}
	defer SetDeployImageResolver(resolver)()

	harness := newAdmissionHarness(t, nil)
	bogus := "nginx:1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	recorder := harness.post(t, "bogus-digest-1", `{"environment_id":"env_prod","image":"`+bogus+`"}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != string(failurecodes.ResourceValidationFailed) {
		t.Fatalf("code = %q, want %q", code, failurecodes.ResourceValidationFailed)
	}
	if len(harness.queueStore.requests) != 0 {
		t.Fatalf("queue requests = %d, want nothing queued for an image that does not exist", len(harness.queueStore.requests))
	}
	records, err := harness.deploys.ListDeploymentsByService(context.Background(), string(harness.service.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("deployment records = %d, want none", len(records))
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != bogus {
		t.Fatalf("resolver calls = %v, want exactly the caller reference", resolver.calls)
	}
}

func TestDeployIntakeFailsClosedWhenTheRegistryIsUnreachable(t *testing.T) {
	defer SetDeployImageResolver(&fakeImageResolver{unavailable: true})()

	harness := newAdmissionHarness(t, nil)
	recorder := harness.post(t, "registry-down-1",
		`{"environment_id":"env_prod","image":"registry.example/web@`+admissionTestDigest+`"}`)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != string(failurecodes.CapabilityUnavailable) {
		t.Fatalf("code = %q, want %q; an unreachable registry is not the caller's mistake", code, failurecodes.CapabilityUnavailable)
	}
	if len(harness.queueStore.requests) != 0 {
		t.Fatalf("queue requests = %d, want nothing queued", len(harness.queueStore.requests))
	}
}

func TestDeployIntakeKeepsHistoricalContractWhenNoResolverIsConfigured(t *testing.T) {
	defer SetDeployImageResolver(nil)()

	harness := newAdmissionHarness(t, nil)
	if recorder := harness.post(t, "no-resolver-tag", `{"environment_id":"env_prod","image":"nginx:1.27"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("tag status = %d, want 400 while image admission is unconfigured", recorder.Code)
	}
	recorder := harness.post(t, "no-resolver-digest",
		`{"environment_id":"env_prod","image":"registry.example/web@`+admissionTestDigest+`"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("digest status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
}

func TestAdmitDeployIntakeImageMapsResolverErrors(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		err    error
		status int
		code   failurecodes.Code
	}{
		{"missing", registry.ErrManifestNotFound, http.StatusBadRequest, failurecodes.ResourceValidationFailed},
		{"denied", registry.ErrRegistryDenied, http.StatusBadRequest, failurecodes.ResourceValidationFailed},
		{"invalid", registry.ErrReferenceInvalid, http.StatusBadRequest, failurecodes.ResourceValidationFailed},
		{"unavailable", registry.ErrRegistryUnavailable, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable},
		{"unknown", errors.New("boom"), http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			restore := SetDeployImageResolver(erroringResolver{err: testCase.err})
			defer restore()
			request := DeployIntakeRequest{Image: "nginx:1.27"}
			failure := admitDeployIntakeImage(context.Background(), &request)
			if failure == nil {
				t.Fatal("failure = nil, want a typed admission rejection")
			}
			if failure.Status != testCase.status || failure.Code != testCase.code {
				t.Fatalf("failure = %d/%s, want %d/%s", failure.Status, failure.Code, testCase.status, testCase.code)
			}
		})
	}
}

type erroringResolver struct{ err error }

func (e erroringResolver) Resolve(context.Context, string) (registry.Resolution, error) {
	return registry.Resolution{}, e.err
}

// -- Deploy now --------------------------------------------------------------

func TestDeployNowRepeatsTheLastImageTheServiceRan(t *testing.T) {
	defer SetDeployImageResolver(nil)()
	harness := newAdmissionHarness(t, nil)

	previous := "registry.example/web@" + admissionTestDigest
	if _, err := harness.deploys.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     string(harness.project.ID),
		ServiceID:     string(harness.service.ID),
		EnvironmentID: "env_prod",
		Action:        deploy.ActionDeployImage,
		ImageRef:      previous,
		ImageDigest:   admissionTestDigest,
		State:         deploy.DeploymentActive,
		Active:        true,
		TriggerKey:    "trigger:test:1",
	}); err != nil {
		t.Fatal(err)
	}

	recorder := harness.post(t, "deploy-now-image", `{"environment_id":"env_prod","deploy_now":true}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
	if len(harness.queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(harness.queueStore.requests))
	}
	if harness.queueStore.requests[0].Queue != queue.Deploy {
		t.Fatalf("queue = %q, want the deploy queue for an image replay", harness.queueStore.requests[0].Queue)
	}
	var payload struct {
		Candidate struct {
			Image string `json:"image"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(harness.queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Candidate.Image != previous {
		t.Fatalf("candidate image = %q, want the last image the service ran (%q)", payload.Candidate.Image, previous)
	}
}

func TestDeployNowBuildsTheLinkedSourceForAServiceThatNeverDeployed(t *testing.T) {
	defer SetDeployImageResolver(nil)()
	harness := newAdmissionHarness(t, map[string]any{
		"github": map[string]any{
			"installation_id": float64(42),
			"repo":            "acme/web",
			"branch":          "main",
			"clone_url":       "https://github.com/acme/web.git",
		},
	})

	recorder := harness.post(t, "deploy-now-source", `{"environment_id":"env_prod","deploy_now":true}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
	if len(harness.queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(harness.queueStore.requests))
	}
	if harness.queueStore.requests[0].Queue != queue.Build {
		t.Fatalf("queue = %q, want the build queue for a source deploy", harness.queueStore.requests[0].Queue)
	}
	var payload struct {
		Request struct {
			Source struct {
				Provider string `json:"provider"`
				Repo     string `json:"repo"`
				Ref      string `json:"ref"`
			} `json:"source"`
		} `json:"request"`
	}
	if err := json.Unmarshal(harness.queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	source := payload.Request.Source
	if source.Provider != "git" || source.Repo != "https://github.com/acme/web.git" || source.Ref != "main" {
		t.Fatalf("queued source = %#v, want the linked repository at its branch", source)
	}
	records, err := harness.deploys.ListDeploymentsByService(context.Background(), string(harness.service.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Action != deploy.ActionDeploySource {
		t.Fatalf("deployment records = %#v, want one canonical source deployment", records)
	}
}

func TestDeployNowUsesTheInstalledSourceLinkLookupFirst(t *testing.T) {
	defer SetDeployImageResolver(nil)()
	defer SetServiceSourceLinkLookup(stubSourceLinkLookup{
		link: ServiceSourceLink{
			Provider: "git",
			Repo:     "https://github.com/acme/tenant-linked.git",
			Ref:      "release",
		},
		found: true,
	})()

	harness := newAdmissionHarness(t, nil)
	recorder := harness.post(t, "deploy-now-lookup", `{"environment_id":"env_prod","deploy_now":true}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Request struct {
			Source struct {
				Repo string `json:"repo"`
				Ref  string `json:"ref"`
			} `json:"source"`
		} `json:"request"`
	}
	if err := json.Unmarshal(harness.queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Request.Source.Repo != "https://github.com/acme/tenant-linked.git" || payload.Request.Source.Ref != "release" {
		t.Fatalf("queued source = %#v, want the canonical link lookup result", payload.Request.Source)
	}
}

type stubSourceLinkLookup struct {
	link  ServiceSourceLink
	found bool
}

func (s stubSourceLinkLookup) LookupServiceSourceLink(context.Context, string, string) (ServiceSourceLink, bool, error) {
	return s.link, s.found, nil
}

func TestDeployNowRejectsAServiceWithNothingToDeploy(t *testing.T) {
	defer SetDeployImageResolver(nil)()
	harness := newAdmissionHarness(t, nil)

	recorder := harness.post(t, "deploy-now-empty", `{"environment_id":"env_prod","deploy_now":true}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != string(failurecodes.ResourceValidationFailed) {
		t.Fatalf("code = %q, want %q", code, failurecodes.ResourceValidationFailed)
	}
	if len(harness.queueStore.requests) != 0 {
		t.Fatalf("queue requests = %d, want none", len(harness.queueStore.requests))
	}
}

func TestDeployNowRejectsAnExplicitImageOrSource(t *testing.T) {
	defer SetDeployImageResolver(nil)()
	harness := newAdmissionHarness(t, nil)

	recorder := harness.post(t, "deploy-now-conflict",
		`{"environment_id":"env_prod","deploy_now":true,"image":"registry.example/web@`+admissionTestDigest+`"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if len(harness.queueStore.requests) != 0 {
		t.Fatalf("queue requests = %d, want none", len(harness.queueStore.requests))
	}
}

func TestValidateDeployIntakeRequestRefusesUnresolvedDeployNow(t *testing.T) {
	code, _, _, ok := validateDeployIntakeRequest(DeployIntakeRequest{
		EnvironmentID: "env_prod",
		DeployNow:     true,
	})
	if ok || code != failurecodes.ResourceValidationFailed {
		t.Fatalf("validate = ok %v code %q, want a rejection", ok, code)
	}
}
