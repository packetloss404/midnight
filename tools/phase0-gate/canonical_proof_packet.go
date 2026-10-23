package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	canonicalProofPacketID     = "phase0.beta.b4.canonical-proof-packet"
	canonicalProofPacketStatus = "pass"
	canonicalProofPacketMode   = "local-contract"
	canonicalNotProvided       = "not-provided"
)

// canonicalProofPacketContract records the local-only proof-packet contract
// for the Phase 0 release gate. It is intentionally small: it binds the
// command, runner, revision hint, operator notes, generated release artifacts,
// and release evidence index verification status into one deterministic block.
type canonicalProofPacketContract struct {
	ID                      string                     `json:"id"`
	Sprint                  string                     `json:"sprint"`
	Status                  string                     `json:"status"`
	Mode                    string                     `json:"mode"`
	OfflineByDefault        bool                       `json:"offlineByDefault"`
	GeneratedAt             generatedTimestampEvidence `json:"generatedAt"`
	Command                 string                     `json:"command"`
	RunnerOS                string                     `json:"runnerOS"`
	CommitSHA               string                     `json:"commitSHA"`
	OperatorNotesRef        string                     `json:"operatorNotesRef"`
	ReleaseEvidenceRef      string                     `json:"releaseEvidenceRef"`
	ReleaseEvidenceIndexRef string                     `json:"releaseEvidenceIndexRef"`
	ReleaseSummaryRef       string                     `json:"releaseSummaryRef"`
	VerificationStatus      string                     `json:"verificationStatus"`
	RequiredFields          []string                   `json:"requiredFields"`
}

func buildCanonicalProofPacketContract(flags cliModeFlags, verification *releaseEvidenceIndexVerification) (canonicalProofPacketContract, error) {
	contract := canonicalProofPacketContract{
		ID:                      canonicalProofPacketID,
		Sprint:                  "Phase 0 Beta Sprint B4",
		Status:                  canonicalProofPacketStatus,
		Mode:                    canonicalProofPacketMode,
		OfflineByDefault:        true,
		GeneratedAt:             generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "phase0-gate deterministic placeholder"},
		Command:                 canonicalProofPacketCommand(flags),
		RunnerOS:                runtime.GOOS,
		CommitSHA:               canonicalValueOrPlaceholder(flags.LastGreen),
		OperatorNotesRef:        canonicalOperatorNotesRef(flags.EvidenceRefs),
		ReleaseEvidenceRef:      canonicalPathOrPlaceholder(flags.ReleaseEvidence),
		ReleaseEvidenceIndexRef: canonicalPathOrPlaceholder(flags.ReleaseEvidenceIndex),
		ReleaseSummaryRef:       canonicalPathOrPlaceholder(flags.ReleaseSummary),
		VerificationStatus:      canonicalVerificationStatus(verification),
		RequiredFields:          canonicalProofPacketRequiredFields(),
	}
	if err := validateCanonicalProofPacketContract(contract); err != nil {
		return canonicalProofPacketContract{}, err
	}
	return contract, nil
}

func canonicalProofPacketRequiredFields() []string {
	return []string{
		"command",
		"runnerOS",
		"commitSHA",
		"operatorNotesRef",
		"releaseEvidenceRef",
		"releaseEvidenceIndexRef",
		"releaseSummaryRef",
		"verificationStatus",
	}
}

func canonicalProofPacketCommand(flags cliModeFlags) string {
	args := []string{"go", "run", "./tools/phase0-gate"}
	if strings.TrimSpace(flags.Format) != "" {
		args = append(args, "--format="+strings.TrimSpace(flags.Format))
	}
	if flags.ExecuteDrills {
		args = append(args, "--execute-drills")
	}
	if flags.RequireExecutedDrills {
		args = append(args, "--require-executed-drills")
	}
	if strings.TrimSpace(flags.LastGreen) != "" {
		args = append(args, "--last-green="+strings.TrimSpace(flags.LastGreen))
	}
	if adapter := strings.TrimSpace(flags.DrillAdapter); adapter != "" {
		args = append(args, "--drill-adapter="+adapter)
	}
	if path := strings.TrimSpace(flags.ReleaseEvidence); path != "" {
		args = append(args, "--release-evidence="+filepath.ToSlash(path))
	}
	if path := strings.TrimSpace(flags.ReleaseEvidenceIndex); path != "" {
		args = append(args, "--release-evidence-index="+filepath.ToSlash(path))
	}
	if path := strings.TrimSpace(flags.VerifyReleaseEvidenceIndex); path != "" {
		args = append(args, "--verify-release-evidence-index="+filepath.ToSlash(path))
	}
	if flags.VerifyEvidenceFiles {
		args = append(args, "--verify-evidence-files")
	}
	if path := strings.TrimSpace(flags.ReleaseSummary); path != "" {
		args = append(args, "--release-summary="+filepath.ToSlash(path))
	}
	for _, ref := range sortedEvidenceRefs(flags.EvidenceRefs) {
		args = append(args, "--evidence-ref="+ref.Name+"="+filepath.ToSlash(ref.Path))
	}
	for i, arg := range args {
		args[i] = shellQuoteIfNeeded(arg)
	}
	return strings.Join(args, " ")
}

