package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	coreaudit "github.com/midnight/midnight/internal/audit"
)

var ErrInvalidExportPlan = errors.New("invalid enterprise audit export plan")

type ExportFormat string

const ExportFormatNDJSON ExportFormat = "ndjson"

type DestinationType string

const (
	DestinationDownload DestinationType = "download"
	DestinationLogDrain DestinationType = "log_drain"
)

type ExportRequest struct {
	WorkspaceID        string           `json:"workspace_id"`
	RequestedBy        string           `json:"requested_by"`
	Purpose            string           `json:"purpose"`
	Format             ExportFormat     `json:"format"`
	Destination        DestinationType  `json:"destination"`
	DrainID            string           `json:"drain_id,omitempty"`
	Filter             coreaudit.Filter `json:"filter"`
	IncludeHashChain   bool             `json:"include_hash_chain"`
	IncludeManifest    bool             `json:"include_manifest"`
	RetentionExpiresAt time.Time        `json:"retention_expires_at,omitempty"`
}

type ExportPlan struct {
	WorkspaceID        string           `json:"workspace_id"`
	RequestedBy        string           `json:"requested_by"`
	Purpose            string           `json:"purpose"`
	Format             ExportFormat     `json:"format"`
	Destination        DestinationType  `json:"destination"`
	DrainID            string           `json:"drain_id,omitempty"`
	Filter             coreaudit.Filter `json:"filter"`
	IncludeHashChain   bool             `json:"include_hash_chain"`
	IncludeManifest    bool             `json:"include_manifest"`
	PlannedAuditEvents []string         `json:"planned_audit_events"`
}

func PlanExport(request ExportRequest) (ExportPlan, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return ExportPlan{}, fmt.Errorf("%w: workspace_id is required", ErrInvalidExportPlan)
	}
	if strings.TrimSpace(request.RequestedBy) == "" {
		return ExportPlan{}, fmt.Errorf("%w: requested_by is required", ErrInvalidExportPlan)
	}
	if strings.TrimSpace(request.Purpose) == "" {
		return ExportPlan{}, fmt.Errorf("%w: purpose is required", ErrInvalidExportPlan)
	}
	if request.Format != ExportFormatNDJSON {
		return ExportPlan{}, fmt.Errorf("%w: only ndjson export is supported", ErrInvalidExportPlan)
	}
	drainID := strings.TrimSpace(request.DrainID)
	if request.Destination != DestinationDownload && request.Destination != DestinationLogDrain {
		return ExportPlan{}, fmt.Errorf("%w: unsupported destination %q", ErrInvalidExportPlan, request.Destination)
	}
	if request.Destination == DestinationDownload && drainID != "" {
		return ExportPlan{}, fmt.Errorf("%w: drain_id is only valid for log_drain destination", ErrInvalidExportPlan)
	}
	if request.Destination == DestinationLogDrain && drainID == "" {
		return ExportPlan{}, fmt.Errorf("%w: drain_id is required for log_drain destination", ErrInvalidExportPlan)
	}
	if !request.Filter.Since.IsZero() && !request.Filter.Until.IsZero() {
		if request.Filter.Until.Before(request.Filter.Since) {
			return ExportPlan{}, fmt.Errorf("%w: until must be after since", ErrInvalidExportPlan)
		}
		if request.Filter.Until.Sub(request.Filter.Since) > 370*24*time.Hour {
			return ExportPlan{}, fmt.Errorf("%w: export window must not exceed 370 days", ErrInvalidExportPlan)
		}
	}
	if !request.RetentionExpiresAt.IsZero() && request.RetentionExpiresAt.Before(time.Now().UTC()) {
		return ExportPlan{}, fmt.Errorf("%w: retention_expires_at must be in the future", ErrInvalidExportPlan)
	}

	return ExportPlan{
		WorkspaceID:        request.WorkspaceID,
		RequestedBy:        request.RequestedBy,
		Purpose:            request.Purpose,
		Format:             request.Format,
		Destination:        request.Destination,
		DrainID:            drainID,
		Filter:             request.Filter,
		IncludeHashChain:   request.IncludeHashChain,
		IncludeManifest:    request.IncludeManifest,
		PlannedAuditEvents: []string{"enterprise.audit.export.planned", "audit.exported"},
	}, nil
}
