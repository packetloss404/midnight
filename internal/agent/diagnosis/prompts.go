// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package diagnosis implements the build-failure auto-diagnosis runner
// described in docs/adr/0025-agent-build-failure-diagnosis.md.
//
// The package contains a single side-effecting unit, DiagnosisRunner, that
// listens for FAILED transitions on deploy/build operations and asks the
// Anthropic agent (via the agent.Caller seam owned by the core track) to
// produce a terse diagnosis. Results are persisted as a new conversation
// in the chat-backend's ConversationStore so the dashboard can surface a
// "Diagnosis available" indicator on the failed row.
//
// Hard constraints (see ADR-0025 for the full rationale):
//
//   - The runner is read-only. CallRequest.AllowMutations is always false;
//     suggested fixes are surfaced as markdown text only.
//   - The runner must NEVER re-fail the underlying operation. Caller
//     errors, store errors, and panics are all logged and swallowed.
//   - At most one diagnosis run per operation_id (idempotent dedupe).
//   - A token-bucket rate limit caps host-wide diagnosis spend and a
//     circuit breaker disables the runner after three consecutive Caller
//     errors.
//   - Diagnosis is OFF by default; production hosts opt in via
//     MIDNIGHT_AGENT_DIAGNOSIS_ENABLED=1.
package diagnosis

