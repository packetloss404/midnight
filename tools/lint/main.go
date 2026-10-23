// Package main runs the Midnight scaffold and policy guard. It is invoked
// from `make lint`, `task lint`, the pre-commit hook, the manual lint workflow,
// and local release checks. Failures print one diagnostic per violation
// to stderr followed by a stable LINT_* reason code on the final line so
// release automation can route the failure deterministically.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var requiredPaths = []string{
	"README.md",
	"Makefile",
	"Taskfile.yml",
	".pre-commit-config.yaml",
	".gitignore",
	"go.mod",
	"go.sum",
	".github/workflows/build.yml",
	".github/workflows/commitlint.yml",
	".github/workflows/doctor-vm-ec1.yml",
	".github/workflows/doctor-vm-ec2.yml",
	".github/workflows/lint.yml",
	".github/workflows/release-check.yml",
	".github/workflows/supplychain.yml",
	".github/workflows/test.yml",
	"docs/adr/README.md",
	"docs/contributing/npm.md",
	"docs/governance/branch-protection.json",
	"docs/governance/release-evidence.md",
	"docs/governance/release-key-custody.md",
	"tools/versions.txt",
}

var requiredGovernanceDocs = []string{
	"docs/governance/branch-protection.json",
	"docs/governance/release-evidence.md",
	"docs/governance/release-key-custody.md",
	"docs/contributing/npm.md",
}

var requiredLocalVerificationCommands = []string{
	"go test ./...",
	"go vet ./...",
	"go build ./...",
	"go run ./tools/lint",
	"go run ./tools/contract-tests",
	"go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills",
	"go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files",
	"--release-summary artifacts/phase0/release-summary.json",
}

var requiredReleaseCheckCommands = []string{
	"go test ./...",
	"go vet ./...",
	"go build ./...",
	"go run ./tools/lint",
	"go run ./tools/contract-tests",
	"go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills",
	"--verify-evidence-files",
	"--release-summary",
}

var requiredWorkflowFiles = []string{
	"lint.yml",
	"release-check.yml",
	"test.yml",
	"supplychain.yml",
	"build.yml",
	"commitlint.yml",
	"doctor-vm-ec1.yml",
	"doctor-vm-ec2.yml",
}

var requiredWorkflowNames = map[string]string{
	"lint.yml":          "lint",
	"release-check.yml": "release-check",
	"test.yml":          "test",
	"supplychain.yml":   "supplychain",
	"build.yml":         "build",
	"commitlint.yml":    "commitlint",
	"doctor-vm-ec1.yml": "doctor-vm-ec1",
	"doctor-vm-ec2.yml": "doctor-vm-ec2",
}

var requiredReleaseScopePhrases = []string{
	"phase 0 local release evidence is accepted without protected signing",
	"protected signed releases require release-key custody",
	"protected signed releases require remote branch protection",
	"protected signed releases require real signing",
}

var manualWorkflowCheckNames = []string{
	"lint",
	"test",
	"govulncheck",
	"osv-scanner",
	"npm-audit",
	"build",
	"commitlint",
	"release-check",
	"doctor-vm-ec1",
	"doctor-vm-ec2",
}

var requiredReleaseEvidencePathPolicyPhrases = []string{
	"artifacts/phase0/release-evidence.json",
	"artifacts/phase0/release-evidence-index.json",
	"artifacts/phase0/release-summary.json",
	"single-file paths ending in `.json`",
	"must not use relative traversal or glob patterns",
}

var requiredReleaseEvidenceAutomationDocs = []string{
	"release-check",
	"phase0-drills",
	"clean-artifacts",
	releaseEvidenceDir,
}

const (
	releaseEvidenceDir                  = "artifacts/phase0"
	releaseEvidenceJSON                 = "artifacts/phase0/release-evidence.json"
	releaseEvidenceIndexJSON            = "artifacts/phase0/release-evidence-index.json"
	requiredReleaseCheckWorkflowCommand = "make release-check"
	releaseArtifactUploadAction         = "actions/upload-artifact@v4"
	releaseArtifactName                 = "phase0-release-evidence"
	maxReleaseArtifactRetentionDays     = 14
)

var requiredAutomationNames = []string{
	"lint",
	"test",
	"build",
	"phase0-gate",
	"phase0-drills",
	"release-check",
	"clean-artifacts",
	"audit",
}

const executedDrillGateCommand = "go run ./tools/phase0-gate --format=json --execute-drills"
const makefileVerifyEvidenceFilesCommand = "go run ./tools/phase0-gate --format=json --verify-release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-evidence-files"
const taskfileVerifyEvidenceFilesCommand = "go run ./tools/phase0-gate --format=json --verify-release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}} --verify-evidence-files"
const cleanArtifactsCommand = "find $(RELEASE_EVIDENCE_DIR) -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"
const cleanArtifactsTaskCommand = "find {{.RELEASE_EVIDENCE_DIR}} -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"

var requiredCodeownersGlobs = []string{
	"/cmd/",
	"/internal/",
	"/api/",
	"/deploy/",
	"/test/",
	"/tools/",
}

