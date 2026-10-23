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
