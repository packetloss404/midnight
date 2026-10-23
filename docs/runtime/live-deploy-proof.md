# Live Deploy Check

Status: procedure ready; fresh-install and source-to-HTTP observations recorded,
with full candidate acceptance still pending (reconciled 2026-09-21).

This runbook proves that one operator can take a real single-VM Midnight install through deploy intake, durable queue execution, Docker-backed runtime start, Traefik file-provider promotion, and a public routed response with support-safe evidence.

This is a live proof, not a local release gate. The local gate remains the prerequisite contract check. The live deploy check starts only after that gate is green and then exercises the parts that local drills intentionally do not touch: Docker Engine, worker process wiring, Traefik filesystem publication, Traefik reload, DNS reachability, and the public route.

The September 5 reports record a fresh schema-71 installation and a real source
deployment served through Traefik over HTTP (`MID-116` and `MID-117`). The older
alpha-6/schema-70 upgrade is historical preflight evidence. These observations
do not identify today's installed package or complete external HTTPS,
restart/rollback, and recovery acceptance. Record the candidate's actual
identity and run the applicable assertions below; see
[deployment readiness](../deployment-readiness.md) for the evidence ledger.

## Pass Criteria

The run passes only when all of these are true:

1. The repo-local release check passed before the VM proof run.
2. `midnight-server` is serving with Postgres storage, not memory storage.
3. `midnight-worker` is serving with `runtime=docker` and `route_configured=true`.
4. The deploy API returns `202 Accepted` for one service deployment request with a fresh `Idempotency-Key`.
5. The operation event stream shows `queued`, the worker transition to `RUNNING`, successful deploy step messages through `promote`, and the worker transition to `SUCCEEDED`.
6. Docker shows a running candidate container for the deployment or instance id from the request.
7. Traefik dynamic config for the configured route group exists as a flat `<route-group>[.<route>].traefik.dynamic.yaml` file in the dynamic directory.
8. A public HTTP or HTTPS request to the configured host returns the candidate application response.
9. Evidence is captured under a workspace-relative `artifacts/mvp/live-deploy/<run-id>/` directory with secrets redacted.

## Prerequisites

- One disposable Linux VM that matches the single-VM MVP target profile.
- The [operator install path](../install/operator-install-path.md) has passed on that VM.
- Docker Engine reachable by the Midnight worker and compatible with the doctor `OR-CHK-DOC-003` Docker API floor.
- Cgroup v2, ingress ports, firewall, MAC, clock, hostname, Docker package, Postgres DSN, cursor key, registry config, and external runtime binary readiness reviewed with `midnight-doctor --json --live`.
- A Postgres database reachable from `midnight-server` and `midnight-worker`.
- `midnight-server`, `midnight-worker`, Docker, and Traefik installed on the VM.
- Operator shell tools available on the VM: `curl`, `jq`, `openssl`, `git`, and `make`.
- Traefik static config has `providers.file.directory` pointed at the same directory passed to `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR`.
- A route publication root exists, is absolute, and is writable by the `midnight` service user, normally `/etc/midnight/traefik/dynamic`.
- The public DNS name used for the proof resolves to the VM. HTTPS proof additionally requires a usable wildcard/generated-domain certificate path and resolver configuration.
- A small candidate image is already pushed to a registry reachable by Docker on the VM. Use an immutable digest reference such as `registry.example/web@sha256:<digest>`.
- The candidate app listens on the declared target port, default `8080`, or carries one of the supported runtime labels: `midnight.internal_port`, `midnight.target_port`, or `midnight.port`.

## Failure Boundaries

Stop the proof and record `blocked` evidence when any boundary below is hit:

- `make release-check` or the canonical `tools/phase0-gate` strict command fails.
- `midnight-doctor --json --live` reports fatal failures for host or package-boot checks.
- The server starts with `storage=memory`.
- The worker starts with `runtime=local`, an empty route config directory, or `route_configured=false`.
- The deploy request omits an immutable image digest.
- The deploy operation reaches `FAILED`, the queue heartbeat becomes stuck, or required step telemetry is missing.
- The readiness/routing gate fails before `promote`; do not manually write Traefik config to force the run forward.
- Traefik does not observe the generated file-provider artifact.
- Public DNS, firewall, certificate, or route response checks fail.
- Evidence would expose secrets, raw DSNs, provider credentials, Docker socket paths, rendered Traefik YAML, or host-absolute paths.

