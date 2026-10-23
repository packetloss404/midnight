package observability

import "errors"

// AlertRule is the minimal metadata for Phase 0 locked Prometheus alerts.
type AlertRule struct {
	Name        string
	Expression  string
	Severity    string
	Description string
}

// LockedAlerts are the Phase 0 alerts that must exist in packaged rules.
var LockedAlerts = []AlertRule{
	{Name: "DiskPressure", Expression: "midnight_disk_used_ratio >= 0.85", Severity: "critical", Description: "Disk usage has crossed the Phase 0 critical threshold."},
	{Name: "AgentLost", Expression: "midnight_agent_heartbeat_age_seconds > 15", Severity: "critical", Description: "A host agent heartbeat is stale."},
	{Name: "BackupFailed", Expression: "increase(midnight_backup_failed_total[15m]) > 0", Severity: "critical", Description: "A platform backup attempt failed."},
	{Name: "CertExpiringSoon", Expression: "midnight_certificate_expires_in_seconds < 604800", Severity: "warning", Description: "A managed certificate expires within seven days."},
	{Name: "DeployJobStuck", Expression: "midnight_deploy_job_heartbeat_gap_seconds > 180", Severity: "critical", Description: "A build or deploy job heartbeat gap exceeds 3x lease TTL."},
	{Name: "BuildKitCacheCorruption", Expression: "increase(midnight_buildkit_cache_corrupt_total[10m]) > 0", Severity: "warning", Description: "BuildKit cache verification detected corruption."},
	{Name: "ConntrackPressure", Expression: "midnight_conntrack_used_ratio > 0.80", Severity: "warning", Description: "Conntrack usage is above the Phase 0 warning threshold."},
	{Name: "BackupOffhostMissing", Expression: "midnight_backup_offhost_destinations_total == 0", Severity: "warning", Description: "No off-host backup destination is configured."},
}

var lockedAlertNames = map[string]struct{}{
	"DiskPressure":            {},
	"AgentLost":               {},
	"BackupFailed":            {},
	"CertExpiringSoon":        {},
	"DeployJobStuck":          {},
	"BuildKitCacheCorruption": {},
	"ConntrackPressure":       {},
	"BackupOffhostMissing":    {},
}

// ValidateLockedAlerts verifies the locked alert set remains complete and named.
func ValidateLockedAlerts(alerts []AlertRule) error {
	if len(alerts) != 8 {
		return errors.New("expected exactly eight locked alerts")
	}
	seen := make(map[string]struct{}, len(alerts))
	for _, alert := range alerts {
		if alert.Name == "" || alert.Expression == "" || alert.Severity == "" {
			return errors.New("locked alerts require name, expression, and severity")
		}
		if _, ok := lockedAlertNames[alert.Name]; !ok {
			return errors.New("locked alert name is not part of the Phase 0 alert set")
		}
		if _, ok := seen[alert.Name]; ok {
			return errors.New("locked alert names must be unique")
		}
		seen[alert.Name] = struct{}{}
	}
	return nil
}
