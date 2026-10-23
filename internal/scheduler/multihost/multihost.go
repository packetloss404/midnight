// Package multihost is the stub for the future MultiHostScheduler.
//
// The MVP runs single-host, so MultiHostScheduler ships as a stub today. The
// scope of this package is to lock in the wire shape between the control
// plane and remote midnight-agent binaries so the day we flip
// MIDNIGHT_SCHEDULER=multihost we are filling in implementation, not
// reshaping interfaces.
//
// What is here:
//   - HostRepository / DesiredStateWriter interfaces that the existing
//     hosts + host_heartbeats and /api/v1alpha/agents/{id}/desired plumbing
//     will satisfy.
//   - A first-fit placement function with the documented contract: pick the
//     first healthy host that advertises {"runtime": "docker"} in its
//     hosts.labels JSON column. Health is sourced from host_heartbeats.
//   - Schedule(): selects a host and writes desired-state for the agent.
//   - Reconcile() and Drain(): return scheduler.ErrNotImplemented with a
//     clear TODO referencing the /api/v1alpha/agents/{id}/status callback
//     path. No silent fallback to the local scheduler.
//
// See docs/adr/0018-scheduler-interface.md for the full migration story and
// the capability-advertising model.
package multihost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
)

// DefaultRequiredCapability is the runtime label MultiHostScheduler requires
// in hosts.labels when no explicit RequiredCapabilities are supplied on the
// intent. It matches the only adapter shipping today (docker) and mirrors
// the "stacker capability advertising" pattern documented in ADR-0018.
const (
	DefaultRequiredCapabilityKey   = "runtime"
	DefaultRequiredCapabilityValue = "docker"
)

// HealthyHeartbeatWindow is the maximum age of a host_heartbeats.observed_at
// row for a host to be considered eligible for placement. Matches the agent
// API heartbeat interval (5 seconds) multiplied by the 6x miss budget the
// host-heartbeat ADR locks in.
const HealthyHeartbeatWindow = 30 * time.Second

// HostRecord mirrors one row from the hosts + host_heartbeats join. It is
// runtime-neutral so the multi-host scheduler can be tested without
// pulling in pgx.
type HostRecord struct {
	ID                string
	Hostname          string
	Labels            map[string]string
	Status            string
	HeartbeatAt       time.Time
	HeartbeatDetails  map[string]any
	AdvertisedVersion string
}

// HostRepository is the read side of the multihost scheduler. The production
// implementation queries the hosts + host_heartbeats tables (see
// migrations/0001_alpha_core.up.sql §3). Tests inject a fixture.
type HostRepository interface {
	// HealthyHosts returns hosts with a heartbeat fresher than maxAge and a
	// non-failing status. Implementations MUST return hosts in a
	// deterministic order; the scheduler relies on stable iteration for
	// idempotency.
	HealthyHosts(ctx context.Context, maxAge time.Duration) ([]HostRecord, error)
}

// DesiredStateWriter is the write side of the multihost scheduler. The
// production implementation writes the agent_desired_state row that the
// existing GET /api/v1alpha/agents/{id}/desired endpoint serves. The
// scheduler does NOT reach into the agent endpoint directly; it writes
// through the same persistence the endpoint reads from, so the wire
// contract is preserved.
type DesiredStateWriter interface {
	WriteDesiredState(ctx context.Context, agentID string, etag string, payload []byte) error
}

// AgentStatusReader is the (future) callback path the multi-host scheduler
// will use to translate agent status reports back into ReconcileResult /
// DeploymentState. It is defined here so the interface is locked even
// though Reconcile/Drain are stubs for now.
type AgentStatusReader interface {
	LastStatusReport(ctx context.Context, agentID string) (AgentInstanceStatus, error)
}

// AgentInstanceStatus is the subset of pkg/agentapi.StatusReport the
// scheduler needs. Defined here (rather than importing pkg/agentapi) to
// keep the scheduler package free of HTTP-layer dependencies.
type AgentInstanceStatus struct {
	DeploymentID  string
	InstanceID    string
	State         runtime.InstanceState
	RoutingTarget runtime.RoutingTarget
	ObservedAt    time.Time
}

// MultiHostScheduler picks hosts from the registered fleet and dispatches
// work via the existing /api/v1alpha/agents/{id}/desired endpoint. It is a
// stub today: Schedule selects a host and writes desired state, but
// Reconcile and Drain return ErrNotImplemented. The interface contract is
// locked so the implementation can be filled in without re-plumbing.
type MultiHostScheduler struct {
	Hosts    HostRepository
	Desired  DesiredStateWriter
	Statuses AgentStatusReader
	// HeartbeatWindow overrides the staleness threshold for healthy hosts.
	// Defaults to HealthyHeartbeatWindow.
	HeartbeatWindow time.Duration
	// Now is the clock seam for tests. nil falls back to time.Now().UTC().
	Now func() time.Time
}

