package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateCLIOptionsRequiresExecuteDrills(t *testing.T) {
	if err := validateCLIOptions(false, true, "local", "", "", "", "", false, nil); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want --execute-drills requirement")
	}
	if err := validateCLIOptions(true, true, "local", "", "", "", "", false, nil); err != nil {
		t.Fatalf("validateCLIOptions() error = %v, want valid required execution options", err)
	}
}

func TestValidateCLIOptionsRejectsLiveDrillAdapter(t *testing.T) {
	if err := validateCLIOptions(true, false, "live", "", "", "", "", false, nil); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want live adapter guard")
	}
	if err := validateCLIOptions(true, false, "bogus", "", "", "", "", false, nil); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want unsupported adapter error")
	}
}

func TestValidateCLIOptionsRejectsEvidenceRefsWithoutIndex(t *testing.T) {
	if err := validateCLIOptions(false, false, "local", "", "", "", "", false, []evidenceRef{{Name: "runbook", Path: "docs/release/runbook.json"}}); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want --release-evidence-index requirement")
	}
}

func TestValidateCLIOptionsRejectsAbsoluteReleaseEvidenceInArchiveIndex(t *testing.T) {
	tempDir := t.TempDir()
	indexPath := filepath.Join(tempDir, "index.json")
	releaseEvidencePath := filepath.Join(tempDir, "release.json")

	if err := validateCLIOptions(false, false, "local", releaseEvidencePath, "", indexPath, "", false, nil); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want workspace-relative release evidence path when archive index is written")
	}
}

func TestValidateCLIOptionsRejectsFileVerificationWithoutIndexVerification(t *testing.T) {
	if err := validateCLIOptions(false, false, "local", "", "", "", "", true, nil); err == nil {
		t.Fatal("validateCLIOptions() error = nil, want --verify-release-evidence-index requirement")
	}
}

func TestCanonicalReleaseCommandSucceedsWithRelativeArtifactsPath(t *testing.T) {
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	workspace := t.TempDir()
	for _, dir := range []string{
		filepath.Join("artifacts", "phase0"),
		filepath.Join("deploy", "prometheus"),
		filepath.Join("test", "drills"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "deploy", "prometheus", "alerts.yaml"), validAlertRules(), 0o644); err != nil {
		t.Fatalf("WriteFile(alerts) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "test", "drills", "manifest.yaml"), []byte(validDrillManifest()), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "artifacts", "phase0", "operator-notes.md"), []byte("operator notes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(operator notes) error = %v", err)
	}

	binaryPath := filepath.Join(workspace, "phase0-gate-test.exe")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = packageDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	cmd := exec.Command(binaryPath,
		"--format=json",
		"--last-green", "abc123",
		"--execute-drills",
		"--require-executed-drills",
		"--release-evidence", "artifacts/phase0/phase0-release-evidence.json",
		"--release-evidence-index", "artifacts/phase0/phase0-release-evidence-index.json",
		"--verify-release-evidence-index", "artifacts/phase0/phase0-release-evidence-index.json",
		"--verify-evidence-files",
		"--release-summary", "artifacts/phase0/phase0-release-summary.json",
		"--evidence-ref", "operator-notes=artifacts/phase0/operator-notes.md",
	)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("canonical release command failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		"artifacts/phase0/phase0-release-evidence.json",
		"artifacts/phase0/phase0-release-evidence-index.json",
		"artifacts/phase0/phase0-release-summary.json",
	} {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(path))); err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
	}
	evidenceData, err := os.ReadFile(filepath.Join(workspace, "artifacts", "phase0", "phase0-release-evidence.json"))
	if err != nil {
		t.Fatalf("ReadFile(release evidence) error = %v", err)
	}
	var evidence releaseEvidence
	if err := json.Unmarshal(evidenceData, &evidence); err != nil {
		t.Fatalf("Unmarshal(release evidence) error = %v", err)
	}
	if evidence.CanonicalProofPacket == nil || evidence.CanonicalProofPacket.RunnerOS != runtime.GOOS || evidence.CanonicalProofPacket.CommitSHA != "abc123" || evidence.CanonicalProofPacket.OperatorNotesRef != "artifacts/phase0/operator-notes.md" || evidence.CanonicalProofPacket.VerificationStatus != "pass" {
		t.Fatalf("CanonicalProofPacket = %+v, want canonical command proof fields", evidence.CanonicalProofPacket)
	}
	summaryData, err := os.ReadFile(filepath.Join(workspace, "artifacts", "phase0", "phase0-release-summary.json"))
	if err != nil {
		t.Fatalf("ReadFile(release summary) error = %v", err)
	}
	var summary releaseSummary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("Unmarshal(release summary) error = %v", err)
	}
	if summary.CanonicalProofPacket == nil || summary.CanonicalProofPacket.ReleaseEvidenceRef != "artifacts/phase0/phase0-release-evidence.json" || summary.CanonicalProofPacket.ReleaseEvidenceIndexRef != "artifacts/phase0/phase0-release-evidence-index.json" || summary.CanonicalProofPacket.ReleaseSummaryRef != "artifacts/phase0/phase0-release-summary.json" {
		t.Fatalf("summary CanonicalProofPacket = %+v, want release artifact refs", summary.CanonicalProofPacket)
	}
}