## Operator Steps

Run commands from the repository root unless a step says otherwise. Keep evidence paths workspace-relative.

1. Create an evidence directory and pin proof variables:

   ```sh
   export LIVE_RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
   export LIVE_EVIDENCE_DIR="artifacts/mvp/live-deploy/${LIVE_RUN_ID}"
   export MIDNIGHT_BASE_URL="http://127.0.0.1:8080"
   export LIVE_PROJECT_SLUG="live-deploy"
   export LIVE_SERVICE_SLUG="web"
   export LIVE_ENVIRONMENT_ID="env_live"
   export LIVE_DEPLOYMENT_ID="dep_live_${LIVE_RUN_ID}"
   export LIVE_INSTANCE_ID="inst_live_${LIVE_RUN_ID}_0"
   export LIVE_ROUTE_GROUP="live-deploy"
   export LIVE_PUBLIC_HOST="app.example.test"
   export LIVE_PUBLIC_PATH="/"
   export LIVE_ROUTE_TARGET_PORT="8080"
   export LIVE_IMAGE="registry.example/web@sha256:<digest>"
   mkdir -p "${LIVE_EVIDENCE_DIR}"
   ```

2. Record immutable run context:

   ```sh
   git rev-parse HEAD | tee "${LIVE_EVIDENCE_DIR}/commit.txt"
   git status --short | tee "${LIVE_EVIDENCE_DIR}/git-status.txt"
   docker version --format '{{json .Server}}' | tee "${LIVE_EVIDENCE_DIR}/docker-server.json"
   ```

3. Run the local release gate before live proof:

   ```sh
   make release-check 2>&1 | tee "${LIVE_EVIDENCE_DIR}/release-check.log"
   ```

   Expected evidence: exit code `0`, plus release evidence files under the configured artifact directory. If this fails, stop; the live deploy check cannot override the local gate.

4. Capture host/package readiness:

   ```sh
   sudo midnight-doctor --json --live | tee "${LIVE_EVIDENCE_DIR}/doctor-live.json"
   ```

   Expected evidence: no fatal findings. Warnings require operator notes in `operator-notes.md`.

5. Configure the worker route environment in `/etc/midnight/midnight.env` or the equivalent systemd drop-in:

   ```sh
   MIDNIGHT_WORKER_RUNTIME_BACKEND=docker
   MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=/etc/midnight/traefik/dynamic
   MIDNIGHT_WORKER_ROUTE_GROUP=live-deploy
   MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST=app.example.test
   MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH=/
   MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=false
   MIDNIGHT_WORKER_ROUTE_TARGET_PORT=8080
   ```

   If proving HTTPS, also set `MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=true` and the matching `MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER`; set the deploy request `route.tls` field to `true` in step 8.

6. Restart services and capture status:

   ```sh
   sudo systemctl restart midnight-server midnight-worker traefik
   sudo systemctl --no-pager --full status midnight-server midnight-worker traefik \
     | tee "${LIVE_EVIDENCE_DIR}/systemd-status.txt"
   journalctl -u midnight-server -u midnight-worker -u traefik --since "10 minutes ago" --no-pager \
     | tee "${LIVE_EVIDENCE_DIR}/service-startup.log"
   ```

   Expected evidence: server startup includes Postgres storage, worker startup includes `runtime=docker route_configured=true`, and Traefik starts without file-provider errors.

7. Create the proof project and service:

   ```sh
   cat >"${LIVE_EVIDENCE_DIR}/midnight-live-deploy.json" <<EOF
   {
     "api_version": "midnight/v1alpha",
     "projects": [{"slug": "${LIVE_PROJECT_SLUG}", "display_name": "Live Deploy"}],
     "services": [{"project_slug": "${LIVE_PROJECT_SLUG}", "slug": "${LIVE_SERVICE_SLUG}", "display_name": "Live Deploy Web"}]
   }
   EOF

   midnight config apply \
     --file "${LIVE_EVIDENCE_DIR}/midnight-live-deploy.json" \
     --endpoint "${MIDNIGHT_BASE_URL}" \
     --idempotency-key "live-config-${LIVE_RUN_ID}" \
     | tee "${LIVE_EVIDENCE_DIR}/config-apply.json"
   curl -fsS "${MIDNIGHT_BASE_URL}/api/v1alpha/services" \
     | tee "${LIVE_EVIDENCE_DIR}/services.json"
   ```

   Expected evidence: config apply succeeds and `services.json` contains the service id used in the next step.

