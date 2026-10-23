# Midnight — Definition of Ready and Definition of Done

These are gates, not aspirations. A story or epic that does not meet the criteria below should not enter sprint or be called complete. Reviewers enforce; the team self-polices.

The shorthand "panel" below means an "Eight-Specialist Panel Locked Decision" recorded at `D:/projects/midnight/midnight-platform-plan.md` lines 71–204. Stories that touch any panel-locked surface MUST cite the specific decision they implement.

---

## 1. Definition of Ready — Story

A story is Ready when ALL of the following are true. Reviewer checks each box during sprint planning.

### 1.1 Source linkage

- [ ] **Panel decision link.** If the story touches any decision in the panel-locked section, the story body cites the exact line(s) in `midnight-platform-plan.md`. If the story does not touch a panel decision, the body explicitly says so.
- [ ] **Epic link.** Story is linked to a single parent epic in `D:/projects/midnight/agile/epics/`.
- [ ] **Feature dossier link** (if applicable). Story references the relevant feature in `D:/projects/midnight/agile/features/FEAT-<id>.md`.
- [ ] **Spec section link.** Body cites the relevant section header from the spec (e.g., "§ Deployment State Machine").

### 1.2 Acceptance criteria written

- [ ] AC are written in **Given / When / Then** form, one per behavioral guarantee.
- [ ] Each AC is independently testable.
- [ ] Happy path AC exists.
- [ ] Failure path AC exists with the **exact failure reason code** the spec assigns (`BUILD_FAILED`, `IMAGE_SIGNATURE_INVALID`, `IDEMPOTENCY_REQUEST_IN_PROGRESS`, etc.).
- [ ] Idempotent-retry AC exists if the endpoint is mutating.
- [ ] Observable-status AC exists: how does CLI/API surface success and failure?
- [ ] Supportability AC exists: what shows up in `support-bundle`, `audit_logs`, `events`?

Example pattern:

```
AC1: Given an authenticated user with `deployments:create` scope,
     when they POST /api/v1alpha/services/{svc_id}/deployments
     with valid body and Idempotency-Key,
     then the response is 202 with Location: /api/v1alpha/operations/{op_id}
     and the operation transitions QUEUED → BUILDING → DEPLOYING → ACTIVE
     within the configured deploy timeout.

AC2: Given the same key + body is replayed within 24h,
     then the same operation is returned with header Idempotent-Replayed: true.

AC3: Given the same key + a different body within 24h,
     then 409 IDEMPOTENCY_KEY_REUSED is returned and no new operation is created.
```

### 1.3 Test plan exists

- [ ] Unit tests named in story body (file paths or test names).
- [ ] Integration tests named (testcontainers, Postgres, Docker engine).
- [ ] Contract test impact noted (Schemathesis, Pact). If the story changes an API contract, the OpenAPI diff plan is in the story.
- [ ] Drill impact noted. If the story affects an Alpha-gating drill, the drill update is part of the story.
- [ ] Fixture impact noted. If the story affects redaction, the secret-leakage fixture set is updated.

### 1.4 Dependencies are clear

- [ ] All blocking stories are linked.
- [ ] External dependencies (BuildKit version, Railpack version, Traefik version, Docker Engine API floor) are stated.
- [ ] Schema-migration prerequisites are stated. Cross-cutting schema changes block until ADR exists.

### 1.5 Sized ≤ 8 points

- [ ] Estimated in points using the team's reference set (1, 2, 3, 5, 8).
- [ ] Stories > 8 points must be split before entering sprint. No exceptions.
- [ ] Stories with 8 points need a written split plan in case of mid-sprint risk.

### 1.6 Security review trigger evaluated

If the story touches any of the following, it requires pre-implementation security review (see `SECURITY_REVIEW_PLAN.md`):

- Bootstrap admin / authentication / session management
- KEK / DEK / secret store / sealed variables
- Build host / BuildKit / build secrets
- Ingress, ACME, certificate state
- Webhook ingestion (SSRF, replay, signature verification)
- AI gateway / MCP server
- Dashboard auth / cookies / CSP
- Audit chain / audit checkpoint signing
- Registry signing / image signature verification
- Docker socket / capability allowlist / runtime capability
- IaC plan/apply, drift, import/export

The story body records the security reviewer's name and the date of pre-implementation review.

### 1.7 Documentation impact noted

- [ ] User-facing docs to update are listed.
- [ ] Operator runbook impact is noted (does this require a new runbook in `OPERATIONS_RUNBOOK_INDEX.md`?).
- [ ] Compatibility matrix impact noted (`docs/compat/docker.md`, `docs/compat/components.md`).
- [ ] Error code registry impact noted (`docs/error-codes.md`).
- [ ] CLI reference impact noted.

---

## 2. Definition of Done — Story

A story is Done when ALL of the following are true. The reviewer enforces; merge is blocked otherwise.

### 2.1 Code merged

