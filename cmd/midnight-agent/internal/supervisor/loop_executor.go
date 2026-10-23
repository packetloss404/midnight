package supervisor

import (
	"context"
	"sort"
	"strings"
	"time"
)

// LoopActionExecutor performs side-effecting loop actions for an injected runtime.
// The supervisor owns planning and artifact recording; executor implementations own
// the transport boundary for register, heartbeat, and desired-state calls.
type LoopActionExecutor interface {
	Register(context.Context) (LoopInjectedOutcome, error)
	Heartbeat(context.Context) (LoopInjectedOutcome, error)
	FetchDesired(context.Context) (LoopInjectedOutcome, error)
}

// ExecutePlannedLoopActions executes planned loop actions through an injected executor.
// Backoff and noop actions are respected as planned delays and do not call the executor.
// The returned artifact is the only loop output; this function does not perform
// Docker work or write evidence files directly.
func ExecutePlannedLoopActions(ctx context.Context, planned []LoopPlannerDecision, executor LoopActionExecutor) LoopExecutionArtifact {
	actionOutcomes := make([]LoopExecutionActionOutcome, 0, len(planned))
	issueSet := make(map[string]struct{})
	statusRank := 0
	var nextDelay time.Duration

	for _, decision := range planned {
		outcome := executePlannedLoopAction(ctx, decision, executor)
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

func executePlannedLoopAction(ctx context.Context, decision LoopPlannerDecision, executor LoopActionExecutor) LoopExecutionActionOutcome {
	if executor == nil {
		return buildLoopExecutionActionOutcome(decision, LoopInjectedOutcomes{})
	}

	switch decision.Action {
	case LoopActionRegister:
		outcome := executeLoopAction(ctx, executor.Register)
		return buildLoopExecutionActionOutcome(decision, LoopInjectedOutcomes{Register: &outcome})
	case LoopActionHeartbeat:
		outcome := executeLoopAction(ctx, executor.Heartbeat)
		return buildLoopExecutionActionOutcome(decision, LoopInjectedOutcomes{Heartbeat: &outcome})
	case LoopActionFetchDesired:
		outcome := executeLoopAction(ctx, executor.FetchDesired)
		return buildLoopExecutionActionOutcome(decision, LoopInjectedOutcomes{FetchDesired: &outcome})
	default:
		return buildLoopExecutionActionOutcome(decision, LoopInjectedOutcomes{})
	}
}

func executeLoopAction(ctx context.Context, execute func(context.Context) (LoopInjectedOutcome, error)) LoopInjectedOutcome {
	outcome, err := execute(ctx)
	if err != nil {
		outcome.Completed = false
		if strings.TrimSpace(outcome.Message) == "" {
			outcome.Message = err.Error()
		}
		return outcome
	}
	outcome.Completed = true
	return outcome
}
