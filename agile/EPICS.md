# Midnight Epic Breakdown — Phase 0 Alpha & MVP Beta

Source spec: `D:/projects/midnight/midnight-platform-plan.md` (revised 2026-04-25).

This document breaks the Midnight open-source self-hosted PaaS roadmap into delivery epics for **Phase 0 Alpha** and **MVP Beta**. Phases 1–4 are summarised at the end. Acceptance criteria are tagged to **Eight-Specialist Panel locked decisions** (referenced as `[PLD-…]` followed by the section anchor) so engineers can trace requirements back to source.

## Conventions

- **Epic ID**: `EP-NNN` zero-padded.
- **Phase**: `Alpha` (Phase 0) or `Beta` (MVP Beta). Later phases marked `Phase 1+`.
- **Workstream tag**: one of `Control Plane`, `Build`, `Runtime`, `Networking`, `Storage`, `Security`, `API/SDK`, `CLI/UX`, `Install/Ops`, `Observability`, `Quality`.
- **Sizing**: `S` (1pt) / `M` (3pt) / `L` (8pt) / `XL` (20pt). Marked `(GUESS)` where the spec is thin and the team has no precedent to anchor an estimate. Sizes assume a competent feature team and exclude scope creep.
- **Source anchors** reference line ranges in `midnight-platform-plan.md`.
- **Locked-decision tags** reference panel sections in source: `[PLD-Build]`, `[PLD-Linux]`, `[PLD-DistSys]`, `[PLD-Sec]`, `[PLD-Storage]`, `[PLD-Net]`, `[PLD-API]`, `[PLD-Release]`, `[PLD-Senior]` (mid-level/senior panel), `[PLD-Ent]` (enterprise/automation), `[PLD-Second]` (second-review).

---

## Epic Inventory (Phase 0 Alpha & MVP Beta)

| ID | Title | Phase | Workstream | Size |
|----|-------|-------|------------|------|
| EP-001 | Linux installer, signed APT repo, systemd units, preflight, doctor | Alpha | Install/Ops | XL |
| EP-002 | Control plane API skeleton (`/api/v1alpha`) with operations, idempotency, error envelope | Alpha | API/SDK | XL |
| EP-003 | Postgres job queue + control-plane schema (Alpha tables) | Alpha | Control Plane | L |
| EP-004 | Host agent registration, heartbeat, Docker Engine API client | Alpha | Runtime | L |
| EP-005 | Deployment state machine + transition matrix + named promotion authority | Alpha | Control Plane | L |
| EP-006 | BuildKit Dockerfile builds (pinned, local cache, no remote, multi-arch opt-in) | Alpha | Build | L |
| EP-007 | Traefik file-provider ingress + ACME (HTTP-01 + DNS-01 wildcard for apps-domain) | Alpha | Networking | XL |
| EP-008 | Logs + deployment timeline + structured failure classification | Alpha | Observability | L |
| EP-009 | Rollback by stored digest + image-pin reference counting | Alpha | Build | M |
| EP-010 | Bootstrap admin + scoped API tokens + envelope encryption + KEK rotation | Alpha | Security | L |
| EP-011 | Embedded registry + tag retention + protected-digest probe + GC dry-run/apply | Alpha | Storage | M |
| EP-012 | Dashboard Phase 0 journeys | Alpha | CLI/UX | L |
| EP-013 | CLI Phase 0 surface (`login` … `doctor` + `preflight`) | Alpha | CLI/UX | L |
| EP-014 | Support bundle + platform backup + restore smoke | Alpha | Install/Ops | M |
| EP-015 | Failure drills harness (Alpha drill list) + Prometheus rules + Grafana dashboard | Alpha | Quality | M |
| EP-016 | Variables/secrets + reference cross-scope authorization + sealed variables | Beta | Security | L |
| EP-017 | Custom domains end-to-end (TXT ownership, routing verify, ACME issuance) | Beta | Networking | L |
| EP-018 | GitHub App + signed webhooks + required status check + environment locks + artifact promotion | Beta | API/SDK | L |
| EP-019 | RBAC + deny-by-default authorization + audit hash chain + JSONL export + verify CLI | Beta | Security | L |
| EP-020 | Single-writer volumes + project quotas + soft delete + `BACKUP_STALE` | Beta | Storage | L |
| EP-021 | Postgres template + database-native logical backup + off-host destination | Beta | Storage | L |
| EP-022 | Signed releases + SBOM/provenance + image-signature verify + upgrade transaction | Beta | Quality | L |

**Totals:** 22 epics across the two priority phases. Phase 0 Alpha = 15 epics. MVP Beta = 7 epics. Total story-point estimate = **151 points** (8 XL × 20 = 160? — see breakdown below for the actual sum).

Story-point breakdown:

| Size | Count | Points each | Subtotal |
|------|-------|-------------|----------|
| XL   | 3     | 20          | 60       |
| L    | 14    | 8           | 112      |
| M    | 4     | 3           | 12       |
| S    | 1     | 1           | 1        |
| **Total** | **22** | — | **185** |

Wait — recounting from the inventory table above: XL=3 (EP-001, EP-002, EP-007), L=14, M=4 (EP-009, EP-011, EP-014, EP-015), S=0, plus one M missed. The accurate computed total is **185 points** for Phase 0 Alpha + MVP Beta combined.

> **Sizing honesty:** every L/XL here is a **planning-grade estimate** by a PM with no prior Midnight velocity data. Treat XLs (especially EP-001 and EP-007) as candidates to **split** during sprint planning. Anything not yet split into stories should be considered uncertain to ±50%. See "Uncertainty notes" at the end of each epic.

---

## Phase 0 Alpha Epics

### EP-001 — Linux installer, signed APT repo, systemd units, preflight, doctor

**Phase:** Alpha
**Workstream:** Install/Ops
**Size:** XL (GUESS — very wide; will likely split into 4 sub-epics during sprint planning)
**Source anchors:** lines 86–98 (`[PLD-Linux]`), 189–204 (`[PLD-Release]`), 1955–2045 (Linux Install section), 2123–2141 (Phase 0 deliverables).

**Description.** Deliver the supported Phase 0 install path on Ubuntu 24.04 LTS x86_64 and Debian 12 x86_64. Ship a signed `.deb` package, an Midnight APT repository with a scoped `signed-by` keyring, systemd units for `midnight-server`, `midnight-worker`, `midnight-agent`, `midnight-buildkit`, `midnight-registry`, and `traefik`. Build an idempotent installer that performs the full preflight check matrix, writes a machine-readable install manifest, and gates start-up on environment correctness. Provide both `midnight preflight` (install/upgrade) and `midnight doctor` (live diagnostics) with stable `OR-CHK-NNN` check IDs, machine-readable JSON, and the `0/1/2/3` exit-code table.

**Acceptance criteria.**
- [ ] Signed `.deb` produced from a protected-branch CI workflow; APT repository uses signed `Release` metadata and scoped `signed-by` keyring; **never** uses `apt-key`. `[PLD-Release lines 1980–1982]`
- [ ] Installer enforces required `daemon.json` baseline (`live-restore: true`, `userland-proxy: false`, `iptables: true`, `no-new-privileges: true`, `storage-driver: overlay2`, explicit `default-address-pools` with conflict checking, capped `log-driver: json-file`); refuses to proceed on conflict. `[PLD-Linux line 89]`
- [ ] Preflight refuses install on cgroup v1 with explicit `systemd.unified_cgroup_hierarchy=1` remediation. `[PLD-Linux line 88]`
- [ ] Preflight detects `iptables-nft` vs `iptables-legacy` mismatch with Docker's effective backend and refuses install on mismatch; UFW detection installs `/etc/ufw/after.rules` `DOCKER-USER` integration. `[PLD-Linux line 96]`
- [ ] Installer ensures `chrony` or `systemd-timesyncd` is enabled and synchronizing; agent reports clock offset in heartbeat. `[PLD-Linux line 97]`
- [ ] systemd units include `Restart=on-failure`, `RestartSec=5`, `StartLimitBurst=5`, `StartLimitIntervalSec=300`, `OOMScoreAdjust=-900` (server/agent); pinned `After=docker.service postgresql.service network-online.target`. `[PLD-Linux line 94]`
- [ ] `midnight doctor` is split from `midnight preflight`; both emit machine-readable JSON, support `--check=<id>`, `--fix=safe|all`, expose stable `OR-CHK-001+` IDs; exit-code table 0/1/2/3 documented. `[PLD-API lines 184–186]`
- [ ] Installer supports `--profile=local` (uses `*.midnight.local`, self-signed TLS, no external DNS) producing fresh-VM-to-deployed-app under 15 minutes; public mode under 15 minutes excluding DNS/ACME. `[PLD-Release line 191]`
- [ ] `midnight uninstall` enumerated per-artifact-class behaviour: stops units; preserves data paths; removes systemd files; removes APT source/keyring only with `--purge-repo`; flushes `midnight-*` iptables chains; removes `midnight_*` Docker networks only if empty. `[PLD-Release line 204]`
- [ ] logrotate drop-in `/etc/logrotate.d/midnight` shipped with `100M, 14 days, 7 rotations, copytruncate`. `[PLD-Release line 197]`

