package acme

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/timeline"
)

func TestNewIngressEvidenceBundleRollsUpAndRendersDeterministicJSON(t *testing.T) {
	bundle, err := NewIngressEvidenceBundle(IngressEvidenceBundleInput{
		RoutePublicationEvidence: validRoutePublicationEvidencePacketForBundleTest(RoutePublicationEvidenceNeedsReview),
		RouteEvidenceIndex:       validIngressRouteReleaseEvidenceIndexForBundleTest(IngressRouteReleaseEvidenceNeedsReview),
		ApplyEvidence: router.RouteArtifactPublicationApplyEvidence{
			Status:       router.RouteArtifactPublicationApplyStatusReady,
			Writes:       1,
			BytesWritten: 42,
		},
	})
	if err != nil {
		t.Fatalf("NewIngressEvidenceBundle returned error: %v", err)
	}
	if bundle.Status != IngressRouteReleaseEvidenceNeedsReview {
		t.Fatalf("Status = %q, want needs-review", bundle.Status)
	}

	encoded, err := RenderIngressEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("RenderIngressEvidenceBundleJSON returned error: %v", err)
	}
	second, err := RenderIngressEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("second render returned error: %v", err)
	}
	if !bytes.Equal(encoded, second) {
		t.Fatalf("JSON was not deterministic:\nfirst: %s\nsecond: %s", encoded, second)
	}
	for _, want := range []string{"schemaVersion", "routePublicationEvidence", "routeEvidenceIndex", "applyEvidence"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON missing %q: %s", want, encoded)
		}
	}
	for _, leaked := range []string{"example.test", "secret", "token", "http://127.0.0.1"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("bundle leaked %q: %s", leaked, encoded)
		}
	}
}

func TestIngressEvidenceBundleValidateRejectsStatusMismatches(t *testing.T) {
	bundle, err := NewIngressEvidenceBundle(IngressEvidenceBundleInput{
		RoutePublicationEvidence: validRoutePublicationEvidencePacketForBundleTest(RoutePublicationEvidenceReady),
		RouteEvidenceIndex:       validIngressRouteReleaseEvidenceIndexForBundleTest(IngressRouteReleaseEvidenceReady),
		ApplyEvidence:            router.RouteArtifactPublicationApplyEvidence{Status: router.RouteArtifactPublicationApplyStatusReady},
	})
	if err != nil {
		t.Fatalf("NewIngressEvidenceBundle returned error: %v", err)
	}

	badRollup := bundle
	badRollup.Status = IngressRouteReleaseEvidenceReady
	badRollup.ApplyEvidence = router.RouteArtifactPublicationApplyEvidence{
		Status:      router.RouteArtifactPublicationApplyStatusError,
		Diagnostics: []string{"code=route_publication_write_failed"},
	}
	if err := badRollup.Validate(); err == nil {
		t.Fatal("Validate returned nil for mismatched bundle rollup")
	}

	badRef := bundle
	badRef.Status = IngressRouteReleaseEvidenceNeedsReview
	badRef.RouteEvidenceIndex.RoutePublicationEvidence.Status = IngressRouteReleaseEvidenceNeedsReview
	badRef.RouteEvidenceIndex.Status = IngressRouteReleaseEvidenceNeedsReview
	if err := badRef.Validate(); err == nil {
		t.Fatal("Validate returned nil for route publication packet/index mismatch")
	}
}

