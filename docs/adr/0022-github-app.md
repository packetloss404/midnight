# ADR-0022: GitHub App + auto-deploy on push

- Status: superseded in part by ADR 0038 and migration 0049
- Date: 2026-05-17
- Deciders: Track A (GitHub App + auto-deploy on push)
- Related: ADR 0017 (panel decisions), ADR 0018 (deploy intake)

## Context

> Historical note: the App/HMAC/push-routing rationale remains useful, but
> `service.spec.github`, App-global product discovery, accepted-but-ignored
> CI-wait input, broad installation tokens, and synchronous-delivery
> reliability claims below are no longer the production contract. ADR 0038,
> migration 0049, and `docs/github-app.md` are authoritative.

Midnight needs to let an operator install a per-instance GitHub App on
a repo, link it to a Midnight service, and auto-deploy every push to
the configured branch. Webhook receive, signature verify, dedupe, and
audit chain wiring already exist (`internal/webhook/`). The build
pipeline already accepts a `source` block in deployment intent
(`internal/build/`). What's missing: a GitHub-specific webhook
dispatcher that turns a `push` into a deploy, the App-JWT plumbing
that mints clone tokens for private repos, the link-flow REST surface,
and the JSONB index that makes routing O(log n).

## Decision

### App vs OAuth

We ship a **GitHub App**, not an OAuth App. Apps grant scoped, fine-
grained, short-lived installation tokens; OAuth grants user-level
PATs. Per panel decision GH-1005, Midnight's minimum-trust footprint
is App-only.

Two install postures coexist:

* **Operator-owned shared App** for self-hosted Midnight: one App per
  Midnight instance, installed on the operator's repos.
* **User-provided App credentials** for users who already manage an
  App they want Midnight to reuse (`MIDNIGHT_GITHUB_*` env override).

There is no central Anthropic-owned / Midnight-owned App.

### service.spec.github vs separate table

We persist the per-service link as a JSONB blob under
`service.spec.github`, not a dedicated `service_github_links` table.
Justification:

* Link cardinality is ~1 per service. The `services.spec` JSONB column
  already exists (migration 0001) and the deploy executor already
  reads from it.
* Adding a separate table forces a join on every deploy intake; a
  partial expression index on `(spec->'github'->>'repo', spec->'github'->>'branch')`
  gives the same lookup latency without the join.
* A dedicated table makes sense when links acquire their own
  lifecycle (multiple repos per service, scheduled deploys, etc.).
  Not yet.

When link cardinality justifies it — typically when a single service
maps to multiple monorepo branches or PR-environment fan-out — we lift
this into a `service_github_links` table. The migration is additive
and backward-compatible with the JSONB shape.

### Token-mint lifecycle

* App JWT: RS256, signed by the operator's private key, 9-minute TTL
  (max allowed by GitHub is 10 minutes; we leave a 1-minute buffer).
* Installation token: exchanged via `/app/installations/{id}/access_tokens`;
  the response carries a 1-hour TTL. We don't cache the token; the build
  worker mints fresh immediately before every private checkout.
* Tokens NEVER appear in audit rows, operation events, job payloads, clone
  URLs, process arguments, or evidence. Git receives the token through an
  environment-only `http.extraHeader`, and checkout stderr is redacted.
* Before minting, the worker verifies that the HTTPS clone host and path match
  the operator-configured host and durable canonical repository identity. A
  custom GitHub API base requires an explicit clone host.

### watch_paths semantics

Prefix-match, not glob:

```
watch_paths = ["services/api"]
# matches:    services/api/main.go, services/api
# rejects:    services/worker/foo.go, README.md
```

Glob (`services/*/api/**`) is panel-deferred. Operators with monorepo
fan-out today either set `watch_paths` to a coarse prefix or run one
service per monorepo subtree.

### Wait-for-CI

Implemented as an optional `WaitForCIGate` boundary the
`GitHubWebhookDispatcher` consults before enqueueing a deploy. When
the operator wires a gate (e.g. backed by `AppClient.CombinedStatusLive`):

* `spec.github.wait_for_ci = true` → dispatcher consults the gate.
* Gate denies → deploy is **deferred** (no queue job, audit row
  records `DEPLOY_GATE_PENDING_CHECKS`). The reconciler picks it up
  on the next `check_run`/`check_suite` event.

When no gate is wired (default), `wait_for_ci=true` is **accepted but
ignored** and a startup log warns that gating is non-functional. This
preserves the API contract for operators that toggle the flag in
config-as-code before they finish CI integration.

A standalone gate goroutine that polls the Checks API on a schedule
is deliberately out of scope: it duplicates GitHub's own
`check_suite` webhook, doubles API quota usage, and requires
operating-system-level state for retry budgets. The webhook-driven
gate is the long-term path.

### PR environments

Out of scope. The receiver accepts `pull_request` events (they're in
the App manifest) but routes them to `Skipped: event_not_routed`. PR
environments are a separate track with its own ADR.

### GitLab / Bitbucket