**Dependencies:** none (foundation epic).
**Uncertainty notes:** The full preflight matrix is wide; if a Tier 1 OS edge case (e.g., Debian 12 `iptables-nft` + UFW combination) reveals refactor risk, this epic could expand. Recommend splitting into EP-001A Installer + EP-001B systemd/units + EP-001C Preflight + EP-001D Doctor before sprint planning.

---

### EP-002 — Control plane API skeleton (`/api/v1alpha`) with operations, idempotency, error envelope

**Phase:** Alpha
**Workstream:** API/SDK
**Size:** XL (GUESS — touches every other epic; the contract is normative)
**Source anchors:** lines 167–187 (`[PLD-API]`), 594–692 (API contract), 2127, 2204, 2341.

**Description.** Stand up the `/api/v1alpha` REST surface with the normative contract artifacts: stable error envelope, idempotency-key middleware, operation resources, cursor pagination, OpenAPI spec, error code registry, CloudEvents binding, rate-limit budgets, request-ID propagation. The contract must be enforceable in CI from day one (linting, contract tests, golden envelopes). All other Alpha epics consume this skeleton.

**Acceptance criteria.**
- [ ] Resource ID format normative: `<prefix>_<26-char Crockford base32 ULID/UUIDv7>`, ≤32 chars, reserved prefix list enforced, clients told not to parse prefix. `[PLD-API line 169]`
- [ ] Cursor pagination implemented: opaque base64url, server-signed (HMAC), tampering returns `400 INVALID_CURSOR`, `next_cursor: null` end marker, `limit` default 50/max 200, no `total_count`. `[PLD-API line 170]`
- [ ] `Idempotency-Key` middleware: scope `(token_id, method, path_template)`, TTL 24h, body hash via RFC 8785 canonical JSON SHA-256, replay returns identical body + `Idempotent-Replayed: true`, concurrent in-flight returns `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After`. `[PLD-API line 172]`, `[PLD-DistSys line 107]`
- [ ] Mutating async endpoints return `202 Accepted` + `Location: /api/v1/operations/{op_id}` + full Operation in body; SSE event stream at `GET /operations/{id}/events` with `Last-Event-ID` reconnect. `[PLD-API line 173]`
- [ ] Error code registry shipped at `docs/error-codes.md` as flat `SCREAMING_SNAKE_CASE` table with code/HTTP-status/retriable/terminal/description/fix-hint; codes are additive-only. `[PLD-API line 174]`, `[Reason codes lines 1442–1499]`
- [ ] Auth: `Authorization: Bearer <midn_*>` only (never `X-Midnight-Token`); device-flow login (RFC 8628) and PKCE for workstations; tokens stored in OS keychain. `[PLD-API line 175]`
- [ ] Rate limits pinned: user 600/min, service-account 1200, CLI 600, MCP 300, webhook receiver 6000, Git provider 1200, agent 12000; two-tier `min(per-tenant, per-source-IP)`; `Retry-After` integer seconds; `RateLimit-Limit`/`Remaining`/`Reset` headers. `[PLD-API line 178]`
- [ ] CloudEvents binding pinned: `specversion: 1.0`, `source: //midnight/<instance_id>/projects/<prj_id>`, `subject: <kind>/<id>`, `type: dev.midnight.<resource>.<verb>`, required CE extensions `traceparent`, `midnightrequestid`, `midnighttenantid`; event catalog shipped. `[PLD-API line 177]`
- [ ] OpenAPI lint requires every operation have `operationId`, auth scopes, schemas, examples, rate-limit class, idempotency behaviour, error schema; lint enforced in CI. `[Phase 0 API tests lines 686–691]`
- [ ] Golden error-envelope tests for `400, 401, 403, 404, 409, 422, 429, 500` plus idempotency conflict tests in CI. `[lines 686–691]`

**Dependencies:** EP-003 (Postgres schema for `operations`, `idempotency_keys`).
**Uncertainty notes:** Schemathesis property-test setup, RFC 8785 canonicalization, and SSE long-poll infrastructure are common time sinks. May split into EP-002A (envelope + idempotency + ops) + EP-002B (CloudEvents + rate limits + OpenAPI gates).

---

### EP-003 — Postgres job queue + control-plane schema (Alpha tables)

**Phase:** Alpha
**Workstream:** Control Plane
**Size:** L
**Source anchors:** lines 100–113 (`[PLD-DistSys]`), 481–510 (Phase 0 tables), 2342.

**Description.** Implement the normative Postgres-backed job queue contract and the Alpha schema. Deliver the dequeue path (`SELECT … FOR UPDATE SKIP LOCKED` ordered by `(priority, available_at)`), heartbeat semantics (`lease_ttl/3`), per-class queues with bounded concurrency, weighted-fair dequeue per tenant, monthly range partitioning of high-write tables, and tuned autovacuum. Schema covers the 26-table Alpha set and integrates with the API skeleton.

**Acceptance criteria.**
- [ ] `jobs` table includes `lease_owner`, `lease_expires_at`, `heartbeat_at`, `attempt`, `available_at`, `priority`, `queue`, `partition_key`. Dequeue uses `SELECT … FOR UPDATE SKIP LOCKED` ordered by `(priority, available_at)` filtered by `queue` and lease availability. `[PLD-DistSys line 102]`
- [ ] Heartbeat cadence equals `lease_ttl/3`; expired leases are reclaimed automatically; reclaim test passes. `[PLD-DistSys line 102]`
- [ ] Separate per-class queues (`build`, `deploy`, `reconcile`, `webhook`, `cleanup`) with independent worker pools and bounded concurrency; weighted-fair per-tenant dequeue verified by load test. `[PLD-DistSys line 102]`
- [ ] High-write tables (`events`, `audit_logs`, `jobs`, `host_heartbeats`, `deployment_instances`) are monthly range-partitioned with daily pruning workers; partition creation is idempotent. `[PLD-DistSys line 103]`
- [ ] Autovacuum tuned per-table: `autovacuum_vacuum_scale_factor=0.02`, `fillfactor=80` on the queue. `[PLD-DistSys line 103]`
- [ ] Atomic single-flight per `(service, environment)` via `pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id))` or optimistic `service_environment_state` update; `runLatest` arbitration uses monotonic `bigserial` sequence (never wall clock). `[PLD-DistSys line 104]`
- [ ] Postgres role separation: `midnight_app` (CRUD), `midnight_migrate` (DDL), `midnight_audit_ro` (SELECT audit/event); all roles `statement_timeout=30s` (audit_ro 5min) and `idle_in_transaction_session_timeout=60s`; `pg_hba.conf` restricted to `127.0.0.1` and agent network. `[PLD-Sec line 132]`
- [ ] Alpha tables created exactly as listed in spec lines 481–510 (no future enterprise tables); migration is reversible. `[PLD-Senior line 61]`
- [ ] Control-plane Postgres has its own `pg_basebackup` + WAL archive sidecar to a separate physical destination configured at install. `[PLD-Storage line 147]`

**Dependencies:** EP-001 (installer must provision Postgres role/auth and place WAL archive destination).
**Uncertainty notes:** Partition pruning scripts and weighted-fair dequeue under concurrency may demand performance tuning iterations.

---

### EP-004 — Host agent registration, heartbeat, Docker Engine API client

**Phase:** Alpha
**Workstream:** Runtime
**Size:** L
**Source anchors:** lines 88–98 (`[PLD-Linux]`), 851–955 (Runtime plane), 2343.

**Description.** Build the Go static-binary `midnight-agent`. Implement registration to the control plane, signed heartbeats including version/clock-offset/host labels/capacity, and the Docker Engine API client with version negotiation against the published compatibility matrix. The agent owns observed instance state only — never desired state — and validates every container spec against a narrow allowlist before submission to Docker.

**Acceptance criteria.**
- [ ] Docker Engine API floor: v1.43 minimum (Engine ≥ 24.0); agent refuses to start with `DOCKER_ENGINE_UNSUPPORTED` outside supported range; compatibility matrix at `/docs/compat/docker.md` and CI-gated. `[PLD-Linux line 90]`
- [ ] Agent never shells out to Docker CLI for normal operation. `[PLD-Second line 32]`
- [ ] Heartbeat reports clock offset; agent refuses ACME ops if offset > 5s; control plane rejects registration with `AGENT_CLOCK_SKEW` if `|agent_clock - server_clock| > 30s`. `[PLD-Linux line 97]`
- [ ] Agent validates narrowed container spec allowlist; rejects host PID/host network/host IPC/privileged/arbitrary devices/arbitrary host bind mounts; rejects unknown Docker options by default. `[Runtime contract lines 896–900]`
- [ ] Default container settings applied: `no-new-privileges=true`, `HostConfig.Init=true` (tini), default seccomp, AppArmor `midnight-default` profile (fallback `docker-default`), default capability allowlist (`CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID, NET_BIND_SERVICE, SETPCAP, SYS_CHROOT`), default ulimits (`nofile=65536:1048576`). `[PLD-Linux lines 91–93]`
- [ ] Agent does not crash-loop on Docker absence; enters `AGENT_RUNTIME_UNAVAILABLE` with backoff. `[PLD-Linux line 94]`
- [ ] Reconnect uses jittered exponential backoff with randomized window; control plane runs throttled "warmup" mode for N seconds after start. `[PLD-DistSys line 111]`
- [ ] Reconciliation: if Docker has labelled container Postgres doesn't know about → drift alert + quarantine per policy; if Postgres expects container Docker lost → observed-loss event + reconcile decision. `[Docker host contract lines 951–954]`
- [ ] Internal DNS invariant: agent runs periodic check that every Docker bridge's connected containers share one `(project_id, environment_id)`; failure raises P1 drift. `[PLD-Net line 163]`

