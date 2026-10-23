# Midnight vs Railway: interface comparison and original product direction

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Date: 2026-07-27

Status: implementation-ready product and interaction proposal

Related:

- `docs/archive/railway-parity-review-2026-07-27.md`
- `docs/archive/railway-parity-delivery-plan-2026-07-27.md`
- `docs/archive/railway-gui-data-coverage-2026-08-31.md` (current GUI coverage)
- `docs/railway-ux-9-of-10-target-2026-09-01.md` (current target design)

## Decision

Midnight does not need a Railway skin. It needs the same degree of workflow
coherence.

The current interface has a credible dark visual system and several good
foundations: URL-addressable service tabs, an environment selector, a
draggable topology canvas, a global command palette, a local variable-draft
preview, and an embedded agent. The problem is that these pieces do not yet
form one obvious journey.

The practical assessment from browser testing is approximately **7/10 visual
foundation, 3/10 product journey**. This is encouraging: Midnight needs
workflow consolidation and spatial redesign more than a wholesale visual
rebrand.

The next GUI loop should focus on a single operator promise:

> Choose code, watch it become healthy, expose it intentionally, and always
> know what Midnight is doing.

The recommended direction is an original **operations workspace**:

- Midnight remains dark, compact, local-first, and operator-oriented, using
  its existing crescent/horizon blue brand while reserving green for health.
- A dense, deterministic resource grid becomes the default project view.
- The current canvas becomes an optional topology Map for projects whose
  relationships benefit from spatial layout.
- Service details become a non-modal split inspector that preserves spatial
  context.
- Production is selected when a project is created; "All environments" is a
  clearly read-only comparison view.
- Project and service creation use one source picker and one backend journey.
- Deployment progress is visible on the service node, in the inspector, and in
  the project activity stream from the same deployment record.

## Evidence and limits

This comparison is based on:

- live desktop and 390x844 browser testing of Midnight in memory mode
- direct inspection of the current `web-next` route and component code
- Railway's current official documentation and July 2026 changelog
- Railway's official March 2026 project UI demonstration

The available browser session was signed out of Railway during the first
comparison pass. No account-level mutation was attempted. Authenticated
Railway journey verification should be added when a signed-in read-only session
is available; it must not silently be replaced by guesses.

## What Railway is actually teaching us

The useful lessons are interaction principles, not colors or exact component
shapes.

### 1. Infrastructure is the primary object

Railway gives the project environment and its services the majority of the
screen. Global navigation is persistent but visually secondary. The operator
does not have to leave the infrastructure context to inspect a service.

### 2. Creation is available in place

New projects are reachable globally. New services, templates, databases, and
empty resources are reachable from the project canvas and command palette.
The same concepts recur across the dashboard, canvas, templates, and CLI.

### 3. Environment scope is structural

Canvas positions and groups are environment-scoped. Environment state is not
just a filter applied after the fact; it determines the topology being edited
and deployed.

### 4. Configuration changes are a reviewable batch

Changes can be staged, reviewed, applied together, undone, or committed without
deploying. This turns a collection of settings forms into a coherent change
workflow.

### 5. The canvas is both map and status surface

Service nodes communicate what exists, what is healthy, what changed, and what
needs attention. The canvas can also surface real relationships and network
flows without forcing the user to assemble the picture from separate pages.

### 6. Detail views keep project context

Opening a service retains the surrounding project. The service view is a
focused working state within the project, not an unrelated destination.

### 7. Keyboard and command surfaces mirror the visual model

The command palette is not just navigation. It exposes creation and contextual
actions. Context-specific shortcuts have a predictable grammar.

## What Midnight already gets right

### Visual foundation

- The dark neutral palette is restrained and recognizable.
- Green is used as an identity accent rather than coating every surface.
- Typography is compact enough for an operational console.
- Borders and surfaces establish hierarchy without excessive decoration.
- Focus styles, reduced-motion support, labels, and semantic roles are present.

### Product foundations

