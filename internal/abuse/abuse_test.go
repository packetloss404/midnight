package abuse

import (
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/tenancy"
)

var testTenant = tenancy.Scope{WorkspaceID: "wrk", ProjectID: "prj", EnvironmentID: "prod"}

func TestDecideAllowsNormalActivity(t *testing.T) {
	decision, err := Decide(Policy{MaxRequestsPerMinute: 10, MaxFailuresPerMinute: 2, MaxLimitViolations: 1}, Activity{SubjectID: "token-a", Tenant: testTenant, Requests: 10, Failures: 2, LimitViolations: 1})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Action != ActionAllow || len(decision.Reasons) != 0 {
		t.Fatalf("expected allow, got %#v", decision)
	}
}

func TestDecideThrottlesRequestAndFailureSpikes(t *testing.T) {
	decision, err := Decide(Policy{MaxRequestsPerMinute: 10, MaxFailuresPerMinute: 2, ThrottleFor: 30 * time.Second}, Activity{SubjectID: "token-a", Tenant: testTenant, Requests: 11, Failures: 3})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Action != ActionThrottle || decision.RetryAfter != 30*time.Second {
		t.Fatalf("expected throttle, got %#v", decision)
	}
	if len(decision.Reasons) != 2 || decision.Reasons[0] != "failures_per_minute_exceeded" || decision.Reasons[1] != "requests_per_minute_exceeded" {
		t.Fatalf("unexpected reasons: %#v", decision.Reasons)
	}
}

func TestDecideRequiresReviewForRepeatedLimitViolations(t *testing.T) {
	decision, err := Decide(Policy{MaxRequestsPerMinute: 10, MaxLimitViolations: 1, ThrottleFor: time.Minute}, Activity{SubjectID: "token-a", Tenant: testTenant, Requests: 11, LimitViolations: 2})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Action != ActionRequireReview || decision.RetryAfter != 0 {
		t.Fatalf("expected review, got %#v", decision)
	}
}

func TestDecideBatchSortsBySubject(t *testing.T) {
	decisions, err := DecideBatch(Policy{}, []Activity{{SubjectID: "z", Tenant: testTenant}, {SubjectID: "a", Tenant: testTenant}})
	if err != nil {
		t.Fatalf("DecideBatch: %v", err)
	}
	if decisions[0].SubjectID != "a" || decisions[1].SubjectID != "z" {
		t.Fatalf("decisions not sorted: %#v", decisions)
	}
}

func TestDecideRejectsInvalidActivity(t *testing.T) {
	_, err := Decide(Policy{}, Activity{SubjectID: " ", Tenant: testTenant, Requests: -1})
	if !errors.Is(err, ErrEmptySubjectID) {
		t.Fatalf("expected empty subject error, got %v", err)
	}
}

func TestDecideRejectsMissingTenantScope(t *testing.T) {
	_, err := Decide(Policy{}, Activity{SubjectID: "token-a"})
	if !errors.Is(err, ErrInvalidTenant) {
		t.Fatalf("expected invalid tenant, got %v", err)
	}
}
