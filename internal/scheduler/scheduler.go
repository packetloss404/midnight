package scheduler

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// JobClass identifies the worker queue class for a scheduling request.
// Phase 0 names the classes panel-locked in midnight-platform-plan.md.
type JobClass string

const (
	JobClassBuild     JobClass = "build"
	JobClassDeploy    JobClass = "deploy"
	JobClassCron      JobClass = "cron"
	JobClassReconcile JobClass = "reconcile"
	JobClassCleanup   JobClass = "cleanup"
)

// BackoffClass categorizes failures for retry policy selection. The
// scheduler treats transient failures as retriable with capped exponential
// backoff and permanent failures as terminal.
type BackoffClass string

const (
	BackoffClassNone      BackoffClass = ""
	BackoffClassTransient BackoffClass = "transient"
	BackoffClassPermanent BackoffClass = "permanent"
)

// DispatchOutcome enumerates the per-request scheduler decision.
type DispatchOutcome string

const (
	OutcomeAccepted    DispatchOutcome = "accepted"
	OutcomeQueued      DispatchOutcome = "queued"
	OutcomeQuotaDenied DispatchOutcome = "quota_denied"
	OutcomeBackoff     DispatchOutcome = "backoff"
	OutcomeRejected    DispatchOutcome = "rejected"
)

// Quota describes per-(project, class) concurrency caps and per-second
// admission rate. Phase 1 wires only the InFlight cap; rate is reserved
// for live execution.
type Quota struct {
	InFlight int
	RatePer  time.Duration
}

// Policy is the deterministic scheduler policy used by Plan.
type Policy struct {
	DefaultQuota   Quota
	ProjectQuota   map[string]Quota
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	PermanentDelay time.Duration
}

// Request is one input to the scheduler. The scheduler treats Request as
// immutable; it never mutates fields and returns deterministic decisions.
type Request struct {
	ID          string
	ProjectID   string
	Class       JobClass
	Priority    int
	SubmittedAt time.Time
	Attempt     int
	LastFailure BackoffClass
}

// Decision is the scheduler's per-request outcome. The Decision is JSON-
// friendly so it can be folded into evidence packets without rewrapping.
type Decision struct {
	RequestID  string          `json:"request_id"`
	ProjectID  string          `json:"project_id"`
	Class      JobClass        `json:"class"`
	Outcome    DispatchOutcome `json:"outcome"`
	Reason     string          `json:"reason,omitempty"`
	Backoff    time.Duration   `json:"backoff,omitempty"`
	DispatchAt time.Time       `json:"dispatch_at,omitempty"`
}

// DispatchPlan is the deterministic batch result of Plan. Counts are
// rolled up by outcome for diagnostic and evidence reporting.
type DispatchPlan struct {
	Decisions []Decision      `json:"decisions"`
	Counts    []DispatchCount `json:"counts"`
	NextDelay time.Duration   `json:"next_delay,omitempty"`
	Notes     []DispatchNote  `json:"notes,omitempty"`
}

// DispatchCount summarizes outcome counts for one decision batch.
type DispatchCount struct {
	Outcome DispatchOutcome `json:"outcome"`
	Count   int             `json:"count"`
}

// DispatchNote is one deterministic operator-visible note attached to a plan.
type DispatchNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrEmptyRequestID is returned when a request lacks an ID.
var ErrEmptyRequestID = errors.New("scheduler: request id is required")

