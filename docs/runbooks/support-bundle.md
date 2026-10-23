# Support Bundle Runbook

Support bundles are defined by `internal/supportbundle` selector and manifest contracts. The support bundle manifest is one input to the release evidence index; it is reviewed before sharing and before release promotion.

Release evidence:

- The support bundle archive review feeds the backup release evidence packet.
- A rejected archive blocks release evidence; a `needs-review` archive marks the packet and evidence index for operator review.
- Redaction findings and excluded files are preserved as review issues so operators can see why the bundle needs approval.
- The evidence index references the published support bundle manifest path and manifest ID.

Operator review decisions:

- Approve only when the manifest paths are relative, the archive contents are within budget, and redactions/exclusions are acceptable.
- Reject when the bundle contains secrets, customer payloads, unsafe paths, or unexplained oversized content.
- Leave the decision pending while any redaction, exclusion, or archive budget question is unresolved.

Minimum review checklist:

- Confirm the selector includes at least one input kind: logs, metrics, or events.
- Confirm manifest file paths are relative to the bundle root.
- Confirm bundle contents avoid secrets and customer payloads before sharing externally.
- Confirm release evidence records the archive review decision and any redaction or exclusion findings.

Read-only AI diagnosis artifacts may be included only after the same redaction pass. Class-0/1 diagnosis artifacts must contain observations and runbook hints only; they must not contain tokens, customer payloads, queued operations, remediation approvals, or any mutation result.

Class-2 AI draft-action artifacts may include redacted proposed changes and required approvals for operator review. They remain draft-only: support bundles must show `mutationAllowed:false`, `executionAllowed:false`, and `approvalAllowed:false`, and no queued operation, approval grant, or mutation result may be included.
