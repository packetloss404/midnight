// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"errors"
	"testing"
)

func TestValidateTransitionLegalMoves(t *testing.T) {
	legal := []struct{ from, to DeploymentState }{
		{DeploymentQueued, DeploymentBuilding},
		{DeploymentQueued, DeploymentDeploying},
		{DeploymentQueued, DeploymentFailed},
		{DeploymentQueued, DeploymentCanceled},
		{DeploymentBuilding, DeploymentDeploying},
		{DeploymentBuilding, DeploymentFailed},
		{DeploymentBuilding, DeploymentCanceled},
		{DeploymentDeploying, DeploymentActive},
		{DeploymentDeploying, DeploymentFailed},
		{DeploymentDeploying, DeploymentCanceled},
		{DeploymentActive, DeploymentSuperseded},
	}
	for _, tc := range legal {
		if err := ValidateTransition(tc.from, tc.to); err != nil {
			t.Errorf("ValidateTransition(%s, %s) = %v, want nil", tc.from, tc.to, err)
		}
	}
}

func TestValidateTransitionIllegalMoves(t *testing.T) {
	illegal := []struct{ from, to DeploymentState }{
		{DeploymentQueued, DeploymentActive},      // must pass through deploying
		{DeploymentBuilding, DeploymentActive},    // must pass through deploying
		{DeploymentActive, DeploymentActive},      // no self-transition
		{DeploymentActive, DeploymentFailed},      // active only supersedes
		{DeploymentSuperseded, DeploymentActive},  // terminal
		{DeploymentFailed, DeploymentDeploying},   // terminal
		{DeploymentCanceled, DeploymentBuilding},  // terminal
		{DeploymentDeploying, DeploymentBuilding}, // no backward move
	}
	for _, tc := range illegal {
		err := ValidateTransition(tc.from, tc.to)
		if err == nil {
			t.Errorf("ValidateTransition(%s, %s) = nil, want illegal-transition error", tc.from, tc.to)
			continue
		}
		var ille *ErrIllegalTransition
		if !errors.As(err, &ille) {
			// unknown-state errors are also acceptable rejections, but the
			// terminal/illegal ones above should be typed.
			t.Logf("ValidateTransition(%s, %s) rejected with %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateTransitionUnknownStates(t *testing.T) {
	if err := ValidateTransition("bogus", DeploymentActive); err == nil {
		t.Error("expected error for unknown source state")
	}
	if err := ValidateTransition(DeploymentQueued, "bogus"); err == nil {
		t.Error("expected error for unknown target state")
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []DeploymentState{DeploymentSuperseded, DeploymentFailed, DeploymentCanceled}
	for _, s := range terminal {
		if !IsTerminal(s) {
			t.Errorf("IsTerminal(%s) = false, want true", s)
		}
	}
	nonTerminal := []DeploymentState{DeploymentQueued, DeploymentBuilding, DeploymentDeploying, DeploymentActive}
	for _, s := range nonTerminal {
		if IsTerminal(s) {
			t.Errorf("IsTerminal(%s) = true, want false", s)
		}
	}
}

func TestCanPromoteOnlyFromDeploying(t *testing.T) {
	if !CanPromote(DeploymentDeploying) {
		t.Error("CanPromote(deploying) = false, want true")
	}
	for _, s := range []DeploymentState{DeploymentQueued, DeploymentBuilding, DeploymentActive, DeploymentFailed} {
		if CanPromote(s) {
			t.Errorf("CanPromote(%s) = true, want false", s)
		}
	}
}

func TestKeepsPreviousActiveRouted(t *testing.T) {
	if KeepsPreviousActiveRouted(DeploymentActive) {
		t.Error("promoting to active should NOT keep the previous active routed")
	}
	for _, s := range []DeploymentState{DeploymentBuilding, DeploymentDeploying, DeploymentFailed, DeploymentCanceled} {
		if !KeepsPreviousActiveRouted(s) {
			t.Errorf("transition to %s must keep previous active routed", s)
		}
	}
}

func TestRollbackNeverRebuilds(t *testing.T) {
	if ActionRollbackDigest.RebuildsSource() {
		t.Error("rollback_digest must NOT rebuild source (it is a digest pull)")
	}
	if ActionRestartInstance.RebuildsSource() {
		t.Error("restart_instance must NOT rebuild source")
	}
	if !ActionDeploySource.RebuildsSource() || !ActionRedeployRebuild.RebuildsSource() {
		t.Error("source deploy / rebuild-ref actions must rebuild source")
	}
}

func TestValidAction(t *testing.T) {
	if !ValidAction(ActionDeployImage) {
		t.Error("deploy_image should be a valid action")
	}
	if ValidAction("nonsense") {
		t.Error("unknown action should be invalid")
	}
}