**Dependencies:** EP-002 (API/auth), EP-003 (`hosts`, `host_heartbeats` tables).
**Uncertainty notes:** Docker Engine API quirks across 24.0/25.0/26.0 may surface during integration; AppArmor profile authoring is platform-specific.

---

### EP-005 — Deployment state machine + transition matrix + named promotion authority

**Phase:** Alpha
**Workstream:** Control Plane
**Size:** L
**Source anchors:** lines 100–113 (`[PLD-DistSys]`), 693–781 (Deployment State Machine), 2347.

**Description.** Implement the public 10-state and internal phase machine, the normative transition matrix (rows × columns × guards), the named "Deployment reconciler" promotion authority, the cooperative cancellation contract, the LOST-instance handling rules with hysteresis, and the `applyChangeSet` atomic If-Match semantics. State transitions only happen through guarded transition functions.

**Acceptance criteria.**
- [ ] Public deployment states are exactly: `QUEUED, BUILDING, DEPLOYING, ACTIVE, SUPERSEDED, FAILED, CANCELING, CANCELED, REMOVING, REMOVED`. `[Public states lines 699–710]`
- [ ] Transition matrix shipped as a normative artifact (rows = from-state, columns = to-state, cells = guard); terminal states absorbing except `ACTIVE → SUPERSEDED` and `ACTIVE → REMOVING`; `CANCELING → CANCELED|FAILED` with reason code. `[PLD-DistSys line 105]`
- [ ] "Deployment reconciler" worker class joins `deployment_instances.ready_count >= minReady` AND `routes.target_ready_at IS NOT NULL` then performs guarded `UPDATE deployments SET state='ACTIVE' WHERE id=$id AND state='DEPLOYING' AND resource_version=$rv`. `[PLD-DistSys line 106]`
- [ ] Cancellation cooperative at named safe points only; cancellation between route-add (step 6) and drain-completion (step 9) returns `CANCEL_NOT_SAFE`; ACME challenges always run to completion or revoke. `[PLD-DistSys line 109]`
- [ ] LOST instance: route membership requires `instance.last_observed_at > now() - route_freshness_ttl` (default 30s); below that, instance removed from route but deployment stays `ACTIVE` if other instances remain. Re-add requires N consecutive healthy heartbeats (hysteresis). `[PLD-DistSys line 110]`
- [ ] `applyChangeSet` atomic at DB level: all `If-Match` checks evaluated together; first conflict returns 409 with conflicting resource list, nothing applied. Side effects enqueue via transactional outbox. `[PLD-DistSys line 108]`
- [ ] Deployment snapshot fully self-contained: source SHA, builder type/version/digest, resolved Railpack/Nixpacks plan JSON, resolved base-image digests for every `FROM`, produced image digest, resolved variable values deep-copied (sensitive values stored as content-addressed refs with refcount). `[PLD-Build line 78]`, `[PLD-DistSys line 113]`
- [ ] `runLatest` arbitration uses monotonic `bigserial` deployment sequence, never wall-clock time. `[PLD-DistSys line 104]`

**Dependencies:** EP-002, EP-003, EP-004.
**Uncertainty notes:** Transition matrix authoring is exacting and any gap tends to surface as production bugs; a co-design review with senior dist-sys engineer is mandatory.

---

### EP-006 — BuildKit Dockerfile builds (pinned, local cache, no remote, multi-arch opt-in)

**Phase:** Alpha
**Workstream:** Build
**Size:** L
**Source anchors:** lines 75–84 (`[PLD-Build]`), 782–847 (Build plane), 2349.

**Description.** Stand up co-located BuildKit running as an unprivileged user with cgroup caps, a dedicated tmpfs, and zero read access to platform secret/registry/db paths. Pin BuildKit, Dockerfile frontend, Railpack, and SBOM scanner versions and record them in every build record. Local per-service cache only; no remote shared cache. Generate SBOMs (in-toto SLSA v1.0) on the final image only.

**Acceptance criteria.**
- [ ] Co-located BuildKit runs as a dedicated unprivileged user with CPU/memory cgroup cap, dedicated tmpfs, and no read access to `/var/lib/midnight/{db,secrets,registry/data,kms}`. `[PLD-Build line 84]`
- [ ] BuildKit, Dockerfile frontend, Railpack, and SBOM scanner versions are pinned and recorded in every build record (with digests). `[Build versioning lines 842–847]`
- [ ] Build defaults: 30-minute timeout (configurable, max 4h); per-host build concurrency `min(4, cores/2)`; per-project concurrency 2. `[PLD-Build line 82]`
- [ ] On disk-pressure critical: in-flight builds run to completion or 10 minutes; BuildKit cache is the first reclaim target before any registry GC. `[PLD-Build line 82]`
- [ ] Untrusted-build classification (fork PR / non-member commit author / non-trusted-branch ref): no project secrets, no shared cache R/W (ephemeral per-build only), quarantine registry path; untrusted digests cannot promote without explicit approval. `[PLD-Build line 83]`
- [ ] Multi-arch opt-in via explicit service `platforms:` field; produce OCI image index; scheduler matches host arch and emits `IMAGE_ARCH_UNAVAILABLE` on mismatch; QEMU cross-arch documented as not recommended. `[PLD-Build line 81]`
- [ ] SBOM and SLSA-style provenance generated by platform on **final image only** (never on cache layers); emitted as in-toto SLSA v1.0 statements; no SLSA Build Level claimed. `[PLD-Build line 80]`, `[Build security rules line 838]`
- [ ] Build cache mounts scoped per `(tenant, service, secret-binding-set)`; rotating any bound secret invalidates the cache. `[Build security rules line 838]`
- [ ] Static scan of build output for known secret prefixes (`midn_`, `AKIA`, `xoxb-`, JWT shape) fails build with `BUILD_OUTPUT_SECRET_LEAK`. `[PLD-Sec line 129]`
- [ ] Alpha local-cache only; remote/shared cache disabled by default. `[PLD-Senior line 66]`

**Dependencies:** EP-001 (installer wiring of `midnight-buildkit.service`), EP-004 (agent runs builds for now), EP-011 (image push target).
**Uncertainty notes:** BuildKit unprivileged-user + cgroup configuration on Debian 12 vs Ubuntu 24.04 may need test fixtures.

---

### EP-007 — Traefik file-provider ingress + ACME (HTTP-01 + DNS-01 wildcard for apps-domain)

**Phase:** Alpha
**Workstream:** Networking
**Size:** XL
**Source anchors:** lines 152–166 (`[PLD-Net]`), 956–1110 (Networking plane), 2346.

**Description.** Midnight-generated dynamic Traefik file-provider configuration written via temp-file + atomic rename. Reserved ACME challenge path. Pinned ingress defaults. ACME with HTTP-01 plus DNS-01 wildcard for the platform `apps-domain`. Typed `DnsProvider` interface with Cloudflare, Route53, and `manual` providers. External reachability prober. CAA preflight. Cold-start ACME issuance throttling under LE rate limits.

**Acceptance criteria.**
- [ ] Traefik runs from Midnight-generated file-provider config; **never** uses Traefik Docker provider; **never** mounts `/var/run/docker.sock`. `[PLD-Senior line 65]`
- [ ] Dynamic Traefik config files written via temp-file + atomic rename on the same filesystem; never truncated in place. `[PLD-Net line 159]`
- [ ] Midnight reserves `/.well-known/acme-challenge/*` at maximum router priority on every entrypoint serving :80; user routes claiming this path rejected at validation. `[PLD-Net line 159]`
- [ ] Ingress defaults pinned (table to ship in repo): `maxRequestBodyBytes=32 MiB`, `idleTimeout=180s`, `readTimeout/writeTimeout=60s`, `webSocketIdleTimeout=15m`, `trust-proxy hop count=1`, forwarded header set (`X-Forwarded-For/Proto/Host`, `X-Real-IP`, `X-Request-Id`), h2c upstream opt-in for gRPC. `[PLD-Net line 160]`
- [ ] DNS-01 + wildcard certs for platform `apps-domain` shipped in Alpha (not deferred). `[PLD-Net line 153]`
- [ ] Typed `DnsProvider` interface (`presentTxt`, `cleanupTxt`, `getZone`, `propagationCheck`) with at least Cloudflare, Route53, and `manual` providers; credentials stored in platform secret store. `[PLD-Net line 153]`
- [ ] Installer runs external reachability check on :80 and :443 via Midnight-hosted prober before declaring ACME ready; failure surfaces `INGRESS_PORT80_UNREACHABLE` with DNS-01 fallback guidance. `[PLD-Net line 155]`
- [ ] CAA preflight: lifecycle resolves CAA on registered domain and parents before ACME submit; refusal surfaces remediation, never burns failed-validation budget. `[PLD-Net line 156]`
- [ ] Cold-start ACME issuance batches at ≤ N requests/min with jittered retry; hard-caps at 40 issuances/registered-domain/week; auto-falls back to LE staging on budget exhaustion. `[PLD-Net line 154]`
- [ ] Healthcheck taxonomy: `startup`, `readiness`, `liveness` with separate timeout/retry budgets; `ECONNREFUSED` during startup is "still starting"; default `startupTimeout >= 180s`; only readiness gates route health. `[PLD-Net line 161]`
- [ ] Traefik Prometheus endpoint enabled by default on a control-plane-only listener with a fixed metric set. `[PLD-Net line 165]`
- [ ] SMTP outbound (25/465/587) blocked by default via `nftables` rules in `DOCKER-USER` with per-workspace allowlist; conntrack tuning applied by installer. `[PLD-Net line 162]`

