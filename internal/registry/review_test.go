package registry

import (
	"strings"
	"testing"
	"time"
)

func TestNewGCManifestReviewSummaryWritesDeterministicNeedsReviewJSON(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildGCManifest(GCManifestInput{
		GeneratedAt:  now,
		Mode:         GCManifestModeApply,
		ConfirmApply: true,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
		},
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}

	review := NewGCManifestReviewSummary(manifest)
	encoded, err := MarshalGCManifestReviewSummaryJSON(review)
	if err != nil {
		t.Fatalf("MarshalGCManifestReviewSummaryJSON() error = %v", err)
	}

	want := `{
  "status": "needs-review",
  "manifestSchemaVersion": "midnight.dev/registry-gc-manifest/v1alpha1",
  "mode": "apply",
  "dryRun": false,
  "confirmApply": true,
  "candidates": 2,
  "deletions": 1,
  "protectedBytes": 10,
  "deletedBytes": 20
}`
	if string(encoded) != want {
		t.Fatalf("MarshalGCManifestReviewSummaryJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestNewGCManifestReviewSummaryDerivesCleanStatus(t *testing.T) {
	manifest, err := BuildGCManifest(GCManifestInput{
		GeneratedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Mode:        GCManifestModeDryRun,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
		},
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}

	review := NewGCManifestReviewSummary(manifest)
	if review.Status != ReviewStatusClean {
		t.Fatalf("Status = %q, want %q", review.Status, ReviewStatusClean)
	}
}

func TestNewGCManifestReviewSummaryBlocksInvalidManifest(t *testing.T) {
	review := NewGCManifestReviewSummary(GCManifest{
		SchemaVersion: GCManifestSchemaVersion,
		GeneratedAt:   time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Mode:          GCManifestModeApply,
		DryRun:        false,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
		},
	})

	if review.Status != ReviewStatusBlocked {
		t.Fatalf("Status = %q, want %q", review.Status, ReviewStatusBlocked)
	}
	if len(review.ValidationErrors) != 1 || !strings.Contains(review.ValidationErrors[0], "summary must match actions") {
		t.Fatalf("ValidationErrors = %#v, want manifest validation error", review.ValidationErrors)
	}
	if err := review.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestGCManifestReviewSummaryValidateRejectsInconsistentStatus(t *testing.T) {
	review := GCManifestReviewSummary{Status: ReviewStatusClean, Candidates: 1, Deletions: 1}

	if err := review.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want status mismatch", err)
	}
}
