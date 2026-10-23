package registry

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// GCStateMachineSchemaVersion identifies the JSON schema produced by the
// retention/GC state machine planner.
const GCStateMachineSchemaVersion = "midnight.dev/registry-gc-state-machine/v1alpha1"

// PanelLockedKeepLast is the panel-locked default that keeps the last 10
// pushed tags per service.
const PanelLockedKeepLast = 10

// GCState identifies a single transition in the panel-locked
// quiesce -> lock -> dry-run -> apply -> post-GC verify state machine.
type GCState string

const (
	// GCStateQuiesce is the initial transition. The state machine pauses new
	// writes against the registry; in offline mode it merely records intent.
	GCStateQuiesce GCState = "quiesce"
	// GCStateLock acquires the global advisory lock that prevents concurrent
	// destructive GC runs.
	GCStateLock GCState = "lock"
	// GCStateDryRun produces the candidate list without mutating the
	// registry. It always runs even when the operator requested apply.
	GCStateDryRun GCState = "dry-run"
	// GCStateApply executes the deletions implied by the dry-run plan. In
	// offline planner mode the apply transition records intent only.
	GCStateApply GCState = "apply"
	// GCStatePostGCVerify samples retained digests to confirm none were
	// deleted in error and that pinned digests remain pull-able.
	GCStatePostGCVerify GCState = "post-gc-verify"
	// GCStateRelease releases the advisory lock and permits writes again.
	GCStateRelease GCState = "release"
)

// GCEvent is a single structured event emitted by the state machine. Events
// carry the from/to states, an optional reason, the time the event was
// recorded, and a deterministic short message for logging.
type GCEvent struct {
	Sequence    int       `json:"sequence"`
	From        GCState   `json:"from,omitempty"`
	To          GCState   `json:"to"`
	OccurredAt  time.Time `json:"occurredAt"`
	Reason      string    `json:"reason,omitempty"`
	Description string    `json:"description"`
}

// GCInput is the planner input. It is a strict superset of GCPlanRequest so the
// planner can re-use the same retention rules and pin probe contracts as the
// existing dry-run helper.
type GCInput struct {
	GeneratedAt   time.Time
	Repository    string
	Tags          []ImageTag
	Pins          []ImagePin
	ProtectedTags []string
	KeepLast      int
	// RollbackWindowDigests is the optional explicit list of digests inside
	// an active rollback window. Each entry is added as a protected pin
	// reason in the output plan.
	RollbackWindowDigests []string
	// ProtectedSnapshots is the optional explicit list of digests still
	// referenced by protected deployment snapshots. They are treated as
	// protected pins.
	ProtectedSnapshots []string
	// ConfirmApply mirrors the operator-supplied --confirm-apply flag. The
	// planner records it but never deletes data; it is needed so the
	// produced plan can be safely consumed by the offline Apply helper.
	ConfirmApply bool
}

// GCPlan is the immutable state-machine plan a planner returns. Apply uses
// the recorded actions and events.
type GCPlan struct {
	SchemaVersion string            `json:"schemaVersion"`
	GeneratedAt   time.Time         `json:"generatedAt"`
	Repository    string            `json:"repository"`
	KeepLast      int               `json:"keepLast"`
	Summary       GCSummary         `json:"summary"`
	Actions       []RetentionAction `json:"actions"`
	Events        []GCEvent         `json:"events"`
	ConfirmApply  bool              `json:"confirmApply"`
}

// GCReport is the offline Apply result. It records that the planner-recorded
// intent was acknowledged and never includes any registry-mutation side
// effects.
type GCReport struct {
	SchemaVersion string    `json:"schemaVersion"`
	AppliedAt     time.Time `json:"appliedAt"`
	Repository    string    `json:"repository"`
	DryRun        bool      `json:"dryRun"`
	ConfirmApply  bool      `json:"confirmApply"`
	Summary       GCSummary `json:"summary"`
	Events        []GCEvent `json:"events"`
}

