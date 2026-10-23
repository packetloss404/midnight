package timeline

import "time"

const (
	DefaultDeploymentHeartbeatWarningAfter = 90 * time.Second
	DefaultDeploymentHeartbeatStuckAfter   = 180 * time.Second
)

type DeploymentHeartbeatGapStatus string

const (
	DeploymentHeartbeatGapOK      DeploymentHeartbeatGapStatus = "ok"
	DeploymentHeartbeatGapWarning DeploymentHeartbeatGapStatus = "warning"
	DeploymentHeartbeatGapStuck   DeploymentHeartbeatGapStatus = "stuck"
)

type DeploymentHeartbeatGapIssueCode string

const (
	DeploymentHeartbeatIssueClockSkew DeploymentHeartbeatGapIssueCode = "deployment_heartbeat_clock_skew"
	DeploymentHeartbeatIssueMissing   DeploymentHeartbeatGapIssueCode = "deployment_heartbeat_missing"
	DeploymentHeartbeatIssueWarning   DeploymentHeartbeatGapIssueCode = "deployment_heartbeat_gap_warning"
	DeploymentHeartbeatIssueStuck     DeploymentHeartbeatGapIssueCode = "deployment_heartbeat_gap_stuck"
)

type DeploymentHeartbeatGapReviewInput struct {
	Now             time.Time
	LastHeartbeatAt time.Time
	WarningAfter    time.Duration
	StuckAfter      time.Duration
}

// DeploymentHeartbeatGapReview is a support-safe deployment job heartbeat
// diagnostic. It reports durations and stable issue codes, not hostnames,
// tokens, command lines, or raw log content.
type DeploymentHeartbeatGapReview struct {
	Status              DeploymentHeartbeatGapStatus      `json:"status"`
	GapSeconds          int64                             `json:"gapSeconds"`
	WarningAfterSeconds int64                             `json:"warningAfterSeconds"`
	StuckAfterSeconds   int64                             `json:"stuckAfterSeconds"`
	IssueCodes          []DeploymentHeartbeatGapIssueCode `json:"issueCodes,omitempty"`
	Diagnostics         []string                          `json:"diagnostics"`
}

// ReviewDeploymentHeartbeatGap classifies the time since the last deployment
// job heartbeat. Phase 0 treats gaps strictly greater than 180 seconds as stuck.
func ReviewDeploymentHeartbeatGap(input DeploymentHeartbeatGapReviewInput) DeploymentHeartbeatGapReview {
	warningAfter := input.WarningAfter
	if warningAfter <= 0 {
		warningAfter = DefaultDeploymentHeartbeatWarningAfter
	}
	stuckAfter := input.StuckAfter
	if stuckAfter <= 0 {
		stuckAfter = DefaultDeploymentHeartbeatStuckAfter
	}
	if warningAfter >= stuckAfter {
		warningAfter = stuckAfter / 2
	}

	review := DeploymentHeartbeatGapReview{
		Status:              DeploymentHeartbeatGapOK,
		WarningAfterSeconds: int64(warningAfter.Seconds()),
		StuckAfterSeconds:   int64(stuckAfter.Seconds()),
		Diagnostics:         []string{"deployment job heartbeat is current"},
	}
	if input.LastHeartbeatAt.IsZero() {
		review.Status = DeploymentHeartbeatGapWarning
		review.IssueCodes = []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueMissing}
		review.Diagnostics = []string{"deployment job heartbeat has not been observed"}
		return review
	}

	gap := input.Now.Sub(input.LastHeartbeatAt)
	if gap < 0 {
		review.IssueCodes = []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueClockSkew}
		review.Diagnostics = []string{"deployment job heartbeat timestamp is ahead of review time"}
		return review
	}

	review.GapSeconds = int64(gap.Seconds())
	if gap > stuckAfter {
		review.Status = DeploymentHeartbeatGapStuck
		review.IssueCodes = []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueStuck}
		review.Diagnostics = []string{"deployment job heartbeat gap exceeded stuck threshold"}
		return review
	}
	if gap > warningAfter {
		review.Status = DeploymentHeartbeatGapWarning
		review.IssueCodes = []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueWarning}
		review.Diagnostics = []string{"deployment job heartbeat gap exceeded warning threshold"}
	}
	return review
}
