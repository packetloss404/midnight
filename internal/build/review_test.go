package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/registry"
)

func TestNewBuildReviewSummaryWritesDeterministicBlockedJSON(t *testing.T) {
	summary, err := NewBuildReviewSummary(BuildReviewInput{
		RefusedContextFiles: []string{"secrets/token.txt", " .env "},
		OutputSecretFindings: []BuildOutputSecretFinding{
			{Prefix: "AKIA", Line: 2, Column: 8, Redacted: "AKIA<redacted>"},
			{Prefix: "midn_", Line: 1, Column: 4, Redacted: "midn_<redacted>"},
		},
		ProvenanceArtifactRefs: []ArtifactRef{
			{Name: "sbom", Digest: "sha256:bbbb", MediaType: "application/spdx+json"},
			{Name: "provenance", Digest: "sha256:aaaa", MediaType: "application/vnd.in-toto+json"},
		},
	})
	if err != nil {
		t.Fatalf("NewBuildReviewSummary() error = %v", err)
	}

	encoded, err := MarshalBuildReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalBuildReviewSummaryJSON() error = %v", err)
	}

	want := `{
  "status": "blocked",
  "refusedContextFiles": [
    ".env",
    "secrets/token.txt"
  ],
  "outputSecretFindings": [
    {
      "prefix": "midn_",
      "line": 1,
      "column": 4,
      "redacted": "midn_<redacted>"
    },
    {
      "prefix": "AKIA",
      "line": 2,
      "column": 8,
      "redacted": "AKIA<redacted>"
    }
  ],
  "provenanceArtifactRefs": [
    {
      "name": "provenance",
      "digest": "sha256:aaaa",
      "mediaType": "application/vnd.in-toto+json"
    },
    {
      "name": "sbom",
      "digest": "sha256:bbbb",
      "mediaType": "application/spdx+json"
    }
  ]
}`
	if string(encoded) != want {
		t.Fatalf("MarshalBuildReviewSummaryJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestNewBuildReviewSummaryDerivesStatuses(t *testing.T) {
	tests := []struct {
		name  string
		input BuildReviewInput
		want  ReviewStatus
	}{
		{name: "clean", want: ReviewStatusClean},
		{name: "needs review", input: BuildReviewInput{ProvenanceArtifactRefs: []ArtifactRef{{Name: "provenance", Digest: "sha256:aaaa"}}}, want: ReviewStatusNeedsReview},
		{name: "blocked by refused context", input: BuildReviewInput{RefusedContextFiles: []string{".env"}}, want: ReviewStatusBlocked},
		{name: "blocked by secret output", input: BuildReviewInput{OutputSecretFindings: []BuildOutputSecretFinding{{Prefix: "AKIA", Line: 1, Column: 1, Redacted: "AKIA<redacted>"}}}, want: ReviewStatusBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := NewBuildReviewSummary(tt.input)
			if err != nil {
				t.Fatalf("NewBuildReviewSummary() error = %v", err)
			}
			if summary.Status != tt.want {
				t.Fatalf("Status = %q, want %q", summary.Status, tt.want)
			}
		})
	}
}

func TestBuildReviewSummaryValidateRejectsInconsistentStatus(t *testing.T) {
	summary := BuildReviewSummary{Status: ReviewStatusClean, RefusedContextFiles: []string{".env"}}

	if err := summary.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want status mismatch", err)
	}
}

func TestBuildReviewSummaryValidateRejectsInvalidFinding(t *testing.T) {
	summary := BuildReviewSummary{
		Status: ReviewStatusBlocked,
		OutputSecretFindings: []BuildOutputSecretFinding{
			{Prefix: "AKIA", Column: 1, Redacted: "AKIA<redacted>"},
		},
	}

	if err := summary.Validate(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("Validate() error = %v, want invalid finding", err)
	}
}

