// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func computeHubSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type stubGitHubApp struct {
	insts []github.Installation
	repos []github.Repo
	err   error
}

func (s *stubGitHubApp) ListInstallations(_ context.Context) ([]github.Installation, error) {
	return s.insts, s.err
}

func (s *stubGitHubApp) ListReposForInstallation(_ context.Context, _ int64) ([]github.Repo, error) {
	return s.repos, s.err
}

func TestServiceGitHubLinkCRUD(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
	})

	// GET before link -> 404
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/github", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("get before link: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	// PUT to link
	body := `{"installation_id":42,"repo":"acme/web","branch":"main","watch_paths":["services/api"],"root_directory":"apps/web","config_path":"deploy/midnight.toml"}`
	{
		req := httptest.NewRequest(http.MethodPut, "/api/v1alpha/services/"+string(service.ID)+"/github", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("put: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var resp github.LinkPublic
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.InstallationID != 42 || resp.Repo != "acme/web" || resp.Branch != "main" {
			t.Errorf("resp = %+v", resp)
		}
		if len(resp.WatchPaths) != 1 || resp.WatchPaths[0] != "services/api" {
			t.Errorf("watch_paths = %+v", resp.WatchPaths)
		}
		if resp.RootDirectory != "apps/web" || resp.ConfigPath != "deploy/midnight.toml" {
			t.Errorf("source layout = root %q config %q", resp.RootDirectory, resp.ConfigPath)
		}
	}
	// GET after link
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/github", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("get: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	// DELETE unlinks
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/services/"+string(service.ID)+"/github", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("delete: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	// GET after delete -> 404
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/github", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("after delete: status=%d", rr.Code)
		}
	}
}

func TestServiceGitHubLinkValidationFails(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources})
	req := httptest.NewRequest(http.MethodPut, "/api/v1alpha/services/"+string(service.ID)+"/github", strings.NewReader(`{"repo":"bogus"}`))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListInstallationsProxy(t *testing.T) {
	resources := NewMemoryResourceStore()
	app := &stubGitHubApp{insts: []github.Installation{{ID: 1, Account: "acme"}}}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore: resources,
		ServiceStore: resources,
		GitHubApp:    app,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/github/installations", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []github.Installation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Account != "acme" {
		t.Errorf("got = %+v", got)
	}
}