func TestIngressEvidenceBundleReadWriteStrictJSON(t *testing.T) {
	bundle, err := NewIngressEvidenceBundle(IngressEvidenceBundleInput{
		RoutePublicationEvidence: validRoutePublicationEvidencePacketForBundleTest(RoutePublicationEvidenceReady),
		RouteEvidenceIndex:       validIngressRouteReleaseEvidenceIndexForBundleTest(IngressRouteReleaseEvidenceReady),
		ApplyEvidence:            router.RouteArtifactPublicationApplyEvidence{Status: router.RouteArtifactPublicationApplyStatusReady},
	})
	if err != nil {
		t.Fatalf("NewIngressEvidenceBundle returned error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "ingress-bundle.json")
	if err := WriteIngressEvidenceBundleJSON(path, bundle); err != nil {
		t.Fatalf("WriteIngressEvidenceBundleJSON returned error: %v", err)
	}
	read, err := ReadIngressEvidenceBundleJSON(path)
	if err != nil {
		t.Fatalf("ReadIngressEvidenceBundleJSON returned error: %v", err)
	}
	if read.Status != bundle.Status || read.RouteEvidenceIndex.RoutePublicationEvidence.Path != bundle.RouteEvidenceIndex.RoutePublicationEvidence.Path {
		t.Fatalf("read bundle mismatch: %#v", read)
	}

	if err := WriteIngressEvidenceBundleJSON(filepath.Join(t.TempDir(), "ingress-bundle.txt"), bundle); err == nil {
		t.Fatal("WriteIngressEvidenceBundleJSON returned nil for non-json path")
	}
	uncleanPath := t.TempDir() + string(os.PathSeparator) + "nested" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "ingress-bundle.json"
	if err := WriteIngressEvidenceBundleJSON(uncleanPath, bundle); err == nil {
		t.Fatal("WriteIngressEvidenceBundleJSON returned nil for unnormalized path")
	}

	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"schemaVersion":"midnight.dev/ingress-evidence-bundle/v1alpha1","status":"ready","unknown":true}`), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if _, err := ReadIngressEvidenceBundleJSON(badPath); err == nil {
		t.Fatal("ReadIngressEvidenceBundleJSON returned nil for unknown field")
	}

	trailingPath := filepath.Join(t.TempDir(), "trailing.json")
	encoded, err := RenderIngressEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("render valid bundle: %v", err)
	}
	if err := os.WriteFile(trailingPath, append(encoded, []byte(` {}`)...), 0o600); err != nil {
		t.Fatalf("write trailing json: %v", err)
	}
	if _, err := ReadIngressEvidenceBundleJSON(trailingPath); err == nil {
		t.Fatal("ReadIngressEvidenceBundleJSON returned nil for trailing data")
	}
}

func validRoutePublicationEvidencePacketForBundleTest(status RoutePublicationEvidenceStatus) RoutePublicationEvidencePacket {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	input := RoutePublicationEvidenceInput{
		PrewritePlan: router.RouteArtifactPrewritePlan{Skips: 1},
		Publication: router.RoutePublicationSummary{
			Skips:       1,
			NextActions: []string{"no publication needed; artifacts already current"},
		},
		IngressReview: IngressDiagnosticsReviewSummary{
			Status: IngressDiagnosticsHealthy,
			RouteActionCounts: []IngressDiagnosticsActionCount{
				{Action: "error", Count: 0},
				{Action: "skip", Count: 1},
				{Action: "write", Count: 0},
			},
			NextActions: []string{"monitor ingress diagnostics"},
		},
		HeartbeatReview: timeline.ReviewDeploymentHeartbeatGap(timeline.DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-30 * time.Second)}),
	}
	if status == RoutePublicationEvidenceNeedsReview {
		input.PrewritePlan = router.RouteArtifactPrewritePlan{Writes: 1, BytesToWrite: 42}
		input.Publication = router.RoutePublicationSummary{Writes: 1, TotalBytes: 42, NextActions: []string{"publish planned route artifact writes"}}
		input.IngressReview.RouteActionCounts = []IngressDiagnosticsActionCount{
			{Action: "error", Count: 0},
			{Action: "skip", Count: 0},
			{Action: "write", Count: 1},
		}
	}
	return NewRoutePublicationEvidencePacket(input)
}

func validIngressRouteReleaseEvidenceIndexForBundleTest(status IngressRouteReleaseEvidenceStatus) IngressRouteReleaseEvidenceIndex {
	index := validIngressRouteReleaseEvidenceIndexForTest()
	index.Status = status
	index.RoutePublicationEvidence.Status = status
	if status == IngressRouteReleaseEvidenceNeedsReview {
		index.NextActions = []string{"review referenced release evidence before publishing ingress routes"}
	}
	return index
}
