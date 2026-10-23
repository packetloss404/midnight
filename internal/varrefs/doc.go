// Package varrefs implements Railway-compatible reference variables for
// Midnight services.
//
// A variable's stored value is a template string that may contain one or
// more reference expressions wrapped in `${{...}}`. Three reference kinds
// are supported:
//
//   - `${{ServiceName.VAR_NAME}}` — pull VAR_NAME from another service in
//     the same project + environment.
//   - `${{shared.KEY}}`           — pull KEY from a project-scoped
//     "shared" variable group.
//   - `${{VAR}}`                  — pull VAR from the same service that
//     owns the template (chaining permitted).
//
// In addition, a small set of built-in `${{this.*}}` references is
// auto-injected per service:
//
//   - `${{this.MIDNIGHT_PUBLIC_DOMAIN}}`
//   - `${{this.MIDNIGHT_PRIVATE_DOMAIN}}`
//   - `${{this.MIDNIGHT_SERVICE_NAME}}`
//
// Resolution semantics (Sprint MVP):
//
//   - References are resolved at deploy time. The stored value remains
//     the template; only the resolved value is fed to the runtime
//     scheduler's env map.
//   - Cycles in the reference graph produce ErrReferenceCycle with the
//     full visit path.
//   - Missing targets produce ErrUnknownReference identifying the
//     offending (service, name) tuple.
//   - Invalid syntax produces ErrInvalidSyntax pointing at the byte
//     offset where the parser gave up.
//
// The deploy operation rejects the operation with a typed failure code
// when resolution cannot complete (VARIABLE_REFERENCE_UNRESOLVED,
// VARIABLE_REFERENCE_CYCLE). The operation event log records only the
// resolved env-map shape (name + value length), never plaintext.
//
// See ADR-0019 for the design rationale, why we store templates rather
// than resolved values, and why a staged-changes workflow is intentionally
// out of scope for the first cut.
package varrefs
