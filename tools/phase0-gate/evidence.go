package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const releaseEvidenceGeneratedAt = "deterministic-placeholder"

type cliModeFlags struct {
	Format                     string        `json:"format"`
	Out                        string        `json:"out,omitempty"`
	LastGreen                  string        `json:"lastGreen"`
	DrillAdapter               string        `json:"drillAdapter"`
	ExecuteDrills              bool          `json:"executeDrills"`
	RequireExecutedDrills      bool          `json:"requireExecutedDrills"`
	ReleaseEvidence            string        `json:"releaseEvidence"`
	ReleaseSummary             string        `json:"releaseSummary,omitempty"`
	ReleaseEvidenceIndex       string        `json:"releaseEvidenceIndex,omitempty"`
	VerifyReleaseEvidenceIndex string        `json:"verifyReleaseEvidenceIndex,omitempty"`
	VerifyEvidenceFiles        bool          `json:"verifyEvidenceFiles,omitempty"`
	EvidenceRefs               []evidenceRef `json:"evidenceRefs,omitempty"`
}

type evidenceRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type releaseEvidenceArchiveIndex struct {
	GeneratedAt generatedTimestampEvidence    `json:"generatedAt"`
	Entries     []releaseEvidenceArchiveEntry `json:"entries"`
}

type releaseEvidenceArchiveEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Role   string `json:"role"`
	SHA256 string `json:"sha256,omitempty"`
}

type releaseEvidenceIndexVerification struct {
	Path         string   `json:"path"`
	Status       string   `json:"status"`
	Entries      int      `json:"entries"`
	RequiredRefs []string `json:"requiredRefs"`
	FilesChecked bool     `json:"filesChecked"`
	Error        string   `json:"error,omitempty"`
}

type generatedTimestampEvidence struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type releaseEvidence struct {
	GeneratedAt            generatedTimestampEvidence    `json:"generatedAt"`
	ModeFlags              cliModeFlags                  `json:"modeFlags"`
	GateReport             gateReport                    `json:"gateReport"`
	RequiredDrillExecution *drillExecutionSummary        `json:"requiredDrillExecution,omitempty"`
	EvidencePacket         evidencePacket                `json:"evidencePacket"`
	CanonicalProofPacket   *canonicalProofPacketContract `json:"canonicalProofPacket,omitempty"`
	ReleaseReady           *bool                         `json:"releaseReady,omitempty"`
}

type releaseSummary struct {
	ReleaseReady                    bool                              `json:"releaseReady"`
	GateCounts                      releaseSummaryGateCounts          `json:"gateCounts"`
	ExecutedDrillCounts             releaseSummaryExecutedDrillCounts `json:"executedDrillCounts"`
	EvidenceIndexVerificationStatus string                            `json:"evidenceIndexVerificationStatus"`
	EvidencePacketItemStatuses      []releaseSummaryEvidenceItem      `json:"evidencePacketItemStatuses"`
	CanonicalProofPacket            *canonicalProofPacketContract     `json:"canonicalProofPacket,omitempty"`
	// Extended is populated by enrichReleaseSummary and is omitted when nil so
	// existing consumers that decode the legacy summary keep working.
	Extended *extendedReleaseSummary `json:"-"`
}

// MarshalJSON implements custom JSON encoding so the optional Extended block
// is emitted alongside the legacy fields without breaking older consumers.
// When Extended is nil, the encoded shape is identical to the legacy schema.
func (s releaseSummary) MarshalJSON() ([]byte, error) {
	type alias releaseSummary
	if s.Extended == nil {
		return json.Marshal(struct {
			alias
			Extended *extendedReleaseSummary `json:"-"`
		}{alias: alias(s)})
	}
	return json.Marshal(struct {
		alias
		Extended        *extendedReleaseSummary `json:"-"`
		Verdict         readinessVerdict        `json:"verdict"`
		BlockingReasons []blockingReason        `json:"blockingReasons"`
		Lanes           []laneStatusBlock       `json:"lanes"`
		ManifestHash    string                  `json:"manifestHash"`
	}{
		alias:           alias(s),
		Verdict:         s.Extended.Verdict,
		BlockingReasons: s.Extended.BlockingReasons,
		Lanes:           s.Extended.Lanes,
		ManifestHash:    s.Extended.ManifestHash,
	})
}

