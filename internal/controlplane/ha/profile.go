package ha

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/networking"
)

const (
	StatusReady       = "ready"
	StatusNeedsReview = "needs-review"
	StatusBlocked     = "blocked"

	IssueQuorumPolicyInvalid        = "QUORUM_POLICY_INVALID"
	IssueQuorumMembershipMismatch   = "QUORUM_MEMBERSHIP_MISMATCH"
	IssueQuorumUnavailable          = "QUORUM_UNAVAILABLE"
	IssueQuorumVoterNotReady        = "QUORUM_VOTER_NOT_READY"
	IssueMigrationCandidateMissing  = "MIGRATION_CANDIDATE_MISSING"
	IssueMigrationCandidateNotReady = "MIGRATION_CANDIDATE_NOT_READY"
	IssueMigrationCandidateNonVoter = "MIGRATION_CANDIDATE_NON_VOTER"
	IssueMigrationEvidenceNotReady  = "MIGRATION_EVIDENCE_NOT_READY"
	IssueMigrationQuorumNotReady    = "MIGRATION_QUORUM_NOT_READY"
	IssueFailoverEvidenceMissing    = "FAILOVER_EVIDENCE_MISSING"
	IssueFailoverQuorumNotReady     = "FAILOVER_QUORUM_NOT_READY"
	IssueFailoverTargetInvalid      = "FAILOVER_TARGET_INVALID"
	IssueRegionRoutingNotReady      = "REGION_ROUTING_NOT_READY"
	IssueUnsafeHAClaim              = "UNSAFE_HA_CLAIM"
)

type Node struct {
	ID                  string `json:"id"`
	Voter               bool   `json:"voter"`
	Ready               bool   `json:"ready"`
	HeartbeatFresh      bool   `json:"heartbeat_fresh"`
	MigrationCapable    bool   `json:"migration_capable"`
	ObservedTerm        int    `json:"observed_term,omitempty"`
	ObservedLeaderID    string `json:"observed_leader_id,omitempty"`
	LastHeartbeatAgeSec int    `json:"last_heartbeat_age_sec,omitempty"`
}

type QuorumPolicy struct {
	ExpectedVoters int `json:"expected_voters"`
	MinReadyVoters int `json:"min_ready_voters,omitempty"`
}

type Issue struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type QuorumReadinessPlan struct {
	Status           string   `json:"status"`
	IssueCodes       []string `json:"issue_codes"`
	Issues           []Issue  `json:"issues"`
	ExpectedVoters   int      `json:"expected_voters"`
	ConfiguredVoters int      `json:"configured_voters"`
	ReadyVoters      int      `json:"ready_voters"`
	FreshVoters      int      `json:"fresh_voters"`
	MinReadyVoters   int      `json:"min_ready_voters"`
	ReadyForQuorum   bool     `json:"ready_for_quorum"`
}

type MigrationLeaderInput struct {
	CandidateID           string              `json:"candidate_id"`
	Nodes                 []Node              `json:"nodes"`
	Quorum                QuorumReadinessPlan `json:"quorum"`
	SchemaEvidenceStatus  string              `json:"schema_evidence_status"`
	MigrationPlanStatus   string              `json:"migration_plan_status"`
	BackupReadinessStatus string              `json:"backup_readiness_status,omitempty"`
}

type MigrationLeaderEligibility struct {
	Status      string   `json:"status"`
	IssueCodes  []string `json:"issue_codes"`
	Issues      []Issue  `json:"issues"`
	CandidateID string   `json:"candidate_id"`
	Eligible    bool     `json:"eligible"`
}

type FailoverInput struct {
	Requested              bool                `json:"requested"`
	ReasonCode             string              `json:"reason_code"`
	CurrentLeaderID        string              `json:"current_leader_id"`
	TargetLeaderID         string              `json:"target_leader_id"`
	OperatorApproved       bool                `json:"operator_approved"`
	HealthEvidenceAttached bool                `json:"health_evidence_attached"`
	Quorum                 QuorumReadinessPlan `json:"quorum"`
	Nodes                  []Node              `json:"nodes"`
}