func TestNewReleaseArtifactReviewSummaryWritesDeterministicBlockedJSON(t *testing.T) {
	summary := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest: " sha256:source ",
		ImageDigests: []string{"sha256:image-b", " ", "sha256:image-a"},
		ProvenanceRef: ArtifactRef{
			Name:      "app.intoto.jsonl",
			Digest:    "sha256:prov",
			MediaType: "application/vnd.in-toto+json",
		},
		SBOMRef: ArtifactRef{
			Name:      "app.spdx.json",
			Digest:    "sha256:sbom",
			MediaType: "application/spdx+json",
		},
		UntrustedBuild: UntrustedBuildUntrusted,
	})

	encoded, err := MarshalReleaseArtifactReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalReleaseArtifactReviewSummaryJSON() error = %v", err)
	}

	want := `{
  "status": "blocked",
  "sourceDigest": "sha256:source",
  "imageDigests": [
    "sha256:image-a",
    "sha256:image-b"
  ],
  "provenanceRef": {
    "name": "app.intoto.jsonl",
    "digest": "sha256:prov",
    "mediaType": "application/vnd.in-toto+json"
  },
  "sbomRef": {
    "name": "app.spdx.json",
    "digest": "sha256:sbom",
    "mediaType": "application/spdx+json"
  },
  "issues": [
    {
      "code": "missing-image-digest",
      "detail": "imageDigests[1]"
    },
    {
      "code": "untrusted-build-review-required",
      "detail": "untrusted"
    }
  ]
}`
	if string(encoded) != want {
		t.Fatalf("MarshalReleaseArtifactReviewSummaryJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestNewReleaseArtifactReviewSummaryDerivesStatuses(t *testing.T) {
	valid := ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
		SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
		UntrustedBuild: UntrustedBuildTrusted,
	}

	tests := []struct {
		name   string
		mutate func(*ReleaseArtifactReviewInput)
		want   ReviewStatus
		code   ReleaseArtifactIssueCode
	}{
		{name: "clean", want: ReviewStatusClean},
		{name: "untrusted needs review", mutate: func(input *ReleaseArtifactReviewInput) { input.UntrustedBuild = UntrustedBuildIsolated }, want: ReviewStatusNeedsReview, code: ReleaseArtifactIssueUntrustedBuildReviewRequired},
		{name: "missing provenance blocks", mutate: func(input *ReleaseArtifactReviewInput) { input.ProvenanceRef = ArtifactRef{} }, want: ReviewStatusBlocked, code: ReleaseArtifactIssueMissingProvenanceRef},
		{name: "missing sbom blocks", mutate: func(input *ReleaseArtifactReviewInput) { input.SBOMRef = ArtifactRef{} }, want: ReviewStatusBlocked, code: ReleaseArtifactIssueMissingSBOMRef},
		{name: "missing source digest blocks", mutate: func(input *ReleaseArtifactReviewInput) { input.SourceDigest = "" }, want: ReviewStatusBlocked, code: ReleaseArtifactIssueMissingSourceDigest},
		{name: "missing image digest blocks", mutate: func(input *ReleaseArtifactReviewInput) { input.ImageDigests = nil }, want: ReviewStatusBlocked, code: ReleaseArtifactIssueMissingImageDigest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			if tt.mutate != nil {
				tt.mutate(&input)
			}

			summary := NewReleaseArtifactReviewSummary(input)
			if summary.Status != tt.want {
				t.Fatalf("Status = %q, want %q", summary.Status, tt.want)
			}
			if tt.code != "" && !releaseArtifactReviewHasIssue(summary, tt.code) {
				t.Fatalf("Issues = %#v, want code %q", summary.Issues, tt.code)
			}
		})
	}
}

func TestReleaseArtifactReviewSummaryRejectsInvalidProvenanceRef(t *testing.T) {
	summary := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "app:latest"},
		SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
		UntrustedBuild: UntrustedBuildTrusted,
	})

	if summary.Status != ReviewStatusBlocked {
		t.Fatalf("Status = %q, want %q", summary.Status, ReviewStatusBlocked)
	}
	if !releaseArtifactReviewHasIssue(summary, ReleaseArtifactIssueInvalidProvenanceRef) {
		t.Fatalf("Issues = %#v, want invalid provenance issue", summary.Issues)
	}
}

func TestReleaseArtifactReviewSummaryValidateRejectsInconsistentStatus(t *testing.T) {
	summary := ReleaseArtifactReviewSummary{
		Status: ReviewStatusClean,
		Issues: []ReleaseArtifactReviewIssue{{Code: ReleaseArtifactIssueMissingSBOMRef}},
	}

	if err := summary.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want status mismatch", err)
	}
}

