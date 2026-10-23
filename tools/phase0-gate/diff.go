package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/midnight/midnight/test/drills/harness"
)

// ReleaseEvidenceDiff is the structured difference between two release
// evidence bundles. The diff is computed offline from JSON-serialized
// release-evidence files written by phase0-gate; no network or DB access is
// performed.
type ReleaseEvidenceDiff struct {
	LeftPath       string             `json:"leftPath"`
	RightPath      string             `json:"rightPath"`
	ReleaseReady   DiffBoolField      `json:"releaseReady"`
	GateCounts     DiffGateCounts     `json:"gateCounts"`
	EvidencePacket DiffEvidencePacket `json:"evidencePacket"`
	DrillExecution DiffDrillExecution `json:"drillExecution"`
	Regressions    []string           `json:"regressions"`
}

// DiffBoolField captures the change in a boolean release evidence field. The
// Regressed flag is true when the right side flipped from ready to not-ready.
type DiffBoolField struct {
	Left      *bool `json:"left,omitempty"`
	Right     *bool `json:"right,omitempty"`
	Changed   bool  `json:"changed"`
	Regressed bool  `json:"regressed"`
}

// DiffGateCounts captures changes in the top-level gate counts block.
type DiffGateCounts struct {
	DrillRows       diffIntField    `json:"drillRows"`
	AlertRules      diffIntField    `json:"alertRules"`
	StatusAdditions []string        `json:"statusAdditions,omitempty"`
	StatusRemovals  []string        `json:"statusRemovals,omitempty"`
	StatusChanges   []DiffStatusInt `json:"statusChanges,omitempty"`
}

// DiffStatusInt captures a single status counter change.
type DiffStatusInt struct {
	Status string `json:"status"`
	Left   int    `json:"left"`
	Right  int    `json:"right"`
}

type diffIntField struct {
	Left  int `json:"left"`
	Right int `json:"right"`
	Delta int `json:"delta"`
}

// DiffEvidencePacket captures additions and removals in the evidence packet
// items list, plus any per-item status regressions.
type DiffEvidencePacket struct {
	Additions   []string           `json:"additions,omitempty"`
	Removals    []string           `json:"removals,omitempty"`
	Regressions []DiffEvidenceItem `json:"regressions,omitempty"`
}

// DiffEvidenceItem captures a single evidence item status change.
type DiffEvidenceItem struct {
	Name  string `json:"name"`
	Left  string `json:"left"`
	Right string `json:"right"`
}

// DiffDrillExecution captures the change in executed-drill counts.
type DiffDrillExecution struct {
	Assertions diffIntField `json:"assertions"`
	Passed     diffIntField `json:"passed"`
	Failed     diffIntField `json:"failed"`
	Skipped    diffIntField `json:"skipped"`
}

// diffReleaseEvidenceFiles reads two release-evidence JSON files and returns
// the deterministic structured diff between them. The diff also records a
// flat regressions list suitable for surfacing in CI annotations.
func diffReleaseEvidenceFiles(left, right string) (ReleaseEvidenceDiff, error) {
	leftEvidence, err := readReleaseEvidenceFile(left)
	if err != nil {
		return ReleaseEvidenceDiff{}, fmt.Errorf("read left evidence %s: %w", left, err)
	}
	rightEvidence, err := readReleaseEvidenceFile(right)
	if err != nil {
		return ReleaseEvidenceDiff{}, fmt.Errorf("read right evidence %s: %w", right, err)
	}
	return diffReleaseEvidence(left, right, leftEvidence, rightEvidence), nil
}

