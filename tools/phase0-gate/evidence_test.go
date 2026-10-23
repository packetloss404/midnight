package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
}

func TestEvidencePacketJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packet.json")
	want := evidencePacket{
		LastGreen: lastGreenEvidence{Status: "abc123", Source: "flag"},
		Items: []evidenceItem{
			{Name: "drill-manifest", Status: "pass", Path: "test/drills/manifest.yaml"},
		},
	}

	if err := writeEvidencePacket(path, want); err != nil {
		t.Fatalf("writeEvidencePacket() error = %v", err)
	}
	got, err := readEvidencePacket(path)
	if err != nil {
		t.Fatalf("readEvidencePacket() error = %v", err)
	}
	if got.LastGreen.Status != want.LastGreen.Status || got.LastGreen.Source != want.LastGreen.Source || len(got.Items) != 1 || got.Items[0].Name != "drill-manifest" {
		t.Fatalf("readEvidencePacket() = %+v, want %+v", got, want)
	}
}

func TestWriteEvidencePacketRequiresJSONPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packet.txt")

	if err := writeEvidencePacket(path, evidencePacket{}); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("writeEvidencePacket() error = %v, want .json path error", err)
	}
}

func TestWriteReleaseEvidenceRequiresJSONPathAndExistingParent(t *testing.T) {
	if err := writeReleaseEvidence(filepath.Join(t.TempDir(), "evidence.txt"), releaseEvidence{}); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("writeReleaseEvidence() error = %v, want .json path error", err)
	}
	missingParent := filepath.Join(t.TempDir(), "missing", "evidence.json")
	if err := writeReleaseEvidence(missingParent, releaseEvidence{}); err == nil || !strings.Contains(err.Error(), "parent does not exist") {
		t.Fatalf("writeReleaseEvidence() error = %v, want missing parent error", err)
	}
}

func TestWriteReleaseSummaryRequiresJSONPathAndExistingParent(t *testing.T) {
	if err := writeReleaseSummary(filepath.Join(t.TempDir(), "summary.txt"), releaseSummary{}); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("writeReleaseSummary() error = %v, want .json path error", err)
	}
	missingParent := filepath.Join(t.TempDir(), "missing", "summary.json")
	if err := writeReleaseSummary(missingParent, releaseSummary{}); err == nil || !strings.Contains(err.Error(), "parent does not exist") {
		t.Fatalf("writeReleaseSummary() error = %v, want missing parent error", err)
	}
}

