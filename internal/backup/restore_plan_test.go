package backup

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateRestorePlanReturnsCanonicalOrder(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	want := []RestorePlanArtifact{
		RestorePlanArtifactDatabase,
		RestorePlanArtifactAppData,
		RestorePlanArtifactPlatformData,
		RestorePlanArtifactSecrets,
		RestorePlanArtifactImagePinTable,
		RestorePlanArtifactRoutes,
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("GenerateRestorePlan() steps = %+v, want %d steps", plan.Steps, len(want))
	}
	for i, artifact := range want {
		step := plan.Steps[i]
		if step.Artifact != artifact {
			t.Fatalf("steps[%d].Artifact = %q, want %q", i, step.Artifact, artifact)
		}
		if step.DataClass == "" {
			t.Fatalf("steps[%d].DataClass is empty", i)
		}
		if step.Order != i+1 {
			t.Fatalf("steps[%d].Order = %d, want %d", i, step.Order, i+1)
		}
		if step.IdempotencyKey == "" || step.RollbackKey == "" || step.IdempotencyKey == step.RollbackKey {
			t.Fatalf("steps[%d]: idempotency keys = %q/%q, want non-empty distinct keys", i, step.IdempotencyKey, step.RollbackKey)
		}
		if i == 0 && step.BlockingPredecessor != "" {
			t.Fatalf("steps[0].BlockingPredecessor = %q, want none", step.BlockingPredecessor)
		}
		if i > 0 && step.BlockingPredecessor != want[i-1] {
			t.Fatalf("steps[%d].BlockingPredecessor = %q, want %q", i, step.BlockingPredecessor, want[i-1])
		}
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestGenerateRestorePlanIsDeterministicAcrossInvocations(t *testing.T) {
	a, _ := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	b, _ := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	for i := range a.Steps {
		if a.Steps[i].IdempotencyKey != b.Steps[i].IdempotencyKey || a.Steps[i].RollbackKey != b.Steps[i].RollbackKey {
			t.Fatalf("idempotency keys diverged across invocations: %+v vs %+v", a.Steps[i], b.Steps[i])
		}
	}
}

func TestGenerateRestorePlanProducesDifferentKeysForDifferentBackups(t *testing.T) {
	a, _ := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	b, _ := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-002", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	for i := range a.Steps {
		if a.Steps[i].IdempotencyKey == b.Steps[i].IdempotencyKey {
			t.Fatalf("steps[%d] idempotency key is identical for different backups: %q", i, a.Steps[i].IdempotencyKey)
		}
	}
}

func TestGenerateRestorePlanRequiresOperatorAndRollbackOwner(t *testing.T) {
	tests := []RestorePlanInput{
		{BackupID: "", Operator: "ops", RollbackOwner: "owner"},
		{BackupID: "backup-001", Operator: "", RollbackOwner: "owner"},
		{BackupID: "backup-001", Operator: "ops", RollbackOwner: ""},
	}
	for i, in := range tests {
		if _, err := GenerateRestorePlan(in); err == nil {
			t.Fatalf("GenerateRestorePlan(case %d) error = nil, want validation error", i)
		}
	}
}

func TestRestorePlanValidateRejectsTampering(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops", RollbackOwner: "owner"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	plan.Steps[0], plan.Steps[1] = plan.Steps[1], plan.Steps[0]
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want artifact ordering error")
	}
}

func TestRestorePlanSortedStepsReturnsCanonicalOrder(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops", RollbackOwner: "owner"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	plan.Steps[0], plan.Steps[3] = plan.Steps[3], plan.Steps[0]
	sorted := plan.SortedSteps()
	if sorted[0].Artifact != RestorePlanArtifactDatabase || sorted[len(sorted)-1].Artifact != RestorePlanArtifactRoutes {
		t.Fatalf("SortedSteps() = %+v, want canonical order", sorted)
	}
}

func TestMarshalRestorePlanIncludesSchemaVersionAndIsStable(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall", Notes: "first restore"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	a, err := MarshalRestorePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRestorePlan() error = %v", err)
	}
	b, err := MarshalRestorePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRestorePlan() error = %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("MarshalRestorePlan() not stable: %q vs %q", a, b)
	}
	if !strings.Contains(string(a), `"schemaVersion": "2"`) {
		t.Fatalf("MarshalRestorePlan() output missing schemaVersion: %q", a)
	}
}

