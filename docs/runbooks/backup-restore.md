# Backup And Restore Runbook

Midnight has two real recovery paths with different safety boundaries:

- Service volumes use the online `midnight volume backup` and restore
  workflow. A restore is staged into a replacement Docker volume; applying it
  atomically swaps the durable attachment and queues an immutable redeploy.
- The control plane uses `midnight-recovery`. It streams a PostgreSQL custom
  dump and an explicit set of filesystem roots into authenticated,
  AES-256-GCM encrypted objects. It runs outside the API because the API cannot
  safely restore its own database.

The older backup readiness/export packets remain release-planning evidence.
They are not proof that platform bytes were copied or restored. A completed
`midnight-recovery` command or a completed volume backup run is execution
evidence.

## Prerequisites

- Keep an off-host `s3://` destination for production recovery. `local://` and
  `file://` perform real I/O but do not survive loss of the host.
- Store the backup passphrase outside the Midnight host. The file must contain
  at least 16 bytes and should be mode `0600`.
- Keep the PostgreSQL DSN in a mode-`0600` file. The command passes it through
  `PGDATABASE`, never as a process argument or output field.
- Use `pg_dump` and `pg_restore` from a client version compatible with the
  server. The Debian package depends on `postgresql-client`.
- Complete and record service-volume backup IDs separately. Platform manifests
  can reference those IDs, but platform restore does not silently restore
  Docker volumes.

## Offline platform backup

First finish any required service-volume backups. Then stop every component
that can change PostgreSQL or platform filesystem state. PostgreSQL itself must
remain available for `pg_dump`.

```sh
sudo systemctl stop \
  midnight-server midnight-worker midnight-agent \
  midnight-buildkit-worker midnight-buildkit midnight-registry
```

Choose a unique backup ID. Reusing an ID is rejected if any destination objects
already exist. Include only roots that exist. Build caches and uploads are
reproducible and normally should not be archived.

```sh
sudo midnight-recovery create \
  --id 2026-07-27T2300Z \
  --destination s3://midnight-recovery/platform \
  --dsn-file /etc/midnight/db.dsn \
  --passphrase-file /root/midnight-recovery.passphrase \
  --root /etc/midnight=etc/midnight \
  --root /var/lib/midnight/registry=var/lib/midnight/registry \
  --root /var/lib/midnight/acme=var/lib/midnight/acme \
  --root /var/lib/midnight/audit=var/lib/midnight/audit \
  --volume-backup-id vbkp_example \
  --offline-confirmed
```

The command writes encrypted PostgreSQL, filesystem, and manifest objects,
followed by a plaintext commit index authenticated with a separately salted,
310,000-round PBKDF2-SHA-256 key. The slow derived key prevents the index from
becoming a cheap passphrase oracle. Partial object sets without a valid index
must not be treated as a completed backup.

Include the configured audit signing-key directory if it differs from the
default above. Its private key must survive with mode `0600` inside a `0700`
directory; signed database history without that key makes the worker refuse
startup. Keep its protected backup and independently trusted public keys
available off-host. Never attach private key bytes to recovery evidence.

Restart and validate the platform after the command succeeds:

```sh
sudo systemctl start \
  midnight-registry midnight-buildkit midnight-buildkit-worker \
  midnight-agent midnight-worker midnight-server
sudo midnight-doctor --live --json
```

## Restore drill: authenticate and stage

Staging is non-destructive. Use a new, empty directory outside every archived
root:

```sh
sudo midnight-recovery stage \
  --id 2026-07-27T2300Z \
  --destination s3://midnight-recovery/platform \
  --passphrase-file /root/midnight-recovery.passphrase \
  --staging-dir /var/lib/midnight-recovery/2026-07-27T2300Z
```

Staging authenticates the index and every encrypted object, checks ciphertext
and plaintext SHA-256 evidence, rejects unsafe tar paths and symlinks, and
emits the path to `stage.json`. A wrong passphrase or changed object fails
before destination state is touched.

## Destructive apply

Stop all Midnight services again. Verify the target database and filesystem
root carefully. The confirmation value must exactly equal the staged backup
ID:

```sh
sudo systemctl stop \
  midnight-server midnight-worker midnight-agent \
  midnight-buildkit-worker midnight-buildkit midnight-registry

sudo midnight-recovery apply \
  --stage-file /var/lib/midnight-recovery/2026-07-27T2300Z/stage.json \
  --target-root / \
  --dsn-file /etc/midnight/db.dsn \
  --offline-confirmed \
  --confirm-restore 2026-07-27T2300Z
```

Apply rechecks staged plaintext, extracts a fresh verified filesystem candidate,
retains each previous root as
`<path>.midnight-retained-<backup-id>`, and invokes `pg_restore` with
`--single-transaction --exit-on-error --clean --if-exists --no-owner
--no-acl`. If PostgreSQL restore fails, installed roots are rolled back and the
failed candidate is retained as `<path>.midnight-failed-<backup-id>`.

Every apply phase is recorded and fsync'd in `apply-journal.json` inside the
staging directory. Re-running the same apply after a crash rolls back a partial
filesystem rename before retrying. If the crash occurred at the PostgreSQL
commit boundary, Midnight safely replays the single-transaction `pg_restore`
and then marks the journal complete. Do not delete the staging directory while
an apply is incomplete.

If `pg_restore` loses its response after the transaction may have committed,
the journal records `database-outcome-unknown` and deliberately keeps the new
filesystem installed. Keep services offline and rerun the identical apply; a
successful idempotent restore completes the journal. Midnight rolls the
filesystem back only when the restorer can prove that PostgreSQL did not
commit.

Restore referenced service volumes through their own staged replacement/apply
workflow; the platform utility reports references but never guesses which
running service should receive them.

After restore:

```sh
sudo midnight admin migrate --no-prompt
sudo midnight-doctor --live --json
sudo systemctl start \
  midnight-registry midnight-buildkit midnight-buildkit-worker \
  midnight-agent midnight-worker midnight-server
```

Verify API health, one private service, one public route, deployment logs,
metrics, and the expected volume contents before removing retained roots or
the restore staging directory. Removal is a separate operator decision.

## Failure rules

- Never bypass an HMAC, object checksum, plaintext checksum, archive-path, or
  explicit-confirmation failure.
- Never place the staging directory inside an archived target root.
- Do not start services between platform filesystem replacement and
  PostgreSQL restore.
- A successful apply intentionally retains the previous filesystem. The
  utility does not claim a point-in-time database rollback after a successful
  PostgreSQL transaction.
- Destination credentials come from the existing backup destination credential
  chain. They are not persisted in the manifest, stage file, or command output.