- `/new` has a clean four-choice source picker.
- Project, environment, selected service, and service tab can be represented in
  the URL.
- The project canvas supports dragging and actual relationship edges.
- Service status is normalized through a shared status mapper.
- The inspector already groups deployments, logs, variables, metrics, domains,
  and settings.
- The agent is mounted outside route transitions, so a conversation can survive
  navigation.
- Variable edits have a shared project-level local-draft preview. This is a
  useful interaction prototype, not yet a durable environment changeset.

These are worth preserving. The redesign should be a consolidation and
recomposition, not a rewrite of every component.

## Where Midnight currently breaks the journey

### P0: two project-creation experiences

The projects index opens an inline `CreateProjectPanel`, while `/new` contains a
better source picker with GitHub, template, empty, and database entry points.
The primary "New project" action currently sends users to the less capable
flow.

Impact:

- the most polished creation experience is effectively hidden
- behavior depends on where creation started
- GitHub setup, naming, service creation, and templates have duplicate logic

Decision:

- every "New project" action routes to `/new`
- delete the index-owned creation state after API behavior is consolidated
- the source picker becomes a reusable `SourcePicker` for project and service
  creation

### P0: "All environments" is the default editing scope

New projects have a Production environment, but the top navigation selects
"All environments." Clicking "Add service" then opens a form whose deploy
button is disabled and whose corrective instruction is below the image field.

Impact:

- the first meaningful action becomes a dead end
- the user has to understand Midnight's environment model before deploying
  anything
- aggregate and editable states look equivalent

Decision:

- navigate to the newly created Production environment
- remember the most recently selected environment per project
- make omitted `env` resolve to the saved/default Production environment
- make comparison an explicit `view=compare` state or separate route
- hide or disable mutating actions at the page level in comparison mode, with
  one clear "Choose an environment to make changes" action
- reject every mutation without a concrete environment ID at the API boundary

### P0: no visible golden path

The creation flow ends at a canvas, but the interface does not consistently
guide the operator through source, configuration, deploy, health, domain, and
open-app stages.

Decision:

Each service has a small progress checklist until its first successful launch:

1. Source connected
2. Required variables resolved
3. Build complete
4. Service healthy
5. Public domain generated, optional

The checklist lives in the inspector and is summarized on the node. It
disappears into the normal deployment timeline when complete.

### P0: creating and deploying a service is not transactional

The service form currently creates the service and then requests a deployment.
When the deploy queue is unavailable, the service remains on the canvas with an
`unknown` status and no deployment. The user is left with neither a completed
launch nor an obvious way to resume it.

Decision:

- provide one server-side create/link/deploy operation with idempotency
- if atomic creation is not possible, persist an explicit Draft state
- Draft services show "Resume setup," the completed steps, and the failed step
- never collapse a control-plane setup failure into an `unknown` runtime state

### P0: adding a second service is undiscoverable

The visible "Add service" action exists only in the zero-service empty state.
Once one service is present, the populated canvas, project navigation, node
menu, and command palette have no creation action. The underlying query-string
route still exists but is not discoverable.

Decision:

- a persistent project-level `Create` action is available in every project
  state
- `Create` opens the shared source picker for service, database, template,
  volume, or empty resource
- the same action is present in the command palette

### P0: the service canvas wastes the space it claims

With one service, Midnight places a 240px card near the top-left of a very large
stage. There are no visible fit, zoom, reset-layout, or auto-arrange controls.
The service node prioritizes slug, project ID, and "updated" time over source,
deployment, route, and health information.

Decision:

- auto-fit services on load
- center layouts of one to four nodes in the visible viewport
- add fit, zoom, auto-layout, and view-mode controls
- persist layout server-side by project and environment
- use a compact list view below the canvas breakpoint and as an operator choice
- replace low-value node metadata with:
  - source and branch/image
  - deployment state and age
  - replica health
  - public/private reachability
  - one contextual next action

### P0: observability scope contradicts its heading

The project observability page presents itself as environment-scoped while some
summary and health content is fleet-wide. A user can reasonably attribute
another project's health or host data to the selected project.