func TestNewBuildKitCacheIntegrityReviewSummaryWritesDeterministicCorruptJSON(t *testing.T) {
	summary, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{
		VerificationFindings: []BuildKitCacheVerificationFinding{
			{Code: "missing-cache-manifest", Detail: "worker b"},
			{Code: "digest-mismatch", Detail: "worker a"},
		},
		CorruptRecords: 2,
		CorruptBlobs:   1,
	})
	if err != nil {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
	}

	encoded, err := MarshalBuildKitCacheIntegrityReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalBuildKitCacheIntegrityReviewSummaryJSON() error = %v", err)
	}

	want := `{
  "status": "corrupt",
  "verificationFindings": [
    {
      "code": "digest-mismatch",
      "detail": "worker a"
    },
    {
      "code": "missing-cache-manifest",
      "detail": "worker b"
    }
  ],
  "corruptRecords": 2,
  "corruptBlobs": 1,
  "issues": [
    {
      "code": "corrupt-blobs",
      "detail": "1",
      "remediation": "Remove corrupt BuildKit cache blobs and repopulate cache from a trusted build."
    },
    {
      "code": "corrupt-records",
      "detail": "2",
      "remediation": "Prune corrupt BuildKit cache records and rebuild cache from trusted inputs."
    },
    {
      "code": "verification-finding",
      "detail": "digest-mismatch: worker a",
      "remediation": "Review BuildKit cache verification output and rebuild affected cache entries before promotion."
    },
    {
      "code": "verification-finding",
      "detail": "missing-cache-manifest: worker b",
      "remediation": "Review BuildKit cache verification output and rebuild affected cache entries before promotion."
    }
  ]
}`
	if string(encoded) != want {
		t.Fatalf("MarshalBuildKitCacheIntegrityReviewSummaryJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestNewBuildKitCacheIntegrityReviewSummaryDerivesStatuses(t *testing.T) {
	tests := []struct {
		name  string
		input BuildKitCacheIntegrityReviewInput
		want  BuildKitCacheIntegrityStatus
	}{
		{name: "clean", want: BuildKitCacheIntegrityStatusClean},
		{name: "needs review", input: BuildKitCacheIntegrityReviewInput{VerificationFindings: []BuildKitCacheVerificationFinding{{Code: "metadata-warning"}}}, want: BuildKitCacheIntegrityStatusNeedsReview},
		{name: "corrupt records", input: BuildKitCacheIntegrityReviewInput{CorruptRecords: 1}, want: BuildKitCacheIntegrityStatusCorrupt},
		{name: "corrupt blobs", input: BuildKitCacheIntegrityReviewInput{CorruptBlobs: 1}, want: BuildKitCacheIntegrityStatusCorrupt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := NewBuildKitCacheIntegrityReviewSummary(tt.input)
			if err != nil {
				t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
			}
			if summary.Status != tt.want {
				t.Fatalf("Status = %q, want %q", summary.Status, tt.want)
			}
		})
	}
}

func TestBuildKitCacheIntegrityReviewSummaryValidateRejectsInconsistentStatus(t *testing.T) {
	summary := BuildKitCacheIntegrityReviewSummary{Status: BuildKitCacheIntegrityStatusClean, CorruptRecords: 1}

	if err := summary.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want status mismatch", err)
	}
}

func TestNewBuildKitCacheIntegrityReviewSummaryRejectsNegativeCounts(t *testing.T) {
	_, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{CorruptRecords: -1})

	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v, want negative count error", err)
	}
}

