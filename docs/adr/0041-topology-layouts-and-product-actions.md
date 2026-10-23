# ADR-0041: Durable topology layouts and stable product actions

- Status: accepted
- Date: 2026-08-13

## Context

The project workspace already exposes explicit Grid and Map views, but map
coordinates are browser-local and its inferred datastore edges can imply a
relationship that the control plane has never observed. Actions are also
assembled independently by the command palette, page buttons, and service
panels, which makes labels, capability checks, and disabled explanations drift.

## Decision

Midnight stores one topology layout for each project/environment pair. A layout
contains only bounded presentation data: service IDs and integer canvas
coordinates. It is tenant scoped through the project/environment lineage,
versioned with a strong ETag, and replaced with `PUT` plus `If-Match`. The first
write uses `If-Match: "0"`; concurrent writers receive `412` and must reload.
Memory and PostgreSQL stores implement the same create/read/compare-and-swap
contract. Coordinates are never rewritten just because an inspector opens.

The Map toolbar offers fit, zoom in/out, center selection, auto-layout, and
reset. Auto-layout is deterministic and persists; fit/zoom/center are ephemeral
viewport preferences. Small screens get the same readable map with horizontal
overflow and a compact toolbar; Midnight does not claim touch-pinch or a
mobile-native graph editor.

Edges are rendered only from explicit backend relationship fields. Proximity,
resource kind, or the mere presence of a datastore is not evidence of an
attachment.

The console also has a stable typed action catalog. An action has a durable ID,
label, section, required scope/context, keywords, and a setup-oriented disabled
reason. Surfaces bind runtime handlers separately, so the registry cannot hide
API/CLI semantics or invent an executable capability. The command palette is
the first complete consumer; project and topology buttons reuse the same IDs
and labels. Agent presentations may enumerate the same safe metadata, but
execution remains behind existing API authorization and idempotency contracts.

## Consequences

- Layouts follow an authenticated user across browsers without becoming deploy
  configuration or affecting service execution.
- CAS prevents silent last-writer-wins corruption.
- Removing a service does not require mutating the layout; stale entries are
  ignored and the next replacement naturally prunes them.
- A missing durable store is an honest unavailable state, with browser-local
  fallback used only to keep the current editing session usable.
- New product actions must be added to the catalog before being exposed by a
  palette or contextual menu.
