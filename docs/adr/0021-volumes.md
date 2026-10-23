# ADR-0021: Volumes — Docker Named Volumes for Single-Host Persistence

- Status: accepted
- Date: 2026-05-17
- Owners: volumes track

## Context

Midnight's schema has shipped a `volumes` table and `volume_attachments`
table since migration 0008, but the deploy worker did not actually mount
them on the container. Persistent storage is table-stakes for a Railway
clone: stateful services (Postgres, Redis, user uploads) MUST persist
across redeploys, and the volume row in the database has no meaning until
it materializes as a real on-host directory bind-mounted into the
container.

## Decision

For self-host single-host Midnight (Phase 1 alpha):

1. **Backing driver**: each Midnight volume is realized as a **Docker
   named volume** under the default `local` driver. The Docker volume
   name is the stable string `midnight-<vol_id>` where `<vol_id>` is the
   Postgres-allocated ULID. The name is stable across redeploys — the
   same `vol_*` id ALWAYS maps to the same Docker volume, so replacing
   the container preserves the data.

2. **Container-create wiring**: the runtime adapter folds attachments
   into `HostConfig.Mounts` with `Type: mount.TypeVolume`,
   `Source: midnight-<vol_id>`, `Target: <mount_path>`. The
   `ScheduleRequest.Mounts` field on the runtime boundary carries the
   already-materialized attachments from the volume `Manager` so the
   adapter never touches Postgres.

3. **Durable intent and writer lease**: `volumes.mount_path` is the
   authoritative mount target. Deploy jobs carry neither volume IDs nor mount
   paths; the worker loads both from PostgreSQL by exact
   `(project, service, environment)` scope. It reserves a `pending`
   attachment before provisioning, rechecks the same lease immediately before
   container create, and marks it `active` only after Docker scheduling
   succeeds. A partial unique index permits only one `pending` or `active`
   attachment per volume.

4. **Replicas constraint**: a service spec with `replicas > 1` AND a
   volume attachment is rejected at schedule time with the typed code
   `VOLUME_REPLICAS_UNSUPPORTED`. The Docker local driver is
   read-write-once on a single host; mirroring Railway's constraint we
   fail closed rather than risk concurrent-writer data corruption.

5. **Stateful redeploy**: local-volume redeploys are serial. The executor
   drains/stops the previous instance and records its attachment detached
   before the candidate acquires the writer lease. This creates a deliberate
   downtime window; it does not claim zero-downtime stateful deploys.

6. **Recovery**: worker startup inspects running Midnight-managed Docker
   containers and their named-volume mounts. Only observed running mounts are
   restored as `active`; stale pending/active rows are marked detached after a
   complete successful observation. API restarts recover mount intent directly
   from `volumes.mount_path`.

7. **Resize**: PATCH `/api/v1alpha/volumes/{id}` accepts `size_bytes`
   monotonically. Resize UP succeeds and updates the declared quota.
   Resize DOWN is rejected with `VOLUME_RESIZE_DOWN_UNSUPPORTED` —
   shrinking a backing volume without data loss requires a snapshot
   migration that is out of scope for Phase 1.

8. **Delete**: DELETE `/api/v1alpha/volumes/{id}` soft-deletes the
   volume row (7-day grace) and is rejected with `VOLUME_ATTACHED`
   when an attachment row in the `pending` or `active` state still
   references it. The reaper (Sprint 4 / S4-08) is responsible for the
   eventual hard-delete and Docker volume removal.

9. **Size enforcement is advisory**: the default Docker `local` driver
   does NOT enforce a per-volume size limit. Midnight records the
   declared size on the `volumes` row and on the Docker volume label
   set (`midnight.size_bytes`). True enforcement requires either:
   - mounting `/var/lib/midnight` on a quota-enabled filesystem (xfs
     with `prjquota` or ext4 with `quota`) and recording a
     `quota_project_id` per volume (Phase 1.9 already supplies this
     plumbing); or
   - swapping the Docker volume driver for one that supports
     per-volume quotas (ZFS, btrfs, NFS with quotas). The driver swap
     is an operator config knob and is intentionally out of scope.

