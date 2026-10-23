package observability

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// AlertMetadata is the panel-locked descriptor for one Phase 0 alert beyond the bare AlertRule.
//
// AlertID is a stable kebab-case identifier wired into runbooks, support bundles, and dashboard
// panels so renames cannot break operator runbooks. RunbookLink should resolve to a packaged file
// or HTTPS URL; a relative `docs/runbooks/...` path is recommended for `.deb`-shipped runbooks.
// Summary is a one-line operator-facing description used in dashboard tooltips and email.
type AlertMetadata struct {
	AlertID              string `json:"alertId"`
	Name                 string `json:"name"`
	Severity             string `json:"severity"`
	RunbookLink          string `json:"runbookLink"`
	SuggestedFirstAction string `json:"suggestedFirstAction"`
	Summary              string `json:"summary,omitempty"`
}

// LockedAlertMetadata is the panel-locked metadata for the eight Phase 0 alerts. Order matches LockedAlerts.
var LockedAlertMetadata = []AlertMetadata{
	{
		AlertID:              "alert-disk-pressure",
		Name:                 "DiskPressure",
		Severity:             "critical",
		RunbookLink:          "docs/runbooks/disk-pressure.md",
		SuggestedFirstAction: "Run `midnight doctor` and free space on the affected mount before lifting block thresholds.",
		Summary:              "Volume usage is at or above the disk-pressure critical threshold; new builds and deploys may be blocked.",
	},
	{
		AlertID:              "alert-agent-lost",
		Name:                 "AgentLost",
		Severity:             "critical",
		RunbookLink:          "docs/runbooks/alert-triage.md",
		SuggestedFirstAction: "Restart the host agent service and confirm heartbeats resume within the lease TTL.",
		Summary:              "Agent heartbeat has not been received within the lease TTL window.",
	},
	{
		AlertID:              "alert-backup-failed",
		Name:                 "BackupFailed",
		Severity:             "critical",
		RunbookLink:          "docs/runbooks/backup-restore.md",
		SuggestedFirstAction: "Inspect the latest backup attempt log and re-run `midnight backup create --platform`.",
		Summary:              "The most recent platform backup attempt finished with a failure status.",
	},
	{
		AlertID:              "alert-cert-expiring-soon",
		Name:                 "CertExpiringSoon",
		Severity:             "warning",
		RunbookLink:          "docs/runbooks/alert-triage.md",
		SuggestedFirstAction: "Confirm ACME issuance is unblocked and force a renewal for the affected domain.",
		Summary:              "One or more certificates expire within the renewal warn window.",
	},
	{
		AlertID:              "alert-deploy-job-stuck",
		Name:                 "DeployJobStuck",
		Severity:             "critical",
		RunbookLink:          "docs/runbooks/alert-triage.md",
		SuggestedFirstAction: "Drain the stuck job's host, then retry the deploy after restoring queue heartbeats.",
		Summary:              "A deploy queue job heartbeat gap exceeds three lease TTLs.",
	},
	{
		AlertID:              "alert-buildkit-cache-corruption",
		Name:                 "BuildKitCacheCorruption",
		Severity:             "warning",
		RunbookLink:          "docs/runbooks/alert-triage.md",
		SuggestedFirstAction: "Quarantine the affected BuildKit cache directory and trigger a rebuild from a clean state.",
		Summary:              "BuildKit cache verifier reported corruption within the last 10 minutes.",
	},
	{
		AlertID:              "alert-conntrack-pressure",
		Name:                 "ConntrackPressure",
		Severity:             "warning",
		RunbookLink:          "docs/runbooks/alert-triage.md",
		SuggestedFirstAction: "Inspect long-lived TCP flows and tune `net.netfilter.nf_conntrack_max` per the runbook.",
		Summary:              "Conntrack table usage exceeds 80% of nf_conntrack_max.",
	},
	{
		AlertID:              "alert-backup-offhost-missing",
		Name:                 "BackupOffhostMissing",
		Severity:             "warning",
		RunbookLink:          "docs/runbooks/backup-restore.md",
		SuggestedFirstAction: "Configure at least one off-host destination via `midnight backup destination add`.",
		Summary:              "No off-host backup destination is configured for the platform install.",
	},
}

// AlertMetadataByName returns the locked metadata for one alert name.
func AlertMetadataByName(name string) (AlertMetadata, bool) {
	for _, meta := range LockedAlertMetadata {
		if meta.Name == name {
			return meta, true
		}
	}
	return AlertMetadata{}, false
}