func TestListInstallationReposProxy(t *testing.T) {
	resources := NewMemoryResourceStore()
	app := &stubGitHubApp{repos: []github.Repo{{ID: 1, FullName: "acme/web"}}}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore: resources,
		ServiceStore: resources,
		GitHubApp:    app,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/github/installations/42/repos", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListInstallationReposBadID(t *testing.T) {
	resources := NewMemoryResourceStore()
	app := &stubGitHubApp{}
	router := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, GitHubApp: app})
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/github/installations/notanumber/repos", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestListInstallationsErrorBubbles(t *testing.T) {
	resources := NewMemoryResourceStore()
	app := &stubGitHubApp{err: errors.New("api down")}
	router := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, GitHubApp: app})
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/github/installations", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGitHubWebhookEndToEndQueuesStructuredBuild(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	// Link the service.
	_, err = resources.UpdateServiceSpec(context.Background(), service.ID, func(spec map[string]any) (map[string]any, error) {
		spec["github"] = map[string]any{
			"installation_id": float64(99),
			"repository_id":   float64(4242),
			"repo":            "acme/web",
			"branch":          "main",
			"root_directory":  "apps/web",
			"config_path":     "deploy/midnight.toml",
		}
		return spec, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	queueStore := &recordingQueueStore{jobID: "job_gh_1"}
	operationStore := NewMemoryOperationStore()
	chain := audit.NewMemoryChain()
	clock := func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }
	environments := NewMemoryEnvironmentStore(clock)
	production, err := createDefaultProductionEnvironment(context.Background(), environments, project)
	if err != nil {
		t.Fatal(err)
	}
	deployments := NewMemoryDeploymentStore(clock)
	active, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     string(project.ID),
		ServiceID:     string(service.ID),
		EnvironmentID: string(production.ID),
		Action:        deploy.ActionDeployImage,
		ImageRef:      "registry.example/acme/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TriggerKey:    "seed-active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := deployments.MarkDeploymentDeploying(context.Background(), active.ID, deploy.DeploymentQueued); err != nil {
		t.Fatal(err)
	}
	if err := deployments.PromoteDeployment(context.Background(), active.ID, ""); err != nil {
		t.Fatal(err)
	}

	// Configure a webhook endpoint with a known HMAC secret.
	endpoints := webhook.NewMemoryEndpointResolver()
	const secret = "shh-shared"
	endpoints.Set("acme-ep", webhook.EndpointConfig{
		Provider: webhook.ProviderGitHub,
		Keys:     []webhook.Key{{KID: "whk_a", Material: []byte(secret)}},
	})

	router := NewRouterWithDataStack(DataStack{
		OperationStore:     operationStore,
		IdempotencyStore:   idempotency.NewMemoryStore(),
		ProjectStore:       resources,
		ServiceStore:       resources,
		EnvironmentStore:   environments,
		DeploymentStore:    deployments,
		QueueStore:         queueStore,
		AuditChain:         chain,
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
		Clock:              clock,
	})

	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"repository":{"id":4242,"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"},
		"sender":{"type":"User"},
		"pusher":{"name":"alice"},
		"head_commit":{"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","message":"feat"},
		"commits":[{"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","modified":["main.go"]}],
		"installation":{"id":99},
		"deployment_id":"dep_forged_by_webhook",
		"instance_id":"inst_forged_by_webhook"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/acme-ep", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", computeHubSig(secret, body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-test-1")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d body=%s", len(queueStore.requests), rr.Body.String())
	}
	enqueued := queueStore.requests[0]
	if enqueued.Queue != queue.Build {
		t.Fatalf("queue = %q, want %q", enqueued.Queue, queue.Build)
	}
	var payload deployJobPayload
	if err := json.Unmarshal(queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != deployJobPayloadSchema {
		t.Errorf("schema = %q, want %q", payload.SchemaVersion, deployJobPayloadSchema)
	}
	if payload.Request.Source == nil {
		t.Fatal("request.source = nil, want build-worker source intent")
	}
	if payload.Request.Source.Repo != "https://github.com/acme/web.git" ||
		payload.Request.Source.Ref != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Errorf("source = %+v, want immutable GitHub checkout", payload.Request.Source)
	}
	if payload.Request.Source.Provider != "github" ||
		payload.Request.Source.Repository != "acme/web" ||
		payload.Request.Source.InstallationID != 99 ||
		payload.Request.Source.RootDirectory != "apps/web" ||
		payload.Request.Source.ConfigPath != "deploy/midnight.toml" ||
		payload.Request.Source.Subpath != "" {
		t.Errorf("durable source descriptor = %+v", payload.Request.Source)
	}
	payloadText := string(queueStore.requests[0].Payload)
	for _, forbidden := range []string{"Authorization:", "Bearer ", "x-access-token", "private_key", `"token"`} {
		if strings.Contains(payloadText, forbidden) {
			t.Fatalf("durable job payload contains credential material %q: %s", forbidden, payloadText)
		}
	}
	if payload.Request.EnvironmentID != string(production.ID) {
		t.Errorf("environment_id = %q, want %q", payload.Request.EnvironmentID, production.ID)
	}
	if payload.Request.DeploymentID == "" ||
		payload.Request.DeploymentID == "dep_forged_by_webhook" ||
		payload.Request.InstanceID != payload.Request.DeploymentID+"-r0" ||
		payload.Request.InstanceID == "inst_forged_by_webhook" {
		t.Fatalf("server-minted deployment identity not preserved: request=%+v", payload.Request)
	}
	if payload.PreviousActive == nil || payload.PreviousActive.DeploymentID != active.ID {
		t.Fatalf("previous_active = %+v, want deployment %q", payload.PreviousActive, active.ID)
	}
	canonical, err := deployments.GetDeploymentByOperation(context.Background(), payload.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.ID != payload.Request.DeploymentID ||
		canonical.ExpectedActiveID != active.ID ||
		canonical.TriggerKey == "" ||
		canonical.State != deploy.DeploymentQueued {
		t.Fatalf("canonical deployment = %+v", canonical)
	}
	wantTrigger := canonicalDeploymentTriggerKey(service, DeployIntakeRequest{
		EnvironmentID: string(production.ID),
		Source:        &DeploySourceIntent{},
	}, idempotency.Key("gh-push-99-4242-v1-deadbeefdeadbeefdeadbeefdeadbeefdeadbeef-"+string(service.ID)))
	if canonical.TriggerKey != wantTrigger {
		t.Fatalf("canonical trigger = %q, want deterministic %q", canonical.TriggerKey, wantTrigger)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Kind != "build" {
		t.Fatalf("operations = %#v, want one queued build operation", operations)
	}

	// A redelivery with a different GitHub delivery id still represents the
	// same deterministic source intent and must not create another job.
	retry := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/acme-ep", strings.NewReader(string(body)))
	retry.Header.Set("X-Hub-Signature-256", computeHubSig(secret, body))
	retry.Header.Set("X-GitHub-Event", "push")
	retry.Header.Set("X-GitHub-Delivery", "delivery-test-2")
	retryResponse := httptest.NewRecorder()
	router.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusAccepted {
		t.Fatalf("redelivery status = %d body=%s", retryResponse.Code, retryResponse.Body.String())
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("redelivery queued %d jobs, want exactly one", len(queueStore.requests))
	}
	operations, err = operationStore.List(context.Background(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("redelivery created %d operations, want exactly one", len(operations))
	}
}

func TestGitHubWebhookPingSkipped(t *testing.T) {
	resources := NewMemoryResourceStore()
	queueStore := &recordingQueueStore{}
	endpoints := webhook.NewMemoryEndpointResolver()
	const secret = "shh"
	endpoints.Set("acme-ep", webhook.EndpointConfig{Provider: webhook.ProviderGitHub, Keys: []webhook.Key{{KID: "whk_a", Material: []byte(secret)}}})
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         queueStore,
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
	})
	body := []byte(`{"zen":"non sibi sed patriae"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/acme-ep", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", computeHubSig(secret, body))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "ping-1")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("ping status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(queueStore.requests) != 0 {
		t.Errorf("expected no queue jobs on ping")
	}
}

func TestGitHubWebhookBadSignatureRejected(t *testing.T) {
	resources := NewMemoryResourceStore()
	queueStore := &recordingQueueStore{}
	endpoints := webhook.NewMemoryEndpointResolver()
	endpoints.Set("acme-ep", webhook.EndpointConfig{Provider: webhook.ProviderGitHub, Keys: []webhook.Key{{KID: "whk_a", Material: []byte("shh")}}})
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         queueStore,
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
	})
	body := []byte(`{"zen":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/acme-ep", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
	req.Header.Set("X-GitHub-Event", "ping")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestGitHubWebhookPushToUnlinkedBranchSkipped(t *testing.T) {
	resources := NewMemoryResourceStore()
	queueStore := &recordingQueueStore{}
	endpoints := webhook.NewMemoryEndpointResolver()
	const secret = "shh"
	endpoints.Set("acme-ep", webhook.EndpointConfig{Provider: webhook.ProviderGitHub, Keys: []webhook.Key{{KID: "whk_a", Material: []byte(secret)}}})
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         queueStore,
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
	})
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"abcabcabcabcabcabcabcabcabcabcabcabcabca",
		"repository":{"id":4242,"full_name":"acme/web"},
		"sender":{"type":"User"},
		"commits":[],
		"installation":{"id":99}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/acme-ep", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", computeHubSig(secret, body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-no-link")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(queueStore.requests) != 0 {
		t.Errorf("expected no jobs queued: %+v", queueStore.requests)
	}
}

// stubWaitForCIGate counts evaluations and gates by default.
type stubWaitForCIGate struct {
	mu      sync.Mutex
	allowed bool
	calls   int
}

func (s *stubWaitForCIGate) Evaluate(_ context.Context, _ github.DeployIntent) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.allowed, "pending-build-check", nil
}

