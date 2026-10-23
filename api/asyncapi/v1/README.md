# AsyncAPI v1

`midnight.yaml` is the outbound webhook contract for structured Midnight
CloudEvents and built-in monitor JSON notifications. The CloudEvents type catalog in
`internal/cloudevents/catalog.go` is the source registry; contract tests reject
catalog/spec drift.

The server embeds the contract and serves it from
`GET /api/v1alpha/asyncapi.yaml`.

Control-plane deliveries use structured CloudEvents JSON; built-in monitor
deliveries use the separate JSON message documented in the same channel. New
outbound deliveries use a v2 HMAC over
`{delivery_id}.{timestamp}.{raw_body}`; v1 body-only verification is retained
for inbound migration compatibility. The complete envelope is documented in
`agile/engineering/contracts/WEBHOOK_AND_EVENT_CONTRACT.md`.
