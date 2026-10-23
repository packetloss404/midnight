package acme

import (
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/timeline"
)

type RoutePublicationEvidenceStatus string

const (
	RoutePublicationEvidenceReady       RoutePublicationEvidenceStatus = "ready"
	RoutePublicationEvidenceNeedsReview RoutePublicationEvidenceStatus = "needs-review"
	RoutePublicationEvidenceBlocked     RoutePublicationEvidenceStatus = "blocked"
)

type RoutePublicationEvidenceInput struct {
	PrewritePlan    router.RouteArtifactPrewritePlan
	Publication     router.RoutePublicationSummary
	IngressReview   IngressDiagnosticsReviewSummary
	HeartbeatReview timeline.DeploymentHeartbeatGapReview
}

// RoutePublicationEvidencePacket is a deterministic, JSON-friendly support
// packet for route publication decisions. It intentionally excludes route names,
// hosts, service URLs, rendered configs, log content, and filesystem paths.
type RoutePublicationEvidencePacket struct {
	Status       RoutePublicationEvidenceStatus        `json:"status"`
	BytesToWrite int                                   `json:"bytesToWrite"`
	Prewrite     RoutePublicationPrewriteEvidence      `json:"prewrite"`
	Publication  router.RoutePublicationSummary        `json:"publication"`
	Ingress      IngressDiagnosticsReviewSummary       `json:"ingress"`
	Heartbeat    timeline.DeploymentHeartbeatGapReview `json:"heartbeat"`
	Issues       []RoutePublicationEvidenceIssue       `json:"issues,omitempty"`
	NextActions  []string                              `json:"nextActions"`
}

type RoutePublicationPrewriteEvidence struct {
	Writes       int                                    `json:"writes"`
	Skips        int                                    `json:"skips"`
	Errors       int                                    `json:"errors"`
	BytesToWrite int                                    `json:"bytesToWrite"`
	Items        []RoutePublicationPrewriteItemEvidence `json:"items,omitempty"`
}

type RoutePublicationPrewriteItemEvidence struct {
	Format       string                             `json:"format"`
	Hash         string                             `json:"hash"`
	Action       router.RouteArtifactPrewriteAction `json:"action"`
	BytesToWrite int                                `json:"bytesToWrite"`
	Diagnostics  []string                           `json:"diagnostics,omitempty"`
}

type RoutePublicationEvidenceIssue struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Count  int    `json:"count"`
}

func NewRoutePublicationEvidencePacket(input RoutePublicationEvidenceInput) RoutePublicationEvidencePacket {
	packet := RoutePublicationEvidencePacket{
		Status:       routePublicationEvidenceStatus(input),
		BytesToWrite: input.PrewritePlan.BytesToWrite,
		Prewrite: RoutePublicationPrewriteEvidence{
			Writes:       input.PrewritePlan.Writes,
			Skips:        input.PrewritePlan.Skips,
			Errors:       input.PrewritePlan.Errors,
			BytesToWrite: input.PrewritePlan.BytesToWrite,
		},
		Publication: input.Publication,
		Ingress:     input.IngressReview,
		Heartbeat:   input.HeartbeatReview,
	}
	if input.Publication.TotalBytes > packet.BytesToWrite {
		packet.BytesToWrite = input.Publication.TotalBytes
	}

	issues := make(map[string]RoutePublicationEvidenceIssue)
	for _, item := range input.PrewritePlan.Items {
		diagnostics := append([]string(nil), item.Diagnostics...)
		sort.Strings(diagnostics)
		packet.Prewrite.Items = append(packet.Prewrite.Items, RoutePublicationPrewriteItemEvidence{
			Format:       item.Format,
			Hash:         item.Hash,
			Action:       item.Action,
			BytesToWrite: item.BytesToWrite,
			Diagnostics:  diagnostics,
		})
		for _, diagnostic := range diagnostics {
			if code := supportSafeDiagnosticCode(diagnostic); code != "" {
				addRoutePublicationEvidenceIssue(issues, "prewrite", code, 1)
			}
		}
	}
	sort.SliceStable(packet.Prewrite.Items, func(i, j int) bool {
		left := packet.Prewrite.Items[i]
		right := packet.Prewrite.Items[j]
		if left.Format != right.Format {
			return left.Format < right.Format
		}
		if left.Hash != right.Hash {
			return left.Hash < right.Hash
		}
		if left.Action != right.Action {
			return left.Action < right.Action
		}
		return left.BytesToWrite < right.BytesToWrite
	})

	if input.PrewritePlan.Errors > 0 {
		addRoutePublicationEvidenceIssue(issues, "prewrite", "route_prewrite_errors", input.PrewritePlan.Errors)
	}
	if input.Publication.StaleCount > 0 {
		addRoutePublicationEvidenceIssue(issues, "publication", "route_publication_stale_artifacts", input.Publication.StaleCount)
	}
	if input.Publication.Errors > 0 {
		addRoutePublicationEvidenceIssue(issues, "publication", "route_publication_errors", input.Publication.Errors)
	}
	if input.IngressReview.Status == IngressDiagnosticsBlocked {
		addRoutePublicationEvidenceIssue(issues, "ingress", "ingress_diagnostics_blocked", 1)
	} else if input.IngressReview.Status == IngressDiagnosticsNeedsReview {
		addRoutePublicationEvidenceIssue(issues, "ingress", "ingress_diagnostics_needs_review", 1)
	}
	if input.IngressReview.DomainErrorCount > 0 {
		addRoutePublicationEvidenceIssue(issues, "ingress", "ingress_domain_errors", input.IngressReview.DomainErrorCount)
	}
	if input.IngressReview.RouteErrorCount > 0 {
		addRoutePublicationEvidenceIssue(issues, "ingress", "ingress_route_errors", input.IngressReview.RouteErrorCount)
	}
	if input.IngressReview.RouteStaleCount > 0 {
		addRoutePublicationEvidenceIssue(issues, "ingress", "ingress_route_stale", input.IngressReview.RouteStaleCount)
	}
	for _, code := range input.HeartbeatReview.IssueCodes {
		if code != "" {
			addRoutePublicationEvidenceIssue(issues, "heartbeat", string(code), 1)
		}
	}
	packet.Issues = sortedRoutePublicationEvidenceIssues(issues)

	nextActions := make(map[string]struct{})
	for _, action := range input.Publication.NextActions {
		addRoutePublicationEvidenceAction(nextActions, action)
	}
	for _, action := range input.IngressReview.NextActions {
		addRoutePublicationEvidenceAction(nextActions, action)
	}
	if input.HeartbeatReview.Status == timeline.DeploymentHeartbeatGapWarning || input.HeartbeatReview.Status == timeline.DeploymentHeartbeatGapStuck || len(input.HeartbeatReview.IssueCodes) > 0 {
		addRoutePublicationEvidenceAction(nextActions, "review deployment heartbeat telemetry")
	}
	if len(nextActions) == 0 {
		addRoutePublicationEvidenceAction(nextActions, "route publication evidence is ready")
	}
	packet.NextActions = sortedRoutePublicationEvidenceActions(nextActions)

	return packet
}