func TestNewReleaseEvidencePacketWritesDeterministicBlockedJSON(t *testing.T) {
	contextReview, err := NewBuildReviewSummary(BuildReviewInput{
		RefusedContextFiles: []string{" .env "},
		ProvenanceArtifactRefs: []ArtifactRef{
			{Name: "app.intoto.jsonl", Digest: "sha256:prov", MediaType: "application/vnd.in-toto+json"},
		},
	})
	if err != nil {
		t.Fatalf("NewBuildReviewSummary() error = %v", err)
	}
	artifactReview := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
		UntrustedBuild: UntrustedBuildTrusted,
	})
	cacheReview, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{
		VerificationFindings: []BuildKitCacheVerificationFinding{{Code: "metadata-warning", Detail: "worker a"}},
	})
	if err != nil {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
	}
	manifest, err := registry.BuildGCManifest(registry.GCManifestInput{
		GeneratedAt:  time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Mode:         registry.GCManifestModeApply,
		ConfirmApply: true,
		Actions: []registry.RetentionAction{
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true, SizeBytes: 20},
		},
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}

	packet, err := NewReleaseEvidencePacket(ReleaseEvidencePacketInput{
		ContextReview:         contextReview,
		ReleaseArtifactReview: artifactReview,
		CacheIntegrityReview:  cacheReview,
		RegistryGCReview:      registry.NewGCManifestReviewSummary(manifest),
	})
	if err != nil {
		t.Fatalf("NewReleaseEvidencePacket() error = %v", err)
	}
	encoded, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}

	want := `{
  "status": "blocked",
  "contextReview": {
    "status": "blocked",
    "refusedContextFiles": [
      ".env"
    ],
    "provenanceArtifactRefs": [
      {
        "name": "app.intoto.jsonl",
        "digest": "sha256:prov",
        "mediaType": "application/vnd.in-toto+json"
      }
    ]
  },
  "releaseArtifactReview": {
    "status": "blocked",
    "sourceDigest": "sha256:source",
    "imageDigests": [
      "sha256:image"
    ],
    "provenanceRef": {
      "name": "app.intoto.jsonl",
      "digest": "sha256:prov"
    },
    "sbomRef": {
      "name": "",
      "digest": ""
    },
    "issues": [
      {
        "code": "missing-sbom-ref"
      }
    ]
  },
  "cacheIntegrityReview": {
    "status": "needs-review",
    "verificationFindings": [
      {
        "code": "metadata-warning",
        "detail": "worker a"
      }
    ],
    "issues": [
      {
        "code": "verification-finding",
        "detail": "metadata-warning: worker a",
        "remediation": "Review BuildKit cache verification output and rebuild affected cache entries before promotion."
      }
    ]
  },
  "registryGCReview": {
    "status": "needs-review",
    "manifestSchemaVersion": "midnight.dev/registry-gc-manifest/v1alpha1",
    "mode": "apply",
    "dryRun": false,
    "confirmApply": true,
    "candidates": 1,
    "deletions": 1,
    "protectedBytes": 0,
    "deletedBytes": 20
  },
  "issues": [
    {
      "source": "context",
      "status": "blocked",
      "code": "refused-context-file",
      "detail": ".env"
    },
    {
      "source": "context",
      "status": "needs-review",
      "code": "provenance-artifact-ref",
      "detail": "app.intoto.jsonl sha256:prov application/vnd.in-toto+json"
    },
    {
      "source": "release-artifacts",
      "status": "blocked",
      "code": "missing-sbom-ref"
    },
    {
      "source": "cache-integrity",
      "status": "needs-review",
      "code": "verification-finding",
      "detail": "metadata-warning: worker a"
    },
    {
      "source": "registry-gc",
      "status": "needs-review",
      "code": "deletion-candidates",
      "detail": "1"
    }
  ]
}`
	if string(encoded) != want {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestNewReleaseEvidencePacketDerivesStatuses(t *testing.T) {
	cleanContext, err := NewBuildReviewSummary(BuildReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildReviewSummary() error = %v", err)
	}
	cleanArtifact := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
		SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
		UntrustedBuild: UntrustedBuildTrusted,
	})
	cleanCache, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
	}
	cleanManifest, err := registry.BuildGCManifest(registry.GCManifestInput{
		GeneratedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Mode:        registry.GCManifestModeDryRun,
		Actions: []registry.RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
		},
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}
	cleanRegistry := registry.NewGCManifestReviewSummary(cleanManifest)

	tests := []struct {
		name   string
		mutate func(*ReleaseEvidencePacketInput)
		want   ReviewStatus
	}{
		{name: "clean", want: ReviewStatusClean},
		{name: "needs review", mutate: func(input *ReleaseEvidencePacketInput) {
			input.ReleaseArtifactReview = NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
				SourceDigest:   "sha256:source",
				ImageDigests:   []string{"sha256:image"},
				ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
				SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
				UntrustedBuild: UntrustedBuildIsolated,
			})
		}, want: ReviewStatusNeedsReview},
		{name: "blocked", mutate: func(input *ReleaseEvidencePacketInput) {
			blockedContext, err := NewBuildReviewSummary(BuildReviewInput{RefusedContextFiles: []string{".env"}})
			if err != nil {
				t.Fatalf("NewBuildReviewSummary() error = %v", err)
			}
			input.ContextReview = blockedContext
		}, want: ReviewStatusBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := ReleaseEvidencePacketInput{
				ContextReview:         cleanContext,
				ReleaseArtifactReview: cleanArtifact,
				CacheIntegrityReview:  cleanCache,
				RegistryGCReview:      cleanRegistry,
			}
			if tt.mutate != nil {
				tt.mutate(&input)
			}

			packet, err := NewReleaseEvidencePacket(input)
			if err != nil {
				t.Fatalf("NewReleaseEvidencePacket() error = %v", err)
			}
			if packet.Status != tt.want {
				t.Fatalf("Status = %q, want %q", packet.Status, tt.want)
			}
		})
	}
}

