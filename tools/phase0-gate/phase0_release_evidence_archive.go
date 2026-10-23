package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	phase0ReleaseEvidenceArchiveID           = "phase0.beta.b9.release-evidence-archive"
	phase0ReleaseEvidenceArchiveEvidenceName = "b9-phase0-release-evidence-archive-contract"
	phase0ReleaseEvidenceArchiveSourcePath   = "test/drills/phase0-release-evidence-archive.yaml"
	phase0ReleaseEvidencePath                = "artifacts/phase0/phase0-release-evidence.json"
	phase0ReleaseEvidenceIndexPath           = "artifacts/phase0/phase0-release-evidence-index.json"
	phase0ReleaseSummaryPath                 = "artifacts/phase0/phase0-release-summary.json"
)

type phase0ReleaseEvidenceArchiveContract struct {
	ID                         string   `json:"id"`
	Sprint                     string   `json:"sprint"`
	Status                     string   `json:"status"`
	Source                     string   `json:"source"`
	LocalOnly                  bool     `json:"localOnly"`
	ReleaseEvidence            string   `json:"releaseEvidence"`
	ReleaseEvidenceIndex       string   `json:"releaseEvidenceIndex"`
	ReleaseSummary             string   `json:"releaseSummary"`
	VerifyReleaseEvidenceIndex string   `json:"verifyReleaseEvidenceIndex"`
	VerifyEvidenceFiles        bool     `json:"verifyEvidenceFiles"`
	OperatorNotes              string   `json:"operatorNotes"`
	RunnerOS                   string   `json:"runnerOs"`
	RequiredFlags              []string `json:"requiredFlags"`
	LiveHostProbes             bool     `json:"liveHostProbes"`
}

type phase0ReleaseEvidenceArchiveFile struct {
	Version                    string
	Owner                      string
	Sprint                     string
	Status                     string
	LocalOnly                  bool
	ReleaseEvidence            string
	ReleaseEvidenceIndex       string
	ReleaseSummary             string
	VerifyReleaseEvidenceIndex string
	VerifyEvidenceFiles        bool
	OperatorNotes              string
	RunnerOS                   string
	RequiredFlags              []string
	LiveHostProbes             string
	ReadinessStatuses          []string
}

func buildPhase0ReleaseEvidenceArchiveContract() (phase0ReleaseEvidenceArchiveContract, error) {
	data, err := os.ReadFile(phase0ReleaseEvidenceArchiveSourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return phase0ReleaseEvidenceArchiveContract{}, nil
		}
		return phase0ReleaseEvidenceArchiveContract{}, err
	}
	return buildPhase0ReleaseEvidenceArchiveContractFromData(data)
}

func buildPhase0ReleaseEvidenceArchiveContractFromData(data []byte) (phase0ReleaseEvidenceArchiveContract, error) {
	file, err := parsePhase0ReleaseEvidenceArchive(data)
	if err != nil {
		return phase0ReleaseEvidenceArchiveContract{}, err
	}
	status, err := phase0ReleaseEvidenceArchiveStatus(file)
	if err != nil {
		return phase0ReleaseEvidenceArchiveContract{}, err
	}
	requiredFlags := append([]string(nil), file.RequiredFlags...)
	sort.Strings(requiredFlags)
	return phase0ReleaseEvidenceArchiveContract{
		ID:                         phase0ReleaseEvidenceArchiveID,
		Sprint:                     file.Sprint,
		Status:                     status,
		Source:                     phase0ReleaseEvidenceArchiveSourcePath,
		LocalOnly:                  file.LocalOnly,
		ReleaseEvidence:            file.ReleaseEvidence,
		ReleaseEvidenceIndex:       file.ReleaseEvidenceIndex,
		ReleaseSummary:             file.ReleaseSummary,
		VerifyReleaseEvidenceIndex: file.VerifyReleaseEvidenceIndex,
		VerifyEvidenceFiles:        file.VerifyEvidenceFiles,
		OperatorNotes:              file.OperatorNotes,
		RunnerOS:                   file.RunnerOS,
		RequiredFlags:              requiredFlags,
		LiveHostProbes:             false,
	}, nil
}

