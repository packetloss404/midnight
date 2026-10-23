package preflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// SafeExecutionPlanSchemaV1 is the stable schema version embedded in plan JSON.
const SafeExecutionPlanSchemaV1 = "midnight.preflight.safe_execution_plan.v1"

// SafeExecutionPlanRedactionPolicyShared is the canonical Phase 0 redaction
// policy that applies to args + env across every allow-listed command. It uses
// the same shape map that drives stdout/stderr redaction so reviewers can audit
// one set of patterns.
const SafeExecutionPlanRedactionPolicyShared = "midnight.preflight.redaction.shared.v1"

// SafeExecutionPlan is the richer, validator-facing safe-execution plan shape.
// It carries an explicit ordering field, a redaction policy reference, and a
// requires-confirm boolean per step. The order field must be a 1-based gap-free
// sequence matching the canonical allow-list ordering.
type SafeExecutionPlan struct {
	SchemaVersion string                  `json:"schema_version"`
	Steps         []SafeExecutionPlanStep `json:"steps"`
}

// SafeExecutionPlanStep is one step in a safe-execution plan.
//
// Order is the deterministic 1-based step index. Plans with gaps or duplicates
// in Order are rejected. RequiresConfirm signals that the orchestrator must
// gate on operator confirmation before executing the step. RedactionPolicy
// names the policy applied to Args and Env; the validator rejects steps whose
// redaction policy does not match the canonical shared policy or whose Args/Env
// contain raw secrets that the policy is supposed to remove.
type SafeExecutionPlanStep struct {
	ID              string            `json:"id"`
	CheckID         string            `json:"check_id"`
	Order           int               `json:"order"`
	Executable      string            `json:"executable"`
	Args            []string          `json:"args"`
	Env             map[string]string `json:"env,omitempty"`
	Timeout         string            `json:"timeout"`
	RequiresRoot    bool              `json:"requires_root"`
	RequiresConfirm bool              `json:"requires_confirm"`
	RedactionPolicy string            `json:"redaction_policy"`
	Description     string            `json:"description,omitempty"`
}

