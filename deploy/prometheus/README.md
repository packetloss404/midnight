# Prometheus

S07 owns packaged alert rules and metric fixtures.

`alerts.yaml` contains the Phase 0 locked alert placeholders. The same alert names are mirrored in `internal/observability` so the release gate can verify the set stays complete.

Alert packaging evidence is built from the packaged alert bytes and records the publish path, alert count, locked alert count, status, and validation issues. A ready alert package can be referenced by the release evidence index; alert drift or invalid YAML blocks the index through the `alert-packaging-evidence` reference.

Operators should attach the alert packaging evidence JSON with the support/backup release evidence packet and evidence index. Review the evidence before release promotion and reject the packet if packaged alerts differ from the locked Phase 0 alert set.
