package main

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// laneStatusBlock is the per-lane status entry in the enriched release
// summary. Each lane corresponds to one Phase 0 worker (S01..S08); the gate
// captures its readiness via the evidence-packet items the lane is expected
// to surface. A lane is `ready` only when every required item is `pass`.
type laneStatusBlock struct {
	Lane          string   `json:"lane"`
	Status        string   `json:"status"`
	RequiredItems []string `json:"requiredItems"`
	MissingItems  []string `json:"missingItems,omitempty"`
}

// blockingReason captures one readiness blocker.
type blockingReason struct {
	ReasonCode string `json:"reasonCode"`
	Message    string `json:"message"`
}

// readinessVerdict reports the gate's overall readiness verdict. The two
// stable values are `ready` and `not-ready`. The string is encoded once and
// is intentionally a free-form scalar in the JSON output.
type readinessVerdict string

const (
	readinessReady    readinessVerdict = "ready"
	readinessNotReady readinessVerdict = "not-ready"
)

// extendedReleaseSummary augments the original releaseSummary with the
// per-lane breakdown, verdict, blocking-reason list, and the deterministic
// manifest hash. Older consumers that still decode releaseSummary continue to
// work because the extra fields are appended via JSON-only field tags.
type extendedReleaseSummary struct {
	releaseSummary
	Verdict         readinessVerdict  `json:"verdict"`
	BlockingReasons []blockingReason  `json:"blockingReasons"`
	Lanes           []laneStatusBlock `json:"lanes"`
	ManifestHash    string            `json:"manifestHash"`
}

// laneRequiredItems lists the evidence-packet item names each Phase 0 lane is
// expected to mark as passing for a `ready` verdict. The mapping is derived
// from the existing scaffold; lanes whose items are not present in the
// current report mark `missingItems` and degrade to `not-ready`.
var laneRequiredItems = map[string][]string{
	"S01": {"prometheus-alerts"},
	"S02": {"prometheus-alerts"},
	"S03": {"drill-manifest"},
	"S04": {"drill-manifest", "b1-live-deploy-proof-contract", "b2-operator-install-path-contract"},
	"S05": {"drill-manifest"},
	"S06": {"drill-manifest"},
	"S07": {"prometheus-alerts"},
	"S08": {"drill-manifest", "drill-execution", phase3HostDrillMetadataEvidenceName, phase3DrillReservationsEvidenceName},
}

var laneOptionalBlockingItems = map[string][]string{
	"S08": {phase3DrillPromotionReadinessEvidenceName, phase0ReleaseEvidenceArchiveEvidenceName},
}

// orderedLanes returns the lanes in stable order for deterministic output.
func orderedLanes() []string {
	return []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08"}
}

// enrichReleaseSummary copies the original summary and appends the per-lane
// breakdown, verdict, blocking-reason list, and a SHA-256 of the canonical
// drill manifest. The output is deterministic: identical inputs produce
// identical bytes.
func enrichReleaseSummary(summary releaseSummary, manifestData []byte) releaseSummary {
	extended := extendedReleaseSummary{
		releaseSummary: summary,
		ManifestHash:   manifestSHA256Hex(manifestData),
		Lanes:          buildLaneStatuses(summary.EvidencePacketItemStatuses),
	}
	extended.BlockingReasons = buildBlockingReasons(summary, extended.Lanes)
	if summary.ReleaseReady && len(extended.BlockingReasons) == 0 {
		extended.Verdict = readinessReady
	} else {
		extended.Verdict = readinessNotReady
	}
	// The release summary serializer marshals via JSON. Capturing the
	// extended summary back into a releaseSummary preserves the legacy
	// shape; the JSON encoder serializes the embedded fields plus the
	// extras when we wrap the value in a small typed struct. We achieve
	// that by returning a wrapper that embeds the original.
	return wrapExtended(extended)
}

