package backup

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestEvaluateOffhostAlertWarnsWhenURIEmpty(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	alert, err := EvaluateOffhostAlert(context.Background(), nil, OffhostAlertInput{Now: now})
	if err != nil {
		t.Fatalf("EvaluateOffhostAlert: %v", err)
	}
	if alert == nil {
		t.Fatal("expected alert when destination URI is empty")
	}
	if alert.Code != failurecodes.BackupOffhostMissing {
		t.Fatalf("Code = %s, want BACKUP_OFFHOST_MISSING", alert.Code)
	}
	if alert.Severity != SeverityWarn {
		t.Fatalf("Severity = %s, want warn", alert.Severity)
	}
}

func TestEvaluateOffhostAlertNoOpWhenURIPresentAndDestNil(t *testing.T) {
	t.Parallel()

	alert, err := EvaluateOffhostAlert(context.Background(), nil, OffhostAlertInput{DestinationURI: "s3://bucket/prefix"})
	if err != nil {
		t.Fatalf("EvaluateOffhostAlert: %v", err)
	}
	if alert != nil {
		t.Fatalf("expected nil alert when URI present and dest=nil; got %+v", alert)
	}
}
