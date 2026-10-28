# Agent source deployment

The chat tool `deploy_from_source` accepts a small inline UTF-8 source tree for an existing service and environment. It uses the same human source-upload coordinator, content-addressed artifact store, durable build outbox, leased build worker, immutable image handoff, and deployment promotion transaction as `midnight up`.

This input mode does not clone a repository. The model cannot select a host directory, a GitHub installation, a private repository credential, or an arbitrary fetch URL. GitHub linked-source deployments remain a separate product flow.

## Obtain the current target snapshot

Open a chat scoped to the intended project, service and environment. Call the read-only tool first:

```json
{
  "service_id": "svc_example",
  "environment_id": "env_example"
}
```

`get_source_deployment_context` returns these fields:

```json
{
  "service_id": "svc_example",
  "environment_id": "env_example",
  "expected_service_version": 1,
  "expected_environment_version": 1,
  "expected_config_version": 0
}
```

The versions above are examples. Copy the actual tool response; never guess versions. Config version zero means no saved config row exists. A changed target causes a version refusal: read the target again, assess the changes, and use a new request key for the revised logical deployment.

## Request one bounded build

The message must explicitly allow mutations. Project owners and editors can deploy within their current authority; viewers can read the target context but cannot deploy. Chat ownership, current session, membership, environment restrictions and the exact chat target are checked server-side. A previously captured tool handle loses authority when its membership or session is revoked.

For example, after obtaining the actual current versions:

```json
{
  "service_id": "svc_example",
  "environment_id": "env_example",
  "request_key": "hello-source-1",
  "expected_service_version": 1,
  "expected_environment_version": 1,
  "expected_config_version": 0,
  "files": {
    "Dockerfile": "FROM scratch\nCOPY greeting.txt /greeting.txt\n",
    "greeting.txt": "Hello from a bounded source tree.\n"
  }
}
```

This illustrative scratch image has no runnable application; supply an appropriate Dockerfile/application and health configuration for a deployment that can become ready. The normal Dockerfile/Railpack builder selection and saved service configuration still apply.

The accepted result identifies the canonical queued deployment. Acceptance does not mean the image built, the application passed readiness, or traffic was published. Follow `get_deployment` and `get_logs` to observe the eventual result. Read the immutable deployment config and variable snapshot using the normal product views.

Use the same `request_key` for retrying the same source and versions in the same conversation. The server derives its durable idempotency key from the human actor, conversation and request key. Changed files or versions with the same key are refused. A new conversation has its own key namespace. Retries revalidate current authority and target versions; they can be refused after revocation or a target change even when an earlier attempt was accepted.

## Bounds and data handling

Each request contains 1–64 regular text files, at most 128 KiB per file and 512 KiB total UTF-8 contents. The tool also limits the encoded JSON request to 1 MiB, and its deterministic compressed archive to 1 MiB. Paths must be canonical relative POSIX paths, at most 240 bytes and eight components deep. Absolute paths, traversal, backslashes, control/invisible formatting characters, credential/config directories and reserved metadata paths are refused. Binary contents, symlinks and special files are not expressible by this input mode. Executable file modes are not accepted; archive files use mode 0644.

Source bytes supplied to the model intentionally appear in the persisted chat tool transcript. Do not include credentials in source files. The audit entry replaces the `files` object with a digest-bearing redaction. The staged artifact has canonical workspace/project/service/environment/operation/deployment ownership; the worker verifies that ownership before materializing it. Publication and final acceptance repeat the current authority and version checks after upload staging. Acceptance captures the immutable deployment config and variable-version metadata in the same transaction, so later desired-config edits cannot change the queued source build. Failed or canceled work follows the existing artifact reservation, reference-release and garbage-collection rules.

## Verification boundaries

`TestTenantAgentSourceCanonicalIntakeAuthorityReplayAndVersionFences` exercises the production HTTP authentication and chat scope, source adapter, PostgreSQL acceptance/outbox, real filesystem publication, replay conflicts, role/scope refusals, all three version fences, a change during staging and revoked authority. Its upstream model caller is a local double.

`TestTenantAgentSourceChatBuildKitRuntimeHTTP` starts at the production tenant chat route, obtains versions through the read tool, proves mutation-off refusal and retry coalescing, runs the production leased source build worker against actual BuildKit and a disposable registry, dispatches the durable deploy handoff, and uses the production Docker adapter/deployment executor to verify an application HTTP marker and canonical activation. Cleanup asserts no owned containers remain and removes the disposable network. Its upstream model caller is also a local double; its HTTP target is private and has no public route.

The PostgreSQL case is required by `scripts/ci/integration-gate.sh`. The real build/runtime case is required by `scripts/ci/live-runtime-gate.sh`; a missing or skipped canary fails that gate. These are coverage mechanisms; consult the exact CI run for pass/fail evidence. Neither test establishes paid-provider tool selection, a live GitHub installation, public DNS, native ACME or production TLS, whole-host recovery, or production daemon hardening. The Docker fixture permits developer-daemon baseline warnings while retaining the runtime's cgroup/API requirements.