func TestReleaseEvidenceArchiveIndexIncludesDefaultAndExtraRefs(t *testing.T) {
	index, err := buildReleaseEvidenceArchiveIndex("artifacts/phase0/release.json", []evidenceRef{
		{Name: "operator-notes", Path: "docs/release/operator-notes.json"},
	})
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	if len(index.Entries) != 4 {
		t.Fatalf("Entries length = %d, want release evidence, defaults, and extra ref", len(index.Entries))
	}
	if index.Entries[0].Name != "gate-release-evidence" || index.Entries[0].Role != "release-evidence" {
		t.Fatalf("Entries[0] = %+v, want release evidence entry", index.Entries[0])
	}
	if index.Entries[1].Path != "test/drills/manifest.yaml" || index.Entries[2].Path != "deploy/prometheus/alerts.yaml" || index.Entries[3].Name != "operator-notes" {
		t.Fatalf("Entries = %+v, want default relative refs plus extra ref", index.Entries)
	}

	path := filepath.Join(t.TempDir(), "index.json")
	if err := writeReleaseEvidenceArchiveIndex(path, index); err != nil {
		t.Fatalf("writeReleaseEvidenceArchiveIndex() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var decoded struct {
		Entries []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Role   string `json:"role"`
			SHA256 string `json:"sha256,omitempty"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("archive index JSON did not unmarshal: %v", err)
	}
	if len(decoded.Entries) != 4 || decoded.Entries[3].Path != "docs/release/operator-notes.json" {
		t.Fatalf("decoded archive index = %+v, want extra ref", decoded)
	}
	if decoded.Entries[3].SHA256 != "" {
		t.Fatalf("decoded archive index sha256 = %q, want omitted for entries without hash", decoded.Entries[3].SHA256)
	}
}

func TestReleaseEvidenceArchiveIndexRejectsUnsafeRefs(t *testing.T) {
	for _, ref := range []evidenceRef{
		{Name: "absolute", Path: os.TempDir()},
		{Name: "escape", Path: "../evidence.json"},
		{Name: "bad/name", Path: "docs/release/evidence.json"},
	} {
		if _, err := buildReleaseEvidenceArchiveIndex("", []evidenceRef{ref}); err == nil {
			t.Fatalf("buildReleaseEvidenceArchiveIndex(%+v) error = nil, want unsafe ref error", ref)
		}
	}
}

func TestReleaseEvidenceArchiveIndexRejectsUnsafeReleaseEvidencePath(t *testing.T) {
	for _, path := range []string{
		filepath.Join(t.TempDir(), "release.json"),
		"../release.json",
		"artifacts/phase0/release.txt",
	} {
		if _, err := buildReleaseEvidenceArchiveIndex(path, nil); err == nil {
			t.Fatalf("buildReleaseEvidenceArchiveIndex(%q) error = nil, want unsafe release evidence path error", path)
		}
	}
}

func TestVerifyReleaseEvidenceArchiveIndexRequiresRefsAndRejectsUnsafePaths(t *testing.T) {
	tempDir := t.TempDir()
	validIndex, err := buildReleaseEvidenceArchiveIndex("out/phase0-release-evidence.json", nil)
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	validPath := filepath.Join(tempDir, "index.json")
	if err := writeReleaseEvidenceArchiveIndex(validPath, validIndex); err != nil {
		t.Fatalf("writeReleaseEvidenceArchiveIndex() error = %v", err)
	}

	verification := verifyReleaseEvidenceArchiveIndexFile(validPath, false)
	if verification.Status != "pass" || verification.Entries != 3 || len(verification.RequiredRefs) != 3 || verification.Error != "" {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want pass with required refs", verification)
	}

	missingGate := validIndex
	missingGate.Entries = missingGate.Entries[1:]
	missingPath := filepath.Join(tempDir, "missing-gate.json")
	writeRawReleaseEvidenceIndexForTest(t, missingPath, missingGate)
	verification = verifyReleaseEvidenceArchiveIndexFile(missingPath, false)
	if verification.Status != "fail" || !strings.Contains(verification.Error, "gate-release-evidence") {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want missing gate release evidence failure", verification)
	}

	unsafeGate := validIndex
	unsafeGate.Entries[0].Path = "../phase0-release-evidence.json"
	unsafePath := filepath.Join(tempDir, "unsafe-gate.json")
	writeRawReleaseEvidenceIndexForTest(t, unsafePath, unsafeGate)
	verification = verifyReleaseEvidenceArchiveIndexFile(unsafePath, false)
	if verification.Status != "fail" || !strings.Contains(verification.Error, "must not escape workspace") {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want unsafe gate path failure", verification)
	}
}

func TestVerifyReleaseEvidenceArchiveIndexCanCheckReferencedFiles(t *testing.T) {
	tempDir := t.TempDir()
	index, err := buildReleaseEvidenceArchiveIndex("out/phase0-release-evidence.json", nil)
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	indexPath := filepath.Join(tempDir, "index.json")
	if err := writeReleaseEvidenceArchiveIndex(indexPath, index); err != nil {
		t.Fatalf("writeReleaseEvidenceArchiveIndex() error = %v", err)
	}
	for _, dir := range []string{"out", filepath.Join("test", "drills"), filepath.Join("deploy", "prometheus")} {
		if err := os.MkdirAll(filepath.Join(tempDir, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	for _, path := range []string{"out/phase0-release-evidence.json", "test/drills/manifest.yaml", "deploy/prometheus/alerts.yaml"} {
		if err := os.WriteFile(filepath.Join(tempDir, filepath.FromSlash(path)), []byte("evidence\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	chdirForTest(t, tempDir)

	verification := verifyReleaseEvidenceArchiveIndexFile(indexPath, true)
	if verification.Status != "pass" || !verification.FilesChecked || verification.Error != "" {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want pass with file checks", verification)
	}

	if err := os.Remove(filepath.Join(tempDir, "out", "phase0-release-evidence.json")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	verification = verifyReleaseEvidenceArchiveIndexFile(indexPath, false)
	if verification.Status != "pass" || verification.FilesChecked {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want metadata-only pass", verification)
	}
	verification = verifyReleaseEvidenceArchiveIndexFile(indexPath, true)
	if verification.Status != "fail" || !verification.FilesChecked || !strings.Contains(verification.Error, "does not exist") {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want missing referenced file failure", verification)
	}
}

func TestVerifyReleaseEvidenceArchiveIndexComparesOptionalSHA256(t *testing.T) {
	tempDir := t.TempDir()
	index, err := buildReleaseEvidenceArchiveIndex("out/phase0-release-evidence.json", nil)
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	for _, dir := range []string{"out", filepath.Join("test", "drills"), filepath.Join("deploy", "prometheus")} {
		if err := os.MkdirAll(filepath.Join(tempDir, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	for i, entry := range index.Entries {
		data := []byte(entry.Name + "\n")
		if err := os.WriteFile(filepath.Join(tempDir, filepath.FromSlash(entry.Path)), data, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		sum := sha256.Sum256(data)
		index.Entries[i].SHA256 = fmt.Sprintf("%x", sum)
	}
	indexPath := filepath.Join(tempDir, "index.json")
	writeRawReleaseEvidenceIndexForTest(t, indexPath, index)
	chdirForTest(t, tempDir)

	verification := verifyReleaseEvidenceArchiveIndexFile(indexPath, true)
	if verification.Status != "pass" || !verification.FilesChecked || verification.Error != "" {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want pass with sha256 checks", verification)
	}

	if err := os.WriteFile(filepath.Join(tempDir, "out", "phase0-release-evidence.json"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	verification = verifyReleaseEvidenceArchiveIndexFile(indexPath, true)
	if verification.Status != "fail" || !strings.Contains(verification.Error, "sha256 mismatch") {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want sha256 mismatch failure", verification)
	}

	verification = verifyReleaseEvidenceArchiveIndexFile(indexPath, false)
	if verification.Status != "pass" || verification.FilesChecked {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want metadata-only pass without sha256 comparison", verification)
	}
}

func TestVerifyReleaseEvidenceArchiveIndexRejectsInvalidSHA256(t *testing.T) {
	index, err := buildReleaseEvidenceArchiveIndex("out/phase0-release-evidence.json", nil)
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	index.Entries[0].SHA256 = "not-a-sha256"
	path := filepath.Join(t.TempDir(), "index.json")
	writeRawReleaseEvidenceIndexForTest(t, path, index)

	verification := verifyReleaseEvidenceArchiveIndexFile(path, false)
	if verification.Status != "fail" || !strings.Contains(verification.Error, "invalid sha256") {
		t.Fatalf("verifyReleaseEvidenceArchiveIndexFile() = %+v, want invalid sha256 failure", verification)
	}
}

func writeRawReleaseEvidenceIndexForTest(t *testing.T, path string, index releaseEvidenceArchiveIndex) {
	t.Helper()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestWriteReleaseEvidenceArchiveIndexRequiresJSONPath(t *testing.T) {
	index, err := buildReleaseEvidenceArchiveIndex("", nil)
	if err != nil {
		t.Fatalf("buildReleaseEvidenceArchiveIndex() error = %v", err)
	}
	if err := writeReleaseEvidenceArchiveIndex(filepath.Join(t.TempDir(), "index.txt"), index); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("writeReleaseEvidenceArchiveIndex() error = %v, want .json path error", err)
	}
}

func TestBuildReleaseEvidenceIncludesRequiredExecutionAndReadiness(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	report, err := buildGateReportWithDrillExecution(manifestData, validAlertRules(), "abc123", localDrillExecutionAdapter{manifestData: manifestData, alertData: validAlertRules()}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	evidence := buildReleaseEvidence(report, cliModeFlags{Format: "json", LastGreen: "abc123", DrillAdapter: "local", ExecuteDrills: true, RequireExecutedDrills: true, ReleaseEvidence: "release.json"}, true)

	if evidence.GeneratedAt.Value != releaseEvidenceGeneratedAt || evidence.GeneratedAt.Source == "" {
		t.Fatalf("GeneratedAt = %+v, want deterministic placeholder with source", evidence.GeneratedAt)
	}
	if evidence.RequiredDrillExecution == nil || evidence.RequiredDrillExecution.Adapter != "local" || evidence.RequiredDrillExecution.AdapterType != "local-safe" {
		t.Fatalf("RequiredDrillExecution = %+v, want local execution summary", evidence.RequiredDrillExecution)
	}
	if evidence.ReleaseReady == nil || !*evidence.ReleaseReady {
		t.Fatalf("ReleaseReady = %v, want true", evidence.ReleaseReady)
	}
	if evidence.EvidencePacket.LastGreen.Status != "abc123" || len(evidence.EvidencePacket.Items) != 7 || evidenceItemStatus(evidence.EvidencePacket, "b1-live-deploy-proof-contract") != "pass" || evidenceItemStatus(evidence.EvidencePacket, "b2-operator-install-path-contract") != "pass" || evidenceItemStatus(evidence.EvidencePacket, "b4-canonical-proof-packet-contract") != "pass" || evidenceItemStatus(evidence.EvidencePacket, phase3HostDrillMetadataEvidenceName) != "pass" {
		t.Fatalf("EvidencePacket = %+v, want copied packet", evidence.EvidencePacket)
	}
	if evidence.CanonicalProofPacket == nil || evidence.CanonicalProofPacket.Command == "" || evidence.CanonicalProofPacket.RunnerOS == "" || evidence.CanonicalProofPacket.CommitSHA != "abc123" {
		t.Fatalf("CanonicalProofPacket = %+v, want B4 proof packet fields", evidence.CanonicalProofPacket)
	}

	path := filepath.Join(t.TempDir(), "release.json")
	if err := writeReleaseEvidence(path, evidence); err != nil {
		t.Fatalf("writeReleaseEvidence() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var decoded struct {
		ReleaseReady           bool `json:"releaseReady"`
		RequiredDrillExecution struct {
			Adapter     string `json:"adapter"`
			AdapterType string `json:"adapterType"`
		} `json:"requiredDrillExecution"`
		ModeFlags struct {
			Format                string `json:"format"`
			DrillAdapter          string `json:"drillAdapter"`
			RequireExecutedDrills bool   `json:"requireExecutedDrills"`
		} `json:"modeFlags"`
		GateReport struct {
			DrillRows       int `json:"drillRows"`
			LiveDeployProof struct {
				ID string `json:"id"`
			} `json:"liveDeployProof"`
			OperatorInstallPath struct {
				ID string `json:"id"`
			} `json:"operatorInstallPath"`
		} `json:"gateReport"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("release evidence JSON did not unmarshal: %v", err)
	}
	if !decoded.ReleaseReady || decoded.RequiredDrillExecution.Adapter != "local" || decoded.RequiredDrillExecution.AdapterType != "local-safe" || decoded.ModeFlags.Format != "json" || decoded.ModeFlags.DrillAdapter != "local" || !decoded.ModeFlags.RequireExecutedDrills || decoded.GateReport.DrillRows != 19 || decoded.GateReport.LiveDeployProof.ID != liveDeployProofID || decoded.GateReport.OperatorInstallPath.ID != operatorInstallPathID {
		t.Fatalf("decoded release evidence = %+v, want required execution evidence", decoded)
	}
}

func TestBuildReleaseEvidenceMarksRequiredChecksNotReady(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	report, err := buildGateReportWithDrillExecution(manifestData, []byte("not prometheus alerts"), "unknown", localDrillExecutionAdapter{manifestData: manifestData, alertData: []byte("not prometheus alerts")}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	evidence := buildReleaseEvidence(report, cliModeFlags{Format: "json", ExecuteDrills: true, RequireExecutedDrills: true}, true)

	if evidence.ReleaseReady == nil || *evidence.ReleaseReady {
		t.Fatalf("ReleaseReady = %v, want false for failed required checks", evidence.ReleaseReady)
	}
}

func TestBuildReleaseSummaryIncludesConciseDeterministicStatus(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	report, err := buildGateReportWithDrillExecution(manifestData, validAlertRules(), "abc123", localDrillExecutionAdapter{manifestData: manifestData, alertData: validAlertRules()}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	report.ReleaseEvidenceIndexVerification = &releaseEvidenceIndexVerification{Path: "out/index.json", Status: "pass", Entries: 3}
	report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: "release-evidence-index", Status: "pass", Path: "out/index.json"})

	summary := buildReleaseSummary(report)
	if !summary.ReleaseReady || summary.GateCounts.DrillRows != 19 || summary.GateCounts.DrillStatuses["placeholder"] != 19 || summary.GateCounts.AlertRules == 0 || !summary.GateCounts.AlertValidationPassed {
		t.Fatalf("buildReleaseSummary() gate fields = %+v, want ready gate counts", summary)
	}
	if summary.ExecutedDrillCounts.Assertions != 95 || summary.ExecutedDrillCounts.Passed != 95 || summary.ExecutedDrillCounts.Failed != 0 || summary.ExecutedDrillCounts.Skipped != 0 {
		t.Fatalf("ExecutedDrillCounts = %+v, want local execution counts", summary.ExecutedDrillCounts)
	}
	if summary.EvidenceIndexVerificationStatus != "pass" || len(summary.EvidencePacketItemStatuses) != 8 || summary.EvidencePacketItemStatuses[7].Name != "release-evidence-index" || summary.EvidencePacketItemStatuses[2].Name != "b1-live-deploy-proof-contract" || summary.EvidencePacketItemStatuses[3].Name != "b2-operator-install-path-contract" || summary.EvidencePacketItemStatuses[4].Name != "b4-canonical-proof-packet-contract" || summary.EvidencePacketItemStatuses[5].Name != phase3HostDrillMetadataEvidenceName {
		t.Fatalf("buildReleaseSummary() evidence fields = %+v, want index and packet statuses", summary)
	}

	path := filepath.Join(t.TempDir(), "summary.json")
	if err := writeReleaseSummary(path, summary); err != nil {
		t.Fatalf("writeReleaseSummary() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var decoded releaseSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("release summary JSON did not unmarshal: %v", err)
	}
	if !decoded.ReleaseReady || decoded.EvidenceIndexVerificationStatus != "pass" || decoded.ExecutedDrillCounts.Assertions != 95 {
		t.Fatalf("decoded release summary = %+v, want concise status", decoded)
	}
}

func TestBuildReleaseSummaryDefaultsWhenDrillsAndIndexNotRun(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	summary := buildReleaseSummary(report)

	if summary.ReleaseReady || summary.EvidenceIndexVerificationStatus != "not-run" || summary.ExecutedDrillCounts.Assertions != 0 || len(summary.EvidencePacketItemStatuses) != 6 || summary.EvidencePacketItemStatuses[2].Name != "b1-live-deploy-proof-contract" || summary.EvidencePacketItemStatuses[3].Name != "b2-operator-install-path-contract" || summary.EvidencePacketItemStatuses[4].Name != "b4-canonical-proof-packet-contract" || summary.EvidencePacketItemStatuses[5].Name != phase3HostDrillMetadataEvidenceName {
		t.Fatalf("buildReleaseSummary() = %+v, want not ready defaults without executed drills or index verification", summary)
	}
}

func TestBuildReleaseEvidenceOmitsReleaseReadyOutsideRequireMode(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	evidence := buildReleaseEvidence(report, cliModeFlags{Format: "text"}, false)

	if evidence.ReleaseReady != nil || evidence.RequiredDrillExecution != nil {
		t.Fatalf("release evidence = %+v, want require-only fields omitted", evidence)
	}
}
