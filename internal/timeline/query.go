package timeline

import (
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Filter narrows a deployment timeline query.
type Filter struct {
	// DeploymentID, when non-empty, restricts results to a single deployment.
	// Prefer DeploymentIDs for new code; this field is preserved for callers
	// that still pass a single value. When both are set, DeploymentID is
	// treated as one entry of the union.
	DeploymentID string `json:"deploymentId,omitempty"`
	// DeploymentIDs narrows results to the named deployments. Empty matches
	// any deployment. Pairs naturally with cross-deployment queries.
	DeploymentIDs []string `json:"deploymentIds,omitempty"`
	// Types narrows results to the named event types. Empty matches any.
	Types []EventType `json:"types,omitempty"`
	// Kinds is an alias for Types kept for cross-package consistency with the
	// log filter shape. When both Types and Kinds are populated, the
	// effective filter is the union.
	Kinds []EventType `json:"kinds,omitempty"`
	// TenantIDs narrows results to one or more tenants.
	TenantIDs []string `json:"tenantIds,omitempty"`
	// RouteNames narrows results to one or more route names.
	RouteNames []string `json:"routeNames,omitempty"`
	// Hosts narrows results to one or more hosts.
	Hosts []string `json:"hosts,omitempty"`
	// Since restricts events to At >= Since.
	Since time.Time `json:"since,omitempty"`
	// Until restricts events to At <= Until.
	Until time.Time `json:"until,omitempty"`
	// FreeText, when non-empty, requires a case-insensitive match in Message.
	FreeText string `json:"freeText,omitempty"`
}

// effectiveDeployments returns the deduplicated union of DeploymentID and
// DeploymentIDs in declaration order. The result is empty when neither field
// constrains the deployment scope.
func (filter Filter) effectiveDeployments() []string {
	seen := make(map[string]struct{}, len(filter.DeploymentIDs)+1)
	out := make([]string, 0, len(filter.DeploymentIDs)+1)
	if strings.TrimSpace(filter.DeploymentID) != "" {
		seen[filter.DeploymentID] = struct{}{}
		out = append(out, filter.DeploymentID)
	}
	for _, id := range filter.DeploymentIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// effectiveKinds returns the union of Types and Kinds in declaration order
// (Types first, then any new Kinds). The result is empty when neither field
// constrains the event-type scope.
func (filter Filter) effectiveKinds() []EventType {
	seen := make(map[EventType]struct{}, len(filter.Types)+len(filter.Kinds))
	out := make([]EventType, 0, len(filter.Types)+len(filter.Kinds))
	for _, t := range filter.Types {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range filter.Kinds {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// QueryCursor identifies a position in a deployment timeline page.
type QueryCursor struct {
	// DeploymentID is the deployment the cursor was emitted from.
	DeploymentID string `json:"deploymentId"`
	// AfterSeq is exclusive: the next page should start at events with seq > AfterSeq.
	AfterSeq int64 `json:"afterSeq"`
}

// QueryResult is one page of deployment timeline output.
type QueryResult struct {
	Events     []Event     `json:"events"`
	NextCursor QueryCursor `json:"nextCursor,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
}

// Validate enforces filter invariants and returns a registered failure error.
func (filter Filter) Validate() error {
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Until.Before(filter.Since) {
		return failurecodes.New(failurecodes.TimelineQueryInvalid, "until must not precede since")
	}
	for _, name := range filter.RouteNames {
		if strings.TrimSpace(name) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "route filter must not be empty")
		}
	}
	for _, host := range filter.Hosts {
		if strings.TrimSpace(host) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "host filter must not be empty")
		}
	}
	for _, t := range filter.Types {
		if strings.TrimSpace(string(t)) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "type filter must not be empty")
		}
	}
	for _, t := range filter.Kinds {
		if strings.TrimSpace(string(t)) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "kind filter must not be empty")
		}
	}
	for _, id := range filter.TenantIDs {
		if strings.TrimSpace(id) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "tenant filter must not be empty")
		}
	}
	for _, id := range filter.DeploymentIDs {
		if strings.TrimSpace(id) == "" {
			return failurecodes.New(failurecodes.TimelineQueryInvalid, "deployment id filter must not be empty")
		}
	}
	return nil
}

// Match returns true when event satisfies the filter.
func (filter Filter) Match(event Event) bool {
	if deployments := filter.effectiveDeployments(); len(deployments) > 0 && !containsString(deployments, event.DeploymentID) {
		return false
	}
	if kinds := filter.effectiveKinds(); len(kinds) > 0 && !containsEventType(kinds, event.Type) {
		return false
	}
	if len(filter.TenantIDs) > 0 && !containsString(filter.TenantIDs, event.TenantID) {
		return false
	}
	if len(filter.RouteNames) > 0 && !containsString(filter.RouteNames, event.RouteName) {
		return false
	}
	if len(filter.Hosts) > 0 && !containsString(filter.Hosts, event.Host) {
		return false
	}
	if !filter.Since.IsZero() && event.At.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && event.At.After(filter.Until) {
		return false
	}
	if filter.FreeText != "" && !strings.Contains(strings.ToLower(event.Message), strings.ToLower(filter.FreeText)) {
		return false
	}
	return true
}

func containsEventType(haystack []EventType, needle EventType) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func sortEventsAsc(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].DeploymentID != events[j].DeploymentID {
			return events[i].DeploymentID < events[j].DeploymentID
		}
		return events[i].EventSeq < events[j].EventSeq
	})
}
