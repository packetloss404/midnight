package acme

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/timeline"
)

func TestNewIngressRouteReleaseEvidenceIndexRollsUpAndRendersDeterministicJSON(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	packet := NewRoutePublicationEvidencePacket(RoutePublicationEvidenceInput{
		PrewritePlan: router.RouteArtifactPrewritePlan{Errors: 1},
		Publication:  router.RoutePublicationSummary{Errors: 1, NextActions: []string{"review support-safe route artifact diagnostics"}},
		IngressReview: IngressDiagnosticsReviewSummary{
			Status: IngressDiagnosticsNeedsReview,
			RouteActionCounts: []IngressDiagnosticsActionCount{
				{Action: "error", Count: 1},
				{Action: "skip", Count: 0},
				{Action: "write", Count: 0},
			},
		},
		HeartbeatReview: timeline.ReviewDeploymentHeartbeatGap(timeline.DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-30 * time.Second)}),
	})
	heartbeat := timeline.ReviewDeploymentHeartbeatGap(timeline.DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-181 * time.Second)})
	index := NewIngressRouteReleaseEvidenceIndex(IngressRouteReleaseEvidenceIndexInput{
		RoutePublicationEvidenceRef: IngressRouteReleaseEvidenceRef{Path: "evidence/routes/publication.json"},
		ACMEDiagnosticsRef:          IngressRouteReleaseEvidenceRef{Path: "evidence/acme/diagnostics.json"},
		IngressDiagnosticsReviewRef: IngressRouteReleaseEvidenceRef{Path: "evidence/ingress/review.json"},
		DeploymentHeartbeatRef:      IngressRouteReleaseEvidenceRef{Path: "evidence/deployments/heartbeat.json"},
		LogRefs: []IngressRouteReleaseEvidenceRef{
			{Kind: "logs.ndjson", Path: "evidence/logs/b.ndjson"},
			{Kind: "logs.ndjson", Path: "evidence/logs/a.ndjson", Status: IngressRouteReleaseEvidenceNeedsReview},
		},
		TimelineRefs: []IngressRouteReleaseEvidenceRef{
			{Kind: "timeline.json", Path: "evidence/timeline/deployment.json"},
		},
		RoutePublicationEvidence: packet,
		ACMEDiagnostics: []DomainDiagnosticSummary{
			{Actions: []DomainDiagnosticAction{DomainDiagnosticActionCollectMoreTelemetry}},
		},
		IngressDiagnosticsReview: IngressDiagnosticsReviewSummary{
			Status: IngressDiagnosticsNeedsReview,
			RouteActionCounts: []IngressDiagnosticsActionCount{
				{Action: "error", Count: 1},
				{Action: "skip", Count: 0},
				{Action: "write", Count: 0},
			},
			NextActions: []string{"collect additional domain diagnostics telemetry"},
		},
		DeploymentHeartbeatReview: heartbeat,
	})

	if index.Status != IngressRouteReleaseEvidenceBlocked {
		t.Fatalf("Status = %q, want blocked", index.Status)
	}
	if index.RoutePublicationEvidence.Kind != "route-publication-evidence" || index.RoutePublicationEvidence.Status != IngressRouteReleaseEvidenceBlocked {
		t.Fatalf("route publication ref not normalized: %#v", index.RoutePublicationEvidence)
	}
	if len(index.Logs) != 2 || index.Logs[0].Path != "evidence/logs/a.ndjson" || index.Logs[1].Path != "evidence/logs/b.ndjson" {
		t.Fatalf("log refs not sorted: %#v", index.Logs)
	}
	index.RoutePublicationEvidence.SHA256 = sha256ForIngressRouteReleaseEvidenceTest("publication")
	if err := index.Validate(); err != nil {
		t.Fatalf("Validate returned error with sha256 metadata: %v", err)
	}
	assertIngressRouteReleaseEvidenceIssues(t, index.Issues, []IngressRouteReleaseEvidenceIssue{
		{Source: "acme-diagnostics", Code: "collect-more-telemetry", Count: 1},
		{Source: "deployment-heartbeat-review", Code: "deployment_heartbeat_gap_stuck", Count: 1},
		{Source: "route-publication-evidence", Code: "ingress_diagnostics_needs_review", Count: 1},
		{Source: "route-publication-evidence", Code: "route_prewrite_errors", Count: 1},
		{Source: "route-publication-evidence", Code: "route_publication_errors", Count: 1},
	})

	encoded, err := RenderIngressRouteReleaseEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("RenderIngressRouteReleaseEvidenceIndexJSON returned error: %v", err)
	}
	second, err := RenderIngressRouteReleaseEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("second render returned error: %v", err)
	}
	if !bytes.Equal(encoded, second) {
		t.Fatalf("JSON was not deterministic:\nfirst: %s\nsecond: %s", encoded, second)
	}
	for _, want := range []string{"routePublicationEvidence", "acmeDiagnostics", "ingressDiagnosticsReview", "deploymentHeartbeat", "review referenced release evidence before publishing ingress routes"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON missing %q: %s", want, encoded)
		}
	}
	for _, leaked := range []string{"example.test", "secret", "token", "http://127.0.0.1"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("index leaked %q: %s", leaked, encoded)
		}
	}
}