// Plan computes a deterministic dispatch plan for the given requests at
// time now. The plan is side-effect free; callers feed Decisions to a
// real worker dispatcher.
func Plan(now time.Time, policy Policy, requests []Request) (DispatchPlan, error) {
	policy = normalizePolicy(policy)
	ordered := make([]Request, 0, len(requests))
	for _, req := range requests {
		if strings.TrimSpace(req.ID) == "" {
			return DispatchPlan{}, ErrEmptyRequestID
		}
		ordered = append(ordered, req)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		if !ordered[i].SubmittedAt.Equal(ordered[j].SubmittedAt) {
			return ordered[i].SubmittedAt.Before(ordered[j].SubmittedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})

	usage := make(map[string]int, len(ordered))
	plan := DispatchPlan{Decisions: make([]Decision, 0, len(ordered))}
	for _, req := range ordered {
		decision := Decision{
			RequestID: req.ID,
			ProjectID: req.ProjectID,
			Class:     req.Class,
		}
		switch req.LastFailure {
		case BackoffClassPermanent:
			decision.Outcome = OutcomeRejected
			decision.Reason = "permanent failure class"
			decision.Backoff = policy.PermanentDelay
		case BackoffClassTransient:
			delay := transientBackoff(req.Attempt, policy)
			decision.Outcome = OutcomeBackoff
			decision.Reason = "transient failure backoff"
			decision.Backoff = delay
			decision.DispatchAt = now.Add(delay)
		default:
			quota := policy.quotaFor(req.ProjectID)
			key := req.ProjectID + "|" + string(req.Class)
			used := usage[key]
			if quota.InFlight <= 0 || used >= quota.InFlight {
				decision.Outcome = OutcomeQuotaDenied
				decision.Reason = "per-project per-class quota exceeded"
			} else {
				usage[key] = used + 1
				decision.Outcome = OutcomeAccepted
				decision.DispatchAt = now
			}
		}
		plan.Decisions = append(plan.Decisions, decision)
	}

	plan.Counts = countOutcomes(plan.Decisions)
	plan.NextDelay = nextDelay(plan.Decisions)
	plan.Notes = collectNotes(plan)
	return plan, nil
}

// transientBackoff returns the base*2^attempt delay capped at policy.Max.
// Exposed for tests and callers that need to align with scheduler semantics.
func TransientBackoff(attempt int, policy Policy) time.Duration {
	return transientBackoff(attempt, normalizePolicy(policy))
}

func transientBackoff(attempt int, policy Policy) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := policy.BackoffBase
	for i := 0; i < attempt; i++ {
		if delay >= policy.BackoffMax/2 {
			return policy.BackoffMax
		}
		delay *= 2
	}
	if delay > policy.BackoffMax {
		return policy.BackoffMax
	}
	return delay
}

func normalizePolicy(policy Policy) Policy {
	if policy.BackoffBase <= 0 {
		policy.BackoffBase = 500 * time.Millisecond
	}
	if policy.BackoffMax <= 0 || policy.BackoffMax < policy.BackoffBase {
		policy.BackoffMax = 30 * time.Second
	}
	if policy.PermanentDelay < 0 {
		policy.PermanentDelay = 0
	}
	if policy.DefaultQuota.InFlight <= 0 {
		policy.DefaultQuota.InFlight = 4
	}
	return policy
}

func (p Policy) quotaFor(projectID string) Quota {
	if quota, ok := p.ProjectQuota[projectID]; ok {
		if quota.InFlight <= 0 {
			quota.InFlight = p.DefaultQuota.InFlight
		}
		return quota
	}
	return p.DefaultQuota
}

func countOutcomes(decisions []Decision) []DispatchCount {
	counts := make(map[DispatchOutcome]int, len(decisions))
	for _, decision := range decisions {
		counts[decision.Outcome]++
	}
	keys := make([]DispatchOutcome, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]DispatchCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, DispatchCount{Outcome: k, Count: counts[k]})
	}
	return out
}

func nextDelay(decisions []Decision) time.Duration {
	var next time.Duration
	for _, decision := range decisions {
		if decision.Backoff > next {
			next = decision.Backoff
		}
	}
	return next
}

func collectNotes(plan DispatchPlan) []DispatchNote {
	notes := make([]DispatchNote, 0)
	for _, count := range plan.Counts {
		switch count.Outcome {
		case OutcomeQuotaDenied:
			notes = append(notes, DispatchNote{Code: "scheduler.quota_denied", Message: "one or more requests exceeded their per-project quota"})
		case OutcomeBackoff:
			notes = append(notes, DispatchNote{Code: "scheduler.transient_backoff", Message: "one or more requests are in transient backoff"})
		case OutcomeRejected:
			notes = append(notes, DispatchNote{Code: "scheduler.permanent_failure", Message: "one or more requests are in permanent failure"})
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Code < notes[j].Code })
	return notes
}
