package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/acme"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/supportbundle"
	"github.com/midnight/midnight/test/drills/harness"
)

type drillExecutionAdapter interface {
	Name() string
	Type() string
	Execute(drillManifest) (drillExecutionSummary, error)
}

type drillExecutionSummary struct {
	Adapter       string                      `json:"adapter"`
	AdapterType   string                      `json:"adapterType"`
	Assertions    int                         `json:"assertions"`
	Passed        int                         `json:"passed"`
	Failed        int                         `json:"failed"`
	Skipped       int                         `json:"skipped"`
	DrillStatuses map[harness.DrillStatus]int `json:"drillStatuses"`
	Results       []harness.AssertionResult   `json:"results"`
	Evaluations   []harness.DrillEvaluation   `json:"evaluations"`
}

type placeholderDrillExecutionAdapter struct{}

func (placeholderDrillExecutionAdapter) Name() string {
	return "placeholder"
}

func (placeholderDrillExecutionAdapter) Type() string {
	return "placeholder"
}

func (adapter placeholderDrillExecutionAdapter) Execute(manifest drillManifest) (drillExecutionSummary, error) {
	groups := make([]harness.DrillAssertionResults, 0, len(manifest.Drills))
	for _, drill := range manifest.Drills {
		result := harness.AssertionResult{
			ID:         placeholderAssertionID(drill),
			Status:     harness.AssertionSkipped,
			ReasonCode: drill.ReasonCode,
			Diagnostic: fmt.Sprintf("pending placeholder drill: %s", drill.Diagnostic),
		}
		groups = append(groups, harness.DrillAssertionResults{DrillID: drill.ID, Assertions: []harness.AssertionResult{result}})
	}
	return summarizeDrillExecution(adapter.Name(), adapter.Type(), groups)
}

type localDrillExecutionAdapter struct {
	manifestData     []byte
	alertData        []byte
	referenceCatalog *referenceCatalog
}

func (localDrillExecutionAdapter) Name() string {
	return "local"
}

func (localDrillExecutionAdapter) Type() string {
	return "local-safe"
}

// envelopeCatalog returns the reference catalog used to evaluate drill
// envelope sub-assertions. When the adapter has no injected catalog (the
// common path), the offline phase 0 allowlist is used so the adapter remains
// hermetic and does not depend on filesystem state.
func (adapter localDrillExecutionAdapter) envelopeCatalog() referenceCatalog {
	if adapter.referenceCatalog != nil {
		return *adapter.referenceCatalog
	}
	return defaultPhase0EnvelopeCatalog()
}

// defaultPhase0EnvelopeCatalog builds the offline-only reference catalog used
// by the local drill adapter when the gate is invoked without a filesystem
// catalog. It sources reason codes and events from the in-process phase 0
// allowlist so the adapter does not require docs/error-codes.md or
// docs/runbooks/ to be present in the working directory.
func defaultPhase0EnvelopeCatalog() referenceCatalog {
	catalog := referenceCatalog{
		ReasonCodes: make(map[string]struct{}),
		Events:      make(map[string]struct{}),
		Runbooks:    make(map[string]struct{}),
	}
	for _, code := range phase0DrillReasonCodeAllowlist() {
		catalog.ReasonCodes[code] = struct{}{}
	}
	for _, ev := range phase0DrillEventAllowlist() {
		catalog.Events[ev] = struct{}{}
	}
	for _, runbook := range []string{
		"docs/runbooks/backup-restore.md",
		"docs/runbooks/operator-readiness.md",
		"docs/runbooks/support-bundle.md",
		"docs/runbooks/key-rotation.md",
	} {
		catalog.Runbooks[runbook] = struct{}{}
	}
	return catalog
}

