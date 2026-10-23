---
id: GOV-COSIGN-SDK-BOUNDARY
release_tag: v0.1.0-alpha
status: approved-per-product-owner-confirmation
owner: PM-B / Security
deciders: [PM-B, Security, Lane 3 owner, Lane 4 implementer]
related_decisions: []
related_blockers: [V010_ALPHA_GREENLIGHT_BRIEF §4 blocker (3)]
related_features: [§1.15 signed releases + SBOM/provenance + image-sig verify]
related_adrs: []
date: 2026-04-27
status_updated: 2026-04-30
---

# Cosign SDK Boundary (alpha + beta)

This document closes the `v0.1.0-alpha` greenlight blocker (3) — the
"cosign Go SDK approval" question recorded in
`agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md` §4 and
explicitly deferred at the end of Sprint 1 (sub-slice D, commit
`239e3e3`; see `agile/handoff/phase1-subagents/SPRINT1_FINISH_STATUS.md`
"Open questions for PM-A / PM-B" item 2).

The decision is binding for `v0.1.0-alpha` and `v0.4.0-beta`. The Go
SDK question is re-opened for `v1.0.0` once the dependency tree
stabilizes (see "Re-evaluation criteria" below).

## Decision

**Fork-exec the pinned `cosign` binary for `alpha` and `beta`. Re-evaluate
the Go SDK import for `v1.0.0` once the sigstore/cosign dependency tree
stabilizes and the verify-only subset can be extracted with a smaller
transitive surface.**

In concrete terms:

- `internal/runtime/docker/signature_verify.go` ships a
  `CosignExecVerifier` that wraps the pinned `cosign` binary. It is the
  panel-aligned implementation for `alpha` and `beta`.
- `StaticSignatureVerifier` remains the default-test verifier (no
  cosign on PATH; deterministic allowlist).
- `Adapter.SetSignatureVerifier(SignatureVerifier)` is the injection
  seam; `Adapter.EnableSignatureEnforcement(SignatureVerifier)` keeps
  its existing opt-in shape for backwards compatibility.
- `versions.yaml` carries a `cosign:` stanza that pins the binary
  version (`v2.6.3`) and the `linux-amd64` SHA-256 digest
  (`sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`).
  The release pipeline downloads the binary from the published
  release URL and verifies the digest before any signing or
  verification step runs (Blocker 4's territory; this doc records the
  pin, that PR consumes it).
- The Go SDK import (`github.com/sigstore/cosign/v2/...`) is
  **forbidden in `alpha` and `beta`**. CI lint rejects any new import
  of `github.com/sigstore/cosign/v2` until the v1 follow-up flips this
  decision.

## Rationale

### Why fork-exec, not the Go SDK

Importing `github.com/sigstore/cosign/v2/pkg/oci` (or any other
top-level cosign package) drags in a transitive surface that is
unacceptable for a `v0.1.0-alpha` agent that runs on customer
hardware:

- **containerd** (`github.com/containerd/containerd`) — multi-MB
  package set; itself transitively pulls runc, CNI plugins, and the
  containerd metadata layer.
- **go-containerregistry** (`github.com/google/go-containerregistry`)
  — the OCI distribution client; non-trivially overlaps with the
  Docker SDK we already vendor.
- **opa** (`github.com/open-policy-agent/opa`) — Rego policy engine
  pulled by cosign's policy module; we do not run policy here.
- **sigstore/rekor** + **sigstore/fulcio** clients — fine on their own
  but co-evolving with cosign's release cadence; pulling them via
  `cosign/v2` couples our patch cadence to cosign's.