## API surface

- `POST /api/v1alpha/volumes` — create a volume scoped to
  `(project_id, environment_id, service_id, name, size_bytes,
  mount_path)`. `mount_path` is stored on the authoritative volume row and
  copied onto each attachment as an immutable deployment snapshot.
- `GET /api/v1alpha/volumes` — list with optional `?project_id=`,
  `?service_id=`, `?environment_id=` filters.
- `GET /api/v1alpha/volumes/{id}` — get one.
- `PATCH /api/v1alpha/volumes/{id}` — resize. Body: `{"size_bytes": N}`.
  Resize DOWN: `409 VOLUME_RESIZE_DOWN_UNSUPPORTED`.
- `DELETE /api/v1alpha/volumes/{id}` — soft-delete. Active attachment:
  `409 VOLUME_ATTACHED`.

Every mutating endpoint requires the `Idempotency-Key` header per the
panel-locked REST contract.

## Out of scope (intentional)

- **Snapshots and backups** of volume contents (separate ADR).
- **Live volume migration** between services or hosts.
- **Zero-downtime stateful redeploys**. The single-host local driver uses a
  serial stop/detach/start boundary.
- **ZFS / btrfs driver swap** — leave as an operator config knob.
- **Multi-writer (ReadWriteMany)** semantics — Railway-parity is
  single-writer; multi-host distributed volumes require a real CSI
  driver and are out of scope for single-host Phase 1.
- **The OperationStore FSM** — volume actions are synchronous within a
  deploy operation, not separate long-running operations.
- **Reference-variable expansion** — `${{this.MIDNIGHT_VOLUME_MOUNTS}}`
  is a future enhancement; the consumer of a volume already knows its
  mount path from the deploy spec.

## Consequences

- Stateful templates (Postgres, Redis) now have real persistence: the
  template declares a `volumes` attachment, the API creates the row,
  the scheduler materializes the Docker volume, and the container
  mounts it. Redeploy: same `vol_*` id → same Docker volume → same
  data.
- Operators who care about quota enforcement get a panel-cited
  recovery hint pointing at `prjquota` on `/var/lib/midnight`. The
  hint is wired through `VolumeQuotaUnsupported` from the Phase 1.9
  store.
- The runtime boundary stays stable: `ScheduleRequest.Mounts` is
  additive next to the existing `Labels` and the (separate) network
  policy fields, so the private-networking track can extend
  `NetworkingConfig` without touching this code.
- Future work: ship the soft-delete reaper that drives `Docker volume
  remove` once the 7-day grace expires, and decide on a snapshot
  format (zstd-compressed tar to S3? off-host volume export?).

## Evidence and limits

- Unit tests cover scope/replica rejection before Docker writes, pending versus
  active truth, single-writer conflicts, serial takeover, runtime mount
  validation, and restart reconciliation from observed running containers.
- A PostgreSQL integration test applies the real migration and exercises
  durable mount reads plus pending/active/detached writer arbitration.
- A Docker integration test creates a labeled named volume through the
  production manager/driver, writes bytes from one real container, replaces
  the container, and reads the same bytes from the replacement.
- These tests prove API/worker process-restart recovery logic. They do **not**
  constitute a host-reboot or daemon-restart drill; that remains a required VM
  acceptance test.

## Links

- migrations/0008_volumes.up.sql — schema
- migrations/0021_volume_runtime_lifecycle.up.sql — durable mount intent and
  single-writer lease
- internal/volume/ — Store, Manager, DockerDriver
- internal/api/volumes.go — REST CRUD + resize/delete
- internal/runtime/docker/adapter.go — Schedule → HostConfig.Mounts
- internal/runtime/docker/volumes.go — Adapter.VolumeCreate /
  VolumeInspect / VolumeRemove (Phase 1.9 surface that this work
  consumes)
- ADR-009 — REST + OpenAPI as the public contract (no GraphQL gateway)
