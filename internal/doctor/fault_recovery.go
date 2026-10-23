package doctor

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/midnight/midnight/internal/failurecodes"
)

// FaultRecoveryHint is a JSON-friendly mapping from doctor check ID to a
// registered failure code and a short operator recovery hint.
type FaultRecoveryHint struct {
	CheckID     string            `json:"check_id"`
	FailureCode failurecodes.Code `json:"failure_code"`
	DocsURL     string            `json:"docs_url"`
	Hint        string            `json:"hint"`
	Severity    string            `json:"severity"`
}

// faultRecoveryRegistry returns the static doctor-fault → recovery hint map
// used by support-bundle wiring. Every code referenced here MUST be registered
// in internal/failurecodes (and documented in docs/error-codes.md).
func faultRecoveryRegistry() map[string]FaultRecoveryHint {
	return map[string]FaultRecoveryHint{
		"DOC-DISK-001": {
			CheckID:     "DOC-DISK-001",
			FailureCode: failurecodes.DiskPressureCritical,
			DocsURL:     "docs/doctor/checks.md#doc-disk-001",
			Hint:        "free space on the data disk or grow the volume; restart workers if rejecting writes",
			Severity:    "warn",
		},
		"DOC-MEMORY-001": {
			CheckID:     "DOC-MEMORY-001",
			FailureCode: failurecodes.MemoryHeadroomLow,
			DocsURL:     "docs/doctor/checks.md#doc-memory-001",
			Hint:        "increase host memory or reduce concurrent build/registry workers",
			Severity:    "warn",
		},
		"DOC-TIMESYNC-001": {
			CheckID:     "DOC-TIMESYNC-001",
			FailureCode: failurecodes.TimeSyncDrift,
			DocsURL:     "docs/doctor/checks.md#doc-timesync-001",
			Hint:        "ensure chrony or systemd-timesyncd is synchronizing; ACME issuance is blocked above the critical threshold",
			Severity:    "warn",
		},
	}
}

// RecoveryHintForCheck returns the registered recovery hint for a doctor check
// ID, or false if the check has no mapped failure code yet.
func RecoveryHintForCheck(checkID string) (FaultRecoveryHint, bool) {
	hint, ok := faultRecoveryRegistry()[checkID]
	return hint, ok
}

// ValidateFaultRecoveryRegistry rejects any registry entry whose failure code
// is not registered. Tests use this to keep the doctor catalog and the
// failure-code registry in lockstep.
func ValidateFaultRecoveryRegistry() error {
	for checkID, hint := range faultRecoveryRegistry() {
		if hint.CheckID != checkID {
			return fmt.Errorf("fault recovery registry key %q does not match hint check_id %q", checkID, hint.CheckID)
		}
		if !failurecodes.IsRegistered(hint.FailureCode) {
			return fmt.Errorf("fault recovery registry hint %q maps to unregistered failure code %q", checkID, hint.FailureCode)
		}
		if hint.DocsURL == "" || hint.Hint == "" {
			return fmt.Errorf("fault recovery registry hint %q must populate docs_url and hint", checkID)
		}
	}
	return nil
}

// MarshalFaultRecoveryRegistryJSON emits the deterministic JSON view of the
// registry. Entries are sorted by check ID so the output is reproducible.
func MarshalFaultRecoveryRegistryJSON() ([]byte, error) {
	registry := faultRecoveryRegistry()
	keys := make([]string, 0, len(registry))
	for key := range registry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hints := make([]FaultRecoveryHint, 0, len(keys))
	for _, key := range keys {
		hints = append(hints, registry[key])
	}
	return json.Marshal(struct {
		Hints []FaultRecoveryHint `json:"hints"`
	}{Hints: hints})
}
