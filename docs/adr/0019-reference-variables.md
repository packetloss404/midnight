# ADR-0019: Reference Variables (`${{ServiceName.VAR}}`)

- Status: accepted
- Date: 2026-05-17
- Owners: control-plane / variables track

## Context

Railway's most-loved variable feature is the ability to write a variable's
value as a template that references another variable. The reference is
resolved at deploy time so cross-service URLs and credentials propagate
automatically without operators having to retype them on every change.

Midnight stores variables in an encrypted vault (`internal/secrets/`)
that keeps plaintext out of every API response and only releases it via a
narrow injection token. Variables can be `plain`, `secret`, or `sealed`
(panel-locked). The router retains `/api/v1alpha/variables*` as an
installation-wide recovery-operator compatibility surface and exposes exact
human-session service/environment variable collection and item routes. The
per-service deploy executor builds a
`runtime.ScheduleRequest` and hands it to the runtime adapter that runs
the container.

Before this ADR, every variable's value was a literal byte slice that
went into the env map verbatim. Cross-service propagation required the
operator to type the same URL in two places and update both whenever the
underlying resource moved.

## Decision

We introduce a small, additive reference-variable substrate that
parses `${{...}}` expressions in stored variable templates and resolves
them into final string values at deploy time. The implementation lives
in `internal/varrefs/` and is wired into the deploy executor via a
narrow `deploy.VariableResolver` seam. The encrypted-at-rest contract
and the secrets-vault encryption are unchanged.

### Syntax

Three reference forms are recognised:

- `${{ServiceName.VAR_NAME}}` — pull `VAR_NAME` from another service in
  the same project + environment. `ServiceName` is the user-facing
  service name (resolved into the canonical id by the resolver).
- `${{shared.KEY}}` — pull `KEY` from a project-scoped "shared"
  variable group.
- `${{VAR}}` — pull `VAR` from the same service that owns the
  template (chaining permitted).

Additionally, a fixed set of `${{this.*}}` built-ins is auto-injected on
every service:

- `${{this.MIDNIGHT_PUBLIC_DOMAIN}}` — the canonical public domain for
  the service (empty until route promotion completes).
- `${{this.MIDNIGHT_PRIVATE_DOMAIN}}` — the in-cluster DNS name; defaults
  to `<service-id>.midnight.internal` until the private-domain plumbing
  track lands its own lookup.
- `${{this.MIDNIGHT_SERVICE_NAME}}` — the service id itself.

Identifiers are ASCII alphanumeric + `_` + `-`; the leading byte must not
be a digit. Whitespace inside an expression is rejected. Nested dots are
rejected.

### Resolution

- The **stored** value remains the template. Only the deploy-time
  rendered value reaches the runtime container.
- The resolver walks the template, recursively expanding every
  reference; cycles are detected via a visit stack and produce a typed
  `ErrReferenceCycle` carrying the full path.
- Missing targets produce a typed `ErrUnknownReference{Service,Name}`.
- Invalid syntax produces `ErrInvalidSyntax{Pos,Reason}`.

Configuration snapshots use the `midnight.dev/variable-version-refs/v2`
schema to bind every referenced variable to its exact encrypted version and
bind cross-service lookup to the service slug and display name observed when
the snapshot was created. Retries therefore do not consult mutable service
aliases. Legacy `v1` snapshots without alias bindings continue to resolve
same-service references but fail closed for cross-service references.

Production build and deploy workers append a redacted, hash-chained
`variable.decrypt` audit record before decrypted bytes cross the injection
boundary. If that audit append is unavailable or fails, resolution fails
closed. The record carries variable identity/version, scope, resolution
purpose, KEK fingerprint, a length bucket, and the exact
deployment/build/cron worker attempt; it never carries the variable name,
stored template, or plaintext.

The deploy operation calls the resolver in a new `resolve-variables`
step before the existing `pull-verify` step. On error the operation
fails with a typed reason code:

- `VARIABLE_REFERENCE_UNRESOLVED` for a missing target.
- `VARIABLE_REFERENCE_CYCLE` for a cycle.
- `RESOURCE_VALIDATION_FAILED` for a syntax error reaching the deploy
  path (POST `/variables` rejects syntax errors at write time, so this
  failure is reserved for stored templates whose dependency was deleted
  after they were stored).

