# OpenAPI v1

This directory contains the generated stable-v1 promotion candidate. The
canonical authoring source remains `../v1alpha/midnight.yaml`; the strict
machine gate currently reports `ready: true` for its explicitly promoted
operation inventory.

Inspect the deterministic graduation inventory:

```sh
go run ./tools/api-v1-graduation --format=json
```

Run the release-blocking form before promotion:

```sh
go run ./tools/api-v1-graduation --format=json --require-ready
```

The JSON report has schema version `midnight.api.v1-graduation.v1` and stable
check identifiers suitable for CI and release evidence. See
[GRADUATION.md](GRADUATION.md) for the promotion, versioning, and deprecation
policy. `midnight.yaml` is a generated promotion candidate containing only the
explicitly promoted operation inventory, and its freshness is checked in the
contract and release gates. It is not served at `/api/v1` until the promotion
procedure is complete.