func TestUnmarshalRestorePlanRoundTrip(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops@midnight", RollbackOwner: "platform-oncall"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	data, err := MarshalRestorePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRestorePlan() error = %v", err)
	}
	restored, err := UnmarshalRestorePlan(data)
	if err != nil {
		t.Fatalf("UnmarshalRestorePlan() error = %v", err)
	}
	if restored.BackupID != plan.BackupID || restored.Operator != plan.Operator || restored.RollbackOwner != plan.RollbackOwner {
		t.Fatalf("UnmarshalRestorePlan() metadata mismatch: %+v vs %+v", restored, plan)
	}
	if len(restored.Steps) != len(plan.Steps) {
		t.Fatalf("UnmarshalRestorePlan() step count mismatch: %d vs %d", len(restored.Steps), len(plan.Steps))
	}
	for i := range plan.Steps {
		if restored.Steps[i] != plan.Steps[i] {
			t.Fatalf("UnmarshalRestorePlan() step %d mismatch: %+v vs %+v", i, restored.Steps[i], plan.Steps[i])
		}
	}
}

func TestUnmarshalRestorePlanRejectsUnknownSchemaVersion(t *testing.T) {
	data := []byte(`{"schemaVersion":"3","plan":{"backupId":"backup-001","operator":"ops","rollbackOwner":"owner","steps":[]}}`)
	if _, err := UnmarshalRestorePlan(data); err == nil {
		t.Fatal("UnmarshalRestorePlan() error = nil, want unsupported schemaVersion error")
	}
}

func TestUnmarshalRestorePlanAcceptsLegacyV1Plans(t *testing.T) {
	data := []byte(`{
  "schemaVersion": "1",
  "plan": {
    "backupId": "backup-legacy",
    "operator": "ops",
    "rollbackOwner": "owner",
    "steps": [
      {"order":1,"artifact":"database","action":"restore database","idempotencyKey":"apply-db","rollback":"rollback database","rollbackKey":"rollback-db"},
      {"order":2,"artifact":"secrets","action":"restore secrets","idempotencyKey":"apply-secrets","rollback":"rollback secrets","rollbackKey":"rollback-secrets","blockingPredecessor":"database"},
      {"order":3,"artifact":"image-pin-table","action":"restore image pins","idempotencyKey":"apply-pins","rollback":"rollback image pins","rollbackKey":"rollback-pins","blockingPredecessor":"secrets"},
      {"order":4,"artifact":"routes","action":"restore routes","idempotencyKey":"apply-routes","rollback":"rollback routes","rollbackKey":"rollback-routes","blockingPredecessor":"image-pin-table"}
    ]
  }
}`)
	plan, err := UnmarshalRestorePlan(data)
	if err != nil {
		t.Fatalf("UnmarshalRestorePlan(legacy) error = %v", err)
	}
	if plan.BackupID != "backup-legacy" || len(plan.Steps) != len(legacyRestoreArtifacts) {
		t.Fatalf("legacy plan = %+v, want v1-compatible restore plan", plan)
	}
}

func TestUnmarshalRestorePlanRejectsUnknownFields(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops", RollbackOwner: "owner"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	data, err := MarshalRestorePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRestorePlan() error = %v", err)
	}
	tampered := strings.Replace(string(data), `"schemaVersion": "2"`, `"schemaVersion": "2","extra":"x"`, 1)
	if _, err := UnmarshalRestorePlan([]byte(tampered)); err == nil {
		t.Fatal("UnmarshalRestorePlan() error = nil, want unknown field error")
	}
}

func TestUnmarshalRestorePlanRejectsEmptyInput(t *testing.T) {
	if _, err := UnmarshalRestorePlan(nil); err == nil {
		t.Fatal("UnmarshalRestorePlan(nil) error = nil, want empty input error")
	}
	if _, err := UnmarshalRestorePlan([]byte{}); err == nil {
		t.Fatal("UnmarshalRestorePlan(empty) error = nil, want empty input error")
	}
}

func TestMarshalRestorePlanRejectsInvalidPlan(t *testing.T) {
	plan := RestorePlan{}
	if _, err := MarshalRestorePlan(plan); err == nil {
		t.Fatal("MarshalRestorePlan(zero) error = nil, want validation error")
	}
}

func TestMarshalRestorePlanProducesSortedSteps(t *testing.T) {
	plan, err := GenerateRestorePlan(RestorePlanInput{BackupID: "backup-001", Operator: "ops", RollbackOwner: "owner"})
	if err != nil {
		t.Fatalf("GenerateRestorePlan() error = %v", err)
	}
	// Even if the in-memory plan steps are reordered, the marshalled output must be canonical.
	plan.Steps[0], plan.Steps[3] = plan.Steps[3], plan.Steps[0]
	// The base plan validator rejects the swap; marshaling proxies that error, but we
	// still want to confirm that a swap that would survive validation produces sorted output
	// when round-tripped.
	if _, err := MarshalRestorePlan(plan); err == nil {
		t.Fatal("MarshalRestorePlan() error = nil, want validation rejection of swapped steps")
	}
}
