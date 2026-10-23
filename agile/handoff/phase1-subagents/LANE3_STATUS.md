# Lane 3 Status — Build + Ingress (Phase 1)

- **Branch:** `phase1/lane3-build-ingress`
- **Worktree:** `.worktrees/phase1-lane3-build-ingress/`
- **Status:** Minimum scope complete; stretch items deferred (see below).
- **Gates:** `go fmt`, `go vet`, `go build ./...`, `go test ./...`, `go run ./tools/lint`, `go run ./tools/phase0-gate --format=json --execute-drills` all green on Windows host.

---

## What landed

### A. Dependencies + version pins

- `versions.yaml` extended with explicit pins: `buildkit: v0.13.2`, `distribution: v3.0.0`, `traefik: v3.1.5`, `lego: v4.17.4`. The file's preamble records the binary-vs-Go-module decision for every entry so future changes go through review.
- `internal/build/versions.go` exports `Required{BuildKit,Distribution,Traefik,Lego}Version` and a `Versions{}` loader the binaries call before any solve / serve invocation.
- `go.mod` / `go.sum` add `github.com/go-acme/lego/v4 v4.17.4`. The ACME library is the only new direct dependency. BuildKit and distribution stay at the binary boundary (shell-out) — see "Decisions you should know" below.
- Indirect deps brought in by lego v4.17.4 + cloudflare-go + AWS SDK v2 (route53). Provider scope deliberately narrowed to `cloudflare`, `route53`, `manual` only; the universal `providers/dns` lookup helper was rejected because it transitively pulls every DNS provider on lego's compatibility list (Aliyun, Azure, etc.).

### B. `cmd/midnight-buildkit/`

- Real CLI with `version`, `verify`, `build` subcommands.
- `verify` calls `buildctl --version` and rejects mismatches with the pinned `RequiredBuildKitVersion`.
- `build` runs a Dockerfile solve via `buildctl --frontend dockerfile.v0`, pushes to `--registry` (with `--insecure-registry` for the embedded localhost case), and writes:
  - `digest.txt` — produced image digest, single line, `sha256:<hex>`.
  - `provenance.json` — SLSA v1 / in-toto v1 statement (deterministic JSON encoder) listing builder id, dockerfile sha256, build args, resolved base-image digests, build start/finish timestamps.
  - `sbom.json` — CycloneDX 1.5 minimal SBOM with subject component + base-image components, stable URN, deterministic ordering.
  - `buildctl.stdout.log`, `buildctl.stderr.log`, `buildctl.metadata.json`.
- Per-service local cache dir under `--cache-root/<sanitized service id>` via buildctl `--export-cache type=local` / `--import-cache type=local`.
- Multi-arch is opt-in via explicit `--platforms`. Empty default = host platform only, matching the lane brief.

### C. `cmd/midnight-registry/`

- Real CLI with `version`, `serve`, `pin add`, `pin list`, `gc plan`, `gc apply` subcommands.
- `serve` launches the pinned `registry` binary as a child process via `internal/registry.EmbeddedRegistry` (fork-exec; no distribution Go module embedded, see decisions).
- `internal/registry/file_pin_store.go` is the JSON-backed `FilePinStore` Lane 3 ships before the Postgres-backed `deployment_image_pins` table lands. `0600` ownership, atomic temp-file + rename writes, deterministic row ordering, schema-versioned document.
- `internal/registry/gc_orchestrator.go` ties retention planning + pin probing + write-pause gating. `PlanGC` consults the pin store before any deletion is added to the apply set; `ApplyGC` calls `WritePauseController.PauseWrites` before deletion and `ResumeWrites` after, with deferred cleanup on error.
- GC dry-run output is the same `RetentionPlan` JSON shape Phase 0 retention tests already cover, so the existing `TestGCApplyResultProtectedDigests`-class fixtures still pass unchanged.

### D. Traefik ingress (live)