func TestShouldFailForRequiredLocalAdapterFailures(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	report, err := buildGateReportWithDrillExecution(manifestData, []byte("not prometheus alerts"), "unknown", localDrillExecutionAdapter{manifestData: manifestData, alertData: []byte("not prometheus alerts")}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if !shouldFailForDrillExecution(report, true) {
		t.Fatalf("shouldFailForDrillExecution() = false, want true for failed local drill assertions")
	}
}

func TestShouldFailForRequiredIndexVerificationFailure(t *testing.T) {
	report := gateReport{ReleaseEvidenceIndexVerification: &releaseEvidenceIndexVerification{Status: "fail"}}

	if !shouldFailForRequiredChecks(report, true) {
		t.Fatal("shouldFailForRequiredChecks() = false, want required failed index verification to fail")
	}
	if shouldFailForRequiredChecks(report, false) {
		t.Fatal("shouldFailForRequiredChecks() = true, want non-required failed index verification to remain non-blocking")
	}
}

func TestShouldFailForRequiredSkippedOrBlockedDrills(t *testing.T) {
	report, err := buildGateReportWithDrillExecution([]byte(validDrillManifest()), validAlertRules(), "unknown", placeholderDrillExecutionAdapter{}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if !shouldFailForDrillExecution(report, true) {
		t.Fatalf("shouldFailForDrillExecution() = false, want true for skipped/blocked drills")
	}
}

func TestShouldKeepDefaultDrillExecutionFailureBehavior(t *testing.T) {
	report, err := buildGateReportWithDrillExecution([]byte(validDrillManifest()), validAlertRules(), "unknown", placeholderDrillExecutionAdapter{}, false)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if shouldFailForDrillExecution(report, false) {
		t.Fatalf("shouldFailForDrillExecution() = true, want default skipped drills to remain non-blocking")
	}
}

// quietModeWorkspace creates a temporary workspace seeded with the locked
// drill manifest and alert rules, runs `f` from that working directory, and
// returns the captured stdout, stderr, and exit code from a phase0-gate run.
func quietModeWorkspace(t *testing.T, inputs gateInputs) (string, string, ExitCode) {
	t.Helper()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	workspace := t.TempDir()
	for _, dir := range []string{
		filepath.Join("artifacts", "phase0"),
		filepath.Join("deploy", "prometheus"),
		filepath.Join("test", "drills"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "deploy", "prometheus", "alerts.yaml"), validAlertRules(), 0o644); err != nil {
		t.Fatalf("WriteFile(alerts) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "test", "drills", "manifest.yaml"), []byte(validDrillManifest()), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalDir)
	})
	var stdout, stderr bytes.Buffer
	exit := run(inputs, &stdout, &stderr)
	return stdout.String(), stderr.String(), exit
}

func TestQuietSuppressesSuccessOutputForAllFormats(t *testing.T) {
	for _, format := range []string{"text", "markdown", "json"} {
		t.Run(format, func(t *testing.T) {
			stdout, stderr, exit := quietModeWorkspace(t, gateInputs{Format: format, Quiet: true, LastGreen: "unknown"})
			if exit != ExitOK {
				t.Fatalf("run() exit = %s, want ExitOK; stderr=%s", exit, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty in quiet mode", stdout)
			}
		})
	}
}

func TestQuietStillPrintsOnFailure(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	workspace := t.TempDir()
	for _, dir := range []string{
		filepath.Join("deploy", "prometheus"),
		filepath.Join("test", "drills"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	// Invalid alert rules force the local drill adapter to surface a failed
	// alert-validation assertion, which trips the require-executed-drills gate.
	if err := os.WriteFile(filepath.Join(workspace, "deploy", "prometheus", "alerts.yaml"), []byte("not prometheus alerts"), 0o644); err != nil {
		t.Fatalf("WriteFile(alerts) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "test", "drills", "manifest.yaml"), []byte(validDrillManifest()), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	var stdout, stderr bytes.Buffer
	exit := run(gateInputs{
		Format: "text", Quiet: true, LastGreen: "unknown",
		ExecuteDrills: true, RequireExecutedDrills: true,
	}, &stdout, &stderr)
	if exit == ExitOK {
		t.Fatalf("run() exit = %s, want non-OK; stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatalf("stdout empty in quiet mode on failure: stderr=%s", stderr.String())
	}
}

func TestNonQuietPrintsSuccessOutput(t *testing.T) {
	stdout, _, exit := quietModeWorkspace(t, gateInputs{Format: "text", LastGreen: "unknown"})
	if exit != ExitOK {
		t.Fatalf("run() exit = %s, want ExitOK", exit)
	}
	if stdout == "" {
		t.Fatal("stdout = empty, want rendered report when quiet is unset")
	}
}