Receiver already accepts `provider` as a path parameter. Lane-1's
work-in-progress GitLab adapter lives at `internal/gitlab/`. The same
shape — provider-specific verifier + router + spec subkey — applies
when we ship GitLab. Tracked as future work.

### Build-status posting

Midnight does not POST back to GitHub's Checks/Statuses API in this
slice. The App permission is requested (manifest declares
`statuses: write`) so an operator wiring a future status reporter
doesn't need to re-register the App.

## Configuration

Operator env vars (read at boot via `github.LoadAppConfig`):

| Variable | Required | Description |
|----------|----------|-------------|
| `MIDNIGHT_GITHUB_APP_ID` | yes | Numeric App id from the manifest flow. |
| `MIDNIGHT_GITHUB_PRIVATE_KEY_PATH` | yes | Filesystem path to the RSA PEM. |
| `MIDNIGHT_GITHUB_WEBHOOK_SECRET` | yes | The HMAC-SHA256 secret GitHub uses to sign deliveries. |
| `MIDNIGHT_GITHUB_CLIENT_ID` | no | For future user-OAuth flow. |
| `MIDNIGHT_GITHUB_CLIENT_SECRET` | no | For future user-OAuth flow. |
| `MIDNIGHT_GITHUB_API_BASE_URL` | no | GHES override; defaults to https://api.github.com. |

When `MIDNIGHT_GITHUB_APP_ID` is unset, the `/github/installations`
endpoints return 501 and the webhook dispatcher falls through to a
no-op router. Operators without a wired App can still build and
deploy via the public-clone path (`POST /services/{id}/deployments`
with `source: {repo, ref}` for a public repo).

## Webhook decision tree

```
POST /api/v1alpha/webhooks/inbound/github/{endpoint_id}
  -> receiver.verify(X-Hub-Signature-256, body)
     -> bad signature → 401 WEBHOOK_SIGNATURE_INVALID
  -> receiver.dedupe(delivery_id, body_hash)
     -> already seen → 200 Idempotent-Replayed
  -> dispatcher.Dispatch(delivery)
     -> readGitHubEvent(body)
     -> github.Router.Route(event_type, body)
        case ping → Skipped("ping"), 200
        case installation/installation_repositories/pull_request/etc → Skipped, 200
        case push:
          parse → if zero-sha → Skipped("branch_deleted")
          lookup ServiceLookup.FindByRepoBranch(repo, branch)
            none → Skipped("no_linked_service:…")
          for each linked service:
            apply WatchPathsMatch
              no match → drop
            (optional) WaitForCIGate.Evaluate
              deny → audit DEFERRED, drop
            enqueue deploy operation + queue.Deploy job
              payload.request.source = {provider:"github", repository, repo, ref, installation_id, root_directory, config_path}
        case unknown → Skipped("unknown_event:…"), 200
```

The receiver always returns 200/202 for any event the router recognised
(skipped or routed). GitHub disables the webhook delivery channel after
~50 consecutive 4xx/5xx responses, so handling-no-op routes via 200 is
the panel-locked behaviour.

## Surfaces shipped

* `internal/integrations/github/app.go` — `AppConfig`, `AppClient`,
  `MintInstallationToken`, `ListInstallations`, `ListReposForInstallation`.
* `internal/integrations/github/webhook.go` — `WebhookVerifier`,
  push-event commit parser, watch-paths matcher.
* `internal/integrations/github/router.go` — `Router`, `ServiceLookup`,
  `DeployIntent`.
* `internal/integrations/github/link.go` — `ServiceLink`, public view,
  authenticated clone URL helper.
* `internal/build/source_checkout.go` — `SourceCheckoutPlanner`,
  `TokenMinter`, `RedactedCloneURL`.
* `internal/api/github_link.go` — `PUT/GET/DELETE /services/{id}/github`,
  `GET /github/installations`, `GET /github/installations/{id}/repos`.
* `internal/api/github_dispatch.go` — `GitHubWebhookDispatcher`
  promoting GitHub webhook into queued deploy operations; optional
  `WaitForCIGate`.
* `migrations/0010_service_github_index.{up,down}.sql` — JSONB indices.
* `deploy/github-app-manifest.yaml` + `docs/github-app.md` — operator
  setup.

## Consequences

* Operators get auto-deploy on push without an external CI bridge.
* `service.spec.github` is now a first-class subkey; future config-as-
  code surfaces (Midnight CLI's `up` track) MUST round-trip it.
* The default `WebhookDispatcher` in `NewRouterWithDataStack` is now
  `GitHubWebhookDispatcher`. Tests that wanted the old `NoopDispatcher`
  must explicitly pass `WebhookDispatcher: webhook.NoopDispatcher{}` —
  there are none in tree today, but downstream Midnight forks may need
  to update.
* GraphQL surface for the link is **deferred** until the GraphQL
  gateway lands. The REST surface is the canonical seam; a thin
  resolver will mirror it when graphql arrives. (No graphql package
  exists in this repo today.)

## References

* GitHub Apps overview: https://docs.github.com/en/apps/creating-github-apps
* Manifest registration flow: https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-using-url-parameters
* App JWT signing: https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
* Installation tokens: https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