// diffReleaseEvidence produces a deterministic diff between two
// already-decoded release evidence values. Exposed for unit tests.
func diffReleaseEvidence(leftPath, rightPath string, left, right releaseEvidence) ReleaseEvidenceDiff {
	diff := ReleaseEvidenceDiff{LeftPath: leftPath, RightPath: rightPath}
	diff.ReleaseReady = diffBool(left.ReleaseReady, right.ReleaseReady)
	if diff.ReleaseReady.Regressed {
		diff.Regressions = append(diff.Regressions, "releaseReady regressed from true to false or unset")
	}

	diff.GateCounts.DrillRows = diffInt(left.GateReport.DrillRows, right.GateReport.DrillRows)
	diff.GateCounts.AlertRules = diffInt(left.GateReport.AlertRules, right.GateReport.AlertRules)
	diff.GateCounts.StatusAdditions, diff.GateCounts.StatusRemovals, diff.GateCounts.StatusChanges = diffStatusCounts(left.GateReport.DrillStatuses, right.GateReport.DrillStatuses)
	for _, change := range diff.GateCounts.StatusChanges {
		if change.Status == "failed" && change.Right > change.Left {
			diff.Regressions = append(diff.Regressions, fmt.Sprintf("drill status counter for %q regressed from %d to %d", change.Status, change.Left, change.Right))
		}
	}

	diff.EvidencePacket.Additions, diff.EvidencePacket.Removals, diff.EvidencePacket.Regressions = diffEvidencePackets(left.EvidencePacket, right.EvidencePacket)
	for _, reg := range diff.EvidencePacket.Regressions {
		diff.Regressions = append(diff.Regressions, fmt.Sprintf("evidence packet item %q regressed from %q to %q", reg.Name, reg.Left, reg.Right))
	}

	if left.GateReport.DrillExecution != nil && right.GateReport.DrillExecution != nil {
		diff.DrillExecution = DiffDrillExecution{
			Assertions: diffInt(left.GateReport.DrillExecution.Assertions, right.GateReport.DrillExecution.Assertions),
			Passed:     diffInt(left.GateReport.DrillExecution.Passed, right.GateReport.DrillExecution.Passed),
			Failed:     diffInt(left.GateReport.DrillExecution.Failed, right.GateReport.DrillExecution.Failed),
			Skipped:    diffInt(left.GateReport.DrillExecution.Skipped, right.GateReport.DrillExecution.Skipped),
		}
		if diff.DrillExecution.Failed.Right > diff.DrillExecution.Failed.Left {
			diff.Regressions = append(diff.Regressions, fmt.Sprintf("drill execution failed count regressed from %d to %d", diff.DrillExecution.Failed.Left, diff.DrillExecution.Failed.Right))
		}
		if diff.DrillExecution.Passed.Right < diff.DrillExecution.Passed.Left {
			diff.Regressions = append(diff.Regressions, fmt.Sprintf("drill execution passed count regressed from %d to %d", diff.DrillExecution.Passed.Left, diff.DrillExecution.Passed.Right))
		}
	}
	sort.Strings(diff.Regressions)
	return diff
}

// JSON serializes the diff for stdout / file output.
func (d ReleaseEvidenceDiff) JSON() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

func diffBool(left, right *bool) DiffBoolField {
	field := DiffBoolField{Left: left, Right: right}
	leftVal := left != nil && *left
	rightVal := right != nil && *right
	field.Changed = leftVal != rightVal
	field.Regressed = leftVal && !rightVal
	return field
}

func diffInt(left, right int) diffIntField {
	return diffIntField{Left: left, Right: right, Delta: right - left}
}

func diffStatusCounts(left, right map[string]int) (additions, removals []string, changes []DiffStatusInt) {
	leftKeys := make(map[string]struct{}, len(left))
	for k := range left {
		leftKeys[k] = struct{}{}
	}
	for k := range right {
		if _, ok := leftKeys[k]; !ok {
			additions = append(additions, k)
		}
	}
	for k := range left {
		if _, ok := right[k]; !ok {
			removals = append(removals, k)
		}
	}
	all := make(map[string]struct{}, len(left)+len(right))
	for k := range left {
		all[k] = struct{}{}
	}
	for k := range right {
		all[k] = struct{}{}
	}
	for k := range all {
		l, r := left[k], right[k]
		if l != r {
			changes = append(changes, DiffStatusInt{Status: k, Left: l, Right: r})
		}
	}
	sort.Strings(additions)
	sort.Strings(removals)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Status < changes[j].Status })
	return
}

func diffEvidencePackets(left, right evidencePacket) (additions, removals []string, regressions []DiffEvidenceItem) {
	leftIndex := indexEvidenceItems(left.Items)
	rightIndex := indexEvidenceItems(right.Items)
	for name := range rightIndex {
		if _, ok := leftIndex[name]; !ok {
			additions = append(additions, name)
		}
	}
	for name, leftItem := range leftIndex {
		rightItem, ok := rightIndex[name]
		if !ok {
			removals = append(removals, name)
			continue
		}
		if leftItem.Status == "pass" && rightItem.Status != "pass" {
			regressions = append(regressions, DiffEvidenceItem{Name: name, Left: leftItem.Status, Right: rightItem.Status})
		}
	}
	sort.Strings(additions)
	sort.Strings(removals)
	sort.Slice(regressions, func(i, j int) bool { return regressions[i].Name < regressions[j].Name })
	return
}

func indexEvidenceItems(items []evidenceItem) map[string]evidenceItem {
	out := make(map[string]evidenceItem, len(items))
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func readReleaseEvidenceFile(path string) (releaseEvidence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return releaseEvidence{}, err
	}
	var evidence releaseEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return releaseEvidence{}, fmt.Errorf("decode release evidence %s: %w", path, err)
	}
	return evidence, nil
}

// ensureHarnessImported keeps a build-time reference to the harness package so
// tests that close over harness types compile in the diff layer even when no
// other code in the file imports the package directly.
var _ = harness.AssertionPassed
