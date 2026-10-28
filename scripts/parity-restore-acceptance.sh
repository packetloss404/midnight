#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Disposable destructive platform-recovery proof. This uses a real PostgreSQL
# 16 server plus pg_dump/pg_restore from that same image, destroys the source
# database and filesystem state, then restores through midnight-recovery.

set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POSTGRES_IMAGE="${MIDNIGHT_PARITY_POSTGRES_IMAGE:-postgres:16-alpine}"
EVIDENCE_ROOT="${MIDNIGHT_PARITY_EVIDENCE_DIR:-$ROOT_DIR/.midnight/parity-evidence}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-$RANDOM"
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
DESTINATION="${MIDNIGHT_PARITY_BACKUP_DESTINATION:-file://$EVIDENCE_DIR/objects}"
container_owned=false
container_id=""

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

DESTINATION_KIND="$(MIDNIGHT_RESTORE_DESTINATION="$DESTINATION" python3 - <<'PY'
import os, pathlib, re, urllib.parse
try:
    uri = urllib.parse.urlsplit(os.environ['MIDNIGHT_RESTORE_DESTINATION'])
    if uri.scheme not in ('file', 's3') or uri.username or uri.password or uri.fragment:
        raise ValueError()
    if uri.scheme == 'file':
        if uri.netloc or uri.query or not pathlib.Path(uri.path).is_absolute():
            raise ValueError()
    else:
        if not uri.netloc or not uri.path.strip('/'):
            raise ValueError()
        pairs = urllib.parse.parse_qsl(uri.query, keep_blank_values=True)
        if len(dict(pairs)) != len(pairs):
            raise ValueError()
        for key, value in pairs:
            if key == 'endpoint':
                endpoint = urllib.parse.urlsplit('https://' + value)
                if not endpoint.hostname or endpoint.username or endpoint.password or endpoint.path or endpoint.query or endpoint.fragment:
                    raise ValueError()
            elif key == 'region':
                if not re.fullmatch(r'[A-Za-z0-9-]{1,64}', value):
                    raise ValueError()
            elif key == 'scheme':
                if value not in ('http', 'https'):
                    raise ValueError()
            elif key == 'style':
                if value not in ('path', 'virtual'):
                    raise ValueError()
            else:
                raise ValueError()
except (KeyError, ValueError):
    raise SystemExit('restore acceptance: destination must be an absolute file URI or credential-free scoped S3 URI')
print(uri.scheme)
PY
)"

mkdir -p "$EVIDENCE_DIR" "$WRAPPER_DIR"
chmod 0700 "$EVIDENCE_DIR" "$WORK_DIR" "$WRAPPER_DIR"

