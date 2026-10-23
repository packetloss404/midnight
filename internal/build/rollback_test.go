package build

import (
	"strings"
	"testing"
)

func sampleRollbackInput() RollbackInput {
	return RollbackInput{
		ServiceID:     "svc",
		EnvironmentID: "prod",
		ActorID:       "actor",
		Source: DeploymentSnapshotRecord{
			Deployment: "dep-1",
			Revision:   "r1",
			Snapshot: DeploymentSnapshot{
				SchemaVersion: DeploymentSnapshotSchemaVersion,
				Images: []ImageSnapshot{{
					Name:        "app",
					Tag:         "stable",
					Digest:      "sha256:abcd",
					RollbackRef: "sha256:abcd",
				}},
			},
		},
	}
}

func TestPlanRollbackEmits14Steps(t *testing.T) {
	plan, err := PlanRollback(sampleRollbackInput())
	if err != nil {
		t.Fatalf("PlanRollback() error = %v", err)
	}
	if len(plan.Steps) != 14 {
		t.Fatalf("len(steps) = %d, want 14", len(plan.Steps))
	}
	if plan.SourceDeployment != "dep-1" || plan.SourceRevision != "r1" {
		t.Fatalf("source deployment/revision = %q/%q", plan.SourceDeployment, plan.SourceRevision)
	}
	if plan.ImageDigest != "sha256:abcd" {
		t.Fatalf("image digest = %q", plan.ImageDigest)
	}
	if plan.RollbackSource != "manual" {
		t.Fatalf("rollback source default = %q", plan.RollbackSource)
	}

	// Steps must be ordered 1..14.
	for i, step := range plan.Steps {
		if step.Order != i+1 {
			t.Fatalf("step[%d].Order = %d, want %d", i, step.Order, i+1)
		}
	}
}

func TestPlanRollbackHasNoBuildInvariant(t *testing.T) {
	plan, err := PlanRollback(sampleRollbackInput())
	if err != nil {
		t.Fatalf("PlanRollback() error = %v", err)
	}
	if !plan.HasNoBuildInvariant() {
		t.Fatalf("HasNoBuildInvariant() = false, want true")
	}
	if plan.NoBuildInvariant != "noBuild=true" {
		t.Fatalf("NoBuildInvariant = %q, want noBuild=true", plan.NoBuildInvariant)
	}

	// Validate() must reject a plan that loses the invariant.
	plan.NoBuildInvariant = "noBuild=false"
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "noBuildInvariant") {
		t.Fatalf("Validate() must refuse cleared invariant; err = %v", err)
	}
}

func TestPlanRollbackRejectsMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RollbackInput)
		want   string
	}{
		{name: "missing service", mutate: func(i *RollbackInput) { i.ServiceID = "" }, want: "serviceId"},
		{name: "missing environment", mutate: func(i *RollbackInput) { i.EnvironmentID = "" }, want: "environmentId"},
		{name: "missing actor", mutate: func(i *RollbackInput) { i.ActorID = "" }, want: "actorId"},
		{name: "missing source", mutate: func(i *RollbackInput) { i.Source.Deployment = "" }, want: "source deployment"},
		{name: "missing digest", mutate: func(i *RollbackInput) {
			i.Source.Snapshot.Images = nil
		}, want: "digest pin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := sampleRollbackInput()
			tt.mutate(&input)
			_, err := PlanRollback(input)
			if err == nil {
				t.Fatalf("PlanRollback() error = nil, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPlanRollbackStepsCarryCanonicalIDs(t *testing.T) {
	plan, err := PlanRollback(sampleRollbackInput())
	if err != nil {
		t.Fatalf("PlanRollback() error = %v", err)
	}
	want := []RollbackStepID{
		RollbackStepResolveTarget,
		RollbackStepAuthorize,
		RollbackStepAcquireLock,
		RollbackStepInsertDeployment,
		RollbackStepEmitAudit,
		RollbackStepEnqueueDeploy,
		RollbackStepWorkerPull,
		RollbackStepVerifySignature,
		RollbackStepResolveSecrets,
		RollbackStepScheduleAndStart,
		RollbackStepPromote,
		RollbackStepDrainPrevious,
		RollbackStepDecrementPin,
		RollbackStepFinalAudit,
	}
	for i, step := range plan.Steps {
		if step.ID != want[i] {
			t.Fatalf("step[%d].ID = %q, want %q", i, step.ID, want[i])
		}
	}
}

func TestPlanRollbackOverridesRollbackSource(t *testing.T) {
	input := sampleRollbackInput()
	input.RollbackSource = "upgrade-auto"
	plan, err := PlanRollback(input)
	if err != nil {
		t.Fatalf("PlanRollback() error = %v", err)
	}
	if plan.RollbackSource != "upgrade-auto" {
		t.Fatalf("rollback source = %q, want upgrade-auto", plan.RollbackSource)
	}
}

func TestPlanRollbackValidateRejectsShortStepList(t *testing.T) {
	plan, err := PlanRollback(sampleRollbackInput())
	if err != nil {
		t.Fatalf("PlanRollback() error = %v", err)
	}
	plan.Steps = plan.Steps[:5]
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want refusal for truncated step list")
	}
}
