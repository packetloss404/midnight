---
id: GOV-DESIGN-PARTNER-SIGNOFF
decision_ref: P0-D022
status: decided
owner: PM-B / Product
deciders: [PM-B, Product]
related_decisions: [P0-D004]
related_features: []
related_adrs: []
date: 2026-04-27
---

# Design-Partner Signoff Template (P0-D022)

This document closes `PHASE0_DECISION_LOG.md` row **P0-D022 — Phase 0
design-partner signoff counts**. It ratifies the per-tag counts
already in `PROGRAM_OVERVIEW.md` §3 and `RELEASE_PLAN.md` §2.0 (which
were pre-locked) and provides the per-partner signoff template, the
aggregation contract, the reviewer matrix, and the lifecycle.

## Decision — per-tag signoff counts

| Tag | Required signoff | Source |
|---|---|---|
| `v0.1.0-alpha` | **2 design partners** install successfully using only published install docs (`/usr/share/midnight/runbooks/install.md` + `docs/release-verify.md`). No deploy journey required. | `RELEASE_PLAN.md` §2.0; `PROGRAM_OVERVIEW.md` §3. |
| `v0.2.0-alpha` | **3 design partners** complete the first product loop unaided (install → deploy from existing OCI image → generated HTTPS hostname → logs/timeline visible → rollback). | Same. |
| `v0.3.0-alpha` | **5 design partners** sign off on the first product loop **and at least 1 partner runs the backup/restore drill** on their own VM. | Same. |
| `v0.4.0-beta` and onward | Counts are governed by the Beta release plan (see `RELEASE_PLAN.md` §3); this document does not override them. | n/a |

Counts are floors, not caps. Additional partners may sign off; the
release-evidence aggregator records all signoffs and the release does
not ship until the floor is met.

## Per-partner signoff template

Each design partner submits one yaml-fronted markdown file using the
template below. The file is committed under
`agile/release-evidence/v0.X.0-alpha/signoffs/<partner_slug>.md`.

```yaml
---
id: SIGNOFF-<PARTNER_SLUG>-V0X0-ALPHA
release_tag: v0.X.0-alpha
release_candidate: <rcN if applicable>
partner:
  slug: <kebab-case unique handle>
  display_name: <human-friendly name>
  primary_contact: <email or Slack handle>
environment:
  os_distribution: <"Ubuntu 24.04" | "Debian 12">
  os_version: <full version string from `lsb_release -d`>
  kernel: <`uname -r` output>
  architecture: <amd64|arm64>
  cpu_cores: <integer>
  ram_gb: <integer>
  disk_gb: <integer>
  cgroup_version: <"v2" — required; "v1" must be refused, see EC3>
  docker_engine_version: <e.g. "Docker version 24.0.7, build afdd53b">
  docker_engine_api: <e.g. "1.43">
  storage_driver: <e.g. "overlay2">
  daemon_json_baseline_state: <"clean" | "merged" | "conflict-refused">
  network_baseline:
    ufw_state: <"active" | "inactive" | "n/a">
    iptables_mode: <"nft" | "legacy" | "n/a">
  apparmor_state: <"enforced" | "complain" | "disabled">
  selinux_state: <"enforcing" | "permissive" | "disabled">
midnight:
  midnight_version: v0.X.0-alpha
  install_method: <"deb-from-apt" | "deb-direct" | "source-build">
  install_started_at: <UTC ISO8601>
  install_completed_at: <UTC ISO8601>
first_product_loop_milestones_reached:
  install_completed: <true|false>
  doctor_passed: <true|false>          # required for v0.1; informational for v0.2+
  bootstrap_admin_first_login: <true|false>  # required for v0.1
  deploy_from_oci_image: <true|false>  # required for v0.2 / v0.3
  https_on_generated_hostname: <true|false>  # required for v0.2 / v0.3
  logs_and_timeline_visible: <true|false>    # required for v0.2 / v0.3
  rollback_by_digest: <true|false>     # required for v0.2 / v0.3
  backup_restore_drill: <true|false>   # required for ≥1 partner at v0.3
friction_points:
  - description: <free text>
    blocker: <true|false>
    workaround: <free text or "none">
    suggested_remediation: <free text or "n/a">
blockers_found:
  - failure_code: <`docs/error-codes.md` row, or "UNCATEGORIZED">
    repro_steps: <numbered list as inline markdown or external link>
    severity: <"low" | "medium" | "high" | "critical">
    status: <"open" | "fixed-in-rc" | "deferred">
signoff:
  partner_signature: <typed name>
  partner_date: <UTC ISO8601>
  pm_b_review_signature: <typed name; left blank by partner; PM-B fills>
  pm_b_review_date: <UTC ISO8601; left blank by partner>
  product_review_signature: <typed name; left blank by partner; Product fills>
  product_review_date: <UTC ISO8601; left blank by partner>
verdict: <"pass" | "fail" | "pass-with-conditions">
verdict_notes: <free text — required if verdict != "pass">
---

# Design-Partner Signoff — <Partner Display Name>

(Free-form narrative section. Partner describes their experience in
prose: where the first product loop felt obvious, where it felt slow, what they would
change. Aim for ~200–600 words. The yaml frontmatter above is the
contract; this prose is the texture PM-B and Product use to triangulate
patterns across partners.)

## First product loop log

(Optional. Partners may include a chronological log of commands run +
output snippets, with secrets redacted by the partner. Treat this as
operator field notes, not as a formal test transcript.)
```