type FailoverDecisionEvidence struct {
	Status                 string   `json:"status"`
	IssueCodes             []string `json:"issue_codes"`
	Issues                 []Issue  `json:"issues"`
	Decision               string   `json:"decision"`
	Requested              bool     `json:"requested"`
	ReasonCode             string   `json:"reason_code"`
	CurrentLeaderID        string   `json:"current_leader_id"`
	TargetLeaderID         string   `json:"target_leader_id"`
	OperatorApproved       bool     `json:"operator_approved"`
	HealthEvidenceAttached bool     `json:"health_evidence_attached"`
}

type ProfileCInput struct {
	ClaimedHAProfile string                     `json:"claimed_ha_profile"`
	Quorum           QuorumReadinessPlan        `json:"quorum"`
	MigrationLeader  MigrationLeaderEligibility `json:"migration_leader"`
	Failover         FailoverDecisionEvidence   `json:"failover"`
	RegionRouting    networking.RegionRoutePlan `json:"region_routing"`
}

type ProfileCReadiness struct {
	Status           string                     `json:"status"`
	IssueCodes       []string                   `json:"issue_codes"`
	Issues           []Issue                    `json:"issues"`
	ClaimedHAProfile string                     `json:"claimed_ha_profile"`
	HAClaimAllowed   bool                       `json:"ha_claim_allowed"`
	Quorum           QuorumReadinessPlan        `json:"quorum"`
	MigrationLeader  MigrationLeaderEligibility `json:"migration_leader"`
	Failover         FailoverDecisionEvidence   `json:"failover"`
	RegionRouting    networking.RegionRoutePlan `json:"region_routing"`
}

func PlanQuorumReadiness(policy QuorumPolicy, nodes []Node) QuorumReadinessPlan {
	plan := QuorumReadinessPlan{Status: StatusReady, IssueCodes: []string{}, Issues: []Issue{}, ExpectedVoters: policy.ExpectedVoters, MinReadyVoters: policy.MinReadyVoters}
	if plan.MinReadyVoters == 0 && policy.ExpectedVoters > 0 {
		plan.MinReadyVoters = policy.ExpectedVoters/2 + 1
	}
	if policy.ExpectedVoters <= 0 || plan.MinReadyVoters <= 0 || plan.MinReadyVoters > policy.ExpectedVoters {
		plan.addIssue(IssueQuorumPolicyInvalid, "quorum policy must declare expected voters and an achievable ready-voter threshold", map[string]any{"expected_voters": policy.ExpectedVoters, "min_ready_voters": policy.MinReadyVoters})
	}

	for _, node := range nodes {
		if !node.Voter {
			continue
		}
		plan.ConfiguredVoters++
		if node.Ready {
			plan.ReadyVoters++
		} else {
			plan.addIssue(IssueQuorumVoterNotReady, "control-plane voter is not ready", map[string]any{"node_id": node.ID})
		}
		if node.Ready && node.HeartbeatFresh {
			plan.FreshVoters++
		}
	}

	if policy.ExpectedVoters > 0 && plan.ConfiguredVoters != policy.ExpectedVoters {
		plan.addIssue(IssueQuorumMembershipMismatch, "configured voter count does not match the expected quorum membership", map[string]any{"configured_voters": plan.ConfiguredVoters, "expected_voters": policy.ExpectedVoters})
	}
	if plan.FreshVoters < plan.MinReadyVoters {
		plan.addIssue(IssueQuorumUnavailable, "fresh ready voters do not satisfy quorum", map[string]any{"fresh_voters": plan.FreshVoters, "min_ready_voters": plan.MinReadyVoters})
	}

	plan.ReadyForQuorum = plan.FreshVoters >= plan.MinReadyVoters && plan.ConfiguredVoters == plan.ExpectedVoters && len(plan.Issues) == 0
	plan.Status = rollupStatus(plan.Issues)
	return plan
}

