#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Disposable destructive platform-recovery proof. This uses a real PostgreSQL
# 16 server plus pg_dump/pg_restore from that same image, destroys the source
# database and filesystem state, then restores through midnight-recovery.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POSTGRES_IMAGE="${MIDNIGHT_PARITY_POSTGRES_IMAGE:-postgres:16-alpine}"
EVIDENCE_ROOT="${MIDNIGHT_PARITY_EVIDENCE_DIR:-$ROOT_DIR/.midnight/parity-evidence}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
EVIDENCE_DIR="$EVIDENCE_ROOT/restore/$RUN_ID"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/midnight-restore-acceptance.XXXXXX")"
CONTAINER="midnight-restore-$RUN_ID"
BACKUP_ID="restore-$RUN_ID"
TARGET_ROOT="$WORK_DIR/target"
STAGING_DIR="$WORK_DIR/staging/$BACKUP_ID"
WRAPPER_DIR="$WORK_DIR/bin"
DSN_FILE="$WORK_DIR/postgres.dsn"
PASSPHRASE_FILE="$WORK_DIR/passphrase"
RECOVERY_BIN="$WORK_DIR/midnight-recovery"

for command_name in docker go python3; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "restore acceptance: required command '$command_name' was not found" >&2
    exit 78
  }
done
docker info >/dev/null 2>&1 || {
  echo "restore acceptance: Docker daemon is not reachable" >&2
  exit 78
}

mkdir -p "$EVIDENCE_DIR" "$WRAPPER_DIR"
chmod 0700 "$EVIDENCE_DIR" "$WORK_DIR" "$WRAPPER_DIR"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  if [[ -d "$WORK_DIR" && "$WORK_DIR" == "${TMPDIR:-/tmp}"/midnight-restore-acceptance.* ]]; then
    rm -rf -- "$WORK_DIR"
  fi
}
trap cleanup EXIT INT TERM

docker run -d --rm \
  --name "$CONTAINER" \
  -e POSTGRES_PASSWORD=midnight_restore_test \
  -e POSTGRES_DB=midnight \
  "$POSTGRES_IMAGE" >/dev/null

for attempt in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U postgres -d midnight >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "restore acceptance: PostgreSQL did not become ready" >&2
    exit 1
  fi
  sleep 1
done

cat >"$WRAPPER_DIR/pg_dump" <<'WRAPPER'
#!/usr/bin/env bash
set -euo pipefail
exec docker exec \
  -e PGDATABASE=midnight \
  -e PGUSER=postgres \
  -e PGPASSWORD=midnight_restore_test \
  "$POSTGRES_CONTAINER" pg_dump "$@"
WRAPPER
cat >"$WRAPPER_DIR/pg_restore" <<'WRAPPER'
#!/usr/bin/env bash
set -euo pipefail
arguments=("$@")
count="${#arguments[@]}"
(( count > 0 ))
dump_path="${arguments[$((count - 1))]}"
unset 'arguments[$((count - 1))]'
docker exec -i \
  -e PGDATABASE=midnight \
  -e PGUSER=postgres \
  -e PGPASSWORD=midnight_restore_test \
  "$POSTGRES_CONTAINER" \
  pg_restore "${arguments[@]}" <"$dump_path"
WRAPPER
chmod 0700 "$WRAPPER_DIR/pg_dump" "$WRAPPER_DIR/pg_restore"

printf '%s\n' \
  'postgresql://postgres:midnight_restore_test@127.0.0.1:5432/midnight?sslmode=disable' \
  >"$DSN_FILE"
printf '%s\n' 'correct horse battery staple for parity recovery' >"$PASSPHRASE_FILE"
chmod 0600 "$DSN_FILE" "$PASSPHRASE_FILE"

mkdir -p \
  "$TARGET_ROOT/etc/midnight" \
  "$TARGET_ROOT/var/lib/midnight/registry" \
  "$TARGET_ROOT/var/lib/midnight/acme"
printf '%s\n' 'control-plane-config-v1' >"$TARGET_ROOT/etc/midnight/midnight.env"
printf '%s\n' 'sha256:registry-proof' >"$TARGET_ROOT/var/lib/midnight/registry/digest"
printf '%s\n' '{"account":"acme-proof"}' >"$TARGET_ROOT/var/lib/midnight/acme/acme.json"

docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  psql -v ON_ERROR_STOP=1 -U postgres -d midnight -c \
  "CREATE TABLE recovery_probe (id integer PRIMARY KEY, value text NOT NULL); INSERT INTO recovery_probe VALUES (1, 'database-proof-v1');" \
  >/dev/null

(cd "$ROOT_DIR" && go build -trimpath -buildvcs=false -o "$RECOVERY_BIN" ./cmd/midnight-recovery)
export POSTGRES_CONTAINER="$CONTAINER"
export PATH="$WRAPPER_DIR:$PATH"

"$RECOVERY_BIN" create \
  --id "$BACKUP_ID" \
  --destination "file://$EVIDENCE_DIR/objects" \
  --dsn-file "$DSN_FILE" \
  --passphrase-file "$PASSPHRASE_FILE" \
  --root "$TARGET_ROOT/etc/midnight=etc/midnight" \
  --root "$TARGET_ROOT/var/lib/midnight/registry=var/lib/midnight/registry" \
  --root "$TARGET_ROOT/var/lib/midnight/acme=var/lib/midnight/acme" \
  --volume-backup-id vbkp_restore_acceptance \
  --offline-confirmed \
  >"$EVIDENCE_DIR/create.json"

docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  dropdb -U postgres midnight
docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  createdb -U postgres midnight
printf '%s\n' 'destroyed-config' >"$TARGET_ROOT/etc/midnight/midnight.env"
printf '%s\n' 'destroyed-registry' >"$TARGET_ROOT/var/lib/midnight/registry/digest"
printf '%s\n' '{"destroyed":true}' >"$TARGET_ROOT/var/lib/midnight/acme/acme.json"

"$RECOVERY_BIN" stage \
  --id "$BACKUP_ID" \
  --destination "file://$EVIDENCE_DIR/objects" \
  --passphrase-file "$PASSPHRASE_FILE" \
  --staging-dir "$STAGING_DIR" \
  >"$EVIDENCE_DIR/stage.json"

"$RECOVERY_BIN" apply \
  --stage-file "$STAGING_DIR/stage.json" \
  --target-root "$TARGET_ROOT" \
  --dsn-file "$DSN_FILE" \
  --offline-confirmed \
  --confirm-restore "$BACKUP_ID" \
  >"$EVIDENCE_DIR/apply.json"

[[ "$(cat "$TARGET_ROOT/etc/midnight/midnight.env")" == "control-plane-config-v1" ]]
[[ "$(cat "$TARGET_ROOT/var/lib/midnight/registry/digest")" == "sha256:registry-proof" ]]
[[ "$(cat "$TARGET_ROOT/var/lib/midnight/acme/acme.json")" == '{"account":"acme-proof"}' ]]
[[ "$(cat "$TARGET_ROOT/etc/midnight.midnight-retained-$BACKUP_ID/midnight.env")" == "destroyed-config" ]]

database_value="$(
  docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
    psql -At -U postgres -d midnight -c 'SELECT value FROM recovery_probe WHERE id=1'
)"
[[ "$database_value" == "database-proof-v1" ]]

image_id="$(docker inspect "$CONTAINER" --format '{{.Image}}')"
index_sha="$(
  python3 - "$EVIDENCE_DIR/objects/platform/$BACKUP_ID/index.json" <<'PY'
import hashlib
import pathlib
import sys
print(hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest())
PY
)"
python3 - "$EVIDENCE_DIR/evidence.json" "$BACKUP_ID" "$image_id" "$index_sha" <<'PY'
import json
import pathlib
import sys
pathlib.Path(sys.argv[1]).write_text(json.dumps({
    "schema": "midnight.parity.restore.v1",
    "status": "passed",
    "backup_id": sys.argv[2],
    "postgres_image_id": sys.argv[3],
    "index_sha256": sys.argv[4],
    "database_destroyed_and_restored": True,
    "filesystem_destroyed_and_restored": True,
    "previous_filesystem_retained": True,
}, indent=2) + "\n")
PY

echo "restore acceptance: PASS"
echo "  backup=$BACKUP_ID"
echo "  PostgreSQL and platform filesystem were destroyed and restored"
echo "  evidence=$EVIDENCE_DIR"
