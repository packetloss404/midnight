# Integration Tests

Run `bash scripts/ci/integration-gate.sh` with Docker, Go, and jq installed.
The gate builds a MinIO S3 fixture from an exact upstream source revision,
runs the integration-tagged suite, and rejects skips for required durable
journeys. Optional provider checks retain their own opt-ins and skip ledger.

To run only the S3 and sealed-credential boundary tests:

```sh
docker build -t midnight-test-minio:7aac2a2c5b7c test/integration/minio
go test -tags=integration ./internal/backup -count=1
```

MinIO's former Docker Hub and Quay images are unavailable. The fixture uses
[upstream source](https://github.com/minio/minio/tree/7aac2a2c5b7c882e68c1ce017d8256be2feea27f)
and is for disposable test storage only. An S3 round trip is not proof of an
off-host database backup, destructive restore, or application readback.
