# Enterprise Audit Export Foundation

Enterprise audit export planning lives in `internal/enterprise/audit` as a pure validation contract.

The current slice accepts NDJSON export requests for a workspace, validates requester and purpose fields, checks destination shape (`download` or `log_drain`), bounds explicit time windows to 370 days, and returns planned audit action names. It is designed to sit above the existing `internal/audit` hash-chain export primitives.

This foundation performs no file writes, network delivery, object-storage upload, SIEM delivery, billing checks, or retention enforcement. Live export execution and authorization wiring are future work.
