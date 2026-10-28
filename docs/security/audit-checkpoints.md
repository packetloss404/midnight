# Installed audit checkpoints

The deployment worker signs the durable audit chain with Ed25519. On startup it
creates the first checkpoint for existing, uncheckpointed history or catches up
after a missed UTC day. It then checks at UTC midnight. A day with no new rows
does not create an empty checkpoint. Startup with an empty chain retries after
one minute; transient signing/database failures also retry after one minute.

`MIDNIGHT_AUDIT_CHECKPOINT_ENABLED=true` is the default. Set it to `false`, or
pass `--audit-checkpoints=false`, only when intentionally running without this
signing service. `MIDNIGHT_AUDIT_SIGNING_KEY_PATH` / `--audit-signing-key-file`
selects the durable private key; the default is
`/var/lib/midnight/audit/signing-key.pem`.

Signing selects the previous checkpoint and current chain head under the same
Postgres transaction advisory lock as audit appends. Concurrent workers and
same-day restarts cannot create overlapping daily checkpoints. Each signing
pass has a 15-second context deadline, including lock acquisition. Shutdown
cancels the scheduler and joins it before the database pool closes. Catch-up
records the actual signing time; it does not fabricate historical signatures
for days when the worker was offline. Timestamps are normalized to Postgres
microsecond precision before signing.

The compatible signature message authenticates partition label, start/end
timestamps, first/last row IDs, last-row hash, predecessor checkpoint ID, and
signing-key fingerprint. It does not authenticate the checkpoint's own numeric
ID. The existing signed-message format is retained; predecessor and coverage
conflicts are rejected when inserting a checkpoint.

## Key custody and recovery

The first worker boot creates the key only when no signed checkpoint history
exists. The history check and private-key load/create run under the same chain
transaction lock as checkpoint insertion, so another worker cannot create
signed history between that check and key publication.
The directory must be private (`0700`) and the key must be a regular
file with mode `0600`; symlinks, unsafe permissions, malformed keys, and
inconsistent Ed25519 private/public material are rejected on Linux. Creation
publishes a fully written, synced temporary file without replacing an existing
key, so concurrent first boots share the winning durable key.

The worker publishes a **public-only** PEM at
`/var/lib/midnight/audit/public-keys/<fingerprint>.pem` and logs its fingerprint.
The fingerprint is SHA-256 of the raw 32-byte public key, not the PEM file.
Exports, API responses, and JSON serialization of signing-key metadata do not
include private-key bytes. Keep the private key in a protected backup separate
from public audit exports. All signers for one chain must use the same securely
provisioned key/state, including workers on different hosts.

If signed history exists and the configured private key is missing, worker
startup fails. Restore that key from the protected backup with the original
ownership and permissions, then restart. The worker never silently regenerates
a replacement for signed history. Losing both the key and its protected backup
requires deliberate operator recovery and a newly pinned public key; it is not
an automatic repair.

There is no automatic rotation service or key-generation CLI. A deliberate
rotation requires stopping all signers, securely provisioning a new key in the
documented `MIDNIGHT AUDIT PRIVATE KEY` PEM format, pinning its matching public
key through the operator's trusted procedure, and restarting the signers. Keep
all previous trusted public keys: old checkpoints remain signed by their old
keys. Do not delete or rewrite checkpoint history to change keys.

## Independently trusted public keys

The API does not trust a key merely because an export or the worker's public
distribution directory contains it. Configure the server with
`MIDNIGHT_AUDIT_TRUSTED_PUBLIC_KEY_DIR` / `--audit-trusted-public-key-dir` pointing
to an operator-controlled directory of public `.pem` files. The Debian profile
uses `/etc/midnight/audit-trusted-keys`; the installer creates that directory
without copying or trusting generated keys automatically.

After checking the signer identity and fingerprint through a trusted operator
channel, install **only its public PEM**, for example:

```sh
audit_public_key='/var/lib/midnight/audit/public-keys/<fingerprint>.pem'
sudo install -o root -g midnight -m 0644 "$audit_public_key" \
  /etc/midnight/audit-trusted-keys/
```

The API reads configured public keys for each evidence request, allowing an
operator to add a newly pinned public key without restarting the API. It
rejects private PEMs in this directory. An empty or incorrect keyring produces
unverified evidence; an unreadable or malformed configured keyring produces
failed verification and `needs-review`, including when no rows remain. Neither
condition turns signature checks off.
On Linux, symlinked directories/files and group- or world-writable trust
directories/public PEMs are also rejected. Keep trust material operator-owned.

The local Compose profile keeps private signer state in the `midnight-audit-state`
volume and mounts an independent `midnight-audit-trusted` volume read-only in the API at
`/run/midnight-audit-trusted`. Copy a public PEM out of the worker, verify its
fingerprint, and populate the trusted volume through an operator-controlled
helper or volume-management procedure. Do not mount private signer state into
the API or assume the empty trusted volume authenticates a signer. Preserve
both volumes across container replacements; deleting private state after
checkpoints exist prevents the worker from starting.

For the default Compose project name, an explicit public-key pin can be made
with the following commands after checking the fingerprint. If you supplied a
different project name with `-p`, use that name for the volume prefix as well:

```sh
audit_fingerprint='replace-with-the-independently-verified-64-hex-fingerprint'
audit_public_key="$PWD/audit-public-$audit_fingerprint.pem"
audit_trusted_volume="${COMPOSE_PROJECT_NAME:-midnight-local}_midnight-audit-trusted"
docker compose -f docker-compose.local.yml cp \
  "deploy-worker:/var/lib/midnight/audit/public-keys/$audit_fingerprint.pem" \
  "$audit_public_key"
docker run --rm --network none \
  --mount "type=volume,source=$audit_trusted_volume,target=/trusted" \
  --mount "type=bind,source=$audit_public_key,target=/input.pem,readonly" \
  alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc \
  sh -c 'cp /input.pem /trusted/.public-new; chmod 0644 /trusted/.public-new; mv /trusted/.public-new "/trusted/$1.pem"' \
  sh "$audit_fingerprint"
```

## Export and verification

`GET /api/v1alpha/audit/export` contains rows, persisted checkpoints, and a
trailing manifest. The export uses one database snapshot; concurrently created
checkpoints cannot appear without the rows they cover. The JSONL reader checks
the stream hash, counts, checkpoint bounds, and final manifest before verifying
row hashes and checkpoint signatures.

Supply independently pinned public keys to the offline CLI:

```sh
midnight audit verify --from-jsonl ./audit.ndjson \
  --public-key-dir ./trusted-audit-public-keys --output json
```

The report includes `signature_status`, `signed_through_sequence`, and
`unsigned_rows`. New rows after the latest daily checkpoint are an unsigned
tail until the next successful checkpoint. A valid row hash chain alone is not
authenticated history: no-checkpoint verification without supplied keys reports
`signature_status: unsigned`; requesting trusted verification of nonempty
history with no checkpoints fails. A trusted checkpoint with later rows reports
partial signed coverage. Dashboard evidence reports `needs-review` for unsigned,
untrusted, or invalid history and includes the verifier's details.

The application audit API is append-only. This does not prevent a database
administrator from deleting history, rolling back the database, or replacing
the host and its key. Retain trusted public keys and previously observed
checkpoints independently/off-host to detect rollback or complete truncation.
This change does not implement off-host delivery, retention enforcement, or a
remote signing service.

The implementation uses Go's [Ed25519 API](https://pkg.go.dev/crypto/ed25519)
and PostgreSQL [transaction advisory locks](https://www.postgresql.org/docs/16/explicit-locking.html#ADVISORY-LOCKS).