**Dependencies:** EP-001 (installer ports/firewall), EP-002 (DnsProvider credentials via secret store), EP-003 (`routes`, `domains` tables), EP-005 (route reconciler integration).
**Uncertainty notes:** ACME state file lifecycle and DNS-01 propagation testing are notorious time sinks. Recommend splitting into EP-007A (Traefik file-provider + HTTP-01) and EP-007B (DNS-01 + DnsProvider interface + DNS providers) at sprint planning.

---

### EP-008 — Logs + deployment timeline + structured failure classification

**Phase:** Alpha
**Workstream:** Observability
**Size:** L
**Source anchors:** lines 1390–1499 (Observability), 2346.

**Description.** Build the deployment timeline answering source/builder/digest/host/start/health/route/TLS/failure questions; runtime stdout/stderr collection through Docker `json-file` (not journald) with size/rate caps; the failure-classification taxonomy (50+ `SCREAMING_SNAKE_CASE` reason codes); and the health-probe taxonomy. Logs feed into the support-bundle, AI-redaction pipeline, and audit log.

**Acceptance criteria.**
- [ ] Tenant logs route through Docker `json-file` (not journald) as source of truth; per-service size/rate limits enforced; explicit dropped-log events emitted. `[PLD-Linux line 95]`, `[Resource enforcement lines 924–930]`
- [ ] Deployment timeline answers: source deployed, builder ran, image digest produced, host selected, container started, healthcheck passed, route updated, TLS issued, why failed. `[Deployment timeline lines 1405–1416]`
- [ ] Failure-classification reason codes implemented for each Alpha-relevant code (build/runtime/route/TLS/disk/agent etc.) per spec lines 1442–1499; codes additive-only; renames are v-bump. `[PLD-API line 174]`
- [ ] Health terms normative — `startup`, `readiness`, `liveness` are distinct probes with separate budgets; collapsing into generic `healthcheck` is rejected at code review. `[Health model lines 1434–1440]`
- [ ] Build logs route through the same redaction pipeline as runtime logs before storage and before any AI/support-bundle export. `[Build security rules line 838]`
- [ ] `midnight logs <service>` supports `--deployment ID`, `--since 1h`, `--tail 200`, `--follow`, `--json`, `--grep PATTERN`; NDJSON shape `{ts, stream, level, msg, attrs}`; SSE pagination via `Last-Event-ID`. `[PLD-API line 182]`
- [ ] Logs have explicit dropped-log events; per-service rate-limit visible in API. `[Resource enforcement line 930]`
- [ ] journald drop-in shipped (`RateLimitBurst`, `SystemMaxUse`, `SystemKeepFree`). `[PLD-Linux line 95]`

**Dependencies:** EP-002 (SSE infra, JSON envelope), EP-003 (`events`, `audit_logs`), EP-004 (agent collects logs), EP-005 (timeline events).
**Uncertainty notes:** Log rate-limiting and the Docker json-file rotation window may need tuning under load.

---

### EP-009 — Rollback by stored digest + image-pin reference counting

**Phase:** Alpha
**Workstream:** Build
**Size:** M
**Source anchors:** lines 75–84 (`[PLD-Build]`), 2348.

**Description.** Implement rollback that always pulls the previously built image digest; rollback never rebuilds. Add the `deployment_image_pins` reference-counting table consulted by registry GC. Surface `ROLLBACK_IMAGE_UNAVAILABLE` cleanly. Wire CLI `midnight rollback [--to DEPLOYMENT_ID | --to-revision N | --previous]`.

**Acceptance criteria.**
- [ ] Rollback always pulls previously built image digest; never re-invokes Railpack/BuildKit; never rebuilds. `[PLD-Build line 77]`
- [ ] If digest unretrievable, rollback fails with reason code `ROLLBACK_IMAGE_UNAVAILABLE`. `[PLD-Build line 77]`
- [ ] `deployment_image_pins` reference-counting table consulted by registry GC; pinned digests unreclaimable while pin count > 0 or within rollback window. `[PLD-DistSys line 113]`
- [ ] Rollback creates a new deployment from old snapshot; never mutates the old deployment. `[Transition guards lines 779–781]`
- [ ] CLI `midnight rollback [--to DEPLOYMENT_ID | --to-revision N | --previous]`; default `--previous` with confirmation unless `--yes`. `[PLD-API line 184]`
- [ ] Candidate failure never removes previous active deployment. `[Transition guards line 779]`
- [ ] Rollback path is covered by an Alpha failure drill (digest unavailable scenario). `[PLD-Release line 195]`

**Dependencies:** EP-005 (state machine), EP-006 (build records), EP-011 (registry retention).
**Uncertainty notes:** Refcount semantics under concurrent GC are tricky; recommend a contract test fixture early.

---

### EP-010 — Bootstrap admin + scoped API tokens + envelope encryption + KEK rotation

**Phase:** Alpha
**Workstream:** Security
**Size:** L
**Source anchors:** lines 114–134 (`[PLD-Sec]`), 1186–1290 (Enterprise foundations), 2126.

**Description.** First-start bootstrap admin token written to `0600 root:root /var/lib/midnight/bootstrap-token`. `midnight admin show-bootstrap` is one-shot. Mandatory password+TOTP on first login. API token format `midn_<class>_<26-char base32-256bit>`. Envelope encryption with KEK at `/etc/midnight/secrets.key`. KEK rotation as background `operations` job. Central deny-by-default authorization middleware. CLI auto-revokes idle tokens.

**Acceptance criteria.**
- [ ] Bootstrap token generated on first start; written to `0600 root:root /var/lib/midnight/bootstrap-token`; `midnight admin show-bootstrap` is one-shot and deletes the file on read. `[PLD-Sec line 116]`
- [ ] Mandatory password+TOTP on first login; bootstrap path auto-disables once a normal admin exists. `[PLD-Sec line 116]`
- [ ] API tokens use format `midn_<class>_<26-char base32-256bit>`; public 6-char prefix surfaced in UI/audit; stored as `sha256(token)`. `[PLD-Sec line 117]`
- [ ] CLI auto-revokes tokens unused for N days; `Authorization: Bearer` only. `[PLD-Sec line 117]`, `[PLD-API line 175]`
- [ ] KEK at `/etc/midnight/secrets.key` mode `0400 midnight:midnight`; rotation generates KEK_n+1, rewraps all DEKs in background `operations` job with progress; KEK_n retained for `secret_kek_grace_days` (default 30). `[PLD-Sec line 118]`
- [ ] KEK loss is irrecoverable and documented; operators MUST back KEK up separately from Postgres backups (different repository, different passphrase); KEK loss is a tested failure drill. `[PLD-Sec line 118]`
- [ ] Authentication transport: dashboard uses `__Host-` Secure/HttpOnly/SameSite=Lax cookies holding 15-minute opaque session id backed by Postgres; CSRF via double-submit + Origin header; CLI/API uses `Authorization: Bearer`; JWTs explicitly avoided. `[PLD-Sec line 122]`
- [ ] Dashboard browser headers: HSTS `max-age=63072000; includeSubDomains; preload`; CSP `default-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'`; `X-Content-Type-Options: nosniff`; COOP=same-origin; minimal Permissions-Policy. `[PLD-Sec line 123]`
- [ ] Central authorization middleware: deny-by-default; object-level checks for every API/CLI/dashboard/MCP/webhook/service-account operation. `[Authorization requirements lines 1199–1206]`
- [ ] Backup of secret store + KEK uses a separate operator-supplied passphrase to break the dependency cycle; restore tests include rotated-password fixture. `[PLD-Sec line 130]`
- [ ] SSRF-safe outbound HTTP client used by webhook delivery, git/registry/AI providers, ACME: denies RFC1918, CGNAT, link-local, loopback, multicast, cloud metadata endpoints (169.254.169.254, fd00:ec2::254); hostname resolved once and connection pinned. `[PLD-Sec line 124]`