func (adapter localDrillExecutionAdapter) Execute(manifest drillManifest) (drillExecutionSummary, error) {
	catalog := adapter.envelopeCatalog()
	groups := make([]harness.DrillAssertionResults, 0, len(manifest.Drills))
	for _, drill := range manifest.Drills {
		var result harness.AssertionResult
		switch drill.ID {
		case "D-01":
			result = validatorAssertionResult(drill, "api-target-health", validateAPITargetHealth(), "validated API target down and stale scrape classification")
		case "D-02":
			result = validatorAssertionResult(drill, "disk-pressure-alert-validation", validatePackagedLockedAlert(adapter.alertData, "DiskPressure"), "validated DiskPressure alert against locked packaged rules")
		case "D-03":
			result = validatorAssertionResult(drill, "agent-lost-alert-validation", validatePackagedLockedAlert(adapter.alertData, "AgentLost"), "validated AgentLost alert against locked packaged rules")
		case "D-04":
			result = validatorAssertionResult(drill, "backup-failed-alert-validation", validatePackagedLockedAlert(adapter.alertData, "BackupFailed"), "validated BackupFailed alert against locked packaged rules")
		case "D-05":
			result = validatorAssertionResult(drill, "cert-expiry-classification", validateCertificateExpiringSoon(), "validated ACME certificate expiry classification")
		case "D-06":
			result = validatorAssertionResult(drill, "deploy-job-stuck-alert-validation", validatePackagedLockedAlert(adapter.alertData, "DeployJobStuck"), "validated DeployJobStuck alert against locked packaged rules")
		case "D-07":
			result = validatorAssertionResult(drill, "buildkit-cache-corruption-alert-validation", validatePackagedLockedAlert(adapter.alertData, "BuildKitCacheCorruption"), "validated BuildKitCacheCorruption alert against locked packaged rules")
		case "D-08":
			result = validatorAssertionResult(drill, "conntrack-pressure-alert-validation", validatePackagedLockedAlert(adapter.alertData, "ConntrackPressure"), "validated ConntrackPressure alert against locked packaged rules")
		case "D-09":
			result = validatorAssertionResult(drill, "backup-offhost-readiness", validateBackupOffhostMissing(), "validated backup readiness blocks unsupported off-host destinations")
		case "D-10":
			result = validatorAssertionResult(drill, "supportbundle-selector-validation", validateSupportBundleSelector(), "validated support bundle selector bounds")
		case "D-11":
			result = validatorAssertionResult(drill, "supportbundle-blocked-path-validation", validateSupportBundleBlockedPaths(), "validated support bundle blocked path rejection")
		case "D-12":
			result = validatorAssertionResult(drill, "supportbundle-manifest-review", validateSupportBundleManifestReview(), "validated support bundle manifest review preview")
		case "D-13":
			result = validatorAssertionResult(drill, "backup-local-uri-validation", validateBackupLocalURI(), "validated backup local/file URI parsing and rejection")
		case "D-14":
			result = validatorAssertionResult(drill, "backup-destination-local-io-validation", validateBackupDestinationLocalIO(), "validated local backup destination write, read, list, and path safety")
		case "D-15":
			result = validatorAssertionResult(drill, "restore-checksum-readiness", validateRestoreChecksumReadiness(), "validated restore checksum readiness and rollback owner checks")
		case "D-16":
			result = validatorAssertionResult(drill, "alert-packaging-validation", observability.ValidatePrometheusAlerts(adapter.alertData), "validated packaged Prometheus alerts against locked definitions")
		case "D-17":
			result = validatorAssertionResult(drill, "manifest-validation", validateDrillManifest(adapter.manifestData), "validated drill manifest metadata")
		case "D-18":
			result = validatorAssertionResult(drill, "alert-validation", observability.ValidatePrometheusAlerts(adapter.alertData), "validated locked Prometheus alert rules")
		case "D-19":
			result = validatorAssertionResult(drill, "release-readiness-route-promotion", validateReleaseReadinessAndDeployRoutePromotion(adapter.manifestData, adapter.alertData), "validated phase0 gate release readiness report and deploy route promotion contract")
		default:
			result = placeholderAssertionResult(drill)
		}
		assertions := append([]harness.AssertionResult{result}, drillEnvelopeAssertions(drill, catalog)...)
		groups = append(groups, harness.DrillAssertionResults{DrillID: drill.ID, Assertions: assertions})
	}
	return summarizeDrillExecution(adapter.Name(), adapter.Type(), groups)
}

