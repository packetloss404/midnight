# S01_FOUNDATIONS - Phase 0 Worker Handoff

## Scope

Worker S01 owns the Sprint 01 foundations lane from
`agile/sprints/SPRINT_01_FOUNDATIONS.md`: repository scaffold, Go module
layout, local developer bootstrap, lint/pre-commit baseline, CI and supply
chain checks, governance files, ADR seed material, release-signing scaffolding,
npm lockfile policy, naming/codename records, and issue/PR templates.

Primary implementation constraints come from:

- `agile/sprints/SPRINT_01_FOUNDATIONS.md`
- `agile/engineering/skeletons/REPO_LAYOUT.md`
- `agile/engineering/skeletons/MODULE_DEPENDENCY_MATRIX.md`

This is not a completion claim. At handoff time, the workspace contains agile
planning material and `midnight-platform-plan.md`; the product repository
scaffold does not exist yet. There is no `go.mod`, `cmd/`, `internal/`, `pkg/`,
`api/`, `schemas/`, `docs/`, `deploy/`, `test/`, `tools/`, `web/`, or
`.github/` product tree. The workspace now has Git metadata, but branch
protection, remote checks, and CI checks cannot be verified until S01 creates or
connects the product repository and records the protected-branch settings.

## Owned Future Implementation Paths

S01 owns creation and initial wiring of these paths. Other workers may consume
the packages and docs after S01 publishes the scaffold, but should not need to
invent alternate roots.

