# Midnightlight Consolidation — Findings & Port Plan

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Date: 2026-07-15
Status: historical consolidation record

> This document preserves the comparison and port plan as it existed on the
> date above. Many “missing” or “porting now” statements were resolved later.
> It is not a current capability matrix; use the [repository README](../../README.md)
> and [backlog](../../BACKLOG.md) for current disposition.

## Purpose

`../midnightlight` was a June 8–17 2026 clean-room reimplementation of a subset of
this repo ("Midnight Light"). Its own `CODEBASE_REUSE_FINDINGS.md` confirms it was
built by copying Midnight's primitives (queue, idempotency, build, docker runtime,
GitHub, router) and rewriting the "joints." We are consolidating: this repo is the
keeper. This document records what was worth porting back, what was not, and why —
so the light repo can be archived without losing knowledge.

Eight parallel comparative-analysis agents (one per subsystem) plus three frontend
agents produced the findings below. Three asserted Midnight bugs were independently
re-verified by hand (digest extraction, one-shot readiness, deploy-truth-from-strings).

## Verdicts by subsystem

Every subsystem came back **PARTIAL**: light is a strictly smaller subset whose
*execution layers* are behind Midnight (light shells out to `docker`/`buildx` CLIs
where Midnight drives the Docker SDK and BuildKit directly, has SLSA/SBOM/provenance,
evidence bundles, multi-host scheduling, real lego ACME issuance, a richer queue and
idempotency store). Do **not** port those. But light's rewrite fixed several real
correctness defects and filled two genuine feature gaps.

## Port list

### Tier 1 — low-risk, high-value correctness fixes (PORTING NOW)

1. **Readiness poll loop** (`internal/deploy/readiness/readiness.go` from light).
   Midnight evaluates readiness once (`internal/deploy/executor.go:448`); a container
   with a slow-passing `HEALTHCHECK` fails the deploy on first inspect. Port light's
   pure-stdlib `Wait(ctx, probe, opts)` (timeout/backoff, typed `TimeoutError`,
   injectable clock) and wrap Midnight's healthcheck-aware evaluation in it.

2. **Build digest extraction** (`internal/build/buildctl.go`). Midnight writes
   `--metadata-file …/buildctl.metadata.json` (line 340) but never reads it; the digest
   comes from `readDigestFromBuildctlOutput` (line 208/380) which returns the *first*
   `sha256:` in stdout — can be a base-image/cache/layer digest, not the pushed image.
   Fix: parse `containerimage.digest` from the metadata JSON, fall back to a
   candidate-collecting + ambiguity-rejecting stdout scan (light `dockerfile/digest.go`,
   `railpack/digest.go`).

3. **Generated-host DNS label hardening** (`internal/deploy/route_promoter.go` `dnsLabel`).
   Two real bugs: non-ASCII input is written as raw multibyte UTF-8 into a DNS label
   (invalid non-LDH), and there is no 63-octet cap. Port light `internal/domain/host.go`
   (`sanitizeDNSLabel` ASCII-only, `MaxDNSLabelOctets=63`, collision-suffix budget).

4. **Atomic-write durability** (`internal/router/atomic_file.go`). `WriteFileAtomic`
   fsyncs the temp file but never the parent directory, so the rename can be lost on
   power loss (ext4/xfs). Port light's `sync_dir_posix.go`/`sync_dir_windows.go` and
   call `syncDirectory` after rename. Also add a `sha256[:10]` hash suffix to route
   filenames (light `shortKeyHash`) to prevent sanitized-name collisions.

5. **`request_id` in error envelope** (`internal/api` `writeRequestError`). Include the
   already-produced `X-Request-ID` in the error body for support correlation.

6. **Git-checkout secret hygiene** (`internal/build/source_checkout.go`). Midnight bakes
   the token into the clone URL and echoes git stderr verbatim
   (`source_checkout.go:140`) — a URL-embedded token can leak into logs. Port light's
   approach (`internal/source/git/checkout.go`): inject auth via
   `http.extraHeader`/`GIT_CONFIG_*` env, reject credential-carrying URLs, and add the
   `secretValue` redaction layer over stderr/errors. Preserve Midnight's
   `local://`/`SkipClone`/`Subpath` paths.

7. **Build-output secret redaction** (`cmd/midnight-buildkit/worker.go:297`). Build
   progress is streamed verbatim into persisted operation events; a secret echoed by a
   build step lands in the log. Port light's assignment-regex redactor
   (`railpack/redaction.go`, `dockerfile/command.go`) into the progress/failure paths.