var (
	// Compile-time confirmation that MultiHostScheduler satisfies the
	// scheduler.Scheduler boundary.
	_ scheduler.Scheduler = (*MultiHostScheduler)(nil)

	// ErrNoEligibleHost is returned by Schedule when no healthy host
	// advertises the required capabilities.
	ErrNoEligibleHost = errors.New("multihost scheduler: no eligible host for required capabilities")

	// ErrHostRepositoryRequired is returned when the scheduler is invoked
	// without a HostRepository wired in.
	ErrHostRepositoryRequired = errors.New("multihost scheduler: host repository is required")

	// ErrDesiredStateWriterRequired is returned when the scheduler is invoked
	// without a DesiredStateWriter wired in.
	ErrDesiredStateWriterRequired = errors.New("multihost scheduler: desired state writer is required")
)

// New constructs a MultiHostScheduler. The returned scheduler is not yet
// fully implemented; Reconcile and Drain return scheduler.ErrNotImplemented
// until the agent status callback path lands. See TODOs below.
func New(hosts HostRepository, desired DesiredStateWriter) *MultiHostScheduler {
	return &MultiHostScheduler{Hosts: hosts, Desired: desired}
}

// Schedule picks the first healthy host matching the intent's required
// capabilities and writes a desired-state payload that the agent will pull
// from GET /api/v1alpha/agents/{id}/desired.
//
// Placement strategy (documented contract):
//
//  1. Filter hosts.labels for every key/value in intent.RequiredCapabilities;
//     the default contract is {"runtime": "docker"}.
//  2. Drop hosts whose host_heartbeats.observed_at is older than
//     HealthyHeartbeatWindow (defaults to 30s).
//  3. Sort by host id (deterministic) and pick the first match.
//
// This is intentionally simpler than the future GPU/region/tier matcher; the
// ADR captures the upgrade path.
func (m *MultiHostScheduler) Schedule(ctx context.Context, intent scheduler.ScheduleIntent) (scheduler.DeploymentPlacement, error) {
	if m == nil {
		return scheduler.DeploymentPlacement{}, fmt.Errorf("multihost scheduler: receiver is nil")
	}
	if m.Hosts == nil {
		return scheduler.DeploymentPlacement{}, ErrHostRepositoryRequired
	}
	if m.Desired == nil {
		return scheduler.DeploymentPlacement{}, ErrDesiredStateWriterRequired
	}
	required := normalizeCapabilities(intent.RequiredCapabilities)
	hosts, err := m.Hosts.HealthyHosts(ctx, m.heartbeatWindow())
	if err != nil {
		return scheduler.DeploymentPlacement{}, fmt.Errorf("multihost scheduler: list healthy hosts: %w", err)
	}
	picked, ok := pickFirstFit(hosts, required)
	if !ok {
		return scheduler.DeploymentPlacement{}, fmt.Errorf("%w: required=%v", ErrNoEligibleHost, required)
	}
	payload, etag, err := buildDesiredState(picked, intent)
	if err != nil {
		return scheduler.DeploymentPlacement{}, fmt.Errorf("multihost scheduler: build desired state: %w", err)
	}
	if err := m.Desired.WriteDesiredState(ctx, picked.ID, etag, payload); err != nil {
		return scheduler.DeploymentPlacement{}, fmt.Errorf("multihost scheduler: write desired state for %s: %w", picked.ID, err)
	}
	return scheduler.DeploymentPlacement{
		DeploymentID: intent.ScheduleRequest.DeploymentID,
		InstanceID:   intent.ScheduleRequest.InstanceID,
		Assignments: []scheduler.HostAssignment{{
			HostID:       picked.ID,
			Capabilities: copyCapabilities(picked.Labels),
		}},
		InstanceStatus: runtime.InstanceStatus{
			DeploymentID: intent.ScheduleRequest.DeploymentID,
			InstanceID:   intent.ScheduleRequest.InstanceID,
			State:        runtime.InstanceStateAllocated,
			ObservedAt:   m.clock()(),
			Reason:       "multihost scheduler: desired state published; waiting for agent ack",
		},
	}, nil
}

// Reconcile is a stub. To flip the switch we must:
//
//	TODO(multihost): poll AgentStatusReader.LastStatusReport for the
//	deployment's assigned host and project the agent's runtime.InstanceStatus
//	into scheduler.ReconcileResult. The endpoint already exists
//	(POST /api/v1alpha/agents/{id}/status). Wire the projection here.
func (m *MultiHostScheduler) Reconcile(_ context.Context, deploymentID string) (scheduler.ReconcileResult, error) {
	return scheduler.ReconcileResult{DeploymentID: deploymentID},
		fmt.Errorf("%w: Reconcile requires AgentStatusReader projection from /api/v1alpha/agents/{id}/status", scheduler.ErrNotImplemented)
}

