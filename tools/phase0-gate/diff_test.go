package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestDiffReleaseEvidenceComputesAdditionsRemovalsRegressions(t *testing.T) {
	left := releaseEvidence{
		ReleaseReady: boolPtr(true),
		GateReport: gateReport{
			DrillRows:  19,
			AlertRules: 8,
			DrillStatuses: map[string]int{
				"placeholder": 19,
			},
			DrillExecution: &drillExecutionSummary{Adapter: "local", Assertions: 95, Passed: 95, Failed: 0, Skipped: 0},
			EvidencePacket: evidencePacket{Items: []evidenceItem{
				{Name: "drill-manifest", Status: "pass"},
				{Name: "prometheus-alerts", Status: "pass"},
				{Name: "drill-execution", Status: "pass"},
			}},
		},
		EvidencePacket: evidencePacket{Items: []evidenceItem{
			{Name: "drill-manifest", Status: "pass"},
			{Name: "prometheus-alerts", Status: "pass"},
			{Name: "drill-execution", Status: "pass"},
		}},
	}
	right := releaseEvidence{
		ReleaseReady: boolPtr(false),
		GateReport: gateReport{
			DrillRows:  19,
			AlertRules: 9, // +1 alert added
			DrillStatuses: map[string]int{
				"placeholder": 18,
				"failed":      1,
			},
			DrillExecution: &drillExecutionSummary{Adapter: "local", Assertions: 95, Passed: 90, Failed: 5, Skipped: 0},
			EvidencePacket: evidencePacket{Items: []evidenceItem{
				{Name: "drill-manifest", Status: "pass"},
				{Name: "prometheus-alerts", Status: "fail"}, // regression
				{Name: "release-evidence-index", Status: "pass"},
				// drill-execution removed
			}},
		},
		EvidencePacket: evidencePacket{Items: []evidenceItem{
			{Name: "drill-manifest", Status: "pass"},
			{Name: "prometheus-alerts", Status: "fail"},
			{Name: "release-evidence-index", Status: "pass"},
		}},
	}
	diff := diffReleaseEvidence("left.json", "right.json", left, right)

	if !diff.ReleaseReady.Regressed {
		t.Fatalf("ReleaseReady = %+v, want regressed", diff.ReleaseReady)
	}
	if diff.GateCounts.AlertRules.Delta != 1 {
		t.Fatalf("AlertRules delta = %d, want 1", diff.GateCounts.AlertRules.Delta)
	}
	if len(diff.EvidencePacket.Additions) != 1 || diff.EvidencePacket.Additions[0] != "release-evidence-index" {
		t.Fatalf("Additions = %+v, want release-evidence-index added", diff.EvidencePacket.Additions)
	}
	if len(diff.EvidencePacket.Removals) != 1 || diff.EvidencePacket.Removals[0] != "drill-execution" {
		t.Fatalf("Removals = %+v, want drill-execution removed", diff.EvidencePacket.Removals)
	}
	if len(diff.EvidencePacket.Regressions) != 1 || diff.EvidencePacket.Regressions[0].Name != "prometheus-alerts" {
		t.Fatalf("Regressions = %+v, want prometheus-alerts regressed", diff.EvidencePacket.Regressions)
	}
	if diff.DrillExecution.Failed.Delta != 5 {
		t.Fatalf("Failed delta = %d, want 5", diff.DrillExecution.Failed.Delta)
	}
	wantRegressionPrefixes := []string{
		"drill execution failed count regressed",
		"drill execution passed count regressed",
		"drill status counter for \"failed\" regressed",
		"evidence packet item \"prometheus-alerts\" regressed",
		"releaseReady regressed from true to false or unset",
	}
	for _, want := range wantRegressionPrefixes {
		found := false
		for _, got := range diff.Regressions {
			if strings.Contains(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Regressions = %v, want entry containing %q", diff.Regressions, want)
		}
	}
}

func TestDiffReleaseEvidenceFilesRoundTripsJSON(t *testing.T) {
	tmp := t.TempDir()
	leftPath := filepath.Join(tmp, "left.json")
	rightPath := filepath.Join(tmp, "right.json")
	leftEvidence := releaseEvidence{
		GeneratedAt:  generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "test"},
		ReleaseReady: boolPtr(true),
		GateReport:   gateReport{DrillRows: 19, AlertRules: 8, DrillStatuses: map[string]int{"placeholder": 19}},
	}
	rightEvidence := leftEvidence
	rightEvidence.ReleaseReady = boolPtr(false)
	if err := writeReleaseEvidence(leftPath, leftEvidence); err != nil {
		t.Fatalf("writeReleaseEvidence(left) error = %v", err)
	}
	if err := writeReleaseEvidence(rightPath, rightEvidence); err != nil {
		t.Fatalf("writeReleaseEvidence(right) error = %v", err)
	}
	diff, err := diffReleaseEvidenceFiles(leftPath, rightPath)
	if err != nil {
		t.Fatalf("diffReleaseEvidenceFiles() error = %v", err)
	}
	if !diff.ReleaseReady.Regressed {
		t.Fatalf("ReleaseReady = %+v, want regression", diff.ReleaseReady)
	}
	data, err := diff.JSON()
	if err != nil {
		t.Fatalf("diff.JSON() error = %v", err)
	}
	if !strings.Contains(string(data), "\"regressions\"") {
		t.Fatalf("diff JSON missing regressions field: %s", data)
	}
}

func TestDiffReleaseEvidenceFilesRejectsMissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "left.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := diffReleaseEvidenceFiles(path, filepath.Join(tmp, "missing.json"))
	if err == nil {
		t.Fatal("diffReleaseEvidenceFiles() error = nil, want missing right error")
	}
}
