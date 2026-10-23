package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// RouteDiffStep is the per-route action a reconciler should take in order to
// move the data plane from observed state to desired state.
type RouteDiffStep string

const (
	// RouteDiffStepCreate is emitted for a desired route with no observed peer.
	RouteDiffStepCreate RouteDiffStep = "create"
	// RouteDiffStepUpdate is emitted when service URL or middleware metadata
	// drifts but the route name and host are unchanged.
	RouteDiffStepUpdate RouteDiffStep = "update"
	// RouteDiffStepSwap is emitted when the route's host changes; the data
	// plane must atomically swap router rules to avoid traffic loss.
	RouteDiffStepSwap RouteDiffStep = "swap"
	// RouteDiffStepRemove is emitted for an observed route that is no longer desired.
	RouteDiffStepRemove RouteDiffStep = "remove"
	// RouteDiffStepNoop is emitted when the desired and observed states match.
	RouteDiffStepNoop RouteDiffStep = "noop"
)

// RouteDifferInput is the immutable input to PlanRouteDiff.
type RouteDifferInput struct {
	// RouteGroup names the artifact bucket so the idempotency key is unique
	// across groups even when routes share names.
	RouteGroup string
	// Desired is the canonical desired-state list for the group.
	Desired []RouteInput
	// Observed is the current data-plane snapshot, keyed by route name.
	Observed []ObservedRouteState
}

// ObservedRouteState is the live data-plane projection of a previously applied
// route. It mirrors RouteInput but only carries the fields the differ needs.
type ObservedRouteState struct {
	Name        string
	Host        string
	PathPrefix  string
	ServiceURL  string
	TLS         bool
	Middlewares []string
}

// RouteDiffEntry is one per-route reconciliation decision. The IdempotencyKey
// is stable across runs so reconciler workers can deduplicate steps that are
// already in flight or have already been applied.
type RouteDiffEntry struct {
	RouteName      string        `json:"routeName"`
	Step           RouteDiffStep `json:"step"`
	IdempotencyKey string        `json:"idempotencyKey"`
	// FromHost is non-empty for swap/update/remove steps; it records the
	// previously-observed host for audit and rollback purposes.
	FromHost string `json:"fromHost,omitempty"`
	// ToHost is non-empty for create/update/swap steps; it records the
	// desired host the data plane must reach.
	ToHost string `json:"toHost,omitempty"`
	// Reasons is a stable sorted list of structured reason tags
	// (e.g., "host-changed", "service-url-changed"). It is empty for noop.
	Reasons []string `json:"reasons,omitempty"`
}

// RouteDiffPlan is the deterministic, sortable list of per-route steps along
// with aggregate counts for the operator-facing summary.
type RouteDiffPlan struct {
	RouteGroup string           `json:"routeGroup,omitempty"`
	Entries    []RouteDiffEntry `json:"entries"`
	Creates    int              `json:"creates"`
	Updates    int              `json:"updates"`
	Swaps      int              `json:"swaps"`
	Removes    int              `json:"removes"`
	Noops      int              `json:"noops"`
}

// stepOrderRank returns the canonical reconciliation rank for the supplied
// step. A reconciler must apply steps in ascending rank so that:
//   - noops are surfaced first (cosmetic; nothing to do),
//   - updates patch in-place before any swap can compete for the same host,
//   - swaps perform atomic host changes while traffic still flows,
//   - removes unbind retired routes only after dependent swaps are committed,
//   - creates bring up new routes last (so a remove of a same-named predecessor
//     is already applied).
//
// This ordering enforces the Phase 1.5 invariant that `remove` comes AFTER
// `swap` and BEFORE `create`. ValidateStepOrder is the canonical reader.
func stepOrderRank(step RouteDiffStep) int {
	switch step {
	case RouteDiffStepNoop:
		return 0
	case RouteDiffStepUpdate:
		return 1
	case RouteDiffStepSwap:
		return 2
	case RouteDiffStepRemove:
		return 3
	case RouteDiffStepCreate:
		return 4
	default:
		// Unknown steps sort last so the caller surfaces them as failures.
		return 99
	}
}

// StepOrderViolation describes a single ordering violation surfaced by
// ValidateStepOrder. Index is the offending step's index in the input slice.
type StepOrderViolation struct {
	// Index is the position of the offending step in the input slice.
	Index int
	// Step is the offending RouteDiffStep value.
	Step RouteDiffStep
	// PriorStep is the immediately preceding step that established the
	// violated rank floor.
	PriorStep RouteDiffStep
	// PriorIndex is the position of PriorStep in the input slice.
	PriorIndex int
}

