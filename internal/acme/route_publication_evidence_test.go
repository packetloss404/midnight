package acme

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/timeline"
)

func TestNewRoutePublicationEvidencePacketBlocksAndRollsUpSupportSafeIssues(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	packet := NewRoutePublicationEvidencePacket(RoutePublicationEvidenceInput{
		PrewritePlan: router.RouteArtifactPrewritePlan{
			Writes:       1,
			Errors:       1,
			BytesToWrite: 64,
			Items: []router.RouteArtifactPrewriteItem{
				{
					Format:       "traefik.dynamic.yaml",
					Hash:         "b-hash",
					Action:       router.RouteArtifactPrewriteActionWrite,
					BytesToWrite: 64,
				},
				{
					RouteGroup:  "secret-route-group",
					Format:      "traefik.dynamic.yaml",
					Hash:        "a-hash",
					Action:      router.RouteArtifactPrewriteActionError,
					Diagnostics: []string{"route artifact validation failed code=route_artifact_stale_hash host=redacted", "route artifact validation failed code=route_artifact_empty"},
				},
			},
		},
		Publication: router.RoutePublicationSummary{
			Writes:      1,
			Errors:      1,
			TotalBytes:  128,
			StaleCount:  1,
			NextActions: []string{"review support-safe route artifact diagnostics"},
		},
		IngressReview: IngressDiagnosticsReviewSummary{
			Status:           IngressDiagnosticsBlocked,
			DomainErrorCount: 1,
			RouteStaleCount:  1,
			RouteErrorCount:  1,
			RouteActionCounts: []IngressDiagnosticsActionCount{
				{Action: "error", Count: 1},
				{Action: "skip", Count: 0},
				{Action: "write", Count: 1},
			},
			NextActions: []string{"replace expired certificates before routing traffic"},
		},
		HeartbeatReview: timeline.ReviewDeploymentHeartbeatGap(timeline.DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-181 * time.Second)}),
	})

	if packet.Status != RoutePublicationEvidenceBlocked {
		t.Fatalf("Status = %q, want blocked", packet.Status)
	}
	if packet.BytesToWrite != 128 || packet.Prewrite.BytesToWrite != 64 {
		t.Fatalf("bytes = packet %d prewrite %d, want 128 and 64", packet.BytesToWrite, packet.Prewrite.BytesToWrite)
	}
	if len(packet.Prewrite.Items) != 2 || packet.Prewrite.Items[0].Hash != "a-hash" || packet.Prewrite.Items[1].Hash != "b-hash" {
		t.Fatalf("prewrite items not sorted support-safely: %#v", packet.Prewrite.Items)
	}
	assertRoutePublicationEvidenceIssues(t, packet.Issues, []RoutePublicationEvidenceIssue{
		{Source: "heartbeat", Code: "deployment_heartbeat_gap_stuck", Count: 1},
		{Source: "ingress", Code: "ingress_diagnostics_blocked", Count: 1},
		{Source: "ingress", Code: "ingress_domain_errors", Count: 1},
		{Source: "ingress", Code: "ingress_route_errors", Count: 1},
		{Source: "ingress", Code: "ingress_route_stale", Count: 1},
		{Source: "prewrite", Code: "route_artifact_empty", Count: 1},
		{Source: "prewrite", Code: "route_artifact_stale_hash", Count: 1},
		{Source: "prewrite", Code: "route_prewrite_errors", Count: 1},
		{Source: "publication", Code: "route_publication_errors", Count: 1},
		{Source: "publication", Code: "route_publication_stale_artifacts", Count: 1},
	})

	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	second, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("second Marshal returned error: %v", err)
	}
	if string(encoded) != string(second) {
		t.Fatalf("JSON was not deterministic:\nfirst: %s\nsecond: %s", encoded, second)
	}
	for _, want := range []string{"review deployment heartbeat telemetry", "review support-safe route artifact diagnostics", "replace expired certificates before routing traffic"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON missing next action %q: %s", want, encoded)
		}
	}
	for _, leaked := range []string{"secret-route-group", "example.test", "http://127.0.0.1", "token"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("evidence packet leaked %q: %s", leaked, encoded)
		}
	}
}

func TestNewRoutePublicationEvidencePacketReadyAndNeedsReview(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	ready := NewRoutePublicationEvidencePacket(RoutePublicationEvidenceInput{
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
	})
	if ready.Status != RoutePublicationEvidenceReady {
		t.Fatalf("ready Status = %q, want ready", ready.Status)
	}
	if len(ready.Issues) != 0 {
		t.Fatalf("ready Issues = %#v, want none", ready.Issues)
	}

	needsReview := NewRoutePublicationEvidencePacket(RoutePublicationEvidenceInput{
		PrewritePlan: router.RouteArtifactPrewritePlan{Writes: 1, BytesToWrite: 42},
		Publication:  router.RoutePublicationSummary{Writes: 1, TotalBytes: 42},
		IngressReview: IngressDiagnosticsReviewSummary{
			Status: IngressDiagnosticsHealthy,
			RouteActionCounts: []IngressDiagnosticsActionCount{
				{Action: "error", Count: 0},
				{Action: "skip", Count: 0},
				{Action: "write", Count: 1},
			},
		},
		HeartbeatReview: timeline.ReviewDeploymentHeartbeatGap(timeline.DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-30 * time.Second)}),
	})
	if needsReview.Status != RoutePublicationEvidenceNeedsReview {
		t.Fatalf("needsReview Status = %q, want needs-review", needsReview.Status)
	}
	if needsReview.BytesToWrite != 42 {
		t.Fatalf("BytesToWrite = %d, want 42", needsReview.BytesToWrite)
	}
}

func assertRoutePublicationEvidenceIssues(t *testing.T, got, want []RoutePublicationEvidenceIssue) {
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
