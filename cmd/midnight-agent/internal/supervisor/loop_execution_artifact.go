package supervisor

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	LoopExecutionStatusCompleted = "completed"
	LoopExecutionStatusPartial   = "partial"
	LoopExecutionStatusBlocked   = "blocked"

	LoopExecutionIssueMissingRegisterOutcome     = "missing_register_outcome"
	LoopExecutionIssueMissingHeartbeatOutcome    = "missing_heartbeat_outcome"
	LoopExecutionIssueMissingFetchDesiredOutcome = "missing_fetch_desired_outcome"
	LoopExecutionIssueRegisterFailed             = "register_failed"
	LoopExecutionIssueHeartbeatFailed            = "heartbeat_failed"
	LoopExecutionIssueFetchDesiredFailed         = "fetch_desired_failed"
	LoopExecutionIssueUnknownAction              = "unknown_action"
)

// LoopInjectedOutcome is an externally supplied result for one side-effecting loop action.
type LoopInjectedOutcome struct {
	Completed bool          `json:"completed"`
	IssueCode string        `json:"issue_code,omitempty"`
	Message   string        `json:"message,omitempty"`
	NextDelay time.Duration `json:"next_delay,omitempty"`
}

// LoopInjectedOutcomes contains externally supplied results for planned loop actions.
type LoopInjectedOutcomes struct {
	Register     *LoopInjectedOutcome
	Heartbeat    *LoopInjectedOutcome
	FetchDesired *LoopInjectedOutcome
}

// LoopExecutionArtifact is a JSON-friendly description of a pure loop execution pass.
type LoopExecutionArtifact struct {
	Status         string                       `json:"status"`
	ActionOutcomes []LoopExecutionActionOutcome `json:"action_outcomes"`
	NextDelay      time.Duration                `json:"next_delay"`
	IssueCodes     []string                     `json:"issue_codes"`
}

// LoopExecutionActionOutcome is the deterministic result recorded for one planned action.
type LoopExecutionActionOutcome struct {
	Action    string        `json:"action"`
	Status    string        `json:"status"`
	Reason    string        `json:"reason,omitempty"`
	IssueCode string        `json:"issue_code,omitempty"`
	Message   string        `json:"message,omitempty"`
	Delay     time.Duration `json:"delay,omitempty"`
}

// BuildLoopExecutionArtifact combines planned loop actions with injected outcomes.
// It performs no HTTP, Docker, or filesystem I/O.
func BuildLoopExecutionArtifact(planned []LoopPlannerDecision, injected LoopInjectedOutcomes) LoopExecutionArtifact {
	actionOutcomes := make([]LoopExecutionActionOutcome, 0, len(planned))
	issueSet := make(map[string]struct{})
	statusRank := 0
	var nextDelay time.Duration

	for _, decision := range planned {
		outcome := buildLoopExecutionActionOutcome(decision, injected)
		actionOutcomes = append(actionOutcomes, outcome)
		if outcome.Delay > nextDelay {
			nextDelay = outcome.Delay
		}
		if outcome.IssueCode != "" {
			issueSet[outcome.IssueCode] = struct{}{}
		}
		if rank := loopExecutionStatusRank(outcome.Status); rank > statusRank {
			statusRank = rank
		}
	}

	issueCodes := make([]string, 0, len(issueSet))
	for issueCode := range issueSet {
		issueCodes = append(issueCodes, issueCode)
	}
	sort.Strings(issueCodes)

	return LoopExecutionArtifact{
		Status:         loopExecutionStatusFromRank(statusRank),
		ActionOutcomes: actionOutcomes,
		NextDelay:      nextDelay,
		IssueCodes:     issueCodes,
	}
}

// MarshalLoopExecutionArtifact marshals a loop execution artifact with stable field names.
func MarshalLoopExecutionArtifact(artifact LoopExecutionArtifact) ([]byte, error) {
	return json.Marshal(artifact)
}

func buildLoopExecutionActionOutcome(decision LoopPlannerDecision, injected LoopInjectedOutcomes) LoopExecutionActionOutcome {
	base := LoopExecutionActionOutcome{Action: decision.Action, Reason: decision.Reason, Delay: decision.Delay}
	switch decision.Action {
	case LoopActionRegister:
		return applyInjectedLoopOutcome(base, injected.Register, LoopExecutionIssueMissingRegisterOutcome, LoopExecutionIssueRegisterFailed)
	case LoopActionHeartbeat:
		return applyInjectedLoopOutcome(base, injected.Heartbeat, LoopExecutionIssueMissingHeartbeatOutcome, LoopExecutionIssueHeartbeatFailed)
	case LoopActionFetchDesired:
		return applyInjectedLoopOutcome(base, injected.FetchDesired, LoopExecutionIssueMissingFetchDesiredOutcome, LoopExecutionIssueFetchDesiredFailed)
	case LoopActionBackoff, LoopActionNoop:
		base.Status = LoopExecutionStatusCompleted
		return base
	default:
		base.Status = LoopExecutionStatusBlocked
		base.IssueCode = LoopExecutionIssueUnknownAction
		return base
	}
}

func applyInjectedLoopOutcome(base LoopExecutionActionOutcome, outcome *LoopInjectedOutcome, missingIssueCode, failedIssueCode string) LoopExecutionActionOutcome {
	if outcome == nil {
		base.Status = LoopExecutionStatusPartial
		base.IssueCode = missingIssueCode
		return base
	}
	if outcome.NextDelay > base.Delay {
		base.Delay = outcome.NextDelay
	}
	base.Message = strings.TrimSpace(outcome.Message)
	if outcome.Completed {
		base.Status = LoopExecutionStatusCompleted
		return base
	}
	base.Status = LoopExecutionStatusBlocked
	base.IssueCode = strings.TrimSpace(outcome.IssueCode)
	if base.IssueCode == "" {
		base.IssueCode = failedIssueCode
	}
	return base
}

func loopExecutionStatusRank(status string) int {
	switch status {
	case LoopExecutionStatusBlocked:
		return 2
	case LoopExecutionStatusPartial:
		return 1
	default:
		return 0
	}
}

func loopExecutionStatusFromRank(rank int) string {
	switch rank {
	case 2:
		return LoopExecutionStatusBlocked
	case 1:
		return LoopExecutionStatusPartial
	default:
		return LoopExecutionStatusCompleted
	}
}