- Repository root: `README.md`, `.gitignore`, `.editorconfig`, `go.mod`,
  `go.sum`, optional `go.work`, `Taskfile.yml`, `Makefile`,
  `.golangci.yml`, `.pre-commit-config.yaml`, `LICENSE`,
  `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `MAINTAINERS.md`,
  `CODEOWNERS`
- GitHub automation: `.github/workflows/lint.yml`,
  `.github/workflows/test.yml`, `.github/workflows/supplychain.yml`,
  `.github/workflows/build.yml`, `.github/ISSUE_TEMPLATE/*.md`,
  `.github/PULL_REQUEST_TEMPLATE.md`, commitlint config
- Binary scaffold: `cmd/midnight-server/`, `cmd/midnight-worker/`,
  `cmd/midnight-agent/`, `cmd/midnight/`,
  `cmd/midnight-buildkit-supervisor/`
- Package scaffold only: `internal/api/`, `internal/api/middleware/`, and the
  package directories listed in `REPO_LAYOUT.md` and
  `MODULE_DEPENDENCY_MATRIX.md`; S01 should avoid implementing business logic
  for other feature lanes
- Public package scaffold: `pkg/midnightctl/`, `pkg/orailtypes/`
- API and contract roots: `api/openapi/v1alpha/`, `api/openapi/v1/`,
  `api/asyncapi/v1/`, `proto/agent/v1/`, `schemas/`, `migrations/`
- Web supply-chain root: `web/package.json`, `web/package-lock.json`,
  `web/.npmrc`, plus minimal TypeScript/Vite placeholders only if needed for
  CI to run
- Build, release, and ops roots: `deploy/`, `test/`, `tools/`,
  `tools/versions.txt`, `tools/lint/`
- Documentation roots: `docs/architecture/repo-layout.md`,
  `docs/contributing/style.md`, `docs/contributing/ci.md`,
  `docs/contributing/dev-setup.md`, `docs/contributing/npm.md`,
  `docs/governance/branch-protection.json`,
  `docs/governance/merge-policy.md`, `docs/adr/`, `docs/security/keys.md`,
  `docs/runbooks/key-rotation.md`, `docs/brand/logo.svg`,
  `docs/error-codes.md`

## Dependency Outputs Other Workers Need

S01 must publish these outputs before downstream implementation lanes assume
stable paths or CI behavior:

- Canonical module root: `github.com/midnight/midnight`, Go 1.23+, and a
  passing `go build ./...` on the seed codebase
- Stable binary names and directories for `midnight-server`,
  `midnight-worker`, `midnight-agent`, user CLI `midnight`, and the BuildKit
  supervisor; the open naming conflict below must be resolved first
- `Taskfile.yml` and `Makefile` targets: `bootstrap`, `build`, `test`, `lint`,
  `supplychain`, `generate`, and `adr-index`
- CI status check names required by branch protection: lint, test,
  govulncheck, osv-scanner, npm-audit, build, and commitlint
- `CODEOWNERS` coverage for every top-level directory so feature-lane PRs route
  to the correct reviewers
- Dependency matrix enforcement plan in `.golangci.yml` using `depguard`, with
  exceptions matching `MODULE_DEPENDENCY_MATRIX.md`
- Governance baseline: Apache-2.0 license, contributing guide, security
  disclosure process, maintainers, code of conduct, merge policy, branch
  protection export, issue templates, PR template, Conventional Commits policy
- ADR baseline: template, ADR-0001 adopting panel decisions as normative, and
  ADR-0002 locking Midnight name, `midnight-*` binaries, `midn_` token prefix,
  and codename status
- Supply-chain baseline: lockfile policy, `web/package-lock.json` v3,
  `web/.npmrc`, `govulncheck`, `osv-scanner --lockfile`, and
  `npm audit --omit=dev` wired into local and CI paths
- Release-signing baseline: public-key documentation, signing-key runbook, and
  gated build workflow hooks; actual private-key provisioning remains blocked
  until the KMS/offline key-generation decision is made

## Acceptance Checklist

All items are currently unchecked because product code has not been created.

- [ ] S01-001: Top-level repo tree created from `REPO_LAYOUT.md`; root Go
  module exists; seed binaries compile; `go build ./...` and `go mod tidy`
  are clean
- [ ] S01-002: `.golangci.yml`, `.pre-commit-config.yaml`, `tools/lint/`, and
  documented style rules exist; lint passes on seed code
- [ ] S01-003: GitHub Actions for lint, test, supply-chain scan, and build are
  present; fork PRs do not receive secrets; required supply-chain checks run
- [ ] S01-004: Branch protection settings are documented and applied for
  `main` and `release/*`; `CODEOWNERS` covers every top-level directory
- [ ] S01-005: `LICENSE`, `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, and `MAINTAINERS.md` exist and are linked from
  `README.md`
- [ ] S01-006: ADR template, ADR-0001, and generated ADR index exist; ADR-0001
  records the panel decisions, including normative resource ID format
- [ ] S01-007: Cosign and GPG public-key docs and key-rotation runbook exist;
  release workflow is prepared to sign only from protected refs
- [ ] S01-008: `web/` uses npm for v0, commits `package-lock.json` v3, and CI
  rejects dependency edits without matching lockfile updates
- [ ] S01-009: ADR-0002 locks public name Midnight, binary prefix
  `midnight-*`, API token prefix `midn_`, and codename status; placeholder logo
  exists
- [ ] S01-010: `make bootstrap` installs or verifies pinned local tools on
  supported developer platforms and reports native Windows as unsupported
- [ ] S01-011: Issue templates, PR template, Conventional Commits enforcement,
  and commitlint required check are present

## Risks And Open Decisions

- The workspace has local Git metadata, but CI, branch protection, CODEOWNERS
  routing, required checks, and protected-branch release gating cannot be
  exercised until S01 creates or connects the product repository and records the
  remote protected-branch settings.
- Product code scaffold is absent. Do not report Sprint 01 as complete until
  the checklist above is implemented and verified.
- `SPRINT_01_FOUNDATIONS.md` says web must use npm for v0, while
  `REPO_LAYOUT.md` describes the dashboard as a Vite/pnpm workspace. Proposed
  S01 resolution: use npm for Sprint 01 supply-chain acceptance and record the
  deviation in ADR-0002 or a follow-up ADR/update to architecture docs.
- Binary naming needs one explicit decision: Sprint 01 acceptance reserves
  `midnight-buildkit` and `midnight-registry`, while `REPO_LAYOUT.md` creates
  `cmd/midnight-buildkit-supervisor/` and models registry as
  `internal/registry` rather than a standalone binary.
- Release-signing key provisioning depends on KMS/offline key-generation
  choice. S01 can create docs, CI gates, and public-key locations, but should
  not fabricate private-key material.
- Branch protection and fork-secret blackout require GitHub repository admin
  access and audit-log verification; local file scaffolding alone is
  insufficient.
- Empty directories are not tracked by Git. If S01 creates directory-only
  skeletons, add intentional package docs, minimal compile-only code, or
  `.gitkeep` files according to the package purpose.
- `depguard` enforcement must match the dependency matrix exactly. Any added
  package edge requires an ADR and a synchronized matrix/config update.

## Exact First Implementation Tasks

1. Confirm the repository source of truth: initialize or clone
   `github.com/midnight/midnight` into `D:\projects\midnight`, set default
   branch to `main`, and verify Git operations work before creating CI files.
2. Resolve the two Sprint 01/skeleton conflicts before code lands: npm vs pnpm
   for `web/`, and exact BuildKit/registry binary names. Record the resolution
   in the first governance PR or ADR.
3. Create the top-level product tree from `REPO_LAYOUT.md`: `cmd/`,
   `internal/`, `pkg/`, `api/`, `proto/`, `schemas/`, `migrations/`, `docs/`,
   `deploy/`, `test/`, `tools/`, and `web/`.
4. Initialize the Go module as `github.com/midnight/midnight` with Go 1.23+,
   then add compile-only `main` packages for each reserved binary and minimal
   package docs for scaffold packages.
5. Add `Taskfile.yml` as the primary task runner and a thin `Makefile` shim for
   `bootstrap`, `build`, `test`, `lint`, `supplychain`, `generate`, and
   `adr-index`.
6. Add `.editorconfig`, `.golangci.yml`, `.pre-commit-config.yaml`,
   `tools/versions.txt`, and `tools/buildtools.go`; wire `gofumpt`,
   `goimports`, `golangci-lint`, `govulncheck`, `osv-scanner`, and pre-commit.
7. Add the root governance files and `README.md`, with links to license,
   contributing, security, CoC, maintainers, CI, and ADR docs.
8. Add ADR template, ADR-0001, ADR-0002, and the ADR index generator target.
9. Add `.github/` workflows with `permissions: read-all` defaults,
   `pull_request` and protected-branch `push` triggers, no
   `pull_request_target`, and release/signing steps gated to protected refs.
10. Add `web/package.json`, `web/package-lock.json` v3, and `web/.npmrc` using
    npm, then add CI guards for missing lockfiles and missing integrity hashes.
11. Add `CODEOWNERS`, issue templates, PR template, commitlint configuration,
    and branch protection documentation/export.
12. Run and record the first verification set: `go mod tidy`, `go build ./...`,
    `go vet ./...`, `task lint`, `task test`, `task supplychain`, and workflow
    syntax validation with `actionlint`.

## Handoff Completion Bar

S01 is complete only when the product scaffold exists, the commands above pass
from a clean clone, fork PR secret blackout is demonstrated, protected-branch
release gating is documented and enforced, and every Sprint 01 acceptance item
is checked with evidence in CI or committed docs.
