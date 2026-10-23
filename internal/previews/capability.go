package previews

// LifecycleCapability is the server-owned truth for automatic pull-request
// preview environments. It is deliberately closed: event receipt is not the
// same thing as routing, environment creation, deployment, or cleanup.
type LifecycleCapability struct {
	State              string              `json:"state"`
	AutomaticLifecycle bool                `json:"automatic_lifecycle"`
	ReceivedEvent      string              `json:"received_event"`
	CurrentDisposition string              `json:"current_disposition"`
	RequestedActions   []string            `json:"requested_actions"`
	Blockers           []CapabilityBlocker `json:"blockers"`
}

type CapabilityBlocker struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CurrentLifecycleCapability reports the capabilities actually wired by the
// current server. The GitHub App subscribes to pull_request, but Router keeps
// those signed events as acknowledged no-ops until every durable isolation and
// cleanup seam below exists.
func CurrentLifecycleCapability() LifecycleCapability {
	return LifecycleCapabilityFor(false)
}

// LifecycleCapabilityFor reports whether the owning server has wired every
// durable lifecycle seam. Subscription to the webhook event alone never
// enables this claim.
func LifecycleCapabilityFor(enabled bool) LifecycleCapability {
	if enabled {
		return LifecycleCapability{
			State:              "enabled",
			AutomaticLifecycle: true,
			ReceivedEvent:      "pull_request",
			CurrentDisposition: "routed",
			RequestedActions: []string{
				"opened:create",
				"reopened:update",
				"synchronize:update",
				"closed:delete",
			},
			Blockers: []CapabilityBlocker{},
		}
	}
	return LifecycleCapability{
		State:              "setup_required",
		AutomaticLifecycle: false,
		ReceivedEvent:      "pull_request",
		CurrentDisposition: "accepted_but_not_routed",
		RequestedActions: []string{
			"opened:create",
			"reopened:update",
			"synchronize:update",
			"closed:delete",
		},
		Blockers: []CapabilityBlocker{
			{Code: "pull_request_router_missing", Detail: "Signed pull_request deliveries are acknowledged but intentionally produce no deploy target."},
			{Code: "durable_preview_mapping_missing", Detail: "No durable pull-request to project/environment/service mapping exists."},
			{Code: "preview_isolation_missing", Detail: "No preview-specific deploy boundary proves fork secret, cache, and quota isolation."},
			{Code: "preview_cleanup_missing", Detail: "No transactional close-event cleanup coordinator exists for deployments, routes, and environments."},
		},
	}
}