func validateAPITargetHealth() error {
	now := drillCreatedAt()
	clock := func() time.Time { return now }
	tests := []struct {
		name         string
		up           bool
		lastScrapeAt time.Time
		wantStatus   api.TargetHealthStatus
		wantReason   string
	}{
		{name: "healthy", up: true, lastScrapeAt: now.Add(-time.Minute), wantStatus: api.TargetHealthHealthy, wantReason: api.TargetHealthReasonUp},
		{name: "down", up: false, lastScrapeAt: now.Add(-time.Minute), wantStatus: api.TargetHealthDown, wantReason: api.TargetHealthReasonDown},
		{name: "missing scrape", up: true, lastScrapeAt: time.Time{}, wantStatus: api.TargetHealthStale, wantReason: api.TargetHealthReasonScrapeMissing},
		{name: "stale scrape", up: true, lastScrapeAt: now.Add(-6 * time.Minute), wantStatus: api.TargetHealthStale, wantReason: api.TargetHealthReasonScrapeStale},
	}
	for _, tt := range tests {
		got := api.ReviewTargetHealth("midnight-api", tt.up, tt.lastScrapeAt, clock)
		if got.Status != tt.wantStatus || got.ReasonCode != tt.wantReason {
			return fmt.Errorf("%s: ReviewTargetHealth() = %q/%q, want %q/%q", tt.name, got.Status, got.ReasonCode, tt.wantStatus, tt.wantReason)
		}
	}
	return nil
}

func placeholderAssertionResult(drill drill) harness.AssertionResult {
	return harness.AssertionResult{
		ID:         placeholderAssertionID(drill),
		Status:     harness.AssertionSkipped,
		ReasonCode: drill.ReasonCode,
		Diagnostic: fmt.Sprintf("pending placeholder drill: %s", drill.Diagnostic),
	}
}

func validatorAssertionResult(drill drill, name string, err error, passDiagnostic string) harness.AssertionResult {
	result := harness.AssertionResult{
		ID:         drill.ID + "." + name,
		Status:     harness.AssertionPassed,
		ReasonCode: drill.ReasonCode,
		Diagnostic: passDiagnostic,
	}
	if err != nil {
		result.Status = harness.AssertionFailed
		result.Diagnostic = err.Error()
	}
	return result
}

func validatePackagedLockedAlert(alertData []byte, name string) error {
	if err := observability.ValidatePrometheusAlerts(alertData); err != nil {
		return err
	}
	alerts, err := observability.ParsePrometheusAlerts(alertData)
	if err != nil {
		return err
	}
	var expected observability.AlertRule
	for _, alert := range observability.LockedAlerts {
		if alert.Name == name {
			expected = alert
			break
		}
	}
	if expected.Name == "" {
		return fmt.Errorf("locked alert %q is not defined", name)
	}
	for _, alert := range alerts {
		if alert.Name == name {
			if alert.Expression != expected.Expression || alert.Severity != expected.Severity {
				return fmt.Errorf("locked alert %s does not match expected expression and severity", name)
			}
			return nil
		}
	}
	return fmt.Errorf("packaged locked alert %s is missing", name)
}

func validateCertificateExpiringSoon() error {
	now := drillCreatedAt()
	clock := func() time.Time { return now }
	tests := []struct {
		name      string
		expiresAt time.Time
		want      acme.CertificateExpiryStatus
	}{
		{name: "ok", expiresAt: now.Add(acme.RenewBefore + time.Second), want: acme.CertificateExpiryOK},
		{name: "renew soon", expiresAt: now.Add(acme.RenewBefore), want: acme.CertificateExpiryRenewSoon},
		{name: "expired", expiresAt: now, want: acme.CertificateExpiryExpired},
	}
	for _, tt := range tests {
		if got := acme.ClassifyCertificateExpiry(clock, tt.expiresAt, 0); got != tt.want {
			return fmt.Errorf("%s: ClassifyCertificateExpiry() = %q, want %q", tt.name, got, tt.want)
		}
	}
	return nil
}

