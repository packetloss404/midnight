package control

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
	"github.com/midnight/midnight/cmd/midnight-agent/internal/supervisor"
	"github.com/midnight/midnight/internal/failurecodes"
	runtimeapi "github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/docker"
)

// LoopConfig wires the live-network agent loop. Every field is required so
// the loop refuses to silently degrade behavior.
type LoopConfig struct {
	AgentID             string
	Hostname            string
	AgentVersion        string
	HMACSecret          []byte
	HeartbeatInterval   time.Duration
	HeartbeatTimeout    time.Duration
	StatusReportTimeout time.Duration
	HeartbeatFreshTTL   time.Duration
	Now                 func() time.Time
	Adapter             runtimeapi.RuntimeAdapter
	Baseline            docker.BaselineReport
	CGroupMode          docker.CGroupMode
}

// Loop drives the agent's register → heartbeat → status pipeline.
type Loop struct {
	cfg       LoopConfig
	client    protocol.AgentClient
	seq       protocol.HeartbeatSequencer
	statusSeq protocol.HeartbeatSequencer
	mu        sync.Mutex
	lastHB    time.Time
	stopped   bool
}

// NewLoop constructs a Loop bound to the supplied protocol.AgentClient.
func NewLoop(cfg LoopConfig, client protocol.AgentClient) (*Loop, error) {
	if cfg.AgentID == "" {
		return nil, errors.New("agent id is required")
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 5 * time.Second
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 10 * time.Second
	}
	if cfg.StatusReportTimeout <= 0 {
		cfg.StatusReportTimeout = 15 * time.Second
	}
	if cfg.HeartbeatFreshTTL <= 0 {
		cfg.HeartbeatFreshTTL = supervisor.HeartbeatLostAfter
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if len(cfg.HMACSecret) == 0 {
		return nil, errors.New("hmac secret is required for heartbeat signing")
	}
	return &Loop{cfg: cfg, client: client, lastHB: cfg.Now()}, nil
}

// Register dials the control plane and announces this agent. Returns the
// negotiated heartbeat interval; on VERSION_SKEW_REJECTED returns a typed
// docker.Error so callers (cmd/midnight-agent main) refuse to start.
func (l *Loop) Register(ctx context.Context) (protocol.RegistrationResponse, error) {
	body := protocol.RegistrationRequest{
		AgentID:   l.cfg.AgentID,
		Version:   l.cfg.AgentVersion,
		Hostname:  l.cfg.Hostname,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Timestamp: l.cfg.Now(),
		Labels: map[string]string{
			"runtime": "docker",
		},
	}
	resp, _, err := l.client.Register(body)
	if err != nil {
		return protocol.RegistrationResponse{}, classifyServerError(err)
	}
	if resp.HeartbeatInterval > 0 {
		l.cfg.HeartbeatInterval = resp.HeartbeatInterval
	}
	return resp, nil
}

// Tick exposes a single heartbeat + reconcile + status pass for tests and
// supervisor-driven manual ticks. Production code uses Run.
func (l *Loop) Tick(ctx context.Context) {
	l.tick(ctx)
}

// SendHeartbeat is exported so tests can drive the heartbeat path
// directly. Production code calls Run.
func (l *Loop) SendHeartbeat(ctx context.Context) error {
	return l.sendHeartbeat(ctx)
}

// Run executes the heartbeat + reconcile + status loop until ctx is
// cancelled. The loop:
//
//   - Sends a heartbeat at HeartbeatInterval.
//   - Polls desired state immediately after a successful heartbeat.
//   - Calls adapter.Reconcile() on each tick.
//   - Posts a StatusReport summarising observed instances and daemon
//     baseline drift.
//
// The loop does NOT crash-loop on transient network failures; instead it
// records the most recent successful heartbeat timestamp and surfaces lost-
// status via the supervisor.HeartbeatFreshness review.
func (l *Loop) Run(ctx context.Context) error {
	ticker := time.NewTicker(l.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			l.tick(ctx)
		}
	}
}

