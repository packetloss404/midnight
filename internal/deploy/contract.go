// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"fmt"
	"sort"
	"strings"
)

// This file is the Go mirror of the canonical deployment-record finite state
// machine enforced in the database by validate_deployment_contract() (migration
// 0014). Application code validates transitions here first so illegal moves are
// caught before they hit the DB trigger — defense in depth, with app and schema
// agreeing on one contract.
//
// Product truth for a deployment lives in the `deployments` table, NOT in
// operation-event message strings. `operations`/`jobs` remain request tracking.

// DeploymentState is the lifecycle state of a canonical deployment record.
type DeploymentState string

const (
	// DeploymentQueued is the only legal insert state: a deployment intake
	// created the row but no build/deploy work has started.
	DeploymentQueued DeploymentState = "queued"
	// DeploymentBuilding means a source build is in progress for this
	// deployment (skipped for pre-built image deploys).
	DeploymentBuilding DeploymentState = "building"
	// DeploymentDeploying means the candidate image is being pulled, started,
	// and readiness/route-checked, but is not yet serving traffic.
	DeploymentDeploying DeploymentState = "deploying"
	// DeploymentActive means this deployment is the one serving live traffic
	// for its (service, environment). At most one active per (service, env).
	DeploymentActive DeploymentState = "active"
	// DeploymentSuperseded means a newer deployment took over as active.
	DeploymentSuperseded DeploymentState = "superseded"
	// DeploymentFailed is a terminal failure state.
	DeploymentFailed DeploymentState = "failed"
	// DeploymentCanceled is a terminal user/system cancellation state.
	DeploymentCanceled DeploymentState = "canceled"
)

// DeploymentAction is the lifecycle action that created a deployment record.
// Rollback creates a NEW deployment row from a prior immutable digest and never
// rebuilds; redeploy variants and restart are distinct so audit/telemetry can
// tell them apart. Mirrors the enum in the migration.
type DeploymentAction string

const (
	ActionDeploySource    DeploymentAction = "deploy_source"
	ActionDeployImage     DeploymentAction = "deploy_image"
	ActionRedeployRebuild DeploymentAction = "redeploy_rebuild_ref"
	ActionRedeployImage   DeploymentAction = "redeploy_image"
	ActionRollbackDigest  DeploymentAction = "rollback_digest"
	ActionRestartInstance DeploymentAction = "restart_instance"
)

// validActions is the set of recognized lifecycle actions.
var validActions = map[DeploymentAction]struct{}{
	ActionDeploySource:    {},
	ActionDeployImage:     {},
	ActionRedeployRebuild: {},
	ActionRedeployImage:   {},
	ActionRollbackDigest:  {},
	ActionRestartInstance: {},
}

// ValidAction reports whether action is a recognized lifecycle action.
func ValidAction(action DeploymentAction) bool {
	_, ok := validActions[action]
	return ok
}

// RebuildsSource reports whether an action re-invokes the build path. Rollback
// MUST NOT rebuild (it is a digest pull), which callers assert via this.
func (a DeploymentAction) RebuildsSource() bool {
	switch a {
	case ActionDeploySource, ActionRedeployRebuild:
		return true
	default:
		return false
	}
}

// allowedTransitions maps each state to the states it may legally move to.
// The empty-slice entries (active-terminal after supersede, failed, canceled)
// are still present so IsTerminal and the validator agree on the full set.
var allowedTransitions = map[DeploymentState][]DeploymentState{
	DeploymentQueued:     {DeploymentBuilding, DeploymentDeploying, DeploymentFailed, DeploymentCanceled},
	DeploymentBuilding:   {DeploymentDeploying, DeploymentFailed, DeploymentCanceled},
	DeploymentDeploying:  {DeploymentActive, DeploymentFailed, DeploymentCanceled},
	DeploymentActive:     {DeploymentSuperseded},
	DeploymentSuperseded: {},
	DeploymentFailed:     {},
	DeploymentCanceled:   {},
}

// ValidState reports whether s is a known deployment state.
func ValidState(s DeploymentState) bool {
	_, ok := allowedTransitions[s]
	return ok
}

// IsTerminal reports whether s is a terminal state (no legal outgoing
// transitions). superseded/failed/canceled are terminal; active is NOT terminal
// because it can still be superseded.
func IsTerminal(s DeploymentState) bool {
	next, ok := allowedTransitions[s]
	return ok && len(next) == 0
}

// CanPromote reports whether a deployment in state s may be promoted to active.
// Only a deployment that has passed readiness (state=deploying) may promote.
func CanPromote(s DeploymentState) bool {
	return s == DeploymentDeploying
}

// KeepsPreviousActiveRouted reports whether a transition into state `to` must
// leave the previous active deployment serving traffic. Any non-active outcome
// of an in-flight deployment (failed/canceled, or still building/deploying)
// keeps the previous active routed; only a successful promote to active swaps.
func KeepsPreviousActiveRouted(to DeploymentState) bool {
	return to != DeploymentActive
}

// ErrIllegalTransition is returned by ValidateTransition for a move not in the
// contract. It carries the from/to states for actionable diagnostics.
type ErrIllegalTransition struct {
	From DeploymentState
	To   DeploymentState
}

func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("deploy: illegal deployment transition %s -> %s (allowed: %s)",
		e.From, e.To, strings.Join(allowedTransitionNames(e.From), ", "))
}

func allowedTransitionNames(from DeploymentState) []string {
	next, ok := allowedTransitions[from]
	if !ok {
		return []string{"<unknown state>"}
	}
	names := make([]string, 0, len(next))
	for _, s := range next {
		names = append(names, string(s))
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"<terminal>"}
	}
	return names
}

// ValidateTransition returns nil if moving from -> to is legal under the
// deployment FSM, or an error describing why not. Unknown states and no-op
// self-transitions are rejected.
func ValidateTransition(from, to DeploymentState) error {
	if !ValidState(from) {
		return fmt.Errorf("deploy: unknown source deployment state %q", from)
	}
	if !ValidState(to) {
		return fmt.Errorf("deploy: unknown target deployment state %q", to)
	}
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return nil
		}
	}
	return &ErrIllegalTransition{From: from, To: to}
}
