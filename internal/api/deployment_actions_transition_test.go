package api

import (
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/deploy"
)

// A service's only deployment must be removable. Before this, `stop` left
// Active set and `remove` refused while it was set, so stop -> remove ->
// delete never terminated and the service could not be torn down at all.
func TestRuntimeActionRemoveRequiresStopNotInactivity(t *testing.T) {
	running := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "running"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRemove, running); err == nil {
		t.Fatal("removing a running deployment must be refused")
	} else if !errors.Is(err, ErrDeploymentActionConflict) {
		t.Fatalf("want ErrDeploymentActionConflict, got %v", err)
	}

	stopped := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "stopped"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRemove, stopped); err != nil {
		t.Fatalf("a stopped deployment must be removable even while it is still the active record: %v", err)
	}

	superseded := DeploymentRecord{ID: "dep_0", Active: false, RuntimeDesiredState: "stopped"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRemove, superseded); err != nil {
		t.Fatalf("a superseded deployment must remain removable: %v", err)
	}

	already := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "removed"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRemove, already); err == nil {
		t.Fatal("an already-removed deployment must be refused")
	}
}

func TestRuntimeActionStopAndRestartKeepTheirGuards(t *testing.T) {
	running := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "running"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionStop, running); err != nil {
		t.Fatalf("stop on a running active deployment: %v", err)
	}
	stopped := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "stopped"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionStop, stopped); err == nil {
		t.Fatal("stop must still require a running deployment")
	}
	inactive := DeploymentRecord{ID: "dep_0", Active: false, RuntimeDesiredState: "running"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionStop, inactive); err == nil {
		t.Fatal("stop must still require the active deployment")
	}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRestart, inactive); err == nil {
		t.Fatal("restart must still require the active deployment")
	}
	removed := DeploymentRecord{ID: "dep_1", Active: true, RuntimeDesiredState: "removed"}
	if err := validateRuntimeActionTransition(deploy.RuntimeActionRestart, removed); err == nil {
		t.Fatal("restart must still refuse a removed deployment")
	}
}
