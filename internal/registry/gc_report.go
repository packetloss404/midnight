package registry

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

const (
	GCDryRunReportSchemaVersion = "midnight.dev/registry-gc-dry-run/v1alpha1"
	GCApplyReportSchemaVersion  = "midnight.dev/registry-gc-apply/v1alpha1"
	GCManifestSchemaVersion     = "midnight.dev/registry-gc-manifest/v1alpha1"

	GCManifestModeDryRun = "dry-run"
	GCManifestModeApply  = "apply"
)

type GCManifest struct {
	SchemaVersion string            `json:"schemaVersion"`
	GeneratedAt   time.Time         `json:"generatedAt"`
	Mode          string            `json:"mode"`
	DryRun        bool              `json:"dryRun"`
	ConfirmApply  bool              `json:"confirmApply,omitempty"`
	Summary       GCSummary         `json:"summary"`
	Actions       []RetentionAction `json:"actions"`
}

type GCManifestInput struct {
	GeneratedAt  time.Time
	Mode         string
	ConfirmApply bool
	Actions      []RetentionAction
}

type GCDryRunReport struct {
	SchemaVersion string            `json:"schemaVersion"`
	GeneratedAt   time.Time         `json:"generatedAt"`
	DryRun        bool              `json:"dryRun"`
	Summary       GCSummary         `json:"summary"`
	Actions       []RetentionAction `json:"actions"`
}

type GCApplyReport struct {
	SchemaVersion string            `json:"schemaVersion"`
	AppliedAt     time.Time         `json:"appliedAt"`
	DryRun        bool              `json:"dryRun"`
	ConfirmApply  bool              `json:"confirmApply"`
	Summary       GCSummary         `json:"summary"`
	Actions       []RetentionAction `json:"actions"`
}

func NewGCDryRunReport(plan RetentionPlan) GCDryRunReport {
	return GCDryRunReport{
		SchemaVersion: GCDryRunReportSchemaVersion,
		GeneratedAt:   plan.GeneratedAt,
		DryRun:        true,
		Summary:       summarizeGCActions(plan.Actions),
		Actions:       plan.Actions,
	}
}

func NewGCApplyReport(result GCApplyResult) GCApplyReport {
	return GCApplyReport{
		SchemaVersion: GCApplyReportSchemaVersion,
		AppliedAt:     result.AppliedAt,
		DryRun:        result.DryRun,
		ConfirmApply:  result.ConfirmApply,
		Summary:       result.Summary,
		Actions:       result.Actions,
	}
}

// BuildGCManifest combines retention candidates and their summary into a stable
// dry-run or apply manifest. It does not contact a registry or delete images.
func BuildGCManifest(input GCManifestInput) (GCManifest, error) {
	actions := sortedRetentionActions(input.Actions)
	manifest := GCManifest{
		SchemaVersion: GCManifestSchemaVersion,
		GeneratedAt:   input.GeneratedAt,
		Mode:          input.Mode,
		DryRun:        input.Mode == GCManifestModeDryRun,
		ConfirmApply:  input.ConfirmApply,
		Summary:       summarizeGCActions(actions),
		Actions:       actions,
	}
	if err := manifest.Validate(); err != nil {
		return GCManifest{}, err
	}
	return manifest, nil
}

// MarshalGCManifestJSON validates and renders stable, human-readable manifest JSON.
func MarshalGCManifestJSON(manifest GCManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

// WriteGCManifestJSON validates and writes stable, human-readable manifest JSON.
func WriteGCManifestJSON(w io.Writer, manifest GCManifest) error {
	data, err := MarshalGCManifestJSON(manifest)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (m GCManifest) Validate() error {
	if m.SchemaVersion != GCManifestSchemaVersion {
		return errors.New("registry GC manifest schemaVersion is invalid")
	}
	if m.GeneratedAt.IsZero() {
		return errors.New("registry GC manifest generatedAt is required")
	}
	summary := summarizeGCActions(m.Actions)
	if !gcSummariesEqual(m.Summary, summary) {
		return errors.New("registry GC manifest summary must match actions")
	}
	switch m.Mode {
	case GCManifestModeDryRun:
		if !m.DryRun {
			return errors.New("registry GC dry-run manifest must set dryRun")
		}
	case GCManifestModeApply:
		if m.DryRun {
			return errors.New("registry GC apply manifest must not set dryRun")
		}
		if summary.Deletions > 0 && !m.ConfirmApply {
			return errors.New("registry GC destructive apply manifest requires confirmApply")
		}
	default:
		return errors.New("registry GC manifest mode is invalid")
	}

	return nil
}

func gcSummariesEqual(left, right GCSummary) bool {
	if left.Repositories != right.Repositories || left.Candidates != right.Candidates || left.Deletions != right.Deletions || left.ProtectedBytes != right.ProtectedBytes || left.DeletedBytes != right.DeletedBytes {
		return false
	}
	if len(left.ReasonCounts) != len(right.ReasonCounts) {
		return false
	}
	for reason, count := range left.ReasonCounts {
		if right.ReasonCounts[reason] != count {
			return false
		}
	}
	return true
}

func sortedRetentionActions(actions []RetentionAction) []RetentionAction {
	sorted := make([]RetentionAction, len(actions))
	for i, action := range actions {
		sorted[i] = action
		sorted[i].Tags = append([]string(nil), action.Tags...)
		sort.Strings(sorted[i].Tags)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if left.Repository != right.Repository {
			return left.Repository < right.Repository
		}
		if left.Digest != right.Digest {
			return left.Digest < right.Digest
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		if left.Delete != right.Delete {
			return !left.Delete && right.Delete
		}
		if left.SizeBytes != right.SizeBytes {
			return left.SizeBytes < right.SizeBytes
		}
		return tagListLess(left.Tags, right.Tags)
	})
	return sorted
}

func tagListLess(left, right []string) bool {
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}