// Plan returns the panel-locked retention/GC state-machine plan. The planner
// is pure; it does not contact the registry, the database, or the filesystem.
//
// The retention rule is "last N tags per service + all digests in active
// rollback windows + protected snapshots". N defaults to PanelLockedKeepLast.
func Plan(input GCInput) (GCPlan, error) {
	if input.Repository == "" {
		return GCPlan{}, errors.New("registry GC input repository must not be empty")
	}
	for _, tag := range input.Tags {
		if tag.Repository != "" && tag.Repository != input.Repository {
			return GCPlan{}, fmt.Errorf("registry GC input tag %q repository %q does not match input repository %q", tag.Name, tag.Repository, input.Repository)
		}
	}
	keepLast := input.KeepLast
	if keepLast < 0 {
		return GCPlan{}, errors.New("registry GC input keepLast must not be negative")
	}
	if keepLast == 0 {
		keepLast = PanelLockedKeepLast
	}

	now := input.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rule := RetentionRule{
		Repository:  input.Repository,
		KeepLast:    keepLast,
		KeepDigests: append(append([]string(nil), input.RollbackWindowDigests...), input.ProtectedSnapshots...),
		KeepTags:    append([]string(nil), input.ProtectedTags...),
	}

	tags := make([]ImageTag, len(input.Tags))
	for i, tag := range input.Tags {
		copy := tag
		copy.Repository = input.Repository
		tags[i] = copy
	}

	pins := make([]ImagePin, len(input.Pins))
	for i, pin := range input.Pins {
		copy := pin
		if copy.Repository == "" {
			copy.Repository = input.Repository
		}
		pins[i] = copy
	}

	dryRun := PlanGCDryRun(GCPlanRequest{
		GeneratedAt: now,
		Tags:        tags,
		Pins:        pins,
		Rules:       []RetentionRule{rule},
	})

	actions := sortedRetentionActions(dryRun.Actions)
	plan := GCPlan{
		SchemaVersion: GCStateMachineSchemaVersion,
		GeneratedAt:   now,
		Repository:    input.Repository,
		KeepLast:      keepLast,
		Summary:       summarizeGCActions(actions),
		Actions:       actions,
		ConfirmApply:  input.ConfirmApply,
	}
	plan.Events = buildPlanEvents(plan, now)
	return plan, nil
}

// Apply records the operator's acknowledgement of a planner-issued GCPlan.
// Apply is offline: it never deletes blobs or contacts a registry. When
// ConfirmApply is false but the plan contains deletions, Apply returns an
// error so callers must opt in explicitly.
func Apply(plan GCPlan) (GCReport, error) {
	if plan.SchemaVersion != GCStateMachineSchemaVersion {
		return GCReport{}, fmt.Errorf("registry GC plan schema %q is not %q", plan.SchemaVersion, GCStateMachineSchemaVersion)
	}
	if plan.GeneratedAt.IsZero() {
		return GCReport{}, errors.New("registry GC plan generatedAt is required")
	}
	if plan.Repository == "" {
		return GCReport{}, errors.New("registry GC plan repository is required")
	}
	if plan.Summary.Deletions > 0 && !plan.ConfirmApply {
		return GCReport{}, failurecodes.New(failurecodes.RegistryGCLocked,
			"registry GC apply refused: confirmApply is required for destructive plans")
	}
	if violations := verifyPlanRetainsProtected(plan); len(violations) > 0 {
		return GCReport{}, failurecodes.New(failurecodes.RegistryGCVerifyFailed,
			fmt.Sprintf("registry GC verify rejected plan: deletion candidates collide with protected digests: %v", violations))
	}

	report := GCReport{
		SchemaVersion: GCStateMachineSchemaVersion,
		AppliedAt:     plan.GeneratedAt,
		Repository:    plan.Repository,
		DryRun:        !plan.ConfirmApply,
		ConfirmApply:  plan.ConfirmApply,
		Summary:       plan.Summary,
		Events:        buildApplyEvents(plan),
	}
	return report, nil
}

