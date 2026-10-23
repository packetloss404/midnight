package supervisor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	slashpath "path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	AgentEvidenceKindReleaseEvidence       = "agent_release_evidence"
	AgentEvidenceKindLoopExecutionArtifact = "loop_execution_artifact"
	AgentEvidenceKindDecisionArtifact      = "decision_artifact"
	AgentEvidenceKindRuntimeObservation    = "runtime_observation_evidence"
)

// AgentEvidenceIndex is a deterministic manifest of agent pass evidence artifacts.
// The first refs are release, loop execution, and decision artifacts; optional
// runtime observation refs follow in sorted order for stable JSON diffs.
type AgentEvidenceIndex struct {
	EvidenceRefs []AgentEvidenceRef          `json:"evidence_refs"`
	StatusRollup []AgentEvidenceStatusRollup `json:"status_rollup"`
}

// AgentEvidenceRef points at one evidence artifact using a workspace-relative path.
type AgentEvidenceRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// AgentEvidenceStatusRollup summarizes evidence refs by status.
type AgentEvidenceStatusRollup struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// BuildAgentEvidenceIndex creates a deterministic index for one agent pass.
// It performs no HTTP, Docker, or filesystem I/O.
func BuildAgentEvidenceIndex(releaseEvidence AgentEvidenceRef, loopExecution AgentEvidenceRef, decision AgentEvidenceRef, runtimeObservations []AgentEvidenceRef) AgentEvidenceIndex {
	releaseEvidence.Kind = AgentEvidenceKindReleaseEvidence
	loopExecution.Kind = AgentEvidenceKindLoopExecutionArtifact
	decision.Kind = AgentEvidenceKindDecisionArtifact

	refs := []AgentEvidenceRef{releaseEvidence, loopExecution, decision}
	runtimeRefs := append([]AgentEvidenceRef(nil), runtimeObservations...)
	for i := range runtimeRefs {
		runtimeRefs[i].Kind = AgentEvidenceKindRuntimeObservation
	}
	sort.Slice(runtimeRefs, func(i, j int) bool {
		if runtimeRefs[i].Path != runtimeRefs[j].Path {
			return runtimeRefs[i].Path < runtimeRefs[j].Path
		}
		return runtimeRefs[i].Status < runtimeRefs[j].Status
	})
	refs = append(refs, runtimeRefs...)

	return AgentEvidenceIndex{EvidenceRefs: refs, StatusRollup: agentEvidenceStatusRollup(refs)}
}

// ValidateAgentEvidenceIndex validates evidence refs and their deterministic status rollup.
func ValidateAgentEvidenceIndex(index AgentEvidenceIndex) error {
	if len(index.EvidenceRefs) < 3 {
		return fmt.Errorf("agent evidence index must include release, loop execution, and decision evidence refs")
	}
	requiredKinds := []string{AgentEvidenceKindReleaseEvidence, AgentEvidenceKindLoopExecutionArtifact, AgentEvidenceKindDecisionArtifact}
	for i, kind := range requiredKinds {
		if index.EvidenceRefs[i].Kind != kind {
			return fmt.Errorf("agent evidence ref at index %d must have kind %q", i, kind)
		}
	}
	previousRuntimePath := ""
	previousRuntimeStatus := ""
	for i, ref := range index.EvidenceRefs {
		if !isAgentEvidenceKind(ref.Kind) {
			return fmt.Errorf("agent evidence ref at index %d has unsupported kind %q", i, ref.Kind)
		}
		if strings.TrimSpace(ref.Status) == "" {
			return fmt.Errorf("agent evidence ref at index %d must have a status", i)
		}
		if err := validateAgentEvidenceRelativePath(ref.Path); err != nil {
			return fmt.Errorf("agent evidence ref at index %d: %w", i, err)
		}
		if i >= len(requiredKinds) {
			if ref.Kind != AgentEvidenceKindRuntimeObservation {
				return fmt.Errorf("agent evidence ref at index %d must have kind %q", i, AgentEvidenceKindRuntimeObservation)
			}
			if ref.Path < previousRuntimePath || (ref.Path == previousRuntimePath && ref.Status <= previousRuntimeStatus) {
				return fmt.Errorf("runtime observation evidence refs must be sorted by path and status")
			}
			previousRuntimePath = ref.Path
			previousRuntimeStatus = ref.Status
		}
	}

	wantRollup := agentEvidenceStatusRollup(index.EvidenceRefs)
	if len(index.StatusRollup) != len(wantRollup) {
		return fmt.Errorf("agent evidence status rollup does not match evidence refs")
	}
	for i := range wantRollup {
		if index.StatusRollup[i] != wantRollup[i] {
			return fmt.Errorf("agent evidence status rollup does not match evidence refs")
		}
	}
	return nil
}

