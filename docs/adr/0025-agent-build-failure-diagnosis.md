# ADR-0025: Agent build-failure auto-diagnosis

- Status: proposed
- Date: 2026-05-17
- Owners: agent track

## Context

Midnight's deploy / build operations transition to `FAILED` for a wide
range of reasons, from misconfigured environment variables to
Dockerfile bugs to transient registry outages. Today the operator has to
hand-correlate operation events, build logs, and runtime logs to land on
a fix. The Anthropic SDK and tool loop are landing concurrently
(`internal/agent/`), and the dashboard surface for a chat panel attached
to deployments is already in flight.

The opportunity: when a deploy or build operation enters `FAILED`,
automatically ask the agent for a terse, action-oriented diagnosis and
attach the answer as a conversation on the failed deployment. The
operator opens the dashboard, sees a "💡 Diagnosis available" indicator
on the failed row, and clicks through to a chat with a starting
diagnosis already in place.

## Decision

Add `internal/agent/diagnosis/` containing a single `DiagnosisRunner`
that implements `api.OperationLifecycleObserver`. The runner is wired as
an observer on the operations store (`api.ObservedOperationStore`) so
every code path that transitions an operation to `FAILED` — the deploy
executor in `internal/deploy/executor.go`, the worker-side
`workerOperationUpdater`, and any future build executor — emits a single
observer call without each caller knowing about the diagnosis feature.

### Observer interface

```go
type OperationLifecycleObserver interface {
    OnFailed(ctx context.Context, op orailtypes.Operation, events []OperationEvent)
}
```

The observer fan-out is the wrapper's responsibility: `ObservedOperationStore`
calls `OnFailed` after `Fail` or any `Transition` that lands in `FAILED`
succeeds. Observer panics are recovered inside the wrapper so a
misbehaving observer never re-fails the operation; the runner additionally
runs the real work in a goroutine with its own 2-minute timeout context,
detached from the FSM caller's context, so a cancelled request context
never aborts a diagnosis already in flight.

### What the runner sees

1. The `Operation` row plus its tail of `OperationEvent` entries.
2. The build log tail (≤ 200 lines) and runtime log tail (≤ 100 lines)
   pulled via the `LogTailer` seam.
3. Service metadata, the previous succeeded deployment id, and the list
   of variable names (no values, no secrets) via the `ServiceLookup`
   seam.

The runner renders these into a single user message via
`RenderUserMessage`, paired with the system preamble:

> You are diagnosing a failed Midnight deployment. Be specific, terse,
> and propose a concrete fix. If the fix is a variable change, recommend
> `set_variable(...)`. If it's a Dockerfile/source issue, recommend a
> code change with the exact line.

### Model + cost story

- Model: `claude-opus-4-7`, overridable via
  `MIDNIGHT_AGENT_DIAGNOSIS_MODEL`. Opus is the right floor here — the
  diagnosis is a one-shot inference, and the difference between Sonnet
  and Opus on synthesis tasks against truncated logs is material.
- Logs are truncated tail-first (build ≤ 200 lines, runtime ≤ 100). The
  tail bias is deliberate: failure context lives at the bottom of the
  stream.
- One diagnosis is at most one Send call; we deliberately do not enable
  streaming or tool use for this code path (`AllowMutations: false`).

### Dedupe + rate limit

- **Per-operation dedupe.** The runner keeps an in-process set of
  `operation_id`s it has already diagnosed. Cross-restart dedupe is
  enforced by the `diagnosis_id` field on the operation's `metadata`
  JSONB: the dashboard checks that field before re-rendering the
  indicator. Dedupe key = `operation_id`.
- **Host-wide token bucket.** Default 10 diagnoses per 5 minutes,
  overridable via `MIDNIGHT_AGENT_DIAGNOSIS_RATE="<count>/<duration>"`
  (e.g., `MIDNIGHT_AGENT_DIAGNOSIS_RATE=30/15m`). When the bucket is
  empty the runner logs and skips — it does not queue. A flapping
  deploy will get one diagnosis per window, not 10 in a row.
- **Circuit breaker.** Three consecutive Caller errors open the
  breaker for 10 minutes. While open the runner returns immediately
  without consuming dedupe state. The breaker auto-closes on the next
  call after the cooldown elapses; the first success resets the
  counter.

### Why no mutations

Two reasons:

1. The diagnosis runs unsupervised. We do not want an unsupervised
   agent applying a `set_variable` that, if wrong, takes a service
   down — there is no human in the loop at this point in the flow.
2. The Apply Fix workflow naturally lives in the chat panel, where
   the operator is already present and can review the proposed
   action. Making the diagnosis read-only forces the apply path into
   the supervised surface, which is the right factoring.

The agent surfaces fixes as markdown text (`- set_variable(...)`,
`- Edit Dockerfile line 14 ...`) and the frontend's existing tool-call
renderer already handles markdown.

### Result surfacing

- Conversation title: `Diagnosis: <service-slug> deploy <op-id-suffix>`.
- First (and only) assistant message: the model's diagnosis output.
- Conversation tags: `midnight.kind=diagnosis`,
  `midnight.operation_id=<id>`, plus `midnight.deployment_id` and
  `midnight.service_id` when resolved.
- The runner also writes the conversation id back to the operation
  metadata under the key `diagnosis_id` and appends an event with the
  message `deploy:diagnosis_attached diagnosis_id=<id>` so the
  dashboard timeline can render the pill without re-fetching the
  conversation store.

### Failure of the agent call

If `agent.Caller.Send` returns an error, panics, or returns an empty
body, the runner:

1. Logs at warn level with the operation id and the breaker state.
2. Bumps the circuit breaker counter (does NOT record success).
3. Does nothing else — no metadata write, no conversation, no event
   append, and crucially **no re-entry into the operation FSM**. The
   underlying `Fail(...)` call has already completed; the failure of a
   diagnosis run is a soft signal that lives entirely in the runner's
   internal state and structured logs.

### Toggle

The runner is OFF by default. Set `MIDNIGHT_AGENT_DIAGNOSIS_ENABLED=1`
in the host environment to turn it on. The off-by-default posture
keeps test suites and developer hosts from making outbound agent
calls.

## Consequences

- A new package `internal/agent/diagnosis/` is added with three
  source files (`runner.go`, `prompts.go`, `rate_limiter.go`) plus
  tests. The package depends on `internal/api` (for the observer
  interface, operation events, and the log query types) and on
  `pkg/orailtypes`. It does not depend on the chat REST API or the
  frontend.
- `internal/api/operation_store.go` gains an
  `OperationLifecycleObserver` interface and an
  `ObservedOperationStore` decorator. The decorator is opt-in:
  callers that do not register observers see no behaviour change.
- Production wiring (`cmd/midnight-worker/main.go`, the API server
  bootstrap) is the only place that pulls in the diagnosis runner.
  The runner's `Caller`, `ConversationStore`, `LogTailer`,
  `ServiceLookup`, `MetadataWriter`, and `EventAppender` are all
  separate seams to keep production wiring incremental: a host with
  the agent SDK present but the chat backend not yet wired skips
  diagnoses cleanly.
- Future "Apply fix" flow is documented but not implemented. The
  runner's `AllowMutations: false` posture is the explicit boundary;
  when the apply-fix UX lands it will live behind the chat panel's
  human-in-the-loop tool-call confirmation, not in the diagnosis
  runner.

## Links

- Related panel decisions: ADR-0001 (panel decisions normative).
- Related work items: agent core track (`internal/agent/agent.go`),
  chat backend track (`ConversationStore`), dashboard chat panel.
- Related implementation PRs: this commit.
