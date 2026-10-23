package ha

import (
	"bytes"
	"testing"

	"github.com/midnight/midnight/internal/networking"
)

func TestProfileCReadinessAllowsHAClaimWhenEvidenceReady(t *testing.T) {
	nodes := []Node{
		{ID: "cp-1", Voter: true, Ready: true, HeartbeatFresh: true, MigrationCapable: true},
		{ID: "cp-2", Voter: true, Ready: true, HeartbeatFresh: true, MigrationCapable: true},
		{ID: "cp-3", Voter: true, Ready: true, HeartbeatFresh: true, MigrationCapable: true},
	}
	quorum := PlanQuorumReadiness(QuorumPolicy{ExpectedVoters: 3}, nodes)
	migration := EvaluateMigrationLeaderEligibility(MigrationLeaderInput{CandidateID: "cp-1", Nodes: nodes, Quorum: quorum, SchemaEvidenceStatus: StatusReady, MigrationPlanStatus: StatusReady, BackupReadinessStatus: StatusReady})
	failover := BuildFailoverDecisionEvidence(FailoverInput{Requested: true, ReasonCode: "leader-unhealthy", CurrentLeaderID: "cp-1", TargetLeaderID: "cp-2", OperatorApproved: true, HealthEvidenceAttached: true, Quorum: quorum, Nodes: nodes})
	routing := readyRegionRoutePlan(t)

	summary := BuildProfileCReadiness(ProfileCInput{ClaimedHAProfile: "profile-c", Quorum: quorum, MigrationLeader: migration, Failover: failover, RegionRouting: routing})

	if summary.Status != StatusReady || !summary.HAClaimAllowed {
		t.Fatalf("Profile C summary = %+v, want ready and HA claim allowed", summary)
	}
	data, err := MarshalProfileCReadinessJSON(summary)
	if err != nil {
		t.Fatalf("MarshalProfileCReadinessJSON() error = %v", err)
	}
	if !bytes.Contains(data, []byte("\"ha_claim_allowed\": true")) {
		t.Fatalf("readiness JSON missing allowed claim: %s", data)
	}
}

func TestQuorumReadinessBlocksWithoutFreshMajority(t *testing.T) {
	nodes := []Node{
		{ID: "cp-1", Voter: true, Ready: true, HeartbeatFresh: true},
		{ID: "cp-2", Voter: true, Ready: true, HeartbeatFresh: false},
		{ID: "cp-3", Voter: true, Ready: false, HeartbeatFresh: false},
	}

	quorum := PlanQuorumReadiness(QuorumPolicy{ExpectedVoters: 3}, nodes)

	if quorum.Status != StatusBlocked || quorum.ReadyForQuorum {
		t.Fatalf("quorum = %+v, want blocked and not ready", quorum)
	}
	if !hasIssue(quorum.IssueCodes, IssueQuorumUnavailable) {
		t.Fatalf("issue codes = %v, want %s", quorum.IssueCodes, IssueQuorumUnavailable)
	}
}

func TestMigrationLeaderRequiresReadyVoterAndEvidence(t *testing.T) {
	nodes := []Node{{ID: "cp-1", Voter: false, Ready: true, HeartbeatFresh: true, MigrationCapable: true}}
	quorum := QuorumReadinessPlan{Status: StatusReady, ReadyForQuorum: true}

	result := EvaluateMigrationLeaderEligibility(MigrationLeaderInput{CandidateID: "cp-1", Nodes: nodes, Quorum: quorum, SchemaEvidenceStatus: StatusReady, MigrationPlanStatus: StatusNeedsReview})

	if result.Status != StatusBlocked || result.Eligible {
		t.Fatalf("migration leader = %+v, want blocked and ineligible", result)
	}
	if !hasIssue(result.IssueCodes, IssueMigrationCandidateNonVoter) || !hasIssue(result.IssueCodes, IssueMigrationEvidenceNotReady) {
		t.Fatalf("issue codes = %v, want non-voter and evidence issues", result.IssueCodes)
	}
}