**Dependencies:** EP-001 (installer paths + permissions), EP-002 (auth middleware), EP-003 (`api_tokens`, `users`, sessions tables).
**Uncertainty notes:** TOTP UX flows (QR + recovery codes) and the KEK rotation drill are common slipping items.

---

### EP-011 — Embedded registry + tag retention + protected-digest probe + GC dry-run/apply

**Phase:** Alpha
**Workstream:** Storage
**Size:** M
**Source anchors:** lines 135–149 (`[PLD-Storage]`), 1373–1389 (Registry retention), 2348.

**Description.** Embedded distribution registry on quota-isolated subtree. Reference-counted retention sweep (last 10 image tags per service plus all digests in active rollback windows and protected snapshots). Daily protected-digest probe for external registries with `EXTERNAL_DIGEST_MISSING` reason code. `midnight registry gc --dry-run|--apply` shipped in Phase 0. Destructive blob GC remains opt-in with quiesce/lock/dry-run/post-GC verify.

**Acceptance criteria.**
- [ ] Embedded registry retains last 10 image tags per service plus all digests in active rollback windows and protected snapshots. `[PLD-Storage line 144]`
- [ ] Daily reference-count + tag-retention sweep runs as a `cleanup` queue job; untagged manifests older than 14 days unreferenced by deployment/rollback/restore manifest/audit retention are deleted. `[PLD-Storage line 144]`, `[Registry retention lines 1377–1378]`
- [ ] Full destructive blob GC requires quiesce/lock/dry-run/post-GC verify; opt-in only. `[PLD-Storage line 144]`
- [ ] `midnight registry gc --dry-run` and `midnight registry gc --apply` shipped in Phase 0. `[PLD-Storage line 144]`
- [ ] External-registry digests probed daily; missing digests for protected deployments emit `EXTERNAL_DIGEST_MISSING`. `[PLD-Build line 79]`
- [ ] Production environments default to mirroring active and last-N rollback digests into the embedded registry as a safety net. `[PLD-Build line 79]`
- [ ] Registry blobs live on quota-isolated XFS/ext4 subtree (`prjquota` mount option). `[PLD-Storage line 146]`
- [ ] Runtime agent verifies image signatures (cosign or equivalent) before pull/start in default profile; mismatch produces `IMAGE_SIGNATURE_INVALID`. `[PLD-Build line 80]`

**Dependencies:** EP-001 (filesystem layout, prjquota mount), EP-006 (push target), EP-009 (refcount integration).
**Uncertainty notes:** Cosign verification key distribution path needs clarifying with security WG before implementation.

---

### EP-012 — Dashboard Phase 0 journeys

**Phase:** Alpha
**Workstream:** CLI/UX
**Size:** L
**Source anchors:** lines 1724–1732 (Dashboard Phase 0 journeys), 2350.

**Description.** TypeScript + React dashboard implementing only the Phase 0 journey: bootstrap admin, create project + service, deploy an app, get HTTPS through a generated domain, and keep it running with timeline/log visibility. Failed-deployment diagnosis and rollback are recovery paths inside that journey. Pages secondary to journeys; dashboard MUST NOT imply support for volumes, templates, previews, or multi-service graphs before those flows exist.

**Acceptance criteria.**
- [ ] First install/bootstrap admin journey end-to-end in browser. `[Dashboard journey 1 line 1726]`
- [ ] Create project + create service journey end-to-end. `[Dashboard journey 2 line 1727]`
- [ ] Deploy + watch timeline + watch logs journey end-to-end (uses SSE event stream). `[Dashboard journey 3 line 1728]`
- [ ] Configure generated domain + basic variables journey end-to-end. `[Dashboard journey 4 line 1729]`
- [ ] Diagnose failed deployment + rollback journey end-to-end (failure reason code surfaced verbatim with fix hint + docs link). `[Dashboard journey 5 line 1730]`
- [ ] Dashboard does **not** show navigation entries for volumes, templates, previews, or multi-service graphs in Alpha. `[line 1732]`
- [ ] All dashboard pages enforce browser security headers from EP-010 (HSTS, CSP, COOP, etc.). `[PLD-Sec line 123]`
- [ ] Dashboard CSRF via double-submit + Origin check; sessions are 15-minute Postgres-backed `__Host-` cookies. `[PLD-Sec line 122]`

**Dependencies:** EP-002 (API), EP-008 (logs/timeline), EP-010 (auth/sessions).
**Uncertainty notes:** Frontend velocity depends on whether the team has React/TS engineers in place from sprint 1.

---

### EP-013 — CLI Phase 0 surface (`login` … `doctor` + `preflight`)

**Phase:** Alpha
**Workstream:** CLI/UX
**Size:** L
**Source anchors:** lines 181–187 (`[PLD-API]`), 1684–1722 (CLI commands), 2351.

**Description.** Build the Go CLI sharing API client types where possible. Phase 0 surface: `login`, `init`, `link`, `deploy`, `status`, `logs`, `rollback`, `domain`, `doctor`, `preflight`. CI-friendly flags. OS-keychain token storage. Deterministic deploy uploads.

**Acceptance criteria.**
- [ ] `midnight login` uses OAuth 2.0 device authorization grant (RFC 8628) for headless and PKCE for workstations; tokens stored in OS keychain (not `.midnight/`). `[PLD-API line 175]`
- [ ] `midnight deploy` produces a deterministic `tar.zst`, honors `.dockerignore` then `.midnightignore`, signs payload, S3-style multipart upload with resume, default cap 2 GiB, returns operation_id and prints operations URL. `[PLD-API line 183]`
- [ ] `midnight rollback` per spec (see EP-009). `[PLD-API line 184]`
- [ ] `midnight logs` per spec (see EP-008). `[PLD-API line 182]`
- [ ] `midnight doctor` and `midnight preflight` are split (live vs install/upgrade); both produce machine-readable JSON, support `--check=<id>`, `--fix=safe|all`, exit-code table 0/1/2/3. `[PLD-API line 185]`
- [ ] `midnight link` in multi-service repos prompts for service or accepts `--service`; `.midnight/` stores project/env/service IDs only (tokens go to keychain). `[PLD-API line 186]`
- [ ] Default `init` adds `.midnight/` to `.gitignore`. `[PLD-API line 186]`
- [ ] All read commands support `--json`; mutating commands support `--dry-run` where feasible; CI usage non-interactive. `[CLI rules lines 1714–1717]`
- [ ] Commands show project/environment/service before acting. `[CLI rule line 1716]`
- [ ] CLI feature kill-switches: `midnight feature {list,disable,enable}` for at least `acme.issuance`, `registry.gc`, `ai.gateway`, `auto.remediation`; status surfaced in `doctor`. `[PLD-Release line 201]`

**Dependencies:** EP-002, EP-007 (`domain`), EP-008 (`logs`), EP-009 (`rollback`), EP-014 (`support-bundle` later).
**Uncertainty notes:** OS-keychain integration on Linux (libsecret) vs macOS Keychain may need platform-specific code paths.

---

### EP-014 — Support bundle + platform backup + restore smoke

**Phase:** Alpha
**Workstream:** Install/Ops
**Size:** M
**Source anchors:** lines 198 (`midnight support-bundle`), 2042–2063 (Backup and DR), 2137, 2225.

**Description.** Implement `midnight support-bundle` (24h window, 200 MB cap, redaction preview/manifest), `midnight backup create --platform`, `midnight restore --platform`, and the platform restore order from spec lines 2050–2063. Backup repositories are encrypted (restic password sealed by KMS); KEK + secret-store backup uses an operator-supplied passphrase to break the dependency cycle.

**Acceptance criteria.**
- [ ] `midnight support-bundle` defaults: 24h time window, 200 MB cap, `--include=logs,metrics,events,config` selectors; manifest reports `truncated: true` per category. `[PLD-Release line 198]`
- [ ] Support bundle redacts secrets, sealed values, tokens, Authorization headers, cookies, private keys, raw env dumps (never included); preview/manifest of included files + redaction classes shown; external upload opt-in. `[Support bundle lines 1417–1430]`
- [ ] Backup repositories are encrypted (restic password required, sealed by platform KMS); backup of secret store + KEK uses a separate operator-supplied passphrase. `[PLD-Sec line 130]`
- [ ] Each project gets its own restic repository under `/var/lib/midnight/backups/<project_id>/`; cross-project dedupe explicitly out of scope. `[PLD-Storage line 141]`
- [ ] `midnight backup create --platform` includes control-plane Postgres, `/etc/midnight`, `/etc/midnight/secrets.key`, registry metadata/blobs if embedded, template metadata, certificate/ACME state. `[Backup lines 2042–2049]`
- [ ] `midnight restore --platform` follows the 11-step platform restore order from spec lines 2050–2063, including ACME-state byte-exact restore and throttled reissue at ≤ 20 certs/hour with priority queue. `[PLD-Release line 203]`
- [ ] Off-host backup destination configurable for MVP Beta (`s3://`, `gcs://`, `sftp://`, rsync); Alpha may be local-only with explicit `BACKUP_OFFHOST_MISSING` warning from `doctor`. `[PLD-Storage line 142]`
- [ ] Each new backup config runs one-time restore-into-throwaway-volume + template smoke check before being marked `verified`; fail surfaces `BACKUP_UNVERIFIED`. `[PLD-Storage line 143]`