- `internal/router/publication_dir.go` introduces `TraefikDynamicPublicationDirectory` — the typed wrapper for the directory contract. `NewTraefikDynamicPublicationDirectory` validates the absolute root, refuses missing directories, and returns a `LocalRouteArtifactPublicationWriter` scoped to it.
- `PublishRouteGroup(routeGroup, routes, existingHash)` is the high-level reconciler entry point: renders the routes, validates against `ValidateRenderedRouteGroup`, runs the prewrite plan, and applies through the directory-scoped writer. When `existingHash` matches the rendered artifact, the publish is a no-op (no rewrite, no temp file) — keeps inotify quiet and avoids burning ACME budget on unchanged routes.
- `deploy/traefik/traefik.yaml` ships the panel-locked static config: pinned `readTimeout=60s`, `writeTimeout=60s`, `idleTimeout=180s`, `maxRequestBodyBytes=32MiB`, file provider watching `/etc/midnight/traefik/dynamic`, two cert resolvers (`letsencrypt-staging` default + `letsencrypt-prod`), Prometheus metrics, **no** Docker provider.
- `deploy/traefik/README.md` documents the directory contract, atomic-write protocol, ACME defaults, and the credential plumbing path until Lane 4 secrets land.
- `deploy/traefik/dynamic/.gitkeep` documents that the directory is the publication root and operators must not hand-edit it.
- The renderer continues to reserve `/.well-known/acme-challenge/*` at maximum router priority on every entrypoint serving :80; user routes claiming the path are rejected at validation. That guard is **already** in `internal/router/traefik.go` and is exercised by `TestPublishRouteGroupRefusesReservedACMEPath` in the new `publication_dir_test.go`.
- Pre-existing Windows CRLF/LF mismatch in `TestRenderTraefikDynamicV2GoldenFiles` is fixed (the test now normalizes line endings before comparing). This was a baseline Phase 0 oversight; Lane 3 owns `internal/router/`.

### E. ACME live

- `internal/acme/lego_user.go` implements `registration.User`. Account ECDSA P-256 key is persisted to disk (`account.key`, `0600`) under the configured state dir.
- `internal/acme/lego_issuer.go` is the live issuer. It binds a DNS-01 challenge provider, registers (or restores) the LE account, and obtains certificates via `client.Certificate.Obtain(certificate.ObtainRequest{...})`. The result is persisted to `<stateDir>/certs/<sanitized domain>/{fullchain.pem, privkey.pem, issuer.pem}` with `0640` / `0600`.
- `LookupBuiltinDNSProvider("cloudflare" | "route53" | "manual")` resolves panel-locked providers only. Other names are explicitly rejected with a per-provider review hint.
- `LegoEnvironment` selects `LEDirectoryStaging` (default) or `LEDirectoryProduction`. Flipping to production is an explicit operator action; the Phase 0 ACME budget store (`internal/acme/budget.go`) still hard-caps issuance at 40/registered-domain/week per ADR-007.
- `ACME state directory` constant `DefaultStateDirectory = "/var/lib/midnight/acme"` matches the panel-locked path. All state-file writes (account key, registration JSON, cert bundle) go through `writeAtomic` (temp-file + rename) so a crash mid-write cannot leave a partial file.
- The existing offline `Orchestrator` state machine is untouched. Live `LegoIssuer` is deliberately a separate type so the orchestrator's audit log keeps emitting deterministic transitions independent of network outcomes.

### F. Tests

- New unit tests (default `go test ./...`):
  - `internal/build/buildctl_test.go` — fake `CommandRunner` exercises `VerifyVersion`, the deterministic `Build` path (provenance + SBOM + digest), missing-field rejection, digest extraction from log, and service-id sanitization.
  - `internal/registry/file_pin_store_test.go` — file persistence across reopen, duplicate insert rejection, decrement, GC orchestrator pin protection, write-pause around apply, pause-error propagation.
  - `internal/router/publication_dir_test.go` — relative/missing/file root rejection, atomic write, hash-equal skip, reserved ACME path rejection.
  - `internal/acme/lego_issuer_test.go` — account key creation+reuse, validation errors, manual provider lookup, cloudflare-without-credentials clean failure, issuer config validation table.