func EvaluateMigrationLeaderEligibility(input MigrationLeaderInput) MigrationLeaderEligibility {
	result := MigrationLeaderEligibility{Status: StatusReady, IssueCodes: []string{}, Issues: []Issue{}, CandidateID: strings.TrimSpace(input.CandidateID)}
	candidate, ok := findNode(input.Nodes, result.CandidateID)
	if !ok {
		result.addIssue(IssueMigrationCandidateMissing, "migration leader candidate is not present in control-plane observations", nil)
	} else {
		if !candidate.Ready || !candidate.HeartbeatFresh || !candidate.MigrationCapable {
			result.addIssue(IssueMigrationCandidateNotReady, "migration leader candidate is not ready, fresh, and migration-capable", map[string]any{"node_id": candidate.ID})
		}
		if !candidate.Voter {
			result.addIssue(IssueMigrationCandidateNonVoter, "migration leader candidate must be a quorum voter", map[string]any{"node_id": candidate.ID})
		}
	}
	if input.Quorum.Status != StatusReady || !input.Quorum.ReadyForQuorum {
		result.addIssue(IssueMigrationQuorumNotReady, "migration leader election requires ready quorum evidence", map[string]any{"quorum_status": input.Quorum.Status})
	}
	checks := []struct {
		name   string
		status string
	}{
		{name: "schema_evidence", status: input.SchemaEvidenceStatus},
		{name: "migration_plan", status: input.MigrationPlanStatus},
		{name: "backup_readiness", status: input.BackupReadinessStatus},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.status) == "" {
			continue
		}
		if normalizeStatus(check.status) != StatusReady {
			result.addIssue(IssueMigrationEvidenceNotReady, "migration leader is not eligible until migration safety evidence is ready", map[string]any{"component": check.name, "status": check.status})
		}
	}
	result.Status = rollupStatus(result.Issues)
	result.Eligible = result.Status == StatusReady
	return result
}

func BuildFailoverDecisionEvidence(input FailoverInput) FailoverDecisionEvidence {
	evidence := FailoverDecisionEvidence{Status: StatusReady, IssueCodes: []string{}, Issues: []Issue{}, Decision: "hold", Requested: input.Requested, ReasonCode: strings.TrimSpace(input.ReasonCode), CurrentLeaderID: strings.TrimSpace(input.CurrentLeaderID), TargetLeaderID: strings.TrimSpace(input.TargetLeaderID), OperatorApproved: input.OperatorApproved, HealthEvidenceAttached: input.HealthEvidenceAttached}
	if !input.Requested {
		evidence.Decision = "none"
		return evidence
	}
	if evidence.ReasonCode == "" || !input.OperatorApproved || !input.HealthEvidenceAttached {
		evidence.addIssue(IssueFailoverEvidenceMissing, "failover requires reason, operator approval, and health evidence", map[string]any{"operator_approved": input.OperatorApproved, "health_evidence_attached": input.HealthEvidenceAttached})
	}
	if input.Quorum.Status != StatusReady || !input.Quorum.ReadyForQuorum {
		evidence.addIssue(IssueFailoverQuorumNotReady, "failover requires ready quorum evidence", map[string]any{"quorum_status": input.Quorum.Status})
	}
	if target, ok := findNode(input.Nodes, evidence.TargetLeaderID); !ok || !target.Voter || !target.Ready || !target.HeartbeatFresh {
		evidence.addIssue(IssueFailoverTargetInvalid, "failover target must be a ready fresh quorum voter", map[string]any{"target_leader_id": evidence.TargetLeaderID})
	}
	evidence.Status = rollupStatus(evidence.Issues)
	if evidence.Status == StatusReady {
		evidence.Decision = "approve"
	}
	return evidence
}

