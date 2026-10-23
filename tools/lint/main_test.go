package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPassesWithRequiredScaffoldAndNpmLockPolicy(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "lint: ok") {
		t.Fatalf("stdout = %q, want ok message", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunReportsMissingRequiredPath(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	if err := os.Remove(filepath.Join(root, "Taskfile.yml")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "missing required scaffold path: Taskfile.yml") {
		t.Fatalf("stderr = %q, want missing Taskfile.yml", got)
	}
}

func TestRunRejectsNonNpmLockfile(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9'\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "forbidden npm policy lockfile present: pnpm-lock.yaml") {
		t.Fatalf("stderr = %q, want forbidden pnpm lockfile", got)
	}
}

func TestRunRejectsPullRequestTargetWorkflow(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", "on: pull_request_target\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "must not use pull_request_target") {
		t.Fatalf("stderr = %q, want pull_request_target failure", got)
	}
}

func TestRunRejectsReadAllWorkflowPermissions(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/test.yml", "name: test\n\non:\n  workflow_dispatch:\n\npermissions: read-all\n\njobs:\n  test:\n    name: test\n    runs-on: ubuntu-24.04\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "must not use permissions: read-all") {
		t.Fatalf("stderr = %q, want read-all permissions failure", got)
	}
}

func TestRunRejectsMissingWorkflowPermissions(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/test.yml", "name: test\n\non:\n  workflow_dispatch:\n\njobs:\n  test:\n    name: test\n    runs-on: ubuntu-24.04\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "missing explicit top-level permissions block") {
		t.Fatalf("stderr = %q, want missing permissions failure", got)
	}
}

func TestRunRejectsWriteAllWorkflowPermissions(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/build.yml", "name: build\n\non:\n  workflow_dispatch:\n\npermissions: write-all\n\njobs:\n  build:\n    name: build\n    runs-on: ubuntu-24.04\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "must not use permissions: write-all") {
		t.Fatalf("stderr = %q, want write-all permissions failure", got)
	}
}

func TestRunRejectsMissingRequiredWorkflowCheckName(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/supplychain.yml", workflow("supplychain", "govulncheck", "govulncheck", "osv-scanner", "osv-scanner"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows missing manual job/status check name(s): npm-audit") {
		t.Fatalf("stderr = %q, want missing npm-audit workflow check", got)
	}
}

func TestRunRejectsReleaseCheckWorkflowWithoutCommand(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/release-check.yml", workflow("release-check", "release-check", "release-check"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml must run: make release-check") {
		t.Fatalf("stderr = %q, want missing release-check workflow command", got)
	}
}

func TestRunRejectsReleaseCheckWorkflowSecrets(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/release-check.yml", releaseCheckWorkflow()+"      - run: printf '%s' '${{ secrets.RELEASE_TOKEN }}'\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml must not reference secrets") {
		t.Fatalf("stderr = %q, want release-check secrets failure", got)
	}
}

func TestRunRejectsReleaseCheckWorkflowWritePermission(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/release-check.yml", strings.Replace(releaseCheckWorkflow(), "contents: read", "contents: write", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml must declare explicit read-only contents permission") {
		t.Fatalf("stderr = %q, want release-check read-only permissions failure", got)
	}
}

func TestRunRejectsReleaseCheckWorkflowWithoutArtifactUpload(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	workflow := strings.Replace(releaseCheckWorkflow(), "      - name: Upload Phase 0 release evidence\n        uses: actions/upload-artifact@v4\n        with:\n          name: phase0-release-evidence\n          path: artifacts/phase0\n          if-no-files-found: error\n          retention-days: 14\n", "", 1)
	writeFile(t, root, ".github/workflows/release-check.yml", workflow)

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml must upload artifacts/phase0 using actions/upload-artifact@v4") {
		t.Fatalf("stderr = %q, want missing artifact upload failure", got)
	}
}

func TestRunRejectsReleaseCheckWorkflowArtifactRetentionOverLimit(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/release-check.yml", strings.Replace(releaseCheckWorkflow(), "retention-days: 14", "retention-days: 15", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml artifact upload retention-days must be <= 14") {
		t.Fatalf("stderr = %q, want artifact retention failure", got)
	}
}

func TestRunRejectsMissingWorkflowName(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", "on:\n  workflow_dispatch:\n\npermissions:\n  contents: read\n\njobs:\n  lint:\n    name: lint\n    runs-on: ubuntu-24.04\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/lint.yml missing top-level workflow name") {
		t.Fatalf("stderr = %q, want missing workflow name", got)
	}
}