// codeownersTopLevelDenyList names directories that are excluded from the
// "every top-level dir has a CODEOWNERS rule" guard. Bookkeeping directories
// (.git/, .github/, .claude/, .midnight/, .worktrees/), generated
// packaging/build output, and pure-data directories that ride on the catch-all
// `*` rule do not need their own glob. .midnight/ is ignored local CLI and
// parity-loop state, not repository source.
var codeownersTopLevelDenyList = map[string]bool{
	".git":         true,
	".github":      true,
	".claude":      true,
	".midnight":    true,
	".worktrees":   true,
	"build":        true,
	"debian":       true,
	"node_modules": true,
}

// LINT reason codes ride along with non-zero lint exits so release automation
// can route failures by category. Only one code is printed per run; the first
// failing category wins. They are documented in docs/error-codes.md.
const (
	lintReasonScaffoldGuard = "LINT_SCAFFOLD_GUARD_FAILED"
	lintReasonSourceGuard   = "LINT_SOURCE_GUARD_FAILED"
	lintReasonRegistryGuard = "LINT_REGISTRY_GUARD_FAILED"
	lintReasonADRDrift      = "LINT_ADR_INDEX_DRIFT"
	lintReasonGateDocDrift  = "LINT_GATE_DOC_DRIFT"
	lintReasonProductCopy   = "LINT_PRODUCT_COPY_GUARD_FAILED"
	lintReasonInvocation    = "LINT_INVOCATION_FAILED"
)

// Stable lint exit codes. Release automation routes on these; do not renumber.
// Documented in docs/contributing/style.md.
const (
	// ExitOK indicates every guard passed.
	ExitOK = 0
	// ExitInvocation indicates the lint binary could not parse its own
	// arguments or could not load on-disk inputs (parse errors, missing
	// scaffold roots, unreadable files). It corresponds to the
	// LINT_INVOCATION_FAILED reason code.
	ExitInvocation = 2
	// ExitGuardFailed indicates one or more guards rejected the tree. The
	// final stderr line carries the per-category LINT_* reason code.
	ExitGuardFailed = 3
	// ExitDriftWritten indicates the lint binary regenerated a managed
	// artifact (currently the ADR index via --adr-index-write) and wrote it
	// to disk. Exiting with this code rather than 0 surfaces the change to
	// callers that want to detect drift without otherwise modifying the tree.
	ExitDriftWritten = 4
)

var forbiddenNpmLockfiles = []string{
	"pnpm-lock.yaml",
	"yarn.lock",
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI parses the argument list and dispatches to either the regenerate-and-
// write path (`--adr-index-write`) or the standard guard chain. It returns the
// stable exit code from the ExitOK/ExitInvocation/ExitGuardFailed/
// ExitDriftWritten constants. Splitting this out keeps main() trivial and lets
// tests cover argument handling without re-entering os.Exit.
func runCLI(args []string, stdout, stderr io.Writer) int {
	root := "."
	writeIndex := false
	for _, arg := range args {
		switch {
		case arg == "--adr-index-write" || arg == "-adr-index-write":
			writeIndex = true
		case strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "lint: unknown flag %q\nlint reason: %s\n", arg, lintReasonInvocation)
			return ExitInvocation
		default:
			root = arg
		}
	}
	if writeIndex {
		written, err := writeADRIndex(root)
		if err != nil {
			fmt.Fprintf(stderr, "lint: %v\nlint reason: %s\n", err, lintReasonInvocation)
			return ExitInvocation
		}
		if written {
			fmt.Fprintln(stdout, "lint: wrote regenerated ADR index")
			return ExitDriftWritten
		}
		fmt.Fprintln(stdout, "lint: ADR index already current")
		return ExitOK
	}
	if err := run(root, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		// run() already prints a per-category LINT_* reason code on guard
		// failure; the only non-guard error class today is invocation
		// (loadGoSources / file read). We treat any error here as a guard
		// failure for routing simplicity; invocation errors are handled by
		// the explicit branches above.
		return ExitGuardFailed
	}
	return ExitOK
}

