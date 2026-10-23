# Pull Request Preview Environments

Midnight automatically manages one preview environment per GitHub pull
request and linked service. The runtime consumes signed, persist-before-ack
`pull_request` deliveries and snapshots the exact installation, repository,
service-link, and commit authority into the durable webhook target.

## Lifecycle

- `opened` and `reopened` create or restore the focused preview environment.
- `synchronize` refreshes its source commit, configuration, variables, and
  seven-day idle expiry.
- `closed` starts convergent cleanup. Midnight withdraws routes, stops the
  active deployment, removes its runtime, and then soft-deletes the managed
  environment.
- The server reconciler applies the same cleanup after the idle TTL expires.

Retries are idempotent at the retained delivery target, preview mapping,
deployment trigger, runtime action, status, and comment boundaries. A server
restart can resume each stage without rediscovering a different service link.

## Isolation policy

Preview duplication is service-focused. It copies the linked service's config
and scoped variables from `production`; it never clones volumes, databases,
persistent data, deployment history, or production domains.

Trusted same-repository pull requests receive plain and secret variables but
not write-only sealed variables. Fork pull requests receive only plain
variables. Fork builds additionally skip build-variable lookup and decryption,
disable the shared BuildKit cache, clamp the focused service to one replica,
and publish images below the registry quarantine namespace.

Checkout always uses the installed base repository. Midnight fetches GitHub's
read-only `refs/pull/<number>/head` and rejects it unless it still resolves to
the immutable head SHA from the signed delivery. It never sends an
installation token to a fork-controlled clone URL.

## URLs, statuses, and comments

Each preview owns a generated HTTPS URL below `MIDNIGHT_APPS_DOMAIN`. Canonical
deployment state drives the existing retryable GitHub commit-status outbox. A
separate leased outbox creates one pull-request comment and updates it as the
preview moves through building, ready, failed, deleting, and deleted states.
The comment carries an App-owned preview marker, so a retry after an ambiguous
GitHub response recovers and updates the existing comment instead of creating
a duplicate. Comment delivery failures never roll back preview or deployment
state.

The GitHub App therefore requires `pull_requests: write` in addition to the
existing contents, metadata, statuses, checks, and deployments permissions.
An existing installation may need to approve the expanded permission before
comments can be published.

## API and console

`GET /api/v1alpha/services/{id}/preview-environments` is an authenticated,
service-scoped read. A fully wired PostgreSQL server returns
`capability.state: enabled`, `automatic_lifecycle: true`, and durable active
items including PR number, environment/deployment ids, lifecycle state,
generated URL, head SHA, fork classification, expiry, and update time.

Memory-mode or incompletely wired servers remain honest: they return
`setup_required`, an empty list, and concrete blocker codes. The console
renders enabled previews as links and preserves the setup-required boundary
when execution is unavailable.

`internal/previews` retains provider-neutral planning types for future native
adapters. GitHub is the only execution adapter currently wired.