Decision:

- every panel declares and enforces one scope
- project/environment views never silently include fleet data
- fleet panels move to instance Activity/Hosts or carry an unmissable
  instance-wide label
- API queries, titles, empty states, and filters share the same scope contract

### P0: opening details disables the workspace

The current Radix Dialog overlay dims and blurs the canvas. The panel describes
itself as an overlay but behaves as a modal. On mobile it leaves a narrow,
darkened strip of canvas behind, while the six-tab row overflows.

Decision:

- desktop: use a non-modal, resizable split inspector, 420–640px
- re-fit the topology within the remaining canvas width
- allow `Escape` to close and preserve the URL-backed selection
- mobile: navigate to a full-screen service route with a sticky header
- replace the overflowing mobile tab strip with a "Service section" selector or
  two-row compact navigation

### P0: capability truth is too far from the action

GitHub can remain on "Loading installations…" when the integration is not
configured. Logs, metrics, domains, and settings can be honest once opened, but
the node and creation surfaces do not summarize capability readiness.

Decision:

- one centralized `CapabilityGate` maps server capabilities to:
  `available`, `setup-required`, `planned`, and `unavailable`
- setup-required states provide one operator action and one diagnostic detail
- no infinite spinner survives a request timeout or unsupported response
- unavailable capabilities are visible but never presented as a working action

### P0: staged changes are local plaintext drafts, not a changeset

The existing staged-change store is in-memory, variables-only, and lost on
reload. It does not provide delete semantics or a durable environment patch.
The preview can also render a newly entered plaintext value for a secret
variable.

Decision:

- label the existing surface `Local draft` or `Unsaved changes`
- never display a secret value again after entry; diffs say only `value set`,
  `value replaced`, or `value removed`
- do not call the surface `Deploy Changes` until a durable backend changeset
  exists
- add a canonical environment-changeset API with revision/CAS, validation,
  review, discard, audit, and apply semantics
- include networking and storage changes in the canonical model rather than
  preserving a variables-only exception

### P1: project navigation competes with the canvas

Midnight currently uses a global icon rail, a top breadcrumb/switcher bar, and a
second horizontal project navigation row. The result is understandable on
desktop but cramped on mobile and less spatially stable than a project-scoped
sidebar.

Decision:

- retain the global 56px Midnight rail
- keep the compact project navigation during the first redesign slice
- establish a hard rule: Service Inspector or Agent may occupy the right
  utility pane, never both
- defer an expandable project sidebar until the Grid and inspector work
- permit the expanded project sidebar only at 1536px and above; collapse it
  automatically when a right utility pane opens
- reserve the top bar for project name, environment, activity, create, and
  agent entry
- on mobile, use the bottom global rail plus a project-section sheet

### P1: entity naming exposes implementation details

The index says both Projects and Workspaces, while project breadcrumbs show a
raw project ID and the page repeats the display name below it.

Decision:

- call the collection `Projects`
- reserve `instance` or `workspace` for actual global/tenant scope
- use display names in navigation and keep IDs in copy/details affordances
- never make internal IDs the dominant visual label

### P1: command palette is navigation-heavy

Midnight's palette supports project/service jumps, deploy/restart when a
deployment exists, logs, and agent entry. It does not create a project,
service, database, template deployment, environment, or domain.

Decision:

- add `New project`
- add project-scoped `New service`
- nest source choices under creation
- add `Generate domain`, `Open public URL`, and `Copy private address`
- add service-section jumps
- show disabled commands with a concrete reason
- keep one backend action registry shared by buttons, palette, and agent tools

### P1: empty states report absence but do not advance work

The deployments page can offer "Clear filters" even when default filters are
active. A service with no deployments offers Refresh, although refresh cannot
create the missing deployment.

Decision:

- empty-state actions are selected from the resource state machine
- no source: `Connect source`
- draft configuration: `Resume setup`
- ready but never deployed: `Deploy`
- failed: `View failure` and `Redeploy`
- active but private: `Expose publicly`, optional
- filters actually exclude results: `Clear filters`

