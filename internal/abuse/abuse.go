package abuse

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/tenancy"
)

// Action is the abuse-control recommendation for a subject.
type Action string

const (
	ActionAllow         Action = "allow"
	ActionThrottle      Action = "throttle"
	ActionRequireReview Action = "require_review"
)

// Policy defines soft thresholds. Zero thresholds disable the matching check.
type Policy struct {
	MaxRequestsPerMinute int
	MaxFailuresPerMinute int
	MaxLimitViolations   int
	ThrottleFor          time.Duration
}

// Activity captures one subject's recent behavior in a caller-defined window.
type Activity struct {
	SubjectID       string        `json:"subject_id"`
	Tenant          tenancy.Scope `json:"tenant"`
	Requests        int           `json:"requests"`
	Failures        int           `json:"failures"`
	LimitViolations int           `json:"limit_violations"`
}

// Decision is deterministic and evidence-friendly.
type Decision struct {
	SubjectID  string        `json:"subject_id"`
	Tenant     tenancy.Scope `json:"tenant"`
	Action     Action        `json:"action"`
	Reasons    []string      `json:"reasons,omitempty"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
}

var (
	ErrEmptySubjectID = errors.New("abuse: subject id is required")
	ErrInvalidTenant  = errors.New("abuse: tenant scope is required")
	ErrNegativeCount  = errors.New("abuse: activity counts must not be negative")
)

// DefaultPolicy is intentionally conservative and soft: it only recommends
// throttling or review, leaving enforcement to callers.
func DefaultPolicy() Policy {
	return Policy{
		MaxRequestsPerMinute: 600,
		MaxFailuresPerMinute: 60,
		MaxLimitViolations:   3,
		ThrottleFor:          time.Minute,
	}
}

// Decide evaluates one subject activity record.
func Decide(policy Policy, activity Activity) (Decision, error) {
	if err := activity.validate(); err != nil {
		return Decision{}, err
	}
	policy = normalizePolicy(policy)
	decision := Decision{SubjectID: activity.SubjectID, Tenant: activity.Tenant, Action: ActionAllow}

	if policy.MaxRequestsPerMinute > 0 && activity.Requests > policy.MaxRequestsPerMinute {
		decision.Reasons = append(decision.Reasons, "requests_per_minute_exceeded")
	}
	if policy.MaxFailuresPerMinute > 0 && activity.Failures > policy.MaxFailuresPerMinute {
		decision.Reasons = append(decision.Reasons, "failures_per_minute_exceeded")
	}
	if len(decision.Reasons) > 0 {
		decision.Action = ActionThrottle
		decision.RetryAfter = policy.ThrottleFor
	}
	if policy.MaxLimitViolations > 0 && activity.LimitViolations > policy.MaxLimitViolations {
		decision.Action = ActionRequireReview
		decision.RetryAfter = 0
		decision.Reasons = append(decision.Reasons, "limit_violations_exceeded")
	}
	sort.Strings(decision.Reasons)
	return decision, nil
}

// DecideBatch evaluates activities and returns decisions sorted by subject id.
func DecideBatch(policy Policy, activities []Activity) ([]Decision, error) {
	decisions := make([]Decision, 0, len(activities))
	for i, activity := range activities {
		decision, err := Decide(policy, activity)
		if err != nil {
			return nil, fmt.Errorf("activity[%d]: %w", i, err)
		}
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].Tenant.Key() != decisions[j].Tenant.Key() {
			return decisions[i].Tenant.Key() < decisions[j].Tenant.Key()
		}
		return decisions[i].SubjectID < decisions[j].SubjectID
	})
	return decisions, nil
}

func normalizePolicy(policy Policy) Policy {
	if policy.ThrottleFor <= 0 {
		policy.ThrottleFor = time.Minute
	}
	return policy
}

func (a Activity) validate() error {
	if strings.TrimSpace(a.SubjectID) == "" {
		return ErrEmptySubjectID
	}
	if err := a.Tenant.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTenant, err)
	}
	if a.Requests < 0 || a.Failures < 0 || a.LimitViolations < 0 {
		return ErrNegativeCount
	}
	return nil
}
