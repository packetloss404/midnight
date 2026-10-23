package preflight

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DaemonBaselineSeverity is the severity of a daemon.json conflict against the
// Midnight baseline. Values are stable and JSON-safe.
type DaemonBaselineSeverity string

const (
	// DaemonBaselineSeverityBlock means the conflict requires manual operator
	// resolution before install can proceed.
	DaemonBaselineSeverityBlock DaemonBaselineSeverity = "block"
	// DaemonBaselineSeverityWarn means the merge can be presented as a safe
	// default when the operator confirms the proposed change.
	DaemonBaselineSeverityWarn DaemonBaselineSeverity = "warn"
)

// daemonBaselineConflictSeverities classifies known baseline conflict paths.
// Required boolean flags and the log-driver are blockers because they change
// run-time security/observability semantics. Numeric/string log-opts can be
// presented as warnings because the merge can negotiate.
var daemonBaselineConflictSeverities = map[string]DaemonBaselineSeverity{
	"features.buildkit": DaemonBaselineSeverityBlock,
	"log-driver":        DaemonBaselineSeverityBlock,
	"log-opts.max-size": DaemonBaselineSeverityWarn,
	"log-opts.max-file": DaemonBaselineSeverityWarn,
}

// DaemonBaselineConflictClassification reports the per-path severity for a
// DockerDaemonConflictError without running the merge again.
type DaemonBaselineConflictClassification struct {
	Path     string                 `json:"path"`
	Severity DaemonBaselineSeverity `json:"severity"`
	Reason   string                 `json:"reason"`
}

// DaemonBaselineConflictReview is a JSON-friendly classifier rollup used by
// installer wiring to decide whether to surface a conflict as warn or block.
type DaemonBaselineConflictReview struct {
	OverallSeverity   DaemonBaselineSeverity                 `json:"overall_severity"`
	Classifications   []DaemonBaselineConflictClassification `json:"classifications"`
	BlockedPaths      []string                               `json:"blocked_paths,omitempty"`
	WarningPaths      []string                               `json:"warning_paths,omitempty"`
	OperatorReviewMsg string                                 `json:"operator_review_message"`
}

// ClassifyDaemonBaselineConflict returns a stable per-path classification for the
// supplied DockerDaemonConflictError paths. Unknown paths default to block.
func ClassifyDaemonBaselineConflict(err *DockerDaemonConflictError) DaemonBaselineConflictReview {
	review := DaemonBaselineConflictReview{OverallSeverity: DaemonBaselineSeverityWarn}
	if err == nil || len(err.Paths) == 0 {
		review.OverallSeverity = DaemonBaselineSeverityWarn
		review.OperatorReviewMsg = "no daemon.json conflicts found"
		return review
	}

	paths := append([]string(nil), err.Paths...)
	sort.Strings(paths)

	var blocked, warned []string
	for _, path := range paths {
		severity := daemonBaselineConflictSeverityFor(path)
		review.Classifications = append(review.Classifications, DaemonBaselineConflictClassification{
			Path:     path,
			Severity: severity,
			Reason:   daemonBaselineConflictReason(path, severity),
		})
		if severity == DaemonBaselineSeverityBlock {
			blocked = append(blocked, path)
			continue
		}
		warned = append(warned, path)
	}
	review.BlockedPaths = blocked
	review.WarningPaths = warned
	if len(blocked) > 0 {
		review.OverallSeverity = DaemonBaselineSeverityBlock
	}
	review.OperatorReviewMsg = daemonBaselineReviewMessage(review.OverallSeverity, blocked, warned)
	return review
}

// MarshalDaemonBaselineConflictReviewJSON emits the stable JSON shape used by
// installer reports and support bundles.
func MarshalDaemonBaselineConflictReviewJSON(review DaemonBaselineConflictReview) ([]byte, error) {
	return json.MarshalIndent(review, "", "  ")
}

// MergeDockerDaemonBaselinePreservingOperatorKeys returns the merge result and a
// list of operator-only top-level keys that the merge preserved as documented
// non-conflicting additions. It is a wrapper around MergeDockerDaemonBaseline
// that callers can use to surface operator extensions in install manifests.
func MergeDockerDaemonBaselinePreservingOperatorKeys(existing []byte) (DockerDaemonMergeResult, []string, error) {
	result, err := MergeDockerDaemonBaseline(existing)
	if err != nil {
		return DockerDaemonMergeResult{}, nil, err
	}
	preserved, perr := operatorPreservedTopLevelKeys(existing)
	if perr != nil {
		return DockerDaemonMergeResult{}, nil, perr
	}
	return result, preserved, nil
}

func operatorPreservedTopLevelKeys(existing []byte) ([]string, error) {
	if len(strings.TrimSpace(string(existing))) == 0 {
		return nil, nil
	}
	current, err := decodeDockerDaemonObject(existing, true)
	if err != nil {
		return nil, fmt.Errorf("parse existing daemon.json: %w", err)
	}
	baseline, err := decodeDockerDaemonObject([]byte(DockerDaemonBaselineJSON), false)
	if err != nil {
		return nil, fmt.Errorf("parse baseline daemon.json: %w", err)
	}
	var preserved []string
	for key := range current {
		if _, ok := baseline[key]; ok {
			continue
		}
		preserved = append(preserved, key)
	}
	sort.Strings(preserved)
	return preserved, nil
}

func daemonBaselineConflictSeverityFor(path string) DaemonBaselineSeverity {
	if severity, ok := daemonBaselineConflictSeverities[path]; ok {
		return severity
	}
	return DaemonBaselineSeverityBlock
}

func daemonBaselineConflictReason(path string, severity DaemonBaselineSeverity) string {
	if severity == DaemonBaselineSeverityBlock {
		return fmt.Sprintf("daemon.json key %q is required by the Midnight baseline and must be reconciled before install", path)
	}
	return fmt.Sprintf("daemon.json key %q can be merged after operator review", path)
}

func daemonBaselineReviewMessage(severity DaemonBaselineSeverity, blocked, warned []string) string {
	switch severity {
	case DaemonBaselineSeverityBlock:
		return fmt.Sprintf("install is blocked: %d daemon.json key(s) require manual reconciliation", len(blocked))
	default:
		return fmt.Sprintf("install can proceed after operator confirms %d daemon.json key merge(s)", len(warned))
	}
}
