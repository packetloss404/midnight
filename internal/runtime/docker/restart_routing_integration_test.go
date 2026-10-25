//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/deploy/readiness"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/pkg/orailtypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// The Postgres route-refresh integration test separately proves the durable
// active/promotion fences. Here the real Docker adapter must survive a changed
// address, prove HTTP readiness, and restore actual HTTP traffic before success.
func TestRuntimeActionRestartAgainstDockerRestoresHTTPAfterAddressChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine, err := NewEngineClient("")
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	defer func() { _ = engine.Close() }()
	if err := engine.Ping(ctx); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	const image = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	pull, err := engine.ImagePull(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(io.Discard, pull)
	_ = pull.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	var containers []string
	t.Cleanup(func() {
		for _, id := range containers {
			_ = engine.ContainerRemove(context.Background(), id, true)
		}
	})
	create := func(command string) string {
		t.Helper()
		port, _ := network.ParsePort("8080/tcp")
		id, err := engine.ContainerCreate(ctx, "", &container.Config{Image: image, Cmd: []string{"sh", "-c", command}, ExposedPorts: network.PortSet{port: {}}}, &container.HostConfig{NetworkMode: "bridge"})
		if err != nil {
			t.Fatal(err)
		}
		containers = append(containers, id)
		if err := engine.ContainerStart(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := create("mkdir -p /www; printf restart-http-proof > /www/index.html; exec httpd -f -p 8080 -h /www")
	adapter := realOneOffTestAdapter(t, engine)
	adapter.records[instanceKey("dep-restart", "dep-restart-r0")] = &record{
		deploymentID: "dep-restart", instanceID: "dep-restart-r0", containerID: id,
		scheduleReq: runtime.ScheduleRequest{ProjectID: "prj-restart", EnvironmentID: "env-restart", TargetPort: 8080},
	}
	before, err := adapter.ResolveRoutingTarget(ctx, "dep-restart", "dep-restart-r0")
	if err != nil {
		t.Fatal(err)
	}
	if before.ContainerIP == "" {
		inspected, _ := engine.ContainerInspect(ctx, id)
		t.Fatalf("test container has no address: state=%+v networks=%+v", inspected.State, inspected.NetworkSettings)
	}
	oldURL := fmt.Sprintf("http://%s:8080", before.ContainerIP)
	if err := readiness.Wait(ctx, readiness.HTTPProbe{URL: oldURL}, readiness.Options{Timeout: 20 * time.Second, Interval: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	var routeMu sync.RWMutex
	routeURL := oldURL
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeMu.RLock()
		raw := routeURL
		routeMu.RUnlock()
		target, _ := url.Parse(raw)
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	defer proxy.Close()
	assertHTTP := func() error {
		response, err := (&http.Client{Timeout: 2 * time.Second}).Get(proxy.URL)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK || string(body) != "restart-http-proof" {
			return fmt.Errorf("HTTP proof status=%d body=%q", response.StatusCode, body)
		}
		return nil
	}
	if err := assertHTTP(); err != nil {
		t.Fatal(err)
	}
	rt := &restartHTTPRuntime{Adapter: adapter, afterStop: func() { create("exec sleep 120") }}
	instances := &restartHTTPInstances{}
	action := deploy.RuntimeActionRecord{ID: "act-restart", OperationID: "op-restart", DeploymentID: "dep-restart", InstanceID: "dep-restart-r0", ProjectID: "prj-restart", EnvironmentID: "env-restart", ServiceID: "svc-restart", Image: image, Action: deploy.RuntimeActionRestart, DeploymentActive: true}
	store := &restartHTTPStore{record: action}
	routes := &restartHTTPRoutes{restore: func() error {
		fresh := instances.target
		if fresh.ContainerIP == "" || fresh.ContainerIP == before.ContainerIP {
			return fmt.Errorf("test did not force an address change: %q -> %q", before.ContainerIP, fresh.ContainerIP)
		}
		if store.completed {
			return fmt.Errorf("restart completed before route publication")
		}
		// The old routing address must now fail before fresh publication.
		if err := assertHTTP(); err == nil {
			return fmt.Errorf("stale route unexpectedly still serves the application")
		}
		routeMu.Lock()
		routeURL = fmt.Sprintf("http://%s:%d", fresh.ContainerIP, fresh.InternalPort)
		routeMu.Unlock()
		return assertHTTP()
	}}
	cfg := serviceconfig.Default(action.ProjectID, action.ServiceID, action.EnvironmentID)
	cfg.HealthKind, cfg.HealthPath = serviceconfig.HealthHTTP, "/"
	payload, _ := json.Marshal(deploy.DeploymentActionJobPayload{SchemaVersion: deploy.DeploymentActionJobPayloadSchema, ActionID: action.ID, OperationID: action.OperationID, DeploymentID: action.DeploymentID, Action: action.Action})
	out, err := (deploy.ActionExecutor{Store: store, Runtime: rt, Instances: instances, Routes: routes, Configuration: restartHTTPConfig{cfg}, Operations: restartHTTPOperations{}, StopGrace: time.Second, Readiness: deploy.ReadinessConfig{Timeout: 20 * time.Second, Interval: 50 * time.Millisecond}}).Execute(ctx, queue.Job{ID: "job-restart", Payload: payload})
	if err != nil || !out.Succeeded || !store.completed {
		t.Fatalf("restart outcome=%#v err=%v completed=%t", out, err, store.completed)
	}
	if err := assertHTTP(); err != nil {
		t.Fatal(err)
	}
}

type restartHTTPRuntime struct {
	*Adapter
	afterStop func()
}

func (r *restartHTTPRuntime) Stop(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if err := r.Adapter.Stop(ctx, deploymentID, instanceID, grace); err != nil {
		return err
	}
	r.afterStop()
	return nil
}

type restartHTTPInstances struct{ target runtime.RoutingTarget }

func (*restartHTTPInstances) SetDeploymentState(context.Context, string, string, string) error {
	return nil
}
func (s *restartHTTPInstances) Observe(_ context.Context, target runtime.RoutingTarget) error {
	s.target = target
	return nil
}

type restartHTTPRoutes struct{ restore func() error }

func (*restartHTTPRoutes) WithdrawDeployment(context.Context, deploy.RuntimeActionRecord) error {
	return nil
}
func (r *restartHTTPRoutes) RestoreDeployment(context.Context, deploy.RuntimeActionRecord) error {
	return r.restore()
}

type restartHTTPConfig struct{ cfg serviceconfig.Config }

func (c restartHTTPConfig) ResolveDeployment(context.Context, string, string, string, string) (serviceconfig.Snapshot, error) {
	return serviceconfig.Snapshot{Config: c.cfg}, nil
}

type restartHTTPStore struct {
	record    deploy.RuntimeActionRecord
	completed bool
}

func (s *restartHTTPStore) BeginRuntimeAction(context.Context, string, string) (deploy.RuntimeActionRecord, error) {
	return s.record, nil
}
func (s *restartHTTPStore) RecordRuntimeActionStep(_ context.Context, _ string, step string) error {
	s.record.Step = step
	return nil
}
func (s *restartHTTPStore) CompleteRuntimeAction(context.Context, string, string) error {
	s.completed = true
	return nil
}
func (*restartHTTPStore) FailRuntimeAction(context.Context, string, failurecodes.Code, string) error {
	return nil
}

type restartHTTPOperations struct{}

func (restartHTTPOperations) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	return orailtypes.Operation{ID: id, State: state}, nil
}
func (restartHTTPOperations) Fail(context.Context, orailtypes.ID, orailtypes.FailureCode, string) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}