8. Submit the deploy intake request:

   ```sh
   export LIVE_SERVICE_ID="$(jq -r --arg slug "${LIVE_SERVICE_SLUG}" '.data[] | select(.slug == $slug) | .id' "${LIVE_EVIDENCE_DIR}/services.json")"

   cat >"${LIVE_EVIDENCE_DIR}/deploy-request.json" <<EOF
   {
     "environment_id": "${LIVE_ENVIRONMENT_ID}",
     "image": "${LIVE_IMAGE}",
     "deployment_id": "${LIVE_DEPLOYMENT_ID}",
     "instance_id": "${LIVE_INSTANCE_ID}",
     "route": {
       "name": "${LIVE_SERVICE_SLUG}",
       "group": "${LIVE_ROUTE_GROUP}",
       "host": "${LIVE_PUBLIC_HOST}",
       "path": "${LIVE_PUBLIC_PATH}",
       "public": true,
       "tls": false
     }
   }
   EOF

   curl -fsS -D "${LIVE_EVIDENCE_DIR}/deploy-response.headers" \
     -H "Content-Type: application/json" \
     -H "Idempotency-Key: live-deploy-${LIVE_RUN_ID}" \
     --data @"${LIVE_EVIDENCE_DIR}/deploy-request.json" \
     "${MIDNIGHT_BASE_URL}/api/v1alpha/services/${LIVE_SERVICE_ID}/deployments" \
     | tee "${LIVE_EVIDENCE_DIR}/deploy-response.json"
   ```

   Expected evidence: HTTP status `202`, operation kind `deploy`, and initial state `QUEUED` or `queued`.

9. Watch the operation until terminal success:

   ```sh
   export LIVE_OPERATION_ID="$(jq -r '.id // .operation.id' "${LIVE_EVIDENCE_DIR}/deploy-response.json")"

   for i in $(seq 1 60); do
     curl -fsS "${MIDNIGHT_BASE_URL}/api/v1alpha/operations/${LIVE_OPERATION_ID}/events" \
       | tee "${LIVE_EVIDENCE_DIR}/operation-events-${i}.sse"
     if grep -q "deploy worker transitioned operation to SUCCEEDED" "${LIVE_EVIDENCE_DIR}/operation-events-${i}.sse"; then
       break
     fi
     if grep -Eq "deploy worker failed operation|deploy worker step .* failed" "${LIVE_EVIDENCE_DIR}/operation-events-${i}.sse"; then
       exit 1
     fi
     sleep 2
   done
   ```

   Expected evidence: events include `pull-verify`, `schedule`, `start`, `readiness-routing-check`, `promote`, and `succeeded` with successful states. The route must not be checked before `promote` succeeds.

10. Capture runtime and route-publication evidence:

    ```sh
    docker ps --filter "label=midnight.deployment_id=${LIVE_DEPLOYMENT_ID}" --format '{{json .}}' \
      | tee "${LIVE_EVIDENCE_DIR}/docker-ps.jsonl"
    sudo sh -c 'ls -1 /etc/midnight/traefik/dynamic/'"${LIVE_ROUTE_GROUP}"'*.traefik.dynamic.yaml' \
      | tee "${LIVE_EVIDENCE_DIR}/traefik-dynamic-artifacts.txt"
    sudo sh -c 'stat /etc/midnight/traefik/dynamic/'"${LIVE_ROUTE_GROUP}"'*.traefik.dynamic.yaml' \
      | tee "${LIVE_EVIDENCE_DIR}/traefik-dynamic-stat.txt"
    journalctl -u traefik --since "10 minutes ago" --no-pager \
      | tee "${LIVE_EVIDENCE_DIR}/traefik-after-deploy.log"
    ```

    Expected evidence: one running candidate container, a present dynamic route artifact, and no Traefik reload errors. Do not copy rendered route YAML into shareable evidence.

11. Verify the public route:

    ```sh
    curl -fsS -D "${LIVE_EVIDENCE_DIR}/route-response.headers" \
      "http://${LIVE_PUBLIC_HOST}${LIVE_PUBLIC_PATH}" \
      | tee "${LIVE_EVIDENCE_DIR}/route-response.body"
    ```

    For HTTPS proof, use `https://` and add certificate metadata without private key material:

    ```sh
    echo | openssl s_client -servername "${LIVE_PUBLIC_HOST}" -connect "${LIVE_PUBLIC_HOST}:443" 2>/dev/null \
      | openssl x509 -noout -subject -issuer -dates \
      | tee "${LIVE_EVIDENCE_DIR}/certificate-summary.txt"
    ```

    Expected evidence: HTTP status `2xx` or the exact app-level success status agreed for the proof, plus body content that identifies the candidate app without containing secrets.