func routePublicationEvidenceStatus(input RoutePublicationEvidenceInput) RoutePublicationEvidenceStatus {
	if input.PrewritePlan.Errors > 0 || input.Publication.Errors > 0 || input.IngressReview.Status == IngressDiagnosticsBlocked || input.HeartbeatReview.Status == timeline.DeploymentHeartbeatGapStuck {
		return RoutePublicationEvidenceBlocked
	}
	if input.PrewritePlan.Writes > 0 || input.Publication.Writes > 0 || input.Publication.StaleCount > 0 || input.IngressReview.Status == IngressDiagnosticsNeedsReview || input.HeartbeatReview.Status == timeline.DeploymentHeartbeatGapWarning || len(input.HeartbeatReview.IssueCodes) > 0 {
		return RoutePublicationEvidenceNeedsReview
	}
	return RoutePublicationEvidenceReady
}

func supportSafeDiagnosticCode(diagnostic string) string {
	const prefix = "code="
	start := strings.Index(diagnostic, prefix)
	if start < 0 {
		return ""
	}
	code := diagnostic[start+len(prefix):]
	if end := strings.IndexByte(code, ' '); end >= 0 {
		code = code[:end]
	}
	return strings.TrimSpace(code)
}

func addRoutePublicationEvidenceIssue(issues map[string]RoutePublicationEvidenceIssue, source, code string, count int) {
	if source == "" || code == "" || count <= 0 {
		return
	}
	key := source + "\x00" + code
	issue := issues[key]
	issue.Source = source
	issue.Code = code
	issue.Count += count
	issues[key] = issue
}

func sortedRoutePublicationEvidenceIssues(issues map[string]RoutePublicationEvidenceIssue) []RoutePublicationEvidenceIssue {
	sorted := make([]RoutePublicationEvidenceIssue, 0, len(issues))
	for _, issue := range issues {
		sorted = append(sorted, issue)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Source != sorted[j].Source {
			return sorted[i].Source < sorted[j].Source
		}
		return sorted[i].Code < sorted[j].Code
	})
	return sorted
}

func addRoutePublicationEvidenceAction(actions map[string]struct{}, action string) {
	if action != "" {
		actions[action] = struct{}{}
	}
}

func sortedRoutePublicationEvidenceActions(actions map[string]struct{}) []string {
	sorted := make([]string, 0, len(actions))
	for action := range actions {
		sorted = append(sorted, action)
	}
	sort.Strings(sorted)
	return sorted
}
