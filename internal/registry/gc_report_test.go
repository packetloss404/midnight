package registry

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewGCDryRunReportJSONShape(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	report := NewGCDryRunReport(RetentionPlan{
		GeneratedAt: now,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
		},
	})

	if report.SchemaVersion != GCDryRunReportSchemaVersion || !report.DryRun {
		t.Fatalf("report contract = %#v", report)
	}
	if report.Summary.Repositories != 1 || report.Summary.Candidates != 2 || report.Summary.Deletions != 1 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	if report.Summary.ProtectedBytes != 10 || report.Summary.DeletedBytes != 20 {
		t.Fatalf("summary bytes = %#v", report.Summary)
	}
	if report.Summary.ReasonCounts["active pin"] != 1 || report.Summary.ReasonCounts["unprotected"] != 1 {
		t.Fatalf("summary reasons = %#v", report.Summary.ReasonCounts)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("invalid JSON: %s", encoded)
	}
}

func TestNewGCApplyReportIncludesByteTotals(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	result := ApplyGCPlan(GCApplyRequest{Plan: RetentionPlan{
		GeneratedAt: now,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
		},
	}, ConfirmApply: true})

	report := NewGCApplyReport(result)
	if report.SchemaVersion != GCApplyReportSchemaVersion || report.DryRun || !report.ConfirmApply || report.AppliedAt != now {
		t.Fatalf("apply report contract = %#v", report)
	}
	if report.Summary.ProtectedBytes != 10 || report.Summary.DeletedBytes != 20 || len(report.Actions) != 1 {
		t.Fatalf("apply report summary/actions = %#v", report)
	}
	if report.Summary.ReasonCounts["active pin"] != 1 || report.Summary.ReasonCounts["unprotected"] != 1 {
		t.Fatalf("apply report reasons = %#v", report.Summary.ReasonCounts)
	}
}

func TestBuildGCManifestWritesDeterministicJSON(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildGCManifest(GCManifestInput{
		GeneratedAt:  now,
		Mode:         GCManifestModeApply,
		ConfirmApply: true,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:old", Tags: []string{"old"}, Reason: "unprotected", Delete: true, SizeBytes: 20},
			{Repository: "app", Digest: "sha256:keep", Tags: []string{"stable", "latest"}, Reason: "active pin", SizeBytes: 10},
		},
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}

	if manifest.Actions[0].Digest != "sha256:keep" || manifest.Actions[0].Tags[0] != "latest" {
		t.Fatalf("BuildGCManifest() actions = %#v, want sorted actions and tags", manifest.Actions)
	}

	var buf bytes.Buffer
	if err := WriteGCManifestJSON(&buf, manifest); err != nil {
		t.Fatalf("WriteGCManifestJSON() error = %v", err)
	}

	want := `{
  "schemaVersion": "midnight.dev/registry-gc-manifest/v1alpha1",
  "generatedAt": "2026-04-25T12:00:00Z",
  "mode": "apply",
  "dryRun": false,
  "confirmApply": true,
  "summary": {
    "repositories": 1,
    "candidates": 2,
    "deletions": 1,
    "protectedBytes": 10,
    "deletedBytes": 20,
    "reasonCounts": {
      "active pin": 1,
      "unprotected": 1
    }
  },
  "actions": [
    {
      "repository": "app",
      "digest": "sha256:keep",
      "tags": [
        "latest",
        "stable"
      ],
      "reason": "active pin",
      "delete": false,
      "sizeBytes": 10
    },
    {
      "repository": "app",
      "digest": "sha256:old",
      "tags": [
        "old"
      ],
      "reason": "unprotected",
      "delete": true,
      "sizeBytes": 20
    }
  ]
}`
	if buf.String() != want {
		t.Fatalf("WriteGCManifestJSON() =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestBuildGCManifestRejectsUnconfirmedDestructiveApply(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	_, err := BuildGCManifest(GCManifestInput{
		GeneratedAt: now,
		Mode:        GCManifestModeApply,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires confirmApply") {
		t.Fatalf("BuildGCManifest() error = %v, want confirmApply validation", err)
	}
}

func TestWriteGCManifestRejectsSummaryMismatch(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest := GCManifest{
		SchemaVersion: GCManifestSchemaVersion,
		GeneratedAt:   now,
		Mode:          GCManifestModeDryRun,
		DryRun:        true,
		Summary:       GCSummary{},
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
		},
	}

	if err := WriteGCManifestJSON(&bytes.Buffer{}, manifest); err == nil || !strings.Contains(err.Error(), "summary must match actions") {
		t.Fatalf("WriteGCManifestJSON() error = %v, want summary validation", err)
	}
}
