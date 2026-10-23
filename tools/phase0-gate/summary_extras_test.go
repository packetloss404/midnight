package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnrichReleaseSummaryComputesVerdictAndManifestHash(t *testing.T) {
	base := releaseSummary{
		ReleaseReady: true,
		GateCounts: releaseSummaryGateCounts{
			DrillRows:             19,
			AlertRules:            8,
			AlertValidationPassed: true,
			DrillStatuses:         map[string]int{"placeholder": 19},
		},
		ExecutedDrillCounts:             releaseSummaryExecutedDrillCounts{Assertions: 95, Passed: 95},
		EvidenceIndexVerificationStatus: "pass",
		EvidencePacketItemStatuses: []releaseSummaryEvidenceItem{
			{Name: "drill-manifest", Status: "pass"},
			{Name: "prometheus-alerts", Status: "pass"},
			{Name: "b1-live-deploy-proof-contract", Status: "pass"},
			{Name: "b2-operator-install-path-contract", Status: "pass"},
			{Name: "b4-canonical-proof-packet-contract", Status: "pass"},
			{Name: phase3HostDrillMetadataEvidenceName, Status: "pass"},
			{Name: phase3DrillReservationsEvidenceName, Status: "pass"},
			{Name: "drill-execution", Status: "pass"},
		},
	}
	manifest := []byte("manifest-bytes")
	enriched := enrichReleaseSummary(base, manifest)

	if enriched.Extended == nil {
		t.Fatalf("Extended = nil, want populated extended summary")
	}
	if enriched.Extended.Verdict != readinessReady {
		t.Fatalf("Verdict = %q, want %q", enriched.Extended.Verdict, readinessReady)
	}
	if enriched.Extended.ManifestHash == "" || len(enriched.Extended.ManifestHash) != 64 {
		t.Fatalf("ManifestHash = %q, want 64-char hex", enriched.Extended.ManifestHash)
	}
	if enriched.Extended.ManifestHash != manifestSHA256Hex(manifest) {
		t.Fatalf("ManifestHash = %q, want stable sha256", enriched.Extended.ManifestHash)
	}
	if len(enriched.Extended.Lanes) != 8 {
		t.Fatalf("Lanes = %d, want 8 lanes", len(enriched.Extended.Lanes))
	}
	for _, lane := range enriched.Extended.Lanes {
		if lane.Status != "ready" {
			t.Fatalf("lane %s = %q, want ready", lane.Lane, lane.Status)
		}
	}
	if len(enriched.Extended.BlockingReasons) != 0 {
		t.Fatalf("BlockingReasons = %+v, want none", enriched.Extended.BlockingReasons)
	}

	data, err := json.Marshal(enriched)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	for _, want := range []string{"\"verdict\":\"ready\"", "\"manifestHash\"", "\"lanes\":["} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("Marshal() = %s, want %q", data, want)
		}
	}
}

func TestEnrichReleaseSummaryReportsBlockingReasonsAndNotReady(t *testing.T) {
	base := releaseSummary{
		ReleaseReady: false,
		GateCounts: releaseSummaryGateCounts{
			DrillRows:             19,
			AlertRules:            8,
			AlertValidationPassed: false,
		},
		ExecutedDrillCounts:             releaseSummaryExecutedDrillCounts{Assertions: 95, Passed: 90, Failed: 5},
		EvidenceIndexVerificationStatus: "fail",
		EvidencePacketItemStatuses: []releaseSummaryEvidenceItem{
			{Name: "drill-manifest", Status: "fail"},
		},
	}
	enriched := enrichReleaseSummary(base, []byte("x"))
	if enriched.Extended.Verdict != readinessNotReady {
		t.Fatalf("Verdict = %q, want %q", enriched.Extended.Verdict, readinessNotReady)
	}
	codes := make(map[string]bool)
	for _, r := range enriched.Extended.BlockingReasons {
		codes[r.ReasonCode] = true
	}
	for _, want := range []string{"phase0.gate.release-not-ready", "phase0.gate.alert-validation", "phase0.gate.drill-failure", "phase0.gate.evidence-index-verification", "phase0.gate.lane-not-ready"} {
		if !codes[want] {
			t.Fatalf("BlockingReasons missing %q: %+v", want, enriched.Extended.BlockingReasons)
		}
	}
}