// MarshalAgentEvidenceIndex validates and marshals the index with stable field names.
func MarshalAgentEvidenceIndex(index AgentEvidenceIndex) ([]byte, error) {
	if err := ValidateAgentEvidenceIndex(index); err != nil {
		return nil, err
	}
	return json.Marshal(index)
}

// WriteAgentEvidenceIndexJSON writes a validated evidence index to an explicit .json path.
func WriteAgentEvidenceIndexJSON(path string, index AgentEvidenceIndex) error {
	if err := validateAgentEvidenceIndexJSONPath(path); err != nil {
		return err
	}
	if err := validateAgentEvidenceIndexJSONParent(path); err != nil {
		return err
	}
	encoded, err := MarshalAgentEvidenceIndex(index)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// ReadAgentEvidenceIndexJSON strictly decodes and validates an evidence index from an explicit .json path.
func ReadAgentEvidenceIndexJSON(path string) (AgentEvidenceIndex, error) {
	var index AgentEvidenceIndex
	if err := validateAgentEvidenceIndexJSONPath(path); err != nil {
		return index, err
	}
	if err := validateAgentEvidenceIndexJSONParent(path); err != nil {
		return index, err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return index, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return index, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("agent evidence index JSON must contain exactly one value")
		}
		return index, err
	}
	if err := ValidateAgentEvidenceIndex(index); err != nil {
		return index, err
	}
	return index, nil
}

func agentEvidenceStatusRollup(refs []AgentEvidenceRef) []AgentEvidenceStatusRollup {
	counts := make(map[string]int)
	for _, ref := range refs {
		status := strings.TrimSpace(ref.Status)
		if status != "" {
			counts[status]++
		}
	}
	statuses := make([]string, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	rollup := make([]AgentEvidenceStatusRollup, 0, len(statuses))
	for _, status := range statuses {
		rollup = append(rollup, AgentEvidenceStatusRollup{Status: status, Count: counts[status]})
	}
	return rollup
}

func validateAgentEvidenceRelativePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	slashPath := strings.ReplaceAll(path, "\\", "/")
	if strings.HasPrefix(slashPath, "/") || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || hasWindowsDriveVolume(slashPath) {
		return fmt.Errorf("path %q must be relative", path)
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("path %q must use slash separators", path)
	}
	cleaned := slashpath.Clean(slashPath)
	if cleaned == "." || cleaned != path {
		return fmt.Errorf("path %q must be clean", path)
	}
	parts := strings.Split(cleaned, "/")
	for _, part := range parts {
		if part == ".." {
			return fmt.Errorf("path %q must not traverse parent directories", path)
		}
	}
	return nil
}

func hasWindowsDriveVolume(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isAgentEvidenceKind(kind string) bool {
	switch kind {
	case AgentEvidenceKindReleaseEvidence, AgentEvidenceKindLoopExecutionArtifact, AgentEvidenceKindDecisionArtifact, AgentEvidenceKindRuntimeObservation:
		return true
	default:
		return false
	}
}

func validateAgentEvidenceIndexJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("agent evidence index path must not be empty")
	}
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("agent evidence index path %q must end with .json", path)
	}
	return nil
}

func validateAgentEvidenceIndexJSONParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("agent evidence index parent %q is not available: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agent evidence index parent %q is not a directory", parent)
	}
	return nil
}
