package acme

import "github.com/midnight/midnight/internal/router"

type DomainDiagnosticAction string

const (
	DomainDiagnosticActionMonitor              DomainDiagnosticAction = "monitor"
	DomainDiagnosticActionFixCAA               DomainDiagnosticAction = "fix-caa"
	DomainDiagnosticActionRefreshRouteTargets  DomainDiagnosticAction = "refresh-route-targets"
	DomainDiagnosticActionRenewCertificate     DomainDiagnosticAction = "renew-certificate"
	DomainDiagnosticActionReplaceCertificate   DomainDiagnosticAction = "replace-certificate"
	DomainDiagnosticActionCollectMoreTelemetry DomainDiagnosticAction = "collect-more-telemetry"
)

type DomainDiagnosticInputs struct {
	CAA            CAAPreflightResult
	RouteFreshness router.RouteTargetFreshness
	CertExpiry     CertificateExpiryStatus
}

type DomainDiagnosticSummary struct {
	CAA            DomainCAAStatus                   `json:"caa"`
	RouteFreshness router.RouteTargetFreshnessStatus `json:"routeFreshness"`
	CertExpiry     CertificateExpiryStatus           `json:"certExpiry"`
	Actions        []DomainDiagnosticAction          `json:"actions"`
}

func MergeDomainDiagnostics(inputs DomainDiagnosticInputs) DomainDiagnosticSummary {
	caa := inputs.CAA.Normalized().Status
	routeFreshness := inputs.RouteFreshness.Status
	if routeFreshness == "" {
		routeFreshness = router.RouteTargetFreshnessUnknown
	}
	certExpiry := inputs.CertExpiry
	if certExpiry == "" {
		certExpiry = CertificateExpiryOK
	}

	actions := make([]DomainDiagnosticAction, 0, 4)
	if caa == DomainCAABlocked {
		actions = append(actions, DomainDiagnosticActionFixCAA)
	}
	if routeFreshness == router.RouteTargetFreshnessStale {
		actions = append(actions, DomainDiagnosticActionRefreshRouteTargets)
	}
	if routeFreshness == router.RouteTargetFreshnessUnknown || caa == DomainCAAUnknown {
		actions = append(actions, DomainDiagnosticActionCollectMoreTelemetry)
	}
	if certExpiry == CertificateExpiryExpired {
		actions = append(actions, DomainDiagnosticActionReplaceCertificate)
	} else if certExpiry == CertificateExpiryRenewSoon {
		actions = append(actions, DomainDiagnosticActionRenewCertificate)
	}
	if len(actions) == 0 {
		actions = append(actions, DomainDiagnosticActionMonitor)
	}

	return DomainDiagnosticSummary{
		CAA:            caa,
		RouteFreshness: routeFreshness,
		CertExpiry:     certExpiry,
		Actions:        actions,
	}
}
