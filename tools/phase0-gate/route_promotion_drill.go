package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func validateReleaseReadinessAndDeployRoutePromotion(manifestData, alertData []byte) error {
	return errors.Join(
		validateReleaseReadinessReport(manifestData, alertData),
		validateDeployRoutePromotionDrill(),
	)
}

func validateDeployRoutePromotionDrill() error {
	const (
		operationID  = "op-route-promotion-drill"
		deploymentID = "dep-route-promotion-drill"
		instanceID   = deploymentID + "-r0"
		image        = "registry.example.test/drill-web@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	)
	now := drillCreatedAt()
	payload, err := json.Marshal(deploy.DeployJobPayload{
		SchemaVersion: deploy.DeployJobPayloadSchema,
		OperationID:   operationID,
		ServiceID:     "web",
		ProjectID:     "prj-drill",
		EnvironmentID: "prod",
		Request:       deploy.DeployJobRequest{Image: image},
		Candidate: deploy.DeployJobCandidate{
			DeploymentID:   deploymentID,
			InstanceID:     instanceID,
			IdempotencyKey: operationID,
		},
		Route: &deploy.DeployJobRoute{
			Name:   "web",
			Group:  "apps-prod",
			Host:   "web.example.test",
			Path:   "/",
			TLS:    true,
			Public: true,
		},
		PreviousActive: deploy.ActiveInstanceRef{DeploymentID: "dep-previous", InstanceID: "inst-previous"},
		EnqueuedAt:     now,
	})
	if err != nil {
		return fmt.Errorf("marshal deploy route payload: %w", err)
	}

	store := &routePromotionDrillQueue{}
	job, err := store.Enqueue(context.Background(), queue.EnqueueRequest{
		Queue:             queue.Deploy,
		PartitionKey:      "prj-drill",
		ParentOperationID: operationID,
		AvailableAt:       now,
		Payload:           payload,
	})
	if err != nil {
		return err
	}
	if job.Queue != queue.Deploy || job.ParentOperationID != operationID || len(job.Payload) == 0 {
		return fmt.Errorf("queued deploy job = %+v, want deploy queue with parent operation and payload", job)
	}
	decoded, _, err := deploy.DecodeDeployJobPayload(job.Payload)
	if err != nil {
		return err
	}
	if decoded.Route == nil || decoded.Route.Group != "apps-prod" || decoded.Route.Host != "web.example.test" || !decoded.Route.TLS || !decoded.Route.Public {
		return fmt.Errorf("deploy payload route intent = %+v, want apps-prod/web.example.test TLS route", decoded.Route)
	}

	calls := []string{}
	runtimeFake := &routePromotionDrillRuntime{
		calls: &calls,
		pull:  runtime.PullVerifyResult{Image: image, Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111", Verified: true, Trusted: true},
		target: runtime.RoutingTarget{
			DeploymentID:    deploymentID,
			InstanceID:      instanceID,
			HostID:          "host-drill",
			RoutingTargetID: "rt-drill",
			ContainerID:     "ctr-drill",
			ContainerIP:     "10.77.0.42",
			InternalPort:    8080,
			ProjectID:       "prj-drill",
			EnvironmentID:   "prod",
			Ready:           true,
			ObservedAt:      now.Add(30 * time.Second),
		},
	}
	publisher := &routePromotionDrillPublisher{}
	operations := &routePromotionDrillOperations{}
	observer := &routePromotionDrillStepObserver{}
	executor := deploy.Executor{
		Runtime: runtimeFake,
		Promoter: deploy.TraefikRoutePromoter{
			Publisher: publisher,
			Metadata:  deploy.RoutePromotionMetadata{CertResolver: "le-staging"},
		},
		Operations: operations,
		Observer:   observer,
		DrainGrace: 10 * time.Second,
	}

	out, err := executor.Execute(context.Background(), job)
	if err != nil {
		return err
	}
	if !out.Succeeded || out.OperationID != operationID {
		return fmt.Errorf("executor outcome = %+v, want succeeded operation %s", out, operationID)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
	if !reflect.DeepEqual(runtimeFake.callsWithoutPromotion(), wantCalls) {
		return fmt.Errorf("runtime calls = %#v, want %#v", runtimeFake.callsWithoutPromotion(), wantCalls)
	}
	if !reflect.DeepEqual(operations.states, []orailtypes.OperationState{orailtypes.OperationRunning, orailtypes.OperationSucceeded}) {
		return fmt.Errorf("operation events = %#v, want RUNNING then SUCCEEDED", operations.states)
	}
	wantStepTelemetry := []string{
		"pull-verify:started",
		"pull-verify:succeeded",
		"schedule:started",
		"schedule:succeeded",
		"start:started",
		"start:succeeded",
		"readiness-routing-check:started",
		"readiness-routing-check:succeeded",
		"promote:started",
		"promote:succeeded",
		"drain-previous:started",
		"drain-previous:succeeded",
		"succeeded:succeeded",
	}
	if !reflect.DeepEqual(observer.keys(), wantStepTelemetry) {
		return fmt.Errorf("deploy step telemetry = %#v, want %#v", observer.keys(), wantStepTelemetry)
	}
	if publisher.evidence.Status != router.RouteArtifactPublicationApplyStatusReady || publisher.evidence.Writes != 1 || publisher.evidence.BytesWritten == 0 {
		return fmt.Errorf("route publication evidence = %+v, want one ready write", publisher.evidence)
	}
	if publisher.routeGroup != "apps-prod" || len(publisher.routes) != 1 {
		return fmt.Errorf("published route group/routes = %q/%d, want apps-prod/1", publisher.routeGroup, len(publisher.routes))
	}
	route := publisher.routes[0]
	if route.Host != "web.example.test" || route.PathPrefix != "/" || !route.TLS || route.CertResolver != "le-staging" || route.ServiceURL != "http://10.77.0.42:8080" {
		return fmt.Errorf("published route = %+v, want route intent bound to fake runtime target", route)
	}
	if publisher.summary.Writes != 1 || publisher.summary.Errors != 0 || publisher.summary.TotalBytes == 0 {
		return fmt.Errorf("route publication summary = %+v, want one support-safe write summary", publisher.summary)
	}
	return nil
}

type routePromotionDrillQueue struct {
	nextID int
}

func (s *routePromotionDrillQueue) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	if request.Queue != queue.Deploy {
		return queue.Job{}, fmt.Errorf("enqueue queue = %q, want deploy", request.Queue)
	}
	if request.ParentOperationID == "" {
		return queue.Job{}, errors.New("enqueue parent operation id is required")
	}
	s.nextID++
	return queue.Job{
		ID:                fmt.Sprintf("job-route-promotion-%d", s.nextID),
		Queue:             request.Queue,
		State:             queue.JobQueued,
		Priority:          request.Priority,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		LeaseID:           "worker-route-promotion-drill",
		Payload:           append([]byte(nil), request.Payload...),
	}, nil
}

func (s *routePromotionDrillQueue) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	return queue.Job{}, queue.ErrNoWork
}

func (s *routePromotionDrillQueue) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *routePromotionDrillQueue) Complete(context.Context, string, string) error {
	return nil
}