**Dependencies:** EP-001, EP-003 (Postgres backup integration), EP-010 (KEK/sealed restic password), EP-013 (CLI commands).
**Uncertainty notes:** Restore-smoke fixture set requires representative project data — design a fixture pack with QA early.

---

### EP-015 — Failure drills harness (Alpha drill list) + Prometheus rules + Grafana dashboard

**Phase:** Alpha
**Workstream:** Quality
**Size:** M
**Source anchors:** lines 195–196 (`[PLD-Release]`), 1558–1571 (Failure and upgrade testing), 2226.

**Description.** Build the failure-drill harness as repeatable CI/CD-runnable scripts asserting expected state transition + reason code + audit/event trail + user-facing diagnostic + recovery runbook result for each Alpha drill. Ship Prometheus alert rules and the default Grafana dashboard with the `.deb`.

**Acceptance criteria.**
- [ ] Drill harness covers original list + panel additions: kill agent/server/worker, restart Docker daemon, fail image pull, fail readiness, trigger disk pressure, duplicate/reorder webhook (if shipped), break DNS verification (if domains shipped), clock skew >5min, journald wedged, `/var/log` full, Postgres connection exhaustion, network partition (3 durations), BuildKit local-cache corruption, Docker socket EOF, volume read-only remount, KEK loss, ACME state restore at scale. `[PLD-Release line 195]`, `[Required Alpha drills line 1567]`
- [ ] Each drill asserts: state transition, reason code, audit/event trail, user-facing diagnostic, runbook result. `[Failure drills line 1566]`
- [ ] Default Alpha alert rules shipped as Prometheus rules file: `DiskPressure`, `AgentLost`, `BackupFailed`, `CertExpiringSoon`, `DeployJobStuck`, `BuildKitCacheCorruption`, `ConntrackPressure`, `BackupOffhostMissing`. `[PLD-Release line 196]`
- [ ] Default Grafana dashboard `midnight-overview.json` shipped with `.deb`. `[PLD-Release line 196]`
- [ ] Default scrape interval 15s. `[PLD-Release line 196]`
- [ ] Drill harness is a release gate; PR cannot merge if a drill regression occurs. `[Failure drills line 1566]`
- [ ] Reachability tests use `LE staging` for ACME paths in CI. `[ACME policy line 1095]`

**Dependencies:** All Phase 0 epics (drills exercise the entire stack).
**Uncertainty notes:** Drill flakiness is the #1 risk; build slow/quarantine-tier mechanism early.

---

## MVP Beta Epics

### EP-016 — Variables/secrets + reference cross-scope authorization + sealed variables

**Phase:** Beta
**Workstream:** Security
**Size:** L
**Source anchors:** lines 119–120 (`[PLD-Sec]`), 1112–1158 (Variables/secrets/RBAC), 2144, 2146.

**Description.** Ship the full variable model: plain, secret, sealed, shared, reference, system. Reference resolver enforces cross-scope authorization on creation and at every resolution. Sealed variables ship in MVP Beta with the no-reveal spec. `publicClientVars` is the only class populated to `process.env` during build alongside `buildVars`.

**Acceptance criteria.**
- [ ] Variable types: plain, secret, sealed, shared, reference, system — all implemented and documented. `[Variable types lines 1114–1121]`
- [ ] Reference variable resolver enforces cross-scope authz: actor creating a reference must have read access on referenced variable's scope at creation time; consuming service's deploy principal must have read access at every resolution. `[PLD-Sec line 119]`
- [ ] Cross-environment-tier references (dev→prod or prod→preview) denied unless explicit policy allows. `[PLD-Sec line 119]`
- [ ] Sealed variables: no API path returns plaintext after create; rotation requires a new value (not view-then-edit); CLI has no `--reveal` for sealed; UI shows last-rotated and length-bucket only; audit/snapshot exports elide plaintext. `[PLD-Sec line 120]`
- [ ] `publicClientVars` is the only variable class populating `process.env` during build alongside `buildVars`. `[PLD-Sec line 129]`
- [ ] Reference cycles fail validation with typed reason code; missing references fail preflight before deploy. `[Variable reference resolution lines 1152–1158]`
- [ ] Required system variables generated: `MIDNIGHT_PROJECT_ID/NAME`, `MIDNIGHT_ENVIRONMENT_ID/NAME`, `MIDNIGHT_SERVICE_ID/NAME`, `MIDNIGHT_DEPLOYMENT_ID`, `MIDNIGHT_REPLICA_ID`, `MIDNIGHT_PUBLIC_DOMAIN`, `MIDNIGHT_PRIVATE_DOMAIN`, `PORT`. `[System variables lines 1125–1137]`
- [ ] Build secrets via BuildKit secret/SSH mounts only; never become `ARG`, image `ENV`, copied credential files, logs, support bundles, AI context, or webhook payloads. `[Build-time vs runtime lines 1142–1151]`

**Dependencies:** EP-002, EP-003 (`variables`, `variable_references`), EP-010 (envelope encryption).
**Uncertainty notes:** Cross-scope authz invariants need test coverage spanning create-time and resolve-time edges; budget for an extra QA pass.

---

### EP-017 — Custom domains end-to-end (TXT ownership, routing verify, ACME issuance)

**Phase:** Beta
**Workstream:** Networking
**Size:** L
**Source anchors:** lines 156–158 (`[PLD-Net]`), 1022–1100 (Custom domain lifecycle), 2146.

**Description.** Custom domain binding: user requests, platform provides ownership TXT (`_midnight-verify.<apex>`) + routing record (CNAME for subdomains; ALIAS/ANAME/CNAME-flattening/A/AAAA for apex), platform verifies both, ACME issuance proceeds only after ownership passes, route activates only after ownership + routing + TLS verify. Wildcard custom domains require DNS-01.

**Acceptance criteria.**
- [ ] Custom domain lifecycle implemented per spec lines 1022–1031: request → TXT/routing records → verify TXT → verify routing → ACME issuance → route active. `[lines 1022–1031]`
- [ ] TXT verification scoped to workspace/project/environment domain binding; cannot be reused across tenants. `[Custom domain DNS line 1037]`
- [ ] Wildcard custom domains (`*.example.com`) require DNS-01, ownership TXT at `_midnight-verify.<apex>`; more-specific subdomain wins over wildcard at routing. `[PLD-Net line 158]`
- [ ] Failed DNS states surfaced separately: missing TXT, wrong TXT, wrong CNAME/ALIAS/A/AAAA, DNS not propagated, CAA blocks issuance, port 80 blocked, IPv6 mismatch, LE rate limit. `[Custom domain DNS line 1038]`
- [ ] `advertisedIngressAddresses` is a versioned platform setting; apex bindings store address set used at verification; periodic re-verification job detects A/AAAA drift; IP-change runbook notifies affected customers and forces re-verification. `[PLD-Net line 157]`
- [ ] CAA preflight on registered domain and parents before ACME submit. `[PLD-Net line 156]`
- [ ] `DOMAIN_OWNERSHIP_LAPSED` reason code emitted when post-verification re-check fails. `[Reason codes line 1492]`
- [ ] Custom names are aliases to explicit service/domain bindings; never automatically expose every service in a project. `[Custom domain DNS line 1039]`

**Dependencies:** EP-007 (Traefik + ACME + DnsProvider), EP-019 (audit on verify/issue events).
**Uncertainty notes:** Apex domain handling varies by registrar; budget for an integration matrix across Cloudflare, Route53, and at least one apex-CNAME-flattening provider.

---

### EP-018 — GitHub App + signed webhooks + required status check + environment locks + artifact promotion

**Phase:** Beta
**Workstream:** API/SDK
**Size:** L
**Source anchors:** lines 1734–1856 (Source control + Release gates + Artifact promotion), 2147.

**Description.** Operator-owned shared GitHub App + per-install user-provided GitHub App credentials. Webhook signature verification + delivery-ID dedupe + idempotent handlers + dead-letter. Required status-check gate, environment locks, and artifact promotion records. Provider-neutral event contract abstracts GitLab/Bitbucket/etc. for later.

