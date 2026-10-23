# ADR-013: Templates Marketplace, Deploy-on-Midnight Button, and `midnightapp-templates` Org

- Status: Accepted (from Parity Review)
- Date: 2026-04-25
- Deciders: Parity Review (4 reviewers, all marked this as a top-3 must-fix)
- Tags: [v1, phase-2, ecosystem, dx]

## Context

Railway's `railwayapp-templates` GitHub organization plus the "Deploy on Railway" button is a top-of-funnel ecosystem driver — every template is a one-click acquisition path for Railway. Midnight's plan funded "5–10 templates" as content but never wired the marketplace UI, the template-review queue, the deploy-button URL scheme, or a separate GitHub organization. Without these, "1:1 with Railway" claims fail at the first acquisition moment a user encounters a template repo on GitHub.

Spec references after the parity review:

- `Parity Review Locked Decisions` → "Templates ecosystem" section.
- Original `Template and plugin governance` retained.

## Decision

1. A separate GitHub organization **`midnightapp-templates`** hosts official templates (analogous to `railwayapp-templates`). Each template repository ships:
   - `template.yaml` declaring services, variables (with parameter binding), volumes, domains, and supported engine versions.
   - SBOM-reviewed pinned image set.
   - License and security-review status field.
   - `README.md` with screenshots and a "Deploy on Midnight" button.

2. **Deploy-on-Midnight button URL scheme**: `https://<dashboard>/new/template?source=<repo-url>&ref=<sha>&workspace=<id>`. The dashboard renders an instantiation flow with parameter binding, displays the template's review status and SBOM summary, and creates the resources atomically via `applyChangeSet`.

3. **Template marketplace UI** lands in Phase 2 with: search by tag, filter by maintenance status, official-vs-community distinction with clear trust badges, review queue for community submissions, and a template-detail page that renders the README, services graph, and last-updated timestamp.

4. **Template-best-practices repository** (`midnightapp/template-best-practices`) ships in v1 documenting:
   - Image pin policy (digest, not tag).
   - SBOM/provenance generation requirements.
   - Healthcheck/readiness contract.
   - Required minimum CPU/memory.
   - Secrets handling.
   - Backup policy for stateful templates.
   - Versioning (semver of the template, separate from app versions).

5. Templates ship under three trust classes (already in spec): official, community, local. The new ADR pins the **official-template review checklist**: pinned image digests, SBOM passes vulnerability gate, healthcheck reachable, backup policy declared (if stateful), CLA + license compatible.

## Alternatives Considered

- **Use the main Midnight repo for templates.** Mixes platform code with template content; loses the discoverability of a separate org; copy of Railway model is intentional.
- **No marketplace UI; rely on a curated docs page.** Fails parity test; users expect search and one-click instantiation.
- **Allow arbitrary URLs as templates without the `template.yaml` contract.** Removes safety review and parameter binding; opens the platform to template-driven supply-chain attacks.
- **Adopt Coolify/Dokku template formats.** Ecosystem fragmentation; Railway's mental model is closer to what users coming to Midnight expect.

## Consequences

**Positive**: removes a blocker for honest 1:1 Railway parity claims; provides community contribution path; aligns acquisition flow with industry expectation; sets up a Phase 4 marketplace-monetization decision (still NON-GOAL for OSS but unblocks hosted track).

**Negative**: maintaining a second GitHub org adds operational overhead; template review queue requires moderator capacity; deploy-button URL becomes a public API contract that cannot churn.

**Follow-ups**:
- v1: ship `midnightapp-templates` org with at least Postgres, MySQL, Redis, static-site, Node.js, Go, Python templates after each passes the panel-locked support gates.
- v1: ship `midnight add <template>` CLI verb (also covered in CLI parity).
- Phase 2: ship the marketplace UI sprint.
- Phase 2: ship community-submission review workflow.
- Document the deploy-button URL scheme in `docs/`.

## Compliance / Verification

- CI gate `gate.template-button-url-stable` runs `oasdiff` against the deploy-button URL contract; any breaking change requires a major version bump documented in `docs/breaking-changes.md`.
- Test `TestTemplateInstantiationAtomicity` verifies that a partial failure during template instantiation rolls back via `applyChangeSet`.
- Sprint 15 includes story `TEMPLATE-MARKETPLACE-UI` that demos the dashboard search + instantiation flow.
- Code-review checklist: any template change in `midnightapp-templates` must pass the published checklist (pinned digests, SBOM, healthcheck reachable, backup policy if stateful).

## References

- Spec: `Parity Review Locked Decisions` → Templates ecosystem
- Sprint: `agile/sprints/SPRINT_15_PR_PREVIEWS_NOTIFICATIONS.md` (template UI delegated)
- Feature dossier: none yet (template marketplace is light enough to live in the sprint)
- Reviewer source: `agile/parity-review/PARITY_01_PRODUCT_SURFACE.md` (gap rated top-3 must-fix), `PARITY_04_ECOSYSTEM_DX.md`