func TestRunRejectsWorkflowNameChangedToDuplicate(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/test.yml", workflow("lint", "test", "test"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `.github/workflows/test.yml workflow name "lint" must remain "test"`) {
		t.Fatalf("stderr = %q, want unstable workflow name", got)
	}
}

func TestRunRejectsUnstableWorkflowName(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/test.yml", workflow("unit-tests", "test", "test"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `.github/workflows/test.yml workflow name "unit-tests" must remain "test"`) {
		t.Fatalf("stderr = %q, want unstable workflow name", got)
	}
}

func TestRunRejectsDuplicateWorkflowJobID(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", "name: lint\n\non:\n  workflow_dispatch:\n\npermissions:\n  contents: read\n\njobs:\n  lint:\n    name: lint\n    runs-on: ubuntu-24.04\n  lint:\n    name: lint again\n    runs-on: ubuntu-24.04\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `.github/workflows/lint.yml duplicates job id "lint"`) {
		t.Fatalf("stderr = %q, want duplicate job id", got)
	}
}

func TestRunRejectsUnpinnedWorkflowUsesReference(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", workflowWithUses("lint", "actions/checkout"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `action uses reference "actions/checkout" must include a pinned ref`) {
		t.Fatalf("stderr = %q, want missing action ref", got)
	}
}

func TestRunRejectsFloatingWorkflowUsesReference(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", workflowWithUses("lint", "actions/checkout@main"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `action uses reference "actions/checkout@main" must not use floating ref "main"`) {
		t.Fatalf("stderr = %q, want floating action ref", got)
	}
}

func TestRunRejectsBranchLikeWorkflowUsesReference(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", workflowWithUses("lint", "actions/checkout@release"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `action uses reference "actions/checkout@release" must use a semver-style tag or full commit SHA`) {
		t.Fatalf("stderr = %q, want branch-like action ref", got)
	}
}

func TestRunAllowsPinnedWorkflowUsesReferences(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/lint.yml", workflowWithUses("lint", "actions/checkout@v4", "owner/action@v1.2.3", "owner/action@0123456789abcdef0123456789abcdef01234567", "./.github/actions/local"))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "lint: ok") {
		t.Fatalf("stdout = %q, want ok message", got)
	}
}

func TestRunRejectsPreCommitRepoMissingRev(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".pre-commit-config.yaml", "repos:\n  - repo: https://github.com/pre-commit/pre-commit-hooks\n    hooks:\n      - id: trailing-whitespace\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `.pre-commit-config.yaml repo "https://github.com/pre-commit/pre-commit-hooks" missing pinned rev`) {
		t.Fatalf("stderr = %q, want missing pre-commit rev", got)
	}
}

func TestRunRejectsPreCommitFloatingRev(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".pre-commit-config.yaml", "repos:\n  - repo: https://github.com/pre-commit/pre-commit-hooks\n    rev: HEAD\n    hooks:\n      - id: trailing-whitespace\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `.pre-commit-config.yaml repo "https://github.com/pre-commit/pre-commit-hooks" rev "HEAD"`) || !strings.Contains(got, "must not use floating ref") {
		t.Fatalf("stderr = %q, want floating pre-commit rev", got)
	}
}

func TestRunAllowsPinnedPreCommitRevisions(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".pre-commit-config.yaml", "repos:\n  - repo: https://github.com/pre-commit/pre-commit-hooks\n    rev: v4.6.0\n    hooks:\n      - id: trailing-whitespace\n  - repo: https://github.com/example/hooks\n    rev: 0123456789abcdef0123456789abcdef01234567\n    hooks:\n      - id: example\n  - repo: local\n    hooks:\n      - id: gofmt\n        name: gofmt\n        entry: gofmt -w\n        language: system\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "lint: ok") {
		t.Fatalf("stdout = %q, want ok message", got)
	}
}

func TestRunReportsMissingRequiredWorkflow(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	if err := os.Remove(filepath.Join(root, ".github", "workflows", "commitlint.yml")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "missing required scaffold path: .github/workflows/commitlint.yml") {
		t.Fatalf("stderr = %q, want missing commitlint workflow", got)
	}
}

func TestRunRejectsMainBranchProtectionStatusChecks(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/branch-protection.json", `{"branches":[{"pattern":"main","required_status_checks":["lint"]}]}`)

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "main must not declare required status checks while workflows are manual-only") {
		t.Fatalf("stderr = %q, want manual-only main branch protection failure", got)
	}
}