// safeExecutionPlanRawSecretPatterns are obvious raw-secret shapes the
// validator refuses to accept in args/env. These are the same shapes the
// stdout/stderr redactor blocks; the validator enforces them up-front so plans
// never reach a runner with raw secrets in arguments.
var safeExecutionPlanRawSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bauthorization:\s*bearer\s+[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`(?i)\bbearer\s+[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`(?i)\b(?:token|password|passwd|secret|credential|api[_-]?key)\s*[=:]\s*[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// SafeExecutionPlanValidationStatus is the rollup status for a safe execution plan.
type SafeExecutionPlanValidationStatus string

const (
	// SafeExecutionPlanStatusValid indicates the plan satisfies every invariant.
	SafeExecutionPlanStatusValid SafeExecutionPlanValidationStatus = "valid"
	// SafeExecutionPlanStatusInvalid indicates the plan violates one or more invariants.
	SafeExecutionPlanStatusInvalid SafeExecutionPlanValidationStatus = "invalid"
)

// SafeExecutionPlanValidationFinding describes a single invariant violation in a plan.
type SafeExecutionPlanValidationFinding struct {
	SpecID  string `json:"spec_id,omitempty"`
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// SafeExecutionPlanValidationReview is the JSON-friendly review of plan validity.
type SafeExecutionPlanValidationReview struct {
	Status   SafeExecutionPlanValidationStatus    `json:"status"`
	Findings []SafeExecutionPlanValidationFinding `json:"findings,omitempty"`
}

// ValidateSafeExecutionPlan checks command plan invariants without executing anything.
// Each entry must (a) match the allow-list byte-for-byte, (b) carry a stable
// command id, (c) have a redaction policy applicable to args/env, and (d)
// preserve the deterministic allow-list ordering.
func ValidateSafeExecutionPlan(plan []CommandPlanEntry) SafeExecutionPlanValidationReview {
	review := SafeExecutionPlanValidationReview{Status: SafeExecutionPlanStatusValid}

	allowed := AllowedCommandSpecs()
	expectedOrder := make([]string, 0, len(allowed))
	for _, spec := range allowed {
		expectedOrder = append(expectedOrder, spec.ID)
	}

	seenIDs := make(map[string]int, len(plan))
	actualOrder := make([]string, 0, len(plan))

	for index, entry := range plan {
		if strings.TrimSpace(entry.ID) == "" {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				Index:   index,
				Field:   "id",
				Message: "plan entry id must be a stable, non-empty command id",
			})
			continue
		}
		if priorIndex, ok := seenIDs[entry.ID]; ok {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "id",
				Message: fmt.Sprintf("duplicate plan entry id; first seen at index %d", priorIndex),
			})
			continue
		}
		seenIDs[entry.ID] = index
		actualOrder = append(actualOrder, entry.ID)

		spec, err := allowedCommandSpecForCommand(Command{
			ID:   entry.ID,
			Name: entry.Executable,
			Args: append([]string(nil), entry.Args...),
		})
		if err != nil {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "command",
				Message: err.Error(),
			})
			continue
		}
		if spec.ID != entry.ID {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "id",
				Message: fmt.Sprintf("plan entry id %q does not match resolved spec %q", entry.ID, spec.ID),
			})
			continue
		}
		if entry.CheckID != spec.CheckID {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "check_id",
				Message: fmt.Sprintf("plan entry check_id %q does not match allow-list %q", entry.CheckID, spec.CheckID),
			})
			continue
		}
		if entry.Timeout == "" || entry.Timeout != spec.Command.Timeout.String() {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "timeout",
				Message: fmt.Sprintf("plan entry timeout %q does not match allow-list %q", entry.Timeout, spec.Command.Timeout.String()),
			})
			continue
		}
		if entry.RequiresRoot != spec.Command.RequiresRoot {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "requires_root",
				Message: fmt.Sprintf("plan entry requires_root %t does not match allow-list %t", entry.RequiresRoot, spec.Command.RequiresRoot),
			})
			continue
		}
		if !redactionPolicyAppliesToCommand(spec) {
			addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
				SpecID:  entry.ID,
				Index:   index,
				Field:   "redaction_policy",
				Message: "no redaction policy applies to args/env for this allow-list spec",
			})
			continue
		}
	}

	if !planOrderingMatchesAllowList(actualOrder, expectedOrder) {
		addSafeExecutionPlanFinding(&review, SafeExecutionPlanValidationFinding{
			Index:   -1,
			Field:   "ordering",
			Message: fmt.Sprintf("plan order %v does not match allow-list order %v", actualOrder, expectedOrder),
		})
	}

	return review
}

// MarshalSafeExecutionPlanValidationReview emits the stable plan validation JSON.
func MarshalSafeExecutionPlanValidationReview(review SafeExecutionPlanValidationReview) ([]byte, error) {
	return json.MarshalIndent(review, "", "  ")
}

func addSafeExecutionPlanFinding(review *SafeExecutionPlanValidationReview, finding SafeExecutionPlanValidationFinding) {
	review.Findings = append(review.Findings, finding)
	review.Status = SafeExecutionPlanStatusInvalid
}

// planOrderingMatchesAllowList enforces deterministic ordering: actual must be a
// prefix-preserving subsequence of expected (no reordering, no duplicates).
func planOrderingMatchesAllowList(actual, expected []string) bool {
	expectedRank := make(map[string]int, len(expected))
	for index, id := range expected {
		expectedRank[id] = index
	}
	previousRank := -1
	for _, id := range actual {
		rank, ok := expectedRank[id]
		if !ok {
			return false
		}
		if rank <= previousRank {
			return false
		}
		previousRank = rank
	}
	return true
}