func (s *routePromotionDrillQueue) Fail(context.Context, string, string, string) error {
	return nil
}

type routePromotionDrillRuntime struct {
	calls            *[]string
	pull             runtime.PullVerifyResult
	target           runtime.RoutingTarget
	pullImages       []string
	scheduleRequests []runtime.ScheduleRequest
	drainCalls       []string
}

func (r *routePromotionDrillRuntime) PullVerify(_ context.Context, image string) (runtime.PullVerifyResult, error) {
	r.record("pull-verify")
	r.pullImages = append(r.pullImages, image)
	return r.pull, nil
}

func (r *routePromotionDrillRuntime) Schedule(_ context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	r.record("schedule")
	r.scheduleRequests = append(r.scheduleRequests, req)
	return runtime.InstanceStatus{DeploymentID: req.DeploymentID, InstanceID: req.InstanceID, State: runtime.InstanceStateAllocated, ObservedAt: drillCreatedAt()}, nil
}

func (r *routePromotionDrillRuntime) Start(_ context.Context, deploymentID, instanceID string) error {
	r.record("start")
	if deploymentID == "" || instanceID == "" {
		return errors.New("start requires deployment and instance")
	}
	return nil
}

func (r *routePromotionDrillRuntime) ResolveRoutingTarget(_ context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	r.record("resolve-routing")
	if deploymentID != r.target.DeploymentID || instanceID != r.target.InstanceID {
		return runtime.RoutingTarget{}, fmt.Errorf("resolve target for %s/%s, want %s/%s", deploymentID, instanceID, r.target.DeploymentID, r.target.InstanceID)
	}
	return r.target, nil
}

