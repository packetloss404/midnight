# Integration Tests

Run `bash scripts/ci/integration-gate.sh` with Docker, Go, and jq installed.
The gate builds a MinIO S3 fixture from an exact upstream source revision,
runs the integration-tagged suite, and rejects skips for required durable
journeys. Optional provider checks retain their own opt-ins and skip ledger.

The separate Linux runtime gate exercises actual BuildKit/registry and
Traefik processes:

```sh
bash scripts/ci/live-runtime-gate.sh
```

It requires privileged Docker and host networking. The mandatory assertions
are a real minimal-image build/push with repeatable digest and evidence,
Traefik HTTP 200 with a backend marker, observed replacement with a new
marker, and unknown-host 404. The built image and HTTP test servers are
separate fixtures; this does not claim a full Midnight source-to-HTTP rollout.

Durable browser acceptance uses a disposable PostgreSQL API and Chromium:

```sh
cd web-next
npx playwright test --config playwright.postgres.config.ts
```

This lane exercises real first-administrator/TOTP setup, human sessions,
variable/runtime changesets, stale writes, and logout revocation. See the
[browser README](../../web-next/README.md) for its test-environment contract.
These commands describe executable coverage mechanisms; check the retained
job results before claiming the current candidate passes.

To run only the S3 and sealed-credential boundary tests:

```sh
docker build -t midnight-test-minio:7aac2a2c5b7c test/integration/minio
go test -tags=integration ./internal/backup -count=1
```

MinIO's former Docker Hub and Quay images are unavailable. The fixture uses
[upstream source](https://github.com/minio/minio/tree/7aac2a2c5b7c882e68c1ce017d8256be2feea27f)
and is for disposable test storage only. An S3 round trip is not proof of an
off-host database backup, destructive restore, or application readback.
