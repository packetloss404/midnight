# Notifications Foundation

Notification planning lives in `internal/notifications`.

Supported channel contracts in this slice:

- Slack incoming webhook channels store only a sealed `webhook_secret_ref` and optional default channel label.
- SMTP channels require host, port, TLS, a valid `From` address, and paired username/password secret references when authentication is used.

Subscriptions bind a workspace channel to event types such as deployment success/failure and preview create/archive. Mute windows suppress delivery planning and emit a suppression audit action instead of a delivery action.

`BuildDeliveryPreview` validates channel/subscription compatibility and returns the provider, payload field set, audit actions, and stable failure code `NOTIFICATION_DELIVERY_FAILED`. It does not send Slack or SMTP traffic.

## Subscription Catalog And Test Send

`SubscriptionCatalog` exposes the local event catalog for deployment success/failure and preview create/archive subscriptions. Each entry includes display copy, description, and a sample title so UI/API callers can render choices without calling Slack, SMTP, or an external catalog service.

`BuildTestSendSample` validates the channel configuration and event type, then returns the subject/text/field payload that would be used for a test send. The function intentionally stops at payload construction; it does not resolve secrets, open sockets, or deliver messages.
