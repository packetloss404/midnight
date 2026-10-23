package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// PinProbe is the contract a GC orchestrator uses to learn whether a digest
// is protected before it is reclaimed. FilePinStore satisfies this contract
// in Phase 1; the future Postgres-backed PinStore (Lane 1) satisfies the
// same shape.
type PinProbe interface {
	ProbeProtectedDigest(repository, digest string, now time.Time) ProtectedDigestProbeResult
	RefuseGCIfReferenced(digest string, now time.Time) error
}

// WritePauseController is the contract a GC orchestrator uses to pause
// writes to the embedded registry while an apply sweep runs. The
// distribution binary supports a read-only mode flag; in Phase 1 we model
// it as an interface so tests do not need a real registry.
type WritePauseController interface {
	PauseWrites(ctx context.Context) error
	ResumeWrites(ctx context.Context) error
}

// NoopWritePauseController is a sentinel that does nothing. It is the
// default for callers that have not wired write-pause yet; the apply path
// will refuse to delete blobs without one once we ship the distribution
// integration.
type NoopWritePauseController struct{}

// PauseWrites is a no-op.
func (NoopWritePauseController) PauseWrites(context.Context) error { return nil }

// ResumeWrites is a no-op.
func (NoopWritePauseController) ResumeWrites(context.Context) error { return nil }

// GCOrchestrator wires retention planning to pin probing and write-pause
// gating. Phase 1 callers create one per registry instance.
type GCOrchestrator struct {
	Tags         []ImageTag
	Rules        []RetentionRule
	Probe        PinProbe
	WritePause   WritePauseController
	GeneratedAt  time.Time
	ConfirmApply bool
}

// GCApplyEvidence is the support-safe summary an operator sees after an
// apply sweep finishes. It records protected and reclaimed digests, refused
// digests, and the audit trail expected by ADR-008's
// `gate.registry-gc-respects-pins` CI gate.
type GCApplyEvidence struct {
	GeneratedAt        time.Time         `json:"generatedAt"`
	AppliedAt          time.Time         `json:"appliedAt"`
	ConfirmApply       bool              `json:"confirmApply"`
	Summary            GCSummary         `json:"summary"`
	Reclaimed          []RetentionAction `json:"reclaimed,omitempty"`
	Protected          []RetentionAction `json:"protected,omitempty"`
	RefusedByPinReason []string          `json:"refusedByPinReason,omitempty"`
}

// PlanGC produces the dry-run plan and consults the pin store to mark any
// digest the planner thought was deletable but that is in fact still
// protected by an active or rollback-window pin.
func (o GCOrchestrator) PlanGC() (RetentionPlan, error) {
	if o.Probe == nil {
		return RetentionPlan{}, errors.New("registry GC: pin probe is required")
	}
	now := o.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	pins, err := o.pinsFromProbe(now)
	if err != nil {
		return RetentionPlan{}, err
	}
	plan := PlanGCDryRun(GCPlanRequest{
		GeneratedAt: now,
		Tags:        o.Tags,
		Pins:        pins,
		Rules:       o.Rules,
	})
	return plan, nil
}

// ApplyGC pauses writes, applies the plan, and resumes writes. The pin
// probe is consulted again per-deletion as a defence-in-depth check (the
// pin set may have grown between PlanGC and ApplyGC if a new deployment
// landed).
func (o GCOrchestrator) ApplyGC(ctx context.Context, plan RetentionPlan) (GCApplyEvidence, error) {
	now := o.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if o.Probe == nil {
		return GCApplyEvidence{}, errors.New("registry GC: pin probe is required")
	}
	pause := o.WritePause
	if pause == nil {
		pause = NoopWritePauseController{}
	}
	if err := pause.PauseWrites(ctx); err != nil {
		return GCApplyEvidence{}, fmt.Errorf("registry GC: pause writes: %w", err)
	}
	defer func() {
		_ = pause.ResumeWrites(ctx)
	}()

	evidence := GCApplyEvidence{
		GeneratedAt:  plan.GeneratedAt,
		AppliedAt:    now,
		ConfirmApply: o.ConfirmApply,
	}

	deletions := plan.Deletions()
	final := make([]RetentionAction, 0, len(deletions))
	refused := map[string]struct{}{}
	for _, action := range deletions {
		if err := o.Probe.RefuseGCIfReferenced(action.Digest, now); err != nil {
			action.Delete = false
			action.Reason = "pin guard"
			evidence.Protected = append(evidence.Protected, action)
			refused[err.Error()] = struct{}{}
			continue
		}
		final = append(final, action)
	}
	for _, action := range plan.Actions {
		if action.Delete {
			continue
		}
		evidence.Protected = append(evidence.Protected, action)
	}

	if len(refused) > 0 {
		// Surface refused reasons sorted for stable evidence.
		out := make([]string, 0, len(refused))
		for k := range refused {
			out = append(out, k)
		}
		sort.Strings(out)
		evidence.RefusedByPinReason = out
	}

	if !o.ConfirmApply {
		evidence.Summary = summarizeGCActions(plan.Actions)
		return evidence, nil
	}

	apply := ApplyGCPlan(GCApplyRequest{Plan: RetentionPlan{
		GeneratedAt: plan.GeneratedAt,
		DryRun:      false,
		Rules:       plan.Rules,
		Actions:     append(planNonDeletions(plan), final...),
	}, ConfirmApply: o.ConfirmApply})
	evidence.Summary = apply.Summary
	evidence.Reclaimed = apply.Actions
	return evidence, nil
}

func (o GCOrchestrator) pinsFromProbe(now time.Time) ([]ImagePin, error) {
	seen := map[string]ImagePin{}
	for _, tag := range o.Tags {
		key := tag.Repository + "\x00" + tag.Digest
		if _, ok := seen[key]; ok {
			continue
		}
		probe := o.Probe.ProbeProtectedDigest(tag.Repository, tag.Digest, now)
		if !probe.Protected {
			continue
		}
		pin := ImagePin{
			Repository:    tag.Repository,
			Digest:        tag.Digest,
			Active:        probe.Reason == string(PinReasonActive),
			RollbackUntil: now.Add(7 * 24 * time.Hour), // conservative 7-day default if the probe does not return one
		}
		seen[key] = pin
	}
	out := make([]ImagePin, 0, len(seen))
	for _, pin := range seen {
		out = append(out, pin)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Repository != out[j].Repository {
			return out[i].Repository < out[j].Repository
		}
		return out[i].Digest < out[j].Digest
	})
	return out, nil
}

func planNonDeletions(plan RetentionPlan) []RetentionAction {
	non := make([]RetentionAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		if action.Delete {
			continue
		}
		non = append(non, action)
	}
	return non
}
