// Package supervisor coordinates agent process lifecycle concerns.
package supervisor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/desiredstate"
	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

const DefaultShutdownDeadline = 30 * time.Second

const (
	PhaseRegister    = "register"
	PhaseHeartbeat   = "heartbeat"
	PhaseDesiredPoll = "desired_poll"
)

// LoopPlan describes the finite phases a supervisor loop will execute.
// It is intentionally declarative in Phase 0 and does not start background loops.
type LoopPlan struct {
	Phases []string
}

// Registrar is the minimal registration dependency required by the one-shot runner.
type Registrar interface {
	Register(context.Context, protocol.RegistrationRequest) (protocol.RegistrationResponse, error)
}

// Heartbeater is the minimal heartbeat dependency required by the one-shot runner.
type Heartbeater interface {
	Heartbeat(context.Context, protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error)
}

// DesiredFetcher is the minimal desired-state polling dependency required by the one-shot runner.
type DesiredFetcher interface {
	FetchDesired(context.Context, string, string) (DesiredFetchResult, error)
}

// DesiredCache persists the last desired-state payload and validator.
type DesiredCache interface {
	Load() (desiredstate.State, error)
	Save(desiredstate.State) error
}

// DesiredFetchResult represents a single desired-state poll outcome.
type DesiredFetchResult struct {
	State       desiredstate.State
	NotModified bool
}

// Runner executes one finite lifecycle pass. It does not start background loops.
type Runner struct {
	Registrar      Registrar
	Heartbeater    Heartbeater
	DesiredFetcher DesiredFetcher
	DesiredCache   DesiredCache
	Sequencer      *protocol.HeartbeatSequencer
	Now            func() time.Time
}

// RunInput contains the local agent facts needed to build lifecycle requests.
type RunInput struct {
	AgentID         string
	Version         string
	Hostname        string
	OS              string
	Arch            string
	Runtime         string
	Labels          map[string]string
	HeartbeatSecret []byte
	MaintenanceMode bool
	DrainMode       bool
}

// AgentDiagnosticSummary captures the latest status for each one-shot lifecycle phase.
type AgentDiagnosticSummary struct {
	AgentID           string
	Registration      PhaseDiagnostic
	Heartbeat         PhaseDiagnostic
	Desired           PhaseDiagnostic
	HeartbeatSequence uint64
	DesiredETag       string
}

// PhaseDiagnostic summarizes the last attempt for one lifecycle phase.
type PhaseDiagnostic struct {
	Attempted bool
	OK        bool
	Error     string
	At        time.Time
}

// NewLoopPlan returns the default runtime agent loop sequence.
func NewLoopPlan() LoopPlan {
	return LoopPlan{Phases: []string{PhaseRegister, PhaseHeartbeat, PhaseDesiredPoll}}
}

// RunOnce registers, heartbeats, fetches desired state, and persists any fresh desired state once.
func (r Runner) RunOnce(ctx context.Context, input RunInput) (AgentDiagnosticSummary, error) {
	now := r.now()
	agentID := strings.TrimSpace(input.AgentID)
	summary := AgentDiagnosticSummary{AgentID: agentID}
	if agentID == "" {
		return summary, fmt.Errorf("agent id is required")
	}
	if r.Registrar == nil {
		return summary, fmt.Errorf("registrar is required")
	}
	if r.Heartbeater == nil {
		return summary, fmt.Errorf("heartbeater is required")
	}
	if r.DesiredFetcher == nil {
		return summary, fmt.Errorf("desired fetcher is required")
	}
	if r.DesiredCache == nil {
		return summary, fmt.Errorf("desired cache is required")
	}

	registration := protocol.RegistrationRequest{
		AgentID:   agentID,
		Version:   strings.TrimSpace(input.Version),
		Hostname:  strings.TrimSpace(input.Hostname),
		OS:        strings.TrimSpace(input.OS),
		Arch:      strings.TrimSpace(input.Arch),
		Labels:    input.Labels,
		Timestamp: now,
	}
	summary.Registration = phaseAttempt(now)
	registered, err := r.Registrar.Register(ctx, registration)
	if err != nil {
		markPhaseError(&summary.Registration, err)
		return summary, fmt.Errorf("register agent: %w", err)
	}
	markPhaseOK(&summary.Registration)
	if strings.TrimSpace(registered.AgentID) != "" {
		agentID = strings.TrimSpace(registered.AgentID)
		summary.AgentID = agentID
	}

	sequence := r.nextSequence()
	summary.HeartbeatSequence = sequence
	heartbeat := protocol.HeartbeatRequest{
		AgentID:         agentID,
		Sequence:        sequence,
		Status:          heartbeatStatus(input.MaintenanceMode, input.DrainMode),
		Runtime:         strings.TrimSpace(input.Runtime),
		SentAt:          now,
		MaintenanceMode: input.MaintenanceMode || input.DrainMode,
		DrainMode:       input.DrainMode,
	}
	heartbeat.HMAC = protocol.HeartbeatRequestMAC(input.HeartbeatSecret, heartbeat)
	summary.Heartbeat = phaseAttempt(now)
	if _, err := r.Heartbeater.Heartbeat(ctx, heartbeat); err != nil {
		markPhaseError(&summary.Heartbeat, err)
		return summary, fmt.Errorf("heartbeat agent: %w", err)
	}
	markPhaseOK(&summary.Heartbeat)

	summary.Desired = phaseAttempt(now)
	cached, err := r.DesiredCache.Load()
	if err != nil {
		markPhaseError(&summary.Desired, err)
		return summary, fmt.Errorf("load desired cache: %w", err)
	}
	fetched, err := r.DesiredFetcher.FetchDesired(ctx, agentID, cached.ETag)
	if err != nil {
		markPhaseError(&summary.Desired, err)
		return summary, fmt.Errorf("fetch desired state: %w", err)
	}
	if fetched.NotModified {
		summary.DesiredETag = cached.ETag
		markPhaseOK(&summary.Desired)
		return summary, nil
	}
	if err := r.DesiredCache.Save(fetched.State); err != nil {
		markPhaseError(&summary.Desired, err)
		return summary, fmt.Errorf("save desired cache: %w", err)
	}
	summary.DesiredETag = fetched.State.ETag
	markPhaseOK(&summary.Desired)
	return summary, nil
}

// ShutdownDeadline returns the absolute stop time for a graceful shutdown window.
func ShutdownDeadline(now time.Time, grace time.Duration) time.Time {
	if grace <= 0 {
		grace = DefaultShutdownDeadline
	}
	return now.Add(grace)
}

// WithShutdownDeadline derives a context that is canceled at the graceful shutdown deadline.
func WithShutdownDeadline(parent context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, ShutdownDeadline(time.Now(), grace))
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r Runner) nextSequence() uint64 {
	if r.Sequencer != nil {
		return r.Sequencer.Next()
	}
	var sequencer protocol.HeartbeatSequencer
	return sequencer.Next()
}

func phaseAttempt(at time.Time) PhaseDiagnostic {
	return PhaseDiagnostic{Attempted: true, At: at}
}

func markPhaseOK(d *PhaseDiagnostic) {
	d.OK = true
	d.Error = ""
}

func heartbeatStatus(maintenanceMode, drainMode bool) string {
	if drainMode {
		return "draining"
	}
	if maintenanceMode {
		return "maintenance"
	}
	return "ok"
}

func markPhaseError(d *PhaseDiagnostic, err error) {
	d.OK = false
	if err != nil {
		d.Error = err.Error()
	}
}