func parsePhase0ReleaseEvidenceArchive(data []byte) (phase0ReleaseEvidenceArchiveFile, error) {
	var file phase0ReleaseEvidenceArchiveFile
	listName := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		isTopLevel := rawLine == line
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "version:"):
			file.Version = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "owner:"):
			file.Owner = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "sprint:"):
			file.Sprint = valueAfterColon(line)
			listName = ""
		case isTopLevel && strings.HasPrefix(line, "status:"):
			file.Status = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "status:"):
			file.ReadinessStatuses = append(file.ReadinessStatuses, valueAfterColon(line))
			listName = ""
		case strings.HasPrefix(line, "local-only:"):
			file.LocalOnly = strings.EqualFold(valueAfterColon(line), "true")
			listName = ""
		case strings.HasPrefix(line, "release-evidence:"):
			file.ReleaseEvidence = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "release-evidence-index:"):
			file.ReleaseEvidenceIndex = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "release-summary:"):
			file.ReleaseSummary = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "verify-release-evidence-index:"):
			file.VerifyReleaseEvidenceIndex = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "verify-evidence-files:"):
			file.VerifyEvidenceFiles = strings.EqualFold(valueAfterColon(line), "true")
			listName = ""
		case strings.HasPrefix(line, "required-flag:"):
			flag := normalizeB9RequiredFlag(valueAfterColon(line))
			file.RequiredFlags = append(file.RequiredFlags, flag)
			if flag == "verify-evidence-files" {
				file.VerifyEvidenceFiles = true
			}
			listName = ""
		case strings.HasPrefix(line, "operator-notes:"):
			file.OperatorNotes = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "runner-os:"):
			file.RunnerOS = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "live-host-probes:"):
			file.LiveHostProbes = valueAfterColon(line)
			listName = ""
		case line == "required-flags:":
			listName = "required-flags"
		case listName == "required-flags" && strings.HasPrefix(line, "- "):
			flag := normalizeB9RequiredFlag(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			file.RequiredFlags = append(file.RequiredFlags, flag)
			if flag == "verify-evidence-files" {
				file.VerifyEvidenceFiles = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return phase0ReleaseEvidenceArchiveFile{}, err
	}
	return file, nil
}

func phase0ReleaseEvidenceArchiveStatus(file phase0ReleaseEvidenceArchiveFile) (string, error) {
	if file.Version != "1" {
		return "", errors.New("phase0 release evidence archive version must be 1")
	}
	if strings.TrimSpace(file.Owner) == "" {
		return "", errors.New("phase0 release evidence archive owner is required")
	}
	if file.Sprint != "Phase 0 Beta Sprint B9" {
		return "", fmt.Errorf("phase0 release evidence archive sprint = %q, want Phase 0 Beta Sprint B9", file.Sprint)
	}
	if file.Status != "" && file.Status != "ready" && file.Status != "not-ready" {
		return "", fmt.Errorf("phase0 release evidence archive status = %q, want ready or not-ready", file.Status)
	}
	if !file.LocalOnly {
		return "", errors.New("phase0 release evidence archive must be local-only")
	}
	if strings.EqualFold(file.LiveHostProbes, "true") {
		return "", errors.New("phase0 release evidence archive must not declare live host probes")
	}
	if err := validateB9Path("release-evidence", file.ReleaseEvidence, phase0ReleaseEvidencePath); err != nil {
		return "", err
	}
	if err := validateB9Path("release-evidence-index", file.ReleaseEvidenceIndex, phase0ReleaseEvidenceIndexPath); err != nil {
		return "", err
	}
	if err := validateB9Path("release-summary", file.ReleaseSummary, phase0ReleaseSummaryPath); err != nil {
		return "", err
	}
	if file.VerifyReleaseEvidenceIndex != "" {
		if err := validateB9Path("verify-release-evidence-index", file.VerifyReleaseEvidenceIndex, phase0ReleaseEvidenceIndexPath); err != nil {
			return "", err
		}
	}
	for _, ref := range []struct {
		name  string
		value string
	}{
		{name: "operator-notes", value: file.OperatorNotes},
		{name: "runner-os", value: file.RunnerOS},
	} {
		if err := validateWorkspaceRelativeMetadataRef(ref.value); err != nil {
			return "", fmt.Errorf("phase0 release evidence archive %s: %w", ref.name, err)
		}
	}
	if file.Status == "not-ready" || !b9RequiredFlagsReady(file.RequiredFlags) || file.VerifyReleaseEvidenceIndex != phase0ReleaseEvidenceIndexPath || !file.VerifyEvidenceFiles || !b9ReadinessRowsReady(file.ReadinessStatuses) {
		return "not-ready", nil
	}
	return "ready", nil
}

func validateB9Path(name, got, want string) error {
	if got != want {
		return fmt.Errorf("phase0 release evidence archive %s = %q, want %s", name, got, want)
	}
	if err := validateWorkspaceRelativeMetadataRef(got); err != nil {
		return fmt.Errorf("phase0 release evidence archive %s: %w", name, err)
	}
	return nil
}

func b9RequiredFlagsReady(flags []string) bool {
	seen := make(map[string]struct{}, len(flags))
	for _, flag := range flags {
		seen[normalizeB9RequiredFlag(flag)] = struct{}{}
	}
	for _, want := range []string{"verify-release-evidence-index", "verify-evidence-files"} {
		if _, ok := seen[want]; !ok {
			return false
		}
	}
	return true
}

func b9ReadinessRowsReady(statuses []string) bool {
	for _, status := range statuses {
		if status != "ready" && status != "pass" {
			return false
		}
	}
	return true
}

func normalizeB9RequiredFlag(flag string) string {
	return strings.TrimPrefix(strings.TrimSpace(flag), "--")
}