func TestEnrichReleaseSummaryTreatsPresentB8PromotionReadinessAsBlocking(t *testing.T) {
	base := releaseSummary{
		ReleaseReady: true,
		GateCounts: releaseSummaryGateCounts{
			DrillRows:             19,
			AlertRules:            8,
			AlertValidationPassed: true,
			DrillStatuses:         map[string]int{"placeholder": 19},
		},
		ExecutedDrillCounts:             releaseSummaryExecutedDrillCounts{Assertions: 95, Passed: 95},
		EvidenceIndexVerificationStatus: "pass",
		EvidencePacketItemStatuses: []releaseSummaryEvidenceItem{
			{Name: "drill-manifest", Status: "pass"},
			{Name: "prometheus-alerts", Status: "pass"},
			{Name: "b1-live-deploy-proof-contract", Status: "pass"},
			{Name: "b2-operator-install-path-contract", Status: "pass"},
			{Name: phase3HostDrillMetadataEvidenceName, Status: "pass"},
			{Name: phase3DrillReservationsEvidenceName, Status: "pass"},
			{Name: "drill-execution", Status: "pass"},
			{Name: phase3DrillPromotionReadinessEvidenceName, Status: "not-ready"},
		},
	}

	enriched := enrichReleaseSummary(base, []byte("manifest"))
	if enriched.Extended.Verdict != readinessNotReady {
		t.Fatalf("Verdict = %q, want %q", enriched.Extended.Verdict, readinessNotReady)
	}
	var s08 laneStatusBlock
	for _, lane := range enriched.Extended.Lanes {
		if lane.Lane == "S08" {
			s08 = lane
		}
	}
	if s08.Status != "not-ready" || !containsSummaryString(s08.MissingItems, phase3DrillPromotionReadinessEvidenceName) {
		t.Fatalf("S08 lane = %+v, want B8 promotion readiness blocker", s08)
	}
}

func TestEnrichReleaseSummaryTreatsPresentB9ArchiveReadinessAsBlocking(t *testing.T) {
	base := releaseSummary{
		ReleaseReady: true,
		GateCounts: releaseSummaryGateCounts{
			DrillRows:             19,
			AlertRules:            8,
			AlertValidationPassed: true,
			DrillStatuses:         map[string]int{"placeholder": 19},
		},
		ExecutedDrillCounts:             releaseSummaryExecutedDrillCounts{Assertions: 95, Passed: 95},
		EvidenceIndexVerificationStatus: "pass",
		EvidencePacketItemStatuses: []releaseSummaryEvidenceItem{
			{Name: "drill-manifest", Status: "pass"},
			{Name: "prometheus-alerts", Status: "pass"},
			{Name: "b1-live-deploy-proof-contract", Status: "pass"},
			{Name: "b2-operator-install-path-contract", Status: "pass"},
			{Name: phase3HostDrillMetadataEvidenceName, Status: "pass"},
			{Name: phase3DrillReservationsEvidenceName, Status: "pass"},
			{Name: "drill-execution", Status: "pass"},
			{Name: phase0ReleaseEvidenceArchiveEvidenceName, Status: "not-ready"},
		},
	}

	enriched := enrichReleaseSummary(base, []byte("manifest"))
	if enriched.Extended.Verdict != readinessNotReady {
		t.Fatalf("Verdict = %q, want %q", enriched.Extended.Verdict, readinessNotReady)
	}
	var s08 laneStatusBlock
	for _, lane := range enriched.Extended.Lanes {
		if lane.Lane == "S08" {
			s08 = lane
		}
	}
	if s08.Status != "not-ready" || !containsSummaryString(s08.MissingItems, phase0ReleaseEvidenceArchiveEvidenceName) {
		t.Fatalf("S08 lane = %+v, want B9 archive readiness blocker", s08)
	}
}

func TestReleaseSummaryMarshalJSONOmitsExtendedWhenNil(t *testing.T) {
	base := releaseSummary{ReleaseReady: true}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "verdict") || strings.Contains(string(data), "manifestHash") {
		t.Fatalf("Marshal() = %s, want no extended fields when Extended is nil", data)
	}
}

func TestManifestSHA256HexIsDeterministic(t *testing.T) {
	if manifestSHA256Hex([]byte("abc")) != manifestSHA256Hex([]byte("abc")) {
		t.Fatal("manifestSHA256Hex() not deterministic")
	}
	if manifestSHA256Hex([]byte("abc")) == manifestSHA256Hex([]byte("abd")) {
		t.Fatal("manifestSHA256Hex() collisions for trivial inputs")
	}
}

func containsSummaryString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
