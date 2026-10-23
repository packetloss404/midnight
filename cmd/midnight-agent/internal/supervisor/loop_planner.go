package supervisor

import (
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

const (
	LoopActionRegister     = "register"
	LoopActionHeartbeat    = "heartbeat"
	LoopActionFetchDesired = "fetch_desired"
	LoopActionBackoff      = "backoff"
	LoopActionNoop         = "noop"

	DefaultHeartbeatInterval = 30 * time.Second
	DefaultDesiredMaxAge     = time.Minute
)

// LoopPlannerConfig contains timing policy for the pure runtime loop planner.
type LoopPlannerConfig struct {
	HeartbeatInterval time.Duration
	DesiredMaxAge     time.Duration
	HeartbeatBackoff  protocol.RetryBackoff
}

// RegistrationState is the last known registration result kept by the loop.
type RegistrationState struct {
	Registered bool
	AgentID    string
}

// HeartbeatOutcome is the last heartbeat attempt observed by the loop.
type HeartbeatOutcome struct {
	Attempted      bool
	OK             bool
	RetryAttempt   int
	LastSuccessAge time.Duration
}

// LoopPlannerInput contains all state needed to choose the next loop action.
type LoopPlannerInput struct {
	Config          LoopPlannerConfig
	Registration    RegistrationState
	DesiredCacheAge time.Duration
	Heartbeat       HeartbeatOutcome
	MaintenanceMode bool
	DrainMode       bool
	Random          protocol.RandomSource
}

// LoopPlannerDecision is a pure description of what the supervisor loop should do next.
type LoopPlannerDecision struct {
	Action string        `json:"action"`
	Reason string        `json:"reason,omitempty"`
	Delay  time.Duration `json:"delay"`
}

// LoopActionCount is a deterministic JSON-friendly count for one planned action.
type LoopActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// LoopDecisionSummary summarizes a batch of planned loop actions without side effects.
type LoopDecisionSummary struct {
	Counts                  []LoopActionCount `json:"counts"`
	NextDelayMax            time.Duration     `json:"next_delay_max"`
	MaintenanceMode         bool              `json:"maintenance_mode,omitempty"`
	DrainMode               bool              `json:"drain_mode,omitempty"`
	RegistrationWorkPending bool              `json:"registration_work_pending"`
	DesiredStateWorkPending bool              `json:"desired_state_work_pending"`
	HeartbeatWorkPending    bool              `json:"heartbeat_work_pending"`
}

// PlanNextLoopAction chooses the next registration/heartbeat/desired-state loop action.
// It is side-effect free and intentionally performs no HTTP or cache I/O.
func PlanNextLoopAction(input LoopPlannerInput) LoopPlannerDecision {
	config := normalizeLoopPlannerConfig(input.Config)
	registeredAgentID := strings.TrimSpace(input.Registration.AgentID)
	if !input.Registration.Registered || registeredAgentID == "" {
		return LoopPlannerDecision{Action: LoopActionRegister, Reason: "agent is not registered"}
	}

	if input.Heartbeat.Attempted && !input.Heartbeat.OK {
		delay := protocol.HeartbeatRetryDelay(input.Heartbeat.RetryAttempt, config.HeartbeatBackoff, input.Random)
		return LoopPlannerDecision{Action: LoopActionBackoff, Reason: "last heartbeat failed", Delay: delay}
	}

	if !input.Heartbeat.Attempted || input.Heartbeat.LastSuccessAge < 0 || input.Heartbeat.LastSuccessAge >= config.HeartbeatInterval {
		return LoopPlannerDecision{Action: LoopActionHeartbeat, Reason: "heartbeat is due"}
	}

	if input.MaintenanceMode || input.DrainMode {
		delay := config.HeartbeatInterval - input.Heartbeat.LastSuccessAge
		if delay < 0 {
			delay = 0
		}
		return LoopPlannerDecision{Action: LoopActionNoop, Reason: "agent is in maintenance mode", Delay: delay}
	}

	if input.DesiredCacheAge < 0 || input.DesiredCacheAge >= config.DesiredMaxAge {
		return LoopPlannerDecision{Action: LoopActionFetchDesired, Reason: "desired-state cache is stale"}
	}

	delay := minPositiveDuration(config.HeartbeatInterval-input.Heartbeat.LastSuccessAge, config.DesiredMaxAge-input.DesiredCacheAge)
	return LoopPlannerDecision{Action: LoopActionNoop, Reason: "no loop action is due", Delay: delay}
}

// SummarizeLoopDecisions returns deterministic aggregate metadata for planned loop actions.
// It is side-effect free and intentionally performs no HTTP, Docker, or cache I/O.
func SummarizeLoopDecisions(decisions []LoopPlannerDecision) LoopDecisionSummary {
	counts := make(map[string]int)
	var summary LoopDecisionSummary
	for _, decision := range decisions {
		counts[decision.Action]++
		if decision.Delay > summary.NextDelayMax {
			summary.NextDelayMax = decision.Delay
		}

		switch decision.Action {
		case LoopActionRegister:
			summary.RegistrationWorkPending = true
		case LoopActionFetchDesired:
			summary.DesiredStateWorkPending = true
		case LoopActionHeartbeat, LoopActionBackoff:
			summary.HeartbeatWorkPending = true
		}
	}

	for _, action := range knownLoopActionOrder() {
		if count := counts[action]; count > 0 {
			summary.Counts = append(summary.Counts, LoopActionCount{Action: action, Count: count})
			delete(counts, action)
		}
	}

	unknownActions := make([]string, 0, len(counts))
	for action := range counts {
		unknownActions = append(unknownActions, action)
	}
	sort.Strings(unknownActions)
	for _, action := range unknownActions {
		summary.Counts = append(summary.Counts, LoopActionCount{Action: action, Count: counts[action]})
	}

	return summary
}

// SummarizeLoopDecisionsForMode returns a loop summary annotated with the
// local maintenance/drain mode that influenced the planned decisions.
func SummarizeLoopDecisionsForMode(decisions []LoopPlannerDecision, maintenanceMode, drainMode bool) LoopDecisionSummary {
	summary := SummarizeLoopDecisions(decisions)
	summary.MaintenanceMode = maintenanceMode || drainMode
	summary.DrainMode = drainMode
	return summary
}

func normalizeLoopPlannerConfig(config LoopPlannerConfig) LoopPlannerConfig {
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if config.DesiredMaxAge <= 0 {
		config.DesiredMaxAge = DefaultDesiredMaxAge
	}
	return config
}

func minPositiveDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func knownLoopActionOrder() []string {
	return []string{LoopActionRegister, LoopActionHeartbeat, LoopActionFetchDesired, LoopActionBackoff, LoopActionNoop}
}