## Direct comparison

| Area | Railway principle | Midnight today | Recommended Midnight direction |
| --- | --- | --- | --- |
| First run | One recognizable create entry and source choice | Primary button opens a second inline form; better `/new` route is hidden | Route all creation through one source picker |
| Environment | Active editing context | Aggregate "All environments" selected by default | Production by default; aggregate is explicitly read-only |
| Canvas | Environment workspace and status map | Large static field with a corner-pinned node | Dense Grid by default; optional Map with fit, center, and auto-layout |
| Service node | Current operational story | Slug, project ID, and update age dominate | Source, deploy, health, reachability, and next action |
| Service detail | Keeps project context | Modal overlay dims and blocks the canvas | Non-modal split inspector; full-screen mobile route |
| Creation | Available globally and in context | Project and service source flows diverge | Shared `SourcePicker` and action registry |
| Changes | Staged, reviewable environment patch | Variable staging exists but is not the universal edit model | One bottom change tray for all service/environment edits |
| Deploy feedback | Visible in node, service, and activity | Empty panels until backend state appears | One canonical timeline rendered consistently |
| Public access | Explicit domain action after deploy | Domain tab is discoverable only after opening a service | Readiness-driven "Generate domain" next action |
| Commands | Contextual creation and operations | Mostly jumps plus redeploy/restart/logs | Mirror the entire common visual action model |
| Mobile | Dedicated mobile interaction choices | Desktop canvas and drawer compressed into 390px | Compact Grid and full-screen service detail |
| Capability state | Actionable failure and loading feedback | Some indefinite or distant setup states | Central capability gate and bounded async states |

## Proposed information architecture

```text
Midnight instance
├── Projects
├── Activity
├── Fleet
├── Registry
└── Instance settings

Project
├── Environment switcher
├── Services
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
    ├── Source
    ├── Variables
    ├── Networking
    ├── Health & runtime
    └── Volumes
```

Notes:

- "Canvas" becomes "Services" in primary navigation. `Grid | Map` is an in-page
  view choice rather than the project itself.
- Domains and private addresses live together under "Networking."
- The service Overview is new. It answers status, source, active deployment,
  health, replicas, endpoints, and the next recommended action without making
  the user hunt across seven tabs.
- Project activity and deployments remain distinct: activity includes
  configuration, domain, environment, backup, and operator events.

## Unified creation journey

Project and resource creation use the same four-stage grammar:

```text
Source → Target → Configure → Review & Deploy
```

1. **Source** — GitHub, local source, image, template, database, or empty
2. **Target** — new/existing project, environment, and service identity
3. **Configure** — detected build settings and required variables only
4. **Review & Deploy** — materialized changes, exposure choice, and one
   idempotent operation

Advanced settings stay available without becoming mandatory. The review screen
must distinguish "Create draft" from "Deploy now" if both are supported.

## Original spatial model

### Global rail

Keep Midnight's narrow dark rail and crescent/horizon identity. Do not
reproduce Railway's icons, spacing, logo treatment, light canvas, or exact
sidebar.

### Project sidebar, deferred

At very wide sizes, an expandable project sidebar can establish stable context:

```text
UX Comparison
Production ▾

Resources
Deployments
Observability
Variables
Settings

Resources
  web       healthy
  worker    building
  postgres  healthy
```

This can give Midnight an operator-console character and make service selection
possible without precise Map interaction. It is not part of the first slice:
the current compact project header/navigation remains until Grid and the
inspector establish their actual width requirements.

### Resource workspace

The resource workspace has two explicit modes:

1. **Grid** — deterministic CSS auto-fit cards, best for one to eight resources
2. **Map** — draggable topology with verified edges, grouping, zoom, and fit

The default is Grid. Map is useful only when a project has relationships worth
showing. This avoids pretending every single-service project needs an infinite
diagram. The selected mode persists in the URL or user preference and never
changes implicitly based on resource count. Below 768px, Grid uses a compact
list presentation; it is not a third product mode. Map becomes read-only or
unavailable. Map layout persists server-side per environment.