type releaseSummaryGateCounts struct {
	DrillRows             int            `json:"drillRows"`
	DrillStatuses         map[string]int `json:"drillStatuses"`
	AlertRules            int            `json:"alertRules"`
	AlertValidationPassed bool           `json:"alertValidationPassed"`
}

type releaseSummaryExecutedDrillCounts struct {
	Assertions int `json:"assertions"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
}

type releaseSummaryEvidenceItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func buildReleaseEvidence(report gateReport, flags cliModeFlags, requireExecutedDrills bool) releaseEvidence {
	evidence := releaseEvidence{
		GeneratedAt:    generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "phase0-gate deterministic placeholder"},
		ModeFlags:      flags,
		GateReport:     report,
		EvidencePacket: report.EvidencePacket,
	}
	proofPacket, err := buildCanonicalProofPacketContract(flags, report.ReleaseEvidenceIndexVerification)
	if err == nil {
		evidence.CanonicalProofPacket = &proofPacket
	}
	if requireExecutedDrills {
		ready := releaseRequiredChecksPassed(report)
		evidence.ReleaseReady = &ready
		if report.DrillExecution != nil {
			execution := *report.DrillExecution
			evidence.RequiredDrillExecution = &execution
		}
	}
	return evidence
}

func releaseRequiredChecksPassed(report gateReport) bool {
	if !report.AlertValidationPassed || report.DrillExecution == nil || !executedDrillsPassed(*report.DrillExecution) {
		return false
	}
	return report.ReleaseEvidenceIndexVerification == nil || report.ReleaseEvidenceIndexVerification.Status == "pass"
}

func buildReleaseSummary(report gateReport) releaseSummary {
	summary := releaseSummary{
		ReleaseReady: releaseRequiredChecksPassed(report),
		GateCounts: releaseSummaryGateCounts{
			DrillRows:             report.DrillRows,
			DrillStatuses:         copyStringIntMap(report.DrillStatuses),
			AlertRules:            report.AlertRules,
			AlertValidationPassed: report.AlertValidationPassed,
		},
		EvidenceIndexVerificationStatus: "not-run",
		EvidencePacketItemStatuses:      make([]releaseSummaryEvidenceItem, 0, len(report.EvidencePacket.Items)),
	}
	if report.DrillExecution != nil {
		summary.ExecutedDrillCounts = releaseSummaryExecutedDrillCounts{
			Assertions: report.DrillExecution.Assertions,
			Passed:     report.DrillExecution.Passed,
			Failed:     report.DrillExecution.Failed,
			Skipped:    report.DrillExecution.Skipped,
		}
	}
	if report.ReleaseEvidenceIndexVerification != nil {
		summary.EvidenceIndexVerificationStatus = report.ReleaseEvidenceIndexVerification.Status
	}
	for _, item := range report.EvidencePacket.Items {
		summary.EvidencePacketItemStatuses = append(summary.EvidencePacketItemStatuses, releaseSummaryEvidenceItem{Name: item.Name, Status: item.Status})
	}
	return summary
}

func attachCanonicalProofPacketToSummary(summary releaseSummary, report gateReport, flags cliModeFlags) releaseSummary {
	proofPacket, err := buildCanonicalProofPacketContract(flags, report.ReleaseEvidenceIndexVerification)
	if err == nil {
		summary.CanonicalProofPacket = &proofPacket
	}
	return summary
}

func writeReleaseSummary(path string, summary releaseSummary) error {
	if err := validateReleaseEvidencePath(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func copyStringIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func validateReleaseEvidencePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("release evidence path is required")
	}
	if strings.ToLower(filepath.Ext(path)) != ".json" {
		return fmt.Errorf("release evidence path must end in .json: %s", path)
	}
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("release evidence parent does not exist: %s", parent)
	}
	if !info.IsDir() {
		return fmt.Errorf("release evidence parent is not a directory: %s", parent)
	}
	return nil
}

func writeReleaseEvidence(path string, evidence releaseEvidence) error {
	if err := validateReleaseEvidencePath(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func buildReleaseEvidenceArchiveIndex(releaseEvidencePath string, extraRefs []evidenceRef) (releaseEvidenceArchiveIndex, error) {
	entries := []releaseEvidenceArchiveEntry{
		{Name: "drill-manifest", Path: "test/drills/manifest.yaml", Role: "drill-manifest"},
		{Name: "prometheus-alerts", Path: "deploy/prometheus/alerts.yaml", Role: "alert-rules"},
	}
	if strings.TrimSpace(releaseEvidencePath) != "" {
		entries = append([]releaseEvidenceArchiveEntry{{Name: "gate-release-evidence", Path: releaseEvidencePath, Role: "release-evidence"}}, entries...)
	}
	for _, ref := range extraRefs {
		entries = append(entries, releaseEvidenceArchiveEntry{Name: ref.Name, Path: ref.Path, Role: "extra"})
	}
	index := releaseEvidenceArchiveIndex{
		GeneratedAt: generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "phase0-gate deterministic placeholder"},
		Entries:     entries,
	}
	if err := validateReleaseEvidenceArchiveIndex(index); err != nil {
		return releaseEvidenceArchiveIndex{}, err
	}
	return index, nil
}

func writeReleaseEvidenceArchiveIndex(path string, index releaseEvidenceArchiveIndex) error {
	if err := validateReleaseEvidencePath(path); err != nil {
		return err
	}
	if err := validateReleaseEvidenceArchiveIndex(index); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func verifyReleaseEvidenceArchiveIndexFile(path string, verifyEvidenceFiles bool) releaseEvidenceIndexVerification {
	verification := releaseEvidenceIndexVerification{
		Path:         path,
		Status:       "fail",
		RequiredRefs: []string{"gate-release-evidence", "drill-manifest", "prometheus-alerts"},
		FilesChecked: verifyEvidenceFiles,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		verification.Error = err.Error()
		return verification
	}
	var index releaseEvidenceArchiveIndex
	if err := json.Unmarshal(data, &index); err != nil {
		verification.Error = fmt.Sprintf("read release evidence index %s: %v", path, err)
		return verification
	}
	verification.Entries = len(index.Entries)
	if err := validateReleaseEvidenceArchiveIndexForVerification(index); err != nil {
		verification.Error = err.Error()
		return verification
	}
	if verifyEvidenceFiles {
		if err := verifyReleaseEvidenceArchiveIndexFiles(index); err != nil {
			verification.Error = err.Error()
			return verification
		}
	}
	verification.Status = "pass"
	return verification
}

func verifyReleaseEvidenceArchiveIndexFiles(index releaseEvidenceArchiveIndex) error {
	for i, entry := range index.Entries {
		path := filepath.Clean(entry.Path)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("release evidence index entries[%d] referenced file does not exist: %s", i, entry.Path)
			}
			return fmt.Errorf("release evidence index entries[%d] referenced file %s: %w", i, entry.Path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("release evidence index entries[%d] referenced path is a directory: %s", i, entry.Path)
		}
		if strings.TrimSpace(entry.SHA256) != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("release evidence index entries[%d] read referenced file %s: %w", i, entry.Path, err)
			}
			sum := sha256.Sum256(data)
			got := fmt.Sprintf("%x", sum)
			if !strings.EqualFold(got, entry.SHA256) {
				return fmt.Errorf("release evidence index entries[%d] sha256 mismatch for %s: got %s, want %s", i, entry.Path, got, entry.SHA256)
			}
		}
	}
	return nil
}

func validateReleaseEvidenceArchiveIndexForVerification(index releaseEvidenceArchiveIndex) error {
	if err := validateReleaseEvidenceArchiveIndex(index); err != nil {
		return err
	}
	for i, entry := range index.Entries {
		if err := validateRelativeSafeEvidenceRefPath(entry.Path); err != nil {
			return fmt.Errorf("release evidence index entries[%d] path: %w", i, err)
		}
		if entry.Role == "release-evidence" && strings.ToLower(filepath.Ext(entry.Path)) != ".json" {
			return fmt.Errorf("release evidence index entries[%d] release evidence path must end in .json: %s", i, entry.Path)
		}
	}
	return validateRequiredReleaseEvidenceArchiveRefs(index)
}

func validateRequiredReleaseEvidenceArchiveRefs(index releaseEvidenceArchiveIndex) error {
	required := map[string]releaseEvidenceArchiveEntry{
		"gate-release-evidence": {Name: "gate-release-evidence", Role: "release-evidence"},
		"drill-manifest":        {Name: "drill-manifest", Path: "test/drills/manifest.yaml", Role: "drill-manifest"},
		"prometheus-alerts":     {Name: "prometheus-alerts", Path: "deploy/prometheus/alerts.yaml", Role: "alert-rules"},
	}
	found := make(map[string]releaseEvidenceArchiveEntry, len(required))
	for _, entry := range index.Entries {
		if _, ok := required[entry.Name]; ok {
			found[entry.Name] = entry
		}
	}
	for name, want := range required {
		got, ok := found[name]
		if !ok {
			return fmt.Errorf("release evidence index missing required ref %q", name)
		}
		if got.Role != want.Role {
			return fmt.Errorf("release evidence index required ref %q has role %q, want %q", name, got.Role, want.Role)
		}
		if want.Path != "" && filepath.ToSlash(got.Path) != want.Path {
			return fmt.Errorf("release evidence index required ref %q has path %q, want %q", name, got.Path, want.Path)
		}
	}
	return nil
}

func validateReleaseEvidenceArchiveIndex(index releaseEvidenceArchiveIndex) error {
	seen := make(map[string]struct{}, len(index.Entries))
	for i, entry := range index.Entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || strings.ContainsAny(name, `\\/`) {
			return fmt.Errorf("release evidence index entries[%d] has invalid name %q", i, entry.Name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("release evidence index entries[%d] duplicates name %q", i, name)
		}
		seen[name] = struct{}{}
		if sha := strings.TrimSpace(entry.SHA256); sha != "" && !isSHA256Hex(sha) {
			return fmt.Errorf("release evidence index entries[%d] has invalid sha256 %q", i, entry.SHA256)
		}
		if err := validateRelativeSafeEvidenceRefPath(entry.Path); err != nil {
			return fmt.Errorf("release evidence index entries[%d] path: %w", i, err)
		}
		if entry.Role == "release-evidence" && strings.ToLower(filepath.Ext(entry.Path)) != ".json" {
			return fmt.Errorf("release evidence index entries[%d] release evidence path must end in .json: %s", i, entry.Path)
		}
	}
	return nil
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func validateRelativeSafeEvidenceRefPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evidence ref path is required")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return fmt.Errorf("evidence ref path must be relative: %s", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(filepath.ToSlash(clean), "../") {
		return fmt.Errorf("evidence ref path must not escape workspace: %s", path)
	}
	return nil
}

func readEvidencePacket(path string) (evidencePacket, error) {
	if strings.TrimSpace(path) == "" {
		return evidencePacket{}, errors.New("evidence packet path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return evidencePacket{}, err
	}
	var packet evidencePacket
	if err := json.Unmarshal(data, &packet); err != nil {
		return evidencePacket{}, fmt.Errorf("read evidence packet %s: %w", path, err)
	}
	return packet, nil
}

func writeEvidencePacket(path string, packet evidencePacket) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evidence packet path is required")
	}
	if strings.ToLower(filepath.Ext(path)) != ".json" {
		return fmt.Errorf("evidence packet path must end in .json: %s", path)
	}
	data, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
