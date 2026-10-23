package main

import (
	"strings"
	"testing"

	"github.com/midnight/midnight/test/drills/harness"
)

func TestPlaceholderDrillExecutionAdapterReturnsSkippedAssertions(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}

	summary, err := (placeholderDrillExecutionAdapter{}).Execute(manifest)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if summary.Adapter != "placeholder" || summary.AdapterType != "placeholder" || summary.Assertions != 19 || summary.Skipped != 19 || summary.Failed != 0 || summary.Passed != 0 {
		t.Fatalf("Execute() = %+v, want placeholder summary with 19 skipped assertions", summary)
	}
	if len(summary.Results) != 19 || summary.Results[0].ID != "D-01.pending" || summary.Results[0].Status != harness.AssertionSkipped {
		t.Fatalf("Results[0] = %+v, want stable skipped pending assertion", summary.Results[0])
	}
	if summary.Results[13].ID != "D-14.pending" {
		t.Fatalf("Results[13].ID = %q, want D-14.pending", summary.Results[13].ID)
	}
	if summary.DrillStatuses[harness.DrillBlocked] != 19 {
		t.Fatalf("DrillStatuses = %+v, want 19 blocked placeholder drills", summary.DrillStatuses)
	}
}

func TestLocalDrillExecutionAdapterRunsGateValidators(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}

	summary, err := (localDrillExecutionAdapter{manifestData: manifestData, alertData: validAlertRules()}).Execute(manifest)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if summary.Adapter != "local" || summary.AdapterType != "local-safe" || summary.Assertions != 95 || summary.Passed != 95 || summary.Skipped != 0 || summary.Failed != 0 {
		t.Fatalf("Execute() = %+v, want all 95 real assertions (1 validator + 4 envelope) passed for 19 drills", summary)
	}
	if summary.DrillStatuses[harness.DrillPassed] != 19 || summary.DrillStatuses[harness.DrillBlocked] != 0 {
		t.Fatalf("DrillStatuses = %+v, want all local drills passed", summary.DrillStatuses)
	}
	wantHeadlines := []string{
		"D-01.api-target-health",
		"D-02.disk-pressure-alert-validation",
		"D-03.agent-lost-alert-validation",
		"D-04.backup-failed-alert-validation",
		"D-05.cert-expiry-classification",
		"D-06.deploy-job-stuck-alert-validation",
		"D-07.buildkit-cache-corruption-alert-validation",
		"D-08.conntrack-pressure-alert-validation",
		"D-09.backup-offhost-readiness",
		"D-10.supportbundle-selector-validation",
		"D-11.supportbundle-blocked-path-validation",
		"D-12.supportbundle-manifest-review",
		"D-13.backup-local-uri-validation",
		"D-14.backup-destination-local-io-validation",
		"D-15.restore-checksum-readiness",
		"D-16.alert-packaging-validation",
		"D-17.manifest-validation",
		"D-18.alert-validation",
		"D-19.release-readiness-route-promotion",
	}
	envelopeSuffixes := []string{".envelope.reason-code", ".envelope.event", ".envelope.runbook", ".envelope.diagnostic"}
	for i, headline := range wantHeadlines {
		base := i * 5
		if summary.Results[base].ID != headline || summary.Results[base].Status != harness.AssertionPassed {
			t.Fatalf("Results[%d] = %+v, want passed %q", base, summary.Results[base], headline)
		}
		drillID := strings.SplitN(headline, ".", 2)[0]
		for j, suffix := range envelopeSuffixes {
			got := summary.Results[base+1+j]
			if got.ID != drillID+suffix {
				t.Fatalf("Results[%d] = %+v, want %s%s", base+1+j, got, drillID, suffix)
			}
			if got.Status != harness.AssertionPassed {
				t.Fatalf("Results[%d] = %+v, want passed envelope assertion", base+1+j, got)
			}
		}
	}
}

func TestLocalDrillExecutionAdapterFailsRealValidatorAssertions(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	invalidManifestData := []byte(strings.Replace(validDrillManifest(), "version: 1", "version: 2", 1))

	summary, err := (localDrillExecutionAdapter{manifestData: invalidManifestData, alertData: []byte("not prometheus alerts")}).Execute(manifest)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if summary.Passed != 85 || summary.Failed != 10 || summary.Skipped != 0 || summary.DrillStatuses[harness.DrillPassed] != 9 || summary.DrillStatuses[harness.DrillFailed] != 10 || summary.DrillStatuses[harness.DrillBlocked] != 0 {
		t.Fatalf("Execute() = %+v, want package-backed alert/gate validators failed (10) and remaining envelope+headline assertions passed (85)", summary)
	}
	for _, drillIndex := range []int{1, 2, 3, 5, 6, 7, 15, 16, 17, 18} {
		base := drillIndex * 5
		if summary.Results[base].Status != harness.AssertionFailed || summary.Results[base].Diagnostic == "" {
			t.Fatalf("Results[%d] = %+v, want failed validator assertion with diagnostic", base, summary.Results[base])
		}
	}
}

func TestSupportBundleDrillValidators(t *testing.T) {
	validators := []struct {
		name     string
		validate func() error
	}{
		{name: "selector", validate: validateSupportBundleSelector},
		{name: "blocked paths", validate: validateSupportBundleBlockedPaths},
		{name: "manifest review", validate: validateSupportBundleManifestReview},
		{name: "API target health", validate: validateAPITargetHealth},
		{name: "certificate expiring soon", validate: validateCertificateExpiringSoon},
		{name: "disk pressure alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "DiskPressure") }},
		{name: "agent lost alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "AgentLost") }},
		{name: "backup failed alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "BackupFailed") }},
		{name: "deploy job stuck alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "DeployJobStuck") }},
		{name: "BuildKit cache corruption alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "BuildKitCacheCorruption") }},
		{name: "conntrack pressure alert", validate: func() error { return validatePackagedLockedAlert(validAlertRules(), "ConntrackPressure") }},
		{name: "backup offhost missing", validate: validateBackupOffhostMissing},
		{name: "backup local URI", validate: validateBackupLocalURI},
		{name: "backup destination local I/O", validate: validateBackupDestinationLocalIO},
		{name: "restore checksum readiness", validate: validateRestoreChecksumReadiness},
		{name: "deploy route promotion", validate: validateDeployRoutePromotionDrill},
		{name: "release readiness report", validate: func() error { return validateReleaseReadinessReport([]byte(validDrillManifest()), validAlertRules()) }},
		{name: "release readiness and deploy route promotion", validate: func() error {
			return validateReleaseReadinessAndDeployRoutePromotion([]byte(validDrillManifest()), validAlertRules())
		}},
	}

	for _, tt := range validators {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.validate(); err != nil {
				t.Fatalf("validate() error = %v", err)
			}
		})
	}
}