12. Write the operator note and evidence checklist:

    ```sh
    cat >"${LIVE_EVIDENCE_DIR}/operator-notes.md" <<EOF
    # Live Deploy Operator Notes

    - Run id: ${LIVE_RUN_ID}
    - Commit: $(cat "${LIVE_EVIDENCE_DIR}/commit.txt")
    - Public host: ${LIVE_PUBLIC_HOST}
    - Image: ${LIVE_IMAGE}
    - Result: pass | blocked | failed
    - Notes:
    EOF
    ```

## Expected Evidence Checklist

- `commit.txt`
- `git-status.txt`
- `docker-server.json`
- `release-check.log`
- `doctor-live.json`
- `systemd-status.txt`
- `service-startup.log`
- `midnight-live-deploy.json`
- `config-apply.json`
- `services.json`
- `deploy-request.json`
- `deploy-response.headers`
- `deploy-response.json`
- `operation-events-<n>.sse`
- `docker-ps.jsonl`
- `traefik-dynamic-stat.txt`
- `traefik-after-deploy.log`
- `route-response.headers`
- `route-response.body`
- `certificate-summary.txt` when HTTPS is in scope
- `operator-notes.md`

## CLI source-build extension

The numbered procedure above proves digest-pinned API/image intake. Run this
extension when claiming that the developer CLI and source build path are ready.
Use a disposable service and a local application that has either a reviewed
Dockerfile or a supported Railpack-detectable shape. The repository's
`examples/deploy-proof` sample is the default candidate.

1. Authenticate with a normal human session and link the disposable target:

   ```sh
   midnight login --api-base "${MIDNIGHT_BASE_URL}" --email operator@example.test
   (
     cd examples/deploy-proof
     midnight link \
       --project "${LIVE_PROJECT_SLUG}" \
       --environment "${LIVE_ENVIRONMENT_ID}" \
       --service "${LIVE_SERVICE_SLUG}"
   )
   ```

2. From the candidate application's root, submit and stream the deployment:

   ```sh
   (cd examples/deploy-proof && \
     midnight up --message "live source proof ${LIVE_RUN_ID}") 2>&1 \
     | tee "${LIVE_EVIDENCE_DIR}/cli-up.log"
   ```

3. Use the operation ID printed by `up` to retain tenant-scoped status, events,
   and logs:

   ```sh
   midnight deploy status --operation-id "${LIVE_OPERATION_ID}" --json \
     | tee "${LIVE_EVIDENCE_DIR}/cli-operation.json"
   midnight deploy events --operation-id "${LIVE_OPERATION_ID}" \
     | tee "${LIVE_EVIDENCE_DIR}/cli-operation-events.sse"
   midnight deploy logs --operation-id "${LIVE_OPERATION_ID}" --json \
     | tee "${LIVE_EVIDENCE_DIR}/cli-operation-logs.json"
   ```

The extension passes only when the evidence shows content-addressed upload,
an actual BuildKit solve, a digest-pinned registry result, Docker runtime start,
health/promotion success, and terminal operation success. Scrub credentials,
DSNs, source URLs containing credentials, application secrets, and host paths
before retaining or sharing the evidence.

## Known Non-Goals

- Proving the default local runtime adapter, `deploy.NoopRoutePromoter`, or in-memory router paths.
- Proving multi-host scheduling, failover, or HA control-plane behavior.
- The digest-pinned API/image procedure does not by itself prove source
  checkout, BuildKit, SBOM/provenance, registry cache, or image signing. Run
  the CLI source-build extension when those claims are in scope.
- Proving rollback, previous-instance drain, or destructive cleanup unless a second deployment is explicitly added to the run.
- Proving production ACME issuance or DNS provider automation when the run is declared HTTP-only or uses a pre-existing wildcard certificate.
- Proving load, soak, performance, abuse, tenant isolation, or security scanning.
- Capturing rendered Traefik config, raw DSNs, credentials, tokens, private keys, Docker socket paths, or host-absolute paths in shareable release evidence.