// ValidateAlertMetadataSet checks that the locked alert metadata is internally consistent and
// covers exactly the locked alert set. Use this in package tests to fail fast on metadata drift.
func ValidateAlertMetadataSet(metadata []AlertMetadata) error {
	if len(metadata) != len(LockedAlerts) {
		return fmt.Errorf("expected %d alert metadata entries, got %d", len(LockedAlerts), len(metadata))
	}
	seenIDs := make(map[string]struct{}, len(metadata))
	seenNames := make(map[string]struct{}, len(metadata))
	for i, entry := range metadata {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("metadata[%d]: %w", i, err)
		}
		if _, dup := seenIDs[entry.AlertID]; dup {
			return fmt.Errorf("metadata[%d]: duplicate alert id %q", i, entry.AlertID)
		}
		if _, dup := seenNames[entry.Name]; dup {
			return fmt.Errorf("metadata[%d]: duplicate alert name %q", i, entry.Name)
		}
		seenIDs[entry.AlertID] = struct{}{}
		seenNames[entry.Name] = struct{}{}
		if _, ok := lockedAlertNames[entry.Name]; !ok {
			return fmt.Errorf("metadata[%d]: alert name %q is not in the locked set", i, entry.Name)
		}
	}
	// Confirm full coverage.
	wantNames := make([]string, 0, len(lockedAlertNames))
	for name := range lockedAlertNames {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)
	for _, name := range wantNames {
		if _, ok := seenNames[name]; !ok {
			return fmt.Errorf("missing alert metadata for %q", name)
		}
	}
	return nil
}

// ValidateStrict applies the panel-locked Phase 1.5 alert metadata gate: severity,
// runbook_link, first_action, alert_id, and summary must all be present and non-empty.
//
// ValidateStrict is intentionally additive over Validate: it requires every field
// (including the previously optional Summary) so the alert pack can be shipped with
// a single source of truth used for runbooks, dashboard tooltips, and email blurbs.
// Returned errors enumerate each missing field for operator-friendly diagnostics.
func ValidateStrict(meta AlertMetadata) error {
	var missing []string
	if strings.TrimSpace(meta.AlertID) == "" {
		missing = append(missing, "alert_id")
	}
	if strings.TrimSpace(meta.Severity) == "" {
		missing = append(missing, "severity")
	}
	if strings.TrimSpace(meta.RunbookLink) == "" {
		missing = append(missing, "runbook_link")
	}
	if strings.TrimSpace(meta.SuggestedFirstAction) == "" {
		missing = append(missing, "first_action")
	}
	if strings.TrimSpace(meta.Summary) == "" {
		missing = append(missing, "summary")
	}
	if len(missing) > 0 {
		return failurecodes.New(failurecodes.AlertMetadataInvalid,
			fmt.Sprintf("alert metadata strict validation failed: missing %s", strings.Join(missing, ", ")))
	}
	// Reuse the looser validator to enforce shape rules once required fields are present.
	if err := meta.Validate(); err != nil {
		return failurecodes.Wrap(failurecodes.AlertMetadataInvalid, "alert metadata strict validation failed", err)
	}
	return nil
}

// Validate checks that one alert metadata entry has all required fields with safe shapes.
func (m AlertMetadata) Validate() error {
	if strings.TrimSpace(m.AlertID) == "" {
		return errors.New("alertId is required")
	}
	if !isKebabAlertID(m.AlertID) {
		return fmt.Errorf("alertId %q must be lowercase kebab-case", m.AlertID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	switch m.Severity {
	case "critical", "warning", "info":
	default:
		return fmt.Errorf("severity %q is invalid", m.Severity)
	}
	if err := validateRunbookLink(m.RunbookLink); err != nil {
		return fmt.Errorf("runbookLink: %w", err)
	}
	if strings.TrimSpace(m.SuggestedFirstAction) == "" {
		return errors.New("suggestedFirstAction is required")
	}
	return nil
}

func isKebabAlertID(id string) bool {
	if id != strings.ToLower(id) {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	if strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") || strings.Contains(id, "--") {
		return false
	}
	return true
}

func validateRunbookLink(link string) error {
	link = strings.TrimSpace(link)
	if link == "" {
		return errors.New("required")
	}
	if strings.HasPrefix(link, "docs/runbooks/") && strings.HasSuffix(link, ".md") {
		return nil
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("must be a docs/runbooks/*.md path or an https URL, got %q", link)
	}
	return nil
}
