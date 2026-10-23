# Public Launch Checklist

This checklist tracks repository hygiene that must be true before Midnight
accepts external contributors.

## Required Before Opening The Repository

- Enable GitHub private vulnerability reporting for outside reporters and
  confirm the advisory link in `SECURITY.md` is reachable before making the
  repository public.
- Confirm `@packetloss404`, or its successor team, remains a valid owner for
  every entry in `CODEOWNERS`.
- Apply the branch protection baseline in
  `docs/governance/branch-protection.json`.
- Keep [deployatmidnight.com](https://deployatmidnight.com) published as the
  project website. Release signing remains disabled until that site publishes
  the reviewed release-key fingerprint mirror.
- Publish a real release-signing fingerprint in
  `docs/release/keys/midnight-release-fingerprint.txt` before trusting
  signed `.deb` artifacts.
- Confirm the Code of Conduct moderation owner in `MAINTAINERS.md`.
- Confirm release-key custody owners and backup custodians in a private
  operator runbook; do not publish private contact details.

## Pull Request Baseline

- Public PRs go through normal `pull_request` review, never
  `pull_request_target`.
- Fork PRs do not receive repository secrets, release signing keys,
  registry push credentials, or deployment tokens.
- Manual GitHub Actions remain operator checks until the project chooses
  required status checks.
- Contributors paste relevant local command output in the PR template
  when touching code, release, contract, or governance paths.

## Remaining Launch Substitutions

| Placeholder | Where | Operator action |
|---|---|---|
| Website fingerprint mirror | `https://deployatmidnight.com/release/keys/midnight-release-fingerprint.txt` | Publish the reviewed fingerprint at this reserved path before signed releases. |
| Release fingerprint | `docs/release/keys/` | Publish real fingerprint after custody review. |

Public launch is allowed to keep examples and non-routable placeholders
only when the surrounding text says they are examples and no operator is
asked to trust or contact them.