func run(root string, stdout, stderr io.Writer) error {
	var scaffoldFailures []error

	for _, rel := range requiredPaths {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				scaffoldFailures = append(scaffoldFailures, fmt.Errorf("missing required scaffold path: %s", rel))
				continue
			}
			scaffoldFailures = append(scaffoldFailures, fmt.Errorf("stat %s: %w", rel, err))
		}
	}

	for _, rel := range forbiddenNpmLockfiles {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			scaffoldFailures = append(scaffoldFailures, fmt.Errorf("forbidden npm policy lockfile present: %s", rel))
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			scaffoldFailures = append(scaffoldFailures, fmt.Errorf("stat %s: %w", rel, err))
		}
	}

	if err := checkWorkflowPolicy(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkBranchProtection(filepath.Join(root, "docs", "governance", "branch-protection.json")); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkMakefileTargets(filepath.Join(root, "Makefile")); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkTaskfileTasks(filepath.Join(root, "Taskfile.yml")); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkCodeowners(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkPreCommitConfig(filepath.Join(root, ".pre-commit-config.yaml")); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkGovernanceDocs(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkReleaseEvidencePathPolicy(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkGoVersion(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}
	if err := checkDeploySystemdPackaging(root); err != nil {
		scaffoldFailures = append(scaffoldFailures, err)
	}

	sources, sourceLoadErr := loadGoSources(root)
	var sourceFailures []error
	if sourceLoadErr != nil {
		sourceFailures = append(sourceFailures, sourceLoadErr)
	} else {
		sourceFailures = append(sourceFailures, checkForbidPanic(sources)...)
		sourceFailures = append(sourceFailures, checkForbidOSExit(sources)...)
		sourceFailures = append(sourceFailures, checkRequirePackageDoc(sources)...)
	}

	registryFailures := checkErrorCodeRegistryConsistency(root)
	if sourceLoadErr == nil {
		registryFailures = append(registryFailures, checkUnregisteredFailureCodeLiterals(root, sources)...)
	}
	adrFailures := checkADRIndexDrift(root)
	gateDocFailures := checkPhase0GateDocAccuracy(root)
	productCopyFailures := checkProductCopyVocabulary(root)

	totalFailures := len(scaffoldFailures) + len(sourceFailures) + len(registryFailures) + len(adrFailures) + len(gateDocFailures) + len(productCopyFailures)

	for _, err := range scaffoldFailures {
		fmt.Fprintf(stderr, "scaffold guard: %v\n", err)
	}
	for _, err := range sourceFailures {
		fmt.Fprintf(stderr, "source guard: %v\n", err)
	}
	for _, err := range registryFailures {
		fmt.Fprintf(stderr, "registry guard: %v\n", err)
	}
	for _, err := range adrFailures {
		fmt.Fprintf(stderr, "adr guard: %v\n", err)
	}
	for _, err := range gateDocFailures {
		fmt.Fprintf(stderr, "gate-doc guard: %v\n", err)
	}
	for _, err := range productCopyFailures {
		fmt.Fprintf(stderr, "product-copy guard: %v\n", err)
	}

	if totalFailures > 0 {
		// First failing category wins for the reason-code so release
		// automation can dispatch on a stable signal even when multiple
		// guards trip at once.
		reason := lintReasonScaffoldGuard
		switch {
		case len(scaffoldFailures) > 0:
			reason = lintReasonScaffoldGuard
		case len(sourceFailures) > 0:
			reason = lintReasonSourceGuard
		case len(registryFailures) > 0:
			reason = lintReasonRegistryGuard
		case len(adrFailures) > 0:
			reason = lintReasonADRDrift
		case len(gateDocFailures) > 0:
			reason = lintReasonGateDocDrift
		case len(productCopyFailures) > 0:
			reason = lintReasonProductCopy
		}
		fmt.Fprintf(stderr, "lint reason: %s\n", reason)
		return fmt.Errorf("lint failed with %d issue(s)", totalFailures)
	}

	fmt.Fprintln(stdout, "lint: ok")
	return nil
}

// readNormalizedFile reads path and strips carriage returns so subsequent
// "contains" checks behave the same on Windows-checked-out (CRLF) and
// Linux-checked-out (LF) clones.
func readNormalizedFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return data, nil
	}
	// Strip \r in-place rather than allocating a new buffer for the common
	// case where the file already uses LF only.
	if !containsCR(data) {
		return data, nil
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if b == '\r' {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func containsCR(b []byte) bool {
	for _, c := range b {
		if c == '\r' {
			return true
		}
	}
	return false
}

func checkReleaseEvidencePathPolicy(root string) error {
	files := []string{
		".github/workflows/release-check.yml",
		"Makefile",
		"Taskfile.yml",
		"docs/governance/release-evidence.md",
	}
	for _, rel := range files {
		data, err := readNormalizedFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		content := string(data)
		if !strings.Contains(content, releaseEvidenceDir) {
			return fmt.Errorf("%s must use release evidence directory policy %q", rel, releaseEvidenceDir)
		}
		if strings.Contains(content, "dist/release-evidence") {
			return fmt.Errorf("%s must not use legacy release evidence directory dist/release-evidence", rel)
		}
	}

	makefile, err := readNormalizedFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	if !strings.Contains(string(makefile), "RELEASE_EVIDENCE_DIR ?= "+releaseEvidenceDir) {
		return fmt.Errorf("Makefile must default RELEASE_EVIDENCE_DIR to %s", releaseEvidenceDir)
	}
	if !makefileTargetContains(string(makefile), "release-check", "--release-evidence $(RELEASE_EVIDENCE_JSON) --release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON)") {
		return fmt.Errorf("Makefile target release-check must write release evidence JSON and index under %s", releaseEvidenceDir)
	}
	if !makefileTargetContains(string(makefile), "release-check", makefileVerifyEvidenceFilesCommand) {
		return fmt.Errorf("Makefile target release-check must verify release evidence index with strict evidence-file checks")
	}
	if !makefileTargetContains(string(makefile), "release-check", "mkdir -p $(RELEASE_EVIDENCE_DIR)") {
		return fmt.Errorf("Makefile target release-check must create %s before writing release evidence", releaseEvidenceDir)
	}
	if !makefileTargetContains(string(makefile), "clean-artifacts", `test "$(RELEASE_EVIDENCE_DIR)" = "`+releaseEvidenceDir+`"`) {
		return fmt.Errorf("Makefile target clean-artifacts must guard cleanup to %s", releaseEvidenceDir)
	}
	if !makefileTargetContains(string(makefile), "clean-artifacts", cleanArtifactsCommand) {
		return fmt.Errorf("Makefile target clean-artifacts must remove only direct contents of %s", releaseEvidenceDir)
	}

	taskfile, err := readNormalizedFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		return fmt.Errorf("read Taskfile.yml: %w", err)
	}
	if !strings.Contains(string(taskfile), `default "`+releaseEvidenceDir+`"`) {
		return fmt.Errorf("Taskfile.yml must default RELEASE_EVIDENCE_DIR to %s", releaseEvidenceDir)
	}
	if !taskfileTaskContains(string(taskfile), "release-check", "--release-evidence {{.RELEASE_EVIDENCE_JSON}} --release-evidence-index {{.RELEASE_EVIDENCE_INDEX_JSON}}") {
		return fmt.Errorf("Taskfile.yml task release-check must write release evidence JSON and index under %s", releaseEvidenceDir)
	}
	if !taskfileTaskContains(string(taskfile), "release-check", taskfileVerifyEvidenceFilesCommand) {
		return fmt.Errorf("Taskfile.yml task release-check must verify release evidence index with strict evidence-file checks")
	}
	if !taskfileTaskContains(string(taskfile), "release-check", "mkdir -p {{.RELEASE_EVIDENCE_DIR}}") {
		return fmt.Errorf("Taskfile.yml task release-check must create %s before writing release evidence", releaseEvidenceDir)
	}
	if !taskfileTaskContains(string(taskfile), "clean-artifacts", `test "{{.RELEASE_EVIDENCE_DIR}}" = "`+releaseEvidenceDir+`"`) {
		return fmt.Errorf("Taskfile.yml task clean-artifacts must guard cleanup to %s", releaseEvidenceDir)
	}
	if !taskfileTaskContains(string(taskfile), "clean-artifacts", cleanArtifactsTaskCommand) {
		return fmt.Errorf("Taskfile.yml task clean-artifacts must remove only direct contents of %s", releaseEvidenceDir)
	}

	workflow, err := readNormalizedFile(filepath.Join(root, ".github", "workflows", "release-check.yml"))
	if err != nil {
		return fmt.Errorf("read .github/workflows/release-check.yml: %w", err)
	}
	workflowContent := string(workflow)
	if !strings.Contains(workflowContent, "RELEASE_EVIDENCE_DIR: "+releaseEvidenceDir) {
		return fmt.Errorf(".github/workflows/release-check.yml must set RELEASE_EVIDENCE_DIR to %s", releaseEvidenceDir)
	}
	if !strings.Contains(workflowContent, "RELEASE_EVIDENCE_INDEX_JSON: "+releaseEvidenceIndexJSON) {
		return fmt.Errorf(".github/workflows/release-check.yml must set RELEASE_EVIDENCE_INDEX_JSON to %s", releaseEvidenceIndexJSON)
	}
	if !strings.Contains(workflowContent, `make release-check RELEASE_EVIDENCE_DIR="$RELEASE_EVIDENCE_DIR" RELEASE_EVIDENCE_INDEX_JSON="$RELEASE_EVIDENCE_INDEX_JSON"`) {
		return fmt.Errorf(".github/workflows/release-check.yml must pass release evidence paths into make release-check")
	}

	doc, err := readNormalizedFile(filepath.Join(root, "docs", "governance", "release-evidence.md"))
	if err != nil {
		return fmt.Errorf("read docs/governance/release-evidence.md: %w", err)
	}
	docContent := string(doc)
	if !strings.Contains(docContent, releaseEvidenceJSON) || !strings.Contains(docContent, releaseEvidenceIndexJSON) {
		return fmt.Errorf("docs/governance/release-evidence.md must document %s and %s", releaseEvidenceJSON, releaseEvidenceIndexJSON)
	}

	gitignore, err := readNormalizedFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	if !gitignoreExcludesGeneratedArtifacts(string(gitignore)) {
		return fmt.Errorf(".gitignore must exclude generated release artifacts under %s", releaseEvidenceDir)
	}

	return nil
}

func gitignoreExcludesGeneratedArtifacts(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if line == "/artifacts/" || line == "/artifacts/phase0/" || line == "artifacts/phase0/" {
			return true
		}
	}
	return false
}

func checkGovernanceDocs(root string) error {
	for _, rel := range requiredGovernanceDocs {
		data, err := readNormalizedFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("read required governance doc %s: %w", rel, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return fmt.Errorf("required governance doc is empty: %s", rel)
		}
		if rel == "docs/governance/release-key-custody.md" {
			content := strings.ToLower(string(data))
			for _, phrase := range requiredReleaseScopePhrases {
				if !strings.Contains(content, phrase) {
					return fmt.Errorf("docs/governance/release-key-custody.md must document Phase 0 release scope text for %q", phrase)
				}
			}
		}
		if rel == "docs/governance/release-evidence.md" {
			content := string(data)
			for _, phrase := range requiredReleaseEvidenceAutomationDocs {
				if !strings.Contains(content, phrase) {
					return fmt.Errorf("docs/governance/release-evidence.md must document release evidence automation: %s", phrase)
				}
			}
			for _, command := range requiredLocalVerificationCommands {
				if !strings.Contains(content, command) {
					return fmt.Errorf("docs/governance/release-evidence.md must document required local verification command: %s", command)
				}
			}
			for _, phrase := range requiredReleaseEvidencePathPolicyPhrases {
				if !strings.Contains(content, phrase) {
					return fmt.Errorf("docs/governance/release-evidence.md must document generated release evidence JSON path policy phrase: %s", phrase)
				}
			}
		}
	}
	return nil
}

func checkPreCommitConfig(path string) error {
	data, err := readNormalizedFile(path)
	if err != nil {
		return fmt.Errorf("read .pre-commit-config.yaml: %w", err)
	}

	var currentRepo string
	var currentRev string
	var currentLine int
	validateCurrent := func() error {
		if currentRepo == "" || currentRepo == "local" {
			return nil
		}
		if currentRev == "" {
			return fmt.Errorf(".pre-commit-config.yaml repo %q missing pinned rev", currentRepo)
		}
		if err := checkPinnedRepositoryRevision(currentRev); err != nil {
			return fmt.Errorf(".pre-commit-config.yaml repo %q rev %q on line %d %w", currentRepo, currentRev, currentLine, err)
		}
		return nil
	}

	for lineNo, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- repo:") {
			if err := validateCurrent(); err != nil {
				return err
			}
			currentRepo = trimYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- repo:")))
			currentRev = ""
			currentLine = 0
			continue
		}
		if currentRepo != "" && strings.HasPrefix(trimmed, "rev:") {
			currentRev = trimYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "rev:")))
			currentLine = lineNo + 1
		}
	}
	return validateCurrent()
}

