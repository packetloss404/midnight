# Build Proof Fixtures (Railpack/Dockerfile → BuildKit → Registry)

The Node example exercises the full source-build pipeline end-to-end. The
adjacent Python, Go, explicit-Dockerfile, and build-secret fixtures exercise
the production toolchain loop. Together they verify that:

1. The deploy intake accepts a `{source: {repo, ref, subpath?}}` body.
2. The build worker (`midnight-buildkit worker`) drains the queued `build`
   job, runs Railpack against `repo/`, hands the saved plan to the
   digest-pinned BuildKit gateway frontend, and pushes the result to the local
   distribution registry.
3. The build worker enqueues a private follow-up `deploy` job pinned to that
   image digest. This fixture makes no public-route claim: generated and custom
   hostnames must already be owned through the product domain lifecycle before
   broad operator/automation deploy intake may select them.

## Layout

- `repo/` — the source tree the build worker clones. It contains a single
  `server.js` plus `package.json` so Railpack's Node provider takes over.
  There is intentionally **no** Dockerfile so `RailpackBuilder` is selected.
- `python/` and `go/` — language-provider fixtures used by the disposable
  toolchain loop.
- `dockerfile/` — the explicit Dockerfile frontend fixture.
- `secret/` — requires a BuildKit secret during its Railpack build,
  deliberately echoes it, and emits only a safe proof marker. The acceptance
  loop asserts the raw value reaches no persisted evidence.
- `run.sh` — POSIX shell script that POSTs the deploy intent and polls the
  resulting build operation until the digest-pinned deploy handoff completes
  (or times out). Requires `curl` + `jq`.

The repo directory is also the canonical local-clone source for tests that
set `MIDNIGHT_BUILDKIT_SKIP_CLONE=1` (see `internal/build/source_checkout.go`
`SkipClone`).

## Prerequisites

The script assumes the following are already running and reachable:

- Midnight server: `MIDNIGHT_API_URL` (default `http://127.0.0.1:8080`).
- BuildKit + registry + build worker + deploy worker — see
  [`docs/build-pipeline.md`](../../docs/build-pipeline.md) for setup.

The repo directory is exposed to the build worker via a local file URL so the
script does not require pushing the example to a remote git host. The worker
treats `file://` and absolute paths under its checkout root as local
checkouts and skips the git clone.

## Running the proof

```sh
cd examples/build-proof
./run.sh
```

A successful run prints:

```
intake: operation=op_… kind=build queue=build job_id=…
build job: succeeded image=registry.midnight.local:5000/midnight/…@sha256:…
deploy handoff: queued digest-pinned image=registry.midnight.local:5000/midnight/…@sha256:…
PASS
```

## Failure modes

| Symptom                                  | What to check                                                                 |
|------------------------------------------|-------------------------------------------------------------------------------|
| `intake: HTTP 400 ... DEPLOY_SOURCE_REF_UNSUPPORTED` | The server build is stale — rebuild and restart `midnight-server`. |
| `build job: failed source checkout`      | The worker can't reach the repo. Confirm `MIDNIGHT_BUILDPROOF_REPO`.          |
| `build job: failed build solve`          | Railpack or BuildKit fault. Inspect `<output-root>/<svc>-<op>/buildctl.stderr.log`. |

To prove public routing, first create a generated domain or claim and verify a
custom domain through the service domain APIs, then run a deployment selector
against that existing binding and verify its publication evidence separately.
