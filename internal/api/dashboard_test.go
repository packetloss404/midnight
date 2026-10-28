package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/agentapi"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildPhase0ProofPacketSummaryReadsRecordedArtifacts(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)

	got := buildPhase0ProofPacketSummary(root)

	if got.SchemaVersion != phase0ProofPacketSummarySchemaVersion || got.Status != phase0ProofStatusReady {
		t.Fatalf("summary header = %#v", got)
	}
	if got.ReleaseEvidence.Status != phase0ProofStatusReady || !got.ReleaseEvidence.Present {
		t.Fatalf("release evidence = %#v", got.ReleaseEvidence)
	}
	if got.EvidenceIndexVerification.Status != "pass" || got.EvidenceIndexVerification.Entries != 3 || !got.EvidenceIndexVerification.FilesChecked {
		t.Fatalf("evidence index verification = %#v", got.EvidenceIndexVerification)
	}
	if !got.ReleaseSummary.ReleaseReady || got.ReleaseSummary.Verdict != "ready" || got.ReleaseSummary.ManifestHash != "abc123" {
		t.Fatalf("release summary = %#v", got.ReleaseSummary)
	}
	if !got.Command.Available ||
		!strings.Contains(got.Command.Value, "--require-executed-drills") ||
		!strings.Contains(got.Command.Value, "--verify-evidence-files") ||
		!strings.Contains(got.Command.Value, "--release-summary artifacts/phase0/release-summary.json") ||
		!strings.Contains(got.Command.Value, "--evidence-ref operator-notes=artifacts/phase0/operator-notes.md") {
		t.Fatalf("command = %#v", got.Command)
	}
	if got.RunnerOS.Value != "linux/amd64" || got.CommitSHA.Value != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("runner/commit = %#v %#v", got.RunnerOS, got.CommitSHA)
	}
	if !got.OperatorNotes.Available || got.OperatorNotes.RunID != "20260426T120000Z" || got.OperatorNotes.Result != "pass" {
		t.Fatalf("operator notes = %#v", got.OperatorNotes)
	}
	if len(got.IssueCodes) != 0 {
		t.Fatalf("issue codes = %#v, want none", got.IssueCodes)
	}
}

func TestBuildPhase0ProofPacketSummaryRejectsPlaceholderDrillsWithoutExecutionEvidence(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writeFile(t, filepath.Join(root, "release-summary.json"), `{
  "releaseReady": true,
  "gateCounts": {"drillRows": 19, "drillStatuses": {"placeholder": 19}, "alertRules": 8, "alertValidationPassed": true},
  "executedDrillCounts": {"assertions": 0, "passed": 0, "failed": 0, "skipped": 0},
  "evidenceIndexVerificationStatus": "pass",
  "evidencePacketItemStatuses": [{"name": "drill-manifest", "status": "pass"}],
  "verdict": "ready",
  "blockingReasons": [],
  "lanes": [{"lane": "S08", "status": "ready", "requiredItems": ["drill-manifest", "drill-execution"], "missingItems": ["drill-execution"]}],
  "manifestHash": "abc123"
}`)

	got := buildPhase0ProofPacketSummary(root)

	if got.Status != phase0ProofStatusNeedsReview || got.ReleaseSummary.Status != phase0ProofStatusNeedsReview {
		t.Fatalf("summary = %#v, want needs-review for placeholder drills without execution evidence", got)
	}
	if !containsString(got.IssueCodes, phase0ProofIssueReleaseSummaryNotReady) {
		t.Fatalf("issue codes = %#v, want release summary not ready", got.IssueCodes)
	}
}