func trimYAMLScalar(value string) string {
	if hash := strings.IndexByte(value, '#'); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}
	return strings.Trim(strings.TrimSpace(value), "'\"")
}

func checkPinnedRepositoryRevision(rev string) error {
	forbidden := strings.ToLower(rev)
	if forbidden == "main" || forbidden == "master" || forbidden == "latest" || forbidden == "head" {
		return fmt.Errorf("must not use floating ref")
	}
	if isFullCommitSHA(rev) || isSemverLikeRef(rev) {
		return nil
	}
	return fmt.Errorf("must use a semver-like ref or full commit SHA")
}

func isSemverLikeRef(ref string) bool {
	ref = strings.TrimPrefix(ref, "v")
	if ref == "" || ref[0] < '0' || ref[0] > '9' {
		return false
	}
	for _, r := range ref {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' || r == '+' {
			continue
		}
		return false
	}
	return true
}

func checkGoVersion(root string) error {
	versionsGo, err := readVersionsGo(filepath.Join(root, "tools", "versions.txt"))
	if err != nil {
		return err
	}
	modGo, err := readGoModVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	if versionsGo != modGo {
		return fmt.Errorf("tools/versions.txt go version %q must match go.mod directive %q", versionsGo, modGo)
	}
	return nil
}

func readVersionsGo(path string) (string, error) {
	data, err := readNormalizedFile(path)
	if err != nil {
		return "", fmt.Errorf("read tools/versions.txt: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "go" {
			continue
		}
		version := normalizeGoVersion(value)
		if version == "" {
			return "", fmt.Errorf("tools/versions.txt has empty go version")
		}
		return version, nil
	}
	return "", fmt.Errorf("tools/versions.txt missing go version declaration")
}

