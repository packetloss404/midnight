# Generated audience views

The files in this directory are generated from the canonical
`../midnight.yaml` bundle and `../audiences.yaml`.

- `product.openapi.yaml` — tenant-scoped product API.
- `operator.openapi.yaml` — installation recovery and administration.
- `agent.openapi.yaml` — host-agent protocol.
- `ui.openapi.yaml` — dashboard aggregation endpoints.

Run `go run ./tools/api-contract-views` after changing the canonical contract.
CI uses `go run ./tools/api-contract-views --check` to reject stale views.