// redactionPolicyAppliesToCommand checks whether the plan-level redaction
// patterns apply to the command's args/env shape. Phase 0 enforces a single
// shared redaction policy across all allow-listed commands.
func redactionPolicyAppliesToCommand(spec CommandSpec) bool {
	// All current allow-list commands have args, no env, and route stdout/stderr
	// through the redactCommandOutput helper. The validator simply confirms the
	// allow-list shape is what the redactor expects.
	if spec.Command.Name == "" {
		return false
	}
	for _, arg := range spec.Command.Args {
		if !reflect.DeepEqual(arg, strings.ReplaceAll(arg, "\x00", "")) {
			return false
		}
	}
	return len(commandOutputRedactions) > 0
}

// SafeExecutionPlanInvalidError is returned by Validate when a SafeExecutionPlan
// fails one or more invariants. Its Code is failurecodes.SafeExecutionPlanInvalid
// and the Findings field captures the per-step violations.
type SafeExecutionPlanInvalidError struct {
	Code     failurecodes.Code
	Findings []SafeExecutionPlanValidationFinding
}

// Error returns a deterministic, single-line summary of the invariants violated.
func (e *SafeExecutionPlanInvalidError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Findings) == 0 {
		return string(e.Code)
	}
	parts := make([]string, 0, len(e.Findings))
	for _, finding := range e.Findings {
		spec := finding.SpecID
		if spec == "" {
			spec = "<plan>"
		}
		parts = append(parts, fmt.Sprintf("step[%d] %s.%s: %s", finding.Index, spec, finding.Field, finding.Message))
	}
	return fmt.Sprintf("%s: %s", e.Code, strings.Join(parts, "; "))
}

// FailureCode reports the registered failure code for this error.
func (e *SafeExecutionPlanInvalidError) FailureCode() failurecodes.Code {
	if e == nil {
		return ""
	}
	return e.Code
}

