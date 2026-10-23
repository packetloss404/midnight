package acme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/midnight/midnight/internal/router"
)

type IngressDiagnosticsReviewStatus string

const (
	IngressDiagnosticsHealthy     IngressDiagnosticsReviewStatus = "healthy"
	IngressDiagnosticsNeedsReview IngressDiagnosticsReviewStatus = "needs-review"
	IngressDiagnosticsBlocked     IngressDiagnosticsReviewStatus = "blocked"
)

type IngressDiagnosticsReviewInput struct {
	Domains          []DomainDiagnosticsSupportView
	DomainSummaries  []DomainDiagnosticSummary
	RoutePublication router.RoutePublicationSummary
}

type IngressDiagnosticsActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// IngressDiagnosticsReviewSummary is a support-safe reviewer view for ingress
// readiness. It only accepts redacted domain views and route publication counts.
type IngressDiagnosticsReviewSummary struct {
	Status             IngressDiagnosticsReviewStatus  `json:"status"`
	DomainCount        int                             `json:"domainCount"`
	DomainErrorCount   int                             `json:"domainErrorCount"`
	RouteStaleCount    int                             `json:"routeStaleCount"`
	RouteErrorCount    int                             `json:"routeErrorCount"`
	DomainActionCounts []IngressDiagnosticsActionCount `json:"domainActionCounts,omitempty"`
	RouteActionCounts  []IngressDiagnosticsActionCount `json:"routeActionCounts"`
	NextActions        []string                        `json:"nextActions"`
}

func NewIngressDiagnosticsReviewSummary(input IngressDiagnosticsReviewInput) IngressDiagnosticsReviewSummary {
	domains := append([]DomainDiagnosticsSupportView(nil), input.Domains...)
	sort.SliceStable(domains, func(i, j int) bool {
		return domains[i].DomainHash < domains[j].DomainHash
	})

	summary := IngressDiagnosticsReviewSummary{
		DomainCount:     len(domains),
		RouteStaleCount: input.RoutePublication.StaleCount,
		RouteErrorCount: input.RoutePublication.Errors,
		RouteActionCounts: []IngressDiagnosticsActionCount{
			{Action: string(router.RouteArtifactPrewriteActionError), Count: input.RoutePublication.Errors},
			{Action: string(router.RouteArtifactPrewriteActionSkip), Count: input.RoutePublication.Skips},
			{Action: string(router.RouteArtifactPrewriteActionWrite), Count: input.RoutePublication.Writes},
		},
	}

	blocked := input.RoutePublication.Errors > 0
	needsReview := input.RoutePublication.Writes > 0 || input.RoutePublication.StaleCount > 0
	nextActions := make(map[string]struct{})
	for _, action := range input.RoutePublication.NextActions {
		if action != "" {
			nextActions[action] = struct{}{}
		}
	}

	for _, domain := range domains {
		if domain.HasLastError {
			summary.DomainErrorCount++
			needsReview = true
		}
		if domain.CAA == DomainCAABlocked || domain.Ownership == DomainOwnershipFailed || domain.Routing == DomainRoutingFailed || domain.Challenge == DomainChallengeFailed {
			blocked = true
		}
		if domain.CAA == DomainCAAUnknown || domain.Ownership == DomainOwnershipUnknown || domain.Ownership == DomainOwnershipPending || domain.Routing == DomainRoutingUnknown || domain.Routing == DomainRoutingPending || domain.Challenge == DomainChallengeUnknown || domain.Challenge == DomainChallengePending {
			needsReview = true
		}
	}

	actionCounts := make(map[string]int)
	for _, domainSummary := range input.DomainSummaries {
		for _, action := range domainSummary.Actions {
			if action == "" {
				continue
			}
			actionCounts[string(action)]++
			switch action {
			case DomainDiagnosticActionFixCAA, DomainDiagnosticActionReplaceCertificate:
				blocked = true
			case DomainDiagnosticActionMonitor:
			default:
				needsReview = true
			}
			if nextAction := ingressDomainNextAction(action); nextAction != "" {
				nextActions[nextAction] = struct{}{}
			}
		}
	}
	summary.DomainActionCounts = sortedIngressActionCounts(actionCounts)

	if blocked {
		summary.Status = IngressDiagnosticsBlocked
	} else if needsReview {
		summary.Status = IngressDiagnosticsNeedsReview
	} else {
		summary.Status = IngressDiagnosticsHealthy
	}

	if len(nextActions) == 0 {
		nextActions["monitor ingress diagnostics"] = struct{}{}
	}
	summary.NextActions = sortedIngressNextActions(nextActions)
	return summary
}

func (summary IngressDiagnosticsReviewSummary) Validate() error {
	if !validIngressDiagnosticsReviewStatus(summary.Status) {
		return fmt.Errorf("ingress diagnostics review status %q is invalid", summary.Status)
	}
	if summary.DomainCount < 0 || summary.DomainErrorCount < 0 || summary.RouteStaleCount < 0 || summary.RouteErrorCount < 0 {
		return fmt.Errorf("ingress diagnostics review counts must not be negative")
	}
	if !ingressActionCountsSorted(summary.DomainActionCounts) || !ingressActionCountsSorted(summary.RouteActionCounts) {
		return fmt.Errorf("ingress diagnostics review action counts must be sorted")
	}
	if !sort.StringsAreSorted(summary.NextActions) {
		return fmt.Errorf("ingress diagnostics review next actions must be sorted")
	}
	return nil
}

func MarshalIngressDiagnosticsReviewSummaryJSON(summary IngressDiagnosticsReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func ingressDomainNextAction(action DomainDiagnosticAction) string {
	switch action {
	case DomainDiagnosticActionCollectMoreTelemetry:
		return "collect additional domain diagnostics telemetry"
	case DomainDiagnosticActionFixCAA:
		return "review CAA policy before issuing certificates"
	case DomainDiagnosticActionRefreshRouteTargets:
		return "refresh stale route targets before issuing certificates"
	case DomainDiagnosticActionRenewCertificate:
		return "renew certificates approaching expiry"
	case DomainDiagnosticActionReplaceCertificate:
		return "replace expired certificates before routing traffic"
	case DomainDiagnosticActionMonitor:
		return "monitor ingress diagnostics"
	default:
		return ""
	}
}

func sortedIngressActionCounts(counts map[string]int) []IngressDiagnosticsActionCount {
	actions := make([]string, 0, len(counts))
	for action := range counts {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	sorted := make([]IngressDiagnosticsActionCount, 0, len(actions))
	for _, action := range actions {
		sorted = append(sorted, IngressDiagnosticsActionCount{Action: action, Count: counts[action]})
	}
	return sorted
}

func sortedIngressNextActions(actions map[string]struct{}) []string {
	sorted := make([]string, 0, len(actions))
	for action := range actions {
		sorted = append(sorted, action)
	}
	sort.Strings(sorted)
	return sorted
}

func ingressActionCountsSorted(counts []IngressDiagnosticsActionCount) bool {
	return sort.SliceIsSorted(counts, func(i, j int) bool {
		return counts[i].Action < counts[j].Action
	})
}

func validIngressDiagnosticsReviewStatus(status IngressDiagnosticsReviewStatus) bool {
	switch status {
	case IngressDiagnosticsHealthy, IngressDiagnosticsNeedsReview, IngressDiagnosticsBlocked:
		return true
	default:
		return false
	}
}