cleanup_runtime() {
  if "$container_owned"; then
    local remaining
    remaining=$(docker ps -aq --no-trunc --filter "id=$container_id") || return 1
    if [[ -n "$remaining" ]]; then
      docker rm -f "$container_id" >/dev/null || return 1
    fi
    remaining=$(docker ps -aq --no-trunc --filter "id=$container_id") || return 1
    [[ -z "$remaining" ]] || return 1
    container_owned=false
  fi
}
cleanup_work() {
  if [[ -d "$WORK_DIR" ]]; then
    [[ "$WORK_DIR" == "${TMPDIR:-/tmp}"/midnight-restore-acceptance.* ]] || return 1
    rm -rf -- "$WORK_DIR" || return 1
    [[ ! -e "$WORK_DIR" ]] || return 1
  fi
}
on_exit() {
  local restore_exit_status=$?
  trap - EXIT
  cleanup_runtime >/dev/null 2>&1 || true
  cleanup_work >/dev/null 2>&1 || true
  exit "$restore_exit_status"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

container_id=$(docker run -d --rm \
  --name "$CONTAINER" \
  -e POSTGRES_PASSWORD=midnight_restore_test \
  -e POSTGRES_DB=midnight \
  "$POSTGRES_IMAGE")
[[ "$container_id" =~ ^[a-f0-9]{64}$ ]] || exit 1
container_owned=true

for attempt in $(seq 1 60); do
  # The image's initialization server accepts Unix sockets, then shuts down.
  # TCP readiness identifies the final server instead of racing that shutdown.
  if docker exec "$CONTAINER" pg_isready -h 127.0.0.1 -U postgres -d midnight >/dev/null 2>&1; then
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
python3 - "$PASSPHRASE_FILE" "$WORK_DIR/wrong-passphrase" <<'PY'
import pathlib, secrets, sys
for path in sys.argv[1:]:
    pathlib.Path(path).write_text(secrets.token_urlsafe(48) + '\n')
    pathlib.Path(path).chmod(0o600)
PY
chmod 0600 "$DSN_FILE" "$PASSPHRASE_FILE"

mkdir -p \
  "$TARGET_ROOT/etc/midnight" \
  "$TARGET_ROOT/var/lib/midnight/registry" \
  "$TARGET_ROOT/var/lib/midnight/acme" \
  "$TARGET_ROOT/var/lib/midnight/audit"
printf '%s\n' 'control-plane-config-v1' >"$TARGET_ROOT/etc/midnight/midnight.env"
printf '%s\n' 'sha256:registry-proof' >"$TARGET_ROOT/var/lib/midnight/registry/digest"
printf '%s\n' '{"account":"acme-proof"}' >"$TARGET_ROOT/var/lib/midnight/acme/acme.json"
# Representative secret bytes exercise custody and mode preservation. This
# fixture is not an Ed25519 signing key or a signature-verification proof.
python3 - "$TARGET_ROOT/var/lib/midnight/audit" "$WORK_DIR/audit-key-original" <<'PY'
import pathlib, secrets, sys
root = pathlib.Path(sys.argv[1])
root.chmod(0o700)
payload = ('private-key-fixture:' + secrets.token_urlsafe(48) + '\n').encode()
for target in (root / 'signing-key.pem', pathlib.Path(sys.argv[2])):
    target.write_bytes(payload)
    target.chmod(0o600)
PY

docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  psql -v ON_ERROR_STOP=1 -U postgres -d midnight -c \
  "CREATE TABLE recovery_probe (id integer PRIMARY KEY, value text NOT NULL); INSERT INTO recovery_probe VALUES (1, 'database-proof-v1');" \
  >/dev/null

(cd "$ROOT_DIR" && go build -trimpath -buildvcs=false -o "$RECOVERY_BIN" ./cmd/midnight-recovery)
export POSTGRES_CONTAINER="$CONTAINER"
export PATH="$WRAPPER_DIR:$PATH"

"$RECOVERY_BIN" create \
  --id "$BACKUP_ID" \
  --destination "$DESTINATION" \
  --dsn-file "$DSN_FILE" \
  --passphrase-file "$PASSPHRASE_FILE" \
  --root "$TARGET_ROOT/etc/midnight=etc/midnight" \
  --root "$TARGET_ROOT/var/lib/midnight/registry=var/lib/midnight/registry" \
  --root "$TARGET_ROOT/var/lib/midnight/acme=var/lib/midnight/acme" \
  --root "$TARGET_ROOT/var/lib/midnight/audit=var/lib/midnight/audit" \
  --volume-backup-id vbkp_restore_acceptance \
  --offline-confirmed \
  >"$EVIDENCE_DIR/create.json"

if "$RECOVERY_BIN" stage --id "$BACKUP_ID" --destination "$DESTINATION" \
  --passphrase-file "$WORK_DIR/wrong-passphrase" --staging-dir "$WORK_DIR/rejected-stage" \
  >"$EVIDENCE_DIR/wrong-passphrase.json" 2>"$EVIDENCE_DIR/wrong-passphrase.stderr"; then
  echo 'restore acceptance: incorrect passphrase was accepted' >&2
  exit 1
fi
[[ "$(cat "$TARGET_ROOT/etc/midnight/midnight.env")" == "control-plane-config-v1" ]]

docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  dropdb -U postgres midnight
docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
  createdb -U postgres midnight
printf '%s\n' 'destroyed-config' >"$TARGET_ROOT/etc/midnight/midnight.env"
printf '%s\n' 'destroyed-registry' >"$TARGET_ROOT/var/lib/midnight/registry/digest"
printf '%s\n' '{"destroyed":true}' >"$TARGET_ROOT/var/lib/midnight/acme/acme.json"
printf '%s\n' 'destroyed-key-fixture' >"$TARGET_ROOT/var/lib/midnight/audit/signing-key.pem"

"$RECOVERY_BIN" stage \
  --id "$BACKUP_ID" \
  --destination "$DESTINATION" \
  --passphrase-file "$PASSPHRASE_FILE" \
  --staging-dir "$STAGING_DIR" \
  >"$EVIDENCE_DIR/stage.json"

if "$RECOVERY_BIN" apply --stage-file "$STAGING_DIR/stage.json" \
  --target-root "$TARGET_ROOT" --dsn-file "$DSN_FILE" --offline-confirmed \
  --confirm-restore "incorrect-$BACKUP_ID" \
  >"$EVIDENCE_DIR/rejected-confirmation.json" 2>"$EVIDENCE_DIR/rejected-confirmation.stderr"; then
  echo 'restore acceptance: incorrect destructive confirmation was accepted' >&2
  exit 1
fi

# Corrupt the authenticated plaintext candidate, not an unrelated destination
# object. Apply must reject it before touching the destroyed fixture state.
python3 - "$STAGING_DIR" "$WORK_DIR/dump-original" <<'PY'
import json, pathlib, shutil, sys
stage = pathlib.Path(sys.argv[1]).resolve()
dump = pathlib.Path(json.loads((stage / 'stage.json').read_text())['postgres_dump']).resolve()
if stage not in dump.parents:
    raise SystemExit('restore acceptance: staged dump escaped the owned directory')
shutil.copyfile(dump, sys.argv[2])
with dump.open('ab') as target:
    target.write(b'acceptance-tamper')
PY
if "$RECOVERY_BIN" apply --stage-file "$STAGING_DIR/stage.json" \
  --target-root "$TARGET_ROOT" --dsn-file "$DSN_FILE" --offline-confirmed \
  --confirm-restore "$BACKUP_ID" \
  >"$EVIDENCE_DIR/rejected-tamper.json" 2>"$EVIDENCE_DIR/rejected-tamper.stderr"; then
  echo 'restore acceptance: tampered staged bytes were accepted' >&2
  exit 1
fi
[[ "$(cat "$TARGET_ROOT/etc/midnight/midnight.env")" == "destroyed-config" ]]
[[ "$(docker exec "$CONTAINER" psql -At -U postgres -d midnight -c "SELECT to_regclass('public.recovery_probe') IS NULL")" == "t" ]]
python3 - "$STAGING_DIR/stage.json" "$WORK_DIR/dump-original" <<'PY'
import json, pathlib, shutil, sys
dump = pathlib.Path(json.loads(pathlib.Path(sys.argv[1]).read_text())['postgres_dump'])
shutil.copyfile(sys.argv[2], dump)
PY

"$RECOVERY_BIN" apply \
  --stage-file "$STAGING_DIR/stage.json" \
  --target-root "$TARGET_ROOT" \
  --dsn-file "$DSN_FILE" \
  --offline-confirmed \
  --confirm-restore "$BACKUP_ID" \
  >"$EVIDENCE_DIR/apply.json"

"$RECOVERY_BIN" apply --stage-file "$STAGING_DIR/stage.json" \
  --target-root "$TARGET_ROOT" --dsn-file "$DSN_FILE" --offline-confirmed \
  --confirm-restore "$BACKUP_ID" >"$EVIDENCE_DIR/apply-replay.json"

[[ "$(cat "$TARGET_ROOT/etc/midnight/midnight.env")" == "control-plane-config-v1" ]]
[[ "$(cat "$TARGET_ROOT/var/lib/midnight/registry/digest")" == "sha256:registry-proof" ]]
[[ "$(cat "$TARGET_ROOT/var/lib/midnight/acme/acme.json")" == '{"account":"acme-proof"}' ]]
[[ "$(cat "$TARGET_ROOT/etc/midnight.midnight-retained-$BACKUP_ID/midnight.env")" == "destroyed-config" ]]
python3 - "$TARGET_ROOT/var/lib/midnight/audit" "$WORK_DIR/audit-key-original" <<'PY'
import pathlib, stat, sys
root = pathlib.Path(sys.argv[1])
key = root / 'signing-key.pem'
assert key.read_bytes() == pathlib.Path(sys.argv[2]).read_bytes(), 'private fixture bytes changed'
assert stat.S_IMODE(root.stat().st_mode) == 0o700, 'private directory mode changed'
assert stat.S_IMODE(key.stat().st_mode) == 0o600, 'private file mode changed'
PY

database_value="$(
  docker exec -e PGPASSWORD=midnight_restore_test "$CONTAINER" \
    psql -At -U postgres -d midnight -c 'SELECT value FROM recovery_probe WHERE id=1'
)"
[[ "$database_value" == "database-proof-v1" ]]

