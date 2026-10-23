# Enterprise Controls

This is the narrow planning contract for enterprise readiness. It is pure validation only; it does not implement SSO, SAML, SCIM, audit export, billing, abuse controls, HA control plane, multi-region routing, or stronger build isolation.

## Contract

`internal/enterprise.ControlsPlan` emits `midnight.dev/enterprise-controls-plan/v1alpha1` JSON with these gates:

- BYOK: `platform-managed` or `customer-managed`; customer-managed mode requires external key references and rejects inline secret material.
- Enterprise LTS: `standard` or `enterprise-lts`; enterprise LTS requires a `supported_until` date and patch cadence no longer than 90 days.
- Support SLA: `standard`, `premium`, or `mission-critical` with response targets for `sev1` through `sev4`.
- DPA/subprocessor: DPA acceptance, at least 30 days subprocessor notice, and an absolute subprocessor list URL.
- Data residency: `single-region` or `region-set`, explicit allowed regions, and telemetry export mode of `disabled`, `metadata-only`, or `allowed-region-only`.

## Status

- `ready`: all controls are complete.
- `needs-review`: non-blocking review is needed, such as no approved subprocessors recorded.
- `blocked`: required controls are missing or invalid.

The validator is deterministic and strict: unknown JSON fields, trailing JSON, schema mismatches, and status/issue mismatches are rejected.

## HTTP Status

The OpenAPI contract documents enterprise identity, audit-export, and controls
planner paths at `POST /api/v1alpha/enterprise/identity/plan`,
`POST /api/v1alpha/enterprise/audit/export-plan`, and
`POST /api/v1alpha/enterprise/controls/plan`. The router registers those
validation-only HTTP routes only when `MIDNIGHT_EXPERIMENTAL=1`; they are
absent from the normal product API.

The helpers validate request shape and return deterministic plans only; they do not call IdPs, SCIM servers, SAML endpoints, SIEM/log-drain destinations, KMS providers, support systems, legal systems, residency services, databases, or filesystems.

The product dashboard does not expose planner sample endpoints. Experimental
review can enable and call the planning routes; tests and tooling can call the
same planning helpers directly.