func (l *Loop) tick(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, l.cfg.HeartbeatTimeout)
	defer cancel()
	if err := l.sendHeartbeat(hbCtx); err != nil {
		// Record the failure but keep iterating; the freshness review
		// downstream will see the missing update.
		_ = err
		return
	}
	if l.cfg.Adapter != nil {
		_ = l.cfg.Adapter.Reconcile(ctx)
	}
	statusCtx, cancelStatus := context.WithTimeout(ctx, l.cfg.StatusReportTimeout)
	defer cancelStatus()
	_ = l.reportStatus(statusCtx)
}

// LastHeartbeat returns the most recent successful heartbeat send time.
func (l *Loop) LastHeartbeat() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHB
}

// HeartbeatFreshness returns the supervisor freshness review against the
// last successful heartbeat. The agent supervisor uses this to decide
// whether the loop is healthy.
func (l *Loop) HeartbeatFreshness() supervisor.HeartbeatFreshnessReview {
	last := l.LastHeartbeat()
	age := l.cfg.Now().Sub(last)
	return supervisor.ReviewHeartbeatFreshness(age, l.cfg.HeartbeatFreshTTL)
}

func (l *Loop) sendHeartbeat(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	seq := l.seq.Next()
	now := l.cfg.Now()
	body := protocol.HeartbeatRequest{
		AgentID:  l.cfg.AgentID,
		Sequence: seq,
		Status:   "ok",
		Runtime:  "docker",
		SentAt:   now,
	}
	body.HMAC = protocol.HeartbeatRequestMAC(l.cfg.HMACSecret, body)
	_, _, err := l.client.Heartbeat(body)
	if err != nil {
		return classifyServerError(err)
	}
	l.mu.Lock()
	l.lastHB = now
	l.mu.Unlock()
	return nil
}

func (l *Loop) reportStatus(ctx context.Context) error {
	if l.cfg.Adapter == nil {
		return nil
	}
	statuses, err := l.cfg.Adapter.StreamInstanceStatus(ctx)
	if err != nil {
		return err
	}
	routeTargets := l.routeTargetsFromStatuses(ctx, statuses)
	report := protocol.StatusReport{
		AgentID:      l.cfg.AgentID,
		Sequence:     l.statusSeq.Next(),
		ObservedAt:   l.cfg.Now(),
		Drift:        driftFromBaseline(l.cfg.Baseline, l.cfg.CGroupMode),
		Instances:    toInstanceStatusDTO(statuses),
		RouteTargets: routeTargets,
	}
	report.HMAC = protocol.StatusReportMAC(l.cfg.HMACSecret, report)
	_, _, err = l.client.ReportStatus(report)
	if err != nil {
		return classifyServerError(err)
	}
	return nil
}

func (l *Loop) routeTargetsFromStatuses(ctx context.Context, statuses []runtimeapi.InstanceStatus) []protocol.RouteTargetObservation {
	out := make([]protocol.RouteTargetObservation, 0, len(statuses))
	for _, status := range statuses {
		if status.DeploymentID == "" || status.InstanceID == "" || status.State == runtimeapi.InstanceStateLost || status.State == runtimeapi.InstanceStateRemoved {
			continue
		}
		target, err := l.cfg.Adapter.ResolveRoutingTarget(ctx, status.DeploymentID, status.InstanceID)
		if err != nil {
			out = append(out, protocol.RouteTargetObservation{
				DeploymentID:    status.DeploymentID,
				InstanceID:      status.InstanceID,
				ReadinessStatus: readinessStatus(status),
				LastObservedAt:  observedAtOrNow(status.ObservedAt, l.cfg.Now),
			})
			continue
		}
		out = append(out, protocol.RouteTargetObservation{
			ProjectID:           target.ProjectID,
			EnvironmentID:       target.EnvironmentID,
			DeploymentID:        firstNonEmpty(target.DeploymentID, status.DeploymentID),
			InstanceID:          firstNonEmpty(target.InstanceID, status.InstanceID),
			ContainerID:         target.ContainerID,
			ContainerIP:         target.ContainerIP,
			InternalServicePort: target.InternalPort,
			ReadinessStatus:     readinessStatusForTarget(status, target),
			LastObservedAt:      observedAtOrNow(target.ObservedAt, l.cfg.Now),
		})
	}
	return out
}