func readGoModVersion(path string) (string, error) {
	data, err := readNormalizedFile(path)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" {
			return normalizeGoVersion(fields[1]), nil
		}
	}
	return "", fmt.Errorf("go.mod missing go directive")
}

func normalizeGoVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimSuffix(version, "+")
	return strings.TrimPrefix(version, "go")
}

func checkMakefileTargets(path string) error {
	data, err := readNormalizedFile(path)
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}

	found := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		prefix := strings.TrimSpace(line[:colon])
		if prefix == "" || strings.Contains(prefix, "=") {
			continue
		}
		for _, target := range strings.Fields(prefix) {
			found[target] = true
		}
	}

	if missing := missingRequired(found, requiredAutomationNames); len(missing) > 0 {
		return fmt.Errorf("Makefile missing required target(s): %s", strings.Join(missing, ", "))
	}
	if !makefileTargetContains(string(data), "phase0-drills", executedDrillGateCommand) {
		return fmt.Errorf("Makefile target phase0-drills must run: %s", executedDrillGateCommand)
	}
	if !makefileTargetContains(string(data), "clean-artifacts", cleanArtifactsCommand) {
		return fmt.Errorf("Makefile target clean-artifacts must run: %s", cleanArtifactsCommand)
	}
	for _, command := range requiredReleaseCheckCommands {
		if !makefileTargetContains(string(data), "release-check", command) {
			return fmt.Errorf("Makefile target release-check must run: %s", command)
		}
	}
	return nil
}

