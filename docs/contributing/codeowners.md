# Code Ownership

`CODEOWNERS` at the repository root drives auto-review routing. Every
top-level directory in the repository must have at least one rule that
assigns an owner. The lint check `LINT_SCAFFOLD_GUARD_FAILED` enforces
this in two layers:

1. The hard-coded `requiredCodeownersGlobs` list in `tools/lint/main.go`
   guarantees that the canonical Phase 0 directories (`/cmd/`, `/internal/`,
   `/api/`, `/deploy/`, `/test/`, `/tools/`, `/web/`) always carry a rule.
2. The dynamic `missingTopLevelCodeowners` walk asserts that every
   on-disk top-level directory (excluding the `codeownersTopLevelDenyList`
   set: `.git`, `.github`, `.claude`, `node_modules`) carries a rule.

When a new top-level directory lands, also add the matching `/dir/ @owner`
line in `CODEOWNERS` in the same PR. The lint failure message will name the
missing glob.

## Current Ownership

`@packetloss404` currently owns every path. Keep the path-specific rules even
while they name the same owner: they document review boundaries and let the
project assign future teams without reconstructing the ownership map.

## Sub-Path Overrides

Sub-paths can override the parent rule by adding a more specific glob below
the parent. CODEOWNERS uses last-match-wins, so order specific rules after
broader ones. The `LINT_SCAFFOLD_GUARD_FAILED` check does not require
sub-path coverage, only top-level coverage; sub-path rules are an
optimization for routing review traffic.

## Reviews From Forks

PRs from forks do not receive repository secrets and do not push images per
the supply-chain policy. CODEOWNERS still routes the review request even when
the PR cannot exercise secret-bearing workflows.
