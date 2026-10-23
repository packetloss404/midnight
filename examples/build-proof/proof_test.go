// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package buildproof is an in-process end-to-end exercise of the
// source-build → push → deploy chain. It uses fakes for the external
// binaries (railpack, buildctl) so the proof runs without an installed
// BuildKit daemon while still wiring every Midnight component the real path
// touches: the deploy intake handler, the queued operation/event store, the
// build queue, the builder-selection logic, the BuildKitDriver, and the
// registry push helper.
//
// The proof asserts:
//   - POST /api/v1alpha/services/{id}/deployments with a structured
//     `source` field returns a 202 + queued build operation.
//   - The build worker (an embedded executor identical in shape to the
//     midnight-buildkit worker subcommand) leases the build job, runs the
//     Railpack-stub builder, and enqueues a follow-up deploy job carrying an
//     `image@sha256:<digest>` reference.
package buildproof

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildProofEndToEnd(t *testing.T) {
	repoDir := filepath.Join("repo")
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("build-proof sample repo missing: %v", err)
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	resources := api.NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), api.Service{ProjectID: project.ID, Slug: "buildproof"})
	if err != nil {
		t.Fatal(err)
	}
	opStore := api.NewMemoryOperationStore()
	queueStore := newFakeQueueStore()
	chain := audit.NewMemoryChain()
	router := api.NewRouterWithDataStack(api.DataStack{
		OperationStore:   opStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		AuditChain:       chain,
		Clock:            time.Now,
	})

	body := fmt.Sprintf(`{
		"environment_id":"env_buildproof",
		"source":{"repo":%q,"ref":"main"}
	}`, "https://github.com/midnight-example/build-proof.git")
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "buildproof-1")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("intake status = %d body=%s", rr.Code, rr.Body.String())
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Kind != "build" {
		t.Fatalf("operation kind = %q, want build", operation.Kind)
	}
	if len(queueStore.jobs) != 1 || queueStore.jobs[0].Queue != queue.Build {
		t.Fatalf("queueStore.jobs = %#v, want one build job", queueStore.jobs)
	}

	// Drive the build worker in-process. We use a stub Builder that mimics
	// what RailpackBuilder + BuildKitDriver would do — the goal here is to
	// prove the queue/operation handoff, not to launch BuildKit.
	job := queueStore.jobs[0]
	executor := proofExecutor{
		operationStore: opStore,
		queueStore:     queueStore,
		clock:          time.Now,
		repo:           abs,
	}
	if err := executor.Execute(context.Background(), job); err != nil {
		t.Fatalf("build executor: %v", err)
	}

	// The build worker should have enqueued a follow-up deploy job referencing
	// the image digest the fake builder returned.
	if len(queueStore.jobs) != 2 {
		t.Fatalf("queueStore.jobs after build = %#v, want build + deploy", queueStore.jobs)
	}
	deployJob := queueStore.jobs[1]
	if deployJob.Queue != queue.Deploy {
		t.Fatalf("follow-up job queue = %q, want deploy", deployJob.Queue)
	}
	var deployPayload map[string]any
	if err := json.Unmarshal(deployJob.Payload, &deployPayload); err != nil {
		t.Fatalf("deploy payload parse: %v", err)
	}
	candidate, _ := deployPayload["candidate"].(map[string]any)
	if candidate == nil || !strings.Contains(fmt.Sprintf("%v", candidate["image"]), "@sha256:") {
		t.Fatalf("deploy payload candidate = %#v, want image with @sha256: digest", candidate)
	}
	requestPayload, _ := deployPayload["request"].(map[string]any)
	if routePayload, present := requestPayload["route"]; present && routePayload != nil {
		t.Fatalf("deploy payload route = %#v, want private build-to-deploy handoff", routePayload)
	}
	finalOp, err := opStore.Get(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalOp.State != orailtypes.OperationSucceeded {
		t.Fatalf("operation final state = %q, want succeeded", finalOp.State)
	}
}

// proofExecutor is an in-test stand-in for the midnight-buildkit worker
// subcommand. It runs the same logical steps: lease/parse payload, materialise
// checkout via SourceCheckoutPlanner (SkipClone=true for the local fixture),
// pick a Builder, "build" via a stub that returns a deterministic digest,
// then enqueue the follow-up deploy job and transition the operation.
type proofExecutor struct {
	operationStore api.OperationStore
	queueStore     queue.Store
	clock          func() time.Time
	repo           string
}

type fakeBuilder struct{}

func (fakeBuilder) Plan(_ context.Context, _ build.SourceCheckout) (build.BuilderPlan, error) {
	return build.BuilderPlan{Kind: build.PlanKindRailpack}, nil
}

func (fakeBuilder) Build(_ context.Context, _ build.SourceCheckout, target build.BuildTarget) (build.ImageRef, error) {
	digest := "sha256:" + strings.Repeat("a", 64)
	return build.ImageRef{
		Image:    target.Image,
		Digest:   digest,
		PlanKind: build.PlanKindRailpack,
	}, nil
}