func BuildProfileCReadiness(input ProfileCInput) ProfileCReadiness {
	summary := ProfileCReadiness{Status: StatusReady, IssueCodes: []string{}, Issues: []Issue{}, ClaimedHAProfile: strings.TrimSpace(input.ClaimedHAProfile), Quorum: input.Quorum, MigrationLeader: input.MigrationLeader, Failover: input.Failover, RegionRouting: input.RegionRouting}
	routingStatus := StatusReady
	if !input.RegionRouting.HasPrimary || input.RegionRouting.Degraded || len(input.RegionRouting.Standbys) == 0 {
		routingStatus = StatusBlocked
		summary.addIssue(IssueRegionRoutingNotReady, "Profile C HA requires a non-degraded region route plan with primary and standby regions", map[string]any{"has_primary": input.RegionRouting.HasPrimary, "standby_count": len(input.RegionRouting.Standbys), "degraded": input.RegionRouting.Degraded, "reason": input.RegionRouting.Reason})
	}
	componentStatus := rollupComponentStatuses(input.Quorum.Status, input.MigrationLeader.Status, input.Failover.Status, routingStatus)
	if strings.EqualFold(summary.ClaimedHAProfile, "profile-c") && componentStatus != StatusReady {
		summary.addIssue(IssueUnsafeHAClaim, "Profile C HA must not be claimed until quorum, migration leader, failover, and region routing evidence are ready", map[string]any{"quorum_status": input.Quorum.Status, "migration_leader_status": input.MigrationLeader.Status, "failover_status": input.Failover.Status, "region_routing_status": routingStatus})
	}
	summary.Status = rollupComponentStatuses(componentStatus, rollupStatus(summary.Issues))
	summary.HAClaimAllowed = strings.EqualFold(summary.ClaimedHAProfile, "profile-c") && summary.Status == StatusReady
	return summary
}

func MarshalProfileCReadinessJSON(summary ProfileCReadiness) ([]byte, error) {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Profile C readiness: %w", err)
	}
	return append(data, '\n'), nil
}

func findNode(nodes []Node, id string) (Node, bool) {
	for _, node := range nodes {
		if node.ID == id && id != "" {
			return node, true
		}
	}
	return Node{}, false
}

func normalizeStatus(status string) string {
	switch status {
	case StatusReady, StatusNeedsReview, StatusBlocked:
		return status
	default:
		return StatusBlocked
	}
}

func rollupComponentStatuses(statuses ...string) string {
	status := StatusReady
	for _, componentStatus := range statuses {
		switch normalizeStatus(componentStatus) {
		case StatusBlocked:
			return StatusBlocked
		case StatusNeedsReview:
			status = StatusNeedsReview
		}
	}
	return status
}

func rollupStatus(issues []Issue) string {
	if len(issues) == 0 {
		return StatusReady
	}
	return StatusBlocked
}

func (plan *QuorumReadinessPlan) addIssue(code, message string, details map[string]any) {
	plan.IssueCodes = append(plan.IssueCodes, code)
	plan.Issues = append(plan.Issues, Issue{Code: code, Message: message, Details: details})
}

func (result *MigrationLeaderEligibility) addIssue(code, message string, details map[string]any) {
	result.IssueCodes = append(result.IssueCodes, code)
	result.Issues = append(result.Issues, Issue{Code: code, Message: message, Details: details})
}

func (evidence *FailoverDecisionEvidence) addIssue(code, message string, details map[string]any) {
	evidence.IssueCodes = append(evidence.IssueCodes, code)
	evidence.Issues = append(evidence.Issues, Issue{Code: code, Message: message, Details: details})
}

func (summary *ProfileCReadiness) addIssue(code, message string, details map[string]any) {
	summary.IssueCodes = append(summary.IssueCodes, code)
	summary.Issues = append(summary.Issues, Issue{Code: code, Message: message, Details: details})
}