### Service card

```text
┌ web                                      ● healthy ┐
│ GitHub · main · 42fd91a                            │
│ Active 3m ago · 1/1 replicas                       │
│ private                                            │
│                                      Generate URL →│
└─────────────────────────────────────────────────────┘
```

Rules:

- one primary status, not multiple competing pills
- show the source revision that produced the active deployment
- show current/desired replicas
- show public/private state
- show at most one next action
- expose deeper actions through the inspector and palette

### Split inspector

The inspector does not add an overlay. It shares the project workspace:

```text
┌ global rail ┬ resource workspace ────────┬ utility pane ────┐
│             │ Grid or Map reflows here   │ web · healthy    │
│             │                            │ Overview Deploys │
│             │                            │ Logs Metrics …   │
└─────────────┴────────────────────────────┴──────────────────┘
```

The selected card/node stays highlighted. Opening and closing the inspector
must not change the resource origin unpredictably.

Use nested service routes:

```text
/projects/:projectId/services/:serviceId/:section?env=:environmentId
```

The same route renders as an `<aside>` on desktop and a full service page on
mobile. Start with a fixed-width inspector; defer an accessible resizer.
Pointer activation preserves card focus, while keyboard activation may move
focus to the inspector heading.

### Operations tray

Midnight uses one bottom Operations Tray for active work and changes. This is a
distinctive alternative to stacking activity, agent, service, and change
panels around the canvas.

- closed when no operation or draft exists
- compact state: `2 operations · 3 local changes`
- expanded sections: Running and Changes
- Running shows authoritative phase trails and elapsed time
- Changes says Local Draft until a durable changeset exists
- mobile renders the tray as a sheet above bottom navigation

### Deployment feedback

While deploying, the node summarizes the current stage:

```text
web · deploying
Source ✓  Build 03:12  Start ·  Health ·  Route ·
View build output →
```

The inspector shows the full ordered timeline. The project activity feed shows
the same deployment and operation IDs. A failed step provides the failure
reason, affected configuration, logs, and retry/redeploy action in one place.
Do not show a determinate progress bar or percentage unless the backend
supplies an authoritative value. Use completed/current/upcoming/skipped phases
and elapsed time.

### Variables and changes

Variables use dense spreadsheet-like rows rather than a collection of cards:

- key
- masked or sealed value state
- service/shared scope
- reference source
- pending-change indicator
- row actions

Bulk `.env` paste and reference selection use the same model. Until a durable
changeset API exists, edits are labeled Local Draft and save through the
currently supported contract. Once the canonical changeset lands, every edit,
including networking, joins the environment patch. Review groups changes by
service/resource, shows safe current/proposed values, permits individual
discard, and explains which services will redeploy. Secret diffs never render
plaintext.

## Visual direction

Borrow:

- disciplined space allocation
- quiet chrome around a high-information workspace
- environment as a first-class context
- strong spatial continuity between canvas and service
- dense but legible operational metadata

Do not borrow:

- Railway purple
- train language or illustrations
- exact service-card proportions
- the exact light dotted canvas
- Railway's icon family
- exact sidebar, panel, command names, or keyboard bindings

Midnight-specific choices:

- deep graphite/blue-black surfaces
- use the existing crescent/horizon brand mark from `docs/brand/logo.svg`
  instead of the placeholder `M`
- replace emoji and ad hoc glyphs with one original icon wrapper/set
- separate `--brand`, `--selection`, and `--interactive` from
  `--status-success`, `--status-working`, and `--change`
- use horizon blue for brand/selection and reserve green for success
- amber for operator attention, blue for active work, red for failure
- slightly denser cards and stronger monospace source/deployment details
- remove the global glow and decorative grid from Grid mode
- reserve subtle coordinates/dots for Map mode only
- no decorative animation unless it represents real state

## Responsive behavior

### 1536px and above