- [ ] PR is reviewed and approved by at least two reviewers.
- [ ] At least one reviewer is from the affected sub-team.
- [ ] Security-impacting PRs are reviewed by the security lead.
- [ ] PR is rebased onto current main; no merge commits.
- [ ] PR description references the story ID and panel decision (if applicable).

### 2.2 CI green

- [ ] All required CI checks pass:
    - `go vet`, `golangci-lint`, formatting (`gofmt`, `goimports`).
    - Unit + integration tests, race detector enabled, `-count=1` (no test caching).
    - Contract tests (Schemathesis property tests, Pact verification) if API surface touched.
    - Drill suite for any drill the story affects.
    - Coverage floor (see `QUALITY_AND_TEST_STRATEGY.md`).
    - `govulncheck`, `osv-scanner`, `npm audit --omit=dev` pass.
    - `semgrep` pass on changed files.
    - OpenAPI lint (operationId, auth scopes, schemas, examples, rate-limit class, idempotency behavior, error schema).
    - oasdiff: no breaking changes against last released minor (or breaking changes accepted via ADR).
- [ ] CI run completes within the team's agreed budget; otherwise the story includes a CI optimization task.

### 2.3 Tests covering AC

- [ ] Each AC maps to at least one named test. The mapping is in the PR description.
- [ ] Failure-path tests assert the **exact** reason code from the spec (no string-match, no `contains`).
- [ ] Idempotency tests assert: same key + body → same op_id; same key + different body → 409 `IDEMPOTENCY_KEY_REUSED`; concurrent in-flight duplicate → 409 `IDEMPOTENCY_REQUEST_IN_PROGRESS`.
- [ ] No `t.Skip` without a linked follow-up story.
- [ ] No `time.Sleep` longer than 100ms without an explanatory comment; prefer event-based waits.

### 2.4 Panel decision verified by test name

If the story implements a panel-locked decision, at least one test name encodes the decision so it cannot be silently regressed. Example:

```go
func TestRollback_PullsStoredDigest_NeverRebuilds(t *testing.T) { ... }
func TestACME_OffsetGreaterThan5s_BlocksIssuance(t *testing.T) { ... }
func TestIdempotency_KeyReuseDifferentBody_Returns409(t *testing.T) { ... }
func TestDeployment_SupersedeArbitration_UsesMonotonicSequence(t *testing.T) { ... }
func TestAudit_RowHashChain_VerifiesAcrossRestart(t *testing.T) { ... }
```

The PR description lists each panel-decision-verifying test by name.

### 2.5 Documentation updated

- [ ] User docs updated (if user-facing).
- [ ] CLI `--help` output verified against docs.
- [ ] OpenAPI spec updated, regenerated, and committed.
- [ ] CloudEvents schema (if any new event) registered at `/schemas/events/<type>/<version>.json`.
- [ ] Error code registry updated if new code introduced.
- [ ] Compatibility matrix updated if version floors moved.
- [ ] Runbook updated or added (see `OPERATIONS_RUNBOOK_INDEX.md`).

### 2.6 Support bundle output

- [ ] `midnight support-bundle` output is **unchanged**, OR
- [ ] The change is intentional, documented in the PR description, and the redaction fixture set is updated to assert no new sensitive field leaks.
- [ ] Manifest's `truncated:` flags still behave correctly.

### 2.7 No new lint, no new vulns

- [ ] Zero new findings from `golangci-lint`, `semgrep`, `govulncheck`, `osv-scanner`, `npm audit`.
- [ ] If a finding is a false positive, the suppression is annotated with a one-line reason and a link to a follow-up issue.

### 2.8 Audit, events, metrics wired

- [ ] If the story creates or mutates a resource, an audit event is emitted with the schema in the spec (request_id, correlation_id, actor, actor_type, auth_method, source_ip, user_agent, resource, action, before/after, policy decision, timestamp).
- [ ] Audit row participates in the per-row hash chain.
- [ ] CloudEvents lifecycle event is emitted where appropriate.
- [ ] Prometheus metrics added/updated and scraped at the configured 15s interval.

### 2.9 Security checklist for high-risk stories

If the story is on a high-risk surface (see `SECURITY_REVIEW_PLAN.md` § High-Risk Surfaces):

- [ ] Threat-model section in the PR description.
- [ ] Mandatory reviewer signed off.
- [ ] Negative tests included (auth bypass, scope mismatch, replay, SSRF, malicious input).
- [ ] Redaction fixtures pass with `secret_leakage_rate == 0`.

---

## 3. Definition of Ready — Epic

An epic is Ready when sprint planning can carry out child stories without blocking architectural ambiguity.

### 3.1 Acceptance criteria

- [ ] Epic-level AC is written in spec-quoting form: which user-visible promise from the spec does this epic deliver?
- [ ] Epic-level AC names the panel decisions it satisfies and the failure drills that exercise it.
- [ ] Epic-level AC names the demo: what does the design partner see when this epic ships?

### 3.2 Design doc / ADR