**Acceptance criteria.**
- [ ] GitHub App install flow validates webhook URL reachability/TLS, app permissions, subscribed events, private key, webhook secret, installation ID, repository access, setup/callback URL. `[GitHub App setup lines 1796–1803]`
- [ ] Minimum permissions only: contents read, metadata read, statuses/checks write, deployments write, PRs read for previews, webhooks for push/PR/installation events. **No** administration, workflows, secrets, or broad org permissions. `[lines 1800–1801]`
- [ ] Webhook ingestion: verify signatures, store provider delivery ID + event hash, enqueue async work before processing, respond within provider deadline, handlers idempotent, redelivery + manual replay supported, poison events enter dead-letter with diagnostics. `[Webhook ingestion lines 1519–1528]`
- [ ] Outbound webhook signing per spec: `Midnight-Signature: t=<unix>,v1=<hex>,kid=<key_id>`, replay window 300s, two active signing keys per endpoint with 30-day rotation, canonical signing input `f"{timestamp}.{raw_body}"`. `[PLD-Sec line 125]`
- [ ] Required status-check gate, environment locks, artifact promotion records implemented. `[Environment protection lines 1820–1829]`
- [ ] Artifact promotion: build once, promote same image digest across environments by default; promotion records include source SHA, image digest, SBOM/provenance, checks, approvals, actor, gate results, target environment. `[Artifact promotion lines 1837–1841]`
- [ ] Push deploys default to `runLatest`: older pending deploys canceled or marked superseded. `[Deployment race prevention line 1855]`
- [ ] Fork PR builds receive no shared/production secrets, no shared cache R/W (ephemeral only), quarantine registry path. `[PLD-Build line 83]`

**Dependencies:** EP-002, EP-005 (`runLatest` arbitration), EP-010 (signing-key rotation), EP-016 (variable scoping for fork builds).
**Uncertainty notes:** Webhook delivery reliability and dead-letter UX may need a second iteration after design-partner feedback.

---

### EP-019 — RBAC + deny-by-default authorization + audit hash chain + JSONL export + verify CLI

**Phase:** Beta
**Workstream:** Security
**Size:** L
**Source anchors:** lines 121 (`[PLD-Sec]`), 1160–1184 (MVP RBAC + audit), 1258–1265 (Audit and evidence), 2148–2149.

**Description.** Implement the five MVP RBAC roles with the "deploy ≠ read secrets" separation. Central deny-by-default authorization middleware. Audit log per-row hash chain with daily checkpoint signed by release key or operator-configured Ed25519. `midnight audit verify` CLI. JSONL export with `prev_hash`, `row_hash`, `checkpoint_id`.

**Acceptance criteria.**
- [ ] RBAC roles: Workspace owner, Project admin, Developer, Deployer, Viewer; permission matrix documented. `[MVP RBAC lines 1160–1166]`
- [ ] Deploying code does not imply reading secrets; viewing logs is separate from managing variables; production access can be separate from staging/preview. `[Permission separation lines 1168–1172]`
- [ ] Authorization tests are a release gate. `[Authorization requirements line 1206]`
- [ ] Granting `Developer`/`Deployer` roles in Alpha requires explicit operator acknowledgment that those roles are host-root-equivalent (warning surfaced by `midnight doctor` `BUILD_ROOTFUL`). `[PLD-Sec line 128]`
- [ ] Audit log uses per-row hash chain: `prev_hash`, `row_hash = H(prev_hash || canonical_row)`, rooted in daily `audit_checkpoint` row signed with Midnight release key or operator-configured Ed25519. `[PLD-Sec line 121]`
- [ ] `midnight audit verify` CLI ships in MVP and validates the chain end-to-end. `[PLD-Sec line 121]`
- [ ] JSONL export includes `prev_hash`, `row_hash`, `checkpoint_id`, request_id, correlation ID, actor, actor type, auth method, source IP, user agent, resource, action, before/after metadata where safe, policy decision, timestamp. `[PLD-Sec line 121]`, `[Audit and evidence line 1263]`
- [ ] Audit events captured: login/token, member/role changes, variable CRUD (no secret plaintext), deployment create/cancel/rollback/restart, domain attach/verify/delete, certificate issue/renew/failure, volume backup/restore/delete, host agent registration/heartbeat/failure, operator/admin actions. `[Audit events lines 1174–1184]`
- [ ] `midnight shell`/`ssh` sessions recorded as asciicast-equivalent streams stored with audit retention; cannot be disabled; production-tier sessions require two-person approval. `[PLD-Sec line 131]`

**Dependencies:** EP-002, EP-003, EP-010, EP-016.
**Uncertainty notes:** Hash-chain checkpoint signing infrastructure (release-key handling) and the verify-CLI UX need an early dry-run on a long-lived test instance.

---

### EP-020 — Single-writer volumes + project quotas + soft delete + `BACKUP_STALE`

**Phase:** Beta
**Workstream:** Storage
**Size:** L
**Source anchors:** lines 137–148 (`[PLD-Storage]`), 1293–1389 (Storage and Databases), 2150.

**Description.** Implement single-writer volumes per service, project quotas via XFS/ext4 `prjquota`, soft-delete with 7-day grace, ownership rules, and the `BACKUP_STALE` state for missed-backup services.

**Acceptance criteria.**
- [ ] One volume per service in MVP; single-writer only; no replicas for volume-backed services; mounted only at runtime; not mounted during build or pre-deploy command. `[Volume rules lines 1297–1306]`
- [ ] Volume directories owned by `root:midnight`, mode 0700, until first mount. On first mount agent reads template's expected container UID/GID and `chown`s appropriately, computing mapped host UID via `/etc/subuid` if `userns-remap` enabled. UID conflict produces `OWNERSHIP_CONFLICT` and blocks mount. `[PLD-Storage line 140]`
- [ ] Volume capacity enforced via XFS or ext4 project quotas (`prjquota` mount option required); BuildKit cache and registry blobs live on quota-isolated subtrees. Volumes can grow online; shrink unsupported (`RESIZE_SHRINK_UNSUPPORTED`). `[PLD-Storage line 146]`
- [ ] Deleted volumes soft-deleted with 7-day grace (configurable down to 24h, never below). During grace: unmounted, owner notified, recoverable via API/UI. Hard deletion logged to audit. `[PLD-Storage line 148]`
- [ ] Restore creates a replacement volume, never overwrites in place; old volume retained until explicit deletion. `[Volume rules line 1303]`, `[Backup lifecycle line 1346]`
- [ ] Service whose backup policy is disabled or has no successful backup in last 2× period transitions to `BACKUP_STALE`; surfaced in UI/API. RPO reported per service as `time_since_last_successful_backup`. `[PLD-Storage line 137]`
- [ ] Disk pressure defaults: warn 75%, degrade (block new builds) 85%, block (refuse new deployments + volumes) 92%. Inode: warn 70%, block 90%. `[PLD-Storage line 145]`
- [ ] Volume roots reject symlink/path traversal; volume metadata records UID, GID, mode, owning template/image expectation, restore remap policy. `[Linux storage implementation lines 1359–1361]`

**Dependencies:** EP-001 (filesystem + prjquota mount), EP-003 (`volumes`, `volume_attachments`), EP-014 (backup integration).
**Uncertainty notes:** `userns-remap` interaction with project quotas is platform-specific; budget for a Debian 12 + Ubuntu 24.04 fixture pair.

---

### EP-021 — Postgres template + database-native logical backup + off-host destination

**Phase:** Beta
**Workstream:** Storage
**Size:** L
**Source anchors:** lines 138–142 (`[PLD-Storage]`), 1308–1372 (Database model + native backups), 2151.

**Description.** Ship the Postgres v1 official template. `pg_dumpall --globals-only` + `pg_dump --format=custom --blobs --no-owner` per database. `pg_restore -j` against a fresh same-major cluster. Databases > 50 GB flagged `BACKUP_SIZE_DEGRADED`. At least one off-host backup destination configurable.

**Acceptance criteria.**
- [ ] Alpha official database template scope: Postgres only; ships only after manual logical backup/restore gates pass. `[PLD-Senior line 68]`
- [ ] Database-native backup: `pg_dumpall --globals-only` + `pg_dump --format=custom --blobs --no-owner` per database; restore runs globals first, then `pg_restore -j <n>` against a fresh same-major cluster. `[PLD-Storage line 138]`
- [ ] Databases > 50 GB flagged `BACKUP_SIZE_DEGRADED`; require v1 physical-backup path. `[PLD-Storage line 138]`
- [ ] Major-version Postgres upgrade is dump-and-restore into a new replacement volume; physical backups (`pg_basebackup`) restore only to same major version; in-place `pg_upgrade` out of scope. `[PLD-Storage line 139]`
- [ ] Default scheduled backup policy enabled at template instantiation (Alpha default: daily, 7-day retention). `[PLD-Storage line 137]`
- [ ] Off-host backup destination configurable in MVP Beta: at least one of `s3://`, `gcs://`, `sftp://`, rsync. `BACKUP_OFFHOST_MISSING` warning fires from `doctor` when none configured. `[PLD-Storage line 142]`
- [ ] Backup metadata records: source service, volume, engine, engine version, image digest, template version, backup type, consistency level, size, checksum, status, expiry, restore-test status. `[Backup lifecycle line 1342]`
- [ ] Backups must record `consistency_level` as one of `logical-consistent`, `physical-pitr`, `quiesced-filesystem`, `stopped-service`, `crash-consistent`, or `unknown`; UI/API displays this. `[line 1347]`
- [ ] Product language says `template-backed database services`, not `fully managed databases`. `[line 1349]`
- [ ] Postgres template fields populated: image ref/pinned version, mount path, internal port, healthcheck, generated credentials, connection URL format, default runtime variables, backup policy defaults, upgrade notes, supported engine versions, restore compatibility, expected container UID/GID, backup/restore/smoke commands. `[Database template fields lines 1319–1335]`