- optional expanded project context; compact navigation remains valid
- Grid or Map resources
- split service inspector
- one right utility pane: service inspector or agent

### 900–1535px

- compact project navigation
- Grid resources
- fixed/capped service inspector shares the workspace
- agent is an overlay and cannot be pinned simultaneously

Use container queries for cards because the actual resource width changes with
the utility pane. Use a responsive gutter such as `clamp(12px, 2vw, 24px)`.

### Below 900px

- project header with environment selector
- Grid uses its compact list presentation
- service detail is a full-screen route
- service sections use a compact selector
- Operations Tray uses a bottom sheet
- no freeform node dragging

### Below 768px

- global bottom navigation
- single-column compact cards
- visible labels in bottom navigation
- environment selection uses a sheet
- Operations Tray becomes a bottom sheet
- all touch targets are at least 44px

## Accessibility contract

- All primary journeys work without pointer dragging.
- Map positions are never the only representation of resources.
- Grid preserves logical DOM order.
- Grid uses a list of semantic articles with one named open control and a
  separate actions menu; do not reuse the Map node's focusable `role=group`.
- Status uses text and iconography in addition to color.
- Every async state announces progress and completion.
- High-level phase transitions use `aria-live="polite"`; streaming log lines do
  not.
- Every destructive or public-exposure action states its scope.
- Inspector focus moves to its heading and returns to the selected service.
- Keyboard shortcuts are discoverable and remappable when they expand.
- Icon-rail labels use accessible tooltips instead of HTML `title`.
- Critical operational metadata is at least 12px.
- Test at 200% zoom, reduced motion, high contrast, and 390px width.
- Run accessibility and keyboard checks at every implementation gate, not only
  at final polish.

## Implementation sequence

### UI Gate 0: consolidate truth and routes

1. Route every project-create entry to `/new`.
2. Extract one reusable `SourcePicker`.
3. Resolve omitted environment scope to saved/default Production.
4. Give compare mode an explicit read-only route/search state.
5. Add bounded loading, setup-required, unavailable, and error states.
6. Parse and sanitize API errors before rendering.
7. Rename the existing staged surface Local Draft and mask secret diffs.
8. Enforce the scope declared by each observability widget.

Likely files:

- `web-next/src/pages/ProjectsIndex.tsx`
- `web-next/src/pages/NewProject.tsx`
- `web-next/src/components/panels/AddServiceForm.tsx`
- `web-next/src/components/panels/github/RepoPicker.tsx`
- `web-next/src/routes/routes.tsx`
- `web-next/src/api/client.ts`
- `web-next/src/lib/env-scope.ts`
- `web-next/src/lib/stagedChanges.ts`
- `web-next/src/components/StagedChangesBanner.tsx`
- `web-next/src/pages/ProjectObservability.tsx`

Exit:

- the visible new-project path is unique
- every mutation has a concrete environment
- aggregate views cannot mutate
- no secret value appears in a diff or rendered error
- async capability states terminate truthfully

Run axe and keyboard checks for the changed journeys before advancing.

### UI Gate 1: Grid workspace and summary read model

1. Keep a project-level `Create` action visible in every resource state.
2. Make Grid the deterministic default.
3. Use semantic resource cards and compact them into a list presentation on
   narrow screens.
4. Replace per-card latest-deployment queries with one batched
   environment-resource summary endpoint.
5. Populate card source, revision, deployment, replicas, health, endpoint, and
   next-action data only from that read model.
6. Keep Map available behind its current behavior until Gate 4.

The summary contract should resemble:

```text
EnvironmentResourceSummary
  service
  source_type, source_revision
  current_deployment
  desired_replicas, ready_replicas
  health
  private_endpoints, public_endpoints, tls_state
  capability_actions
```

This contract is required. The current `Service` and `DeploymentResource`
types do not contain enough data for the proposed card, while `ServiceNode`
performs an N+1 latest-deployment query.

Likely files:

- `web-next/src/pages/ProjectCanvas.tsx`
- `web-next/src/components/ServiceNode.tsx`
- new `web-next/src/components/ResourceGrid.tsx`
- new `web-next/src/components/ResourceCard.tsx`
- `web-next/src/api/client.ts`
- `web-next/src/api/hooks.ts`
- the API resource/read-model handlers and OpenAPI contract

Exit:

- one through eight resources render without N+1 requests
- primary status and next action match the API read model
- adding a second resource remains visible
- Grid works with keyboard and at 390px/200% zoom

### UI Gate 2: split inspector and service overview

1. Add nested service routes that drive desktop and mobile rendering.
2. Replace the modal drawer with a fixed-width, non-modal split inspector.
3. Reflow Grid within the remaining width.
4. Render the same route as a full service page on mobile.
5. Consolidate sections to Overview, Deployments, Logs, Metrics, and Configure.
6. Add a truthful Overview from summary/canonical deployment data.
7. Enforce one right utility pane: Service Inspector or Agent.
8. Preserve URL, focus, and back-button behavior.

Likely files:

- `web-next/src/routes/routes.tsx`
- `web-next/src/components/AppShell.tsx`
- `web-next/src/components/Drawer.tsx`
- `web-next/src/components/ServiceDetailDrawer.tsx`
- `web-next/src/components/Tabs.tsx`
- `web-next/src/components/AgentPanel/AgentDock.tsx`
- new `web-next/src/components/panels/ServiceOverview.tsx`
- `web-next/src/styles/tokens.css`
- `web-next/src/styles/globals.css`

Exit:

- desktop inspection never dims or blocks the resource workspace
- mobile service detail has no hidden tab overflow
- pointer and keyboard focus behavior pass browser tests
- inspector and agent never overlap or reduce content below supported width

### UI Gate 3: canonical operations and changes

1. Make create/link/deploy one idempotent operation or persist resumable Draft
   state.
2. Render a discrete release trail from the canonical deployment record.
3. Add the first-deploy progress checklist.
4. Open logs at the active/failing phase.
5. Add a backend environment-changeset API.
6. Replace Local Draft with the canonical review/apply tray only after that API
   exists.
7. Include variables, networking, runtime, and storage in the same changeset.

Likely files:

- `web-next/src/components/panels/AddServiceForm.tsx`
- `web-next/src/components/panels/DeploymentList.tsx`
- `web-next/src/components/panels/DeploymentDetail.tsx`
- `web-next/src/components/StagedChangesBanner.tsx`
- `web-next/src/lib/stagedChanges.ts`
- canonical deployment, operation, and changeset API/storage packages

This gate depends on canonical deployment and configuration work in the main
Railway parity plan. The GUI must not synthesize success, phase percentages,
or transactionality.

Exit:

- an enqueue failure resumes the same Draft without duplication
- phases are completed/current/upcoming/skipped with real timestamps
- secret changes reveal no values
- compare/apply/retry behavior survives refresh and concurrent edits

### UI Gate 4: optional Map

1. Make Map an explicit Grid/Map choice.
2. Add fit, zoom, center, and auto-layout controls.
3. Render only verified relationships.
4. Persist Map layout server-side per project/environment.
5. Re-fit Map when the inspector opens without changing saved coordinates.
6. Keep Map read-only or unavailable on mobile.

Likely files:

- `web-next/src/components/Canvas.tsx`
- `web-next/src/components/ServiceNode.tsx`
- `web-next/src/lib/canvasPositions.ts`
- `web-next/src/lib/serviceKind.ts`
- new `web-next/src/components/TopologyToolbar.tsx`
- new layout API/store

Exit:

- Grid is never spatially coupled to Map
- layout survives users, browsers, and environment switches
- no edge exists without durable relationship evidence

### UI Gate 5: one action model

1. Create a shared action registry.
2. Drive buttons, context menus, command palette, and agent tools from it.
3. Add creation, environment, networking, and service-section commands.
4. Provide clear disabled reasons and capability setup actions.
5. Add shortcut discovery.
6. Share stable action IDs and API semantics with CLI and agent tools; do not
   require Go and TypeScript to share literal registry code.