- [ ] Design doc exists at `D:/projects/midnight/docs/design/<epic-id>.md`. It covers:
    - Goals (≤ 5 bullets).
    - Non-goals (explicit).
    - Architecture diagram (ASCII or PNG).
    - Data model changes (tables, indexes, partitions).
    - API surface changes (endpoints, request/response, error codes).
    - Security model: authz checks, secret handling, threat model deltas.
    - Observability: events, metrics, audit, logs.
    - Compatibility: schema version, API version, agent/server skew impact.
    - Testing strategy and drill impact.
    - Rollout plan and feature flag.
- [ ] If the epic deviates from any panel-locked decision, an accepted ADR exists.
- [ ] Design doc reviewed by tech lead + security lead (if security surface) + relevant sub-team lead.

### 3.3 Capacity allocated

- [ ] Sprint capacity exists for at least the first three stories of the epic.
- [ ] Reviewer load is realistic; stories aren't all gated on a single reviewer.
- [ ] On-call coverage is assigned for any drill or runbook the epic introduces.

### 3.4 Story breakdown

- [ ] At least the next two sprints' worth of stories are split, sized ≤ 8 points each, and Ready.
- [ ] Cross-team dependencies are listed and confirmed by the dependent team.

---

## 4. Definition of Done — Epic

An epic is Done when ALL of the following are true.

### 4.1 All stories done

- [ ] Every child story meets the story DoD.
- [ ] No "carryover" or "tech debt" stories without a linked owner and a target sprint.
- [ ] No `// TODO(panel-deviation)` comments remain in the code without an accepted ADR.

### 4.2 Demoed to a design partner

- [ ] Demo session held with at least one design partner, recorded.
- [ ] Demo includes the failure path, not just the happy path.
- [ ] Design-partner feedback is captured in `D:/projects/midnight/docs/design-partner-feedback/<epic-id>.md`.
- [ ] Any blocking feedback is converted to follow-up stories with target sprint.

### 4.3 Panel decisions verified

- [ ] Every panel decision the epic claims to implement has a named test.
- [ ] The test names are listed in the epic's "Done" section.
- [ ] Tests run on every CI; not behind a feature flag.

### 4.4 Runbook entry exists

- [ ] If the epic introduces a new operational concern, a runbook is added at `/usr/share/midnight/runbooks/<name>.md` and indexed in `OPERATIONS_RUNBOOK_INDEX.md`.
- [ ] Runbook follows the template (preconditions / steps / verification / rollback / audit).
- [ ] Runbook has been walk-throughed by at least one operator who did not write it.

### 4.5 Monitoring and alerts wired

- [ ] Prometheus metrics emitted and present in the default Alpha alert rules file.
- [ ] Default Grafana dashboard `midnight-overview.json` shows the new signals where relevant.
- [ ] Default alert rule added if a Class A or higher remediation depends on it.
- [ ] Alert rule has a paired runbook link.

### 4.6 Compatibility recorded

- [ ] Component compatibility matrix (`docs/compat/components.md`) is updated.
- [ ] Schema version bumped if data schema changed.
- [ ] API version bump considered if breaking; if breaking, ADR exists explaining why this is acceptable in `v1alpha`.

### 4.7 Failure drills updated

- [ ] If the epic introduces a new failure mode, a drill is added to `test/drills/`.
- [ ] All Alpha-gating drills still pass after the epic merges.

### 4.8 Release-gate verification

- [ ] All release gates in `QUALITY_AND_TEST_STRATEGY.md` § Release Gates pass on the integration branch.
- [ ] Security checklist in `SECURITY_REVIEW_PLAN.md` is signed off for any high-risk surface touched.

---

## 5. Anti-patterns to reject

A reviewer should reject a story or epic that exhibits any of these:

- "AC: deploys correctly." (Not testable.)
- A failure-path AC without a reason code from the registry.
- A panel-touching story with no panel-decision link.
- A story sized 13 because "it's just one thing." Split it.
- A PR that adds a new test that calls `time.Sleep(30 * time.Second)`.
- A PR that disables a CI check "temporarily" without a follow-up issue.
- A story whose only test is `TestHappyPath`.
- An epic Done without a demo recording.
- A runbook merged without an operator walkthrough.
- A new error code without a registry entry.
- A new audit event that doesn't participate in the hash chain.
- A new endpoint without an idempotency story for `POST/PATCH/DELETE`.
- A new lifecycle event without a CloudEvents schema file.
- A new secret-handling code path that doesn't run against the redaction fixture set.

---

## 6. The five-minute reviewer checklist

For a reviewer who has 5 minutes and one PR:

1. Story link present? Panel decision cited (or explicitly N/A)?
2. AC has happy path, failure path with reason code, idempotent-retry, observable status, supportability?
3. Tests named in PR description map 1:1 to AC?
4. Coverage didn't drop?
5. No new lint, no new vulns?
6. Audit + events + metrics wired?
7. Doc and runbook updated?
8. If high-risk surface — security reviewer signed off?

If any answer is "no," request changes.
