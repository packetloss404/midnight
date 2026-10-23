package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDrillManifest(t *testing.T) {
	data := []byte(validDrillManifest())

	if err := validateDrillManifest(data); err != nil {
		t.Fatalf("validateDrillManifest() error = %v", err)
	}
}

func TestRequiredDrillIDsDoNotIncludePhase3Reservations(t *testing.T) {
	required := requiredDrillIDs()
	for _, id := range []string{"D-20", "D-21", "D-22", "D-23", "D-24"} {
		if _, ok := required[id]; ok {
			t.Fatalf("requiredDrillIDs()[%q] present, want B7/B8 reservation-only drill outside execution manifest", id)
		}
	}
}

func TestValidateDrillManifestRejectsMissingRequiredID(t *testing.T) {
	data := []byte(`version: 1
owner: S08
drills:
  - id: D-01
    title: Drill 01
    status: placeholder
    reason-code: phase0.drill.01
    event: drill.event.01
    diagnostic: drill diagnostic 01
    runbook: docs/runbooks/support-bundle.md
`)

	if err := validateDrillManifest(data); err == nil {
		t.Fatal("validateDrillManifest() error = nil, want missing id error")
	}
}

func TestValidateDrillManifestRejectsMissingRequiredFields(t *testing.T) {
	firstReasonCode := phase0DrillReasonCodeAllowlist()[0]
	data := []byte(strings.Replace(validDrillManifest(), "    reason-code: "+firstReasonCode+"\n", "", 1))

	if err := validateDrillManifest(data); err == nil {
		t.Fatal("validateDrillManifest() error = nil, want missing required fields error")
	}
}

func TestValidateDrillManifestRequiresD14MetadataSubcases(t *testing.T) {
	data := []byte(strings.Replace(validDrillManifest(), "    metadata:\n      subcases:\n        - open-not-implemented\n        - write-not-implemented\n        - close-not-implemented\n", "", 1))

	if err := validateDrillManifest(data); err == nil {
		t.Fatal("validateDrillManifest() error = nil, want missing D-14 metadata subcases error")
	}
}

