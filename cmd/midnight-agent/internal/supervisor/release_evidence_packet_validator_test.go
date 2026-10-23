package supervisor

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAgentReleaseEvidencePacketRejectsInvalidDriftClassification(t *testing.T) {
	packet := AgentReleaseEvidencePacket{
		Status: AgentReleaseStatusReady,
		Drift: []DriftEvent{{
			Classification: DriftClassification("unsupported"),
			At:             time.Unix(1, 0).UTC(),
		}},
	}
	err := ValidateAgentReleaseEvidencePacket(packet)
	if err == nil || !strings.Contains(err.Error(), "drift event") {
		t.Fatalf("expected drift validation error, got %v", err)
	}
}

func TestValidateAgentReleaseEvidencePacketRequiresDriftTimestamp(t *testing.T) {
	packet := AgentReleaseEvidencePacket{
		Status: AgentReleaseStatusReady,
		Drift:  []DriftEvent{{Classification: DriftConverged}},
	}
	err := ValidateAgentReleaseEvidencePacket(packet)
	if err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected timestamp error, got %v", err)
	}
}

func TestValidateAgentReleaseEvidencePacketRejectsInvalidSelfHealDecision(t *testing.T) {
	packet := AgentReleaseEvidencePacket{
		Status: AgentReleaseStatusReady,
		SelfHeal: []SelfHealEvent{{
			Decision: SelfHealDecision("???"),
			At:       time.Unix(1, 0).UTC(),
		}},
	}
	err := ValidateAgentReleaseEvidencePacket(packet)
	if err == nil || !strings.Contains(err.Error(), "self-heal") {
		t.Fatalf("expected self-heal validation error, got %v", err)
	}
}

func TestValidateAgentReleaseEvidencePacketRequiresSelfHealCooldownReason(t *testing.T) {
	packet := AgentReleaseEvidencePacket{
		Status:   AgentReleaseStatusReady,
		SelfHeal: []SelfHealEvent{{Decision: SelfHealCooldownOn, At: time.Unix(1, 0).UTC()}},
	}
	err := ValidateAgentReleaseEvidencePacket(packet)
	if err == nil || !strings.Contains(err.Error(), "explain") {
		t.Fatalf("expected cooldown explain error, got %v", err)
	}
}

func TestValidateAgentReleaseEvidencePacketAcceptsAllDriftClassifications(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	cases := []DriftClassification{DriftConverged, DriftDetected, DriftUnknown}
	for _, classification := range cases {
		packet := AgentReleaseEvidencePacket{
			Status: AgentReleaseStatusReady,
			Drift:  []DriftEvent{{Classification: classification, At: at, ExpectedHash: "h", ObservedHash: "h2", Reason: "test"}},
		}
		if err := ValidateAgentReleaseEvidencePacket(packet); err != nil {
			t.Fatalf("classification %q failed: %v", classification, err)
		}
	}
}

func TestValidateAgentReleaseEvidencePacketAcceptsSelfHealAllowAndCooldownWithReason(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	cases := []SelfHealEvent{
		{Decision: SelfHealAllow, At: at, InstanceID: "inst-1"},
		{Decision: SelfHealCooldownOn, At: at, InstanceID: "inst-1", Reason: "still cooling"},
	}
	for _, event := range cases {
		packet := AgentReleaseEvidencePacket{
			Status:   AgentReleaseStatusReady,
			SelfHeal: []SelfHealEvent{event},
		}
		if err := ValidateAgentReleaseEvidencePacket(packet); err != nil {
			t.Fatalf("event %+v failed: %v", event, err)
		}
	}
}