func toInstanceStatusDTO(in []runtimeapi.InstanceStatus) []protocol.InstanceStatus {
	out := make([]protocol.InstanceStatus, 0, len(in))
	for _, s := range in {
		out = append(out, protocol.InstanceStatus{
			DeploymentID: s.DeploymentID,
			InstanceID:   s.InstanceID,
			State:        string(s.State),
			ObservedAt:   s.ObservedAt,
			Reason:       s.Reason,
		})
	}
	return out
}

func readinessStatusForTarget(status runtimeapi.InstanceStatus, target runtimeapi.RoutingTarget) string {
	if target.Ready {
		return "ready"
	}
	return readinessStatus(status)
}

func readinessStatus(status runtimeapi.InstanceStatus) string {
	switch status.State {
	case runtimeapi.InstanceStateReady:
		return "ready"
	case runtimeapi.InstanceStateRunning:
		return "running"
	case runtimeapi.InstanceStateStarting, runtimeapi.InstanceStateCreating, runtimeapi.InstanceStatePulling, runtimeapi.InstanceStateAllocated:
		return "starting"
	case runtimeapi.InstanceStateDraining:
		return "draining"
	case runtimeapi.InstanceStateStopped:
		return "stopped"
	case runtimeapi.InstanceStateCrashed:
		return "crashed"
	case runtimeapi.InstanceStateUnhealthy:
		return "unhealthy"
	case runtimeapi.InstanceStateRestarting:
		return "restarting"
	default:
		return strings.ToLower(strings.TrimSpace(string(status.State)))
	}
}

func observedAtOrNow(observedAt time.Time, now func() time.Time) time.Time {
	if !observedAt.IsZero() {
		return observedAt
	}
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func driftFromBaseline(b docker.BaselineReport, mode docker.CGroupMode) *protocol.Drift {
	if b.APIVersion == "" && b.EngineVersion == "" && len(b.Conflicts) == 0 && mode == "" {
		return nil
	}
	conflicts := make([]string, 0, len(b.Conflicts))
	for _, c := range b.Conflicts {
		conflicts = append(conflicts, fmt.Sprintf("%s=%s want=%s", c.Key, c.Observed, c.Required))
	}
	cgroup := b.CGroupVersion
	if cgroup == "" && mode != "" {
		cgroup = string(mode)
	}
	return &protocol.Drift{
		APIVersion:        b.APIVersion,
		EngineVersion:     b.EngineVersion,
		StorageDriver:     b.StorageDriver,
		LiveRestore:       b.LiveRestore,
		LoggingDriver:     b.LoggingDriver,
		NoNewPrivileges:   b.NoNewPrivileges,
		CGroupVersion:     cgroup,
		BaselineConflicts: conflicts,
	}
}

// classifyServerError maps server-returned HTTPError codes onto the agent's
// failurecodes registry. The control plane returns
// VERSION_SKEW_REJECTED in the typed envelope for incompatible agents
// (panel: line 193); the loop surfaces that as a typed docker.Error so the
// agent main() can refuse to keep retrying.
func classifyServerError(err error) error {
	if err == nil {
		return nil
	}
	var he protocol.HTTPError
	if errors.As(err, &he) {
		switch failurecodes.Code(strings.ToUpper(he.Code)) {
		case failurecodes.VersionSkewRejected:
			return newCodedError(failurecodes.VersionSkewRejected, he.Message)
		case failurecodes.AgentClockSkew:
			return newCodedError(failurecodes.AgentClockSkew, he.Message)
		case failurecodes.AgentHeartbeatTampered,
			failurecodes.AgentHeartbeatReplay,
			failurecodes.AgentHeartbeatExpired,
			failurecodes.AgentStatusReportTampered,
			failurecodes.AgentStatusReportReplay,
			failurecodes.AgentStatusReportExpired:
			return newCodedError(failurecodes.Code(he.Code), he.Message)
		}
	}
	return err
}

// codedError is a tiny error type the agent loop returns when the server
// reports a registry-coded refusal.
type codedError struct {
	Code    failurecodes.Code
	Message string
}

func (c *codedError) Error() string {
	return fmt.Sprintf("%s: %s", c.Code, c.Message)
}

// CodeOf extracts the failurecodes.Code carried by err, if any.
func CodeOf(err error) failurecodes.Code {
	if err == nil {
		return ""
	}
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func newCodedError(code failurecodes.Code, msg string) *codedError {
	return &codedError{Code: code, Message: msg}
}