- the docker image-spec stack and a handful of moby/sys/* indirect
  deps that we already get a small slice of via the Docker Engine
  API client.

The cumulative weight is in the tens of MB of source and several
hundred new transitive nodes for an agent binary that today stays
under 30 MB after `-trimpath -ldflags "-s -w"`. The supply-chain
blast radius of every CVE in that subtree becomes Midnight's blast
radius.

The fork-exec posture is **panel-aligned with how Lane 3 already
treats BuildKit and the OCI registry** (see `versions.yaml` Lane 3
selection rationale): both BuildKit (`buildctl`) and distribution
(`registry serve`) are run as pinned child processes, exactly because
their Go-client import surfaces are also unacceptable for a
stdlib-first agent. Cosign joins that trio.

### Why not the verify-only subset (today)

`github.com/sigstore/sigstore/pkg/signature` and the Rekor v1 client
(`github.com/sigstore/rekor/pkg/client`) are smaller, more mature,
and have much narrower transitive surfaces than `cosign/v2/...`. They
are a credible **v1 candidate** for in-process verification.

We do not ship them in `alpha` for two reasons:

1. **Verify-only parity is not yet provable.** Cosign's verify path
   includes Rekor inclusion checks, Fulcio CT-log validation, TUF
   trust-root pinning, and (in keyless mode) certificate identity
   matching. Re-implementing that against the lower-level
   `sigstore/sigstore` libraries is non-trivial and error-prone.
   Until we can write a parity test that asserts byte-for-byte
   equivalence with `cosign verify` we should not own that code.
2. **The cosign binary already handles the above correctly.** The
   `cosign-installer` GitHub Action installs a pinned, signed binary
   for CI; the Debian package can ship the same binary as a
   conditional `Recommends` for the agent's enforced-verification
   surface. Re-implementing what the binary already does buys us
   nothing in `alpha`.

The verify-only-subset alternative is documented here so the v1
follow-up has a clear starting point. See "v1 follow-up plan".

### Why the cosign-binary-bundled `.deb` was rejected

A third option floated in `SPRINT1_FINISH_STATUS.md` was bundling the
cosign binary inside our `.deb`. We reject that because:

- It means we ship a sigstore release artifact under our own package
  fingerprint — the operator audit trail conflates our supply chain
  with sigstore's.
- The cosign binary is large (~135 MB for the linux-amd64 build at
  `v2.6.3`); bundling it 1:1 would more than triple the `.deb` size.
- Operators on existing sigstore-aware fleets will already have a
  cosign install in `$PATH`; bundling forces a duplicate.

Instead, we ship `cosign` as a **soft dependency** (the agent runs
without it; only the enforced-verification surface needs it), with
the agent config flag `cosign_binary_path` letting operators point at
their own install. The release workflow (Blocker 4) installs the
pinned binary via `sigstore/cosign-installer` for CI runs only.

## Operational requirements (what this decision binds)

The fork-exec posture is acceptable for `alpha` and `beta` only if the
following conditions are met by the consuming PRs (Blocker 4 +
Sub-slice D's hardening this branch lands):

1. **Pinned binary version in `versions.yaml`.**
   - Field: `cosign: v2.6.3`.
   - Field: `cosign_linux_amd64_digest: sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`.
   - Field: `cosign_linux_arm64_digest: sha256:b7c23659a50a59fd8eec44b87188e9062157d0c87796cac7b38727e5390c4917`.
   - Loaded by `tools/release-verify` and (in the v1 follow-up) by
     `internal/build` so a build-time mismatch is a hard error.
2. **Hash-verified download in the release workflow.**
   `.github/workflows/release.yml` already invokes
   `sigstore/cosign-installer@v3` with `cosign-release: ${{ env.COSIGN_VERSION }}`.
   The Blocker 4 PR will switch the workflow's `COSIGN_VERSION` env to
   read from `versions.yaml` (so the supply-chain pin lives in one
   place) and add a defensive `sha256sum -c` check against the
   architecture-specific digest pinned here. This document
   pre-authorizes that change; the actual workflow edit is **not** in
   scope for this branch (Blocker 3 stays out of `release.yml`).
3. **Agent-config flag `cosign_binary_path`.**
   The agent reads the path to the cosign binary from its config; the
   default is the empty string (signature enforcement is then
   off-by-construction). When `verify_signatures: true` is set, the
   agent MUST also have `cosign_binary_path` populated; absent that
   pair the agent emits `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING` at
   boot and refuses to enable enforcement.
4. **Reproducible-cosign-version assertion at agent startup.**
   When signature enforcement is enabled, the agent runs
   `cosign version --json` once at boot, parses the
   `GitVersion` field, and refuses to start if the value does not
   match the pin in `versions.yaml`. This means a host with the wrong
   cosign on PATH cannot silently downgrade verification semantics.
   Operators see `VERSION_SKEW_REJECTED` (already registered) with the
   exact mismatch in the error detail.
5. **Verify timeout budget.**
   Each `cosign verify` invocation runs under a `context.WithTimeout`
   budget (default 30s) inherited from the calling
   `Adapter.VerifyImageSignature`. Exceeding the budget returns
   `IMAGE_SIGNATURE_VERIFY_TIMEOUT`. The default is operator-tunable
   via the agent config flag `cosign_verify_timeout_seconds`.
6. **Transparency log handling.**
   When `cosign verify` reports a Rekor / Fulcio failure (CT log
   entry missing, transparency-log signature invalid, certificate
   identity mismatch), the agent surfaces
   `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED` rather than the
   broader `IMAGE_SIGNATURE_INVALID`. This split lets SIEMs page
   security on transparency-log breaches separately from "wrong
   signing key" failures.

## Test coverage requirements

The implementation MUST ship the following coverage so the v1 swap to
the Go SDK is verified-equivalent rather than a leap of faith:

| Coverage | File | Build tag(s) | What it asserts |
|---|---|---|---|
| Unit (default) | `internal/runtime/docker/signature_verify_test.go` | none | `StaticSignatureVerifier` allowlist semantics; `Adapter.SetSignatureVerifier` injection; absent-verifier no-op. Default `go test ./...` MUST require no cosign on PATH. |
| Unit (stub binary) | `internal/runtime/docker/cosign_exec_verifier_test.go` | none | A test-only Go-built `cosign-stub` returns canned exit codes / outputs. Asserts: success path, `IMAGE_SIGNATURE_INVALID` on exit 1, `IMAGE_SIGNATURE_REQUIRED` on "no signatures found", `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING` on `os.ErrNotExist`, `IMAGE_SIGNATURE_VERIFY_TIMEOUT` on context cancellation, `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED` on Rekor/CT failure stderr. |
| Parity (skeleton) | `internal/runtime/docker/cosign_parity_test.go` | none for the canonical-input table; `cosign_sdk` for the SDK side | Compares `CosignExecVerifier` to a hypothetical `CosignSDKVerifier`. The SDK side is a skeleton today (build-tag-gated `// +build cosign_sdk`); when the v1 swap happens the parity test enforces equivalence. |
| Integration | `internal/runtime/docker/cosign_exec_verifier_integration_test.go` | `integration && cosign` | Invokes the real `cosign` binary against a known-signed test image. Default test lane skips this; CI runs it on the cosign-aware lane. |

The parity test is the centerpiece. It captures the **canonical
input/output** of the verifier — image ref, expected key/identity,
returned `failurecodes.Code` — so the v1 PR can flip from `Exec` to
`SDK` and prove equivalence by green tests.

## v1 follow-up plan

The v1 PR (post `v0.4.0-beta`, no earlier than `v1.0.0` planning) is
expected to:

1. Add a build-tag-gated implementation file
   `internal/runtime/docker/cosign_sdk_verifier.go` with build tag
   `cosign_sdk` and import `github.com/sigstore/sigstore/pkg/signature`
   + `github.com/sigstore/rekor/pkg/client` (NOT
   `github.com/sigstore/cosign/v2/...`).
2. Make the parity test exercise both implementations against the same
   canonical input table.
3. Promote `CosignSDKVerifier` to the default agent posture once
   parity is green for one full beta cycle.
4. Keep `CosignExecVerifier` as a fallback for hosts without the SDK
   build tag enabled.

The trigger conditions for opening v1 are:

- The verify-only subset (`sigstore/sigstore` + `rekor/pkg/client`)
  has had two consecutive minor releases without a security advisory
  in the transitive set.
- The cumulative `go mod graph` weight added by the SDK swap is
  measured (target: < 12 MB of new source, < 30 net new modules).
- The parity test on this branch has been green for ≥ 30 days against
  a real cosign binary in CI.

## Re-evaluation criteria (when to re-open this decision)

This decision is re-opened — irrespective of the v1 schedule — if any
of the following becomes true:

- A `cosign verify` CVE forces a binary patch cadence faster than our
  release cadence can absorb. (Mitigation: pin a rolling-patch range
  rather than a single `v2.6.3`.)
- The cosign maintainers ship a verify-only subset distribution
  (i.e. `cosign-verify` as a separate binary or a published
  `sigstore/cosign-verify` Go module) — that is strictly preferable
  to the current full-cosign import.
- A panel-locked feature requires cosign verification on a per-pull
  hot path where the fork-exec round-trip cost (≈ 80 ms warm) is the
  measured bottleneck. We do not currently have such a path.

## Cross-references

- Greenlight blocker: `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`
  §4 blocker (3).
- Sprint 1 deferral note: `agile/handoff/phase1-subagents/SPRINT1_FINISH_STATUS.md`
  § "Sub-slice D — §1.15 signed releases + SBOM/provenance + image-sig verify"
  → "Deferred items" → "sigstore/cosign Go SDK integration".
- Lane 3 fork-exec posture (paneled): `versions.yaml` (BuildKit +
  distribution rationale comments).
- Agent-side seam: `internal/runtime/docker/signature_verify.go`
  (`SignatureVerifier`, `StaticSignatureVerifier`, `CosignExecVerifier`,
  `Adapter.EnableSignatureEnforcement`, `Adapter.SetSignatureVerifier`).
- Failure codes added by this branch:
  `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING`,
  `IMAGE_SIGNATURE_VERIFY_TIMEOUT`,
  `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED` (see
  `internal/failurecodes/codes.go` and `docs/error-codes.md`).
- Operator runbook: `docs/release-verify.md` (Step 2 + the failure-code
  mapping table). The runbook will be extended by the Blocker 4 PR
  once the workflow consumes the pinned cosign version.

## PM-B / Security sign-off block

Status update: PM-B / Security approval for the fork-exec posture was
received per product owner / user confirmation on 2026-04-30. The
placeholder signer fields below remain unmodified because this document
does not contain named signer evidence.

```yaml
cosign_sdk_boundary_signoff:
  decision_id: GOV-COSIGN-SDK-BOUNDARY
  status: approved-per-product-owner-confirmation
  confirmation_date: 2026-04-30
  confirmation_source: product-owner/user confirmation
  binding_for_releases: [v0.1.0-alpha, v0.4.0-beta]
  reopens_at: v1.0.0
  fork_exec_posture: ratified
  go_sdk_imports_forbidden: true
  pinned_cosign_version: v2.6.3
  pinned_cosign_linux_amd64_digest: sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4
  pinned_cosign_linux_arm64_digest: sha256:b7c23659a50a59fd8eec44b87188e9062157d0c87796cac7b38727e5390c4917
  agent_config_flags:
    - cosign_binary_path
    - cosign_verify_timeout_seconds
    - verify_signatures
  failure_codes_added:
    - IMAGE_SIGNATURE_VERIFY_BINARY_MISSING
    - IMAGE_SIGNATURE_VERIFY_TIMEOUT
    - IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED
  parity_test_ref: internal/runtime/docker/cosign_parity_test.go
  v1_followup_owner: PM-B
  signers:
    pm_b:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    security:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    lane3_owner:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
```

> Status note: approval was received per product owner / user
> confirmation on 2026-04-30. The placeholder dates are preserved as
> historical evidence fields and should only be replaced by named signer
> evidence.
