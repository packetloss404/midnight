package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildAlertPackagingEvidenceReady(t *testing.T) {
	evidence := BuildAlertPackagingEvidence("deploy/prometheus/alerts.yaml", validPrometheusAlertsYAML())

	if evidence.Status != AlertPackagingEvidenceStatusReady {
		t.Fatalf("BuildAlertPackagingEvidence() status = %q, want ready", evidence.Status)
	}
	if evidence.AlertCount != len(LockedAlerts) || evidence.LockedAlertCount != len(LockedAlerts) || len(evidence.Issues) != 0 {
		t.Fatalf("BuildAlertPackagingEvidence() = %+v, want ready locked alert evidence", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestBuildAlertPackagingEvidenceBlocksDrift(t *testing.T) {
	data := []byte(strings.Replace(string(validPrometheusAlertsYAML()), "midnight_disk_used_ratio >= 0.85", "midnight_disk_used_ratio >= 0.95", 1))
	evidence := BuildAlertPackagingEvidence("deploy/prometheus/alerts.yaml", data)

	if evidence.Status != AlertPackagingEvidenceStatusBlocked {
		t.Fatalf("BuildAlertPackagingEvidence() status = %q, want blocked", evidence.Status)
	}
	if len(evidence.Issues) != 1 || evidence.Issues[0].Code != AlertPackagingEvidenceIssueLockedAlertsInvalid {
		t.Fatalf("BuildAlertPackagingEvidence() issues = %+v, want locked alert drift issue", evidence.Issues)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAlertPackagingEvidenceValidateRejectsUnsafePath(t *testing.T) {
	evidence := BuildAlertPackagingEvidence("../alerts.yaml", validPrometheusAlertsYAML())

	if err := evidence.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsafe path error")
	}
}

func TestMarshalAlertPackagingEvidenceJSON(t *testing.T) {
	evidence := BuildAlertPackagingEvidence("deploy/prometheus/alerts.yaml", validPrometheusAlertsYAML())
	data, err := MarshalAlertPackagingEvidenceJSON(evidence)
	if err != nil {
		t.Fatalf("MarshalAlertPackagingEvidenceJSON() error = %v", err)
	}

	var got AlertPackagingEvidence
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalAlertPackagingEvidenceJSON() returned invalid JSON: %v", err)
	}
	if got.Path != "deploy/prometheus/alerts.yaml" || got.Status != AlertPackagingEvidenceStatusReady {
		t.Fatalf("MarshalAlertPackagingEvidenceJSON() = %+v, want ready evidence", got)
	}
}

func validPrometheusAlertsYAML() []byte {
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