- New integration tests (gated by `//go:build integration`):
  - `internal/build/buildctl_integration_test.go` — drives a live `buildctl` against `MIDNIGHT_BUILDCTL` + `MIDNIGHT_BUILDKIT_REGISTRY` and asserts deterministic digest + byte-equal provenance across two runs.
  - `internal/registry/embedded_integration_test.go` — launches the pinned `registry` binary against the sample config and terminates cleanly.
  - `internal/router/publication_dir_integration_test.go` — writes a route artifact and (when `MIDNIGHT_TRAEFIK_BIN` is set) boots Traefik against the dynamic dir to confirm the file provider accepts it.
  - `internal/acme/lego_integration_test.go` — issues a `*.<apps-domain>` cert against LE staging using the configured DNS provider; gated by `MIDNIGHT_LE_STAGING=1`.
- All integration tests skip cleanly when the requisite env vars / binaries are absent. Default CI runs do **not** require BuildKit, Docker, distribution, or LE network access.

---

## Decisions you should know about

| Decision | Why |
|---|---|
| Shell out to `buildctl` rather than import `github.com/moby/buildkit/client`. | The Go client transitively depends on `containerd`, `runc`, the docker module, and libnetwork — a multi-MB supply-chain expansion. The CLI surface is also what ADR-008 records in deployment snapshots, so binary-edge boundaries are faithful to the contract. |
| Fork-exec the `registry` binary rather than embed `github.com/distribution/distribution/v3`. | Embedding the distribution v3 packages drags in OCI image-spec, Azure / GCS / Swift / OSS storage drivers, and a vendored runc. Phase 1 only needs the filesystem driver. |
| Embed `github.com/go-acme/lego/v4` directly. | DNS-01 + propagation polling needs typed challenge solvers and provider implementations. Shelling out to `lego` would lose typed errors that ADR-007's `DNS_*` failure-code mapping depends on. |
| Lookup is restricted to `cloudflare` / `route53` / `manual`. | Importing `github.com/go-acme/lego/v4/providers/dns` activates **every** DNS provider on lego's compatibility list (Aliyun, Azure, GoDaddy, …) — hundreds of indirect deps. Adding a provider is now a deliberate per-provider review. |
| `FilePinStore` is JSON-backed at `0600`. | Lane 1 has not yet shipped the Postgres `deployment_image_pins` migration. The file-backed store is the documented fallback so Lane 3 can ship without taking a hard dependency on Lane 1's schedule. |
| Pre-existing Windows CRLF in `TestRenderTraefikDynamicV2GoldenFiles` golden files. | Fixed by normalizing line endings in the test before comparing. The fix is in Lane 3 territory (`internal/router/`) and is unrelated to the new code. |

---

## What is deferred

- **§1.6 Custom domains** — TXT verification probe, refuse-routing-before-verification, HTTP-01 issuance for verified domains. Listed as STRETCH in the Lane 3 brief; planner code in `internal/acme/domain_diagnostics.go` is the seam.
- **§1.7 Postgres template + `pg_dump` backup hooks** — Lane 1 owns the DB; Lane 3 only wires the template. Deferred — depends on Lane 1's `internal/db/pg/` landing.
- **§1.14 Railpack builder integration** — separate builder pluggability story; lives behind the same `BuildctlInvocation` shape (or a sibling interface). Deferred.
- **§1.15 cosign signing + signed `.deb`** — release plumbing. Deferred — depends on the broader release-evidence pipeline owned by S08 / Lane 4.
- **`registry` `LookupCloudflareWithProvidedToken` env-var path documented but not wired into the secret store.** Lane 4 is expected to replace `os.Getenv("CLOUDFLARE_DNS_API_TOKEN")` with the envelope-encrypted secret store; the migration TODO is recorded in `deploy/traefik/README.md`.
- **`pgsql` migration `NNNN_image_pins.up.sql`** — the FilePinStore TODO comments call out the future migration. Deferred to Lane 1 by design.