func TestFailoverDecisionEvidenceHoldsWithoutApprovalOrTarget(t *testing.T) {
	nodes := []Node{{ID: "cp-1", Voter: true, Ready: true, HeartbeatFresh: true}}
	quorum := QuorumReadinessPlan{Status: StatusReady, ReadyForQuorum: true}

	evidence := BuildFailoverDecisionEvidence(FailoverInput{Requested: true, ReasonCode: "leader-unhealthy", CurrentLeaderID: "cp-1", TargetLeaderID: "cp-2", OperatorApproved: false, HealthEvidenceAttached: true, Quorum: quorum, Nodes: nodes})

	if evidence.Status != StatusBlocked || evidence.Decision != "hold" {
		t.Fatalf("failover evidence = %+v, want blocked hold", evidence)
	}
	if !hasIssue(evidence.IssueCodes, IssueFailoverEvidenceMissing) || !hasIssue(evidence.IssueCodes, IssueFailoverTargetInvalid) {
		t.Fatalf("issue codes = %v, want missing evidence and invalid target", evidence.IssueCodes)
	}
}

func TestProfileCReadinessRejectsUnsafeHAClaim(t *testing.T) {
	quorum := QuorumReadinessPlan{Status: StatusBlocked, ReadyForQuorum: false}
	migration := MigrationLeaderEligibility{Status: StatusReady, Eligible: true}
	failover := FailoverDecisionEvidence{Status: StatusReady, Decision: "approve"}
	routing := readyRegionRoutePlan(t)

	summary := BuildProfileCReadiness(ProfileCInput{ClaimedHAProfile: "profile-c", Quorum: quorum, MigrationLeader: migration, Failover: failover, RegionRouting: routing})

	if summary.Status != StatusBlocked || summary.HAClaimAllowed {
		t.Fatalf("Profile C summary = %+v, want blocked and no claim", summary)
	}
	if !hasIssue(summary.IssueCodes, IssueUnsafeHAClaim) {
		t.Fatalf("issue codes = %v, want unsafe HA claim", summary.IssueCodes)
	}
}

func TestProfileCReadinessRequiresRegionRoutingStandby(t *testing.T) {
	quorum := QuorumReadinessPlan{Status: StatusReady, ReadyForQuorum: true}
	migration := MigrationLeaderEligibility{Status: StatusReady, Eligible: true}
	failover := FailoverDecisionEvidence{Status: StatusReady, Decision: "approve"}
	routing, err := networking.PlanRegionRoute([]networking.RegionRouteCandidate{
		{RegionID: "us-a", Residency: "us", Health: networking.RegionHealthHealthy},
	}, networking.RegionRoutePolicy{RequiredResidency: "us", MinimumStandbys: 1})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}

	summary := BuildProfileCReadiness(ProfileCInput{ClaimedHAProfile: "profile-c", Quorum: quorum, MigrationLeader: migration, Failover: failover, RegionRouting: routing})

	if summary.Status != StatusBlocked || summary.HAClaimAllowed {
		t.Fatalf("Profile C summary = %+v, want blocked and no claim", summary)
	}
	if !hasIssue(summary.IssueCodes, IssueRegionRoutingNotReady) || !hasIssue(summary.IssueCodes, IssueUnsafeHAClaim) {
		t.Fatalf("issue codes = %v, want routing and unsafe claim issues", summary.IssueCodes)
	}
}

func readyRegionRoutePlan(t *testing.T) networking.RegionRoutePlan {
	t.Helper()
	plan, err := networking.PlanRegionRoute([]networking.RegionRouteCandidate{
		{RegionID: "us-a", Residency: "us", Health: networking.RegionHealthHealthy, LatencyMS: 10},
		{RegionID: "us-b", Residency: "us", Health: networking.RegionHealthHealthy, LatencyMS: 20},
	}, networking.RegionRoutePolicy{RequiredResidency: "us", MinimumStandbys: 1})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}
	return plan
}

func hasIssue(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}