python3 - "$EVIDENCE_DIR" "$PASSPHRASE_FILE" "$WORK_DIR/audit-key-original" <<'PY'
import pathlib, sys
needles = [pathlib.Path(name).read_bytes().strip() for name in sys.argv[2:]]
for path in pathlib.Path(sys.argv[1]).rglob('*'):
    if path.is_file() and any(needle in path.read_bytes() for needle in needles):
        raise SystemExit('restore acceptance: secret fixture leaked into retained evidence')
PY

image_id="$(docker inspect "$CONTAINER" --format '{{.Image}}')"
index_sha="$(
  python3 - "$EVIDENCE_DIR/create.json" <<'PY'
import hashlib
import json
import pathlib
import sys
index = json.loads(pathlib.Path(sys.argv[1]).read_text())['index']
print(hashlib.sha256(json.dumps(index, sort_keys=True, separators=(',', ':')).encode()).hexdigest())
PY
)"
cleanup_runtime || { echo 'restore acceptance: runtime cleanup could not be verified' >&2; exit 1; }
cleanup_work || { echo 'restore acceptance: temporary state cleanup failed' >&2; exit 1; }
python3 - "$EVIDENCE_DIR/evidence.json" "$BACKUP_ID" "$image_id" "$index_sha" "$DESTINATION_KIND" <<'PY'
import json
import pathlib
import sys
pathlib.Path(sys.argv[1]).write_text(json.dumps({
    "schema": "midnight.parity.restore.v2",
    "status": "passed",
    "backup_id": sys.argv[2],
    "postgres_image_id": sys.argv[3],
    "index_receipt_sha256": sys.argv[4],
    "storage_kind": sys.argv[5],
    "whole_host_loss_recovery_proven": False,
    "cleanup_verified": True,
    "incorrect_passphrase_refused": True,
    "incorrect_confirmation_refused": True,
    "tampered_plaintext_refused_before_mutation": True,
    "completed_apply_replay_passed": True,
    "database_destroyed_and_restored": True,
    "filesystem_destroyed_and_restored": True,
    "protected_file_bytes_and_modes_restored": True,
    "previous_filesystem_retained": True,
}, indent=2) + "\n")
PY

echo "restore acceptance: PASS"
echo "  backup=$BACKUP_ID"
echo "  PostgreSQL and platform filesystem were destroyed and restored"
echo "  evidence=$EVIDENCE_DIR"