func TestRunRejectsReleaseBranchProtectionStatusChecks(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/branch-protection.json", `{"branches":[{"pattern":"main","required_status_checks":[]},{"pattern":"release/*","required_status_checks":["lint"]}]}`)

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "release/* must not declare required status checks while workflows are manual-only") {
		t.Fatalf("stderr = %q, want manual-only release branch protection failure", got)
	}
}

func TestRunRejectsMissingMakefileTarget(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "Makefile", "build:\n\tgo build ./...\ntest:\n\tgo test ./...\nlint:\n\tgo vet ./...\naudit:\n\tgo list ./...\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile missing required target(s): phase0-gate") {
		t.Fatalf("stderr = %q, want missing phase0-gate target", got)
	}
}

func TestRunRejectsMakefileDrillGateWithoutExecuteDrills(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	writeFile(t, root, "Makefile", strings.Replace(makefile, executedDrillGateCommand, "go run ./tools/phase0-gate --format=json", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target phase0-drills must run: go run ./tools/phase0-gate --format=json --execute-drills") {
		t.Fatalf("stderr = %q, want missing Makefile executed drill command", got)
	}
}

func TestRunRejectsMakefileReleaseCheckWithoutRequiredCommand(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	writeFile(t, root, "Makefile", strings.Replace(makefile, "\tgo run ./tools/contract-tests\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target release-check must run: go run ./tools/contract-tests") {
		t.Fatalf("stderr = %q, want missing Makefile release-check command", got)
	}
}

func TestRunRejectsMakefileReleaseCheckWithoutArtifactDirectoryCreation(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	writeFile(t, root, "Makefile", strings.Replace(makefile, "\tmkdir -p $(RELEASE_EVIDENCE_DIR)\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target release-check must create artifacts/phase0 before writing release evidence") {
		t.Fatalf("stderr = %q, want Makefile release-check mkdir failure", got)
	}
}

func TestRunRejectsMakefileReleaseCheckWithoutStrictEvidenceFileVerification(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	writeFile(t, root, "Makefile", strings.Replace(makefile, "\t"+makefileVerifyEvidenceFilesCommand+"\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target release-check must verify release evidence index with strict evidence-file checks") {
		t.Fatalf("stderr = %q, want Makefile strict release evidence verification failure", got)
	}
}

func TestRunRejectsMakefileReleaseCheckWithoutStrictEvidenceFileFlag(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	writeFile(t, root, "Makefile", strings.ReplaceAll(makefile, " --verify-evidence-files", ""))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target release-check must verify release evidence index with strict evidence-file checks") {
		t.Fatalf("stderr = %q, want Makefile strict release evidence flag failure", got)
	}
}

func TestRunRejectsMissingMakefileCleanArtifactsTarget(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "Makefile", strings.Replace(readFile(t, root, "Makefile"), "clean-artifacts:\n\ttest \"$(RELEASE_EVIDENCE_DIR)\" = \"artifacts/phase0\"\n\tmkdir -p $(RELEASE_EVIDENCE_DIR)\n\tfind $(RELEASE_EVIDENCE_DIR) -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile missing required target(s): clean-artifacts") {
		t.Fatalf("stderr = %q, want missing clean-artifacts target", got)
	}
}

func TestRunRejectsUnsafeMakefileCleanArtifactsTarget(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	makefile := readFile(t, root, "Makefile")
	makefile = strings.Replace(makefile, `test "$(RELEASE_EVIDENCE_DIR)" = "artifacts/phase0"`, `test -n "$(RELEASE_EVIDENCE_DIR)"`, 1)
	writeFile(t, root, "Makefile", strings.Replace(makefile, cleanArtifactsCommand, "rm -rf $(RELEASE_EVIDENCE_DIR)", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target clean-artifacts must guard cleanup to artifacts/phase0") {
		t.Fatalf("stderr = %q, want unsafe clean-artifacts guard failure", got)
	}
}

func TestRunRejectsMissingTaskfileTask(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "Taskfile.yml", "version: '3'\n\ntasks:\n  build:\n    cmds:\n      - go build ./...\n  test:\n    cmds:\n      - go test ./...\n  lint:\n    cmds:\n      - go vet ./...\n  audit:\n    cmds:\n      - go list ./...\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml missing required task(s): phase0-gate") {
		t.Fatalf("stderr = %q, want missing phase0-gate task", got)
	}
}

func TestRunRejectsTaskfileDrillGateWithoutExecuteDrills(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	taskfile := readFile(t, root, "Taskfile.yml")
	writeFile(t, root, "Taskfile.yml", strings.Replace(taskfile, executedDrillGateCommand, "go run ./tools/phase0-gate --format=json", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml task phase0-drills must run: go run ./tools/phase0-gate --format=json --execute-drills") {
		t.Fatalf("stderr = %q, want missing Taskfile executed drill command", got)
	}
}

func TestRunRejectsTaskfileReleaseCheckWithoutRequiredCommand(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	taskfile := readFile(t, root, "Taskfile.yml")
	writeFile(t, root, "Taskfile.yml", strings.Replace(taskfile, "      - go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence {{.RELEASE_EVIDENCE_JSON}} --release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-evidence-files --release-summary {{.RELEASE_SUMMARY_JSON}}\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml task release-check must run: go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills") {
		t.Fatalf("stderr = %q, want missing Taskfile release-check command", got)
	}
}

func TestRunRejectsTaskfileReleaseCheckWithoutArtifactDirectoryCreation(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	taskfile := readFile(t, root, "Taskfile.yml")
	writeFile(t, root, "Taskfile.yml", strings.Replace(taskfile, "      - mkdir -p {{.RELEASE_EVIDENCE_DIR}}\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml task release-check must create artifacts/phase0 before writing release evidence") {
		t.Fatalf("stderr = %q, want Taskfile release-check mkdir failure", got)
	}
}

func TestRunRejectsTaskfileReleaseCheckWithoutStrictEvidenceFileVerification(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	taskfile := readFile(t, root, "Taskfile.yml")
	writeFile(t, root, "Taskfile.yml", strings.Replace(taskfile, "      - "+taskfileVerifyEvidenceFilesCommand+"\n", "", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml task release-check must verify release evidence index with strict evidence-file checks") {
		t.Fatalf("stderr = %q, want Taskfile strict release evidence verification failure", got)
	}
}

func TestRunRejectsUnsafeTaskfileCleanArtifactsTask(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	taskfile := readFile(t, root, "Taskfile.yml")
	taskfile = strings.Replace(taskfile, `test "{{.RELEASE_EVIDENCE_DIR}}" = "artifacts/phase0"`, `test -n "{{.RELEASE_EVIDENCE_DIR}}"`, 1)
	writeFile(t, root, "Taskfile.yml", strings.Replace(taskfile, cleanArtifactsTaskCommand, "rm -rf {{.RELEASE_EVIDENCE_DIR}}", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Taskfile.yml task clean-artifacts must guard cleanup to artifacts/phase0") {
		t.Fatalf("stderr = %q, want unsafe clean-artifacts guard failure", got)
	}
}

func TestRunRejectsMissingCodeownersGlob(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ @midnight/platform-core\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "CODEOWNERS missing required ownership glob(s): /test/") {
		t.Fatalf("stderr = %q, want missing /test/ glob", got)
	}
}

func TestMissingTopLevelCodeownersIgnoresLocalMidnightState(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".midnight"), 0o755); err != nil {
		t.Fatal(err)
	}

	missing, err := missingTopLevelCodeowners(root, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %#v, want local .midnight state ignored", missing)
	}
}

func TestRunRejectsCodeownersPlaceholderHandle(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ @TODO\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/test/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n/docs/ @midnight/docs\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "is a placeholder") {
		t.Fatalf("stderr = %q, want placeholder failure", got)
	}
}

func TestRunRejectsCodeownersOwnerWithoutAt(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ midnight/platform-core\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/test/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n/docs/ @midnight/docs\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "must start with `@`") {
		t.Fatalf("stderr = %q, want missing @ prefix failure", got)
	}
}

func TestRunRejectsCodeownersInvalidHandleShape(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ @midnight/team@bad\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/test/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n/docs/ @midnight/docs\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "is not a valid `@org/team` or `@user` handle") {
		t.Fatalf("stderr = %q, want invalid handle failure", got)
	}
}

func TestRunRejectsMissingGoVersionDeclaration(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "tools/versions.txt", "node=20+\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "tools/versions.txt missing go version declaration") {
		t.Fatalf("stderr = %q, want missing go version declaration", got)
	}
}

func TestRunRejectsGoVersionMismatch(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "tools/versions.txt", "go=1.22+\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `tools/versions.txt go version "1.22" must match go.mod directive "1.23"`) {
		t.Fatalf("stderr = %q, want go version mismatch", got)
	}
}

func TestRunRejectsMissingGovernanceDoc(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	if err := os.Remove(filepath.Join(root, "docs", "governance", "release-key-custody.md")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "missing required scaffold path: docs/governance/release-key-custody.md") {
		t.Fatalf("stderr = %q, want missing release key custody doc", got)
	}
}

func TestRunRejectsReleaseKeyCustodyWithoutLocalScope(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-key-custody.md", "# Release Key Custody\n\nRelease signing key custody is assigned before releases.\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `must document Phase 0 release scope text`) {
		t.Fatalf("stderr = %q, want release scope failure", got)
	}
}

func TestRunRejectsReleaseKeyCustodyWithoutRemoteBranchProtectionRequirement(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-key-custody.md", "# Release Key Custody\n\nPhase 0 local release evidence is accepted without protected signing.\nProtected signed releases require release-key custody before publication.\nProtected signed releases require real signing before publication.\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, `"protected signed releases require remote branch protection"`) {
		t.Fatalf("stderr = %q, want missing remote branch protection requirement", got)
	}
}

func TestRunRejectsReleaseEvidenceWithoutRequiredLocalCommand(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-evidence.md", "# Release Evidence\n\nRun `release-check`.\n\nLocal automation documents `phase0-drills`, `clean-artifacts`, and `artifacts/phase0`.\n\n- `go test ./...`\n- `go vet ./...`\n- `go build ./...`\n- `go run ./tools/lint`\n- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills`\n- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`\n- `--release-summary artifacts/phase0/release-summary.json`\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "docs/governance/release-evidence.md must document required local verification command: go run ./tools/contract-tests") {
		t.Fatalf("stderr = %q, want missing release evidence command", got)
	}
}

func TestRunRejectsReleaseEvidenceWithoutReleaseCheck(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-evidence.md", "# Release Evidence\n\nLocal automation documents `phase0-drills`, `clean-artifacts`, and `artifacts/phase0`.\n\n- `go test ./...`\n- `go vet ./...`\n- `go build ./...`\n- `go run ./tools/lint`\n- `go run ./tools/contract-tests`\n- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills`\n- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`\n- `--release-summary artifacts/phase0/release-summary.json`\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "docs/governance/release-evidence.md must document release evidence automation: release-check") {
		t.Fatalf("stderr = %q, want missing release-check doc", got)
	}
}

func TestRunRejectsReleaseEvidenceWithoutAutomationDocs(t *testing.T) {
	for _, phrase := range []string{"phase0-drills", "clean-artifacts", "artifacts/phase0"} {
		t.Run(phrase, func(t *testing.T) {
			root := t.TempDir()
			writeScaffold(t, root)
			doc := strings.ReplaceAll(releaseEvidenceDoc(), phrase, "missing-automation-doc")
			writeFile(t, root, "docs/governance/release-evidence.md", doc)

			var stdout, stderr bytes.Buffer
			err := run(root, &stdout, &stderr)
			if err == nil {
				t.Fatal("run returned nil error, want failure")
			}
			if got := stderr.String(); !strings.Contains(got, "docs/governance/release-evidence.md must document release evidence automation: "+phrase) {
				t.Fatalf("stderr = %q, want missing %s automation doc", got, phrase)
			}
		})
	}
}

func TestRunRejectsReleaseEvidenceWithoutJSONPathPolicy(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-evidence.md", "# Release Evidence\n\nRun `release-check`.\n\nLocal automation documents `phase0-drills`, `clean-artifacts`, and `artifacts/phase0`.\n\n- `go test ./...`\n- `go vet ./...`\n- `go build ./...`\n- `go run ./tools/lint`\n- `go run ./tools/contract-tests`\n- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills`\n- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`\n- `--release-summary artifacts/phase0/release-summary.json`\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "must document generated release evidence JSON path policy phrase") {
		t.Fatalf("stderr = %q, want missing release evidence JSON path policy", got)
	}
}

func TestRunRejectsReleaseEvidencePolicyMismatch(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/release-evidence.md", "# Release Evidence\n\nRun `release-check`.\n\nLocal automation documents `phase0-drills`, `clean-artifacts`, and `artifacts/phase0`.\n\n- `go test ./...`\n- `go vet ./...`\n- `go build ./...`\n- `go run ./tools/lint`\n- `go run ./tools/contract-tests`\n- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence dist/release-evidence/release-evidence.json --release-evidence-index dist/release-evidence/release-evidence-index.json --release-summary artifacts/phase0/release-summary.json`\n- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`\n\nGenerated release evidence JSON paths must be explicit, clean, single-file paths ending in `.json`; they must not use relative traversal or glob patterns. Release automation must write to dist/release-evidence/release-evidence.json and dist/release-evidence/release-evidence-index.json.\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "artifacts/phase0/release-evidence.json") {
		t.Fatalf("stderr = %q, want artifacts/phase0 release evidence policy failure", got)
	}
}

func TestRunRejectsMakefileReleaseEvidenceWithoutIndex(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "Makefile", "RELEASE_EVIDENCE_DIR ?= artifacts/phase0\nRELEASE_EVIDENCE_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-evidence.json\n\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\nlint:\n\tgo vet ./...\nphase0-gate:\n\tgo test ./tools/lint\nphase0-drills:\n\tgo run ./tools/phase0-gate --format=json --execute-drills\nrelease-check:\n\tmkdir -p $(RELEASE_EVIDENCE_DIR)\n\tgo test ./...\n\tgo vet ./...\n\tgo build ./...\n\tgo run ./tools/lint\n\tgo run ./tools/contract-tests\n\tgo run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence $(RELEASE_EVIDENCE_JSON)\naudit:\n\tgo list ./...\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Makefile target release-check must write release evidence JSON and index under artifacts/phase0") {
		t.Fatalf("stderr = %q, want Makefile release evidence index failure", got)
	}
}

func TestRunRejectsWorkflowReleaseEvidenceDirMismatch(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".github/workflows/release-check.yml", strings.Replace(releaseCheckWorkflow(), "artifacts/phase0", "out/phase0", 1))

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".github/workflows/release-check.yml must set RELEASE_EVIDENCE_DIR to artifacts/phase0") {
		t.Fatalf("stderr = %q, want workflow release evidence dir failure", got)
	}
}

func TestRunRejectsGitignoreWithoutGeneratedArtifacts(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, ".gitignore", "/bin/\n/dist/\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, ".gitignore must exclude generated release artifacts under artifacts/phase0") {
		t.Fatalf("stderr = %q, want generated artifacts gitignore failure", got)
	}
}

func TestRunRejectsSystemdServiceNotInstalledByDebianRules(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)
	writeFile(t, root, "deploy/systemd/midnight-extra.service", "[Service]\nExecStart=/usr/bin/midnight-server serve\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "midnight-extra.service is not installed by deploy/debian/rules") {
		t.Fatalf("stderr = %q, want service install failure", got)
	}
}

func TestRunRejectsServiceExecStartTargetNotInstalled(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)
	writeFile(t, root, "deploy/systemd/midnight-server.service", "[Service]\nExecStart=/usr/bin/missing-server serve\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "ExecStart target /usr/bin/missing-server is not installed by deploy/debian/rules") {
		t.Fatalf("stderr = %q, want ExecStart target failure", got)
	}
}

func TestRunRejectsSystemdEnvReferenceWithoutPackageDefault(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)
	writeFile(t, root, "deploy/systemd/midnight-buildkit.service", "[Service]\nExecStart=/usr/bin/midnight-buildkit serve --buildkitd ${MIDNIGHT_MISSING_BIN}\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "references MIDNIGHT_MISSING_BIN but deploy/debian/midnight.env does not declare it") {
		t.Fatalf("stderr = %q, want missing env default failure", got)
	}
}

func TestRunRejectsExternalBinaryDefaultsThatAreNotAbsolute(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)
	writeFile(t, root, "deploy/debian/midnight.env", "MIDNIGHT_BUILDKITD_BIN=buildkitd\nMIDNIGHT_BUILDCTL=/usr/local/bin/buildctl\nMIDNIGHT_REGISTRY_BIN=/usr/local/bin/registry\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "MIDNIGHT_BUILDKITD_BIN must be an absolute operator-supplied binary path") {
		t.Fatalf("stderr = %q, want external binary path failure", got)
	}
}

func TestRunRejectsExternalBinaryHardDependency(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)
	writeFile(t, root, "deploy/debian/control", "Package: midnight\nDepends: ${misc:Depends}, buildkit\nSuggests: docker-registry\n")

	var stdout, stderr bytes.Buffer
	err := run(root, &stdout, &stderr)
	if err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "Depends must not hard-depend on external binary package buildkit") {
		t.Fatalf("stderr = %q, want external dependency failure", got)
	}
}

func TestRunAcceptsDeploySystemdPackagingCoherence(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeDeployPackagingFixture(t, root)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error = %v stderr=%q", err, stderr.String())
	}
}

func writeDeployPackagingFixture(t *testing.T, root string) {
	t.Helper()
	services := []string{
		"midnight-server",
		"midnight-worker",
		"midnight-agent",
		"midnight-buildkit",
		"midnight-registry",
	}
	var rules strings.Builder
	rules.WriteString("override_dh_auto_install:\n")
	rules.WriteString("\tinstall -d $(CURDIR)/debian/midnight/usr/bin\n")
	for _, svc := range services {
		rules.WriteString("\tinstall -m 0755 build/")
		rules.WriteString(svc)
		rules.WriteString(" $(CURDIR)/debian/midnight/usr/bin/")
		rules.WriteString(svc)
		rules.WriteString("\n")
	}
	rules.WriteString("\tinstall -d $(CURDIR)/debian/midnight/lib/systemd/system\n")
	for _, svc := range services {
		rules.WriteString("\tinstall -m 0644 deploy/systemd/")
		rules.WriteString(svc)
		rules.WriteString(".service $(CURDIR)/debian/midnight/lib/systemd/system/\n")
		writeFile(t, root, "deploy/systemd/"+svc+".service", "[Service]\nExecStart=/usr/bin/"+svc+"\n")
	}
	writeFile(t, root, "deploy/debian/rules", rules.String())
	writeFile(t, root, "deploy/debian/midnight.env", "MIDNIGHT_BUILDKITD_BIN=/usr/local/bin/buildkitd\nMIDNIGHT_BUILDCTL=/usr/local/bin/buildctl\nMIDNIGHT_REGISTRY_BIN=/usr/local/bin/registry\n")
	writeFile(t, root, "deploy/debian/control", "Package: midnight\nDepends: ${misc:Depends}\nSuggests: buildkit, docker-registry\n")
}

func writeScaffold(t *testing.T, root string) {
	t.Helper()
	for _, rel := range requiredPaths {
		content := "placeholder\n"
		if rel == "docs/governance/branch-protection.json" {
			content = `{"branches":[{"pattern":"main","required_status_checks":[]},{"pattern":"release/*","required_status_checks":[]}]}` + "\n"
		} else if rel == ".github/workflows/lint.yml" {
			content = workflow("lint", "lint", "lint")
		} else if rel == ".github/workflows/test.yml" {
			content = workflow("test", "test", "test")
		} else if rel == ".github/workflows/release-check.yml" {
			content = releaseCheckWorkflow()
		} else if rel == ".github/workflows/supplychain.yml" {
			content = workflow("supplychain", "govulncheck", "govulncheck", "osv-scanner", "osv-scanner", "npm-audit", "npm-audit")
		} else if rel == ".github/workflows/build.yml" {
			content = workflow("build", "build", "build")
		} else if rel == ".github/workflows/commitlint.yml" {
			content = workflow("commitlint", "commitlint", "commitlint")
		} else if rel == ".github/workflows/doctor-vm-ec1.yml" {
			content = workflow("doctor-vm-ec1", "doctor-vm-ec1", "doctor-vm-ec1")
		} else if rel == ".github/workflows/doctor-vm-ec2.yml" {
			content = workflow("doctor-vm-ec2", "doctor-vm-ec2", "doctor-vm-ec2")
		} else if rel == "Makefile" {
			content = "RELEASE_EVIDENCE_DIR ?= artifacts/phase0\nRELEASE_EVIDENCE_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-evidence.json\nRELEASE_EVIDENCE_INDEX_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-evidence-index.json\nRELEASE_SUMMARY_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-summary.json\n\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\nlint:\n\tgo vet ./...\nphase0-gate:\n\tgo test ./tools/lint\nphase0-drills:\n\tgo run ./tools/phase0-gate --format=json --execute-drills\nrelease-check:\n\tmkdir -p $(RELEASE_EVIDENCE_DIR)\n\tgo test ./...\n\tgo vet ./...\n\tgo build ./...\n\tgo run ./tools/lint\n\tgo run ./tools/contract-tests\n\tgo run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence $(RELEASE_EVIDENCE_JSON) --release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-evidence-files --release-summary $(RELEASE_SUMMARY_JSON)\n\tgo run ./tools/phase0-gate --format=json --verify-release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-evidence-files\nclean-artifacts:\n\ttest \"$(RELEASE_EVIDENCE_DIR)\" = \"artifacts/phase0\"\n\tmkdir -p $(RELEASE_EVIDENCE_DIR)\n\tfind $(RELEASE_EVIDENCE_DIR) -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +\naudit:\n\tgo list ./...\n"
		} else if rel == "Taskfile.yml" {
			content = "version: '3'\n\nvars:\n  RELEASE_EVIDENCE_DIR: '{{.RELEASE_EVIDENCE_DIR | default \"artifacts/phase0\"}}'\n  RELEASE_EVIDENCE_JSON: '{{.RELEASE_EVIDENCE_JSON | default (printf \"%s/release-evidence.json\" .RELEASE_EVIDENCE_DIR)}}'\n  RELEASE_EVIDENCE_INDEX_JSON: '{{.RELEASE_EVIDENCE_INDEX_JSON | default (printf \"%s/release-evidence-index.json\" .RELEASE_EVIDENCE_DIR)}}'\n  RELEASE_SUMMARY_JSON: '{{.RELEASE_SUMMARY_JSON | default (printf \"%s/release-summary.json\" .RELEASE_EVIDENCE_DIR)}}'\n\ntasks:\n  build:\n    cmds:\n      - go build ./...\n  test:\n    cmds:\n      - go test ./...\n  lint:\n    cmds:\n      - go vet ./...\n  phase0-gate:\n    cmds:\n      - go test ./tools/lint\n  phase0-drills:\n    cmds:\n      - go run ./tools/phase0-gate --format=json --execute-drills\n  release-check:\n    cmds:\n      - mkdir -p {{.RELEASE_EVIDENCE_DIR}}\n      - go test ./...\n      - go vet ./...\n      - go build ./...\n      - go run ./tools/lint\n      - go run ./tools/contract-tests\n      - go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence {{.RELEASE_EVIDENCE_JSON}} --release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-evidence-files --release-summary {{.RELEASE_SUMMARY_JSON}}\n      - go run ./tools/phase0-gate --format=json --verify-release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-evidence-files\n  clean-artifacts:\n    cmds:\n      - test \"{{.RELEASE_EVIDENCE_DIR}}\" = \"artifacts/phase0\"\n      - mkdir -p {{.RELEASE_EVIDENCE_DIR}}\n      - find {{.RELEASE_EVIDENCE_DIR}} -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +\n  audit:\n    cmds:\n      - go list ./...\n"
		} else if rel == ".gitignore" {
			content = "/artifacts/\n"
		} else if rel == "tools/versions.txt" {
			content = "go=1.23+\n"
		} else if rel == "go.mod" {
			content = "module github.com/midnight/midnight\n\ngo 1.23\n"
		} else if rel == ".pre-commit-config.yaml" {
			content = "repos:\n  - repo: https://github.com/pre-commit/pre-commit-hooks\n    rev: v4.6.0\n    hooks:\n      - id: trailing-whitespace\n  - repo: local\n    hooks:\n      - id: gofmt\n        name: gofmt\n        entry: gofmt -w\n        language: system\n"
		} else if rel == "docs/governance/release-key-custody.md" {
			content = "# Release Key Custody\n\nPhase 0 local release evidence is accepted without protected signing.\nProtected signed releases require release-key custody before publication.\nProtected signed releases require remote branch protection before publication.\nProtected signed releases require real signing before publication.\n"
		} else if rel == "docs/governance/release-evidence.md" {
			content = releaseEvidenceDoc()
		}
		writeFile(t, root, rel, content)
	}
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ @midnight/platform-core\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/test/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n/docs/ @midnight/docs\n")
}

func workflow(name string, jobsAndNames ...string) string {
	var b strings.Builder
	b.WriteString("name: ")
	b.WriteString(name)
	b.WriteString("\n\non:\n  workflow_dispatch:\n\npermissions:\n  contents: read\n\njobs:\n")
	for i := 0; i < len(jobsAndNames); i += 2 {
		b.WriteString("  ")
		b.WriteString(jobsAndNames[i])
		b.WriteString(":\n    name: ")
		b.WriteString(jobsAndNames[i+1])
		b.WriteString("\n    runs-on: ubuntu-24.04\n")
	}
	return b.String()
}

func workflowWithUses(name string, usesRefs ...string) string {
	var b strings.Builder
	b.WriteString(workflow(name, "lint", "lint"))
	b.WriteString("    steps:\n")
	for _, ref := range usesRefs {
		b.WriteString("      - uses: ")
		b.WriteString(ref)
		b.WriteString("\n")
	}
	return b.String()
}

func releaseCheckWorkflow() string {
	return "name: release-check\n\non:\n  workflow_dispatch:\n\npermissions:\n  contents: read\n\nenv:\n  RELEASE_EVIDENCE_DIR: artifacts/phase0\n  RELEASE_EVIDENCE_INDEX_JSON: artifacts/phase0/release-evidence-index.json\n\njobs:\n  release-check:\n    name: release-check\n    runs-on: ubuntu-24.04\n    steps:\n      - run: make release-check RELEASE_EVIDENCE_DIR=\"$RELEASE_EVIDENCE_DIR\" RELEASE_EVIDENCE_INDEX_JSON=\"$RELEASE_EVIDENCE_INDEX_JSON\"\n      - name: Upload Phase 0 release evidence\n        uses: actions/upload-artifact@v4\n        with:\n          name: phase0-release-evidence\n          path: artifacts/phase0\n          if-no-files-found: error\n          retention-days: 14\n"
}

func releaseEvidenceDoc() string {
	return "# Release Evidence\n\nRun `release-check`.\n\nLocal automation documents `phase0-drills`, `clean-artifacts`, and `artifacts/phase0`.\n\n- `go test ./...`\n- `go vet ./...`\n- `go build ./...`\n- `go run ./tools/lint`\n- `go run ./tools/contract-tests`\n- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files --release-summary artifacts/phase0/release-summary.json`\n- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`\n\nGenerated release evidence JSON paths must be explicit, clean, single-file paths ending in `.json`; they must not use relative traversal or glob patterns. Release automation must write the evidence packet to `artifacts/phase0/release-evidence.json`, the release evidence index to `artifacts/phase0/release-evidence-index.json`, and the release summary to `artifacts/phase0/release-summary.json`.\n"
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
