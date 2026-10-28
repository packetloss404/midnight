# CI and Local Verification

GitHub-hosted Actions are the default source of pull-request and `main` check
status. A payment-method, budget, quota, or provider outage can prevent a job
from starting before repository code runs. That condition is account-level;
changing application code cannot clear it.

## Local full gate

Run the provider-independent gate from a clean reviewed commit:

```sh
make ci-local
```

It runs, in order, the release check, Go race suite, browser acceptance,
workflow lint, `govulncheck`, OSV scans, and CGO-free Linux amd64/arm64 builds.
Go commands use the exact patch toolchain declared by `go.mod`, regardless of
the workstation's default Go version.
The result and combined log are written to `artifacts/ci-local/` and bound to
the exact Git commit. A dirty worktree is rejected by default. The clean path
runs in a detached temporary worktree so untracked files and independent nested
repositories cannot enter the build graph. During development,
`scripts/ci/local-gate.sh --allow-dirty` runs the current checkout and records
evidence marked `release_eligible: false`.

Local evidence is not a GitHub status check and must never be represented as
one. Use it to keep verification moving and attach the JSON and log to a review
or release record when hosted jobs cannot start.

Install the browser used by the gate once on a workstation or CI VM:

```sh
npm --prefix web-next ci
npx --prefix web-next playwright install --with-deps chromium
```

For a smaller change, run the matching focused checks:

- `make lint`
- `make test`
- `make build`
- `make supplychain`
- `make release-check` for release, gate, contract, or tooling changes
- `make ci-local-test` after changing the local gate itself

## Disposable end-to-end jobs

The `test` workflow has separate Docker/PostgreSQL integration, real
BuildKit/Traefik runtime, and PostgreSQL human-session browser jobs. They are
additional to the default Go/race and web `test:gate0` checks; `make ci-local`
does not implicitly run these three jobs.

```sh
bash scripts/ci/integration-gate.sh
bash scripts/ci/live-runtime-gate.sh
(cd web-next && npx playwright test --config playwright.postgres.config.ts)
```

The integration gate needs Docker, Go, jq, and Python 3. It builds the S3 test server
from pinned MinIO source because the old Docker Hub/Quay binary images are
unavailable, runs all integration-tagged packages with an independent reaper
session per executable, and requires actual pass
events for durable boot/restart, source intake/materialization, Docker
one-off/volume replacement, and S3/sealed-credential round trips. Supplying
`MIDNIGHT_IDENTITY_TEST_DSN` also makes its administrator-recovery canary
mandatory; hosted CI provides a disposable PostgreSQL service. Skipped tests
remain a separate ledger, not successful execution evidence.

The runtime gate requires Linux Docker with privileged containers and host
networking. It owns disposable BuildKit, registry, and Traefik fixtures.
Required checks perform real builds/pushes and verify Traefik's HTTP backend
marker, watched replacement, and unknown-host 404. This gate does not deploy
the built image through Midnight's worker or prove external DNS/HTTPS.

The PostgreSQL browser lane needs Node/npm, Chromium, Go, and Linux/macOS
key-file permissions. Its startup script creates its own PostgreSQL Docker
container, or accepts `MIDNIGHT_PG_E2E_DSN` only for a disposable loopback
database named `midnight_browser_e2e`. Ports 4182/4183 must be free. The real
browser covers enrollment/TOTP, sessions, desired configuration and encrypted
variables, compare-and-swap, and logout revocation, with no request mocks.
CI uses a separate disposable PostgreSQL service and retains failure
diagnostics. No production database or external provider account is needed.
Additional journeys cover invitation roles, scoped automation credentials,
and administrator recovery. Non-bootstrap users are explicit disposable
identity fixtures. Credential-bearing browser traces and screenshots are
disabled; this is not evidence of a new-account registration flow.

The Railpack toolchain job builds the production worker image and pinned
frontend, pushes and runs Node/Python/Go/Dockerfile fixtures, and checks a real
secret mount without plaintext in retained artifacts. It needs Docker,
privileged BuildKit, outbound access to the pinned registries, Go, and Python.
It does not deploy those images through Midnight's installed control plane.

The offline recovery CLI job uses only an owned PostgreSQL Docker container
and temporary filesystem roots. It backs up real bytes, destroys both fixture
states, rejects wrong passphrases/confirmation and staged tampering, restores,
reads back, and replays a completed apply. Its default file destination is
local proof. Setting `MIDNIGHT_PARITY_BACKUP_DESTINATION` to a credential-free
S3 URI uses the existing backup credential chain; it still does not claim a
whole-host-loss drill. No operator database or production roots are accepted.

Integration JSON results and skip names are written to
`artifacts/ci-integration/`; runtime results/logs go to
`artifacts/ci-live-runtime/`. The workflow uploads both even on failure.
Toolchain and recovery evidence use `artifacts/ci-toolchain/` and
`artifacts/ci-recovery/`; their dedicated jobs retain them on failure too.
Per-commit job results, rather than the presence of these commands, establish
which assertions passed. See [feature verification](../feature-verification.md)
for the remaining installed/external boundaries.

## Hosted-runner continuity

Every workflow resolves `runs-on` from the repository variable
`MIDNIGHT_CI_RUNNER`, falling back to `ubuntu-24.04`. Leave the variable unset
for GitHub-hosted runners.

If hosted runners are unavailable for an extended period, register a dedicated
Ubuntu 24.04 CI VM with a custom label such as `midnight-ci`, then set:

```sh
gh variable set MIDNIGHT_CI_RUNNER --body midnight-ci
```

The VM must be disposable or dedicated to CI, with at least Git, curl, jq,
make, Docker, Go, Node/npm, and the Playwright Chromium dependencies. Give its
service account no production credentials and clean the workspace between
jobs. Never register the Midnight production VM, database host, or marketing
host as a repository runner: workflow code can execute arbitrary commands with
the runner account's authority.

Unset the variable to return to hosted runners:

```sh
gh variable delete MIDNIGHT_CI_RUNNER
```

Runner registration tokens are short-lived secrets. Create them in GitHub's
repository runner settings and never paste them into issues, logs, scripts, or
commits.

## Account-level hosted-runner recovery

When every workflow fails before its first step with a spending-limit or
payment message, the repository owner must resolve the payment method and
Actions budget in GitHub billing settings. Re-run the failed workflows only
after the account reports available Actions usage. This is intentionally a
human financial action, not an automated repository mutation.

Fork pull requests must not use `pull_request_target`, repository secrets,
registry pushes, or release signing steps.

References:

- [GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
- [Set up budgets](https://docs.github.com/en/billing/how-tos/set-up-budgets)
- [Use variables in workflows](https://docs.github.com/en/actions/concepts/workflows-and-actions/variables)
- [Use self-hosted runners](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/use-in-a-workflow)
- [Secure use of GitHub Actions](https://docs.github.com/en/actions/reference/security/secure-use)
