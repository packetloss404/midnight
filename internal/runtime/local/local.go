// Package local implements an in-memory RuntimeAdapter that records intents
// without touching Docker, the host network, or any other external resource.
// It exists so the Phase 1 reconciler/scheduler/agent can be exercised
// end-to-end with no live runtime side effects.
package local

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/runtime"
)

// IntentKind enumerates the recorded RuntimeAdapter operations the local
// adapter captures for assertion.
type IntentKind string

const (
	IntentSchedule       IntentKind = "schedule"
	IntentStart          IntentKind = "start"
	IntentStop           IntentKind = "stop"
	IntentRemove         IntentKind = "remove"
	IntentDrain          IntentKind = "drain"
	IntentReconcile      IntentKind = "reconcile"
	IntentNetworkPolicy  IntentKind = "network_policy"
	IntentAttachVolume   IntentKind = "attach_volume"
	IntentDetachVolume   IntentKind = "detach_volume"
	IntentPullVerify     IntentKind = "pull_verify"
	IntentResolveRouting IntentKind = "resolve_routing"
)

// Intent captures one recorded RuntimeAdapter call.
type Intent struct {
	Kind         IntentKind
	DeploymentID string
	InstanceID   string
	Payload      string
	At           time.Time
}

// Adapter is the local in-memory RuntimeAdapter. It is safe for concurrent
// use and intentionally records only deterministic, redaction-safe metadata.
type Adapter struct {
	mu        sync.Mutex
	now       func() time.Time
	intents   []Intent
	instances map[string]runtime.InstanceStatus
	requests  map[string]runtime.ScheduleRequest
	routing   map[string]runtime.RoutingTarget
	networks  map[string]runtime.NetworkPolicy
	volumes   map[string]runtime.VolumeAttachment
}

// New returns a fresh local adapter.
func New() *Adapter {
	return NewWithClock(func() time.Time { return time.Now().UTC() })
}

// NewWithClock returns a local adapter that timestamps intents using clock.
func NewWithClock(clock func() time.Time) *Adapter {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Adapter{
		now:       clock,
		instances: make(map[string]runtime.InstanceStatus),
		requests:  make(map[string]runtime.ScheduleRequest),
		routing:   make(map[string]runtime.RoutingTarget),
		networks:  make(map[string]runtime.NetworkPolicy),
		volumes:   make(map[string]runtime.VolumeAttachment),
	}
}

// Capabilities returns the local adapter's advertised feature set.
func (a *Adapter) Capabilities() runtime.AdapterCapabilities {
	return runtime.AdapterCapabilities{
		Name:                "local",
		Version:             "phase1",
		Major:               runtime.AdapterMajorVersion,
		IdempotentSchedule:  true,
		NetworkPolicy:       true,
		SignatureEnforce:    false,
		ConformanceVerified: false,
	}
}

// Schedule records a Schedule intent and reports the instance as ALLOCATED.
func (a *Adapter) Schedule(ctx context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	if err := runtime.ValidateSingleReplica(req.Replicas); err != nil {
		return runtime.InstanceStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return runtime.InstanceStatus{}, err
	}
	if req.DeploymentID == "" || req.InstanceID == "" {
		return runtime.InstanceStatus{}, fmt.Errorf("schedule requires deployment and instance ids")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := instanceKey(req.DeploymentID, req.InstanceID)
	a.record(IntentSchedule, req.DeploymentID, req.InstanceID, req.IdempotencyKey)
	if existing, ok := a.instances[key]; ok && req.IdempotencyKey != "" && existing.Reason == req.IdempotencyKey {
		return existing, nil
	}
	status := runtime.InstanceStatus{
		DeploymentID: req.DeploymentID,
		InstanceID:   req.InstanceID,
		State:        runtime.InstanceStateAllocated,
		ObservedAt:   a.now(),
		Reason:       req.IdempotencyKey,
	}
	a.instances[key] = status
	a.requests[key] = req
	return status, nil
}

// Start transitions a recorded instance to RUNNING.
func (a *Adapter) Start(ctx context.Context, deploymentID, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentStart, deploymentID, instanceID, "")
	key := instanceKey(deploymentID, instanceID)
	status, ok := a.instances[key]
	if !ok {
		return fmt.Errorf("local adapter: unknown instance %s", key)
	}
	status.State = runtime.InstanceStateRunning
	status.ObservedAt = a.now()
	a.instances[key] = status
	return nil
}

// Stop transitions a recorded instance to STOPPED.
func (a *Adapter) Stop(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentStop, deploymentID, instanceID, grace.String())
	key := instanceKey(deploymentID, instanceID)
	if status, ok := a.instances[key]; ok {
		status.State = runtime.InstanceStateStopped
		status.ObservedAt = a.now()
		a.instances[key] = status
	}
	return nil
}

// Remove erases adapter state for an instance.
func (a *Adapter) Remove(ctx context.Context, deploymentID, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentRemove, deploymentID, instanceID, "")
	key := instanceKey(deploymentID, instanceID)
	delete(a.instances, key)
	delete(a.requests, key)
	delete(a.routing, key)
	for k, v := range a.volumes {
		if v.DeploymentID == deploymentID && v.InstanceID == instanceID {
			delete(a.volumes, k)
		}
	}
	return nil
}