func TestIngressRouteReleaseEvidenceIndexValidateRejectsUnsafePathsAndBadRollup(t *testing.T) {
	index := validIngressRouteReleaseEvidenceIndexForTest()
	index.RoutePublicationEvidence.Path = "../publication.json"
	if err := index.Validate(); err == nil {
		t.Fatal("Validate returned nil for parent-traversing path")
	}

	index = validIngressRouteReleaseEvidenceIndexForTest()
	index.Logs = []IngressRouteReleaseEvidenceRef{{Kind: "logs", Path: `evidence\\logs\\release.ndjson`}}
	if err := index.Validate(); err == nil {
		t.Fatal("Validate returned nil for backslash path")
	}

	index = validIngressRouteReleaseEvidenceIndexForTest()
	index.Status = IngressRouteReleaseEvidenceReady
	index.IngressDiagnosticsReview.Status = IngressRouteReleaseEvidenceNeedsReview
	if err := index.Validate(); err == nil {
		t.Fatal("Validate returned nil for mismatched status rollup")
	}

	index = validIngressRouteReleaseEvidenceIndexForTest()
	index.RoutePublicationEvidence.SHA256 = strings.ToUpper(sha256ForIngressRouteReleaseEvidenceTest("publication"))
	if err := index.Validate(); err == nil {
		t.Fatal("Validate returned nil for uppercase sha256")
	}
}

func TestVerifyIngressRouteReleaseEvidenceIndexFilesChecksExistenceAndSHA256(t *testing.T) {
	root := t.TempDir()
	index := validIngressRouteReleaseEvidenceIndexForTest()
	refs := []*IngressRouteReleaseEvidenceRef{
		&index.RoutePublicationEvidence,
		&index.ACMEDiagnostics,
		&index.IngressDiagnosticsReview,
		&index.DeploymentHeartbeat,
		&index.Logs[0],
		&index.Timeline[0],
	}
	for _, ref := range refs {
		content := "evidence for " + ref.Path
		writeIngressRouteReleaseEvidenceRefFileForTest(t, root, ref.Path, content)
		ref.SHA256 = sha256ForIngressRouteReleaseEvidenceTest(content)
	}

	if err := VerifyIngressRouteReleaseEvidenceIndexFiles(root, index); err != nil {
		t.Fatalf("VerifyIngressRouteReleaseEvidenceIndexFiles returned error: %v", err)
	}

	index.Logs[0].SHA256 = ""
	if err := VerifyIngressRouteReleaseEvidenceIndexFiles(root, index); err != nil {
		t.Fatalf("VerifyIngressRouteReleaseEvidenceIndexFiles returned error for existence-only ref: %v", err)
	}
}

func TestVerifyIngressRouteReleaseEvidenceIndexFilesRejectsMissingAndHashMismatch(t *testing.T) {
	root := t.TempDir()
	index := validIngressRouteReleaseEvidenceIndexForTest()
	if err := VerifyIngressRouteReleaseEvidenceIndexFiles(root, index); err == nil {
		t.Fatal("VerifyIngressRouteReleaseEvidenceIndexFiles returned nil for missing ref")
	}

	refs := []*IngressRouteReleaseEvidenceRef{
		&index.RoutePublicationEvidence,
		&index.ACMEDiagnostics,
		&index.IngressDiagnosticsReview,
		&index.DeploymentHeartbeat,
		&index.Logs[0],
		&index.Timeline[0],
	}
	for _, ref := range refs {
		writeIngressRouteReleaseEvidenceRefFileForTest(t, root, ref.Path, "evidence")
	}
	index.RoutePublicationEvidence.SHA256 = sha256ForIngressRouteReleaseEvidenceTest("different")
	if err := VerifyIngressRouteReleaseEvidenceIndexFiles(root, index); err == nil {
		t.Fatal("VerifyIngressRouteReleaseEvidenceIndexFiles returned nil for hash mismatch")
	}
	if err := VerifyIngressRouteReleaseEvidenceIndexFiles("", index); err == nil {
		t.Fatal("VerifyIngressRouteReleaseEvidenceIndexFiles returned nil for empty root")
	}
}