func TestParseDrillManifestReadsB6HostScopedCatalogMetadata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "drills", "manifest.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	manifest, err := parseDrillManifest(data)
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}

	drill, ok := findDrill(manifest, "D-08")
	if !ok {
		t.Fatal("D-08 missing from canonical manifest")
	}
	for _, tag := range []string{"phase3", "host-scoped", "local-only"} {
		if !hasMetadataValue(drill.MetadataTags, tag) {
			t.Fatalf("D-08 metadata tags = %v, want %q", drill.MetadataTags, tag)
		}
	}
	for _, note := range []string{
		"phase3-drill=conntrack-flush-correctness",
		"host-scope=runtime-host",
		"domain=networking",
		"evidence-ref=artifacts/phase3/drills/conntrack-flush/plan.json",
		"execution=planner-only",
	} {
		if !hasMetadataValue(drill.MetadataNotes, note) {
			t.Fatalf("D-08 metadata notes = %v, want %q", drill.MetadataNotes, note)
		}
	}
	for _, subcase := range []string{
		"phase3.host=runtime-host",
		"phase3.domain=networking",
		"phase3.planner=conntrack-flush-correctness",
		"phase3.evidence=artifacts/phase3/drills/conntrack-flush/plan.json",
	} {
		if !hasMetadataValue(drill.MetadataSubcases, subcase) {
			t.Fatalf("D-08 metadata subcases = %v, want %q", drill.MetadataSubcases, subcase)
		}
	}
	contract, err := buildPhase3HostDrillMetadataContract(manifest)
	if err != nil {
		t.Fatalf("buildPhase3HostDrillMetadataContract() error = %v", err)
	}
	if !contract.OfflineByDefault {
		t.Fatalf("OfflineByDefault = false, want local-only candidate catalog contract")
	}
	if contract.ID != phase3HostDrillMetadataID || contract.Sprint != "Phase 0 Beta Sprint B6" {
		t.Fatalf("contract identity = %q/%q, want B6 candidate catalog", contract.ID, contract.Sprint)
	}
	if strings.Join(contract.RequiredDomains, ",") != "networking,runtime_adapter,registry_cache,volume_attach" {
		t.Fatalf("RequiredDomains = %v, want all B6 readiness domains in stable order", contract.RequiredDomains)
	}
	if filepath.IsAbs(contract.Source) || strings.Contains(contract.Source, `\`) || strings.Contains(contract.Source, ":") || contract.Source != "test/drills/manifest.yaml" {
		t.Fatalf("Source = %q, want portable workspace-relative manifest path", contract.Source)
	}
	if len(contract.HostScopedDrills) < len(phase3HostMetadataRequiredDomains()) {
		t.Fatalf("HostScopedDrills = %+v, want B6 catalog coverage for all readiness domains", contract.HostScopedDrills)
	}
	covered := make(map[string]bool)
	var got phase3HostScopedDrillMetadata
	for _, drill := range contract.HostScopedDrills {
		covered[drill.Domain] = true
		if drill.DrillID == "D-08" {
			got = drill
		}
	}
	for _, domain := range phase3HostMetadataRequiredDomains() {
		if !covered[domain] {
			t.Fatalf("HostScopedDrills = %+v, missing required B6 domain %q", contract.HostScopedDrills, domain)
		}
	}
	if got.DrillID != "D-08" || got.HostScope != "runtime-host" || got.Domain != "networking" || got.Phase3Drill != "conntrack-flush-correctness" || got.EvidenceRef != "artifacts/phase3/drills/conntrack-flush/plan.json" || got.Execution != "planner-only" {
		t.Fatalf("D-08 catalog row = %+v, want runtime-host networking planner metadata", got)
	}
}

func TestValidateDrillManifestRequiresB6HostScopedCatalogMetadata(t *testing.T) {
	data := []byte(strings.Replace(validDrillManifest(), "        - host-scoped\n", "", 1))

	err := validateDrillManifest(data)
	if err == nil || !strings.Contains(err.Error(), "missing tag") {
		t.Fatalf("validateDrillManifest() error = %v, want missing B6 host-scoped catalog metadata error", err)
	}
}

func TestValidateDrillManifestRejectsB6HostLocalCatalogMetadata(t *testing.T) {
	for _, value := range []string{
		"host-scope=C:/Users/operator",
		`host-scope=control\plane`,
		"host-scope=https://example.invalid/host",
	} {
		data := []byte(strings.Replace(validDrillManifest(), "host-scope=runtime-host", value, 1))

		err := validateDrillManifest(data)
		if err == nil || !strings.Contains(err.Error(), "must be a stable symbolic id") {
			t.Fatalf("validateDrillManifest(%q) error = %v, want host-local metadata error", value, err)
		}
	}
}

func TestValidateDrillManifestRejectsB6NonPortableCatalogEvidenceRef(t *testing.T) {
	for _, value := range []string{
		"evidence-ref=C:/Users/operator/summary.json",
		"evidence-ref=../outside/summary.json",
		"evidence-ref=https://example.invalid/summary.json",
		`evidence-ref=artifacts\phase3\summary.json`,
	} {
		data := []byte(strings.Replace(validDrillManifest(), "evidence-ref=artifacts/phase3/drills/conntrack-flush/plan.json", value, 1))

		err := validateDrillManifest(data)
		if err == nil || !strings.Contains(err.Error(), "phase 3 evidence-ref") {
			t.Fatalf("validateDrillManifest(%q) error = %v, want non-portable evidence-ref error", value, err)
		}
	}
}

func TestValidateDrillManifestRequiresB6CatalogDomainCoverageWhenMultipleHostRows(t *testing.T) {
	data := addB6HostScopedMetadataToGeneratedDrill(validDrillManifest(), 9, "runtime-adapter-host", "runtime_adapter", "scheduler-reschedule", "artifacts/phase3/drills/scheduler-reschedule/plan.json")

	err := validateDrillManifest([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "must cover readiness domains") || !strings.Contains(err.Error(), "registry_cache") || !strings.Contains(err.Error(), "volume_attach") {
		t.Fatalf("validateDrillManifest() error = %v, want missing catalog domain coverage error", err)
	}
}

func TestValidateDrillManifestAcceptsB6CatalogCoveringAllReadinessDomains(t *testing.T) {
	data := validDrillManifest()
	data = addB6HostScopedMetadataToGeneratedDrill(data, 9, "runtime-adapter-host", "runtime_adapter", "scheduler-reschedule", "artifacts/phase3/drills/scheduler-reschedule/plan.json")
	data = addB6HostScopedMetadataToGeneratedDrill(data, 10, "registry-cache-host", "registry_cache", "registry-cache-rehydrate", "artifacts/phase3/drills/registry-cache-rehydrate/plan.json")
	data = addB6HostScopedMetadataToGeneratedDrill(data, 11, "volume-attach-host", "volume_attach", "volume-attach-retry", "artifacts/phase3/drills/volume-attach-retry/plan.json")

	manifest, err := parseDrillManifest([]byte(data))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	contract, err := buildPhase3HostDrillMetadataContract(manifest)
	if err != nil {
		t.Fatalf("buildPhase3HostDrillMetadataContract() error = %v", err)
	}
	if len(contract.HostScopedDrills) != 4 {
		t.Fatalf("HostScopedDrills = %+v, want four-domain B6 catalog", contract.HostScopedDrills)
	}
	covered := make(map[string]bool)
	for _, drill := range contract.HostScopedDrills {
		covered[drill.Domain] = true
	}
	for _, domain := range phase3HostMetadataRequiredDomains() {
		if !covered[domain] {
			t.Fatalf("HostScopedDrills = %+v, missing domain %q", contract.HostScopedDrills, domain)
		}
	}
}

func validDrillManifest() string {
	reasonCodes := phase0DrillReasonCodeAllowlist()
	events := phase0DrillEventAllowlist()
	var builder strings.Builder
	builder.WriteString("version: 1\nowner: S08\ndrills:\n")
	for i := 1; i <= 19; i++ {
		builder.WriteString(fmt.Sprintf("  - id: D-%02d\n", i))
		builder.WriteString(fmt.Sprintf("    title: Drill %02d\n", i))
		builder.WriteString("    status: placeholder\n")
		builder.WriteString(fmt.Sprintf("    reason-code: %s\n", reasonCodes[i-1]))
		builder.WriteString(fmt.Sprintf("    event: %s\n", events[i-1]))
		builder.WriteString(fmt.Sprintf("    diagnostic: drill diagnostic %02d\n", i))
		builder.WriteString("    runbook: docs/runbooks/support-bundle.md\n")
		if i == 8 {
			builder.WriteString(b6D08MetadataBlock())
		}
		if i == 14 {
			builder.WriteString("    metadata:\n")
			builder.WriteString("      subcases:\n")
			builder.WriteString("        - open-not-implemented\n")
			builder.WriteString("        - write-not-implemented\n")
			builder.WriteString("        - close-not-implemented\n")
		}
	}
	return builder.String()
}

func b6D08MetadataBlock() string {
	return b6HostScopedMetadataBlock("runtime-host", "networking", "conntrack-flush-correctness", "artifacts/phase3/drills/conntrack-flush/plan.json")
}

func b6HostScopedMetadataBlock(hostScope, domain, phase3Drill, evidenceRef string) string {
	return "    metadata:\n" +
		"      tags:\n" +
		"        - phase3\n" +
		"        - host-scoped\n" +
		"        - local-only\n" +
		"      notes:\n" +
		"        - phase3-drill=" + phase3Drill + "\n" +
		"        - host-scope=" + hostScope + "\n" +
		"        - domain=" + domain + "\n" +
		"        - evidence-ref=" + evidenceRef + "\n" +
		"        - execution=planner-only\n" +
		"      subcases:\n" +
		"        - phase3.host=" + hostScope + "\n" +
		"        - phase3.domain=" + domain + "\n" +
		"        - phase3.planner=" + phase3Drill + "\n" +
		"        - phase3.evidence=" + evidenceRef + "\n"
}

func addB6HostScopedMetadataToGeneratedDrill(data string, drillNumber int, hostScope, domain, phase3Drill, evidenceRef string) string {
	needle := fmt.Sprintf("    runbook: docs/runbooks/support-bundle.md\n  - id: D-%02d", drillNumber+1)
	replacement := "    runbook: docs/runbooks/support-bundle.md\n" + b6HostScopedMetadataBlock(hostScope, domain, phase3Drill, evidenceRef) + fmt.Sprintf("  - id: D-%02d", drillNumber+1)
	return strings.Replace(data, needle, replacement, 1)
}

func findDrill(manifest drillManifest, id string) (drill, bool) {
	for _, drill := range manifest.Drills {
		if drill.ID == id {
			return drill, true
		}
	}
	return drill{}, false
}

func TestValidateDrillManifestRejectsUnknownID(t *testing.T) {
	data := []byte(`version: 1
owner: S08
drills:
  - id: api-target-down
    title: API target down
    status: placeholder
`)

	if err := validateDrillManifest(data); err == nil {
		t.Fatal("validateDrillManifest() error = nil, want unknown id error")
	}
}

func TestBuildGateReportSummarizesCounts(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	if report.DrillRows != 19 || report.DrillStatuses["placeholder"] != 19 || report.AlertRules != 8 || !report.AlertValidationPassed {
		t.Fatalf("buildGateReport() = %+v, want drill and alert counts", report)
	}
	if got := report.String(); !strings.Contains(got, "drills=19") || !strings.Contains(got, "alerts=8") || !strings.Contains(got, "placeholder:19") {
		t.Fatalf("String() = %q, want counts", got)
	}
	if !strings.Contains(report.String(), "live_deploy_proof=pass") {
		t.Fatalf("String() = %q, want live deploy proof status", report.String())
	}
	if !strings.Contains(report.String(), "operator_install_path=pass") {
		t.Fatalf("String() = %q, want operator install path status", report.String())
	}
	markdown := report.Markdown()
	for _, want := range []string{"# Phase 0 Gate Report", "| Drill manifest rows | pass | 19 |", "| Prometheus alert rules | pass | 8 |", "| B1 live deploy proof contract | pass | 21 |", "| B2 operator install path contract | pass | 9 |", "| B6 Phase 3 host drill candidate catalog | pass | 1 |", "| placeholder | 19 |", "| D-08 | runtime-host | networking | conntrack-flush-correctness | planner-only | artifacts/phase3/drills/conntrack-flush/plan.json |"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("Markdown() = %q, want %q", markdown, want)
		}
	}
	jsonData, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded struct {
		DrillRows       int `json:"drillRows"`
		LiveDeployProof struct {
			ID               string `json:"id"`
			OfflineByDefault bool   `json:"offlineByDefault"`
		} `json:"liveDeployProof"`
		OperatorInstallPath struct {
			ID               string `json:"id"`
			OfflineByDefault bool   `json:"offlineByDefault"`
		} `json:"operatorInstallPath"`
		Phase3HostDrillMetadata struct {
			ID               string `json:"id"`
			OfflineByDefault bool   `json:"offlineByDefault"`
			HostScopedDrills []struct {
				DrillID   string `json:"drillId"`
				HostScope string `json:"hostScope"`
				Domain    string `json:"domain"`
			} `json:"hostScopedDrills"`
		} `json:"phase3HostDrillMetadata"`
		EvidencePacket struct {
			LastGreen struct {
				Status string `json:"status"`
			} `json:"lastGreen"`
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		} `json:"evidencePacket"`
	}
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("JSON() output did not unmarshal: %v", err)
	}
	if decoded.DrillRows != 19 || decoded.LiveDeployProof.ID != liveDeployProofID || !decoded.LiveDeployProof.OfflineByDefault || decoded.OperatorInstallPath.ID != operatorInstallPathID || !decoded.OperatorInstallPath.OfflineByDefault || decoded.Phase3HostDrillMetadata.ID != phase3HostDrillMetadataID || !decoded.Phase3HostDrillMetadata.OfflineByDefault || len(decoded.Phase3HostDrillMetadata.HostScopedDrills) != 1 || decoded.Phase3HostDrillMetadata.HostScopedDrills[0].HostScope != "runtime-host" || decoded.EvidencePacket.LastGreen.Status != "unknown" || len(decoded.EvidencePacket.Items) != 6 {
		t.Fatalf("JSON() decoded = %+v, want report with evidence packet", decoded)
	}
}

func TestBuildGateReportUsesLastGreenFlag(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "abc123")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	if report.EvidencePacket.LastGreen.Status != "abc123" || report.EvidencePacket.LastGreen.Source != "flag" {
		t.Fatalf("LastGreen = %+v, want flag-supplied sha", report.EvidencePacket.LastGreen)
	}
}

func TestBuildGateReportIncludesDrillExecutionWhenRequested(t *testing.T) {
	report, err := buildGateReportWithDrillExecution([]byte(validDrillManifest()), validAlertRules(), "unknown", placeholderDrillExecutionAdapter{}, false)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if report.DrillExecution == nil || report.DrillExecution.Assertions != 19 || report.DrillExecution.Skipped != 19 {
		t.Fatalf("DrillExecution = %+v, want placeholder execution summary", report.DrillExecution)
	}
	if len(report.EvidencePacket.Items) != 7 || evidenceItemStatus(report.EvidencePacket, "drill-execution") != "skipped" || evidenceItemStatus(report.EvidencePacket, "b1-live-deploy-proof-contract") != "pass" || evidenceItemStatus(report.EvidencePacket, "b2-operator-install-path-contract") != "pass" || evidenceItemStatus(report.EvidencePacket, "b4-canonical-proof-packet-contract") != "pass" || evidenceItemStatus(report.EvidencePacket, phase3HostDrillMetadataEvidenceName) != "pass" {
		t.Fatalf("EvidencePacket.Items = %+v, want skipped drill-execution evidence", report.EvidencePacket.Items)
	}

	jsonData, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded struct {
		DrillExecution struct {
			Adapter     string `json:"adapter"`
			AdapterType string `json:"adapterType"`
			Assertions  int    `json:"assertions"`
			Skipped     int    `json:"skipped"`
			Results     []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"results"`
		} `json:"drillExecution"`
	}
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("JSON() output did not unmarshal: %v", err)
	}
	if decoded.DrillExecution.Adapter != "placeholder" || decoded.DrillExecution.AdapterType != "placeholder" || decoded.DrillExecution.Assertions != 19 || decoded.DrillExecution.Skipped != 19 || decoded.DrillExecution.Results[0].ID != "D-01.pending" {
		t.Fatalf("decoded drillExecution = %+v, want placeholder execution JSON", decoded.DrillExecution)
	}
}