// Error implements the error interface for the violation.
func (v StepOrderViolation) Error() string {
	return fmt.Sprintf("step %q at index %d must not precede step %q at index %d",
		v.PriorStep, v.PriorIndex, v.Step, v.Index)
}

// Step is the canonical name a reconciler emits for one entry of a
// RouteDiffPlan. It is provided as a thin alias so callers that operate on
// only the step kind (e.g., ValidateStepOrder) need not import the full
// RouteDiffEntry shape. Index is non-negative when the caller wants the
// violation report to reference an external slice.
type Step = RouteDiffEntry

// ValidateStepOrder enforces the Phase 1.5 reconciliation invariant: every
// step must appear in the canonical order returned by stepOrderRank. The
// canonical order is noop -> update -> swap -> remove -> create. The
// invariant guarantees that for any pair of steps the `remove` step comes
// AFTER any `swap` step and BEFORE any `create` step, so a host swap that
// frees a name does not race with the create that re-binds it.
//
// ValidateStepOrder returns nil when steps is empty, when every step has the
// same rank, or when ranks are non-decreasing across the slice. It returns
// the first detected violation otherwise.
func ValidateStepOrder(steps []Step) error {
	priorRank := -1
	priorStep := RouteDiffStep("")
	priorIndex := -1
	for i, entry := range steps {
		rank := stepOrderRank(entry.Step)
		if rank == 99 {
			return fmt.Errorf("step %q at index %d is not a recognized RouteDiffStep", entry.Step, i)
		}
		if rank < priorRank {
			return StepOrderViolation{
				Index:      i,
				Step:       entry.Step,
				PriorStep:  priorStep,
				PriorIndex: priorIndex,
			}
		}
		priorRank = rank
		priorStep = entry.Step
		priorIndex = i
	}
	return nil
}

// SortStepsCanonical returns a copy of steps reordered to satisfy
// ValidateStepOrder. The relative order of steps that share a rank is
// preserved (stable sort), so callers that pre-sorted by route name keep
// that secondary ordering intact.
func SortStepsCanonical(steps []Step) []Step {
	out := make([]Step, len(steps))
	copy(out, steps)
	sort.SliceStable(out, func(i, j int) bool {
		return stepOrderRank(out[i].Step) < stepOrderRank(out[j].Step)
	})
	return out
}

// PlanRouteDiff computes the per-route step plan that moves the observed data
// plane to the desired state. It is pure: identical inputs always produce
// identical entries (sorted by route name). Idempotency keys hash both the
// route group and the canonical step content so retries do not collide across
// concurrent reconcilers.
func PlanRouteDiff(input RouteDifferInput) RouteDiffPlan {
	desired := make(map[string]RouteInput, len(input.Desired))
	desiredOrder := make([]string, 0, len(input.Desired))
	for _, route := range input.Desired {
		route = normalizeRoute(route)
		if route.Name == "" {
			continue
		}
		if _, exists := desired[route.Name]; !exists {
			desiredOrder = append(desiredOrder, route.Name)
		}
		desired[route.Name] = route
	}

	observed := make(map[string]ObservedRouteState, len(input.Observed))
	observedOrder := make([]string, 0, len(input.Observed))
	for _, state := range input.Observed {
		state = normalizeObserved(state)
		if state.Name == "" {
			continue
		}
		if _, exists := observed[state.Name]; !exists {
			observedOrder = append(observedOrder, state.Name)
		}
		observed[state.Name] = state
	}

	plan := RouteDiffPlan{RouteGroup: input.RouteGroup}

	names := mergeRouteNames(desiredOrder, observedOrder)
	for _, name := range names {
		desiredRoute, hasDesired := desired[name]
		observedRoute, hasObserved := observed[name]

		switch {
		case hasDesired && !hasObserved:
			entry := RouteDiffEntry{
				RouteName: name,
				Step:      RouteDiffStepCreate,
				ToHost:    desiredRoute.Host,
				Reasons:   []string{"new-route"},
			}
			entry.IdempotencyKey = idempotencyKey(input.RouteGroup, entry, desiredRoute, ObservedRouteState{})
			plan.Entries = append(plan.Entries, entry)
			plan.Creates++
		case !hasDesired && hasObserved:
			entry := RouteDiffEntry{
				RouteName: name,
				Step:      RouteDiffStepRemove,
				FromHost:  observedRoute.Host,
				Reasons:   []string{"undesired"},
			}
			entry.IdempotencyKey = idempotencyKey(input.RouteGroup, entry, RouteInput{}, observedRoute)
			plan.Entries = append(plan.Entries, entry)
			plan.Removes++
		case hasDesired && hasObserved:
			reasons := diffReasons(desiredRoute, observedRoute)
			step := classifyChange(desiredRoute, observedRoute, reasons)
			entry := RouteDiffEntry{
				RouteName: name,
				Step:      step,
				FromHost:  observedRoute.Host,
				ToHost:    desiredRoute.Host,
				Reasons:   reasons,
			}
			entry.IdempotencyKey = idempotencyKey(input.RouteGroup, entry, desiredRoute, observedRoute)
			plan.Entries = append(plan.Entries, entry)
			switch step {
			case RouteDiffStepUpdate:
				plan.Updates++
			case RouteDiffStepSwap:
				plan.Swaps++
			case RouteDiffStepNoop:
				entry.Reasons = nil
				plan.Entries[len(plan.Entries)-1] = entry
				plan.Noops++
			}
		}
	}

	return plan
}

