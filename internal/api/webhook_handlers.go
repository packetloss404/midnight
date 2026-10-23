package api

import "github.com/midnight/midnight/internal/webhook"

// WebhookRoutePrefix is the panel-locked path prefix the receiver
// dispatches under. Lane 5 wires this from
// internal/api/router.go::newRouterWithDataStack so every router built
// via NewRouter / NewRouterWithDataStack exposes the four inbound
// provider paths below.
const WebhookRoutePrefix = v1alphaPrefix + "/webhooks/inbound/"

// WebhookHandlerOptions bundles the dependencies the receiver needs.
// All fields are required when wiring real receivers; tests can pass a
// zero-value MemoryEndpointResolver / NoopDispatcher for spot checks.
type WebhookHandlerOptions struct {
	Receiver *webhook.Receiver
}

// RegisterWebhookHandlers registers the inbound webhook endpoints on
// the supplied mux. The handlers are panel-locked at:
//
//   - POST /api/v1alpha/webhooks/inbound/github/{endpoint_id}
//   - POST /api/v1alpha/webhooks/inbound/gitlab-generic/{endpoint_id}
//   - POST /api/v1alpha/webhooks/inbound/bitbucket/{endpoint_id}
//   - POST /api/v1alpha/webhooks/inbound/midnight-incoming/{endpoint_id}
//
// Callers MUST pass a non-nil receiver. A nil receiver causes
// RegisterWebhookHandlers to skip registration so the function is safe
// to invoke from a router that has not yet been wired with a real
// receiver.
func RegisterWebhookHandlers(mux handlerRegistrar, opts WebhookHandlerOptions) {
	if mux == nil || opts.Receiver == nil {
		return
	}
	mux.Handle(WebhookRoutePrefix+"github/", opts.Receiver.HandleInbound(webhook.ProviderGitHub))
	mux.Handle(WebhookRoutePrefix+"gitlab-generic/", opts.Receiver.HandleInbound(webhook.ProviderGitLab))
	mux.Handle(WebhookRoutePrefix+"bitbucket/", opts.Receiver.HandleInbound(webhook.ProviderBitbucket))
	mux.Handle(WebhookRoutePrefix+"midnight-incoming/", opts.Receiver.HandleInbound(webhook.ProviderMidnightIncoming))
}
