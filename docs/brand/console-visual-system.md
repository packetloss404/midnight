# Console Visual System and Accessibility Gate

Status: implemented foundation; browser acceptance is release-gated.

Midnight follows Railway's workflow density without copying Railway's brand,
palette, iconography, or component geometry. The console uses the existing
crescent-over-horizon mark from `docs/brand/logo.svg`, a blue identity channel,
green operational success and primary interaction, purple desired-state
changes, amber warnings, and red failures.

## Semantic color channels

- `brand`: Midnight identity and the crescent/horizon mark.
- `interaction`: actionable controls and focus.
- `selection`: selected resources and navigation context.
- `status-*`: reported runtime or operation evidence only.
- `change`: unapplied or reviewable desired state.

The legacy `accent` aliases remain while components migrate, but new code must
choose a semantic channel. A selected row must not imply that it is healthy,
and a green status must never be used merely as decoration.

## Surface rules

- Grid is deterministic and avoids decorative glow or topology dots.
- Map may use the topology dot field because position and relationships are
  meaningful there.
- The portfolio may use a restrained constellation preview, but it must not
  manufacture service health.
- Capability loading, empty, forbidden, degraded, and unavailable states stay
  visibly distinct and terminate without indefinite spinners.
- Ko-fi is optional project support. It never changes policy limits, feature
  availability, priority, or authorization.

## Accessibility acceptance

Every release runs component tests plus real Chromium journeys covering:

1. keyboard navigation and focus restoration;
2. desktop, mobile, and a 200-percent-equivalent viewport;
3. reduced motion and forced-color rendering;
4. WCAG 2.1 A/AA critical and serious automated checks;
5. no horizontal page overflow or hidden primary action; and
6. stable accessible names independent of icon or visual treatment.

Automated axe results complement rather than replace keyboard assertions and
manual live-browser inspection.