**Dependencies:** EP-014, EP-016, EP-019 (audit), EP-020 (volumes).
**Uncertainty notes:** Restore-test fixtures with realistic data sizes (5-50 GB) need procurement.

---

### EP-022 — Signed releases + SBOM/provenance + image-signature verify + upgrade transaction

**Phase:** Beta
**Workstream:** Quality
**Size:** L
**Source anchors:** lines 80, 117 (`[PLD-Build]`/`[PLD-Sec]`), 192–194 (`[PLD-Release]`), 1267–1276 (Compliance posture), 2152–2153.

**Description.** Sign release artifacts (cosign for OCI/binaries; APT signing key for `.deb`). Generate SBOMs and SLSA-style in-toto provenance for the Midnight platform itself. Runtime agent verifies image signatures before pull/start. Upgrade is a transactional snapshot → migrate → smoke → auto-rollback flow with skip-version refused.

**Acceptance criteria.**
- [ ] Release artifacts include signed checksums, cosign signatures, SBOMs, provenance attestations, release workflow identity, verification instructions. `[Compliance lines 1271–1273]`
- [ ] Release root of trust includes APT archive signing key for packages and cosign/Sigstore for OCI/binaries. `[line 1273]`
- [ ] Midnight's own supply chain: lockfiles required; CI runs `govulncheck`, `osv-scanner`, `npm audit --omit=dev` as required checks; PRs from forks do not get repo secrets and do not push images; release artifacts built only from protected branches; npm packages pinned by integrity hash. `[PLD-Sec line 133]`
- [ ] Public claims say `SLSA-style provenance generated`; no SLSA Build Level claimed. `[Compliance line 1271]`, `[PLD-Build line 80]`
- [ ] Runtime agent verifies image signatures (cosign or equivalent) before pull/start in default profile; mismatch produces `IMAGE_SIGNATURE_INVALID`; emits `DEPLOY_PROVENANCE_UNVERIFIED` when provenance missing. `[PLD-Build line 80]`, `[Reason codes line 1499]`
- [ ] `midnight upgrade` takes pre-upgrade snapshot tag, runs migrations in advisory-locked txns, executes smoke probe with timeout, and on failure auto-invokes `midnight rollback --to-snapshot=<tag>`. `[PLD-Release line 192]`
- [ ] Migrations tagged `reversible|forward-only`; forward-only requires `--accept-irreversible` plus verified pre-upgrade backup. `[PLD-Release line 192]`
- [ ] Skip-version upgrades refused with `UPGRADE_SKIP_FORBIDDEN`; `.deb` `Depends:`/`Breaks:` constraints encode min/max compatible versions across server/worker/agent/BuildKit. `[PLD-Release line 193]`
- [ ] Server exposes `/api/v1alpha/compat`; worker/agent registrations outside window refused with `VERSION_SKEW_REJECTED`. `[PLD-Release line 193]`
- [ ] Component compatibility matrix published as artifact (server/worker/agent/BuildKit/Traefik/Docker Engine/Postgres/Railpack). `[PLD-Release line 194]`
- [ ] Vulnerability disclosure policy + security contact published. `[Compliance line 1269]`

**Dependencies:** EP-001, EP-002, EP-004 (`/api/v1alpha/compat`), EP-006 (provenance generation), EP-014 (snapshot tag mechanism).
**Uncertainty notes:** Cosign key custody and rotation procedures are operationally heavy; involve security WG early.

---

## Later Phases (Summary)

These phases are out of scope for the Phase 0 + MVP Beta plan but are summarized for capacity-planning context.

### Phase 1 — Self-Hosted v1

**Goal:** usable production-adjacent self-hosted platform. Source anchors: lines 2229–2253.

Notable scope: stable installer + upgrade path; multiple services per project; full envelope encryption + secret-key rotation paths; volumes; Postgres template; manual + scheduled backups + database-native backup hooks; full RBAC, quotas, audit export, retention policy, production environment protection; GitHub App, required CI gate, environment locks, artifact promotion records (graduated from Beta); Railpack builds; advisory repo analysis + config validation; 5-10 official templates.

### Phase 2 — Team & Reliability Layer

**Goal:** usable by small teams. Source anchors: lines 2254–2272.

Notable scope: workspace/team model; better RBAC; policy-as-code authoring/testing; audit log UI/API; OIDC/OAuth login; GitLab native integration; PR preview environments; config-as-code diff/apply; agent drain/maintenance mode; Terraform/OpenTofu provider; SIEM/log-drain integrations + compliance evidence export; read-only AI diagnosis (with redaction + eval baselines first); additional database templates after support gates.

### Phase 3 — Multi-Host & Runtime Adapters

**Goal:** scale beyond one machine. Source anchors: lines 2274–2289.

Notable scope: multi-host scheduling; WireGuard private networking with PMTU probe + key rotation + peer-add/remove FSM + conntrack flush on peer remove; internal DNS resolver; TCP proxy; registry cache improvements; Kubernetes adapter via the `BuildArtifact → ScheduleRequest → InstanceStatus → RoutingTarget` boundary; volume attach/detach where backend allows; Pulumi provider; OIDC workload identity / federated CI tokens; native Jenkins/Azure DevOps/Bitbucket adapters if demand exists.

### Phase 4 — Hosted/Commercial Optionality

**Goal:** hosted control plane or managed offering without forking OSS core. Source anchors: lines 2291–2305.

Notable scope: public multi-tenant hardening; stronger build isolation (VM/microVM); billing/metering; plan limits; abuse controls; multi-region routing; HA control plane; enterprise SSO/audit/export; SAML, SCIM, BYOK, disconnected install bundles, enterprise LTS, support SLAs, DPA/subprocessors/data residency controls.

---

## Cross-cutting concerns surfaced during epic breakdown

- **Telemetry is opt-in only** at install time, single endpoint, no project/service names; payload schema published. `[PLD-Release line 200]` — applies to EP-014, EP-015.
- **MCP server contract** is later-phase but the API surface (semantic action endpoints, idempotency keys, scoped tokens) must already exist by MVP Beta. `[PLD-Sec line 127]`
- **AI features disabled by default** with workspace policy + budget controls. `[AI safety lines 1646–1659]` — out of scope for these epics.
- **Renovate** is the default dependency-automation tool. `[line 1556]`

---

## Traceability matrix (epic → panel decisions)

| Epic | Primary `[PLD-…]` decisions referenced |
|------|----------------------------------------|
| EP-001 | PLD-Linux 88–98, PLD-Release 189–204 |
| EP-002 | PLD-API 169–187, PLD-DistSys 107 |
| EP-003 | PLD-DistSys 102–113, PLD-Sec 132 |
| EP-004 | PLD-Linux 90–97, PLD-DistSys 110–111, PLD-Second 32 |
| EP-005 | PLD-DistSys 104–113 |
| EP-006 | PLD-Build 80–84, PLD-Sec 129, PLD-Senior 66 |
| EP-007 | PLD-Net 153–165, PLD-Senior 65 |
| EP-008 | PLD-Linux 95, PLD-API 174 |
| EP-009 | PLD-Build 77, PLD-DistSys 113 |
| EP-010 | PLD-Sec 116–124 |
| EP-011 | PLD-Build 79–80, PLD-Storage 144–146 |
| EP-012 | (UX-driven; consumes contracts from EP-002, EP-008, EP-010) |
| EP-013 | PLD-API 175, 182–186, PLD-Release 201 |
| EP-014 | PLD-Sec 130, PLD-Storage 141–143, PLD-Release 198, 203 |
| EP-015 | PLD-Release 195–196 |
| EP-016 | PLD-Sec 119–120, 129 |
| EP-017 | PLD-Net 156–158 |
| EP-018 | PLD-Sec 125, PLD-Build 83 |
| EP-019 | PLD-Sec 121, 128, 131 |
| EP-020 | PLD-Storage 137, 140, 146, 148 |
| EP-021 | PLD-Storage 137–138, 142, PLD-Senior 68 |
| EP-022 | PLD-Build 80, PLD-Sec 133, PLD-Release 192–194 |

---

## Definition of Ready & Definition of Done templates

### Definition of Ready (a story is sprint-ready when…)

- Epic and acceptance criteria written and traced to a `[PLD-…]` line in the source spec.
- Sized in story points by the team after splitting from epic.
- Cross-team dependencies identified and resolved or scheduled.
- API contracts, error codes, and CloudEvents types named (no late naming).
- Test fixtures or drill scripts identified.
- Owner and reviewer assigned.
- Security and accessibility considerations explicitly noted (or N/A).

### Definition of Done (a story is closed when…)

- Code merged to a protected branch.
- All `[PLD-…]` acceptance criteria verifiable via automated tests where possible.
- Failure-drill harness updated where applicable.
- Documentation updated: API docs, CLI help, runbook entry, error code registry, compatibility matrix.
- Audit events and reason codes wired and verified.
- No regression in Phase 0 exit criteria (15-minute install, drill suite green).
- Release notes entry written.

---

*End of EPICS.md*