func (r *routePromotionDrillRuntime) Drain(_ context.Context, deploymentID, instanceID string, grace time.Duration) error {
	r.record("drain")
	r.drainCalls = append(r.drainCalls, fmt.Sprintf("%s/%s/%s", deploymentID, instanceID, grace))
	return nil
}

func (r *routePromotionDrillRuntime) record(call string) {
	*r.calls = append(*r.calls, call)
}

func (r *routePromotionDrillRuntime) callsWithoutPromotion() []string {
	return append([]string(nil), *r.calls...)
}

type routePromotionDrillPublisher struct {
	routeGroup   string
	routes       []router.RouteInput
	existingHash string
	summary      router.RoutePublicationSummary
	evidence     router.RouteArtifactPublicationApplyEvidence
}

func (p *routePromotionDrillPublisher) PublishRouteGroup(routeGroup string, routes []router.RouteInput, existingHash string) (router.RouteArtifactPublicationApplyEvidence, error) {
	p.routeGroup = routeGroup
	p.routes = append([]router.RouteInput(nil), routes...)
	p.existingHash = existingHash
	file, err := router.RenderTraefikFile(routes)
	if err != nil {
		return router.RouteArtifactPublicationApplyEvidence{}, err
	}
	rendered, err := router.RenderTraefikDynamicYAML(routes)
	if err != nil {
		return router.RouteArtifactPublicationApplyEvidence{}, err
	}
	prewrite := router.PlanRouteArtifactPrewrite([]router.RouteArtifactPrewriteInput{{
		RouteGroup:   routeGroup,
		Format:       router.TraefikDynamicArtifactExtension,
		File:         file,
		Rendered:     rendered,
		ExistingHash: existingHash,
	}})
	p.summary = router.SummarizeRoutePublication(prewrite)
	evidence, err := router.ApplyRouteArtifactPublication(prewrite, &routePromotionDrillWriter{})
	p.evidence = evidence
	return evidence, err
}

type routePromotionDrillWriter struct{}

func (routePromotionDrillWriter) WriteRouteArtifact(write router.RouteArtifactPublicationWrite) (int, error) {
	if write.RouteGroup == "" || write.Format != router.TraefikDynamicArtifactExtension || len(write.Content) == 0 {
		return 0, fmt.Errorf("invalid route artifact write: %+v", write)
	}
	return len(write.Content), nil
}

type routePromotionDrillOperations struct {
	states []orailtypes.OperationState
}

func (o *routePromotionDrillOperations) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	if id == "" {
		return orailtypes.Operation{}, errors.New("operation id is required")
	}
	o.states = append(o.states, state)
	return orailtypes.Operation{ID: id, Kind: "deploy", State: state}, nil
}

func (o *routePromotionDrillOperations) Fail(_ context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	if id == "" || code == "" || reason == "" {
		return orailtypes.Operation{}, errors.New("failed operation requires id, code, and reason")
	}
	o.states = append(o.states, orailtypes.OperationFailed)
	return orailtypes.Operation{ID: id, Kind: "deploy", State: orailtypes.OperationFailed, Code: code, Reason: reason}, nil
}

type routePromotionDrillStepObserver struct {
	events []deploy.DeployStepEvent
}

func (o *routePromotionDrillStepObserver) ObserveDeployStep(_ context.Context, event deploy.DeployStepEvent) {
	o.events = append(o.events, event)
}

func (o *routePromotionDrillStepObserver) keys() []string {
	keys := make([]string, 0, len(o.events))
	for _, event := range o.events {
		keys = append(keys, string(event.Step)+":"+string(event.State))
	}
	return keys
}