func TestGitHubWebhookDispatcherWaitForCIDefersDeploy(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, _ := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	service, _ := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	_, _ = resources.UpdateServiceSpec(context.Background(), service.ID, func(spec map[string]any) (map[string]any, error) {
		spec["github"] = map[string]any{
			"installation_id": float64(99),
			"repository_id":   float64(4242),
			"repo":            "acme/web",
			"branch":          "main",
			"wait_for_ci":     true,
		}
		return spec, nil
	})
	queueStore := &recordingQueueStore{}
	operationStore := NewMemoryOperationStore()
	chain := audit.NewMemoryChain()
	emitter := newAuditEmitter(chain, time.Now)
	gate := &stubWaitForCIGate{allowed: false}
	disp := NewGitHubWebhookDispatcher(resources, operationStore, queueStore, emitter, time.Now, gate)
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"repository":{"id":4242,"full_name":"acme/web"},
		"sender":{"type":"User"},
		"commits":[{"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","modified":["x.go"]}],
		"installation":{"id":99}
	}`)
	if err := disp.Dispatch(context.Background(), webhook.ReceivedDelivery{Provider: webhook.ProviderGitHub, Body: body, DeliveryID: "d1"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(queueStore.requests) != 0 {
		t.Errorf("expected deploy gated, got %d jobs", len(queueStore.requests))
	}
	if gate.calls != 1 {
		t.Errorf("calls = %d", gate.calls)
	}
}

func TestGitHubWebhookDispatcherWaitForCIPassesAfterGate(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, _ := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	service, _ := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	_, _ = resources.UpdateServiceSpec(context.Background(), service.ID, func(spec map[string]any) (map[string]any, error) {
		spec["github"] = map[string]any{
			"installation_id": float64(99),
			"repository_id":   float64(4242),
			"repo":            "acme/web",
			"branch":          "main",
			"wait_for_ci":     true,
		}
		return spec, nil
	})
	queueStore := &recordingQueueStore{jobID: "ci_passed"}
	operationStore := NewMemoryOperationStore()
	environments := NewMemoryEnvironmentStore(time.Now)
	if _, err := createDefaultProductionEnvironment(context.Background(), environments, project); err != nil {
		t.Fatal(err)
	}
	disp := NewGitHubWebhookDispatcher(resources, operationStore, queueStore, nil, time.Now, &stubWaitForCIGate{allowed: true}, environments)
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"repository":{"id":4242,"full_name":"acme/web"},
		"sender":{"type":"User"},
		"commits":[{"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","modified":["x.go"]}],
		"installation":{"id":99}
	}`)
	if err := disp.Dispatch(context.Background(), webhook.ReceivedDelivery{Provider: webhook.ProviderGitHub, Body: body, DeliveryID: "d2"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(queueStore.requests) != 1 {
		t.Errorf("expected 1 job after gate, got %d", len(queueStore.requests))
	}
}

func TestGitHubWebhookDispatcherEnqueueFailureFailsCanonicalDeployment(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.UpdateServiceSpec(context.Background(), service.ID, func(spec map[string]any) (map[string]any, error) {
		spec["github"] = map[string]any{
			"installation_id": float64(99),
			"repository_id":   float64(4242),
			"repo":            "acme/web",
			"branch":          "main",
		}
		return spec, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC) }
	environments := NewMemoryEnvironmentStore(clock)
	if _, err := createDefaultProductionEnvironment(context.Background(), environments, project); err != nil {
		t.Fatal(err)
	}
	deployments := NewMemoryDeploymentStore(clock)
	operations := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{err: errors.New("queue unavailable")}
	dispatcher := NewGitHubWebhookDispatcher(
		resources,
		operations,
		queueStore,
		nil,
		clock,
		nil,
		environments,
	).WithDeploymentStore(deployments)

	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"feedfacefeedfacefeedfacefeedfacefeedface",
		"repository":{"id":4242,"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"},
		"sender":{"type":"User"},
		"commits":[{"id":"feedfacefeedfacefeedfacefeedfacefeedface","modified":["main.go"]}],
		"installation":{"id":99}
	}`)
	err = dispatcher.Dispatch(context.Background(), webhook.ReceivedDelivery{
		Provider:   webhook.ProviderGitHub,
		EventType:  string(github.EventPush),
		DeliveryID: "delivery-enqueue-failure",
		Body:       body,
	})
	if err == nil || !strings.Contains(err.Error(), "queue unavailable") {
		t.Fatalf("Dispatch() error = %v, want queue failure", err)
	}
	records, err := deployments.ListDeploymentsByService(context.Background(), string(service.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 ||
		records[0].State != deploy.DeploymentFailed ||
		records[0].FailureCode != string(failurecodes.DeployJobEnqueueFailed) {
		t.Fatalf("canonical deployments = %+v, want failed enqueue record", records)
	}
	operationRows, err := operations.List(context.Background(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operationRows) != 1 ||
		operationRows[0].State != orailtypes.OperationFailed ||
		operationRows[0].Code != orailtypes.FailureCode(failurecodes.DeployJobEnqueueFailed) {
		t.Fatalf("operations = %+v, want failed enqueue operation", operationRows)
	}
}