func TestVerifyIngressRouteReleaseEvidenceIndexFilesRejectsDirectoryRef(t *testing.T) {
	root := t.TempDir()
	index := validIngressRouteReleaseEvidenceIndexForTest()
	refs := []*IngressRouteReleaseEvidenceRef{
		&index.RoutePublicationEvidence,
		&index.ACMEDiagnostics,
		&index.IngressDiagnosticsReview,
		&index.DeploymentHeartbeat,
		&index.Logs[0],
		&index.Timeline[0],
	}
	for _, ref := range refs {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(ref.Path))), 0o700); err != nil {
			t.Fatalf("create evidence parent: %v", err)
		}
		if ref == &index.RoutePublicationEvidence {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(ref.Path)), 0o700); err != nil {
				t.Fatalf("create directory ref: %v", err)
			}
			continue
		}
		writeIngressRouteReleaseEvidenceRefFileForTest(t, root, ref.Path, "evidence")
	}

	if err := VerifyIngressRouteReleaseEvidenceIndexFiles(root, index); err == nil {
		t.Fatal("VerifyIngressRouteReleaseEvidenceIndexFiles returned nil for directory ref")
	}
}

func TestIngressRouteReleaseEvidenceIndexReadWriteStrictJSON(t *testing.T) {
	index := validIngressRouteReleaseEvidenceIndexForTest()
	path := filepath.Join(t.TempDir(), "release-index.json")
	if err := WriteIngressRouteReleaseEvidenceIndexJSON(path, index); err != nil {
		t.Fatalf("WriteIngressRouteReleaseEvidenceIndexJSON returned error: %v", err)
	}
	read, err := ReadIngressRouteReleaseEvidenceIndexJSON(path)
	if err != nil {
		t.Fatalf("ReadIngressRouteReleaseEvidenceIndexJSON returned error: %v", err)
	}
	if read.Status != index.Status || read.RoutePublicationEvidence.Path != index.RoutePublicationEvidence.Path {
		t.Fatalf("read index mismatch: %#v", read)
	}

	if err := WriteIngressRouteReleaseEvidenceIndexJSON(filepath.Join(t.TempDir(), "release-index.txt"), index); err == nil {
		t.Fatal("WriteIngressRouteReleaseEvidenceIndexJSON returned nil for non-json path")
	}
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"status":"ready","unknown":true}`), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if _, err := ReadIngressRouteReleaseEvidenceIndexJSON(badPath); err == nil {
		t.Fatal("ReadIngressRouteReleaseEvidenceIndexJSON returned nil for unknown field")
	}
	trailingPath := filepath.Join(t.TempDir(), "trailing.json")
	encoded, err := RenderIngressRouteReleaseEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("render valid index: %v", err)
	}
	if err := os.WriteFile(trailingPath, append(encoded, []byte(` {}`)...), 0o600); err != nil {
		t.Fatalf("write trailing json: %v", err)
	}
	if _, err := ReadIngressRouteReleaseEvidenceIndexJSON(trailingPath); err == nil {
		t.Fatal("ReadIngressRouteReleaseEvidenceIndexJSON returned nil for trailing data")
	}
}

func validIngressRouteReleaseEvidenceIndexForTest() IngressRouteReleaseEvidenceIndex {
	return IngressRouteReleaseEvidenceIndex{
		Status:                   IngressRouteReleaseEvidenceReady,
		RoutePublicationEvidence: IngressRouteReleaseEvidenceRef{Kind: "route-publication-evidence", Path: "evidence/routes/publication.json", Status: IngressRouteReleaseEvidenceReady},
		ACMEDiagnostics:          IngressRouteReleaseEvidenceRef{Kind: "acme-diagnostics", Path: "evidence/acme/diagnostics.json", Status: IngressRouteReleaseEvidenceReady},
		IngressDiagnosticsReview: IngressRouteReleaseEvidenceRef{Kind: "ingress-diagnostics-review", Path: "evidence/ingress/review.json", Status: IngressRouteReleaseEvidenceReady},
		DeploymentHeartbeat:      IngressRouteReleaseEvidenceRef{Kind: "deployment-heartbeat-review", Path: "evidence/deployments/heartbeat.json", Status: IngressRouteReleaseEvidenceReady},
		Logs:                     []IngressRouteReleaseEvidenceRef{{Kind: "logs.ndjson", Path: "evidence/logs/release.ndjson"}},
		Timeline:                 []IngressRouteReleaseEvidenceRef{{Kind: "timeline.json", Path: "evidence/timeline/deployment.json"}},
		NextActions:              []string{"release evidence index is ready"},
	}
}

func assertIngressRouteReleaseEvidenceIssues(t *testing.T, got, want []IngressRouteReleaseEvidenceIssue) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Issues length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Issues[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func writeIngressRouteReleaseEvidenceRefFileForTest(t *testing.T, root, refPath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(refPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write evidence file: %v", err)
	}
}

func sha256ForIngressRouteReleaseEvidenceTest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:])
}
