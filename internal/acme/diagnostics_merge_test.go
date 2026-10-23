package acme

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/router"
)

func TestMergeDomainDiagnosticsReturnsSupportSafeActions(t *testing.T) {
	summary := MergeDomainDiagnostics(DomainDiagnosticInputs{
		CAA: CAAPreflightResult{
			Domain:    "app.example.test",
			Status:    DomainCAABlocked,
			LastError: "CAA for app.example.test blocks issuance",
		},
		RouteFreshness: router.RouteTargetFreshness{Status: router.RouteTargetFreshnessStale},
		CertExpiry:     CertificateExpiryExpired,
	})

	wantActions := []DomainDiagnosticAction{
		DomainDiagnosticActionFixCAA,
		DomainDiagnosticActionRefreshRouteTargets,
		DomainDiagnosticActionReplaceCertificate,
	}
	if !sameActions(summary.Actions, wantActions) {
		t.Fatalf("actions = %#v, want %#v", summary.Actions, wantActions)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(data), "example.test") || strings.Contains(string(data), "blocks issuance") {
		t.Fatalf("summary leaked support-unsafe values: %s", data)
	}
}

func TestMergeDomainDiagnosticsDefaultsAndMonitorAction(t *testing.T) {
	summary := MergeDomainDiagnostics(DomainDiagnosticInputs{
		CAA:            CAAPreflightResult{Status: DomainCAAAllowed},
		RouteFreshness: router.RouteTargetFreshness{Status: router.RouteTargetFreshnessFresh},
		CertExpiry:     CertificateExpiryOK,
	})

	if summary.CAA != DomainCAAAllowed || summary.RouteFreshness != router.RouteTargetFreshnessFresh || summary.CertExpiry != CertificateExpiryOK {
		t.Fatalf("unexpected summary statuses: %#v", summary)
	}
	if !sameActions(summary.Actions, []DomainDiagnosticAction{DomainDiagnosticActionMonitor}) {
		t.Fatalf("actions = %#v, want monitor", summary.Actions)
	}
}

func TestMergeDomainDiagnosticsRequestsTelemetryForUnknownInputs(t *testing.T) {
	summary := MergeDomainDiagnostics(DomainDiagnosticInputs{})
	if summary.CAA != DomainCAAUnknown || summary.RouteFreshness != router.RouteTargetFreshnessUnknown || summary.CertExpiry != CertificateExpiryOK {
		t.Fatalf("unexpected default statuses: %#v", summary)
	}
	if !sameActions(summary.Actions, []DomainDiagnosticAction{DomainDiagnosticActionCollectMoreTelemetry}) {
		t.Fatalf("actions = %#v, want collect-more-telemetry", summary.Actions)
	}
}

func sameActions(got, want []DomainDiagnosticAction) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