func validateBackupOffhostMissing() error {
	createdAt := drillCreatedAt()
	manifest, err := backup.BuildExportManifest(backup.ExportManifestInput{
		ID:                    "drill-d09",
		CreatedAt:             createdAt,
		DatabaseDumpBytes:     100,
		RegistryMetadataBytes: 10,
		ConfigSnapshotBytes:   20,
		SupportReferenceBytes: 5,
		SupportReferenceCount: 1,
		OperatorPassphrase:    "confirmed",
	})
	if err != nil {
		return fmt.Errorf("BuildExportManifest() failed validation: %w", err)
	}

	ready := backup.ReviewBackupReadiness(backup.BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []backup.RestoreSmokeProbeResult{
			{Name: "read-postgres", State: backup.RestoreStepSucceeded, CheckedAt: createdAt, Object: backup.Object{Key: "postgres.dump", Bytes: 100}},
		},
	})
	if ready.Status != backup.BackupReadinessStatusReady || !ready.DestinationValid {
		return fmt.Errorf("ReviewBackupReadiness(local) = %+v, want ready valid destination", ready)
	}

	offhost := backup.ReviewBackupReadiness(backup.BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "s3://offhost/midnight",
		VerificationResults: []backup.RestoreSmokeProbeResult{
			{Name: "read-postgres", State: backup.RestoreStepSucceeded, CheckedAt: createdAt, Object: backup.Object{Key: "postgres.dump", Bytes: 100}},
		},
	})
	if offhost.Status != backup.BackupReadinessStatusBlocked || offhost.DestinationValid {
		return fmt.Errorf("ReviewBackupReadiness(offhost) = %+v, want blocked invalid destination", offhost)
	}
	for _, issue := range offhost.Issues {
		if issue.Code == backup.BackupReadinessIssueDestinationURIInvalid {
			return nil
		}
	}
	return fmt.Errorf("ReviewBackupReadiness(offhost) issues = %+v, want destination-uri-invalid", offhost.Issues)
}

func validateReleaseReadinessReport(manifestData, alertData []byte) error {
	report, err := buildGateReport(manifestData, alertData, "drill-d19")
	if err != nil {
		return err
	}
	if report.DrillRows != 19 {
		return fmt.Errorf("release readiness drill rows = %d, want 19", report.DrillRows)
	}
	if report.DrillStatuses["placeholder"] != 19 {
		return fmt.Errorf("release readiness placeholder rows = %d, want 19", report.DrillStatuses["placeholder"])
	}
	if !report.AlertValidationPassed {
		return errors.New("release readiness alert validation did not pass")
	}
	data, err := report.JSON()
	if err != nil {
		return err
	}
	var decoded gateReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("release readiness report JSON did not decode: %w", err)
	}
	if decoded.EvidencePacket.LastGreen.Status != "drill-d19" ||
		evidencePacketItemStatus(decoded.EvidencePacket, "b1-live-deploy-proof-contract") != "pass" ||
		evidencePacketItemStatus(decoded.EvidencePacket, "b2-operator-install-path-contract") != "pass" ||
		evidencePacketItemStatus(decoded.EvidencePacket, "b4-canonical-proof-packet-contract") != "pass" {
		return fmt.Errorf("release readiness evidence packet = %+v, want last-green and B1/B2/B4 contracts", decoded.EvidencePacket)
	}
	return nil
}

func evidencePacketItemStatus(packet evidencePacket, name string) string {
	for _, item := range packet.Items {
		if item.Name == name {
			return item.Status
		}
	}
	return ""
}

func validateSupportBundleSelector() error {
	if err := (supportbundle.Selector{}).Validate(); err == nil {
		return errors.New("empty support bundle selector unexpectedly passed validation")
	}
	if err := (supportbundle.Selector{Components: []string{"api"}, Logs: true}).Validate(); err != nil {
		return fmt.Errorf("bounded support bundle selector failed validation: %w", err)
	}
	if _, err := supportbundle.BuildPreviewManifest("drill-d10", drillCreatedAt(), supportbundle.Selector{}, []supportbundle.PreviewFile{{Path: "logs/api.log", Kind: "log"}}); err == nil {
		return errors.New("unbounded support bundle preview unexpectedly passed validation")
	}
	return nil
}