## Aggregation contract

- Storage path: `agile/release-evidence/v0.X.0-alpha/signoffs/<partner_slug>.md`.
- Index file: `agile/release-evidence/v0.X.0-alpha/signoffs/INDEX.md`,
  one row per signoff with the partner slug, verdict, partner date, and
  link to the signoff file.
- Roll-up: PM-B's release-evidence packet (per
  `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md` Section 6)
  references the signoff INDEX.md and reports
  `signoff_count_total / signoff_count_pass / signoff_count_pass_with_conditions /
   signoff_count_fail`.
- Triggers rejection of the release tag if any of:
  - Pass count below the floor for the tag (see "Decision" above).
  - Any signoff has `verdict: fail` and the failure cites a
    panel-locked exit criterion (e.g. `0.1-EC1`/`EC2`/`EC5`/`EC7`).
  - Any signoff has a `blockers_found` row with `severity: critical`
    and `status: open` at greenlight time.
  - For `v0.3.0-alpha`: zero partners completed the backup/restore
    drill (`backup_restore_drill: true`).

## Reviewer matrix

| Reviewer role | Verifies |
|---|---|
| **PM-B (release-risk governance)** | Count + completeness — every claim in the yaml frontmatter is filled; every partner with `verdict: fail` has a recorded release condition with a failure code; the floor count for the tag is satisfied; no critical-open greenlight conditions remain at release decision. |
| **Product** | Friction-points capture — the `friction_points` array is populated; partner narrative is present; "pass-with-conditions" verdicts have actionable remediation suggestions; cross-partner patterns are surfaced into the next sprint's planning input. |
| **PM-A (delivery coordination)** | Confirms that any blocker with `status: deferred` has an owner + due date + visible follow-up story in the next sprint backlog. |
| **Release Manager** | Confirms cross-link to `P0-D004` (release-key custody) — the first protected `alpha` release cannot tag until both the per-tag signoff floor (this doc) and `RELEASE_KEY_CUSTODY.md` operational checklist are satisfied. |

## Lifecycle

- **Submission window.** Signoffs are submitted in the 14-day window
  before the candidate tag is signed. Signoffs older than that window
  are **stale** and trigger a re-run (see below).
- **Stale signoffs (>90 days).** A signoff is automatically considered
  stale if the partner submitted it more than **90 days** before the
  release tag. Stale signoffs do not count toward the floor; PM-B asks
  the partner to re-verify against the current rc bundle.
- **Re-run trigger.** Any of the following invalidate prior signoffs
  for the affected tag and require a re-run:
  1. Signed installer materials change (the .deb is re-built from a
     newer source SHA).
  2. The release-key custody decision (P0-D004) is amended (e.g.,
     migration from option ii to option iii).
  3. Any panel-locked Phase 0 decision (P0-D001..P0-D022) is reopened
     and changed.
  4. A `severity: critical` blocker is fixed in a subsequent rc; the
     partners who reported it must re-confirm against the new rc.
- **Public surface.** Aggregated counts (not individual signoffs) are
  published in the release notes. Individual signoff files remain
  private to the design-partner program until the partner explicitly
  opts in to public attribution.

## Cross-references

- Pre-locked counts: `agile/PROGRAM_OVERVIEW.md` §3 ("Phase Map") and
  §6 ("Workstreams and Dependencies"); `agile/RELEASE_PLAN.md` §2.0
  ("Phase 0 release governance").
- First-protected-release gate: this doc's `lifecycle.re_run_trigger.2`
  cross-links `agile/handoff/governance/RELEASE_KEY_CUSTODY.md`
  (P0-D004) — both must be satisfied for the `v0.1.0-alpha` tag to
  ship.
- Greenlight integration: `agile/handoff/governance/
  V010_ALPHA_GREENLIGHT_BRIEF.md` Section 6 references the
  signoff INDEX.md as part of the PM-B release-evidence packet.

## PM-B / Product sign-off block

```yaml
design_partner_signoff_template_signoff:
  decision_id: P0-D022
  status: decided
  per_tag_floors:
    v0_1_0_alpha:
      install_signoffs: 2
      first_product_loop_signoffs: 0
      backup_restore_drill_required: false
    v0_2_0_alpha:
      install_signoffs: 0
      first_product_loop_signoffs: 3
      backup_restore_drill_required: false
    v0_3_0_alpha:
      install_signoffs: 0
      first_product_loop_signoffs: 5
      backup_restore_drill_required: true
  signers:
    pm_b:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    product:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
```

> Note: this sign-off block is a placeholder. Agent 4 (the Phase 0
> closure agent) cannot sign on PM-B / Product's behalf. The
> placeholder dates are filled by the named humans before
> `v0.1.0-alpha` (final) is tagged.
