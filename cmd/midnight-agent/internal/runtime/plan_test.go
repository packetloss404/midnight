package runtime

import "testing"

func TestDiffPlanReturnsDeterministicActions(t *testing.T) {
	desired := []Deployment{
		{ID: "beta", Revision: "2", Spec: hardenedSpec()},
		{ID: "alpha", Revision: "1", Spec: hardenedSpec()},
		{ID: "delta", Revision: "1", Spec: ContainerSpec{Image: "example/delta:2", CapDrop: []string{"ALL"}, Init: true}},
	}
	current := []ContainerState{
		{DeploymentID: "gamma", Revision: "1", Spec: hardenedSpec()},
		{DeploymentID: "beta", Revision: "1", Spec: hardenedSpec()},
		{DeploymentID: "alpha", Revision: "1", Spec: hardenedSpec()},
		{DeploymentID: "delta", Revision: "1", Spec: ContainerSpec{Image: "example/delta:1", CapDrop: []string{"ALL"}, Init: true}},
	}

	plan := DiffPlan(desired, current)
	want := []struct {
		id     string
		action PlanAction
	}{
		{id: "alpha", action: PlanNoop},
		{id: "beta", action: PlanUpdate},
		{id: "delta", action: PlanUpdate},
		{id: "gamma", action: PlanDelete},
	}

	if len(plan) != len(want) {
		t.Fatalf("len(plan) = %d, want %d", len(plan), len(want))
	}
	for i, step := range plan {
		if step.DeploymentID != want[i].id || step.Action != want[i].action {
			t.Fatalf("plan[%d] = %s %s, want %s %s", i, step.DeploymentID, step.Action, want[i].id, want[i].action)
		}
	}
}