func validateSupportBundleBlockedPaths() error {
	tests := []struct {
		path string
		want supportbundle.BlockedPathClass
	}{
		{path: "logs/app-secret.txt", want: supportbundle.BlockedPathSecret},
		{path: "debug/session-token.log", want: supportbundle.BlockedPathToken},
		{path: "certs/acme-account.key", want: supportbundle.BlockedPathACME},
	}
	for _, tt := range tests {
		if got := supportbundle.ClassifyHardBlockedPath(tt.path); got != tt.want {
			return fmt.Errorf("ClassifyHardBlockedPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
		_, err := supportbundle.BuildPreviewManifest("drill-d11", drillCreatedAt(), supportbundle.Selector{Logs: true}, []supportbundle.PreviewFile{{Path: tt.path, Kind: "log"}})
		if err == nil {
			return fmt.Errorf("hard-blocked path %q unexpectedly passed preview validation", tt.path)
		}
	}
	return nil
}

func validateSupportBundleManifestReview() error {
	data, err := supportbundle.BuildPreviewJSON("drill-d12", drillCreatedAt(), supportbundle.Selector{Components: []string{"api"}, Logs: true}, []supportbundle.PreviewFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 42},
		{Path: "events/api.json", Kind: "event", Bytes: 12},
	})
	if err != nil {
		return err
	}
	var manifest supportbundle.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("preview manifest JSON did not decode: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("preview manifest failed review validation: %w", err)
	}
	for _, file := range manifest.Files {
		cleaned := filepath.Clean(file.Path)
		if filepath.IsAbs(file.Path) || strings.HasPrefix(cleaned, "..") || supportbundle.ClassifyHardBlockedPath(file.Path) != supportbundle.BlockedPathNone {
			return fmt.Errorf("preview manifest contains unsafe path %q", file.Path)
		}
	}
	return nil
}