Likely files:

- `web-next/src/components/CommandPalette.tsx`
- `web-next/src/components/ServiceNode.tsx`
- `web-next/src/api/deployActions.ts`
- new `web-next/src/lib/actionRegistry.ts`
- agent tool presentation components

### UI Gate 6: visual-system hardening

1. Split brand, selection, interaction, status, and change tokens.
2. Use the crescent/horizon brand asset instead of the placeholder `M`.
3. Remove emoji from primary source identity in favor of an original icon set.
4. Remove Grid-mode decorative glow/dots.
5. Add loading/error/empty-state stories for every capability.
6. Complete responsive visual regression coverage.

Accessibility is not deferred to this gate. Every earlier gate already carries
axe, focus, keyboard, reduced-motion, zoom, and responsive acceptance.

The current `web-next` suite is Vitest/jsdom-only. Add the real browser suite in
Gate 0 and expand it throughout:

- `web-next/playwright.config.ts`
- `web-next/e2e/fixtures.ts`
- `web-next/e2e/golden-path.spec.ts`
- `web-next/e2e/responsive.spec.ts`
- `web-next/e2e/accessibility.spec.ts`
- `web-next/e2e/capability-states.spec.ts`

Use deterministic seeded API data for layout assertions and a separate
integration project for real deploy-state transitions. Accessibility checks do
not replace keyboard-journey assertions.

## Browser test matrix

Across the gates, run the following journeys at 1440x900, 1024x768, 768x1024,
and 390x844. Applicable journeys from prior gates remain required:

1. Empty instance → create project → choose source
2. GitHub source missing integration → configure or recover
3. Create empty project → add service → configure source
4. First deploy → build output → readiness → active
5. Failed deploy → reason → logs → redeploy
6. Healthy private service → generate domain → open
7. Switch Production/Staging → verify resource and data scope
8. Change variables/settings → review Local Draft, then durable changeset when
   supported
9. Open service → move between Overview/Deployments/Logs/Metrics/Configure
10. Complete the common journey with keyboard only
11. Open and close the agent without losing project/service context
12. Unsupported backend capability → honest non-blocking state
13. One existing service → create and deploy a second service
14. Deploy enqueue failure → resume the same draft without duplication
15. Project observability → assert every widget uses the selected scope

Acceptance requires:

- no horizontal page overflow
- no hidden primary action
- no indefinite spinner
- no editable action in aggregate environment mode
- no modal canvas dimming on desktop
- no raw HTML or sensitive backend error text
- the same resource and deployment IDs across canvas, inspector, activity, and
  CLI
- every screenshot state backed by an executable interaction assertion

## First implementation slice

The first slice should be deliberately narrow and visible:

1. Make all "New project" actions open `/new`.
2. Resolve omitted scope to Production and make Compare explicit/read-only.
3. Mask secret Local Draft diffs and bound all capability-loading states.
4. Add the batched environment-resource summary read model.
5. Ship semantic Grid cards and a persistent project `Create` action.
6. Replace the service modal with a fixed non-modal desktop inspector and a
   nested full-page mobile route.
7. Add Overview with current truthful data only.
8. Add Playwright/axe desktop, mobile, and keyboard journeys for those changes.

This slice fixes the highest-friction interaction problems without waiting for
the entire platform parity roadmap. Deployment progress, metrics, domains, and
backups should then light up the new structure only as their canonical backend
contracts become real.

## Current official Railway references

- https://railway.com/changelog/2026-03-20-new-dashboard-layout
- https://railway.com/changelog/2026-03-06-domains-ui-layout
- https://railway.com/changelog/2026-02-20-domains
- https://railway.com/changelog/2026-02-13-chat-with-your-canvas
- https://docs.railway.com/guides/deploying-a-monorepo
- https://docs.railway.com/overview/keyboard-shortcuts
- https://docs.railway.com/networking/domains/working-with-domains
- https://docs.railway.com/observability/metrics
- https://docs.railway.com/templates/deploy