// wrapExtended preserves the legacy releaseSummary surface while embedding
// the extended fields. The function is a no-op type wrapper used so callers
// that take a releaseSummary continue to work; the JSON encoder picks up the
// extra fields through a marshaler shim defined below.
func wrapExtended(e extendedReleaseSummary) releaseSummary {
	cached := e
	return releaseSummary{
		ReleaseReady:                    e.ReleaseReady,
		GateCounts:                      e.GateCounts,
		ExecutedDrillCounts:             e.ExecutedDrillCounts,
		EvidenceIndexVerificationStatus: e.EvidenceIndexVerificationStatus,
		EvidencePacketItemStatuses:      e.EvidencePacketItemStatuses,
		CanonicalProofPacket:            e.CanonicalProofPacket,
		Extended:                        &cached,
	}
}

// buildLaneStatuses computes per-lane readiness from the evidence packet
// items. A lane is ready only when every required item is `pass`.
func buildLaneStatuses(items []releaseSummaryEvidenceItem) []laneStatusBlock {
	statusByName := make(map[string]string, len(items))
	for _, item := range items {
		statusByName[item.Name] = item.Status
	}
	out := make([]laneStatusBlock, 0, len(orderedLanes()))
	for _, lane := range orderedLanes() {
		required := laneRequiredItems[lane]
		block := laneStatusBlock{Lane: lane, RequiredItems: append([]string(nil), required...), Status: "ready"}
		for _, name := range required {
			status, ok := statusByName[name]
			if !ok {
				block.Status = "not-ready"
				block.MissingItems = append(block.MissingItems, name)
				continue
			}
			if status != "pass" {
				block.Status = "not-ready"
				block.MissingItems = append(block.MissingItems, name)
			}
		}
		for _, name := range laneOptionalBlockingItems[lane] {
			if status, ok := statusByName[name]; ok && status != "pass" {
				block.Status = "not-ready"
				block.MissingItems = append(block.MissingItems, name)
			}
		}
		sort.Strings(block.MissingItems)
		out = append(out, block)
	}
	return out
}

// buildBlockingReasons converts gate-level readiness signals into a flat
// list of deterministic blocking reasons. Reason codes are stable.
func buildBlockingReasons(summary releaseSummary, lanes []laneStatusBlock) []blockingReason {
	var reasons []blockingReason
	if !summary.ReleaseReady {
		reasons = append(reasons, blockingReason{ReasonCode: "phase0.gate.release-not-ready", Message: "release readiness rollup did not pass all required checks"})
	}
	if !summary.GateCounts.AlertValidationPassed {
		reasons = append(reasons, blockingReason{ReasonCode: "phase0.gate.alert-validation", Message: "locked Prometheus alert validation failed"})
	}
	if summary.ExecutedDrillCounts.Failed > 0 {
		reasons = append(reasons, blockingReason{ReasonCode: "phase0.gate.drill-failure", Message: fmt.Sprintf("%d executed drill assertions failed", summary.ExecutedDrillCounts.Failed)})
	}
	if summary.ExecutedDrillCounts.Skipped > 0 {
		reasons = append(reasons, blockingReason{ReasonCode: "phase0.gate.drill-skipped", Message: fmt.Sprintf("%d executed drill assertions were skipped", summary.ExecutedDrillCounts.Skipped)})
	}
	if summary.EvidenceIndexVerificationStatus != "pass" && summary.EvidenceIndexVerificationStatus != "not-run" {
		reasons = append(reasons, blockingReason{ReasonCode: "phase0.gate.evidence-index-verification", Message: "release evidence index verification did not pass"})
	}
	for _, lane := range lanes {
		if lane.Status != "ready" {
			reasons = append(reasons, blockingReason{
				ReasonCode: "phase0.gate.lane-not-ready",
				Message:    fmt.Sprintf("lane %s is not ready: missing %s", lane.Lane, strings.Join(lane.MissingItems, ",")),
			})
		}
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].ReasonCode == reasons[j].ReasonCode {
			return reasons[i].Message < reasons[j].Message
		}
		return reasons[i].ReasonCode < reasons[j].ReasonCode
	})
	return reasons
}

// manifestSHA256Hex computes the deterministic SHA-256 of the drill manifest
// bytes, encoded as a lowercase hex string. The hash is stable as long as
// the manifest bytes are byte-identical.
func manifestSHA256Hex(manifestData []byte) string {
	sum := sha256.Sum256(manifestData)
	return fmt.Sprintf("%x", sum)
}