func TestDiffPlanCreatesMissingDeployment(t *testing.T) {
	plan := DiffPlan([]Deployment{{ID: "app", Revision: "1", Spec: hardenedSpec()}}, nil)

	if len(plan) != 1 || plan[0].Action != PlanCreate || plan[0].Desired == nil || plan[0].Current != nil {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestDiffPlanTreatsCapabilityOrderAsEqual(t *testing.T) {
	desired := []Deployment{{ID: "app", Revision: "1", Spec: ContainerSpec{CapDrop: []string{"all", "NET_RAW"}, CapAdd: []string{"setuid", "chown"}, Init: true}}}
	current := []ContainerState{{DeploymentID: "app", Revision: "1", Spec: ContainerSpec{CapDrop: []string{"NET_RAW", "ALL"}, CapAdd: []string{"CHOWN", "SETUID"}, Init: true}}}

	plan := DiffPlan(desired, current)
	if len(plan) != 1 || plan[0].Action != PlanNoop {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReconcilePlanCreatesMissingDeploymentInRuntimeOrder(t *testing.T) {
	desired := Deployment{
		ID:       "app",
		Revision: "rev1",
		Spec:     hardenedSpec(),
		Network:  BridgeNetworkSpec{Name: "or-net-project-env"},
		RouteID:  "route-a",
	}

	steps := ReconcilePlan([]PlanStep{{Action: PlanCreate, DeploymentID: "app", Desired: &desired}}, RuntimeObservations{})
	want := []ReconcileStepKind{
		ReconcileCreateNetwork,
		ReconcilePullImage,
		ReconcileCreateContainer,
		ReconcileStartContainer,
		ReconcileWaitHealth,
		ReconcileUpdateRouteTarget,
	}

	assertStepKinds(t, steps, want)
	if steps[0].Reason == "" || steps[0].IdempotencyKey != "create_network:app:or-net-project-env" {
		t.Fatalf("step[0] = %+v", steps[0])
	}
}

func TestReconcilePlanSkipsObservedSatisfiedCreateSteps(t *testing.T) {
	desired := Deployment{
		ID:       "app",
		Revision: "rev1",
		Spec:     hardenedSpec(),
		Network:  BridgeNetworkSpec{Name: "or-net-project-env"},
		RouteID:  "route-a",
	}
	observed := RuntimeObservations{
		Networks:   []ObservedNetwork{{Name: "or-net-project-env"}},
		Images:     []ObservedImage{{Image: "example/app:rev1"}},
		Containers: []ObservedContainer{{DeploymentID: "app", Revision: "rev1", Created: true, Running: true, Healthy: true}},
		RouteTargets: []ObservedRouteTarget{{
			RouteID:      "route-a",
			DeploymentID: "app",
			Revision:     "rev1",
		}},
	}

	steps := ReconcilePlan([]PlanStep{{Action: PlanCreate, DeploymentID: "app", Desired: &desired}}, observed)

	if len(steps) != 0 {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestReconcilePlanUpdatesBeforeCleaningOldRevision(t *testing.T) {
	desiredSpec := hardenedSpec()
	desiredSpec.Image = "example/app:rev2"
	desired := Deployment{ID: "app", Revision: "rev2", Spec: desiredSpec, RouteID: "route-a"}
	current := ContainerState{DeploymentID: "app", Revision: "rev1", Spec: hardenedSpec()}
	observed := RuntimeObservations{
		Images:     []ObservedImage{{Image: "example/app:rev1"}},
		Containers: []ObservedContainer{{DeploymentID: "app", Revision: "rev1", Created: true, Running: true, Healthy: true}},
	}

	steps := ReconcilePlan([]PlanStep{{Action: PlanUpdate, DeploymentID: "app", Desired: &desired, Current: &current}}, observed)
	want := []ReconcileStepKind{
		ReconcilePullImage,
		ReconcileCreateContainer,
		ReconcileStartContainer,
		ReconcileWaitHealth,
		ReconcileUpdateRouteTarget,
		ReconcileStopContainer,
		ReconcileRemoveContainer,
	}

	assertStepKinds(t, steps, want)
	if steps[5].IdempotencyKey != "stop_container:app:rev1" || steps[6].IdempotencyKey != "remove_container:app:rev1" {
		t.Fatalf("cleanup steps = %+v", steps[5:])
	}
}

func TestReconcilePlanDeletesOnlyCleanupSteps(t *testing.T) {
	current := ContainerState{DeploymentID: "app", Revision: "rev1", Spec: hardenedSpec()}
	observed := RuntimeObservations{Containers: []ObservedContainer{{DeploymentID: "app", Revision: "rev1", Created: true, Running: false}}}

	steps := ReconcilePlan([]PlanStep{{Action: PlanDelete, DeploymentID: "app", Current: &current}}, observed)

	assertStepKinds(t, steps, []ReconcileStepKind{ReconcileRemoveContainer})
}

func TestReconcilePlanAttachesVolumesWhenBackendSupportsVolumes(t *testing.T) {
	desiredSpec := hardenedSpec()
	desiredSpec.Volumes = []VolumeMount{{VolumeID: "vol_data", Target: "/data"}}
	desired := Deployment{ID: "app", Revision: "rev1", Spec: desiredSpec}

	steps := ReconcilePlan([]PlanStep{{Action: PlanCreate, DeploymentID: "app", Desired: &desired}}, RuntimeObservations{Capabilities: RuntimeCapabilities{Volumes: true}})

	assertStepKinds(t, steps, []ReconcileStepKind{ReconcilePullImage, ReconcileAttachVolume, ReconcileCreateContainer, ReconcileStartContainer, ReconcileWaitHealth})
	if steps[1].IdempotencyKey != "attach_volume:app:rev1:vol_data:/data" {
		t.Fatalf("attach step = %+v", steps[1])
	}
}

func TestReconcilePlanSkipsVolumeStepsWhenBackendDoesNotSupportVolumes(t *testing.T) {
	desiredSpec := hardenedSpec()
	desiredSpec.Volumes = []VolumeMount{{VolumeID: "vol_data", Target: "/data"}}
	desired := Deployment{ID: "app", Revision: "rev1", Spec: desiredSpec}

	steps := ReconcilePlan([]PlanStep{{Action: PlanCreate, DeploymentID: "app", Desired: &desired}}, RuntimeObservations{})

	for _, step := range steps {
		if step.Kind == ReconcileAttachVolume || step.Kind == ReconcileDetachVolume {
			t.Fatalf("unexpected volume step: %+v in %+v", step, steps)
		}
	}
}

func TestReconcilePlanDetachesVolumesAfterRemovingContainer(t *testing.T) {
	currentSpec := hardenedSpec()
	currentSpec.Volumes = []VolumeMount{{VolumeID: "vol_data", Target: "/data"}}
	current := ContainerState{DeploymentID: "app", Revision: "rev1", Spec: currentSpec}
	observed := RuntimeObservations{
		Capabilities: RuntimeCapabilities{Volumes: true},
		Containers:   []ObservedContainer{{DeploymentID: "app", Revision: "rev1", Created: true, Running: false}},
	}

	steps := ReconcilePlan([]PlanStep{{Action: PlanDelete, DeploymentID: "app", Current: &current}}, observed)

	assertStepKinds(t, steps, []ReconcileStepKind{ReconcileRemoveContainer, ReconcileDetachVolume})
}

func assertStepKinds(t *testing.T, steps []ReconcileStep, want []ReconcileStepKind) {
	t.Helper()
	if len(steps) != len(want) {
		t.Fatalf("len(steps) = %d, want %d: %+v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i].Kind != want[i] {
			t.Fatalf("steps[%d].Kind = %s, want %s: %+v", i, steps[i].Kind, want[i], steps)
		}
	}
}