func makefileTargetContains(content, target, command string) bool {
	inTarget := false
	for _, line := range strings.Split(content, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			colon := strings.IndexByte(line, ':')
			if colon < 0 {
				inTarget = false
				continue
			}
			inTarget = false
			for _, name := range strings.Fields(strings.TrimSpace(line[:colon])) {
				if name == target {
					inTarget = true
					break
				}
			}
			continue
		}
		if inTarget && strings.Contains(line, command) {
			return true
		}
	}
	return false
}

func checkTaskfileTasks(path string) error {
	data, err := readNormalizedFile(path)
	if err != nil {
		return fmt.Errorf("read Taskfile.yml: %w", err)
	}

	found := make(map[string]bool)
	inTasks := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inTasks = trimmed == "tasks:"
			continue
		}
		if !inTasks || !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "    ") || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		name := strings.TrimSuffix(trimmed, ":")
		if name != "" {
			found[name] = true
		}
	}

	if missing := missingRequired(found, requiredAutomationNames); len(missing) > 0 {
		return fmt.Errorf("Taskfile.yml missing required task(s): %s", strings.Join(missing, ", "))
	}
	if !taskfileTaskContains(string(data), "phase0-drills", executedDrillGateCommand) {
		return fmt.Errorf("Taskfile.yml task phase0-drills must run: %s", executedDrillGateCommand)
	}
	if !taskfileTaskContains(string(data), "clean-artifacts", cleanArtifactsTaskCommand) {
		return fmt.Errorf("Taskfile.yml task clean-artifacts must run: %s", cleanArtifactsTaskCommand)
	}
	for _, command := range requiredReleaseCheckCommands {
		if !taskfileTaskContains(string(data), "release-check", command) {
			return fmt.Errorf("Taskfile.yml task release-check must run: %s", command)
		}
	}
	return nil
}

func taskfileTaskContains(content, task, command string) bool {
	inTasks := false
	inTask := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inTasks = trimmed == "tasks:"
			inTask = false
			continue
		}
		if inTasks && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(trimmed, ":") {
			inTask = strings.TrimSuffix(trimmed, ":") == task
			continue
		}
		if inTask && strings.Contains(line, command) {
			return true
		}
	}
	return false
}

func checkCodeowners(root string) error {
	path := filepath.Join(root, "CODEOWNERS")
	data, err := readNormalizedFile(path)
	if err != nil {
		return fmt.Errorf("read CODEOWNERS: %w", err)
	}

	found := make(map[string]bool)
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		found[fields[0]] = true
		if err := validateCodeownersOwners(lineNo+1, fields[0], fields[1:]); err != nil {
			return err
		}
	}

	if missing := missingRequired(found, requiredCodeownersGlobs); len(missing) > 0 {
		return fmt.Errorf("CODEOWNERS missing required ownership glob(s): %s", strings.Join(missing, ", "))
	}

	if missing, err := missingTopLevelCodeowners(root, found); err != nil {
		return err
	} else if len(missing) > 0 {
		return fmt.Errorf("CODEOWNERS missing rule for top-level director(ies): %s", strings.Join(missing, ", "))
	}
	return nil
}

// validateCodeownersOwners enforces that every CODEOWNERS rule lists at least
// one real owner handle. A real handle is `@<org>/<team>` (preferred) or
// `@<user>`; placeholder values such as `@TODO`, `@TBD`, `@FIXME`, an empty
// owner list, or anything that does not start with `@` are rejected. Tightens
// the Phase 0 scaffold so a directory cannot land with unowned reviews.
func validateCodeownersOwners(lineNo int, glob string, owners []string) error {
	if len(owners) == 0 {
		return fmt.Errorf("CODEOWNERS line %d %s has no owner handle", lineNo, glob)
	}
	for _, owner := range owners {
		if owner == "" {
			continue
		}
		if !strings.HasPrefix(owner, "@") {
			return fmt.Errorf("CODEOWNERS line %d %s owner %q must start with `@`", lineNo, glob, owner)
		}
		handle := strings.TrimPrefix(owner, "@")
		if handle == "" {
			return fmt.Errorf("CODEOWNERS line %d %s owner %q is empty", lineNo, glob, owner)
		}
		upper := strings.ToUpper(handle)
		switch upper {
		case "TODO", "TBD", "FIXME", "PLACEHOLDER", "NONE":
			return fmt.Errorf("CODEOWNERS line %d %s owner %q is a placeholder; assign a real team handle", lineNo, glob, owner)
		}
		if !isValidCodeownersHandle(handle) {
			return fmt.Errorf("CODEOWNERS line %d %s owner %q is not a valid `@org/team` or `@user` handle", lineNo, glob, owner)
		}
	}
	return nil
}

// isValidCodeownersHandle accepts a GitHub user handle (alphanumeric and
// dashes) or a `<org>/<team>` pair separated by exactly one slash, with each
// segment non-empty and limited to alphanumeric, dash, and underscore. The
// rule mirrors what GitHub itself accepts in a CODEOWNERS file.
func isValidCodeownersHandle(handle string) bool {
	if handle == "" {
		return false
	}
	if strings.Count(handle, "/") > 1 {
		return false
	}
	for _, segment := range strings.Split(handle, "/") {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '-' || r == '_':
			default:
				return false
			}
		}
	}
	return true
}

