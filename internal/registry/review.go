package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type ReviewStatus string

const (
	ReviewStatusClean       ReviewStatus = "clean"
	ReviewStatusNeedsReview ReviewStatus = "needs-review"
	ReviewStatusBlocked     ReviewStatus = "blocked"
)

type GCManifestReviewSummary struct {
	Status                ReviewStatus `json:"status"`
	ManifestSchemaVersion string       `json:"manifestSchemaVersion,omitempty"`
	Mode                  string       `json:"mode,omitempty"`
	DryRun                bool         `json:"dryRun"`
	ConfirmApply          bool         `json:"confirmApply,omitempty"`
	Candidates            int          `json:"candidates"`
	Deletions             int          `json:"deletions"`
	ProtectedBytes        int64        `json:"protectedBytes"`
	DeletedBytes          int64        `json:"deletedBytes"`
	ValidationErrors      []string     `json:"validationErrors,omitempty"`
}

func NewGCManifestReviewSummary(manifest GCManifest) GCManifestReviewSummary {
	summary := summarizeGCActions(manifest.Actions)
	review := GCManifestReviewSummary{
		ManifestSchemaVersion: manifest.SchemaVersion,
		Mode:                  manifest.Mode,
		DryRun:                manifest.DryRun,
		ConfirmApply:          manifest.ConfirmApply,
		Candidates:            summary.Candidates,
		Deletions:             summary.Deletions,
		ProtectedBytes:        summary.ProtectedBytes,
		DeletedBytes:          summary.DeletedBytes,
	}
	if err := manifest.Validate(); err != nil {
		review.ValidationErrors = []string{err.Error()}
	}
	review.Status = deriveGCManifestReviewStatus(review)
	return review
}

func (s GCManifestReviewSummary) Validate() error {
	if !validReviewStatus(s.Status) {
		return fmt.Errorf("registry GC review status %q is invalid", s.Status)
	}
	if s.Candidates < 0 || s.Deletions < 0 || s.ProtectedBytes < 0 || s.DeletedBytes < 0 {
		return errors.New("registry GC review counts and bytes must not be negative")
	}
	if s.Deletions > s.Candidates {
		return errors.New("registry GC review deletions must not exceed candidates")
	}
	for _, validationError := range s.ValidationErrors {
		if validationError == "" {
			return errors.New("registry GC review validation errors must not be empty")
		}
	}
	if !sort.StringsAreSorted(s.ValidationErrors) {
		return errors.New("registry GC review validation errors must be sorted")
	}
	if want := deriveGCManifestReviewStatus(s); s.Status != want {
		return fmt.Errorf("registry GC review status %q does not match outcome status %q", s.Status, want)
	}
	return nil
}

func MarshalGCManifestReviewSummaryJSON(summary GCManifestReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func deriveGCManifestReviewStatus(summary GCManifestReviewSummary) ReviewStatus {
	if len(summary.ValidationErrors) > 0 {
		return ReviewStatusBlocked
	}
	if summary.Deletions > 0 {
		return ReviewStatusNeedsReview
	}
	return ReviewStatusClean
}

func validReviewStatus(status ReviewStatus) bool {
	switch status {
	case ReviewStatusClean, ReviewStatusNeedsReview, ReviewStatusBlocked:
		return true
	default:
		return false
	}
}