func normalizeObserved(state ObservedRouteState) ObservedRouteState {
	state.Name = strings.TrimSpace(state.Name)
	state.Host = strings.TrimSpace(state.Host)
	if state.PathPrefix == "" {
		state.PathPrefix = "/"
	}
	if len(state.Middlewares) > 0 {
		mw := append([]string(nil), state.Middlewares...)
		sort.Strings(mw)
		state.Middlewares = mw
	}
	return state
}

func mergeRouteNames(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, name := range a {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range b {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func diffReasons(desired RouteInput, observed ObservedRouteState) []string {
	reasons := make([]string, 0, 4)
	if desired.Host != observed.Host {
		reasons = append(reasons, "host-changed")
	}
	if desired.ServiceURL != observed.ServiceURL {
		reasons = append(reasons, "service-url-changed")
	}
	if desired.PathPrefix != observed.PathPrefix {
		reasons = append(reasons, "path-prefix-changed")
	}
	if desired.TLS != observed.TLS {
		reasons = append(reasons, "tls-toggled")
	}
	if !sortedEqual(desired.Middlewares, observed.Middlewares) {
		reasons = append(reasons, "middlewares-changed")
	}
	sort.Strings(reasons)
	return reasons
}

func classifyChange(desired RouteInput, observed ObservedRouteState, reasons []string) RouteDiffStep {
	if len(reasons) == 0 {
		_ = desired
		_ = observed
		return RouteDiffStepNoop
	}
	for _, reason := range reasons {
		if reason == "host-changed" {
			return RouteDiffStepSwap
		}
	}
	return RouteDiffStepUpdate
}

func sortedEqual(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func idempotencyKey(group string, entry RouteDiffEntry, desired RouteInput, observed ObservedRouteState) string {
	parts := []string{
		"group=" + group,
		"name=" + entry.RouteName,
		"step=" + string(entry.Step),
	}
	switch entry.Step {
	case RouteDiffStepCreate:
		parts = append(parts,
			"to_host="+desired.Host,
			"to_path="+desired.PathPrefix,
			"to_service="+desired.ServiceURL,
			fmt.Sprintf("to_tls=%t", desired.TLS),
			"to_middlewares="+strings.Join(sortedCopy(desired.Middlewares), ","),
		)
	case RouteDiffStepRemove:
		parts = append(parts,
			"from_host="+observed.Host,
			"from_path="+observed.PathPrefix,
			"from_service="+observed.ServiceURL,
			fmt.Sprintf("from_tls=%t", observed.TLS),
			"from_middlewares="+strings.Join(sortedCopy(observed.Middlewares), ","),
		)
	case RouteDiffStepUpdate, RouteDiffStepSwap:
		parts = append(parts,
			"from_host="+observed.Host,
			"to_host="+desired.Host,
			"from_path="+observed.PathPrefix,
			"to_path="+desired.PathPrefix,
			"from_service="+observed.ServiceURL,
			"to_service="+desired.ServiceURL,
			fmt.Sprintf("from_tls=%t", observed.TLS),
			fmt.Sprintf("to_tls=%t", desired.TLS),
			"from_middlewares="+strings.Join(sortedCopy(observed.Middlewares), ","),
			"to_middlewares="+strings.Join(sortedCopy(desired.Middlewares), ","),
			"reasons="+strings.Join(entry.Reasons, ","),
		)
	case RouteDiffStepNoop:
		// Use desired so callers can detect content equality by key alone.
		parts = append(parts,
			"host="+desired.Host,
			"path="+desired.PathPrefix,
			"service="+desired.ServiceURL,
		)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