---

## Open questions for PM-A / PM-B

1. **PM-A: Postgres pin migration.** When Lane 1 ships `migrations/NNNN_image_pins.up.sql`, do we keep `FilePinStore` as a best-effort offline cache, or hard-cut to the DB-backed store? The fork-exec registry process needs *some* pin store reachable from a single host even when Postgres is degraded; my recommendation is "keep the file as a degraded-mode cache, write-through both stores".
2. **PM-A: Versions.yaml ownership.** Lane 3 added `distribution`, `traefik`, `lego` keys. Do we want to assert an integrity hash for the buildctl / registry / traefik tarballs ourselves (i.e., extend `versions.yaml` with `*_sha256` fields), or let the .deb's APT metadata be the source of truth? Recommendation: extend versions.yaml with hashes once the .deb pipeline is stable.
3. **PM-B: ACME provider scope.** Lane 3 enforces a hard `cloudflare | route53 | manual` allow-list. ADR-007 says "at least three providers"; that bar is met. If a design partner needs a fourth (e.g. Azure DNS), the addition is a deliberate per-provider review — please confirm that's the intended posture.
4. **PM-B: Custom-domain stretch.** §1.6 is documented as stretch in the lane brief; should Lane 3 own it in a follow-up sprint, or hand it to a Lane 5 / dedicated networking sprint? Recommendation: Lane 3 owns it because the planner code (`internal/acme/domain_diagnostics.go`, `internal/acme/manual_dns.go`) is already in this lane's territory.
5. **PM-B / Security: panel `gate.no-docker-provider` CI gate.** ADR-006 names a CI gate that asserts the Docker provider stanza is absent from default profiles. The gate is **not** wired in this lane — `tools/lint` does not yet scan `deploy/traefik/traefik.yaml` for forbidden stanzas. The sample config we ship is clean, but the regression guard is missing. Recommend a small follow-up to add a guard in `tools/lint`.

---

## Branch + verification

- Branch: `phase1/lane3-build-ingress` (off `main`).
- All commits are local; nothing pushed. Leave the branch for human review per the lane brief.
- Local verification commands (Windows PowerShell):

  ```powershell
  cd .worktrees/phase1-lane3-build-ingress
  go build ./...
  go test ./... -count=1
  go run ./tools/lint
  go run ./tools/phase0-gate --format=json --execute-drills
  ```

- Integration-test commands (Linux / macOS preferred):

  ```bash
  # BuildKit + embedded registry
  buildkitd &
  registry serve deploy/debian/registry.yml &
  MIDNIGHT_BUILDCTL=$(which buildctl) \
  MIDNIGHT_BUILDKIT_REGISTRY=127.0.0.1:5000 \
    go test -tags=integration ./internal/build/...

  MIDNIGHT_REGISTRY_BIN=$(which registry) \
  MIDNIGHT_REGISTRY_CONFIG=$(pwd)/deploy/debian/registry.yml \
    go test -tags=integration ./internal/registry/...

  MIDNIGHT_TRAEFIK_BIN=$(pwd)/traefik \
    go test -tags=integration ./internal/router/...

  # Let's Encrypt staging (DNS-01 wildcard)
  export MIDNIGHT_LE_STAGING=1
  export MIDNIGHT_LE_ACCOUNT_EMAIL=ops@example.invalid
  export MIDNIGHT_LE_APPS_DOMAIN=apps.example.invalid
  export MIDNIGHT_LE_DNS_PROVIDER=cloudflare
  export CLOUDFLARE_DNS_API_TOKEN=...
  go test -tags=integration -timeout=10m ./internal/acme/...
  ```