func TestBuildPhase0ProofPacketSummaryMissingArtifactsNeedsReview(t *testing.T) {
	got := buildPhase0ProofPacketSummary(t.TempDir())

	if got.Status != phase0ProofStatusNeedsReview {
		t.Fatalf("status = %q, want needs-review", got.Status)
	}
	for _, want := range []string{
		phase0ProofIssueReleaseEvidenceMissing,
		phase0ProofIssueEvidenceIndexMissing,
		phase0ProofIssueReleaseSummaryMissing,
		phase0ProofIssueEvidenceIndexNotPass,
	} {
		if !containsString(got.IssueCodes, want) {
			t.Fatalf("issue codes = %#v, missing %s", got.IssueCodes, want)
		}
	}
	if got.Command.Available || got.RunnerOS.Available || got.CommitSHA.Available || got.OperatorNotes.Available {
		t.Fatalf("optional live fields should be unavailable: %#v %#v %#v %#v", got.Command, got.RunnerOS, got.CommitSHA, got.OperatorNotes)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryReadsArtifactsAndManifest(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B5DrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.SchemaVersion != phase0B5HostDrillSummarySchemaVersion || got.Status != phase0ProofStatusReady {
		t.Fatalf("summary header = %#v", got)
	}
	if got.Source != "release-artifacts+manifest" || !got.NoLiveHostProbe {
		t.Fatalf("source/probe = %q/%v", got.Source, got.NoLiveHostProbe)
	}
	if got.Release.ReleaseEvidence.Status != phase0ProofStatusReady || got.Release.GateDrillRows != 19 || got.Release.ExecutedDrillCounts.Passed != 95 {
		t.Fatalf("release snapshot = %#v", got.Release)
	}
	if !got.Manifest.Present || got.Manifest.Path != defaultPhase0DrillManifestPath || got.Manifest.DrillCount != 3 || filepath.IsAbs(got.Manifest.Path) {
		t.Fatalf("manifest snapshot = %#v", got.Manifest)
	}
	if !got.Portability.WorkspaceRelativeRefs || len(got.Portability.AbsolutePathRefs) != 0 {
		t.Fatalf("portability = %#v", got.Portability)
	}
	if got.HostMetadata.HostScopedDrillCount != 3 || !containsString(got.HostMetadata.Components, "host") || !containsString(got.HostMetadata.Components, "network") {
		t.Fatalf("host metadata = %#v", got.HostMetadata)
	}
	if len(got.Drills) != 3 || got.Drills[0].MetadataSource != "manifest-envelope" || !got.Drills[0].HostScoped {
		t.Fatalf("drills = %#v", got.Drills)
	}
	if got.CandidateCatalog.SchemaVersion != phase0B6CandidateCatalogSchemaVersion || got.CandidateCatalog.Status != phase0ProofStatusNeedsReview || got.CandidateCatalog.CandidateCounts.Total != 0 {
		t.Fatalf("candidate catalog = %#v", got.CandidateCatalog)
	}
	if len(got.IssueCodes) != 0 {
		t.Fatalf("issue codes = %#v, want none", got.IssueCodes)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryIncludesB6CandidateCatalog(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B6CandidateDrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.SchemaVersion != phase0B5HostDrillSummarySchemaVersion || got.Status != phase0ProofStatusReady {
		t.Fatalf("summary header = %#v", got)
	}
	catalog := got.CandidateCatalog
	if catalog.SchemaVersion != phase0B6CandidateCatalogSchemaVersion || catalog.Version != 1 || catalog.Status != phase0ProofStatusReady {
		t.Fatalf("candidate catalog header = %#v", catalog)
	}
	if catalog.Source != defaultPhase0DrillManifestPath || !catalog.OfflineByDefault || !catalog.NoLiveHostProbe {
		t.Fatalf("candidate catalog source/boundary = %#v", catalog)
	}
	if catalog.CandidateCounts.Total != 4 || catalog.CandidateCounts.HostScoped != 4 || catalog.CandidateCounts.LocalOnly != 4 || catalog.CandidateCounts.PlannerOnly != 4 {
		t.Fatalf("candidate counts = %#v", catalog.CandidateCounts)
	}
	for _, domain := range []string{"networking", "runtime_adapter", "registry_cache", "volume_attach"} {
		if catalog.CandidateCounts.ByDomain[domain] != 1 || !domainCoverageCovered(catalog.DomainCoverage, domain) {
			t.Fatalf("domain %s counts/coverage = %#v %#v", domain, catalog.CandidateCounts.ByDomain, catalog.DomainCoverage)
		}
	}
	if len(catalog.MissingDomains) != 0 || len(catalog.Candidates) != 4 {
		t.Fatalf("candidate catalog = %#v", catalog)
	}
	if got.Drills[2].ID != "D-08" || !got.Drills[2].Candidate || got.Drills[2].Phase3Domain != "networking" || got.Drills[2].Execution != "planner-only" {
		t.Fatalf("D-08 candidate row = %#v", got.Drills[2])
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryIncludesB7ReservationIndex(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B6CandidateDrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))
	writePhase0B7ReservationIndex(t, filepath.Join(root, defaultPhase0B7ReservationIndexPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.SchemaVersion != phase0B5HostDrillSummarySchemaVersion || got.Status != phase0ProofStatusReady {
		t.Fatalf("summary header = %#v", got)
	}
	index := got.ReservationIndex
	if index.SchemaVersion != phase0B7ReservationIndexSchemaVersion || index.Version != 1 || index.Status != phase0ProofStatusReady {
		t.Fatalf("reservation index header = %#v", index)
	}
	if index.Source != defaultPhase0B7ReservationIndexPath || index.SourceManifest != defaultPhase0DrillManifestPath || !index.LocalOnly || !index.OfflineByDefault || !index.NoLiveHostProbe {
		t.Fatalf("reservation index boundary/source = %#v", index)
	}
	if index.ReservationCounts.Total != 4 || index.ReservationCounts.Reserved != 4 || index.ReservationCounts.FutureRig != 4 || index.ReservationCounts.PlannerOnly != 4 {
		t.Fatalf("reservation counts = %#v", index.ReservationCounts)
	}
	for _, domain := range []string{"networking", "runtime_adapter", "registry_cache", "volume_attach"} {
		if index.ReservationCounts.ByDomain[domain] != 1 || !domainCoverageCovered(index.DomainCoverage, domain) {
			t.Fatalf("domain %s counts/coverage = %#v %#v", domain, index.ReservationCounts.ByDomain, index.DomainCoverage)
		}
	}
	if len(index.MissingDomains) != 0 || len(index.Reservations) != 4 {
		t.Fatalf("reservation index = %#v", index)
	}
	if index.Reservations[0].ID != "D-20" || index.Reservations[0].CarrierID != "D-08" || index.Reservations[0].PlannedDrill != "conntrack-flush-correctness" {
		t.Fatalf("reservation row = %#v", index.Reservations[0])
	}
	if len(got.IssueCodes) != 0 {
		t.Fatalf("issue codes = %#v, want none", got.IssueCodes)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryIncludesB8PromotionReadiness(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B6CandidateDrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))
	writePhase0B7ReservationIndex(t, filepath.Join(root, defaultPhase0B7ReservationIndexPath))
	writePhase0B8PromotionReadiness(t, filepath.Join(root, defaultPhase0B8PromotionReadinessPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	readiness := got.PromotionReadiness
	if readiness.SchemaVersion != phase0B8PromotionReadinessSchemaVersion || readiness.Version != 1 || readiness.Status != phase0ProofStatusNeedsReview {
		t.Fatalf("promotion readiness header = %#v", readiness)
	}
	if readiness.Source != defaultPhase0B8PromotionReadinessPath || readiness.SourceManifest != defaultPhase0DrillManifestPath || !readiness.LocalOnly || !readiness.OfflineByDefault || !readiness.NoLiveHostProbe {
		t.Fatalf("promotion readiness boundary/source = %#v", readiness)
	}
	if readiness.ReadinessCounts.Total != 4 || readiness.ReadinessCounts.Ready != 0 || readiness.ReadinessCounts.NeedsUnblocker != 4 || readiness.ReadinessCounts.LocalOnly != 4 || readiness.ReadinessCounts.PlannerOnly != 0 || readiness.ReadinessCounts.WithUnblockers != 4 {
		t.Fatalf("promotion readiness counts = %#v", readiness.ReadinessCounts)
	}
	for _, domain := range []string{"networking", "runtime_adapter", "registry_cache", "volume_attach"} {
		if readiness.ReadinessCounts.ByDomain[domain] != 1 || !domainCoverageCovered(readiness.DomainCoverage, domain) {
			t.Fatalf("domain %s counts/coverage = %#v %#v", domain, readiness.ReadinessCounts.ByDomain, readiness.DomainCoverage)
		}
	}
	if len(readiness.MissingDomains) != 0 || len(readiness.ReadinessRows) != 4 || len(readiness.Unblockers) != 5 {
		t.Fatalf("promotion readiness = %#v", readiness)
	}
	if readiness.ReadinessRows[3].ID != "D-23" || readiness.ReadinessRows[3].Status != phase0ProofStatusNeedsReview || readiness.ReadinessRows[3].ReservationID != "D-23" || len(readiness.ReadinessRows[3].Unblockers) != 5 {
		t.Fatalf("promotion readiness row = %#v", readiness.ReadinessRows[3])
	}
	if got.Status != phase0ProofStatusReady || len(got.IssueCodes) != 0 {
		t.Fatalf("top-level summary should stay compatible: %#v", got)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryIncludesB9ReleaseEvidenceArchive(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B6CandidateDrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))
	writePhase0B7ReservationIndex(t, filepath.Join(root, defaultPhase0B7ReservationIndexPath))
	writePhase0B8PromotionReadiness(t, filepath.Join(root, defaultPhase0B8PromotionReadinessPath))
	writePhase0B9ReleaseEvidenceArchive(t, filepath.Join(root, defaultPhase0B9ReleaseArchivePath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	archive := got.ReleaseArchive
	if archive.SchemaVersion != phase0B9ReleaseArchiveSchemaVersion || archive.Version != 1 || archive.Status != phase0ProofStatusNeedsReview {
		t.Fatalf("release archive header = %#v", archive)
	}
	if archive.Source != defaultPhase0B9ReleaseArchivePath || archive.SourceManifest != defaultPhase0DrillManifestPath || archive.ArchiveRoot != "artifacts/phase0" || !archive.LocalOnly || !archive.OfflineByDefault || !archive.NoLiveHostProbe {
		t.Fatalf("release archive boundary/source = %#v", archive)
	}
	if archive.Command.Name != "phase0-release-gate" || archive.Command.Mode != "local-only" || archive.Command.CommandRef != "artifacts/phase0/phase0-release-gate-command.txt" || archive.Command.RequiredFlag != "--verify-evidence-files" {
		t.Fatalf("release archive command = %#v", archive.Command)
	}
	if len(archive.EvidenceRefs) != 6 || archive.EvidenceRefs[0].Group != "canonical" || archive.EvidenceRefs[0].Name != "release-evidence" || archive.EvidenceRefs[0].Path != "artifacts/phase0/phase0-release-evidence.json" {
		t.Fatalf("release archive evidence refs = %#v", archive.EvidenceRefs)
	}
	if archive.ReadinessCounts.Total != 6 || archive.ReadinessCounts.NeedsReview != 6 || archive.ReadinessCounts.PendingLocalArchive != 6 || archive.ReadinessCounts.LocalOnly != 6 {
		t.Fatalf("release archive readiness counts = %#v", archive.ReadinessCounts)
	}
	if len(archive.VerificationRequirements) != 8 || len(archive.ReadinessRows) != 6 || archive.ReadinessRows[5].ID != "commit-refs" || archive.ReadinessRows[5].RequiredRef != "artifacts/phase0/commit-refs.json" {
		t.Fatalf("release archive rows = %#v", archive)
	}
	if got.Status != phase0ProofStatusReady || len(got.IssueCodes) != 0 {
		t.Fatalf("top-level summary should stay compatible: %#v", got)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryFlagsInvalidB9ReleaseEvidenceArchive(t *testing.T) {
	root := writePhase0ProofPacketArtifacts(t)
	writePhase0B9ReleaseEvidenceArchive(t, filepath.Join(root, defaultPhase0B9ReleaseArchivePath))
	writeFile(t, filepath.Join(root, defaultPhase0B9ReleaseArchivePath), `version: 1
owner: S08
`)

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.Status != phase0ProofStatusBlocked || !containsString(got.IssueCodes, phase0B9IssueReleaseArchiveInvalid) {
		t.Fatalf("summary = %#v", got)
	}
	if got.ReleaseArchive.Status != phase0ProofStatusBlocked || !containsString(got.ReleaseArchive.IssueCodes, phase0B9IssueReleaseArchiveInvalid) || got.ReleaseArchive.Error == "" {
		t.Fatalf("release archive = %#v", got.ReleaseArchive)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryFallsBackToManifestOnly(t *testing.T) {
	root := t.TempDir()
	writePhase0B5DrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.Status != phase0ProofStatusReady || got.Source != "manifest" {
		t.Fatalf("summary = %#v", got)
	}
	if got.Release.ReleaseEvidence.Present || !got.Manifest.Present || got.Manifest.DrillCount != 3 {
		t.Fatalf("release/manifest = %#v %#v", got.Release, got.Manifest)
	}
	if got.Release.ReleaseEvidence.Path == "" || filepath.IsAbs(got.Manifest.Path) {
		t.Fatalf("paths should remain API-relative: %#v %#v", got.Release.ReleaseEvidence, got.Manifest)
	}
}

func TestBuildPhase0B5HostDrillMetadataSummaryFlagsAbsoluteIndexRefs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "release-evidence-index.json"), `{
  "generatedAt": {"value": "deterministic-placeholder"},
  "entries": [{"name": "drill-manifest", "path": "/tmp/manifest.yaml", "role": "drill-manifest"}]
}`)
	writePhase0B5DrillManifest(t, filepath.Join(root, defaultPhase0DrillManifestPath))

	got := buildPhase0B5HostDrillMetadataSummary(root)

	if got.Status != phase0ProofStatusNeedsReview || !containsString(got.IssueCodes, phase0B5IssueAbsoluteArtifactRef) {
		t.Fatalf("summary = %#v", got)
	}
	if got.Portability.WorkspaceRelativeRefs || len(got.Portability.AbsolutePathRefs) != 1 {
		t.Fatalf("portability = %#v", got.Portability)
	}
	if got.Manifest.Path != defaultPhase0DrillManifestPath || filepath.IsAbs(got.Manifest.Path) {
		t.Fatalf("manifest path = %#v", got.Manifest)
	}
}

func TestRouterServesDashboardSummary(t *testing.T) {
	rr := httptest.NewRecorder()

	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/summary", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var got dashboardSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ready" || got.APIVersion == "" || got.AuditStatus != "empty" {
		t.Fatalf("dashboard summary = %#v", got)
	}
}

func TestRouterServesDashboardRuntimeInventory(t *testing.T) {
	now := time.Date(2026, 5, 2, 17, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now.Add(-time.Minute) })
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "Public API"})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistry([]byte("test-secret"), func() time.Time { return now })
	registry.agents["agent-a"] = &registeredAgent{
		id:            "agent-a",
		hostname:      "worker-1",
		agentVersion:  "0.1.0-alpha",
		registeredAt:  now.Add(-5 * time.Minute),
		lastHeartbeat: now.Add(-10 * time.Second),
		lastSequence:  7,
		lastStatus:    "ready",
		lastRuntime:   "docker",
		statusReport: agentapi.StatusReport{
			AgentID: "agent-a",
			Drift:   &agentapi.Drift{EngineVersion: "24.0.7", CGroupVersion: "v2"},
			RouteTargets: []agentapi.RouteTargetObservation{{
				RouteName:           "api",
				DeploymentID:        "dep-1",
				ContainerIP:         "10.0.0.2",
				InternalServicePort: 8080,
				ReadinessStatus:     "ready",
			}},
		},
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		AgentRegistry:  registry,
		Clock:          func() time.Time { return now },
		OperationStore: NewMemoryOperationStore(),
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "Public API" || got.Services[0].Route != "api" || got.Services[0].Slug != "api" {
		t.Fatalf("services = %#v", got.Services)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].Name != "worker-1" || got.Hosts[0].Doctor != "ready" || got.Hosts[0].Docker != "24.0.7" {
		t.Fatalf("hosts = %#v", got.Hosts)
	}
	if len(got.Routes) != 2 || got.Routes[0].Route != "api" || got.Routes[0].State != "promoted" || got.Routes[1].Route != "api" || got.Routes[1].State != "ready" || !strings.Contains(got.Routes[1].Detail, "runtime target 10.0.0.2:8080") {
		t.Fatalf("routes = %#v", got.Routes)
	}
	if got.Routes[0].ServiceID != string(service.ID) || got.Routes[0].Service != "Public API" || got.Routes[0].Host != "api" {
		t.Fatalf("service route linkage = %#v, want service id/name for acceptance probes", got.Routes[0])
	}
	if got.Routes[1].ServiceID != string(service.ID) || got.Routes[1].Service != "Public API" || got.Routes[1].RouteName != "api" || got.Routes[1].DeploymentID != "dep-1" || got.Routes[1].Host != "" {
		t.Fatalf("agent route linkage = %#v, want slug-matched service/deployment route metadata without a fake host", got.Routes[1])
	}
}

func TestDashboardRuntimeRouteLinkingPrefersDeploymentMatch(t *testing.T) {
	services := []dashboardRuntimeService{
		{ID: "svc-api-old", Slug: "api", Name: "Legacy API", Route: "api", DeploymentID: "dep-old", InstanceID: "inst-old"},
		{ID: "svc-api-new", Slug: "api-new", Name: "Current API", Route: "api-v2", DeploymentID: "dep-new", InstanceID: "inst-new"},
	}
	target := agentapi.RouteTargetObservation{
		RouteName:    "api",
		DeploymentID: "dep-new",
		InstanceID:   "inst-new",
	}

	service := serviceFromRouteTarget(services, target)
	if service == nil || service.ID != "svc-api-new" {
		t.Fatalf("serviceFromRouteTarget = %#v, want exact deployment match before route-name fallback", service)
	}
}

func TestDashboardRuntimeInventoryKeepsAbsentRouteDataHonest(t *testing.T) {
	now := time.Date(2026, 5, 2, 17, 30, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"}); err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistry([]byte("test-secret"), func() time.Time { return now })
	registry.agents["agent-a"] = &registeredAgent{
		id:            "agent-a",
		hostname:      "worker-1",
		registeredAt:  now.Add(-5 * time.Minute),
		lastHeartbeat: now.Add(-10 * time.Second),
		lastStatus:    "ready",
		statusReport: agentapi.StatusReport{
			AgentID: "agent-a",
			RouteTargets: []agentapi.RouteTargetObservation{{
				DeploymentID:        "dep-1",
				InstanceID:          "dep-1-r0",
				ContainerIP:         "10.0.0.2",
				InternalServicePort: 8080,
				ReadinessStatus:     "ready",
			}},
		},
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		AgentRegistry:  registry,
		Clock:          func() time.Time { return now },
		OperationStore: NewMemoryOperationStore(),
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Route != dashboardRouteNotPromoted {
		t.Fatalf("services = %#v", got.Services)
	}
	if len(got.Routes) != 2 || got.Routes[0].Route != dashboardRouteNotPromoted || got.Routes[0].State != dashboardRouteNotPromoted || got.Routes[1].Route != dashboardRouteNotReported || got.Routes[1].State != dashboardRouteNotPromoted {
		t.Fatalf("routes = %#v", got.Routes)
	}
	if got.Routes[1].Route == "10.0.0.2:8080" || !strings.Contains(got.Routes[1].Detail, "runtime target 10.0.0.2:8080") {
		t.Fatalf("route target detail = %#v", got.Routes[1])
	}
}

func TestDashboardRuntimeInventoryReflectsQueuedDeployIntake(t *testing.T) {
	now := time.Date(2026, 5, 2, 17, 45, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		QueueStore:     &leaseableDashboardQueueStore{jobID: "job_deploy_queued_1"},
		DomainStore:    generatedDeployRouteTestStore(string(project.ID), "env_prod", string(service.ID)),
		Clock:          func() time.Time { return now },
	})

	body := `{
		"environment_id":"env_prod",
		"image":"registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"deployment_id":"dep_api_20260502",
		"instance_id":"inst_api_20260502_0",
		"route":{"public":true}
	}`
	deployRR := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-dashboard")
	router.ServeHTTP(deployRR, req)
	if deployRR.Code != http.StatusAccepted {
		t.Fatalf("deploy status = %d, body = %s", deployRR.Code, deployRR.Body.String())
	}

	inventoryRR := httptest.NewRecorder()
	router.ServeHTTP(inventoryRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))
	if inventoryRR.Code != http.StatusOK {
		t.Fatalf("inventory status = %d, body = %s", inventoryRR.Code, inventoryRR.Body.String())
	}
	var inventory dashboardRuntimeInventory
	if err := json.Unmarshal(inventoryRR.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Services) != 1 {
		t.Fatalf("services = %#v", inventory.Services)
	}
	got := inventory.Services[0]
	if got.OperationID == "" ||
		got.OperationState != "QUEUED" ||
		got.RuntimeState != "waiting-for-worker" ||
		got.Queue != string(queue.Deploy) ||
		got.JobID != "job_deploy_queued_1" ||
		got.JobState != string(queue.JobQueued) ||
		got.DeploymentID == "" ||
		got.DeploymentID == "dep_api_20260502" ||
		got.InstanceID != got.DeploymentID+"-r0" ||
		got.Route != dashboardRouteNotPromoted ||
		got.RouteIntent != "web" ||
		!strings.Contains(got.LastEvent, "deploy.intake") ||
		got.LastEventAt == nil ||
		!got.LastEventAt.Equal(now) {
		t.Fatalf("service deploy visibility = %#v", got)
	}
}

func TestRouterCreatesDashboardTemplateDeployJob(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	now := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	queueStore := &leaseableDashboardQueueStore{jobID: "job_dashboard_template_1"}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:     resources,
		ServiceStore:     resources,
		EnvironmentStore: environments,
		OperationStore:   operations,
		QueueStore:       queueStore,
		Clock:            func() time.Time { return now },
	})

	body := bytes.NewBufferString(`{"template_id":"postgres-16","service_name":"Postgres 16","database_name":"mvp"}`)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/dashboard/template-deploy", body))

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(queueStore.requests) != 0 {
		t.Fatalf("database template failure enqueued %d jobs", len(queueStore.requests))
	}
}

func TestDashboardTemplateDeployFailsClosedOutsideExperimentalMode(t *testing.T) {
	t.Setenv(experimentalEnv, "")
	resources := NewMemoryResourceStore()
	operations := NewMemoryOperationStore()
	queueStore := &leaseableDashboardQueueStore{jobID: "must-not-be-enqueued"}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		QueueStore:     queueStore,
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/dashboard/template-deploy", bytes.NewBufferString(`{"template_id":"postgres-16","service_name":"Postgres 16"}`)))

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusNotImplemented)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.NotImplemented ||
		got.Error.Details["capability"] != "database-template-deploy" ||
		got.Error.Details["state"] != "unavailable" {
		t.Fatalf("error = %#v, want honest unavailable capability", got.Error)
	}
	services, err := resources.ListServices(context.Background(), ResourceListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	deploys, err := operations.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 || len(deploys) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("services/deploys/jobs = %#v/%#v/%#v, want no side effects", services, deploys, queueStore.requests)
	}
}

func TestDashboardTemplateDeployRequiresQueueStore(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	now := time.Date(2026, 5, 2, 18, 30, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		Clock:          func() time.Time { return now },
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/dashboard/template-deploy", bytes.NewBufferString(`{"template_id":"postgres-16","service_name":"Postgres 16"}`)))

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, body = %s, want queue not wired", rr.Code, rr.Body.String())
	}
	services, err := resources.ListServices(context.Background(), ResourceListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	deploys, err := operations.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 || len(deploys) != 0 {
		t.Fatalf("services/deploys = %#v/%#v, want no fake draft without queue", services, deploys)
	}
}

func TestDashboardRuntimeInventoryReflectsTerminalTemplateDeployState(t *testing.T) {
	now := time.Date(2026, 5, 2, 19, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "local-dashboard", DisplayName: "Local dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "redis-7-durable", DisplayName: "Cache"})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := operations.Create(context.Background(), orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationFailed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.AppendEvent(context.Background(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     dashboardTemplateDeployEventMessage("redis-7-durable", string(service.ID)),
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		Clock:          func() time.Time { return now },
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var inventory dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Services) != 1 {
		t.Fatalf("services = %#v", inventory.Services)
	}
	got := inventory.Services[0]
	if got.TemplateID != "redis-7-durable" || got.OperationID != string(operation.ID) || got.OperationState != "FAILED" || got.RuntimeState != "failed" || got.Health != "failed" || got.Replicas != "0/1 failed" {
		t.Fatalf("service = %#v", got)
	}
	if got.Engine != "redis" || got.EngineVersion != "7" {
		t.Fatalf("engine metadata = %#v", got)
	}
}

func TestDashboardRuntimeInventoryReflectsPromotedOperationRoute(t *testing.T) {
	now := time.Date(2026, 5, 2, 20, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "local-dashboard", DisplayName: "Local dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := operations.Create(context.Background(), orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		dashboardTemplateDeployEventMessage("api", string(service.ID)),
		"deploy worker step promote succeeded: host=api.example.test",
	} {
		if _, err := operations.AppendEvent(context.Background(), OperationEvent{
			OperationID: operation.ID,
			State:       operation.State,
			Message:     message,
			CreatedAt:   now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		Clock:          func() time.Time { return now },
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var inventory dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Services) != 1 || inventory.Services[0].Route != "api.example.test" || inventory.Services[0].RuntimeState != "active" {
		t.Fatalf("services = %#v", inventory.Services)
	}
	if len(inventory.Routes) != 1 || inventory.Routes[0].Route != "api.example.test" || inventory.Routes[0].State != "promoted" {
		t.Fatalf("routes = %#v", inventory.Routes)
	}
}

func TestDashboardRuntimeInventoryPrefersRecoveredAgentRuntimeState(t *testing.T) {
	now := time.Date(2026, 5, 2, 20, 15, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "local-dashboard", DisplayName: "Local dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := operations.Create(context.Background(), orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		"deploy.intake service_id=" + string(service.ID) + " project_id=" + string(project.ID) + " queue=deploy job_id=job_api job_state=SUCCEEDED deployment_id=dep_api_restart instance_id=inst_api_restart_0 route_intent=api.example.test",
		"deploy worker step promote succeeded job_id=job_api deployment_id=dep_api_restart instance_id=inst_api_restart_0 service_id=" + string(service.ID) + " route_name=api host=api.example.test container_id=ctr-api container_ip=10.0.0.2 internal_port=8080",
	} {
		if _, err := operations.AppendEvent(context.Background(), OperationEvent{
			OperationID: operation.ID,
			State:       operation.State,
			Message:     message,
			CreatedAt:   now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	registry := NewAgentRegistry([]byte("test-secret"), func() time.Time { return now })
	registry.agents["agent-a"] = &registeredAgent{
		id:            "agent-a",
		hostname:      "worker-1",
		registeredAt:  now.Add(-5 * time.Minute),
		lastHeartbeat: now.Add(-10 * time.Second),
		lastStatus:    "ready",
		lastRuntime:   "docker",
		statusReport: agentapi.StatusReport{
			AgentID: "agent-a",
			Instances: []agentapi.InstanceStatus{{
				DeploymentID: "dep_api_restart",
				InstanceID:   "inst_api_restart_0",
				State:        "READY",
				ObservedAt:   now.Add(-2 * time.Second),
			}},
			RouteTargets: []agentapi.RouteTargetObservation{{
				DeploymentID:        "dep_api_restart",
				InstanceID:          "inst_api_restart_0",
				ContainerID:         "ctr-api",
				ContainerIP:         "10.0.0.2",
				InternalServicePort: 8080,
				ReadinessStatus:     "ready",
				LastObservedAt:      now.Add(-2 * time.Second),
			}},
		},
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		AgentRegistry:  registry,
		Clock:          func() time.Time { return now },
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var inventory dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Services) != 1 || inventory.Services[0].RuntimeState != "active" || inventory.Services[0].Health != "healthy" || inventory.Services[0].Replicas != "1/1" {
		t.Fatalf("services = %#v, want recovered healthy runtime state", inventory.Services)
	}
	if len(inventory.Routes) != 2 || inventory.Routes[1].ServiceID != string(service.ID) || !strings.Contains(inventory.Routes[1].Detail, "runtime target 10.0.0.2:8080") {
		t.Fatalf("routes = %#v, want recovered agent route target linked to service", inventory.Routes)
	}
}

func TestDashboardRuntimeInventoryShowsRecoveredDrainState(t *testing.T) {
	now := time.Date(2026, 5, 2, 20, 30, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	operations := NewMemoryOperationStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "local-dashboard", DisplayName: "Local dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := operations.Create(context.Background(), orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.AppendEvent(context.Background(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     "deploy.intake service_id=" + string(service.ID) + " deployment_id=dep_api_drain instance_id=inst_api_drain_0",
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistry([]byte("test-secret"), func() time.Time { return now })
	registry.agents["agent-a"] = &registeredAgent{
		id:            "agent-a",
		hostname:      "worker-1",
		registeredAt:  now.Add(-5 * time.Minute),
		lastHeartbeat: now.Add(-10 * time.Second),
		lastStatus:    "ready",
		lastRuntime:   "docker",
		statusReport: agentapi.StatusReport{
			AgentID: "agent-a",
			Instances: []agentapi.InstanceStatus{{
				DeploymentID: "dep_api_drain",
				InstanceID:   "inst_api_drain_0",
				State:        "DRAINING",
				ObservedAt:   now.Add(-time.Second),
			}},
		},
	}

	rr := httptest.NewRecorder()
	NewRouterWithDataStack(DataStack{
		ProjectStore:   resources,
		ServiceStore:   resources,
		OperationStore: operations,
		AgentRegistry:  registry,
		Clock:          func() time.Time { return now },
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/runtime-inventory", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var inventory dashboardRuntimeInventory
	if err := json.Unmarshal(rr.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Services) != 1 || inventory.Services[0].RuntimeState != "draining" || inventory.Services[0].Health != "draining" || inventory.Services[0].Replicas != "1/1 draining" {
		t.Fatalf("services = %#v, want recovered drain state", inventory.Services)
	}
}

func TestRouterServesDashboardCompatibilityAndAuditEvidence(t *testing.T) {
	router := NewRouter()

	compatRR := httptest.NewRecorder()
	router.ServeHTTP(compatRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/compatibility-matrix", nil))
	if compatRR.Code != http.StatusOK {
		t.Fatalf("compat status = %d, want %d", compatRR.Code, http.StatusOK)
	}
	if body := compatRR.Body.String(); !strings.Contains(body, `"api_version"`) || !strings.Contains(body, `"matrix"`) {
		t.Fatalf("compat body missing expected fields: %s", body)
	}

	auditRR := httptest.NewRecorder()
	router.ServeHTTP(auditRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/audit-evidence", nil))
	if auditRR.Code != http.StatusOK {
		t.Fatalf("audit status = %d, want %d", auditRR.Code, http.StatusOK)
	}
	if body := auditRR.Body.String(); !strings.Contains(body, `"schema_version"`) || !strings.Contains(body, `"event_count"`) {
		t.Fatalf("audit body missing expected fields: %s", body)
	}
}

func TestRouterServesDashboardAuditFeedsAndExport(t *testing.T) {
	router := NewRouter()

	for _, path := range []string{
		"/api/v1alpha/audit/events?limit=2",
		"/api/v1alpha/audit/log-drains",
		"/api/v1alpha/audit/export",
		"/api/v1alpha/audit/evidence",
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusOK)
		}
		if rr.Body.Len() == 0 {
			t.Fatalf("%s returned an empty body", path)
		}
		if strings.Contains(rr.Body.String(), "deployment.created") || strings.Contains(rr.Body.String(), "https://siem.example.invalid") {
			t.Fatalf("%s returned demo audit data: %s", path, rr.Body.String())
		}
	}
}

func TestDashboardAuditFeedsReadAuditChain(t *testing.T) {
	now := time.Date(2026, 5, 2, 21, 0, 0, 0, time.UTC)
	chain := audit.NewMemoryChain()
	if _, err := chain.Append(context.Background(), audit.Row{
		RequestID:    "req_1",
		OccurredAt:   now,
		RecordedAt:   now,
		ActorID:      "user_123",
		ActorType:    "user",
		ResourceKind: "service",
		ResourceID:   "svc_1",
		Action:       "service.created",
		After:        map[string]any{"name": "api"},
	}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{AuditChain: chain})

	eventsRR := httptest.NewRecorder()
	router.ServeHTTP(eventsRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/audit/events?limit=1", nil))
	if eventsRR.Code != http.StatusOK {
		t.Fatalf("events status = %d body=%s", eventsRR.Code, eventsRR.Body.String())
	}
	if body := eventsRR.Body.String(); !strings.Contains(body, `"service.created"`) || !strings.Contains(body, `"resource":"service/svc_1"`) {
		t.Fatalf("events body missing chain row: %s", body)
	}

	evidenceRR := httptest.NewRecorder()
	router.ServeHTTP(evidenceRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/audit-evidence", nil))
	if evidenceRR.Code != http.StatusOK {
		t.Fatalf("evidence status = %d body=%s", evidenceRR.Code, evidenceRR.Body.String())
	}
	if body := evidenceRR.Body.String(); !strings.Contains(body, `"event_count":1`) || !strings.Contains(body, `"status":"needs-review"`) || !strings.Contains(body, `"signature_status":"unsigned"`) {
		t.Fatalf("evidence body missing chain summary: %s", body)
	}

	exportRR := httptest.NewRecorder()
	router.ServeHTTP(exportRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/audit/export", nil))
	if exportRR.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", exportRR.Code, exportRR.Body.String())
	}
	src, err := audit.NewJSONLSourceFromReader(bytes.NewReader(exportRR.Body.Bytes()))
	if err != nil {
		t.Fatalf("parse canonical audit export: %v\n%s", err, exportRR.Body.String())
	}
	report, err := audit.Verify(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("verify canonical audit export: %v", err)
	}
	if !report.Valid || report.Status != "verified" || report.RowsChecked != 1 {
		t.Fatalf("verify report = %+v, want one verified row", report)
	}
}

func writePhase0ProofPacketArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "release-evidence.json"), `{
  "generatedAt": {"value": "deterministic-placeholder", "source": "phase0-gate deterministic placeholder"},
  "modeFlags": {
    "format": "json",
    "lastGreen": "unknown",
    "drillAdapter": "local",
    "executeDrills": true,
    "requireExecutedDrills": true,
    "releaseEvidence": "artifacts/phase0/release-evidence.json",
    "releaseEvidenceIndex": "artifacts/phase0/release-evidence-index.json",
    "verifyReleaseEvidenceIndex": "artifacts/phase0/release-evidence-index.json",
    "verifyEvidenceFiles": true,
    "releaseSummary": "artifacts/phase0/release-summary.json",
    "evidenceRefs": [{"name": "operator-notes", "path": "artifacts/phase0/operator-notes.md"}]
  },
  "gateReport": {
    "releaseEvidenceIndexVerification": {
      "path": "artifacts/phase0/release-evidence-index.json",
      "status": "pass",
      "entries": 3,
      "requiredRefs": ["gate-release-evidence", "drill-manifest", "prometheus-alerts"],
      "filesChecked": true
    }
  },
  "evidencePacket": {"lastGreen": {"status": "unknown", "source": "flag"}, "items": []},
  "releaseReady": true
}`)
	writeFile(t, filepath.Join(root, "release-evidence-index.json"), `{
  "generatedAt": {"value": "deterministic-placeholder", "source": "phase0-gate deterministic placeholder"},
  "entries": [
    {"name": "gate-release-evidence", "path": "artifacts/phase0/release-evidence.json", "role": "release-evidence"},
    {"name": "drill-manifest", "path": "test/drills/manifest.yaml", "role": "drill-manifest"},
    {"name": "prometheus-alerts", "path": "deploy/prometheus/alerts.yaml", "role": "alert-rules"}
  ]
}`)
	writeFile(t, filepath.Join(root, "release-summary.json"), `{
  "releaseReady": true,
  "gateCounts": {"drillRows": 19, "drillStatuses": {"pass": 19}, "alertRules": 8, "alertValidationPassed": true},
  "executedDrillCounts": {"assertions": 95, "passed": 95, "failed": 0, "skipped": 0},
  "evidenceIndexVerificationStatus": "pass",
  "evidencePacketItemStatuses": [{"name": "drill-manifest", "status": "pass"}, {"name": "drill-execution", "status": "pass"}],
  "verdict": "ready",
  "blockingReasons": [],
  "lanes": [{"lane": "S08", "status": "ready", "requiredItems": ["drill-manifest", "drill-execution"]}],
  "manifestHash": "abc123"
}`)
	writeFile(t, filepath.Join(root, "runner-os.txt"), "linux/amd64\n")
	runDir := filepath.Join(root, "sprint-b1-live-deploy", "20260426T120000Z")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runDir, "commit.txt"), "0123456789abcdef0123456789abcdef01234567\n")
	writeFile(t, filepath.Join(runDir, "operator-notes.md"), `# Sprint B1 Operator Notes

- Run id: 20260426T120000Z
- Commit: 0123456789abcdef0123456789abcdef01234567
- Result: pass
`)
	return root
}

func writePhase0B5DrillManifest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `version: 1
owner: S08
drills:
  - id: D-03
    title: Agent lost alert drill
    status: placeholder
    reason-code: phase0.alert.agent-lost
    event: alert.agent_lost
    diagnostic: confirm stale heartbeat age and impacted host
    runbook: docs/runbooks/support-bundle.md
  - id: D-08
    title: Conntrack pressure alert drill
    status: placeholder
    reason-code: phase0.alert.conntrack-pressure
    event: alert.conntrack_pressure
    diagnostic: confirm conntrack usage ratio and node pressure
    runbook: docs/runbooks/support-bundle.md
  - id: D-09
    title: Off-host backup missing drill
    status: placeholder
    reason-code: phase0.backup.offhost-missing
    event: backup.offhost_missing
    diagnostic: confirm no off-host destination is configured
    runbook: docs/runbooks/backup-restore.md
`)
}

func writePhase0B6CandidateDrillManifest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `version: 1
owner: S08
drills:
  - id: D-06
    title: Deploy job stuck alert drill
    status: placeholder
    reason-code: phase0.alert.deploy-job-stuck
    event: alert.deploy_job_stuck
    diagnostic: confirm deploy heartbeat gap exceeds lease threshold
    runbook: docs/runbooks/support-bundle.md
    metadata:
      tags:
        - phase3
        - host-scoped
        - local-only
      notes:
        - phase3-drill=runtime-adapter-lease-recovery
        - host-scope=runtime-host
        - domain=runtime_adapter
        - evidence-ref=artifacts/phase3/drills/runtime-adapter-lease/plan.json
        - execution=planner-only
      subcases:
        - phase3.host=runtime-host
        - phase3.domain=runtime_adapter
        - phase3.planner=runtime-adapter-lease-recovery
        - phase3.evidence=artifacts/phase3/drills/runtime-adapter-lease/plan.json
  - id: D-07
    title: BuildKit cache corruption drill
    status: placeholder
    reason-code: phase0.alert.buildkit-cache-corruption
    event: alert.buildkit_cache_corruption
    diagnostic: confirm cache verification failure and builder host
    runbook: docs/runbooks/support-bundle.md
    metadata:
      tags:
        - phase3
        - host-scoped
        - local-only
      notes:
        - phase3-drill=registry-cache-integrity
        - host-scope=builder-host
        - domain=registry_cache
        - evidence-ref=artifacts/phase3/drills/registry-cache-integrity/plan.json
        - execution=planner-only
      subcases:
        - phase3.host=builder-host
        - phase3.domain=registry_cache
        - phase3.planner=registry-cache-integrity
        - phase3.evidence=artifacts/phase3/drills/registry-cache-integrity/plan.json
  - id: D-08
    title: Conntrack pressure alert drill
    status: placeholder
    reason-code: phase0.alert.conntrack-pressure
    event: alert.conntrack_pressure
    diagnostic: confirm conntrack usage ratio and node pressure
    runbook: docs/runbooks/support-bundle.md
    metadata:
      tags:
        - phase3
        - host-scoped
        - local-only
      notes:
        - phase3-drill=conntrack-flush-correctness
        - host-scope=runtime-host
        - domain=networking
        - evidence-ref=artifacts/phase3/drills/conntrack-flush/plan.json
        - execution=planner-only
      subcases:
        - phase3.host=runtime-host
        - phase3.domain=networking
        - phase3.planner=conntrack-flush-correctness
        - phase3.evidence=artifacts/phase3/drills/conntrack-flush/plan.json
  - id: D-14
    title: Local backup destination placeholder drill
    status: placeholder
    reason-code: phase0.backup.destination-placeholder
    event: backup.destination_placeholder
    diagnostic: confirm placeholder methods return not implemented
    runbook: docs/runbooks/backup-restore.md
    metadata:
      tags:
        - phase3
        - host-scoped
        - local-only
      notes:
        - phase3-drill=volume-attach-destination-contract
        - host-scope=backup-host
        - domain=volume_attach
        - evidence-ref=artifacts/phase3/drills/volume-attach-destination/plan.json
        - execution=planner-only
      subcases:
        - phase3.host=backup-host
        - phase3.domain=volume_attach
        - phase3.planner=volume-attach-destination-contract
        - phase3.evidence=artifacts/phase3/drills/volume-attach-destination/plan.json
`)
}

func writePhase0B7ReservationIndex(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `version: 1
owner: S08
capability: phase3 host drill reservations
local-only: true
source-manifest: test/drills/manifest.yaml
reservations:
  - id: D-20
    status: reserved
    domain: networking
    carrier-id: D-08
    carrier-reason-code: phase0.alert.conntrack-pressure
    planned-drill: conntrack-flush-correctness
    host-scope: runtime-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill; carried locally by D-08 until the Phase 3 rig exists
    evidence-refs:
      - artifacts/phase3/drills/conntrack-flush/plan.json
  - id: D-21
    status: reserved
    domain: runtime_adapter
    carrier-id: D-06
    carrier-reason-code: phase0.alert.deploy-job-stuck
    planned-drill: runtime-adapter-lease-recovery
    host-scope: runtime-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill; carried locally by D-06 until the Phase 3 rig exists
    evidence-refs:
      - artifacts/phase3/drills/runtime-adapter-lease/plan.json
  - id: D-22
    status: reserved
    domain: registry_cache
    carrier-id: D-07
    carrier-reason-code: phase0.alert.buildkit-cache-corruption
    planned-drill: registry-cache-integrity
    host-scope: builder-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill; carried locally by D-07 until the Phase 3 rig exists
    evidence-refs:
      - artifacts/phase3/drills/registry-cache-integrity/plan.json
  - id: D-23
    status: reserved
    domain: volume_attach
    carrier-id: D-14
    carrier-reason-code: phase0.backup.destination-placeholder
    planned-drill: volume-attach-destination-contract
    host-scope: backup-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill; carried locally by D-14 until the Phase 3 rig exists
    evidence-refs:
      - artifacts/phase3/drills/volume-attach-destination/plan.json
`)
}

func writePhase0B8PromotionReadiness(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `version: 1
owner: S08
capability: phase3 promotion readiness
local-only: true
source-manifest: test/drills/manifest.yaml
checks:
  checklist:
    - id: D-20
    status: not-ready
    domain: networking
    reservation-id: D-20
    carrier-id: D-08
    required-unblockers:
      - accepted-id-range
      - executable-rig
      - harness-assertion
      - live-evidence-ref
      - rollback-boundary
    evidence-refs:
      - artifacts/phase3/promotion/D-20/accepted-id-range.json
      - artifacts/phase3/promotion/D-20/executable-rig.json
      - artifacts/phase3/promotion/D-20/harness-assertion.json
      - artifacts/phase3/promotion/D-20/live-evidence-ref.json
      - artifacts/phase3/promotion/D-20/rollback-boundary.json
    - id: D-21
    status: not-ready
    domain: runtime_adapter
    reservation-id: D-21
    carrier-id: D-06
    required-unblockers:
      - accepted-id-range
      - executable-rig
      - harness-assertion
      - live-evidence-ref
      - rollback-boundary
    evidence-refs:
      - artifacts/phase3/promotion/D-21/accepted-id-range.json
      - artifacts/phase3/promotion/D-21/executable-rig.json
      - artifacts/phase3/promotion/D-21/harness-assertion.json
      - artifacts/phase3/promotion/D-21/live-evidence-ref.json
      - artifacts/phase3/promotion/D-21/rollback-boundary.json
    - id: D-22
    status: not-ready
    domain: registry_cache
    reservation-id: D-22
    carrier-id: D-07
    required-unblockers:
      - accepted-id-range
      - executable-rig
      - harness-assertion
      - live-evidence-ref
      - rollback-boundary
    evidence-refs:
      - artifacts/phase3/promotion/D-22/accepted-id-range.json
      - artifacts/phase3/promotion/D-22/executable-rig.json
      - artifacts/phase3/promotion/D-22/harness-assertion.json
      - artifacts/phase3/promotion/D-22/live-evidence-ref.json
      - artifacts/phase3/promotion/D-22/rollback-boundary.json
    - id: D-23
    status: not-ready
    domain: volume_attach
    reservation-id: D-23
    carrier-id: D-14
    required-unblockers:
      - accepted-id-range
      - executable-rig
      - harness-assertion
      - live-evidence-ref
      - rollback-boundary
    evidence-refs:
      - artifacts/phase3/promotion/D-23/accepted-id-range.json
      - artifacts/phase3/promotion/D-23/executable-rig.json
      - artifacts/phase3/promotion/D-23/harness-assertion.json
      - artifacts/phase3/promotion/D-23/live-evidence-ref.json
      - artifacts/phase3/promotion/D-23/rollback-boundary.json
`)
}

func writePhase0B9ReleaseEvidenceArchive(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `version: 1
owner: S08
capability: phase0 release evidence archive
local-only: true
source-manifest: test/drills/manifest.yaml
archive-root: artifacts/phase0
gate-command:
  name: phase0-release-gate
  mode: local-only
  command-ref: artifacts/phase0/phase0-release-gate-command.txt
  required-flag: --verify-evidence-files
evidence-refs:
  canonical:
    release-evidence: artifacts/phase0/phase0-release-evidence.json
    release-evidence-index: artifacts/phase0/phase0-release-evidence-index.json
    release-summary: artifacts/phase0/phase0-release-summary.json
  operator-metadata:
    operator-notes: artifacts/phase0/operator-notes.md
    runner-os: artifacts/phase0/runner-os.json
    commit-refs: artifacts/phase0/commit-refs.json
verification-requirements:
  - canonical-evidence-files-exist
  - canonical-evidence-index-references-release-evidence
  - canonical-evidence-index-references-release-summary
  - gate-command-recorded-with-verify-evidence-files
  - runner-os-recorded-as-data
  - commit-refs-recorded-as-data
  - operator-notes-recorded-or-explicitly-empty
  - evidence-refs-are-workspace-relative
readiness:
  rows:
    - id: phase0-release-evidence
      status: pending-local-archive
      required-ref: artifacts/phase0/phase0-release-evidence.json
      verification:
        - canonical-evidence-files-exist
        - canonical-evidence-index-references-release-evidence
    - id: phase0-release-evidence-index
      status: pending-local-archive
      required-ref: artifacts/phase0/phase0-release-evidence-index.json
      verification:
        - canonical-evidence-files-exist
        - canonical-evidence-index-references-release-summary
    - id: phase0-release-summary
      status: pending-local-archive
      required-ref: artifacts/phase0/phase0-release-summary.json
      verification:
        - canonical-evidence-files-exist
    - id: operator-notes
      status: pending-local-archive
      required-ref: artifacts/phase0/operator-notes.md
      verification:
        - operator-notes-recorded-or-explicitly-empty
    - id: runner-os
      status: pending-local-archive
      required-ref: artifacts/phase0/runner-os.json
      verification:
        - runner-os-recorded-as-data
    - id: commit-refs
      status: pending-local-archive
      required-ref: artifacts/phase0/commit-refs.json
      verification:
        - commit-refs-recorded-as-data
`)
}

type leaseableDashboardQueueStore struct {
	requests []queue.EnqueueRequest
	jobs     []queue.Job
	jobID    string
}

func (s *leaseableDashboardQueueStore) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	s.requests = append(s.requests, request)
	id := s.jobID
	if id == "" {
		id = "job_dashboard_template"
	}
	job := queue.Job{
		ID:                id,
		Queue:             request.Queue,
		State:             queue.JobQueued,
		Priority:          request.Priority,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}
	s.jobs = append(s.jobs, job)
	return job, nil
}

func (s *leaseableDashboardQueueStore) Lease(_ context.Context, request queue.LeaseRequest) (queue.Job, error) {
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for i := range s.jobs {
		job := &s.jobs[i]
		if job.Queue != request.Queue || job.State != queue.JobQueued || job.AvailableAt.After(now) {
			continue
		}
		job.State = queue.JobLeased
		job.LeaseID = request.WorkerID
		job.LeasedUntil = now.Add(request.LeaseTime)
		return *job, nil
	}
	return queue.Job{}, queue.ErrNoWork
}

func (s *leaseableDashboardQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *leaseableDashboardQueueStore) Complete(context.Context, string, string) error {
	return nil
}

func (s *leaseableDashboardQueueStore) Fail(context.Context, string, string, string) error {
	return nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func domainCoverageCovered(values []phase0B6DomainCoverage, domain string) bool {
	for _, value := range values {
		if value.Domain == domain {
			return value.Covered
		}
	}
	return false
}
