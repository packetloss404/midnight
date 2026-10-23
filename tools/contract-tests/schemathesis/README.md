# Stateful API contract harness

This directory retains the familiar Schemathesis discovery path, but the
executable is a dependency-free Go state machine. It runs the real in-process
HTTP router and memory operation/idempotency stores; it never binds a socket or
requires Python, Docker, Postgres, or a running Midnight installation.

The deterministic scenarios cover operation creation, lifecycle transitions,
terminal-state rejection, exact idempotency replay, different-body conflict,
event ordering, and lifecycle timestamps:

```sh
go run ./tools/contract-tests/schemathesis
go test ./tools/contract-tests/schemathesis -count=1
```

Use `-seeds` and `-steps` to change the bounded exploration size. A failing
message includes the reproducible integer seed.
