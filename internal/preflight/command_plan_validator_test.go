package preflight

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestValidateSafeExecutionPlanValid(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusValid {
		t.Fatalf("Status = %q, want %q", review.Status, SafeExecutionPlanStatusValid)
	}
	if len(review.Findings) != 0 {
		t.Fatalf("Findings = %+v, want none for canonical plan", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsMissingID(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	plan[0].ID = ""
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want %q", review.Status, SafeExecutionPlanStatusInvalid)
	}
	if len(review.Findings) == 0 || review.Findings[0].Field != "id" {
		t.Fatalf("Findings = %+v, want id finding", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsDuplicateID(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	duplicate := plan[0]
	plan = append(plan, duplicate)
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want %q", review.Status, SafeExecutionPlanStatusInvalid)
	}
	foundDuplicate := false
	for _, finding := range review.Findings {
		if finding.SpecID == duplicate.ID && finding.Field == "id" {
			foundDuplicate = true
		}
	}
	if !foundDuplicate {
		t.Fatalf("Findings = %+v, want duplicate id finding", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsTamperedArgs(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	plan[0].Args = []string{"-fc", "%T", "/etc/passwd"}
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want %q", review.Status, SafeExecutionPlanStatusInvalid)
	}
	if len(review.Findings) == 0 || review.Findings[0].Field != "command" {
		t.Fatalf("Findings = %+v, want command finding", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsReorderedEntries(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	if len(plan) < 2 {
		t.Fatalf("CommandExecutionPlan() length = %d, want at least 2 entries to catch ordering regressions", len(plan))
	}
	plan[0], plan[1] = plan[1], plan[0]
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want invalid for reordered plan", review.Status)
	}
	foundOrdering := false
	for _, finding := range review.Findings {
		if finding.Field == "ordering" {
			foundOrdering = true
		}
	}
	if !foundOrdering {
		t.Fatalf("Findings = %+v, want ordering finding", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsTimeoutDrift(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	plan[0].Timeout = "10s"
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want invalid for tampered timeout", review.Status)
	}
	if len(review.Findings) == 0 || review.Findings[0].Field != "timeout" {
		t.Fatalf("Findings = %+v, want timeout finding", review.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsMismatchedCheckID(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	plan[0].CheckID = "PRE-WRONG-001"
	review := ValidateSafeExecutionPlan(plan)
	if review.Status != SafeExecutionPlanStatusInvalid {
		t.Fatalf("Status = %q, want invalid for tampered check id", review.Status)
	}
}

func TestSortPlanEntriesByAllowList(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	if len(plan) < 2 {
		t.Fatalf("CommandExecutionPlan() length = %d, want at least 2 entries to exercise canonical sorting", len(plan))
	}
	swapped := []CommandPlanEntry{plan[1], plan[0]}
	sorted := SortPlanEntriesByAllowList(swapped)
	if sorted[0].ID != plan[0].ID || sorted[1].ID != plan[1].ID {
		t.Fatalf("SortPlanEntriesByAllowList() = %+v, want canonical allow-list order", sorted)
	}
}

func TestMarshalSafeExecutionPlanValidationReviewJSON(t *testing.T) {
	t.Parallel()

	review := ValidateSafeExecutionPlan(CommandExecutionPlan())
	data, err := MarshalSafeExecutionPlanValidationReview(review)
	if err != nil {
		t.Fatalf("MarshalSafeExecutionPlanValidationReview() error = %v", err)
	}
	var decoded SafeExecutionPlanValidationReview
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(review) error = %v", err)
	}
	if decoded.Status != SafeExecutionPlanStatusValid {
		t.Fatalf("decoded.Status = %q, want %q", decoded.Status, SafeExecutionPlanStatusValid)
	}
}

func TestValidateSafeExecutionPlanNewPlanIsValid(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	if err := Validate(plan); err != nil {
		t.Fatalf("Validate(NewSafeExecutionPlan()) error = %v, want nil", err)
	}
}

func TestValidateSafeExecutionPlanRequiresSchemaVersionAndSteps(t *testing.T) {
	t.Parallel()

	if err := Validate(SafeExecutionPlan{SchemaVersion: "wrong"}); err == nil {
		t.Fatal("Validate(wrong schema) error = nil, want SafeExecutionPlanInvalid")
	} else if !IsSafeExecutionPlanInvalid(err) {
		t.Fatalf("IsSafeExecutionPlanInvalid(err) = false, want true: %v", err)
	}

	if err := Validate(SafeExecutionPlan{}); err == nil {
		t.Fatal("Validate(empty plan) error = nil, want error for no steps")
	}
}

func TestValidateSafeExecutionPlanRejectsDuplicateOrder(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	if len(plan.Steps) < 2 {
		t.Fatalf("NewSafeExecutionPlan() step count = %d, want at least 2 steps to catch duplicate-order regressions", len(plan.Steps))
	}
	plan.Steps[1].Order = plan.Steps[0].Order
	err := Validate(plan)
	if err == nil {
		t.Fatal("Validate(duplicate order) error = nil")
	}
	var typed *SafeExecutionPlanInvalidError
	if !errors.As(err, &typed) {
		t.Fatalf("errors.As(SafeExecutionPlanInvalidError) = false, err = %v", err)
	}
	if typed.FailureCode() != failurecodes.SafeExecutionPlanInvalid {
		t.Fatalf("FailureCode = %q, want %q", typed.FailureCode(), failurecodes.SafeExecutionPlanInvalid)
	}
	found := false
	for _, finding := range typed.Findings {
		if finding.Field == "order" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Findings = %+v, want order finding", typed.Findings)
	}
}

func TestValidateSafeExecutionPlanRejectsGapInOrder(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	plan.Steps[0].Order = 99
	err := Validate(plan)
	if err == nil {
		t.Fatal("Validate(gap order) error = nil")
	}
	if !strings.Contains(err.Error(), "order") {
		t.Fatalf("error = %v, want order finding", err)
	}
}

func TestValidateSafeExecutionPlanRejectsMissingRedactionPolicy(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	plan.Steps[0].RedactionPolicy = ""
	if err := Validate(plan); err == nil {
		t.Fatal("Validate(missing redaction policy) error = nil")
	}
}

func TestValidateSafeExecutionPlanRejectsRawSecretInArgs(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	plan.Steps[0].Args = append(plan.Steps[0].Args, "Authorization: Bearer abcdef0123456789")
	err := Validate(plan)
	if err == nil {
		t.Fatal("Validate(raw secret in args) error = nil")
	}
	if !IsSafeExecutionPlanInvalid(err) {
		t.Fatalf("IsSafeExecutionPlanInvalid = false: %v", err)
	}
}

func TestValidateSafeExecutionPlanRejectsRawSecretInEnv(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	if plan.Steps[0].Env == nil {
		plan.Steps[0].Env = map[string]string{}
	}
	plan.Steps[0].Env["TOKEN"] = "midn_admin_AAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := Validate(plan); err == nil {
		t.Fatal("Validate(raw secret in env) error = nil")
	}
}

func TestValidateSafeExecutionPlanRequiresConfirmField(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	// Confirm-required boolean is part of every step. Setting it explicitly
	// must not affect validation; the field must marshal/unmarshal cleanly.
	plan.Steps[0].RequiresConfirm = true
	if err := Validate(plan); err != nil {
		t.Fatalf("Validate(requires_confirm = true) error = %v", err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan) error = %v", err)
	}
	if !strings.Contains(string(data), `"requires_confirm":true`) {
		t.Fatalf("plan JSON missing requires_confirm: %s", data)
	}
}

func TestValidateSafeExecutionPlanRejectsTamperedArgsPath(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	plan.Steps[0].Args = []string{"-fc", "%T", "/etc/passwd"}
	if err := Validate(plan); err == nil {
		t.Fatal("Validate(tampered args) error = nil")
	}
}

func TestSafeExecutionPlanInvalidErrorString(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	plan.Steps[0].ID = ""
	err := Validate(plan)
	if err == nil {
		t.Fatal("Validate(empty id) error = nil")
	}
	if !strings.Contains(err.Error(), string(failurecodes.SafeExecutionPlanInvalid)) {
		t.Fatalf("error string = %q, want failure code", err.Error())
	}
}

func TestSafeExecutionPlanRoundTripJSON(t *testing.T) {
	t.Parallel()

	plan := NewSafeExecutionPlan()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal(plan) error = %v", err)
	}
	var decoded SafeExecutionPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal(plan) error = %v", err)
	}
	if err := Validate(decoded); err != nil {
		t.Fatalf("Validate(decoded plan) error = %v", err)
	}
	if decoded.SchemaVersion != SafeExecutionPlanSchemaV1 {
		t.Fatalf("SchemaVersion = %q, want %q", decoded.SchemaVersion, SafeExecutionPlanSchemaV1)
	}
}