import (
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// SystemPreamble is the system message prepended to every diagnosis
// CallRequest. It locks the model into a terse, action-oriented
// failure-analysis mode and steers it toward two well-known fix shapes
// (variable change vs. source change). The preamble is intentionally
// short to keep the cost of each diagnosis predictable.
//
// The diagnosis loop is strictly read-only: it writes a report into a
// conversation that a person reads later, and nobody is present to
// approve a change. The preamble used to instruct the model to
// "recommend `set_variable(...)`" while the runner passed no tools at
// all — an instruction to call something that did not exist. It now
// asks for the change in prose, which is what the reader can act on.
const SystemPreamble = "You are diagnosing a failed Midnight deployment. " +
	"Be specific, terse, and propose a concrete fix. " +
	"You are producing a written report for an engineer to read and act on: describe the fix, do not perform it. " +
	"If the fix is an environment-variable change, name the exact variable and the value it should hold. " +
	"If it's a Dockerfile/source issue, recommend a code change with the exact line."

// MaxBuildLogLines is the upper bound on build-log lines included in the
// diagnosis user message. The tail is preferred so the most recent
// failure context is always present. Mirrors the ADR-0025 cost story.
const MaxBuildLogLines = 200

// MaxRuntimeLogLines is the upper bound on runtime-log lines included.
const MaxRuntimeLogLines = 100

// MaxEventMessages is the upper bound on operation events included in
// the diagnosis user message. The tail is preferred.
const MaxEventMessages = 20

// FailureContext is the structured input the runner feeds to the agent.
// It is rendered into a single user message via RenderUserMessage. The
// struct is exported so tests can construct deterministic inputs without
// relying on the live operation store.
type FailureContext struct {
	OperationID    string
	OperationKind  string
	FailureCode    orailtypes.FailureCode
	FailureReason  string
	ServiceID      string
	ServiceSlug    string
	DeploymentID   string
	PreviousDeploy string
	Variables      []string
	EventMessages  []string
	BuildLogTail   []string
	RuntimeLogTail []string
	SpecDigest     string
}

// FromOperation builds a FailureContext from the operation + event
// snapshot delivered by OperationLifecycleObserver.OnFailed. The runner
// enriches the result with logs/service/variable metadata before calling
// the agent.
func FromOperation(op orailtypes.Operation, events []api.OperationEvent) FailureContext {
	ctx := FailureContext{
		OperationID:   string(op.ID),
		OperationKind: op.Kind,
		FailureCode:   op.Code,
		FailureReason: op.Reason,
	}
	if len(events) == 0 {
		return ctx
	}
	from := 0
	if len(events) > MaxEventMessages {
		from = len(events) - MaxEventMessages
	}
	messages := make([]string, 0, len(events)-from)
	for _, event := range events[from:] {
		message := strings.TrimSpace(event.Message)
		if message == "" {
			continue
		}
		messages = append(messages, fmt.Sprintf("[%s] %s", event.State, message))
	}
	ctx.EventMessages = messages
	return ctx
}

// RenderUserMessage produces the single user message the runner sends to
// the agent. The format is stable so the eval suite can pin diagnoses to
// regression fixtures without snapshotting model output. Sections are
// only emitted when their underlying data is present, which keeps
// short-context failures (e.g., DeployJobMalformed before any log lines
// exist) cheap.
func RenderUserMessage(ctx FailureContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A Midnight %s operation failed.\n\n", strings.TrimSpace(ctx.OperationKind))
	fmt.Fprintf(&b, "operation_id: %s\n", ctx.OperationID)
	if ctx.FailureCode != "" {
		fmt.Fprintf(&b, "failure_code: %s\n", ctx.FailureCode)
	}
	if ctx.FailureReason != "" {
		fmt.Fprintf(&b, "failure_reason: %s\n", ctx.FailureReason)
	}
	if ctx.ServiceID != "" {
		fmt.Fprintf(&b, "service_id: %s\n", ctx.ServiceID)
	}
	if ctx.ServiceSlug != "" {
		fmt.Fprintf(&b, "service_slug: %s\n", ctx.ServiceSlug)
	}
	if ctx.DeploymentID != "" {
		fmt.Fprintf(&b, "deployment_id: %s\n", ctx.DeploymentID)
	}
	if ctx.PreviousDeploy != "" {
		fmt.Fprintf(&b, "previous_succeeded_deployment: %s\n", ctx.PreviousDeploy)
	}
	if ctx.SpecDigest != "" {
		fmt.Fprintf(&b, "service_spec_digest: %s\n", ctx.SpecDigest)
	}
	if len(ctx.Variables) > 0 {
		fmt.Fprintf(&b, "variables: %s\n", strings.Join(ctx.Variables, ", "))
	}
	if len(ctx.EventMessages) > 0 {
		b.WriteString("\noperation_events:\n")
		for _, message := range ctx.EventMessages {
			b.WriteString("  - ")
			b.WriteString(message)
			b.WriteString("\n")
		}
	}
	if len(ctx.BuildLogTail) > 0 {
		b.WriteString("\nbuild_log_tail:\n")
		for _, line := range ctx.BuildLogTail {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if len(ctx.RuntimeLogTail) > 0 {
		b.WriteString("\nruntime_log_tail:\n")
		for _, line := range ctx.RuntimeLogTail {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\nReturn:\n")
	b.WriteString("  1. A one-sentence diagnosis.\n")
	b.WriteString("  2. A short bullet list of suggested actions (a variable to change, a code fix, etc.).\n")
	b.WriteString("  3. Optional: why you ruled out other causes.\n")
	return b.String()
}

// ConversationTitle returns the conversation title the runner attaches
// to the persisted diagnosis. Stable format so the dashboard's "find
// existing diagnosis" UX can group reliably.
func ConversationTitle(serviceSlug, operationID string) string {
	slug := strings.TrimSpace(serviceSlug)
	if slug == "" {
		slug = "service"
	}
	op := strings.TrimSpace(operationID)
	suffix := op
	if idx := strings.LastIndex(op, "_"); idx >= 0 && idx < len(op)-1 {
		suffix = op[idx+1:]
	}
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	if suffix == "" {
		suffix = op
	}
	return fmt.Sprintf("Diagnosis: %s deploy %s", slug, suffix)
}

// TailLines returns the last n entries of lines without copying when no
// truncation is needed. Used by the runner to keep large log payloads
// bounded before they leave the host.
func TailLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