func TestBuildGateReportMarksRequiredDrillExecutionEvidencePass(t *testing.T) {
	report, err := buildGateReportWithDrillExecution([]byte(validDrillManifest()), validAlertRules(), "unknown", localDrillExecutionAdapter{manifestData: []byte(validDrillManifest()), alertData: validAlertRules()}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if report.DrillExecution == nil || !executedDrillsPassed(*report.DrillExecution) {
		t.Fatalf("DrillExecution = %+v, want fully passed local execution", report.DrillExecution)
	}
	if len(report.EvidencePacket.Items) != 7 || evidenceItemStatus(report.EvidencePacket, "drill-execution") != "pass" || evidenceItemStatus(report.EvidencePacket, "b4-canonical-proof-packet-contract") != "pass" || evidenceItemStatus(report.EvidencePacket, phase3HostDrillMetadataEvidenceName) != "pass" {
		t.Fatalf("EvidencePacket.Items = %+v, want passed drill-execution evidence", report.EvidencePacket.Items)
	}
}

func TestBuildGateReportMarksRequiredDrillExecutionEvidenceFail(t *testing.T) {
	manifestData := []byte(validDrillManifest())
	report, err := buildGateReportWithDrillExecution(manifestData, []byte("not prometheus alerts"), "unknown", localDrillExecutionAdapter{manifestData: manifestData, alertData: []byte("not prometheus alerts")}, true)
	if err != nil {
		t.Fatalf("buildGateReportWithDrillExecution() error = %v", err)
	}
	if report.DrillExecution == nil || executedDrillsPassed(*report.DrillExecution) || report.DrillExecution.Failed == 0 {
		t.Fatalf("DrillExecution = %+v, want failed local execution", report.DrillExecution)
	}
	if len(report.EvidencePacket.Items) != 7 || evidenceItemStatus(report.EvidencePacket, "drill-execution") != "fail" || evidenceItemStatus(report.EvidencePacket, "b4-canonical-proof-packet-contract") != "pass" || evidenceItemStatus(report.EvidencePacket, phase3HostDrillMetadataEvidenceName) != "pass" {
		t.Fatalf("EvidencePacket.Items = %+v, want failed drill-execution evidence", report.EvidencePacket.Items)
	}
}

func evidenceItemStatus(packet evidencePacket, name string) string {
	for _, item := range packet.Items {
		if item.Name == name {
			return item.Status
		}
	}
	return ""
}

func TestRenderSelectedFormats(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	for _, tc := range []struct {
		format string
		want   string
	}{
		{format: "text", want: "phase0 gate passed: drills=19"},
		{format: "markdown", want: "# Phase 0 Gate Report"},
		{format: "json", want: "\"evidencePacket\""},
	} {
		data, err := report.Render(tc.format)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", tc.format, err)
		}
		if !strings.Contains(string(data), tc.want) {
			t.Fatalf("Render(%q) = %q, want %q", tc.format, data, tc.want)
		}
	}
}