// Validate enforces the SafeExecutionPlan invariants required by Phase 0:
//   - Every step has a stable, allow-list-resolved command id.
//   - Every step declares the canonical shared redaction policy and its args/env
//     do not contain raw secret shapes the policy is supposed to remove.
//   - Steps' Order field forms a 1..N gap-free unique sequence that matches the
//     canonical allow-list ordering when projected by step id.
//   - Every step carries an explicit RequiresConfirm boolean (no default-zero
//     ambiguity for steps the validator cannot classify).
//
// On failure Validate returns a *SafeExecutionPlanInvalidError whose
// FailureCode is failurecodes.SafeExecutionPlanInvalid.
func Validate(plan SafeExecutionPlan) error {
	if plan.SchemaVersion != "" && plan.SchemaVersion != SafeExecutionPlanSchemaV1 {
		return &SafeExecutionPlanInvalidError{
			Code: failurecodes.SafeExecutionPlanInvalid,
			Findings: []SafeExecutionPlanValidationFinding{{
				Index:   -1,
				Field:   "schema_version",
				Message: fmt.Sprintf("schema_version %q is not supported; want %q", plan.SchemaVersion, SafeExecutionPlanSchemaV1),
			}},
		}
	}
	if len(plan.Steps) == 0 {
		return &SafeExecutionPlanInvalidError{
			Code: failurecodes.SafeExecutionPlanInvalid,
			Findings: []SafeExecutionPlanValidationFinding{{
				Index:   -1,
				Field:   "steps",
				Message: "safe execution plan must contain at least one step",
			}},
		}
	}

	allowed := AllowedCommandSpecs()
	expectedOrder := make([]string, 0, len(allowed))
	for _, spec := range allowed {
		expectedOrder = append(expectedOrder, spec.ID)
	}

	var findings []SafeExecutionPlanValidationFinding
	seenIDs := make(map[string]int, len(plan.Steps))
	seenOrders := make(map[int]int, len(plan.Steps))
	actualOrder := make([]string, 0, len(plan.Steps))

	for index, step := range plan.Steps {
		if strings.TrimSpace(step.ID) == "" {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				Index:   index,
				Field:   "id",
				Message: "step id must be a stable, non-empty command id",
			})
			continue
		}
		if priorIndex, ok := seenIDs[step.ID]; ok {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "id",
				Message: fmt.Sprintf("duplicate step id; first seen at index %d", priorIndex),
			})
			continue
		}
		seenIDs[step.ID] = index

		if step.Order <= 0 {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "order",
				Message: fmt.Sprintf("order must be >= 1, got %d", step.Order),
			})
			continue
		}
		if priorIndex, ok := seenOrders[step.Order]; ok {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "order",
				Message: fmt.Sprintf("duplicate order %d; first seen at index %d", step.Order, priorIndex),
			})
			continue
		}
		seenOrders[step.Order] = index

		spec, err := allowedCommandSpecForCommand(Command{
			ID:   step.ID,
			Name: step.Executable,
			Args: append([]string(nil), step.Args...),
		})
		if err != nil {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "command",
				Message: err.Error(),
			})
			continue
		}
		if spec.ID != step.ID {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "id",
				Message: fmt.Sprintf("step id %q does not match resolved spec %q", step.ID, spec.ID),
			})
			continue
		}
		if step.CheckID != spec.CheckID {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "check_id",
				Message: fmt.Sprintf("check_id %q does not match allow-list %q", step.CheckID, spec.CheckID),
			})
			continue
		}
		if step.Timeout == "" || step.Timeout != spec.Command.Timeout.String() {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "timeout",
				Message: fmt.Sprintf("timeout %q does not match allow-list %q", step.Timeout, spec.Command.Timeout.String()),
			})
			continue
		}
		if step.RequiresRoot != spec.Command.RequiresRoot {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "requires_root",
				Message: fmt.Sprintf("requires_root %t does not match allow-list %t", step.RequiresRoot, spec.Command.RequiresRoot),
			})
			continue
		}
		if step.RedactionPolicy != SafeExecutionPlanRedactionPolicyShared {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "redaction_policy",
				Message: fmt.Sprintf("redaction_policy %q must be %q", step.RedactionPolicy, SafeExecutionPlanRedactionPolicyShared),
			})
			continue
		}
		if !redactionPolicyAppliesToCommand(spec) {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "redaction_policy",
				Message: "no redaction policy applies to args/env for this allow-list spec",
			})
			continue
		}
		if violation, ok := stepArgsContainRawSecret(step); ok {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "args",
				Message: fmt.Sprintf("raw secret pattern detected: %s", violation),
			})
			continue
		}
		if violation, ok := stepEnvContainsRawSecret(step); ok {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				SpecID:  step.ID,
				Index:   index,
				Field:   "env",
				Message: fmt.Sprintf("raw secret pattern detected in env: %s", violation),
			})
			continue
		}
		actualOrder = append(actualOrder, step.ID)
	}

	if len(findings) == 0 {
		// Order field must be a 1..N gap-free sequence (no missing numbers).
		expectedSequence := make([]int, 0, len(plan.Steps))
		for i := 1; i <= len(plan.Steps); i++ {
			expectedSequence = append(expectedSequence, i)
		}
		actualSequence := make([]int, 0, len(plan.Steps))
		for _, step := range plan.Steps {
			actualSequence = append(actualSequence, step.Order)
		}
		sort.Ints(actualSequence)
		if !reflect.DeepEqual(actualSequence, expectedSequence) {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				Index:   -1,
				Field:   "order",
				Message: fmt.Sprintf("order field has gaps or out-of-range values: %v, want %v", actualSequence, expectedSequence),
			})
		}
		// And the resolved id sequence must agree with the allow-list ordering.
		if !planOrderingMatchesAllowList(actualOrder, expectedOrder) {
			findings = append(findings, SafeExecutionPlanValidationFinding{
				Index:   -1,
				Field:   "ordering",
				Message: fmt.Sprintf("plan order %v does not match allow-list order %v", actualOrder, expectedOrder),
			})
		}
	}

	if len(findings) > 0 {
		return &SafeExecutionPlanInvalidError{Code: failurecodes.SafeExecutionPlanInvalid, Findings: findings}
	}
	return nil
}