func sortedEvidenceRefs(refs []evidenceRef) []evidenceRef {
	out := append([]evidenceRef(nil), refs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func shellQuoteIfNeeded(value string) string {
	if strings.ContainsAny(value, " \t\r\n\"'") {
		return strconv.Quote(value)
	}
	return value
}

func canonicalOperatorNotesRef(refs []evidenceRef) string {
	for _, ref := range refs {
		if ref.Name == "operator-notes" {
			return canonicalPathOrPlaceholder(ref.Path)
		}
	}
	return canonicalNotProvided
}

func canonicalPathOrPlaceholder(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return canonicalNotProvided
	}
	return filepath.ToSlash(path)
}

func canonicalValueOrPlaceholder(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func canonicalVerificationStatus(verification *releaseEvidenceIndexVerification) string {
	if verification == nil || strings.TrimSpace(verification.Status) == "" {
		return "not-run"
	}
	return verification.Status
}

func validateCanonicalProofPacketContract(contract canonicalProofPacketContract) error {
	if contract.ID != canonicalProofPacketID {
		return fmt.Errorf("canonical proof packet contract id = %q, want %q", contract.ID, canonicalProofPacketID)
	}
	if contract.Sprint != "Phase 0 Beta Sprint B4" {
		return fmt.Errorf("canonical proof packet contract sprint = %q, want Phase 0 Beta Sprint B4", contract.Sprint)
	}
	if contract.Status != canonicalProofPacketStatus {
		return fmt.Errorf("canonical proof packet contract status = %q, want %q", contract.Status, canonicalProofPacketStatus)
	}
	if contract.Mode != canonicalProofPacketMode || !contract.OfflineByDefault {
		return errors.New("canonical proof packet contract must remain local-only and offline by default")
	}
	if contract.GeneratedAt.Value != releaseEvidenceGeneratedAt || strings.TrimSpace(contract.GeneratedAt.Source) == "" {
		return errors.New("canonical proof packet contract must use deterministic generatedAt evidence")
	}
	for _, field := range canonicalProofPacketRequiredFields() {
		if !stringSliceContains(contract.RequiredFields, field) {
			return fmt.Errorf("canonical proof packet contract missing required field marker %q", field)
		}
	}
	for name, value := range map[string]string{
		"command":                 contract.Command,
		"runnerOS":                contract.RunnerOS,
		"commitSHA":               contract.CommitSHA,
		"operatorNotesRef":        contract.OperatorNotesRef,
		"releaseEvidenceRef":      contract.ReleaseEvidenceRef,
		"releaseEvidenceIndexRef": contract.ReleaseEvidenceIndexRef,
		"releaseSummaryRef":       contract.ReleaseSummaryRef,
		"verificationStatus":      contract.VerificationStatus,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("canonical proof packet contract %s is required", name)
		}
	}
	for name, path := range map[string]string{
		"operatorNotesRef":        contract.OperatorNotesRef,
		"releaseEvidenceRef":      contract.ReleaseEvidenceRef,
		"releaseEvidenceIndexRef": contract.ReleaseEvidenceIndexRef,
		"releaseSummaryRef":       contract.ReleaseSummaryRef,
	} {
		if path == canonicalNotProvided {
			continue
		}
		if err := validateRelativeSafeEvidenceRefPath(path); err != nil {
			return fmt.Errorf("canonical proof packet contract %s: %w", name, err)
		}
	}
	return nil
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
