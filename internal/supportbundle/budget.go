package supportbundle

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// FailureCodeBudgetExceeded is the stable failure code surfaced when a support bundle
// exceeds its per-category or total archive byte budgets.
const FailureCodeBudgetExceeded = "SUPPORT_BUNDLE_BUDGET_EXCEEDED"

// ErrBudgetExceeded is the sentinel returned by EnforceCategoryBudgets when at least
// one category or the total cap is exceeded. Callers can match with errors.Is and
// type-assert to *BudgetBreakdownError to inspect the per-category breakdown.
var ErrBudgetExceeded = errors.New("support bundle budget exceeded")

// BudgetCategoryBreakdown captures one category's contribution toward an exceeded budget.
type BudgetCategoryBreakdown struct {
	Category       string `json:"category"`
	BudgetBytes    int64  `json:"budgetBytes"`
	AttemptedBytes int64  `json:"attemptedBytes"`
	OverflowBytes  int64  `json:"overflowBytes"`
}

// BudgetBreakdownError is returned when EnforceCategoryBudgets rejects a bundle plan.
//
// It carries the failure code, the per-category breakdown, and the total-archive overflow
// so reviewers and CLI surfaces can render a precise reason without recomputing the budget math.
type BudgetBreakdownError struct {
	FailureCode    string                    `json:"failureCode"`
	TotalBudget    int64                     `json:"totalBudget"`
	TotalAttempted int64                     `json:"totalAttempted"`
	TotalOverflow  int64                     `json:"totalOverflow"`
	Categories     []BudgetCategoryBreakdown `json:"categories"`
}

// Error implements error and renders a deterministic, reviewer-friendly summary string.
func (e *BudgetBreakdownError) Error() string {
	parts := make([]string, 0, len(e.Categories))
	for _, c := range e.Categories {
		parts = append(parts, fmt.Sprintf("%s=%d/%d (+%d)", c.Category, c.AttemptedBytes, c.BudgetBytes, c.OverflowBytes))
	}
	return fmt.Sprintf("%s: total=%d/%d (+%d); %s", e.FailureCode, e.TotalAttempted, e.TotalBudget, e.TotalOverflow, strings.Join(parts, ", "))
}

// Is reports that BudgetBreakdownError matches the ErrBudgetExceeded sentinel.
func (e *BudgetBreakdownError) Is(target error) bool {
	return target == ErrBudgetExceeded //nolint:errorlint // sentinel comparison is intentional
}

// EnforceCategoryBudgets validates per-category byte budgets and a total archive cap against a
// set of planned files. It returns nil when every category and the total cap are honored.
//
// When totalCap is zero or negative, only per-category budgets are enforced. When a category in
// files has no entry in budgets, the category is treated as having a zero budget so a planning
// bug cannot smuggle bytes into the archive without an explicit budget.
func EnforceCategoryBudgets(files []PlannedFile, budgets map[string]int64, totalCap int64) error {
	attempted := make(map[string]int64, len(budgets))
	categories := make(map[string]struct{}, len(budgets))
	var total int64
	for _, file := range files {
		if file.Bytes < 0 {
			return fmt.Errorf("planned file %q bytes must be non-negative", file.Path)
		}
		attempted[file.Kind] += file.Bytes
		categories[file.Kind] = struct{}{}
		total += file.Bytes
	}
	for category := range budgets {
		categories[category] = struct{}{}
	}
	breakdown := make([]BudgetCategoryBreakdown, 0, len(categories))
	overBudget := false
	for category := range categories {
		budget := budgets[category]
		used := attempted[category]
		overflow := used - budget
		if overflow < 0 {
			overflow = 0
		}
		if overflow > 0 {
			overBudget = true
		}
		breakdown = append(breakdown, BudgetCategoryBreakdown{
			Category:       category,
			BudgetBytes:    budget,
			AttemptedBytes: used,
			OverflowBytes:  overflow,
		})
	}
	sort.Slice(breakdown, func(i, j int) bool { return breakdown[i].Category < breakdown[j].Category })
	totalOverflow := int64(0)
	if totalCap > 0 && total > totalCap {
		totalOverflow = total - totalCap
		overBudget = true
	}
	if !overBudget {
		return nil
	}
	return &BudgetBreakdownError{
		FailureCode:    FailureCodeBudgetExceeded,
		TotalBudget:    totalCap,
		TotalAttempted: total,
		TotalOverflow:  totalOverflow,
		Categories:     breakdown,
	}
}

// PreviewToPlanned converts preview file metadata into planned file shape so callers can
// run the same byte-budget enforcement at preview time.
func PreviewToPlanned(files []PreviewFile) []PlannedFile {
	out := make([]PlannedFile, 0, len(files))
	for _, file := range files {
		out = append(out, PlannedFile{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes})
	}
	return out
}
