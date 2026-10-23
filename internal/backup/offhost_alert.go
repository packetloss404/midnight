package backup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// OffhostAlertSeverity is the panel-locked alert severity tag.
type OffhostAlertSeverity string

const (
	// SeverityWarn is the steady-state level emitted when no off-host
	// destination is configured (BACKUP_OFFHOST_MISSING).
	SeverityWarn OffhostAlertSeverity = "warn"
	// SeverityCritical is emitted when the latest successful backup
	// is older than the configured staleness threshold (BACKUP_STALE).
	SeverityCritical OffhostAlertSeverity = "critical"
)

// OffhostAlert is the typed alert row consumed by the Prometheus
// rules in deploy/prometheus/alerts.yaml. Phase 1 wires this through
// the existing alert channel; the typed shape is what the runbook +
// audit emit reference.
type OffhostAlert struct {
	Code      failurecodes.Code    `json:"code"`
	Severity  OffhostAlertSeverity `json:"severity"`
	Reason    string               `json:"reason"`
	URI       string               `json:"destination_uri,omitempty"`
	Age       time.Duration        `json:"age,omitempty"`
	Threshold time.Duration        `json:"threshold,omitempty"`
	EmittedAt time.Time            `json:"emitted_at"`
}

// OffhostAlertInput captures the inputs the alert evaluator needs.
type OffhostAlertInput struct {
	DestinationURI string
	StaleAfter     time.Duration
	Now            time.Time
}

// EvaluateOffhostAlert returns a typed alert when the configured
// destination URI is empty (warn), or when the latest backup at the
// destination is older than StaleAfter (critical), or no alert when
// everything is healthy.
//
// The destination probe is delegated through dest. When dest is nil
// (no destination configured), only the URI guard fires.
func EvaluateOffhostAlert(ctx context.Context, dest *S3Destination, in OffhostAlertInput) (*OffhostAlert, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(in.DestinationURI) == "" {
		return &OffhostAlert{
			Code:      failurecodes.BackupOffhostMissing,
			Severity:  SeverityWarn,
			Reason:    "no off-host backup destination configured",
			EmittedAt: now,
		}, nil
	}
	if dest == nil {
		return nil, nil
	}
	threshold := in.StaleAfter
	if threshold <= 0 {
		threshold = 26 * time.Hour
	}
	stale, found, err := dest.IsStale(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("evaluate offhost alert: %w", err)
	}
	if !found {
		return &OffhostAlert{
			Code:      failurecodes.BackupStale,
			Severity:  SeverityCritical,
			Reason:    "destination contains no backup objects",
			URI:       in.DestinationURI,
			Threshold: threshold,
			EmittedAt: now,
		}, nil
	}
	if stale {
		age, _, _ := dest.LatestSuccessfulBackupAge(ctx, "")
		return &OffhostAlert{
			Code:      failurecodes.BackupStale,
			Severity:  SeverityCritical,
			Reason:    "latest successful backup is older than the configured threshold",
			URI:       in.DestinationURI,
			Age:       age,
			Threshold: threshold,
			EmittedAt: now,
		}, nil
	}
	return nil, nil
}
