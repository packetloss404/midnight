package orailtypes

import (
	"errors"
	"testing"
)

func TestErrorEnvelopeWireFields(t *testing.T) {
	envelope := ErrorEnvelope{Code: FailureCode("INTERNAL_ERROR"), Message: "failed", RequestID: "req-1", DocsURL: "docs"}
	if envelope.Code != "INTERNAL_ERROR" || envelope.RequestID != "req-1" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
}

func TestNormalizePageLimit(t *testing.T) {
	got, clamped, err := NormalizePageLimit(0)
	if err != nil || got != DefaultPageLimit || clamped {
		t.Fatalf("NormalizePageLimit(0) = %d, %v, %v", got, clamped, err)
	}

	got, clamped, err = NormalizePageLimit(500)
	if err != nil || got != MaxPageLimit || !clamped {
		t.Fatalf("NormalizePageLimit(500) = %d, %v, %v", got, clamped, err)
	}

	if _, _, err := NormalizePageLimit(-1); !errors.Is(err, ErrInvalidCursorPage) {
		t.Fatalf("NormalizePageLimit(-1) error = %v, want ErrInvalidCursorPage", err)
	}
}

func TestCursorPageDTOValidateAcceptsNilOrBase64URLCursor(t *testing.T) {
	if err := (CursorPageDTO{}).Validate(); err != nil {
		t.Fatalf("Validate() nil cursor error = %v", err)
	}
	cursor := "abcDEF012-_"
	if err := (CursorPageDTO{NextCursor: &cursor}).Validate(); err != nil {
		t.Fatalf("Validate() base64url cursor error = %v", err)
	}
}

func TestCursorPageDTOValidateRejectsEmptyOrNonOpaqueCursor(t *testing.T) {
	for _, cursor := range []string{"", "has space", "abc="} {
		if err := (CursorPageDTO{NextCursor: &cursor}).Validate(); !errors.Is(err, ErrInvalidCursorPage) {
			t.Fatalf("Validate(%q) error = %v, want ErrInvalidCursorPage", cursor, err)
		}
	}
}

func TestCursorPageResponseValidateRequiresDataSlice(t *testing.T) {
	if err := (CursorPageResponse[string]{}).Validate(); !errors.Is(err, ErrInvalidCursorPage) {
		t.Fatalf("Validate() error = %v, want ErrInvalidCursorPage", err)
	}
	if err := (CursorPageResponse[string]{Data: []string{}, Page: CursorPageDTO{}}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestOperationValidate(t *testing.T) {
	if err := (Operation{ID: "op_1", Kind: "deploy", State: OperationQueued}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, operation := range []Operation{
		{State: OperationRunning},
		{ID: "op_1", State: OperationState("UNKNOWN")},
	} {
		if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("Validate(%#v) error = %v, want ErrInvalidOperation", operation, err)
		}
	}
}

func TestValidateDeploymentStateAcceptsRunningJob(t *testing.T) {
	for _, state := range []DeploymentState{
		DeploymentQueued,
		DeploymentBuilding,
		DeploymentDeploying,
		DeploymentRunningJob,
		DeploymentActive,
		DeploymentSuperseded,
		DeploymentFailed,
		DeploymentCanceling,
		DeploymentCanceled,
		DeploymentRemoving,
		DeploymentRemoved,
	} {
		if err := ValidateDeploymentState(state); err != nil {
			t.Fatalf("ValidateDeploymentState(%q) error = %v", state, err)
		}
	}
}

func TestValidateDeploymentStateRejectsUnknown(t *testing.T) {
	if err := ValidateDeploymentState(DeploymentState("UNKNOWN")); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("ValidateDeploymentState() error = %v, want ErrInvalidDeployment", err)
	}
}

func TestValidateDeploymentKindAndJobHelper(t *testing.T) {
	valid := map[DeploymentKind]bool{
		DeploymentKindWeb:            false,
		DeploymentKindStatic:         false,
		DeploymentKindCronInvocation: true,
		DeploymentKindPredeploy:      true,
		DeploymentKindOneOff:         true,
	}
	for kind, wantJob := range valid {
		if err := ValidateDeploymentKind(kind); err != nil {
			t.Fatalf("ValidateDeploymentKind(%q) error = %v", kind, err)
		}
		if got := kind.IsJob(); got != wantJob {
			t.Fatalf("%q IsJob() = %v, want %v", kind, got, wantJob)
		}
	}

	if err := ValidateDeploymentKind(DeploymentKind("cron")); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("ValidateDeploymentKind() error = %v, want ErrInvalidDeployment", err)
	}
}

func TestValidateOperationTransition(t *testing.T) {
	valid := [][2]OperationState{
		{OperationQueued, OperationRunning},
		{OperationQueued, OperationFailed},
		{OperationQueued, OperationCanceled},
		{OperationRunning, OperationSucceeded},
		{OperationRunning, OperationFailed},
		{OperationRunning, OperationCanceled},
		{OperationSucceeded, OperationSucceeded},
	}
	for _, transition := range valid {
		if err := ValidateOperationTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("ValidateOperationTransition(%q, %q) error = %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]OperationState{
		{OperationQueued, OperationSucceeded},
		{OperationSucceeded, OperationRunning},
		{OperationFailed, OperationRunning},
		{OperationCanceled, OperationRunning},
	}
	for _, transition := range invalid {
		if err := ValidateOperationTransition(transition[0], transition[1]); !errors.Is(err, ErrInvalidOperationTransition) {
			t.Fatalf("ValidateOperationTransition(%q, %q) error = %v, want ErrInvalidOperationTransition", transition[0], transition[1], err)
		}
	}
}