Each deploy emits a redaction-safe snapshot of the resolved env map
attached to the `resolve-variables` step event — variable name plus
length bucket only, never plaintext.

### API extension

Both the recovery-operator write path and human
`POST|PATCH /api/v1alpha/services/{service_id}/environments/{environment_id}/variables*`
paths parse the supplied value at write time. Syntax errors are rejected eagerly
with `RESOURCE_VALIDATION_FAILED`. Recovery-operator compatibility retains its
structured offset detail; the human route returns redaction-safe
`{field: "value", reason: "variable.references.invalid"}` detail.

Human responses use a strict metadata-only schema and never contain the
write-only `value`. Human update/delete require a positive
`expected_version`, and all three human mutations require a singleton
`Idempotency-Key`. The server derives project/workspace ownership from the
exact route lineage, requires `variables.read`/`variables.write`, reauthorizes
exact replay, and keeps project automation credentials closed. Human-created
names use the portable `[A-Za-z_][A-Za-z0-9_]*` identifier subset and are
bounded to 255 bytes so accepted desired state is injectable through both
runtime environment and BuildKit secret boundaries. Saving desired variables
does not deploy or restart a service.

`VariableMeta` gains two additive fields:

- `value_kind`: `"literal"` for a plain template, `"reference"` when at
  least one `${{...}}` expression is present.
- `references`: an array of `{kind, service?, name}` tuples listing the
  parsed targets (names only — plaintext is still never surfaced).

### Storage

Migration `0010_variable_references.up.sql` adds an additive
`service_variables` table. The encrypted `value_ciphertext` continues to
hold the **template string** (not the resolved value); a new
`value_kind` column carries `literal` | `reference` and a `references`
JSONB column carries the parsed targets (names only). The migration is
purely additive (CREATE TABLE / CREATE INDEX with IF NOT EXISTS) — the
forward and backward migrations are reversible without data loss because
no existing column is touched.

## Why store templates rather than resolved values?

- Resolving at write-time would require eager re-resolution every time a
  referenced variable changes, which would silently mutate seemingly
  unrelated services and surprise operators.
- The resolved value can leak the topology of dependent services
  (`db.midnight.internal:5432`) that the consuming service may not be
  authorised to enumerate. Resolving at deploy time keeps the
  authorisation seam at the deploy operation.
- Sealed/secret variables already store ciphertext over the template; a
  resolved value would need a second encryption flow keyed by the
  consumer's session, which is out of scope for the MVP.

## Why no staged-changes / pre-apply review yet?

Railway's UI shows a staged changes drawer that batches variable
updates and lets operators preview before applying. That workflow
requires a per-actor "draft" lifecycle, RBAC for review approval, and a
diff UI; we intentionally defer it to a later track. The substrate
needed for it (template storage, parsed references, deploy-time
resolution) is in place — adding a staged-changes table on top is purely
additive.

## Why no auto-redeploy on variable change?

The MVP does not auto-redeploy when a referenced variable changes. The
next deploy picks up the new resolved values. Auto-redeploy needs a
project-wide dependency graph plus rate-limiting; it is a separate track.

## Consequences

- Operators can now write `${{cache.HOST}}` and have it follow the
  service's actual host across redeploys.
- The deploy operation now has one more step (`resolve-variables`) that
  may fail with two new typed codes. Clients should treat both as
  permanent until the offending variable is fixed.
- The runtime adapter contract gains an `Env map[string]string` field on
  `runtime.ScheduleRequest`; adapters MUST inject every key/value
  exactly as supplied.

## Future work

- Staged-changes / pre-apply review workflow.
- Auto-redeploy on variable change (rate-limited, opt-in).
- Human lifecycle for project/environment shared variables.
- A richer dashboard affordance for visualizing the full reference graph.

## Links

- Related panel decisions: encrypted-at-rest substrate (Sprint 9
  VAR-904/905), deploy executor (`internal/deploy/executor.go`).
- Related work items: this ADR.
- Related implementation PRs: see `internal/varrefs/`,
  `internal/deploy/references_test.go`, `migrations/0010_variable_references.up.sql`.