func TestReleaseEvidencePacketValidateRejectsInconsistentIssues(t *testing.T) {
	contextReview, err := NewBuildReviewSummary(BuildReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildReviewSummary() error = %v", err)
	}
	artifactReview := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
		SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
		UntrustedBuild: UntrustedBuildTrusted,
	})
	cacheReview, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
	}
	manifest, err := registry.BuildGCManifest(registry.GCManifestInput{GeneratedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), Mode: registry.GCManifestModeDryRun})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}
	packet, err := NewReleaseEvidencePacket(ReleaseEvidencePacketInput{
		ContextReview:         contextReview,
		ReleaseArtifactReview: artifactReview,
		CacheIntegrityReview:  cacheReview,
		RegistryGCReview:      registry.NewGCManifestReviewSummary(manifest),
	})
	if err != nil {
		t.Fatalf("NewReleaseEvidencePacket() error = %v", err)
	}
	packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceRegistryGC, Status: ReviewStatusNeedsReview, Code: "unexpected"})

	if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("Validate() error = %v, want issue mismatch", err)
	}
}

func TestWriteAndReadReleaseEvidencePacketJSONFile(t *testing.T) {
	packet := cleanReleaseEvidencePacketForTest(t)
	path := filepath.Join(t.TempDir(), "release-evidence.json")

	if err := WriteReleaseEvidencePacketJSONFile(path, packet); err != nil {
		t.Fatalf("WriteReleaseEvidencePacketJSONFile() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("written release evidence =\n%s\nwant\n%s", written, want)
	}

	read, err := ReadReleaseEvidencePacketJSONFile(path)
	if err != nil {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile() error = %v", err)
	}
	if err := read.Validate(); err != nil {
		t.Fatalf("read packet Validate() error = %v", err)
	}
}

func TestReleaseEvidencePacketJSONFileRejectsInvalidPaths(t *testing.T) {
	packet := cleanReleaseEvidencePacketForTest(t)
	tempDir := t.TempDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: " ", want: "must not be empty"},
		{name: "not json", path: filepath.Join(tempDir, "release-evidence.txt"), want: "explicit .json file path"},
		{name: "directory", path: tempDir, want: "explicit .json file path"},
		{name: "missing parent", path: filepath.Join(tempDir, "missing", "release-evidence.json"), want: "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteReleaseEvidencePacketJSONFile(tt.path, packet)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WriteReleaseEvidencePacketJSONFile() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReadReleaseEvidencePacketJSONFileValidatesContent(t *testing.T) {
	packet := cleanReleaseEvidencePacketForTest(t)
	encoded, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}
	tampered := strings.Replace(string(encoded), `"status": "clean"`, `"status": "blocked"`, 1)
	path := filepath.Join(t.TempDir(), "release-evidence.json")
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = ReadReleaseEvidencePacketJSONFile(path)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile() error = %v, want validation error", err)
	}
}

func releaseArtifactReviewHasIssue(summary ReleaseArtifactReviewSummary, code ReleaseArtifactIssueCode) bool {
	for _, issue := range summary.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func cleanReleaseEvidencePacketForTest(t *testing.T) ReleaseEvidencePacket {
	t.Helper()

	contextReview, err := NewBuildReviewSummary(BuildReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildReviewSummary() error = %v", err)
	}
	artifactReview := NewReleaseArtifactReviewSummary(ReleaseArtifactReviewInput{
		SourceDigest:   "sha256:source",
		ImageDigests:   []string{"sha256:image"},
		ProvenanceRef:  ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
		SBOMRef:        ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
		UntrustedBuild: UntrustedBuildTrusted,
	})
	cacheReview, err := NewBuildKitCacheIntegrityReviewSummary(BuildKitCacheIntegrityReviewInput{})
	if err != nil {
		t.Fatalf("NewBuildKitCacheIntegrityReviewSummary() error = %v", err)
	}
	manifest, err := registry.BuildGCManifest(registry.GCManifestInput{
		GeneratedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Mode:        registry.GCManifestModeDryRun,
	})
	if err != nil {
		t.Fatalf("BuildGCManifest() error = %v", err)
	}
	packet, err := NewReleaseEvidencePacket(ReleaseEvidencePacketInput{
		ContextReview:         contextReview,
		ReleaseArtifactReview: artifactReview,
		CacheIntegrityReview:  cacheReview,
		RegistryGCReview:      registry.NewGCManifestReviewSummary(manifest),
	})
	if err != nil {
		t.Fatalf("NewReleaseEvidencePacket() error = %v", err)
	}
	return packet
}