func TestBuildGateReportDoesNotFailInvalidAlerts(t *testing.T) {
	report, err := buildGateReport([]byte(validDrillManifest()), []byte("not prometheus alerts"), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v, want manifest-only gate failure", err)
	}
	if report.AlertRules != 0 || report.AlertValidationPassed {
		t.Fatalf("buildGateReport() = %+v, want invalid alerts reported without failing", report)
	}
}

func validAlertRules() []byte {
	return []byte(`groups:
  - name: midnight-phase0.locked
    rules:
      - alert: DiskPressure
        expr: midnight_disk_used_ratio >= 0.85
        labels:
          severity: critical
      - alert: AgentLost
        expr: midnight_agent_heartbeat_age_seconds > 15
        labels:
          severity: critical
      - alert: BackupFailed
        expr: increase(midnight_backup_failed_total[15m]) > 0
        labels:
          severity: critical
      - alert: CertExpiringSoon
        expr: midnight_certificate_expires_in_seconds < 604800
        labels:
          severity: warning
      - alert: DeployJobStuck
        expr: midnight_deploy_job_heartbeat_gap_seconds > 180
        labels:
          severity: critical
      - alert: BuildKitCacheCorruption
        expr: increase(midnight_buildkit_cache_corrupt_total[10m]) > 0
        labels:
          severity: warning
      - alert: ConntrackPressure
        expr: midnight_conntrack_used_ratio > 0.80
        labels:
          severity: warning
      - alert: BackupOffhostMissing
        expr: midnight_backup_offhost_destinations_total == 0
        labels:
          severity: warning
`)
}
