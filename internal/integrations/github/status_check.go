package github

import (
	"context"
	"errors"
	"sort"
)

// CheckRunStatus mirrors the GitHub combined-status conclusion strings.
type CheckRunStatus string

const (
	CheckSuccess CheckRunStatus = "success"
	CheckFailure CheckRunStatus = "failure"
	CheckPending CheckRunStatus = "pending"
	CheckMissing CheckRunStatus = "missing"
)

// CheckObservation is a single (name, status) tuple Lane 4 receives
// from either a stub StatusChecker (in tests) or the live GitHub
// combined-status API (under build tag).
type CheckObservation struct {
	Name   string
	Status CheckRunStatus
}

// StatusChecker is the boundary between the promotion gate and GitHub.
// Implementations MUST treat the result as advisory only; the
// PromotionGate enforces the policy.
type StatusChecker interface {
	CombinedStatus(ctx context.Context, repoFullName, sha string) ([]CheckObservation, error)
}

// StubStatusChecker is the default StatusChecker used outside the
// midnightlivegithub build tag. It returns ErrLiveGitHubDisabled so
// callers without live credentials see an explicit signal.
type StubStatusChecker struct{}

// CombinedStatus implements StatusChecker.
func (StubStatusChecker) CombinedStatus(_ context.Context, _, _ string) ([]CheckObservation, error) {
	return nil, ErrLiveGitHubDisabled
}

// FixtureStatusChecker is a StatusChecker used by tests. It returns the
// supplied observations regardless of the SHA.
type FixtureStatusChecker struct {
	Observations []CheckObservation
}

// CombinedStatus implements StatusChecker.
func (f FixtureStatusChecker) CombinedStatus(_ context.Context, _, _ string) ([]CheckObservation, error) {
	out := make([]CheckObservation, len(f.Observations))
	copy(out, f.Observations)
	return out, nil
}

// ErrLiveGitHubDisabled is returned by StubStatusChecker.
var ErrLiveGitHubDisabled = errors.New("github: live GitHub API not wired (compile with -tags midnightlivegithub)")

// RequiredCheckPolicy describes the panel-locked required-check gate.
//
// Panel decision pinning (Sprint 10 DEP-1011):
//
//	"On deploy promotion (DEPLOYING -> ACTIVE), gate refuses promotion
//	 until all required checks report success in external_checks
//	 table; refusal surfaces DEPLOY_GATE_PENDING_CHECKS."
type RequiredCheckPolicy struct {
	// Required is the set of check-run names that MUST report success
	// before promotion is allowed.
	Required []string
}

// Decision is the structured outcome of Evaluate.
type Decision struct {
	Allowed       bool
	ReasonCode    string
	MissingChecks []string
	FailedChecks  []string
	PendingChecks []string
}

// Evaluate returns the panel-locked decision given the observed checks.
// The function is pure — call StatusChecker first to populate
// observations.
func (p RequiredCheckPolicy) Evaluate(observations []CheckObservation) Decision {
	if len(p.Required) == 0 {
		return Decision{Allowed: true}
	}
	byName := map[string]CheckRunStatus{}
	for _, o := range observations {
		byName[o.Name] = o.Status
	}
	d := Decision{Allowed: true}
	required := append([]string(nil), p.Required...)
	sort.Strings(required)
	for _, name := range required {
		status, ok := byName[name]
		if !ok {
			d.Allowed = false
			d.MissingChecks = append(d.MissingChecks, name)
			continue
		}
		switch status {
		case CheckSuccess:
			// pass
		case CheckPending, CheckMissing:
			d.Allowed = false
			d.PendingChecks = append(d.PendingChecks, name)
		default:
			d.Allowed = false
			d.FailedChecks = append(d.FailedChecks, name)
		}
	}
	if !d.Allowed {
		d.ReasonCode = "DEPLOY_GATE_PENDING_CHECKS"
	}
	return d
}

// Gate composes a StatusChecker + RequiredCheckPolicy. Promotion code
// calls Gate.Evaluate(ctx, repo, sha) and proceeds only when
// Decision.Allowed is true.
type Gate struct {
	Checker StatusChecker
	Policy  RequiredCheckPolicy
}

// Evaluate is the convenience wrapper that does the StatusChecker call
// and runs the policy.
func (g Gate) Evaluate(ctx context.Context, repo, sha string) (Decision, error) {
	if g.Checker == nil {
		g.Checker = StubStatusChecker{}
	}
	obs, err := g.Checker.CombinedStatus(ctx, repo, sha)
	if err != nil {
		return Decision{}, err
	}
	return g.Policy.Evaluate(obs), nil
}
