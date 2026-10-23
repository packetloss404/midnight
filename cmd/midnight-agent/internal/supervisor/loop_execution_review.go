package supervisor

import (
	"sort"
	"strings"
	"time"
)

const (
	LoopExecutionReleaseGateReady       = "ready"
	LoopExecutionReleaseGateNeedsReview = "needs-review"
	LoopExecutionReleaseGateBlocked     = "blocked"

	LoopExecutionRetryRecommendationNone    = "none"
	LoopExecutionRetryRecommendationRetry   = "retry"
	LoopExecutionRetryRecommendationBackoff = "backoff"
)

// LoopExecutionReview is a JSON-friendly release gate view of a loop execution artifact.
type LoopExecutionReview struct {
	ReleaseGate         string                           `json:"release_gate"`
	ActionOutcomeCounts []LoopExecutionOutcomeCount      `json:"action_outcome_counts"`
	IssueRollup         []LoopExecutionIssueRollup       `json:"issue_rollup"`
	RetryRecommendation LoopExecutionRetryRecommendation `json:"retry_recommendation"`
}

// LoopExecutionOutcomeCount is a deterministic count for one action/status pair.
type LoopExecutionOutcomeCount struct {
	Action string `json:"action"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// LoopExecutionIssueRollup summarizes occurrences of one issue code.
type LoopExecutionIssueRollup struct {
	IssueCode string   `json:"issue_code"`
	Count     int      `json:"count"`
	Actions   []string `json:"actions"`
}

// LoopExecutionRetryRecommendation describes whether the next pass should retry immediately or wait.
type LoopExecutionRetryRecommendation struct {
	Action    string        `json:"action"`
	Delay     time.Duration `json:"delay"`
	IssueCode string        `json:"issue_code,omitempty"`
}

// ReviewLoopExecutionArtifact converts a pure loop execution artifact into release-gate metadata.
// It performs no HTTP, Docker, or filesystem I/O.
func ReviewLoopExecutionArtifact(artifact LoopExecutionArtifact) LoopExecutionReview {
	review := LoopExecutionReview{
		ReleaseGate:         releaseGateForLoopExecutionStatus(artifact.Status),
		ActionOutcomeCounts: loopExecutionOutcomeCounts(artifact.ActionOutcomes),
		IssueRollup:         loopExecutionIssueRollup(artifact),
		RetryRecommendation: loopExecutionRetryRecommendation(artifact),
	}
	return review
}

func releaseGateForLoopExecutionStatus(status string) string {
	switch status {
	case LoopExecutionStatusCompleted:
		return LoopExecutionReleaseGateReady
	case LoopExecutionStatusPartial:
		return LoopExecutionReleaseGateNeedsReview
	default:
		return LoopExecutionReleaseGateBlocked
	}
}

func loopExecutionOutcomeCounts(outcomes []LoopExecutionActionOutcome) []LoopExecutionOutcomeCount {
	type key struct {
		action string
		status string
	}
	counts := make(map[key]int)
	for _, outcome := range outcomes {
		counts[key{action: outcome.Action, status: outcome.Status}]++
	}

	keys := make([]key, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if actionRank(keys[i].action) != actionRank(keys[j].action) {
			return actionRank(keys[i].action) < actionRank(keys[j].action)
		}
		if keys[i].action != keys[j].action {
			return keys[i].action < keys[j].action
		}
		if loopExecutionStatusRank(keys[i].status) != loopExecutionStatusRank(keys[j].status) {
			return loopExecutionStatusRank(keys[i].status) < loopExecutionStatusRank(keys[j].status)
		}
		return keys[i].status < keys[j].status
	})

	result := make([]LoopExecutionOutcomeCount, 0, len(keys))
	for _, key := range keys {
		result = append(result, LoopExecutionOutcomeCount{Action: key.action, Status: key.status, Count: counts[key]})
	}
	return result
}

func loopExecutionIssueRollup(artifact LoopExecutionArtifact) []LoopExecutionIssueRollup {
	type issueSummary struct {
		count     int
		actionSet map[string]struct{}
	}
	summaries := make(map[string]issueSummary)
	for _, issueCode := range artifact.IssueCodes {
		issueCode = strings.TrimSpace(issueCode)
		if issueCode == "" {
			continue
		}
		summaries[issueCode] = issueSummary{actionSet: make(map[string]struct{})}
	}
	for _, outcome := range artifact.ActionOutcomes {
		issueCode := strings.TrimSpace(outcome.IssueCode)
		if issueCode == "" {
			continue
		}
		summary := summaries[issueCode]
		if summary.actionSet == nil {
			summary.actionSet = make(map[string]struct{})
		}
		summary.count++
		summary.actionSet[outcome.Action] = struct{}{}
		summaries[issueCode] = summary
	}

	issueCodes := make([]string, 0, len(summaries))
	for issueCode := range summaries {
		issueCodes = append(issueCodes, issueCode)
	}
	sort.Strings(issueCodes)

	rollup := make([]LoopExecutionIssueRollup, 0, len(issueCodes))
	for _, issueCode := range issueCodes {
		summary := summaries[issueCode]
		actions := make([]string, 0, len(summary.actionSet))
		for action := range summary.actionSet {
			actions = append(actions, action)
		}
		sortLoopActions(actions)
		rollup = append(rollup, LoopExecutionIssueRollup{IssueCode: issueCode, Count: summary.count, Actions: actions})
	}
	return rollup
}

func loopExecutionRetryRecommendation(artifact LoopExecutionArtifact) LoopExecutionRetryRecommendation {
	if artifact.Status == LoopExecutionStatusCompleted {
		return LoopExecutionRetryRecommendation{Action: LoopExecutionRetryRecommendationNone}
	}
	action := LoopExecutionRetryRecommendationRetry
	if artifact.NextDelay > 0 {
		action = LoopExecutionRetryRecommendationBackoff
	}
	return LoopExecutionRetryRecommendation{Action: action, Delay: artifact.NextDelay, IssueCode: firstLoopExecutionIssueCode(artifact)}
}

func firstLoopExecutionIssueCode(artifact LoopExecutionArtifact) string {
	for _, issueCode := range artifact.IssueCodes {
		if strings.TrimSpace(issueCode) != "" {
			return strings.TrimSpace(issueCode)
		}
	}
	for _, outcome := range artifact.ActionOutcomes {
		if strings.TrimSpace(outcome.IssueCode) != "" {
			return strings.TrimSpace(outcome.IssueCode)
		}
	}
	return ""
}

func sortLoopActions(actions []string) {
	sort.Slice(actions, func(i, j int) bool {
		if actionRank(actions[i]) != actionRank(actions[j]) {
			return actionRank(actions[i]) < actionRank(actions[j])
		}
		return actions[i] < actions[j]
	})
}

func actionRank(action string) int {
	for rank, knownAction := range knownLoopActionOrder() {
		if action == knownAction {
			return rank
		}
	}
	return len(knownLoopActionOrder())
}
