//go:build integration

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	midnightnetwork "github.com/midnight/midnight/internal/network"
	"github.com/midnight/midnight/internal/runtime"
)

func TestRunOneOffAgainstDockerSurvivesAdapterRestartWithoutRerun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine, err := NewEngineClient("")
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err := engine.Ping(ctx); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	// Docker Desktop intentionally differs from Midnight's production daemon
	// baseline (live-restore/no-new-privileges). This integration test builds
	// the adapter internals directly so it exercises the real Engine one-off
	// protocol without weakening New's production admission gate.
	adapter := realOneOffTestAdapter(t, engine)
	const image = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
	adapter.SetSignatureVerifier(StaticSignatureVerifier{Signed: map[string]struct{}{image: {}}})
	token := time.Now().UTC().Format("20060102150405.000000000")
	sum := sha256.Sum256([]byte(token))
	spec := hex.EncodeToString(sum[:])
	projectID, environmentID := "prj-predeploy-"+token, "env-predeploy-"+token
	request := runtime.OneOffRequest{
		ScheduleRequest: runtime.ScheduleRequest{
			DeploymentID: "dep-predeploy-" + token,
			InstanceID:   "inst-predeploy-" + token,
			ProjectID:    projectID, EnvironmentID: environmentID, ServiceID: "svc-predeploy",
			Image: image, Env: []string{"PROOF=immutable"},
		},
		Command:    "test \"$PROOF\" = immutable",
		SpecSHA256: spec,
		Timeout:    30 * time.Second,
		Deadline:   time.Now().UTC().Add(30 * time.Second),
	}
	first, err := adapter.RunOneOff(ctx, request)
	if err != nil {
		t.Fatalf("first RunOneOff: %v", err)
	}
	if first.ExitCode != 0 || first.TimedOut {
		t.Fatalf("first result = %+v", first)
	}
	t.Cleanup(func() {
		_ = engine.ContainerRemove(context.Background(), first.ContainerID, true)
		networkName := (midnightnetwork.ProjectNetwork{
			ProjectID: projectID, EnvironmentID: environmentID,
		}).Name()
		networks, _ := engine.NetworkList(context.Background(), map[string]string{
			midnightnetwork.LabelManagedBy: midnightnetwork.LabelManagedByV,
		})
		for _, item := range networks {
			if item.Name == networkName {
				_ = engine.NetworkRemove(context.Background(), item.ID)
			}
		}
	})
	before, err := engine.ContainerInspect(ctx, first.ContainerID)
	if err != nil || before.State == nil {
		t.Fatalf("inspect first: %+v, %v", before.State, err)
	}

	// A fresh adapter models a worker restart after the command completed but
	// before the durable success write. It must inspect, not start, the
	// retained deterministic container.
	restarted := realOneOffTestAdapter(t, engine)
	restarted.SetSignatureVerifier(StaticSignatureVerifier{Signed: map[string]struct{}{image: {}}})
	second, err := restarted.RunOneOff(ctx, request)
	if err != nil {
		t.Fatalf("retry RunOneOff: %v", err)
	}
	after, err := engine.ContainerInspect(ctx, first.ContainerID)
	if err != nil || after.State == nil {
		t.Fatalf("inspect retry: %+v, %v", after.State, err)
	}
	if second.ContainerID != first.ContainerID || second.ExitCode != 0 ||
		after.State.StartedAt != before.State.StartedAt ||
		after.State.FinishedAt != before.State.FinishedAt {
		t.Fatalf("one-off reran across adapter restart: first=%+v second=%+v before=%+v after=%+v",
			first, second, before.State, after.State)
	}
}

func realOneOffTestAdapter(t *testing.T, engine EngineClient) *Adapter {
	t.Helper()
	provisioner, err := midnightnetwork.NewDockerProvisioner(engineNetworkClient{client: engine})
	if err != nil {
		t.Fatalf("network provisioner: %v", err)
	}
	return &Adapter{
		cfg: Config{
			AgentID: "agent-predeploy-test", HostID: "host-predeploy-test", Engine: engine,
		},
		client: engine, provisioner: provisioner,
		records: make(map[string]*record), networks: make(map[string]runtime.NetworkPolicy),
		now: func() time.Time { return time.Now().UTC() },
	}
}
