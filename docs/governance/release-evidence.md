# Release Evidence

Before cutting a Phase 0 release candidate, run `release-check` and attach local verification evidence that includes these exact commands and their outcomes:

- `go test ./...`
- `go vet ./...`
- `go build ./...`
- `go run ./tools/lint`
- `go run ./tools/contract-tests`
- `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files --release-summary artifacts/phase0/release-summary.json`
- `go run ./tools/phase0-gate --format=json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files`

The executed drill gate is required release evidence until a remote manual gate records equivalent drill execution.
The release evidence index must also be verified with strict referenced evidence-file existence checks before release evidence is accepted.

For `v0.1.0-alpha`, the evidence packet must also name the durable boot/package state: `midnight-server serve` and `midnight-worker serve` are the package-managed service entrypoints, `midnight admin migrate --no-prompt` applies embedded Postgres migrations when `MIDNIGHT_DB_DSN` or `MIDNIGHT_DB_DSN_FILE` is configured, and post-install setup generates the cursor key plus registry config before running doctor. If the release is accepted with any of those paths unverified on a real PR or RC package, record the exception in the greenlight sign-off block rather than describing it as complete.

Local automation must keep the release evidence flow consistent:

- `phase0-drills` runs the executable drill gate used by `release-check`.
- `release-check` writes release evidence under `artifacts/phase0`.
- `clean-artifacts` removes only generated contents under `artifacts/phase0` before evidence is regenerated.

Generated release evidence JSON paths must be explicit, clean, single-file paths ending in `.json`; they must not use relative traversal or glob patterns. Release automation must write the evidence packet to `artifacts/phase0/release-evidence.json`, the release evidence index to `artifacts/phase0/release-evidence-index.json`, and the release summary to `artifacts/phase0/release-summary.json`.
