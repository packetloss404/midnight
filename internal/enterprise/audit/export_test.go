package audit

import (
	"errors"
	"testing"
	"time"

	coreaudit "github.com/midnight/midnight/internal/audit"
)

func TestPlanExportAllowsNDJSONDownloadWithHashChain(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := PlanExport(ExportRequest{
		WorkspaceID:      "wrk_acme",
		RequestedBy:      "usr_admin",
		Purpose:          "SOC2 evidence",
		Format:           ExportFormatNDJSON,
		Destination:      DestinationDownload,
		Filter:           coreaudit.Filter{Action: "deployment.created", Since: since, Until: since.Add(30 * 24 * time.Hour)},
		IncludeHashChain: true,
		IncludeManifest:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Format != ExportFormatNDJSON || plan.Destination != DestinationDownload || !plan.IncludeHashChain || !plan.IncludeManifest {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.PlannedAuditEvents) != 2 || plan.PlannedAuditEvents[0] != "enterprise.audit.export.planned" {
		t.Fatalf("planned audit events = %#v", plan.PlannedAuditEvents)
	}
}

func TestPlanExportValidatesDestinationAndWindow(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := PlanExport(ExportRequest{
		WorkspaceID: "wrk_acme",
		RequestedBy: "usr_admin",
		Purpose:     "SOC2 evidence",
		Format:      ExportFormatNDJSON,
		Destination: DestinationLogDrain,
		Filter:      coreaudit.Filter{Since: since, Until: since.Add(time.Hour)},
	})
	if !errors.Is(err, ErrInvalidExportPlan) {
		t.Fatalf("err = %v, want ErrInvalidExportPlan", err)
	}

	_, err = PlanExport(ExportRequest{
		WorkspaceID: "wrk_acme",
		RequestedBy: "usr_admin",
		Purpose:     "SOC2 evidence",
		Format:      ExportFormatNDJSON,
		Destination: DestinationDownload,
		DrainID:     "siem-primary",
		Filter:      coreaudit.Filter{Since: since, Until: since.Add(time.Hour)},
	})
	if !errors.Is(err, ErrInvalidExportPlan) {
		t.Fatalf("err = %v, want ErrInvalidExportPlan", err)
	}

	_, err = PlanExport(ExportRequest{
		WorkspaceID: "wrk_acme",
		RequestedBy: "usr_admin",
		Purpose:     "SOC2 evidence",
		Format:      ExportFormatNDJSON,
		Destination: DestinationDownload,
		Filter:      coreaudit.Filter{Since: since, Until: since.Add(371 * 24 * time.Hour)},
	})
	if !errors.Is(err, ErrInvalidExportPlan) {
		t.Fatalf("err = %v, want ErrInvalidExportPlan", err)
	}
}

func TestPlanExportTrimsLogDrainID(t *testing.T) {
	plan, err := PlanExport(ExportRequest{
		WorkspaceID: "wrk_acme",
		RequestedBy: "usr_admin",
		Purpose:     "SOC2 evidence",
		Format:      ExportFormatNDJSON,
		Destination: DestinationLogDrain,
		DrainID:     " siem-primary ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DrainID != "siem-primary" {
		t.Fatalf("DrainID = %q, want trimmed drain id", plan.DrainID)
	}
}
