package doctor

import (
	"encoding/json"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestRecoveryHintForCheck(t *testing.T) {
	t.Parallel()

	hint, ok := RecoveryHintForCheck("DOC-DISK-001")
	if !ok {
		t.Fatal("RecoveryHintForCheck() ok = false, want hint for DOC-DISK-001")
	}
	if hint.FailureCode != failurecodes.DiskPressureCritical {
		t.Fatalf("FailureCode = %q, want %q", hint.FailureCode, failurecodes.DiskPressureCritical)
	}
	if hint.Hint == "" || hint.DocsURL == "" {
		t.Fatalf("hint = %+v, want populated hint and docs_url", hint)
	}

	if _, ok := RecoveryHintForCheck("DOC-NOT-REGISTERED"); ok {
		t.Fatal("RecoveryHintForCheck() ok = true, want false for unknown check")
	}
}

func TestValidateFaultRecoveryRegistry(t *testing.T) {
	t.Parallel()

	if err := ValidateFaultRecoveryRegistry(); err != nil {
		t.Fatalf("ValidateFaultRecoveryRegistry() error = %v", err)
	}
}

func TestMarshalFaultRecoveryRegistryJSON(t *testing.T) {
	t.Parallel()

	data, err := MarshalFaultRecoveryRegistryJSON()
	if err != nil {
		t.Fatalf("MarshalFaultRecoveryRegistryJSON() error = %v", err)
	}
	var decoded struct {
		Hints []FaultRecoveryHint `json:"hints"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(registry) error = %v", err)
	}
	if len(decoded.Hints) == 0 {
		t.Fatalf("Hints = %+v, want at least one entry", decoded.Hints)
	}
}