8. **GitHub token `Secret` wrapper** (`internal/integrations/github/app.go:157`).
   `InstallationToken.Token` is a bare string with a live `json:"token"` tag — a
   `%v`/marshal leaks it. Port light `internal/github/app/secret.go` (redacting
   `String`/`GoString`/`Format`/`MarshalJSON` + response-body scrubber).

### Tier 2 — medium effort, high value (feature gaps / real fixes)

9. **GitHub commit-status write-back** — Midnight cannot post commit statuses at all
   (only reads check-runs for a gate). Port light `internal/github/app/status.go` +
   `status_publisher.go` + metadata validation (`jobs/github.go`) + a drain worker.
   Reuse Midnight's existing `AppClient.MintInstallationToken`. Emit pending at enqueue,
   terminal state from the deploy worker.

10. **Path-filter fail-open on truncated pushes** — Midnight's `WatchPathsMatch`
    (`internal/integrations/github/webhook.go`) iterates only delivered commits with no
    truncation awareness, so a large push whose watch-path change is in a dropped commit
    silently fails closed (missed auto-deploy). Add a truncation predicate + fail-open,
    **but base the signal on `len(commits) >= 20`**, not light's `size`/`distinct_size`
    (those fields are API-only; GitHub's push webhook does not populate them).

11. **Local dev stack** — Midnight has **no** docker-compose / local stack at all. Port
    light's `docker-compose.local.yml` (postgres + migrate + registry + buildkit +
    traefik + api + workers), remapping `midnightlight-*`→`midnight-*`,
    `MIDNIGHTLIGHT_*`→`MIDNIGHT_*`, and binaries to Midnight's `cmd/` layout. Add a
    `make dev-up`/Taskfile target. Reimplement `doctor.ps1`'s good design as bash/Go
    (do NOT port the PowerShell scripts).

12. **Per-route artifact files** (`internal/router` local publication writer) — Midnight
    publishes a single route into a route group whose default key is shared across
    services (`route_promoter.go:191`; worker default is a single static `"default"`
    group) and overwrites one file per group, so a second service clobbers the first.
    Move to one artifact file per (service,environment) within the group dir; Traefik's
    file provider unions the directory. (MED-HIGH; overlaps #4.)

13. **CAA `accounturi` binding (RFC 8657)** — fold light's `parseCAAValue` param handling
    into `internal/acme/caa.go` `ClassifyCAARecords` (Midnight discards CAA params).
    Keep Midnight's offline (caller-supplied) contract; skip live tree-climbing.

14. **`mutation(scope, handler)` middleware seam** — in Midnight, idempotency is enforced
    ad-hoc inside ~6 handlers; a new mutation route can ship without it. Adopt light's
    pattern: a single seam that enforces `Idempotency-Key` presence + scope for every
    mutation. Keep Midnight's richer `idempotency.Reserve/Attach/Complete` store for the
    operation body; only lift the presence+scope composition.

### Tier 3 — large / higher-risk; design captured, execution gated on decision

15. **Canonical `deployments` table + FSM + one-active invariant.** ✅ IMPLEMENTED on
    branch `feat/canonical-deployments` (see `docs/canonical-deployments-plan.md`):
    migrations 0014 (tables + FSM validator), 0015 (gated backfill), 0016 (attach the
    trigger); the Go FSM guard + `PostgresDeploymentStore`; dual-write on intake, worker
    reconcile, and redeploy/rollback/restart mirror; and a guarded read-flip. Live-DB
    migration apply remains a human/CI step (rehearse on a prod clone). Original finding:
    The single deepest
    finding: Midnight has **no canonical deployments table** and reconstructs deploy
    truth by parsing operation-event *message strings* (`internal/api/deployments.go:120`
    `active := route != ""`; `latestActiveDeployment` picks newest by event timestamp —
    nothing prevents multiple "actives"). Light introduces `deployments` +
    `service_environment_state` with a generated `active_environment_id` +
    `UNIQUE(service_id, active_environment_id) DEFERRABLE`, a `validate_deployment_contract()`
    trigger FSM, a typed `deployment_events` ledger, and an atomic `PromoteDeployment`
    CAS swap (`internal/db/pg/repository.go:1506`). Genuine correctness fix, but it is a
    **live-schema migration + historical backfill** (parse legacy event text once at
    migration time) and the one-active UNIQUE will reject any existing data that already
    has duplicate "actives". Requires rehearsal on a prod clone and an audit of every
    deployment-state writer before enabling the trigger. **Highest value, highest risk.**
    NOTE: Midnight has *since* added `webhook_deliveries`, `environment_locks`, and
    `artifact_promotions` (migrations 0002/0003/0005, dated 2026-07-13) — so light's
    dedupe/serialization/promotion-ledger findings are already covered; only the
    canonical deployment record survives.

16. **Per-route scoped-token authorization.** Midnight's HTTP boundary uses a single
    shared bearer token; it *has* token classes (`internal/auth/token.go`) and an RBAC
    policy engine (`internal/auth/policy.go`) but neither is wired into per-route authz.
    Light's per-route `requireScope` shows the seam; adopt it as a deliberate authz
    project using Midnight's richer scope vocabulary. Invasive (touches every route).

17. **Custom-domain lifecycle de-stub.** Midnight's `customDomainCreate` is an explicit
    GraphQL stub (`internal/graphql/resolver.go:618`). When it becomes real, reuse light's
    `custom/hostname.go` `NormalizeHostname` validation and the domain FSM *shape*
    (pending → ownership → routing → CAA → TLS → active, with the failure-state enum).
    Do not port light's `TLSCertificateIssuer` (it only dials :443; Midnight has real
    lego issuance).