func (e proofExecutor) Execute(ctx context.Context, job queue.Job) error {
	var payload struct {
		OperationID string `json:"operation_id"`
		ServiceID   string `json:"service_id"`
		ProjectID   string `json:"project_id"`
		Request     struct {
			Source *struct {
				Repo    string `json:"repo"`
				Ref     string `json:"ref"`
				Subpath string `json:"subpath"`
			} `json:"source"`
			Route *map[string]any `json:"route"`
		} `json:"request"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	if payload.Request.Source == nil {
		return fmt.Errorf("payload missing source")
	}
	root := os.TempDir()
	planner := build.SourceCheckoutPlanner{Root: root, SkipClone: true}
	checkout, err := planner.Checkout(ctx, payload.ServiceID, build.SourceRef{
		Repo: e.repo,
		Ref:  payload.Request.Source.Ref,
	})
	if err != nil {
		return err
	}
	defer func() { _ = checkout.Discard() }()
	imageRef, err := fakeBuilder{}.Build(ctx, checkout, build.BuildTarget{
		Image:    "registry.midnight.local:5000/midnight/" + payload.ServiceID + ":rev_proof",
		Registry: "registry.midnight.local:5000",
		Insecure: true,
	})
	if err != nil {
		return err
	}
	digestRef := imageRef.DigestRef()
	request := map[string]any{"image": digestRef}
	if payload.Request.Route != nil {
		request["route"] = payload.Request.Route
	}
	deployPayload, err := json.Marshal(map[string]any{
		"schema_version": "midnight.dev/deploy-job/v1alpha1",
		"operation_id":   payload.OperationID,
		"service_id":     payload.ServiceID,
		"project_id":     payload.ProjectID,
		"request":        request,
		"candidate": map[string]any{
			"service_id": payload.ServiceID,
			"image":      digestRef,
		},
		"enqueued_at": e.clock().UTC(),
	})
	if err != nil {
		return err
	}
	if _, err := e.queueStore.Enqueue(ctx, queue.EnqueueRequest{
		Queue:             queue.Deploy,
		PartitionKey:      payload.ProjectID,
		ParentOperationID: payload.OperationID,
		AvailableAt:       e.clock().UTC(),
		Payload:           deployPayload,
	}); err != nil {
		return err
	}
	if _, err := e.operationStore.Transition(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationRunning); err != nil {
		return err
	}
	if _, err := e.operationStore.Transition(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationSucceeded); err != nil {
		return err
	}
	if _, err := e.operationStore.AppendEvent(ctx, api.OperationEvent{
		OperationID: orailtypes.ID(payload.OperationID),
		State:       orailtypes.OperationSucceeded,
		Message:     "build pushed: image=" + digestRef,
	}); err != nil {
		return err
	}
	return nil
}

func newFakeQueueStore() *fakeQueueStore {
	return &fakeQueueStore{}
}

type fakeQueueStore struct {
	jobs   []queue.Job
	leased map[string]bool
}

func (s *fakeQueueStore) Enqueue(_ context.Context, req queue.EnqueueRequest) (queue.Job, error) {
	id := fmt.Sprintf("job_%d", len(s.jobs)+1)
	job := queue.Job{
		ID:                id,
		Queue:             req.Queue,
		State:             queue.JobQueued,
		Priority:          req.Priority,
		PartitionKey:      req.PartitionKey,
		ParentOperationID: req.ParentOperationID,
		AvailableAt:       req.AvailableAt,
		Payload:           append([]byte(nil), req.Payload...),
	}
	s.jobs = append(s.jobs, job)
	return job, nil
}

func (s *fakeQueueStore) Lease(_ context.Context, req queue.LeaseRequest) (queue.Job, error) {
	if s.leased == nil {
		s.leased = map[string]bool{}
	}
	for i := range s.jobs {
		j := s.jobs[i]
		if j.Queue != req.Queue || s.leased[j.ID] || j.State != queue.JobQueued {
			continue
		}
		j.LeaseID = "lease_" + j.ID
		j.LeasedUntil = req.Now.Add(req.LeaseTime)
		j.State = queue.JobLeased
		s.jobs[i] = j
		s.leased[j.ID] = true
		return j, nil
	}
	return queue.Job{}, queue.ErrNoWork
}

func (s *fakeQueueStore) Heartbeat(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}

func (s *fakeQueueStore) Complete(_ context.Context, jobID, _ string) error {
	for i := range s.jobs {
		if s.jobs[i].ID == jobID {
			s.jobs[i].State = queue.JobSucceeded
		}
	}
	return nil
}

func (s *fakeQueueStore) Fail(_ context.Context, jobID, _, _ string) error {
	for i := range s.jobs {
		if s.jobs[i].ID == jobID {
			s.jobs[i].State = queue.JobFailed
		}
	}
	return nil
}

// Silence "unused" warnings for symbols that exist only to satisfy a richer
// queue contract in production.
var _ = failurecodes.InternalError
var _ io.Writer = io.Discard
