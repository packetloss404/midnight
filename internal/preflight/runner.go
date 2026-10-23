package preflight

import (
	"context"
	"fmt"
)

// Status is the normalized outcome for an installer preflight check.
type Status string

const (
	StatusPass        Status = "pass"
	StatusWarn        Status = "warn"
	StatusFail        Status = "fail"
	StatusSkip        Status = "skip"
	StatusConfigError Status = "config_error"
)

// Result captures the executable outcome for a preflight check.
type Result struct {
	ID               string
	Description      string
	Severity         string
	Status           Status
	Message          string
	Err              error
	SafeFixAvailable bool
}

// CheckFunc executes a preflight check.
type CheckFunc func(context.Context) Result

// RunnableCheck binds preflight metadata to executable logic.
type RunnableCheck struct {
	ID          string
	Description string
	Severity    string
	Run         CheckFunc
}

// RunChecks executes checks in input order and preserves one result per check.
func RunChecks(ctx context.Context, checks []RunnableCheck) []Result {
	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		result := Result{
			ID:          check.ID,
			Description: check.Description,
			Severity:    check.Severity,
		}
		if check.Run == nil {
			result.Status = StatusSkip
			result.Message = "check has no runner"
			results = append(results, result)
			continue
		}

		result = check.Run(ctx)
		if result.ID == "" {
			result.ID = check.ID
		}
		if result.Description == "" {
			result.Description = check.Description
		}
		if result.Severity == "" {
			result.Severity = check.Severity
		}
		if result.Status == "" {
			result.Status = StatusFail
			result.Err = fmt.Errorf("check %s returned empty status", check.ID)
		}
		results = append(results, result)
	}
	return results
}