// Drain is a stub. To flip the switch we must:
//
//	TODO(multihost): write a desired-state document with the instance marked
//	for drain (state=DRAINING, grace=X) so the remote agent's reconciler
//	picks it up. The agent already converges desired-state diffs; the only
//	missing piece is the drain marker in the agent_desired_state JSON shape.
//	Round-trip completion is verified by polling AgentStatusReader for
//	state=STOPPED.
func (m *MultiHostScheduler) Drain(_ context.Context, _ string, _ time.Duration) error {
	return fmt.Errorf("%w: Drain requires desired-state drain marker + /api/v1alpha/agents/{id}/status round-trip", scheduler.ErrNotImplemented)
}

// State is a stub on the read side. It returns ErrNotImplemented until the
// AgentStatusReader projection lands; the executor falls back to its own
// CandidateActivationRequest reconstruction in the meantime.
func (m *MultiHostScheduler) State(_ context.Context, deploymentID string) (scheduler.DeploymentState, error) {
	return scheduler.DeploymentState{DeploymentID: deploymentID},
		fmt.Errorf("%w: State requires AgentStatusReader projection from /api/v1alpha/agents/{id}/status", scheduler.ErrNotImplemented)
}

func (m *MultiHostScheduler) heartbeatWindow() time.Duration {
	if m.HeartbeatWindow > 0 {
		return m.HeartbeatWindow
	}
	return HealthyHeartbeatWindow
}

func (m *MultiHostScheduler) clock() func() time.Time {
	if m.Now != nil {
		return m.Now
	}
	return func() time.Time { return time.Now().UTC() }
}

func normalizeCapabilities(required map[string]string) map[string]string {
	if len(required) == 0 {
		return map[string]string{DefaultRequiredCapabilityKey: DefaultRequiredCapabilityValue}
	}
	out := make(map[string]string, len(required))
	for k, v := range required {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		out[k] = v
	}
	if _, ok := out[DefaultRequiredCapabilityKey]; !ok {
		out[DefaultRequiredCapabilityKey] = DefaultRequiredCapabilityValue
	}
	return out
}

// pickFirstFit implements the documented "first healthy host advertising the
// required capability set" strategy. The host list is sorted by id so the
// pick is deterministic across calls — important for idempotency on the
// agent_desired_state row's etag.
func pickFirstFit(hosts []HostRecord, required map[string]string) (HostRecord, bool) {
	sorted := append([]HostRecord(nil), hosts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, host := range sorted {
		if !hostMatchesCapabilities(host, required) {
			continue
		}
		return host, true
	}
	return HostRecord{}, false
}

func hostMatchesCapabilities(host HostRecord, required map[string]string) bool {
	for k, want := range required {
		got, ok := host.Labels[k]
		if !ok {
			return false
		}
		if want != "" && got != want {
			return false
		}
	}
	return true
}

// buildDesiredState marshals the runtime.ScheduleRequest into the JSON shape
// the existing /api/v1alpha/agents/{id}/desired endpoint already serves. The
// etag is the IdempotencyKey on the schedule request so duplicate Schedule
// calls (e.g. worker retries) write the same row and the agent's
// If-None-Match short-circuits the GET.
func buildDesiredState(host HostRecord, intent scheduler.ScheduleIntent) ([]byte, string, error) {
	req := intent.ScheduleRequest
	payload := struct {
		AgentID   string            `json:"agent_id"`
		HostID    string            `json:"host_id"`
		Instances []desiredInstance `json:"instances"`
		Issued    time.Time         `json:"issued_at"`
		Operation string            `json:"operation_id,omitempty"`
		Required  map[string]string `json:"required_capabilities,omitempty"`
	}{
		AgentID: host.ID,
		HostID:  host.ID,
		Instances: []desiredInstance{{
			DeploymentID:   req.DeploymentID,
			InstanceID:     req.InstanceID,
			ProjectID:      req.ProjectID,
			EnvironmentID:  req.EnvironmentID,
			ServiceID:      req.ServiceID,
			Image:          req.Image,
			Labels:         req.Labels,
			IdempotencyKey: req.IdempotencyKey,
			Desired:        "RUNNING",
		}},
		Issued:    time.Now().UTC(),
		Operation: intent.OperationID,
		Required:  intent.RequiredCapabilities,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	etag := req.IdempotencyKey
	if etag == "" {
		etag = req.DeploymentID + ":" + req.InstanceID
	}
	return encoded, `"` + etag + `"`, nil
}

type desiredInstance struct {
	DeploymentID   string            `json:"deployment_id"`
	InstanceID     string            `json:"instance_id"`
	ProjectID      string            `json:"project_id,omitempty"`
	EnvironmentID  string            `json:"environment_id,omitempty"`
	ServiceID      string            `json:"service_id,omitempty"`
	Image          string            `json:"image"`
	Labels         map[string]string `json:"labels,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Desired        string            `json:"desired_state"`
}

func copyCapabilities(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
