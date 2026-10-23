// Package webhook owns signed outbound webhook dispatch and the inbound
// receiver semantics (HMAC verification, replay window, dedupe).
//
// Legacy inbound compatibility uses the original v1 envelope:
//
//	"Outbound webhook signing: Midnight-Signature: t=<unix>,v1=<hex>,
//	 kid=<key_id>, replay window 300s, two active signing keys per
//	 endpoint with 30-day rotation. Canonical signing input =
//	 f'{timestamp}.{raw_body}'. Multi-v1 accepted during rotation
//	 overlap."
//
// New outbound deliveries use v2 signatures over
// f'{delivery_id}.{timestamp}.{raw_body}' so the delivery identity cannot be
// substituted independently of the payload. VerifyDeliverySignature accepts
// v1-only headers during migration but never downgrades a header containing v2.
//
// The package surfaces two primary types:
//
//   - Signer: produces a Midnight-Signature header from a body + key set.
//   - Receiver: verifies an inbound request, enforces the 300s replay
//     window, dedupes by (delivery_id, event_hash), and (on success)
//     hands the validated CloudEvents envelope to a downstream handler.
//
// The receiver is plumbed into the API mux from the
// internal/api/webhook_handlers.go RegisterWebhookHandlers helper. Lane
// 1 is expected to wire RegisterWebhookHandlers from internal/api/router.go
// in a follow-up change; until then the helper is exposed for tests.
package webhook