// missingTopLevelCodeowners returns top-level directories that exist on disk
// but lack a /<dir>/ rule in CODEOWNERS. Bookkeeping directories listed in
// codeownersTopLevelDenyList are excluded. The catch-all `*` rule covers
// loose root files; this guard only enforces directory-scoped ownership.
func missingTopLevelCodeowners(root string, found map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read repo root: %w", err)
	}
	var missing []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if codeownersTopLevelDenyList[name] {
			continue
		}
		glob := "/" + name + "/"
		if !found[glob] {
			missing = append(missing, glob)
		}
	}
	return missing, nil
}

func missingRequired(found map[string]bool, required []string) []string {
	var missing []string
	for _, name := range required {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func checkWorkflowPolicy(root string) error {
	workflowDir := filepath.Join(root, ".github", "workflows")
	foundManualWorkflowChecks := make(map[string]bool)
	workflowNames := make(map[string]string)
	for _, name := range requiredWorkflowFiles {
		path := filepath.Join(workflowDir, name)
		data, err := readNormalizedFile(path)
		if err != nil {
			return fmt.Errorf("read .github/workflows/%s: %w", name, err)
		}
		content := string(data)
		if strings.Contains(content, "pull_request_target") {
			return fmt.Errorf(".github/workflows/%s must not use pull_request_target", name)
		}
		if !strings.Contains(content, "workflow_dispatch") {
			return fmt.Errorf(".github/workflows/%s must provide a workflow_dispatch trigger", name)
		}
		if name == "release-check.yml" {
			if err := checkReleaseCheckWorkflow(content); err != nil {
				return err
			}
		}
		workflowName, workflowChecks, err := parseWorkflowPolicy(name, content)
		if err != nil {
			return err
		}
		if workflowName == "" {
			return fmt.Errorf(".github/workflows/%s missing top-level workflow name", name)
		}
		if want := requiredWorkflowNames[name]; workflowName != want {
			return fmt.Errorf(".github/workflows/%s workflow name %q must remain %q", name, workflowName, want)
		}
		if previousFile := workflowNames[workflowName]; previousFile != "" {
			return fmt.Errorf(".github/workflows/%s duplicates workflow name %q from .github/workflows/%s", name, workflowName, previousFile)
		}
		workflowNames[workflowName] = name
		for check := range workflowChecks {
			foundManualWorkflowChecks[check] = true
		}
	}
	if missing := missingRequired(foundManualWorkflowChecks, manualWorkflowCheckNames); len(missing) > 0 {
		return fmt.Errorf(".github/workflows missing manual job/status check name(s): %s", strings.Join(missing, ", "))
	}

	return nil
}

func checkReleaseCheckWorkflow(content string) error {
	if strings.Contains(content, "secrets.") || strings.Contains(content, "${{ secrets") {
		return fmt.Errorf(".github/workflows/release-check.yml must not reference secrets")
	}
	if !strings.Contains(content, "permissions:\n  contents: read") {
		return fmt.Errorf(".github/workflows/release-check.yml must declare explicit read-only contents permission")
	}
	if strings.Contains(content, ": write") {
		return fmt.Errorf(".github/workflows/release-check.yml must not request write permissions")
	}
	if !strings.Contains(content, requiredReleaseCheckWorkflowCommand) {
		return fmt.Errorf(".github/workflows/release-check.yml must run: %s", requiredReleaseCheckWorkflowCommand)
	}
	if !strings.Contains(content, "uses: "+releaseArtifactUploadAction) {
		return fmt.Errorf(".github/workflows/release-check.yml must upload %s using %s", releaseEvidenceDir, releaseArtifactUploadAction)
	}
	if !strings.Contains(content, "name: "+releaseArtifactName) {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload must name artifact %q", releaseArtifactName)
	}
	if !strings.Contains(content, "path: "+releaseEvidenceDir) {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload must use path %s", releaseEvidenceDir)
	}
	if !strings.Contains(content, "if-no-files-found: error") {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload must fail when %s is missing", releaseEvidenceDir)
	}
	retentionDays, ok, err := workflowScalarInt(content, "retention-days")
	if err != nil {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload retention-days must be an integer: %w", err)
	}
	if !ok {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload must set retention-days")
	}
	if retentionDays > maxReleaseArtifactRetentionDays {
		return fmt.Errorf(".github/workflows/release-check.yml artifact upload retention-days must be <= %d", maxReleaseArtifactRetentionDays)
	}
	return nil
}

func workflowScalarInt(content, key string) (int, bool, error) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		value := trimYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, key+":")))
		if value == "" {
			return 0, true, fmt.Errorf("empty value")
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, true, err
		}
		return n, true, nil
	}
	return 0, false, nil
}

