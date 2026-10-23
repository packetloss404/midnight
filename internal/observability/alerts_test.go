package observability

import "testing"

func TestValidateLockedAlerts(t *testing.T) {
	if err := ValidateLockedAlerts(LockedAlerts); err != nil {
		t.Fatalf("ValidateLockedAlerts() error = %v", err)
	}
}

func TestValidateLockedAlertsRejectsUnknownName(t *testing.T) {
	alerts := append([]AlertRule(nil), LockedAlerts...)
	alerts[0].Name = "MidnightAPIDown"

	if err := ValidateLockedAlerts(alerts); err == nil {
		t.Fatal("ValidateLockedAlerts() error = nil, want unknown-name error")
	}
}

func TestValidateLockedAlertsRejectsWrongCount(t *testing.T) {
	if err := ValidateLockedAlerts(LockedAlerts[:7]); err == nil {
		t.Fatal("ValidateLockedAlerts() error = nil, want count error")
	}
}

func TestValidatePrometheusAlerts(t *testing.T) {
	data := []byte(`groups:
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

	if err := ValidatePrometheusAlerts(data); err != nil {
		t.Fatalf("ValidatePrometheusAlerts() error = %v", err)
	}
}

func TestValidatePrometheusAlertsRejectsDrift(t *testing.T) {
	data := []byte(`groups:
  - name: midnight-phase0.locked
    rules:
      - alert: DiskPressure
        expr: midnight_disk_used_ratio >= 0.95
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

	if err := ValidatePrometheusAlerts(data); err == nil {
		t.Fatal("ValidatePrometheusAlerts() error = nil, want drift error")
	}
}
