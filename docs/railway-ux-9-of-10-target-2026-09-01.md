# Midnight 9/10 project-configuration target

Date: 2026-09-01

Status: Gates 9A–9E implemented, peer-reviewed, and installed in alpha-6;
independent human re-score still required before claiming 9/10

Related:

- `docs/archive/midnight-railway-ui-comparison-2026-07-27.md`
- `docs/archive/railway-gui-data-coverage-2026-08-31.md`
- `docs/archive/railway-parity-review-2026-07-27.md`

## Decision

Midnight's project model is already recognizably Railway-like. The independent
focus group scored it 8/10 for a Railway power user, 7/10 for a new user, and
8/10 for UX coherence. The concrete navigation defects found in that review
have since been fixed, but the product has not been re-scored and must not yet
claim 9/10.

The remaining gap is not missing data or a visual rebrand. It is interaction
economy. A user should be able to answer four questions without interpreting
control-plane language:

1. What is serving traffic now?
2. Is a newer rollout in progress or failing?
3. What configuration have I changed?
4. What will happen when I apply those changes?

The 9/10 target is a familiar environment workspace with an original Midnight
identity: compact, self-hosted, evidence-aware, and explicit about safety, but
with technical provenance disclosed only when requested.

## Implementation checkpoint

The September 1 implementation loop delivered Gates 9A through 9E:

- monotonic applied and deployed configuration revisions, per-service deploy
  impact, idempotent apply-and-deploy, tenant-scoped operation groups, and
  server-derived source-rebuild versus immutable-image-redeploy actions;
- one Changes flow with safe non-secret autosave, explicit sensitive-value
  confirmation, grouped review, apply-and-deploy as the primary action, and
  apply-only under Advanced;
- compact service cards and Overview language that distinguish the serving
  release from a newer rollout, plus evidence-driven setup resume;
- searchable Configure and Add Resource registries, remembered configuration
  sections, shared command vocabulary, and keyboard shortcuts;
- 320 CSS px, forced-colors, reduced-motion, focus, overflow, and automated
  accessibility coverage.

The automated gates pass, including the API contract, full Go suite, web unit
suite, production build, and browser suite. This is not a substitute for Gate
9F's independent human protocol, so the historical 7.7/10 result remains the
last measured score. The implementation is installed on the primary upgraded
VPS at commit `94df089`; installation alone is not a human usability score.

Two product boundaries remain explicit. A service without an active immutable
deployment cannot yet be cloned for grouped redeployment, so configuration is
applied and the group reports a partial intake. Group-level retry and
dependency-DAG ordering are also future work; current intake order is stable by
service identifier and never rolls back an already-applied revision.

## Research findings

Railway's current product documentation reinforces the following patterns:

- A project canvas is the primary environment workspace. Services are created
  in place and opened from service tiles.
- Service configuration lives with deployments, logs, and metrics rather than
  in a separate administration product.
- Changes are environment-scoped and collected into a staged-change banner.
- The normal staged-change outcome is review and deploy. Commit-without-deploy
  exists as a secondary keyboard-modified action.
- Railway currently documents networking changes as an immediate-apply
  exception. Midnight should keep networking and storage inside its universal
  reviewed draft instead of copying that inconsistency.
- Environment sync marks affected services as New, Edited, or Removed directly
  on the canvas.
- The command palette and keyboard shortcuts mirror visual navigation and
  service actions.
- Logs and metrics retain the active project, environment, service, and
  deployment context.

Primary references:

- [Railway services](https://docs.railway.com/services)
- [Railway staged changes](https://docs.railway.com/deployments/staged-changes)
- [Railway environments](https://docs.railway.com/environments)
- [Railway variables](https://docs.railway.com/variables)
- [Railway keyboard shortcuts](https://docs.railway.com/overview/keyboard-shortcuts)
- [Railway metrics](https://docs.railway.com/observability/metrics)
- [Railway logs](https://docs.railway.com/observability/logs)

The transferable lesson is not Railway's colors, card shapes, or exact labels.
It is that infrastructure, configuration, and deployment form one continuous
environment-scoped workflow.

## Current Midnight baseline

The implementation already provides:

- Grid by default with an optional topology Map;
- project and environment scope in the URL;
- an environment resource summary that distinguishes the active release from
  the newest rollout;
- a non-modal desktop service inspector and full-screen mobile route;
- Overview, Deployments, Logs, Metrics, and Configure sections;
- Source, Variables, Networking, Storage, and Runtime configuration;
- a project-level Add resource flow and contextual command palette;
- a durable, revisioned environment changeset;
- tenant-safe logs, metrics, source, variable, domain, runtime, and deployment
  reads;
- accessible keyboard navigation, responsive layouts, and explicit unavailable
  states.

The focus-group fixes completed after the first score include:

- Source and Networking recommendations now open the correct sections;
- project cards now land on Grid;
- configuration has a plain-language lifecycle cue;
- configuration subsections support arrow keys and a mobile selector;
- resource cards no longer lead with operation IDs and provenance;
- first-deploy guidance is service-scoped and failed work is never marked
  complete.

These corrections improve the baseline, but only another independent test can
establish a new score.

## Why the experience is not yet 9/10

### 1. The visible change lifecycle has one step too many

The current UI exposes browser-local edits, Save draft, Apply configuration,
and deployment as separate user decisions. Those distinctions are valid
internally, but a typical user wants to review changes and decide whether to
deploy them.

### 2. Changes and running operations share one tray

The Operations tray combines configuration drafts with active deployment work.
This makes a stable draft and a running rollout look like variations of the
same state even though they require different attention.

### 3. Settings still require scanning rather than finding

The five Configure sections are coherent, but a user looking for a health
check, replica count, root directory, or domain must already know which section
owns it. Railway's service-settings search is a useful interaction principle
here.

### 4. Service cards are accurate but still read like compact reports

The cards contain correct evidence, but status, source, release, rollout,
revision, health, and endpoint still compete. A scan surface should emphasize
the current operational story and defer everything else.

### 5. First-run guidance is visible but not yet a complete resume flow

The checklist is now service-scoped, but setup progress should live with the
selected service, show the next action, and resume an interrupted create/link/
deploy flow rather than merely describe it.

### 6. Product copy still leaks implementation language

Words such as canonical, durable, evidence, admitted, and operation are useful
in diagnostics and API documentation. They create unnecessary translation work
when used in primary actions, empty states, and ordinary service status.

## Target information architecture

The structure remains stable; the change is hierarchy, not navigation churn.

```text
Project / Environment
├── Resources                     Grid | Map
├── Deployments
├── Observability
├── Shared variables
└── Project settings

Selected service
├── Overview
├── Deployments
├── Logs
├── Metrics
└── Configure
    ├── Search settings
    ├── Source
    ├── Variables
    ├── Networking
    ├── Storage
    └── Runtime

Environment workspace overlays
├── Changes bar                   draft configuration only
├── Change review sheet           diff, validation, apply/deploy choice
└── Activity indicator            builds, deploys, restarts, restores
```

`Configure` stays distinct from Railway's `Settings` label because it describes
the action clearly and preserves Midnight's product vocabulary. Search and
deep links make it as discoverable as a settings page.

## Target workspace

```text
┌ Project name ─ Production · protected ───── Activity  Create ┐
│ Resources                                      Grid | Map    │
│                                                               │
│  ┌ API ─ healthy ┐  ┌ Worker ─ deploying ┐  ┌ Postgres ┐     │
│  │ main · a1b2c3 │  │ main · d4e5f6      │  │ ready    │     │
│  │ 2/2 ready     │  │ new rollout        │  │ volume   │     │
│  │ api.example ↗ │  │ View rollout       │  │ private  │     │
│  └───────────────┘  └─────────────────────┘  └──────────┘     │
│                                                               │
│  3 changes · Production          Review   Apply & deploy  ▾   │
└───────────────────────────────────────────────────────────────┘
```

Rules:

- The project, active environment, and protection state are always visible.
- Grid remains the landing view. Map is an intentional topology tool.
- A card communicates name, status, serving revision, exceptional rollout,
  health, reachability, and one next action.
- New, Edited, and Removed tags appear only while a draft affects that service.
- Running work appears in Activity, not in the Changes bar.
- The right inspector preserves the resource workspace on desktop.

## Target service card

The default card has three visual levels:

1. **Identity and health** — service name and one status pill.
2. **Serving state** — branch/image plus short revision, replicas, and public
   endpoint when one is verified.
3. **Exception or action** — newer rollout, failure, draft tag, or one contextual
   action.

The following remain in Overview or Technical details:

- full image references;
- operation IDs;
- truth-source names;
- private identity explanations;
- full timestamps;
- builder and configuration provenance.

Card behavior by state:

| State | Primary story | Action |
| --- | --- | --- |
| No source | Setup incomplete | Connect source |
| Ready, never deployed | Ready to launch | Deploy |
| Building/deploying | New rollout in progress | View rollout |
| Latest failed, active healthy | Current release still serving; newer rollout failed | Review failure |
| Active degraded | Service needs attention | View logs |
| Active healthy and public | Serving normally | Open live |
| Active healthy and private | Serving privately | Configure networking |
| Draft affects service | Edited | Review changes |

## Target service inspector

### Overview

The header answers the operational question in one sentence:

> `a1b2c3 is serving production on 2 healthy replicas.`

If a newer rollout exists, it becomes a separate callout:

> `d4e5f6 failed during deploy. The current release is still serving.`

Below that:

- Source and revision
- Health and replicas
- Public and private reachability
- One recommended action
- Collapsed Technical details

### Configure

Configure gains a filter input that searches a registry of setting labels,
descriptions, and aliases. Searching `health`, `replicas`, `root`, `domain`, or
`cron` jumps directly to the owning section and focuses the control.

Default section selection:

1. Source when the service has no source;
2. the last section used for this service and environment;
3. Variables otherwise.

The configuration workflow explanation remains available, but compresses to a
single line after the first successful change review.

### Setup and resume

Incomplete services show a setup card inside Overview:

```text
Finish setting up API
✓ Service created
✓ Source connected
○ Required configuration
○ First deployment
○ Healthy

Continue setup
```

The card is driven by one service state machine. `Continue setup` navigates to
the first incomplete actionable step. Failed work includes the failure and a
retry/resume action; it never becomes an unknown runtime status.

## One change workflow

### User-facing model

```text
Edit → Review changes → Apply & deploy
                         └ Apply only (advanced)
```

The backend may still retain input buffering, an encrypted revisioned draft,
validation, atomic apply, and separate deployments. Those are implementation
stages, not four primary actions.

### Draft persistence

- Non-secret edits auto-save to the environment draft after a short debounce.
- Secret input is submitted explicitly, immediately removed from browser state,
  and represented only as `value set` or `value replaced`.
- Each save uses changeset revision/CAS and displays `Saving…`, `Saved`, or a
  recoverable conflict.
- Closing or refreshing the page never silently loses an acknowledged draft.
- If the browser is offline or the server rejects the draft, the UI says the
  edit is not saved and keeps a local recovery buffer.

### Changes bar

Collapsed:

```text
3 changes · Production              Review   Apply & deploy  ▾
```

The overflow menu contains:

- Apply without deploying
- Add commit message
- Discard all changes

The primary action is disabled with a concrete reason when validation or
authority fails.

### Review sheet

The review sheet groups diffs by affected service and resource type. It shows:

- old and new non-secret values;
- secret metadata only;
- services that will redeploy;
- changes that apply immediately or do not require deployment;
- validation errors next to their owning control;
- per-change discard and Discard all;
- optional commit message.

Affected cards receive New, Edited, or Removed tags while the draft exists.

### Apply and deploy

`Apply & deploy` is one idempotent environment operation:

1. revalidate authority and resource versions;
2. apply the exact reviewed changeset revision;
3. snapshot affected services;
4. enqueue deployments in dependency order;
5. return an operation group rendered in Activity and on affected cards.

If apply succeeds but deployment intake partially fails, the operation reports
that exact split and offers `Retry failed deployments`. It never rolls back or
hides an already-applied configuration revision.

`Apply without deploying` remains available for maintenance windows, manual
coordination, and expert workflows.

## Activity and deployment feedback

Configuration drafts and running operations become separate components:

- **Changes bar** — only un-applied environment changes.
- **Activity** — builds, deploys, restarts, restores, domain verification, and
  other asynchronous work.

Activity summarizes `3 running`, opens an environment-scoped list, and links
each item back to its service or deployment. A completed apply-and-deploy group
shows per-service success or failure without losing the environment-level
batch context.

## Add resource experience

The Add resource palette becomes a compact searchable catalog:

- search first;
- recent choices at the top;
- categories for Source, Data, Storage, and Template;
- consistent icon, title, and one-line description;
- explicit `Project / Environment` target in the header;
- disabled capabilities remain visible but do not dominate available choices;
- keyboard selection and command-palette commands use the same registry.

The common path remains GitHub repository, container image, empty service,
database, template, or volume. Midnight does not copy Railway's catalog layout
or visual assets.

## Keyboard model

Existing `Cmd/Ctrl+K`, arrow navigation, Enter, and Escape behavior remains.
Add:

- `G` then `O` — service Overview
- `G` then `D` — Deployments
- `G` then `L` — Logs
- `G` then `M` — Metrics
- `G` then `C` — Configure
- `/` — focus Configure search or log filter in the relevant section
- `Cmd/Ctrl+Enter` — open change review when a draft exists

Shortcuts are hints, never the only way to reach an action. They are disabled
inside text-entry controls unless the control explicitly owns them.

## Copy system

Primary UI language:

| Avoid in primary UI | Use instead |
| --- | --- |
| canonical deployment | current release or deployment record |
| durable changeset | saved draft or changes |
| truth source | record source, under Technical details |
| admitted repository | connected repository |
| operation | activity, rollout, build, deploy, or restore |
| persisted replica evidence | last reported health |
| apply changeset | apply configuration |

Technical terms remain in API docs, audit views, diagnostics, and expandable
Technical details. Truth is preserved by hierarchy rather than jargon.

### Sweep outcome, 2026-09-04

A survey on 2026-09-04 counted 108 user-visible occurrences of implementation
vocabulary, 76 of them outside the in-app documentation pages. That sweep is
now complete:

- 78 user-visible strings were rewritten across the console and the CLI —
  every remaining occurrence of `durable`, `evidence`, `admitted`, `intake`,
  and `changeset` in primary UI, plus the `canonical` and `operation` uses that
  did not name an operation the reader was actually looking at.
- 6 thrown `Error` strings that leaked the idempotency-key invariant to a user
  as a red box now read "Something went wrong preparing that request. Please
  try again." The exact invariant survives in a `console.error` and in the
  error's `cause`, so debuggability is unchanged.
- 4 strings were deliberately kept and recorded in
  `tools/lint/product_copy_allowlist.txt`: `Operation ID` and
  `Legacy operation record` in the service Overview's Technical details block,
  and the `Operation` / `Operation state` rows in Deployment detail. In each
  case the reader is looking at a raw operation identifier returned by the API,
  which is exactly the disclosure this document reserves for Technical details.
- `web-next/src/pages/docs/**` stays technical. The in-app documentation
  teaches the control-plane model, so `operation`, `changeset`, and
  `idempotency` are its subject rather than leaked jargon.

The vocabulary cannot silently return. `go run ./tools/lint` now fails with
`LINT_PRODUCT_COPY_GUARD_FAILED` when a banned word appears in JSX text or in a
string literal in a rendered position under `web-next/src/`. Identifiers, URLs,
header names, query keys, comments, `console.*` arguments, and error `cause`
values are out of scope; `pages/docs/**` is excluded by path; and the allowlist
is exact, so a reworded string retires its own exemption. The rule for authors
is in `docs/contributing/style.md`.

## Responsive behavior

### Desktop

- Resource Grid and inspector remain visible together.
- Inspector width stays within 420–640px and may be resized.
- Change review opens above the workspace without destroying service context.

### Tablet

- Inspector takes approximately half to two-thirds of the viewport.
- Add resource and change review use full-height sheets.

### Mobile

- Service inspection is a full-screen route.
- Configure uses the existing labelled section selector.
- The Changes bar becomes a sticky two-row bottom action bar.
- Review is a full-screen route/sheet with a sticky primary action.
- No horizontal tab or action overflow is permitted at 320 CSS px.

## Accessibility contract

- One `h1` identifies the current project or service route.
- Resource cards are a keyboard-navigable collection with visible focus.
- Status is never color-only.
- Configure navigation supports arrows on desktop and a labelled select on
  mobile.
- Change rows expose action, resource, old/new meaning, validation, and discard
  controls to assistive technology.
- Saving, saved, applying, deploying, success, and failure are announced through
  appropriately scoped live regions without repeating continuously.
- Focus moves into review and returns to the initiating control.
- Reduced motion and forced colors preserve state distinctions.

## Required backend/read-model support

The design should not infer deployment necessity from timestamps. Add or expose:

- applied environment configuration revision;
- deployed configuration revision per service;
- affected service IDs and resource kinds on changeset validation;
- whether each change requires deployment;
- a tenant-safe, idempotent apply-and-deploy coordinator;
- operation-group status with per-service intake and deployment results;
- draft conflict details that identify the affected setting without exposing
  secret values.

Suggested product contract:

```text
POST /environments/{environment_id}/changeset/apply-and-deploy
If-Match: <changeset revision>
Idempotency-Key: <exact environment/revision intent>

202 Accepted
{
  "applied_revision": 12,
  "operation_group_id": "opg_...",
  "affected_services": ["svc_api", "svc_worker"]
}
```

The existing apply-only endpoint remains and is used by the advanced action.

## Implementation order

### Gate 9A — truthful change/deployment state

- Add applied/deployed revision fields and affected-service validation output.
- Add the idempotent apply-and-deploy coordinator and operation group.
- Prove authorization loss, revision conflicts, partial intake, replay, and
  recovery behavior with PostgreSQL integration tests.

Exit: the GUI can state whether deployment is required without inference.

### Gate 9B — one visible change journey

- Add autosaved encrypted drafts with visible saving/error states.
- Replace Save draft + Apply configuration as primary steps with Review and
  Apply & deploy.
- Retain Apply without deploying in the overflow menu.
- Split Changes from Activity.
- Add review grouping, affected-service preview, and service-card draft tags.

Exit: a user can edit a variable and deploy the result without understanding
the storage layers behind the draft.

### Gate 9C — operational hierarchy

- Finalize the compact three-level service card.
- Move remaining provenance and IDs under Technical details.
- Add the one-sentence Overview status and explicit active-versus-new-rollout
  callout.
- Move first-run progress into the selected service and make Continue setup
  resume the first incomplete step.

Exit: users correctly identify the serving release and failed candidate in
every test scenario.

### Gate 9D — findability and creation polish

- Add Configure search with a typed settings registry.
- Add last-used/default Configure-section behavior.
- Make Add resource searchable, categorized, keyboard complete, and explicit
  about project/environment scope.
- Add service navigation shortcuts and visible shortcut hints.

Exit: every focus-group configuration task begins without documentation or
trial-and-error section hopping.

### Gate 9E — language, responsive, and accessibility refinement

- Apply the primary-copy vocabulary across product surfaces.
- Preserve technical terms only in diagnostics, audit, docs, and details.
- Finish sticky mobile review/actions and 320px overflow coverage.
- Run screen-reader, keyboard-only, forced-colors, reduced-motion, 200% zoom,
  and automated accessibility checks.

Exit: no serious/critical automated violations and no blocking manual issue.

### Gate 9F — independent validation and iteration

- Run the focus-group protocol below against a fresh build.
- Fix all task blockers and repeat the affected tasks.
- Do not round an 8.x result up to 9 unless every segment threshold passes.

## Focus-group protocol

Recruit at least 12 participants:

- 4 current Railway users;
- 4 technically capable users new to both products;
- 2 self-hosting/platform operators;
- 2 keyboard or assistive-technology users.

Use a clean seeded project and ask each participant to:

1. enter a project and identify the active environment;
2. identify what release is serving while a newer rollout has failed;
3. add a second service from GitHub or an image;
4. change a variable and explain what will happen next;
5. review changes and deploy affected services;
6. apply another change without deploying;
7. expose a service publicly and find its private address;
8. diagnose a failed rollout using deployment history and logs;
9. switch environments without attributing one environment's state to another;
10. repeat one configuration task using only the keyboard.

Measure:

- task completion without moderator assistance;
- time on task and wrong turns;
- whether the participant correctly identifies serving versus newest release;
- whether the participant correctly predicts apply versus deploy behavior;
- confidence after each task on a 1–7 scale;
- Railway familiarity, new-user ease, and UX coherence on a 1–10 scale;
- SUS or an equivalent standardized usability score;
- accessibility blockers and recovery behavior.

## 9/10 acceptance threshold

The product earns the target only when all are true:

- mean overall focus-group rating is at least 8.8/10;
- median rating is at least 9/10;
- no participant segment averages below 8.5/10;
- at least 90% of core tasks complete without moderator help;
- 100% correctly identify the serving release in the failed-rollout scenario;
- at least 90% correctly predict whether an action applies only or applies and
  deploys;
- median post-task confidence is at least 6/7;
- SUS is at least 85;
- no serious or critical accessibility violation and no blocking manual issue;
- no test exposes a secret value, crosses tenant scope, or presents desired
  configuration as observed runtime state.

## Non-goals

- Copying Railway's visual design, wording, icons, or exact canvas behavior.
- Hiding Midnight's evidence model from diagnostics and advanced users.
- Claiming multi-host, multi-region, managed-HA, hosted-edge, or external DNS
  capabilities that Midnight does not provide.
- Solving the remaining tenant volume/backup/restore read seam by exposing
  installation recovery routes.
- Adding billing or Stripe.

## Recommended approval scope

Approve Gates 9A through 9F as one product loop, delivered in order. Gate 9A is
the enabling correctness work; Gate 9B is the largest expected score gain;
Gates 9C–9E remove the remaining scan, findability, language, and responsive
friction; Gate 9F is the only valid basis for claiming 9/10.
