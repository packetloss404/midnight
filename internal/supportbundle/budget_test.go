package supportbundle

import (
	"errors"
	"strings"
	"testing"
)

func TestEnforceCategoryBudgetsAcceptsWithinBudget(t *testing.T) {
	files := []PlannedFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 80},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 20},
	}
	if err := EnforceCategoryBudgets(files, map[string]int64{"log": 100, "metric": 100}, 1000); err != nil {
		t.Fatalf("EnforceCategoryBudgets() error = %v, want nil", err)
	}
}

func TestEnforceCategoryBudgetsReportsCategoryOverflow(t *testing.T) {
	files := []PlannedFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 200},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 50},
	}
	err := EnforceCategoryBudgets(files, map[string]int64{"log": 100, "metric": 100}, 0)
	if err == nil {
		t.Fatal("EnforceCategoryBudgets() error = nil, want budget exceeded")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("EnforceCategoryBudgets() error = %v, want ErrBudgetExceeded sentinel", err)
	}
	var breakdown *BudgetBreakdownError
	if !errors.As(err, &breakdown) {
		t.Fatalf("EnforceCategoryBudgets() error = %v, want *BudgetBreakdownError", err)
	}
	if breakdown.FailureCode != FailureCodeBudgetExceeded {
		t.Fatalf("breakdown.FailureCode = %q, want %q", breakdown.FailureCode, FailureCodeBudgetExceeded)
	}
	if len(breakdown.Categories) != 2 {
		t.Fatalf("breakdown.Categories = %+v, want two entries", breakdown.Categories)
	}
	wantOverflow := map[string]int64{"log": 100, "metric": 0}
	for _, c := range breakdown.Categories {
		if c.OverflowBytes != wantOverflow[c.Category] {
			t.Fatalf("breakdown[%q].OverflowBytes = %d, want %d", c.Category, c.OverflowBytes, wantOverflow[c.Category])
		}
	}
}

func TestEnforceCategoryBudgetsReportsTotalCapOverflow(t *testing.T) {
	files := []PlannedFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 50},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 50},
	}
	err := EnforceCategoryBudgets(files, map[string]int64{"log": 100, "metric": 100}, 80)
	if err == nil {
		t.Fatal("EnforceCategoryBudgets() error = nil, want total cap exceeded")
	}
	var breakdown *BudgetBreakdownError
	if !errors.As(err, &breakdown) {
		t.Fatalf("EnforceCategoryBudgets() error = %v, want breakdown error", err)
	}
	if breakdown.TotalBudget != 80 || breakdown.TotalAttempted != 100 || breakdown.TotalOverflow != 20 {
		t.Fatalf("breakdown total = %+v, want (80/100/+20)", breakdown)
	}
	if !strings.Contains(breakdown.Error(), FailureCodeBudgetExceeded) {
		t.Fatalf("breakdown.Error() = %q, missing failure code", breakdown.Error())
	}
}

func TestEnforceCategoryBudgetsTreatsMissingCategoryAsZeroBudget(t *testing.T) {
	files := []PlannedFile{
		{Path: "events/a.json", Kind: "event", Bytes: 10},
	}
	err := EnforceCategoryBudgets(files, map[string]int64{"log": 100}, 0)
	if err == nil {
		t.Fatal("EnforceCategoryBudgets() error = nil, want zero-budget overflow")
	}
}

func TestEnforceCategoryBudgetsRejectsNegativeBytes(t *testing.T) {
	files := []PlannedFile{{Path: "logs/api.log", Kind: "log", Bytes: -1}}
	if err := EnforceCategoryBudgets(files, nil, 0); err == nil {
		t.Fatal("EnforceCategoryBudgets() error = nil, want negative bytes error")
	}
}

func TestPreviewToPlannedCopiesFields(t *testing.T) {
	preview := []PreviewFile{{Path: "logs/api.log", Kind: "log", Bytes: 12}}
	planned := PreviewToPlanned(preview)
	if len(planned) != 1 || planned[0].Path != "logs/api.log" || planned[0].Bytes != 12 {
		t.Fatalf("PreviewToPlanned() = %+v, want copied PlannedFile", planned)
	}
}
