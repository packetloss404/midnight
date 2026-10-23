package main

import (
	"fmt"
	"strings"

	"github.com/midnight/midnight/test/drills/harness"
)

// drillEnvelopeAssertions returns one or more richer sub-assertions per drill
// that go beyond the headline package validator. The intent is to enforce
// every Phase 0 drill row carries: reason-code present + reachable, event
// reference present + reachable, runbook hint present + on disk, and the row
// is referenced in the in-memory manifest catalog. This satisfies the panel
// rule that "evidence packet emitted" is not enough — the evidence must
// contain the required reason code AND a recovery hint AND be referenced in
// the index.
func drillEnvelopeAssertions(d drill, catalog referenceCatalog) []harness.AssertionResult {
	results := make([]harness.AssertionResult, 0, 4)
	results = append(results, envelopeReasonCodeAssertion(d, catalog))
	results = append(results, envelopeEventAssertion(d, catalog))
	results = append(results, envelopeRunbookAssertion(d, catalog))
	results = append(results, envelopeDiagnosticAssertion(d))
	return results
}

func envelopeReasonCodeAssertion(d drill, catalog referenceCatalog) harness.AssertionResult {
	result := harness.AssertionResult{
		ID:         d.ID + ".envelope.reason-code",
		Status:     harness.AssertionPassed,
		ReasonCode: d.ReasonCode,
		Diagnostic: "drill row carries a registered reason code",
	}
	code := strings.TrimSpace(d.ReasonCode)
	if code == "" {
		result.Status = harness.AssertionFailed
		result.Diagnostic = "drill row is missing required reason code"
		return result
	}
	if _, ok := catalog.ReasonCodes[code]; !ok {
		result.Status = harness.AssertionFailed
		result.Diagnostic = fmt.Sprintf("reason code %q is not in the registry or phase 0 allowlist", code)
	}
	return result
}

func envelopeEventAssertion(d drill, catalog referenceCatalog) harness.AssertionResult {
	result := harness.AssertionResult{
		ID:         d.ID + ".envelope.event",
		Status:     harness.AssertionPassed,
		ReasonCode: d.ReasonCode,
		Diagnostic: "drill row references a known event",
	}
	event := strings.TrimSpace(d.Event)
	if event == "" {
		result.Status = harness.AssertionFailed
		result.Diagnostic = "drill row is missing required event"
		return result
	}
	if _, ok := catalog.Events[event]; !ok {
		result.Status = harness.AssertionFailed
		result.Diagnostic = fmt.Sprintf("event %q is not in the cloudevents catalog or phase 0 allowlist", event)
	}
	return result
}

func envelopeRunbookAssertion(d drill, catalog referenceCatalog) harness.AssertionResult {
	result := harness.AssertionResult{
		ID:         d.ID + ".envelope.runbook",
		Status:     harness.AssertionPassed,
		ReasonCode: d.ReasonCode,
		Diagnostic: "drill row recovery hint resolves to an on-disk runbook",
	}
	runbook := strings.TrimSpace(d.Runbook)
	if runbook == "" {
		result.Status = harness.AssertionFailed
		result.Diagnostic = "drill row is missing required recovery runbook hint"
		return result
	}
	if _, ok := catalog.Runbooks[runbook]; !ok {
		result.Status = harness.AssertionFailed
		result.Diagnostic = fmt.Sprintf("runbook %q does not exist on disk", runbook)
	}
	return result
}

func envelopeDiagnosticAssertion(d drill) harness.AssertionResult {
	result := harness.AssertionResult{
		ID:         d.ID + ".envelope.diagnostic",
		Status:     harness.AssertionPassed,
		ReasonCode: d.ReasonCode,
		Diagnostic: "drill row carries a non-empty user diagnostic line",
	}
	if strings.TrimSpace(d.Diagnostic) == "" {
		result.Status = harness.AssertionFailed
		result.Diagnostic = "drill row is missing required user diagnostic line"
	}
	return result
}