func parseWorkflowPolicy(fileName, content string) (string, map[string]bool, error) {
	workflowName := ""
	foundChecks := make(map[string]bool)
	lines := strings.Split(content, "\n")
	hasPermissions := false
	permissionsHasEntries := false
	inPermissions := false
	inJobs := false
	currentJob := ""
	jobIDs := make(map[string]bool)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if usesRef, ok := workflowUsesReference(trimmed); ok {
			if err := checkWorkflowUsesReference(fileName, usesRef); err != nil {
				return "", nil, err
			}
		}

		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if strings.HasPrefix(trimmed, "name:") {
				workflowName = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "name:")), "'\"")
			}
			inJobs = trimmed == "jobs:"
			currentJob = ""
			if strings.HasPrefix(trimmed, "permissions:") {
				hasPermissions = true
				inPermissions = true
				value := strings.TrimSpace(strings.TrimPrefix(trimmed, "permissions:"))
				if value == "read-all" || value == "write-all" {
					return "", nil, fmt.Errorf(".github/workflows/%s must not use permissions: %s", fileName, value)
				}
				if value != "" {
					return "", nil, fmt.Errorf(".github/workflows/%s permissions must be an explicit top-level block", fileName)
				}
				continue
			}
			inPermissions = false
			continue
		}

		if inPermissions {
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
				permissionName := strings.TrimSpace(strings.TrimSuffix(strings.SplitN(trimmed, ":", 2)[0], ":"))
				if permissionName == "read-all" || permissionName == "write-all" {
					return "", nil, fmt.Errorf(".github/workflows/%s must not use permissions: %s", fileName, permissionName)
				}
				permissionsHasEntries = true
			}
			continue
		}

		if !inJobs {
			continue
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(trimmed, ":") {
			currentJob = strings.TrimSuffix(trimmed, ":")
			if jobIDs[currentJob] {
				return "", nil, fmt.Errorf(".github/workflows/%s duplicates job id %q", fileName, currentJob)
			}
			jobIDs[currentJob] = true
			if isManualWorkflowCheckName(currentJob) {
				foundChecks[currentJob] = true
			}
			continue
		}
		if currentJob != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && strings.HasPrefix(trimmed, "name:") {
			jobName := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "name:")), "'\"")
			if isManualWorkflowCheckName(jobName) {
				foundChecks[jobName] = true
			}
		}
	}

	if !hasPermissions {
		return "", nil, fmt.Errorf(".github/workflows/%s missing explicit top-level permissions block", fileName)
	}
	if !permissionsHasEntries {
		return "", nil, fmt.Errorf(".github/workflows/%s permissions block must not be empty", fileName)
	}
	return workflowName, foundChecks, nil
}

func workflowUsesReference(trimmed string) (string, bool) {
	trimmed = strings.TrimPrefix(trimmed, "- ")
	if !strings.HasPrefix(trimmed, "uses:") {
		return "", false
	}
	value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:")), "'\"")
	if hash := strings.IndexByte(value, '#'); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}
	return strings.Trim(value, "'\""), true
}

func checkWorkflowUsesReference(fileName, ref string) error {
	if ref == "" || strings.HasPrefix(ref, "./") {
		return nil
	}
	action, version, ok := strings.Cut(ref, "@")
	if !ok || action == "" || version == "" {
		return fmt.Errorf(".github/workflows/%s action uses reference %q must include a pinned ref", fileName, ref)
	}
	forbidden := strings.ToLower(version)
	if forbidden == "main" || forbidden == "master" || forbidden == "latest" {
		return fmt.Errorf(".github/workflows/%s action uses reference %q must not use floating ref %q", fileName, ref, version)
	}
	if isFullCommitSHA(version) || isSemverStyleActionRef(version) {
		return nil
	}
	return fmt.Errorf(".github/workflows/%s action uses reference %q must use a semver-style tag or full commit SHA", fileName, ref)
}

func isFullCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isSemverStyleActionRef(ref string) bool {
	if len(ref) < 2 || ref[0] != 'v' || ref[1] < '0' || ref[1] > '9' {
		return false
	}
	for _, r := range ref[1:] {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isManualWorkflowCheckName(name string) bool {
	for _, check := range manualWorkflowCheckNames {
		if name == check {
			return true
		}
	}
	return false
}

func checkBranchProtection(path string) error {
	data, err := readNormalizedFile(path)
	if err != nil {
		return fmt.Errorf("read docs/governance/branch-protection.json: %w", err)
	}

	var config struct {
		Branches []struct {
			Pattern              string   `json:"pattern"`
			RequiredStatusChecks []string `json:"required_status_checks"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse docs/governance/branch-protection.json: %w", err)
	}

	foundMain := false
	for _, branch := range config.Branches {
		if branch.Pattern == "main" {
			foundMain = true
			if len(branch.RequiredStatusChecks) != 0 {
				return fmt.Errorf("docs/governance/branch-protection.json main must not declare required status checks while workflows are manual-only")
			}
			continue
		}
		if branch.Pattern == "release/*" {
			if len(branch.RequiredStatusChecks) != 0 {
				return fmt.Errorf("docs/governance/branch-protection.json release/* must not declare required status checks while workflows are manual-only")
			}
		}
	}
	if !foundMain {
		return fmt.Errorf("docs/governance/branch-protection.json missing main branch protection rule")
	}

	return nil
}