// verifyPlanRetainsProtected returns the list of digests that the plan marks
// for deletion but that are also retained for a protection reason (rollback
// window, active pin, explicit pin, protected snapshot). An empty slice means
// the post-GC verify can pass. The protected-reason set mirrors the protection
// vocabulary produced by `PlanGCDryRun` in `internal/registry/retention.go`.
func verifyPlanRetainsProtected(plan GCPlan) []string {
	if plan.Summary.Deletions == 0 {
		return nil
	}
	protectedReasons := map[string]struct{}{
		"rollback window":    {},
		"active pin":         {},
		"explicit pin":       {},
		"protected snapshot": {},
	}
	protected := make(map[string]struct{})
	for _, action := range plan.Actions {
		if action.Delete {
			continue
		}
		if _, ok := protectedReasons[action.Reason]; !ok {
			continue
		}
		if action.Digest != "" {
			protected[action.Digest] = struct{}{}
		}
	}
	if len(protected) == 0 {
		return nil
	}
	var violations []string
	seen := make(map[string]struct{})
	for _, action := range plan.Actions {
		if !action.Delete {
			continue
		}
		if _, ok := protected[action.Digest]; !ok {
			continue
		}
		if _, dup := seen[action.Digest]; dup {
			continue
		}
		seen[action.Digest] = struct{}{}
		violations = append(violations, action.Digest)
	}
	sort.Strings(violations)
	return violations
}

func buildPlanEvents(plan GCPlan, now time.Time) []GCEvent {
	transitions := []struct {
		from   GCState
		to     GCState
		reason string
		desc   string
	}{
		{from: "", to: GCStateQuiesce, desc: "halt new writes against the registry"},
		{from: GCStateQuiesce, to: GCStateLock, desc: "acquire global GC advisory lock"},
		{from: GCStateLock, to: GCStateDryRun, desc: fmt.Sprintf("compute candidate set for %s (keep last %d)", plan.Repository, plan.KeepLast)},
		{from: GCStateDryRun, to: GCStateApply, reason: applyReason(plan), desc: applyDescription(plan)},
		{from: GCStateApply, to: GCStatePostGCVerify, desc: postGCVerifyDescription(plan)},
		{from: GCStatePostGCVerify, to: GCStateRelease, desc: "release advisory lock and resume writes"},
	}
	events := make([]GCEvent, 0, len(transitions))
	for i, transition := range transitions {
		events = append(events, GCEvent{
			Sequence:    i + 1,
			From:        transition.from,
			To:          transition.to,
			OccurredAt:  now.Add(time.Duration(i) * time.Second),
			Reason:      transition.reason,
			Description: transition.desc,
		})
	}
	return events
}

func buildApplyEvents(plan GCPlan) []GCEvent {
	out := make([]GCEvent, 0, len(plan.Events))
	for _, event := range plan.Events {
		copy := event
		out = append(out, copy)
	}
	return out
}

func applyReason(plan GCPlan) string {
	if plan.Summary.Deletions == 0 {
		return "no candidates"
	}
	if !plan.ConfirmApply {
		return "deferred (confirmApply=false)"
	}
	return "operator confirmed"
}

func applyDescription(plan GCPlan) string {
	if plan.Summary.Deletions == 0 {
		return "apply: no destructive actions required"
	}
	if !plan.ConfirmApply {
		return fmt.Sprintf("apply deferred: %d destructive actions await --confirm-apply", plan.Summary.Deletions)
	}
	return fmt.Sprintf("apply: record intent for %d destructive actions", plan.Summary.Deletions)
}

func postGCVerifyDescription(plan GCPlan) string {
	protected := plan.Summary.Candidates - plan.Summary.Deletions
	return fmt.Sprintf("post-GC verify: probe %d retained digests for retrievability", protected)
}

// SortEvents returns plan events sorted by sequence. The planner emits events
// in order already, but downstream consumers that merge events from multiple
// planners can use this helper to re-establish the canonical order.
func SortEvents(events []GCEvent) []GCEvent {
	out := append([]GCEvent(nil), events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}