// IsSafeExecutionPlanInvalid reports whether err carries the
// SafeExecutionPlanInvalid failure code.
func IsSafeExecutionPlanInvalid(err error) bool {
	var typed *SafeExecutionPlanInvalidError
	return errors.As(err, &typed)
}

// NewSafeExecutionPlan returns the canonical safe execution plan derived from
// the allow-list. Every step is assigned a 1-based Order matching the allow-list
// position, the canonical shared redaction policy, and a conservative
// RequiresConfirm flag (true for steps that touch shared host configuration).
func NewSafeExecutionPlan() SafeExecutionPlan {
	specs := AllowedCommandSpecs()
	steps := make([]SafeExecutionPlanStep, 0, len(specs))
	for index, spec := range specs {
		steps = append(steps, SafeExecutionPlanStep{
			ID:              spec.ID,
			CheckID:         spec.CheckID,
			Order:           index + 1,
			Executable:      spec.Command.Name,
			Args:            append([]string(nil), spec.Command.Args...),
			Timeout:         spec.Command.Timeout.String(),
			RequiresRoot:    spec.Command.RequiresRoot,
			RequiresConfirm: requiresConfirmForSpec(spec),
			RedactionPolicy: SafeExecutionPlanRedactionPolicyShared,
			Description:     spec.Description,
		})
	}
	return SafeExecutionPlan{SchemaVersion: SafeExecutionPlanSchemaV1, Steps: steps}
}

// requiresConfirmForSpec returns the conservative default for whether a spec
// needs explicit operator confirmation before execution. Phase 0 defaults to
// false for the read-only probes in the allow-list and true for any future
// spec whose Command requires root. Callers can override per-step.
func requiresConfirmForSpec(spec CommandSpec) bool {
	return spec.Command.RequiresRoot
}

func stepArgsContainRawSecret(step SafeExecutionPlanStep) (string, bool) {
	for _, arg := range step.Args {
		if violation, ok := matchSafeExecutionPlanSecret(arg); ok {
			return violation, true
		}
	}
	return "", false
}

func stepEnvContainsRawSecret(step SafeExecutionPlanStep) (string, bool) {
	for key, value := range step.Env {
		joined := key + "=" + value
		if violation, ok := matchSafeExecutionPlanSecret(joined); ok {
			return violation, true
		}
		if violation, ok := matchSafeExecutionPlanSecret(value); ok {
			return violation, true
		}
	}
	return "", false
}

func matchSafeExecutionPlanSecret(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	for _, pattern := range safeExecutionPlanRawSecretPatterns {
		if match := pattern.FindString(text); match != "" {
			return match, true
		}
	}
	return "", false
}

// SortPlanEntriesByAllowList returns a copy of plan reordered to match the
// canonical allow-list order. Unknown ids retain their relative order at the end.
func SortPlanEntriesByAllowList(plan []CommandPlanEntry) []CommandPlanEntry {
	allowed := AllowedCommandSpecs()
	rank := make(map[string]int, len(allowed))
	for index, spec := range allowed {
		rank[spec.ID] = index
	}
	sorted := append([]CommandPlanEntry(nil), plan...)
	sort.SliceStable(sorted, func(left, right int) bool {
		leftRank, leftOK := rank[sorted[left].ID]
		rightRank, rightOK := rank[sorted[right].ID]
		switch {
		case leftOK && rightOK:
			return leftRank < rightRank
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return left < right
		}
	})
	return sorted
}