## Peer-review follow-ups (dormant paths to finish)

Four peer-review agents cleared the ported diff with no high-severity regressions.
Low-severity findings that touch not-yet-wired paths were left as follow-ups rather
than patched into dormant code:

- **Git-checkout token not threaded into the buildkit worker.** `cmd/midnight-buildkit`
  constructs `SourceCheckoutPlanner` without setting `Token`, so authenticated
  private-repo clone (and the new credential-redaction path) are dormant until the
  source token (GitHub App / PAT) is threaded from the job payload into the planner.
- **Commit-status write-back is dormant** until: a `github_commit_statuses` table +
  migration, deploy enqueue hooks in `internal/api` (pending at deploy start; terminal
  state on completion), worker registration in packaging, and granting the App
  installation `statuses:write`. When wiring the drain worker, also add a per-row
  attempt cap / dead-letter (today a permanently-failing row retries forever and can
  starve newer rows) and re-validate metadata bounds at the publish boundary.
- **Per-route removal wiring.** `router.RemoveRouteArtifact` exists but has no caller;
  add a removal path through the deploy `RouteGroupPublisher` boundary so a deleted
  route's per-route file is cleaned up (until then a stale file lingers but no longer
  clobbers siblings).

## Frontend consolidation (web-next)

`web-next` (React/Vite/Tailwind) was an orphaned 2-route proof-of-concept; the
server actually shipped the older vanilla-JS `web/`. It was rebuilt into a
Railway-modeled dashboard wired to the real backend (REST + `/graphql/v2` + SSE):
real environment model, agent-dock demoted to an overlay (and its broken
send/stream contract fixed), full service-detail tabs (Deployments w/ detail +
actions, Logs w/ live tail, Variables w/ staged changes, Metrics, Domains,
Settings), creation/GitHub flows, canvas polish, and a Cmd+K palette. It
typechecks, builds, and tests green.

The server now serves `web-next/dist` at `/` when a built dist is present
(`internal/api/webui.go`, guarded so `go build`/dev without a build are
unaffected); the legacy `web/` stays at `/dashboard/`. `make web-build` builds
the dist. **Productionization follow-up:** embed the dist via `go:embed` (behind
a build tag or with a committed placeholder so `go build ./...` never breaks) and
add a CI/release step that runs `make web-build` before packaging, so a released
binary is self-contained rather than depending on a co-located `web-next/dist`.
Also: bundle is ~540 KB (code-split later), and a few backend gaps the UI worked
around are worth closing — `variableDelete`, `serviceDelete`, `DELETE /projects`,
per-deployment `trigger`/`commit`/`created_at`, a project-wide deployments query,
real custom-domain resolver, and a metrics/time-series endpoint. Also the
`/services/{id}/deployments[/latest-active]` endpoints are not env-filtered
(the UI passes no env there), so with multiple environments the canvas status
/ Logs tab / deployments feed reflect the service's latest active deployment
regardless of selected env — correct only if deployments are modeled
per-service; add an `environment_id` filter if they are per-env.

## Explicitly NOT porting (Midnight is ahead)

Light's docker CLI adapter, image `reference.go`, `env.go`, `internal/cli`, its
idempotency store (mutex-per-request), its queue (no partition_key), its
`MemoryDeliveryStore` dedupe (weaker than Midnight's `(source,delivery_id,event_hash)`),
its signature verifier (equivalent), its `TLSCertificateIssuer`, its Dockerfile/Railpack
CLI builders, and its bare-stub web frontend (`web/` is a single `main.tsx`; Midnight's
`web-next` is the real React app).