// Drain marks the instance as draining and removes it from routing.
func (a *Adapter) Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentDrain, deploymentID, instanceID, grace.String())
	key := instanceKey(deploymentID, instanceID)
	if status, ok := a.instances[key]; ok {
		status.State = runtime.InstanceStateDraining
		status.ObservedAt = a.now()
		a.instances[key] = status
	}
	delete(a.routing, key)
	return nil
}

// StreamInstanceStatus returns a deterministic snapshot of recorded statuses.
func (a *Adapter) StreamInstanceStatus(ctx context.Context) ([]runtime.InstanceStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]string, 0, len(a.instances))
	for k := range a.instances {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]runtime.InstanceStatus, 0, len(keys))
	for _, k := range keys {
		out = append(out, a.instances[k])
	}
	return out, nil
}

// Reconcile records that a sweep happened and is otherwise a no-op.
func (a *Adapter) Reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentReconcile, "", "", "")
	return nil
}

// ApplyNetworkPolicy stores the policy, refusing tenant-scope conflicts on
// the same bridge name.
func (a *Adapter) ApplyNetworkPolicy(ctx context.Context, policy runtime.NetworkPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if policy.BridgeName == "" {
		return fmt.Errorf("local adapter: network policy bridge name is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentNetworkPolicy, "", "", policy.BridgeName)
	if existing, ok := a.networks[policy.BridgeName]; ok {
		if existing.ProjectID != policy.ProjectID || existing.EnvironmentID != policy.EnvironmentID {
			return fmt.Errorf("local adapter: bridge %q already bound to (%s,%s)", policy.BridgeName, existing.ProjectID, existing.EnvironmentID)
		}
	}
	a.networks[policy.BridgeName] = policy
	return nil
}

// ResolveRoutingTarget returns a deterministic synthetic routing target.
func (a *Adapter) ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	if err := ctx.Err(); err != nil {
		return runtime.RoutingTarget{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentResolveRouting, deploymentID, instanceID, "")
	key := instanceKey(deploymentID, instanceID)
	if target, ok := a.routing[key]; ok {
		return target, nil
	}
	status, ok := a.instances[key]
	if !ok {
		return runtime.RoutingTarget{}, fmt.Errorf("local adapter: no instance %s", key)
	}
	req := a.requests[key]
	target := runtime.RoutingTarget{
		DeploymentID:    deploymentID,
		InstanceID:      instanceID,
		HostID:          req.Labels["midnight.host"],
		RoutingTargetID: key,
		ContainerID:     "local-" + instanceID,
		ContainerIP:     "127.0.0.1",
		InternalPort:    0,
		ProjectID:       req.ProjectID,
		EnvironmentID:   req.EnvironmentID,
		Ready:           status.State == runtime.InstanceStateReady || status.State == runtime.InstanceStateRunning,
		ObservedAt:      a.now(),
	}
	a.routing[key] = target
	return target, nil
}

// AttachVolume records a volume attachment.
func (a *Adapter) AttachVolume(ctx context.Context, attach runtime.VolumeAttachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attach.VolumeName == "" {
		return fmt.Errorf("local adapter: volume name is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentAttachVolume, attach.DeploymentID, attach.InstanceID, attach.VolumeName)
	a.volumes[volumeKey(attach.DeploymentID, attach.InstanceID, attach.VolumeName)] = attach
	return nil
}

// DetachVolume releases a recorded volume attachment.
func (a *Adapter) DetachVolume(ctx context.Context, deploymentID, instanceID, volumeName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentDetachVolume, deploymentID, instanceID, volumeName)
	delete(a.volumes, volumeKey(deploymentID, instanceID, volumeName))
	return nil
}

// PullVerify always reports unverified for the local adapter; default
// profile callers MUST refuse to pull when verification is unavailable.
func (a *Adapter) PullVerify(ctx context.Context, image string) (runtime.PullVerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.PullVerifyResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(IntentPullVerify, "", "", image)
	return runtime.PullVerifyResult{
		Image:    image,
		Verified: false,
		Trusted:  false,
		Reason:   "local adapter does not verify signatures",
	}, nil
}

// MarkReady is a test/diagnostic hook that flips a recorded instance into
// the READY state without sending it through the adapter's RPC surface.
func (a *Adapter) MarkReady(deploymentID, instanceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := instanceKey(deploymentID, instanceID)
	if status, ok := a.instances[key]; ok {
		status.State = runtime.InstanceStateReady
		status.ObservedAt = a.now()
		a.instances[key] = status
	}
}

// Intents returns a copy of all recorded intents in insertion order.
func (a *Adapter) Intents() []Intent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Intent, len(a.intents))
	copy(out, a.intents)
	return out
}

// IntentCount returns the number of intents recorded for kind.
func (a *Adapter) IntentCount(kind IntentKind) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, intent := range a.intents {
		if intent.Kind == kind {
			count++
		}
	}
	return count
}

func (a *Adapter) record(kind IntentKind, deploymentID, instanceID, payload string) {
	a.intents = append(a.intents, Intent{
		Kind:         kind,
		DeploymentID: deploymentID,
		InstanceID:   instanceID,
		Payload:      payload,
		At:           a.now(),
	})
}

func instanceKey(deploymentID, instanceID string) string {
	return deploymentID + "/" + instanceID
}

func volumeKey(deploymentID, instanceID, volumeName string) string {
	return deploymentID + "/" + instanceID + "/" + volumeName
}
