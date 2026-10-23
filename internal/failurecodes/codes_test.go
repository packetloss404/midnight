package failurecodes

import (
	"net/http"
	"strings"
	"testing"
)

func TestRegistryRecognizesInitialCodes(t *testing.T) {
	if !IsRegistered(AuthTokenHeaderInvalid) {
		t.Fatalf("expected %s to be registered", AuthTokenHeaderInvalid)
	}
	if !IsRegistered(NotImplemented) || !IsRegistered(OperationNotFound) || !IsRegistered(OperationInvalidTransition) {
		t.Fatal("expected HTTP placeholder codes to be registered")
	}
	if !IsRegistered(CapabilityUnavailable) || RecoveryHint(CapabilityUnavailable) == "" {
		t.Fatal("expected stable capability-unavailable code and recovery guidance")
	}
	if IsRegistered(Code("NOT_REGISTERED")) {
		t.Fatal("unexpected unregistered code to be registered")
	}
}

func TestDocsURLFallsBackForUnknownCode(t *testing.T) {
	got := DocsURL(Code("NOT_REGISTERED"))
	want := "https://midnight.dev/docs/errors#INTERNAL_ERROR"
	if got != want {
		t.Fatalf("DocsURL() = %q, want %q", got, want)
	}
}

func TestRegistryRecognizesS06Codes(t *testing.T) {
	for _, code := range []Code{
		ACMECAAForbidden,
		ACMERateBudgetExhausted,
		ACMERestoreBudgetExhausted,
		ACMEHTTP01Unreachable,
		ACMEDNSMismatch,
		ACMERateLimited,
		ACMEOrderFailedPermanent,
		ACMEOrderFailedRetryable,
		ACMECertificateExpired,
		ACMECertificateExpiringCritical,
		ACMECertificateExpiringWarn,
		RestoreACMEThrottled,
		DNSPropagationTimeout,
		IngressPort80Unreachable,
		IngressReloadThrash,
		RouteReservedPath,
		WildcardRenewalFailed,
		LogQueryInvalid,
		TimelineQueryInvalid,
		RedactionRuleInvalid,
	} {
		if !IsRegistered(code) {
			t.Fatalf("expected %s to be registered", code)
		}
		if RecoveryHint(code) == "" {
			t.Fatalf("expected non-empty recovery hint for %s", code)
		}
	}
}

func TestRecoveryHintEmptyForUnknown(t *testing.T) {
	if RecoveryHint(Code("NOT_REGISTERED")) != "" {
		t.Fatal("expected empty recovery hint for unknown code")
	}
}

func TestAllReturnsDeterministicOrder(t *testing.T) {
	got := All()
	if len(got) == 0 {
		t.Fatal("expected non-empty registered code list")
	}
	if got[0] != InternalError {
		t.Fatalf("expected first code to be InternalError, got %s", got[0])
	}
	again := All()
	if len(again) != len(got) {
		t.Fatalf("All length drifted: %d vs %d", len(got), len(again))
	}
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("All order drifted at %d: %s vs %s", i, got[i], again[i])
		}
	}
}

func TestEveryRegisteredCodeHasRecoveryHint(t *testing.T) {
	for code := range registered {
		if RecoveryHint(code) == "" {
			t.Errorf("registered code %s has no recovery hint", code)
		}
	}
}

func TestAPIProblemTypesAreClosedDeterministicAndSelfConsistent(t *testing.T) {
	definitions := APIProblemTypes()
	if len(definitions) == 0 {
		t.Fatal("HTTP problem registry is empty")
	}
	seen := make(map[Code]bool, len(definitions))
	for _, definition := range definitions {
		if seen[definition.Code] {
			t.Fatalf("duplicate HTTP problem code %s", definition.Code)
		}
		seen[definition.Code] = true
		if !IsRegistered(definition.Code) {
			t.Errorf("HTTP problem code %s is absent from failure registry", definition.Code)
		}
		if definition.Type != DocsURL(definition.Code) || !strings.HasPrefix(definition.Type, "https://") {
			t.Errorf("HTTP problem %s has non-canonical type %q", definition.Code, definition.Type)
		}
		if strings.TrimSpace(definition.Title) == "" {
			t.Errorf("HTTP problem %s has empty title", definition.Code)
		}
		if !IsPermittedProblemStatus(definition.Code, definition.Status) {
			t.Errorf("HTTP problem %s recommended status %d is not permitted", definition.Code, definition.Status)
		}
		for index, status := range definition.Statuses {
			if status < http.StatusBadRequest || status > 599 {
				t.Errorf("HTTP problem %s has invalid status %d", definition.Code, status)
			}
			if index > 0 && definition.Statuses[index-1] >= status {
				t.Errorf("HTTP problem %s statuses are not strictly increasing: %v", definition.Code, definition.Statuses)
			}
		}
	}

	copy := APIProblemTypes()
	copy[0].Statuses[0] = http.StatusTeapot
	again, ok := LookupProblemType(definitions[0].Code)
	if !ok || again.Statuses[0] == http.StatusTeapot {
		t.Fatal("HTTP problem registry leaked mutable status storage")
	}
}

func TestProblemStatusRejectsUnregisteredPair(t *testing.T) {
	if IsPermittedProblemStatus(ResourceNotFound, http.StatusConflict) {
		t.Fatal("RESOURCE_NOT_FOUND unexpectedly permits 409")
	}
	if IsPermittedProblemStatus(Code("NOT_REGISTERED"), http.StatusInternalServerError) {
		t.Fatal("unregistered code/status pair accepted")
	}
}
