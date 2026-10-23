package supervisor

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agentruntime "github.com/midnight/midnight/cmd/midnight-agent/internal/runtime"
)

func TestBuildAgentReleaseEvidencePacketWithExtrasIncludesDriftAndSelfHeal(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})
	extras := AgentReleaseEvidenceExtras{
		DiffSteps: []AgentDiffStepCount{{ResourceKind: "container", Action: "create", Count: 2}},
		Drift: []DriftEvent{{
			Classification: DriftDetected,
			ExpectedHash:   "abc",
			ObservedHash:   "def",
			Reason:         "image rolled back",
			At:             time.Unix(1, 0).UTC(),
		}},
		SelfHeal: []SelfHealEvent{{
			InstanceID: "inst-1",
			Decision:   SelfHealCooldownOn,
			Reason:     "min cooldown",
			WaitFor:    5 * time.Second,
			At:         time.Unix(1, 0).UTC(),
		}},
	}
	packet := BuildAgentReleaseEvidencePacketWithExtras(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusCompleted}),
		ReviewHeartbeatFreshness(2*time.Second, 10*time.Second),
		extras,
	)
	if packet.DiffPlan.StepCount != 2 {
		t.Fatalf("expected diff plan step count 2, got %d", packet.DiffPlan.StepCount)
	}
	if len(packet.Drift) != 1 || packet.Drift[0].Classification != DriftDetected {
		t.Fatalf("expected drift event, got %+v", packet.Drift)
	}
	if len(packet.SelfHeal) != 1 || packet.SelfHeal[0].Decision != SelfHealCooldownOn {
		t.Fatalf("expected self-heal cooldown event, got %+v", packet.SelfHeal)
	}
	wantCodes := map[string]bool{
		AgentReleaseIssueDriftDetected:    true,
		AgentReleaseIssueSelfHealCooldown: true,
		AgentReleaseIssueDiffStepsPending: true,
	}
	for _, issue := range packet.IssueRollup {
		delete(wantCodes, issue.Code)
	}
	if len(wantCodes) != 0 {
		t.Fatalf("missing issue codes %v in rollup %+v", wantCodes, packet.IssueRollup)
	}
	if packet.Status != AgentReleaseStatusNeedsReview {
		t.Fatalf("expected needs-review status, got %q", packet.Status)
	}
}

func TestAgentReleaseEvidencePacketWithExtrasRoundTripsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "packet.json")

	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})
	extras := AgentReleaseEvidenceExtras{
		DiffSteps: []AgentDiffStepCount{{ResourceKind: "network", Action: "update", Count: 1}},
		Drift: []DriftEvent{{
			Classification: DriftConverged,
			ExpectedHash:   "h",
			ObservedHash:   "h",
			At:             time.Unix(1, 0).UTC(),
		}},
		SelfHeal: []SelfHealEvent{{
			InstanceID: "inst-1",
			Decision:   SelfHealAllow,
			At:         time.Unix(1, 0).UTC(),
		}},
	}
	packet := BuildAgentReleaseEvidencePacketWithExtras(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusCompleted}),
		ReviewHeartbeatFreshness(time.Second, 10*time.Second),
		extras,
	)
	if err := WriteAgentReleaseEvidencePacketJSON(path, packet); err != nil {
		t.Fatalf("WriteAgentReleaseEvidencePacketJSON: %v", err)
	}
	got, err := ReadAgentReleaseEvidencePacketJSON(path)
	if err != nil {
		t.Fatalf("ReadAgentReleaseEvidencePacketJSON: %v", err)
	}
	if !reflect.DeepEqual(got.DiffPlan, packet.DiffPlan) {
		t.Fatalf("diff plan mismatch: %+v vs %+v", got.DiffPlan, packet.DiffPlan)
	}
	if !reflect.DeepEqual(got.Drift, packet.Drift) {
		t.Fatalf("drift mismatch: %+v vs %+v", got.Drift, packet.Drift)
	}
	if !reflect.DeepEqual(got.SelfHeal, packet.SelfHeal) {
		t.Fatalf("self-heal mismatch: %+v vs %+v", got.SelfHeal, packet.SelfHeal)
	}
	_ = os.Remove(path)
}
