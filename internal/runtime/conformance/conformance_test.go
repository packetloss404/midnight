package conformance_test

import (
	"context"
	"testing"

	"github.com/midnight/midnight/internal/runtime/conformance"
	"github.com/midnight/midnight/internal/runtime/local"
)

func TestLocalAdapterPassesConformance(t *testing.T) {
	adapter := local.New()
	report := conformance.Run(context.Background(), adapter, conformance.DefaultFixture())
	if report.Total == 0 {
		t.Fatal("expected at least one conformance check to run")
	}
	if !report.Ok() {
		for _, result := range report.Results {
			if !result.Passed {
				t.Errorf("conformance check %q failed: %s", result.Name, result.Message)
			}
		}
	}
}