func validateBackupLocalURI() error {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "local://backups/nightly", want: filepath.Join("backups", "nightly")},
		{raw: "local:///var/lib/midnight/backups", want: filepath.FromSlash("/var/lib/midnight/backups")},
		{raw: "file:///var/lib/midnight/backups", want: filepath.FromSlash("/var/lib/midnight/backups")},
	}
	for _, tt := range tests {
		got, err := backup.ParseLocalURI(tt.raw)
		if err != nil {
			return fmt.Errorf("ParseLocalURI(%q) failed validation: %w", tt.raw, err)
		}
		if got != tt.want {
			return fmt.Errorf("ParseLocalURI(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}

	for _, raw := range []string{"", "s3://bucket/path", "local://", "file://", "local://backups?token=secret"} {
		if _, err := backup.ParseLocalURI(raw); err == nil {
			return fmt.Errorf("ParseLocalURI(%q) unexpectedly passed validation", raw)
		}
	}
	return nil
}

func validateBackupDestinationLocalIO() error {
	root, err := os.MkdirTemp("", "midnight-drill-backups-*")
	if err != nil {
		return fmt.Errorf("MkdirTemp() failed validation: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	destination, err := backup.NewLocalDestination(root)
	if err != nil {
		return fmt.Errorf("NewLocalDestination() failed validation: %w", err)
	}
	if _, err := backup.NewLocalDestination(" "); err == nil {
		return errors.New("NewLocalDestination() unexpectedly accepted an empty root")
	}

	ctx := context.Background()
	object, err := destination.Put(ctx, "runs/drill/backup.tar", strings.NewReader("data"))
	if err != nil {
		return fmt.Errorf("Put() failed validation: %w", err)
	}
	if object.Key != "runs/drill/backup.tar" || object.Bytes != 4 || object.SHA256 == "" {
		return fmt.Errorf("Put() object = %+v, want persisted object with key, bytes, and checksum", object)
	}
	reader, gotObject, err := destination.Get(ctx, object.Key)
	if err != nil {
		return fmt.Errorf("Get() failed validation: %w", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return fmt.Errorf("Get() read failed validation: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("Get() close failed validation: %w", closeErr)
	}
	if string(data) != "data" || gotObject.Key != object.Key || gotObject.Bytes != object.Bytes {
		return fmt.Errorf("Get() = %q/%+v, want round-trip object %+v", string(data), gotObject, object)
	}
	objects, err := destination.List(ctx, "runs/")
	if err != nil {
		return fmt.Errorf("List() failed validation: %w", err)
	}
	if len(objects) != 1 || objects[0].Key != object.Key || objects[0].Bytes != object.Bytes {
		return fmt.Errorf("List() = %+v, want one persisted object", objects)
	}
	if _, err := destination.Put(ctx, "../escape.tar", strings.NewReader("bad")); err == nil {
		return errors.New("Put() unexpectedly accepted an escaping key")
	}
	return nil
}

func validateRestoreChecksumReadiness() error {
	checkedAt := drillCreatedAt()
	ready := backup.ReviewRestoreReadiness(backup.RestoreReadinessInput{
		Steps: []backup.RestoreStep{
			{Name: "load-database", State: backup.RestoreStepSucceeded},
			{Name: "load-config", State: backup.RestoreStepSucceeded},
		},
		SmokeTestResults: []backup.RestoreSmokeProbeResult{
			{Name: "read-postgres", State: backup.RestoreStepSucceeded, CheckedAt: checkedAt, Object: backup.Object{Key: "database/postgres.dump", Bytes: 100, SHA256: "sha256:abc"}},
		},
		ChecksumSummary: backup.ChecksumVerificationSummary{Total: 2, Verified: 2, OverallState: backup.RestoreStepSucceeded},
		RollbackOwner:   "platform-oncall",
	})
	if ready.Status != backup.RestoreReadinessStatusReady || ready.ChecksumSummary.Verified != 2 || ready.RollbackOwner != "platform-oncall" {
		return fmt.Errorf("ReviewRestoreReadiness(ready) = %+v, want checksum-ready restore", ready)
	}

	blocked := backup.ReviewRestoreReadiness(backup.RestoreReadinessInput{
		Steps: []backup.RestoreStep{
			{Name: "load-database", State: backup.RestoreStepSucceeded},
		},
		SmokeTestResults: []backup.RestoreSmokeProbeResult{
			{Name: "read-postgres", State: backup.RestoreStepSucceeded, CheckedAt: checkedAt, Object: backup.Object{Key: "database/postgres.dump", Bytes: 100, SHA256: "sha256:abc"}},
		},
		ChecksumSummary: backup.ChecksumVerificationSummary{Total: 2, Verified: 1, Failed: 1, OverallState: backup.RestoreStepFailed, FailedReasons: map[string]int{"checksum-mismatch": 1}},
		RollbackOwner:   "TBD",
	})
	if blocked.Status != backup.RestoreReadinessStatusBlocked {
		return fmt.Errorf("ReviewRestoreReadiness(blocked) status = %q, want %q", blocked.Status, backup.RestoreReadinessStatusBlocked)
	}
	if !hasRestoreReadinessIssue(blocked.Issues, backup.RestoreReadinessIssueChecksumFailed) || !hasRestoreReadinessIssue(blocked.Issues, backup.RestoreReadinessIssueRollbackOwnerTBD) {
		return fmt.Errorf("ReviewRestoreReadiness(blocked) issues = %+v, want checksum failure and rollback owner review", blocked.Issues)
	}
	return nil
}

func hasRestoreReadinessIssue(issues []backup.RestoreReadinessIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func drillCreatedAt() time.Time {
	return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
}

func summarizeDrillExecution(adapter, adapterType string, groups []harness.DrillAssertionResults) (drillExecutionSummary, error) {
	summary := drillExecutionSummary{
		Adapter:       adapter,
		AdapterType:   adapterType,
		DrillStatuses: make(map[harness.DrillStatus]int),
		Results:       make([]harness.AssertionResult, 0, len(groups)),
		Evaluations:   make([]harness.DrillEvaluation, 0, len(groups)),
	}
	for _, group := range groups {
		summary.Results = append(summary.Results, group.Assertions...)
	}

	evaluations, err := harness.EvaluateDrills(groups)
	if err != nil {
		return drillExecutionSummary{}, err
	}
	summary.Evaluations = evaluations
	summary.Assertions = len(summary.Results)
	for _, result := range summary.Results {
		switch result.Status {
		case harness.AssertionPassed:
			summary.Passed++
		case harness.AssertionFailed:
			summary.Failed++
		case harness.AssertionSkipped:
			summary.Skipped++
		}
	}
	for _, evaluation := range evaluations {
		summary.DrillStatuses[evaluation.Status]++
	}
	return summary, nil
}

func placeholderAssertionID(drill drill) string {
	return drill.ID + ".pending"
}
