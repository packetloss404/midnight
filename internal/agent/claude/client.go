// Package claude wires the Anthropic SDK to the platform [agent.Caller]
// seam. Only this package imports
// [github.com/anthropics/anthropic-sdk-go]; every other Midnight track
// consumes the agent through the interface in [internal/agent].
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

// Environment variables read at NewCaller time. The agent fails closed
// when [EnvAPIKey] is unset and no explicit key was passed to Options.
const (
	EnvAPIKey         = "ANTHROPIC_API_KEY"
	EnvChatModel      = "MIDNIGHT_AGENT_CHAT_MODEL"
	EnvDiagnosisModel = "MIDNIGHT_AGENT_DIAGNOSIS_MODEL"

	// EnvThinking selects the default extended-thinking mode for every
	// call that does not override it. Recognized values: "adaptive"
	// (default), "off"/"disabled"/"0"/"false".
	EnvThinking = "MIDNIGHT_AGENT_THINKING" // not-a-failure-code

	// EnvEffort selects the default `output_config.effort` level.
	// Recognized values: low, medium, high (default), xhigh, max.
	EnvEffort = "MIDNIGHT_AGENT_EFFORT" // not-a-failure-code

	// EnvMaxTokens overrides the default generation cap (an integer).
	// Applies to Send; Stream uses [agent.DefaultStreamMaxTokens] unless
	// this is set.
	EnvMaxTokens = "MIDNIGHT_AGENT_MAX_TOKENS" // not-a-failure-code

	// DefaultChatModel is the model used for the in-product agent chat
	// surface. Pinned by ADR-0023.
	DefaultChatModel anthropic.Model = "claude-opus-5"

	// DefaultDiagnosisModel is the model used for the auto-diagnosis
	// worker (a concurrent track). Pinned by ADR-0023.
	DefaultDiagnosisModel anthropic.Model = "claude-opus-5"
)

// Options configures [NewCaller]. The zero value is enough for production
// use as long as ANTHROPIC_API_KEY is set in the environment.
type Options struct {
	// APIKey overrides ANTHROPIC_API_KEY. When empty the env var is
	// consulted; if that is also empty NewCaller returns
	// [agent.AgentAPIKeyMissing].
	APIKey string

	// BaseURL overrides the Anthropic base URL. Honors ANTHROPIC_BASE_URL
	// when empty (the SDK reads that automatically).
	BaseURL string

	// HTTPClient is the underlying transport. When non-nil it replaces
	// the SDK's default — used by tests to inject a fake transport that
	// asserts on cache_control headers without hitting the API.
	HTTPClient option.HTTPClient

	// Registry is the tool set the caller will expose to Claude. When
	// nil, [tools.NewDefaultRegistry] is used.
	Registry *tools.Registry

	// DefaultModel overrides [DefaultChatModel]. Honors MIDNIGHT_AGENT_CHAT_MODEL
	// when empty.
	DefaultModel string

	// Metrics receives one Usage event per [Caller.Send] / [Caller.Stream].
	// When nil, a stdlib-log emitter is used.
	Metrics agent.MetricsEmitter

	// Logger is the stdlib logger for diagnostic output. Defaults to
	// [log.Default].
	Logger *log.Logger

	// Thinking is the default extended-thinking mode for calls that
	// leave [agent.CallRequest.Thinking] unset. When empty, MIDNIGHT_AGENT_THINKING
	// is consulted, then [DefaultThinking].
	Thinking agent.ThinkingMode

	// Effort is the default effort level for calls that leave
	// [agent.CallRequest.Effort] unset. When empty, MIDNIGHT_AGENT_EFFORT
	// is consulted, then [DefaultEffort].
	Effort agent.Effort

	// MaxTokens overrides the default generation cap. 0 consults
	// MIDNIGHT_AGENT_MAX_TOKENS, then the agent-package defaults.
	MaxTokens int
}

// Model-control defaults. Adaptive thinking is on because every task
// this agent takes on — reading deployment logs, correlating a failure
// with a variable change, deciding whether a rollback is safe — is
// reasoning work, and because it is the only supported thinking mode on
// the models Midnight ships with.
const (
	DefaultThinking = agent.ThinkingAdaptive
	DefaultEffort   = agent.EffortHigh
)

// NewCaller constructs a [agent.Caller] backed by the Anthropic SDK.
// Returns [agent.AgentAPIKeyMissing] when no API key can be resolved —
// the agent never silently degrades.
func NewCaller(opts Options) (agent.Caller, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(EnvAPIKey)
	}
	if apiKey == "" {
		return nil, agent.AgentAPIKeyMissing
	}

	// Credential resolution above is complete. SDK environment defaults can
	// otherwise add ANTHROPIC_AUTH_TOKEN alongside our explicit API key,
	// forwarding that unrelated bearer credential to a compatible provider.
	clientOpts := []option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithAPIKey(apiKey)}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL") // not-a-failure-code
	}
	if baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(opts.HTTPClient))
	}
	client := anthropic.NewClient(clientOpts...)

	registry := opts.Registry
	if registry == nil {
		registry = tools.NewDefaultRegistry()
	}

	defaultModel := opts.DefaultModel
	if defaultModel == "" {
		if env := os.Getenv(EnvChatModel); env != "" {
			defaultModel = env
		} else {
			defaultModel = DefaultChatModel
		}
	}

	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	metrics := opts.Metrics
	if metrics == nil {
		metrics = newLogMetricsEmitter(logger)
	}

	return &caller{
		client:          &client,
		registry:        registry,
		defaultModel:    defaultModel,
		defaultThinking: resolveThinking(opts.Thinking),
		defaultEffort:   resolveEffort(opts.Effort, logger),
		defaultMaxTok:   resolveMaxTokens(opts.MaxTokens),
		metrics:         metrics,
		logger:          logger,
		pricing:         defaultPricing(),
	}, nil
}

// resolveThinking picks the caller-level thinking default: the explicit
// option, then MIDNIGHT_AGENT_THINKING, then [DefaultThinking].
func resolveThinking(opt agent.ThinkingMode) agent.ThinkingMode {
	if opt != agent.ThinkingDefault {
		return opt
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvThinking))) {
	case "":
		return DefaultThinking
	case "off", "disabled", "disable", "0", "false", "no", "none":
		return agent.ThinkingOff
	case "adaptive", "on", "1", "true", "yes", "enabled", "enable":
		return agent.ThinkingAdaptive
	default:
		return DefaultThinking
	}
}

// resolveEffort picks the caller-level effort default. An unrecognized
// env value is logged and ignored rather than silently changing the
// model's behaviour.
func resolveEffort(opt agent.Effort, logger *log.Logger) agent.Effort {
	if opt != agent.EffortDefault {
		if opt.Valid() {
			return opt
		}
		return DefaultEffort
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvEffort)))
	if raw == "" {
		return DefaultEffort
	}
	candidate := agent.Effort(raw)
	if !candidate.Valid() || candidate == agent.EffortDefault {
		if logger != nil {
			logger.Printf("agent: ignoring unrecognized %s=%q (want low|medium|high|xhigh|max)", EnvEffort, raw)
		}
		return DefaultEffort
	}
	return candidate
}

// resolveMaxTokens picks the caller-level generation cap. 0 means "use
// the per-entrypoint agent default" (Send and Stream differ).
func resolveMaxTokens(opt int) int {
	if opt > 0 {
		return opt
	}
	if raw := strings.TrimSpace(os.Getenv(EnvMaxTokens)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// caller is the concrete [agent.Caller] implementation.
type caller struct {
	client          *anthropic.Client
	registry        *tools.Registry
	defaultModel    string
	defaultThinking agent.ThinkingMode
	defaultEffort   agent.Effort
	defaultMaxTok   int
	metrics         agent.MetricsEmitter
	logger          *log.Logger
	pricing         map[string]modelPrice
}

// Send drives the tool loop end-to-end and returns the aggregated result.
func (c *caller) Send(ctx context.Context, req agent.CallRequest) (agent.CallResult, error) {
	loop, err := c.prepareLoop(ctx, req, entrypointSend)
	if err != nil {
		return agent.CallResult{}, err
	}
	defer loop.cancel()

	var result agent.CallResult
	result.Usage.Model = loop.model

	for loop.iter < loop.maxIter {
		loop.iter++
		msg, err := c.client.Messages.New(loop.ctx, loop.params)
		if err != nil {
			c.emitUsage(ctx, &result.Usage)
			return result, fmt.Errorf("agent: model call failed: %w", err)
		}
		c.accumulateUsage(&result.Usage, msg)

		// Capture the assistant turn as agent.Message for the result.
		assistant := assistantToAgentMessage(msg)
		result.Messages = append(result.Messages, assistant)

		// Append the assistant turn to params for the next iteration.
		loop.params.Messages = append(loop.params.Messages, msg.ToParam())

		if msg.StopReason != anthropic.StopReasonToolUse {
			result.FinalText = collectFinalText(msg)
			result.StopReason = string(msg.StopReason)
			result.Iterations = loop.iter
			c.emitUsage(ctx, &result.Usage)
			return result, nil
		}

		// Execute every tool_use block and append results as a single
		// user turn.
		toolResults, userTurn, runErr := c.runTools(loop.ctx, msg, req.Env)
		if userTurn.Role == agent.RoleUser && len(userTurn.Content) > 0 {
			result.Messages = append(result.Messages, userTurn)
		}
		if len(toolResults) > 0 {
			loop.params.Messages = append(loop.params.Messages, anthropic.NewUserMessage(toolResults...))
		}
		if runErr != nil {
			// Tool errors do not abort the loop — they are surfaced to
			// the model as is_error=true tool_results so Claude can
			// recover. We track them only for logging.
			c.logger.Printf("agent: tool errors in iteration %d: %v", loop.iter, runErr)
		}
	}

	result.Iterations = loop.iter
	result.StopReason = "max_iterations"
	c.emitUsage(ctx, &result.Usage)
	return result, agent.ErrMaxIterations
}

// Stream runs the loop and emits StreamEvents as they arrive from the
// upstream SSE stream: one EventTextDelta per token chunk, one
// EventToolCall as each tool_use block completes, one EventToolResult
// per executed tool, one EventIterationEnd per model turn, and finally
// one EventDone. The channel is closed before Stream returns.
func (c *caller) Stream(ctx context.Context, req agent.CallRequest, out chan<- agent.StreamEvent) (err error) {
	defer func() {
		// Always send EventDone (or EventError) before closing.
		if err != nil {
			safeSend(out, agent.StreamEvent{Kind: agent.EventError, Delta: err.Error()})
		}
		close(out)
	}()

	loop, prepErr := c.prepareLoop(ctx, req, entrypointStream)
	if prepErr != nil {
		return prepErr
	}
	defer loop.cancel()

	var agg agent.Usage
	agg.Model = loop.model

	for loop.iter < loop.maxIter {
		loop.iter++
		msg, callErr := c.streamIteration(loop.ctx, loop.params, out)
		if callErr != nil {
			c.emitUsage(ctx, &agg)
			return fmt.Errorf("agent: model call failed: %w", callErr)
		}
		c.accumulateUsage(&agg, msg)
		loop.params.Messages = append(loop.params.Messages, msg.ToParam())

		if msg.StopReason != anthropic.StopReasonToolUse {
			usageCopy := agg
			usageCopy.IterationCount = loop.iter
			safeSend(out, agent.StreamEvent{
				Kind:       agent.EventIterationEnd,
				Usage:      &usageCopy,
				StopReason: string(msg.StopReason),
			})
			safeSend(out, agent.StreamEvent{
				Kind:       agent.EventDone,
				Usage:      &usageCopy,
				StopReason: string(msg.StopReason),
			})
			c.emitUsage(ctx, &agg)
			return nil
		}

		// Execute tools and emit results.
		toolResults, _, runErr := c.runTools(loop.ctx, msg, req.Env)
		for _, tr := range toolResults {
			if tr.OfToolResult == nil {
				continue
			}
			payload := extractToolResultText(*tr.OfToolResult)
			tres := &agent.ToolResult{
				ToolUseID: tr.OfToolResult.ToolUseID,
				Output:    payload,
				IsError:   tr.OfToolResult.IsError.Or(false),
			}
			safeSend(out, agent.StreamEvent{Kind: agent.EventToolResult, ToolResult: tres})
		}
		if len(toolResults) > 0 {
			loop.params.Messages = append(loop.params.Messages, anthropic.NewUserMessage(toolResults...))
		}

		// Iteration boundary.
		usageCopy := agg
		usageCopy.IterationCount = loop.iter
		safeSend(out, agent.StreamEvent{Kind: agent.EventIterationEnd, Usage: &usageCopy})

		if runErr != nil {
			c.logger.Printf("agent: tool errors in streaming iteration %d: %v", loop.iter, runErr)
		}
	}

	agg.IterationCount = loop.iter
	safeSend(out, agent.StreamEvent{Kind: agent.EventDone, Usage: &agg, StopReason: "max_iterations"})
	c.emitUsage(ctx, &agg)
	return agent.ErrMaxIterations
}

// streamIteration runs exactly one model turn against the streaming
// endpoint. Text arrives token-by-token and is forwarded to out as it
// lands; a tool_use block is announced on out the moment its input JSON
// is complete (at content_block_stop), which is the earliest point the
// call is safe to show. The fully accumulated Message is returned so the
// caller keeps the same post-turn handling it had with the
// non-streaming endpoint — including thinking blocks, which must be
// echoed back verbatim in the turn that carries a tool_use.
func (c *caller) streamIteration(ctx context.Context, params anthropic.MessageNewParams, out chan<- agent.StreamEvent) (*anthropic.Message, error) {
	stream := c.client.Messages.NewStreaming(ctx, params)
	msg := &anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return nil, fmt.Errorf("accumulate stream event: %w", err)
		}
		switch variant := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			// Only text reaches the console. Thinking deltas stay in the
			// accumulated message: the StreamEvent contract has no
			// thinking frame, and forwarding reasoning as assistant text
			// would put it in the persisted transcript as if the model
			// had said it.
			if delta, ok := variant.Delta.AsAny().(anthropic.TextDelta); ok && delta.Text != "" {
				safeSend(out, agent.StreamEvent{Kind: agent.EventTextDelta, Delta: delta.Text})
			}
		case anthropic.ContentBlockStopEvent:
			if len(msg.Content) == 0 {
				continue
			}
			block := msg.Content[len(msg.Content)-1]
			if block.Type != "tool_use" {
				continue
			}
			safeSend(out, agent.StreamEvent{
				Kind: agent.EventToolCall,
				ToolCall: &agent.ToolCall{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				},
			})
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return msg, nil
}

// loopState bundles the per-call mutable state.
type loopState struct {
	ctx     context.Context
	cancel  context.CancelFunc
	params  anthropic.MessageNewParams
	model   string
	maxIter int
	iter    int
}

// entrypoint distinguishes the two Caller methods for defaults that
// legitimately differ between them (notably max_tokens).
type entrypoint int

const (
	entrypointSend entrypoint = iota
	entrypointStream
)

func (c *caller) prepareLoop(ctx context.Context, req agent.CallRequest, from entrypoint) (*loopState, error) {
	if len(req.Conversation) == 0 {
		return nil, errors.New("agent: CallRequest.Conversation is empty")
	}

	model := req.Model
	if model == "" {
		model = c.defaultModel
	}

	maxTokens := c.resolveCallMaxTokens(req.MaxTokens, model, from)

	maxIter := req.MaxIterations
	if maxIter <= 0 {
		maxIter = agent.DefaultMaxIterations
	}

	deadline := req.Deadline
	if deadline <= 0 {
		deadline = agent.DefaultDeadline
	}

	// Apply the deadline as a derived context. The parent ctx still
	// wins if it expires first.
	loopCtx, cancel := context.WithTimeout(ctx, deadline)

	// Cache-control strategy (ADR-0023): the system prompt and the
	// last tool definition both carry cache_control=ephemeral so the
	// (system + tools) prefix becomes a cacheable segment that costs
	// 10x less to read on every subsequent turn in the conversation.
	systemBlocks := buildSystemBlocks(req.SystemPreamble)
	toolParams := c.registry.BuildAnthropicTools(req.AllowedTools, req.Env.AllowMutations)

	params := anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: int64(maxTokens),
		System:    systemBlocks,
		Tools:     toolParams,
		Messages:  agentMessagesToSDK(req.Conversation),
	}
	c.applyModelControls(&params, req, model)

	return &loopState{
		ctx:     loopCtx,
		cancel:  cancel,
		params:  params,
		model:   model,
		maxIter: maxIter,
	}, nil
}

// resolveCallMaxTokens layers the per-call value over the caller-level
// default over the per-entrypoint agent default, then clamps to what the
// target model will actually emit.
func (c *caller) resolveCallMaxTokens(requested int, model string, from entrypoint) int {
	maxTokens := requested
	if maxTokens <= 0 {
		maxTokens = c.defaultMaxTok
	}
	if maxTokens <= 0 {
		if from == entrypointStream {
			maxTokens = agent.DefaultStreamMaxTokens
		} else {
			maxTokens = agent.DefaultMaxTokens
		}
	}
	if ceiling := maxOutputTokens(model); maxTokens > ceiling {
		maxTokens = ceiling
	}
	if maxTokens > agent.MaxTokensCeiling {
		maxTokens = agent.MaxTokensCeiling
	}
	return maxTokens
}

// applyModelControls sets `thinking` and `output_config.effort` on the
// request, degrading gracefully on models that do not accept them. A
// model this build has never heard of gets neither knob rather than a
// 400 — the agent's job is to answer, not to be right about the shape of
// a request to a model that shipped after it did.
func (c *caller) applyModelControls(params *anthropic.MessageNewParams, req agent.CallRequest, model string) {
	thinking := req.Thinking
	if thinking == agent.ThinkingDefault || !thinking.Valid() {
		thinking = c.defaultThinking
	}
	effort := req.Effort
	if effort == agent.EffortDefault || !effort.Valid() {
		effort = c.defaultEffort
	}

	if !supportsEffort(model) {
		effort = agent.EffortDefault
	} else if effort == agent.EffortXHigh && !supportsXHighEffort(model) {
		effort = agent.EffortHigh
	}

	switch {
	case !supportsAdaptiveThinking(model):
		// Older models take the deprecated budget_tokens form, which we
		// do not emit. Leave `thinking` unset: no extended thinking.
	case thinking == agent.ThinkingAdaptive:
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		}
	case thinking == agent.ThinkingOff:
		// Disabling thinking is rejected at the top two effort levels on
		// some models; step effort down rather than fail the call.
		if thinkingOffRejectsHighEffort(model) &&
			(effort == agent.EffortXHigh || effort == agent.EffortMax) {
			effort = agent.EffortHigh
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
		}
	}

	if effort != agent.EffortDefault {
		params.OutputConfig = anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffort(effort),
		}
	}
}

// buildSystemBlocks splits the system prompt into two text blocks: the
// platform preamble (cache_control=ephemeral so it sits in the cacheable
// prefix) and the op-supplied SystemPreamble (also cacheable). The
// boundary keeps cache hits stable when the op-supplied text changes.
func buildSystemBlocks(extra string) []anthropic.TextBlockParam {
	blocks := []anthropic.TextBlockParam{
		{
			Text:         PlatformSystemPrompt,
			CacheControl: anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m},
		},
	}
	if strings.TrimSpace(extra) != "" {
		blocks = append(blocks, anthropic.TextBlockParam{
			Text:         extra + SystemPromptFooter,
			CacheControl: anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m},
		})
	} else {
		// Keep the footer text in a separate (non-cached) block so a
		// bare "no preamble" call still hits the platform cache.
		blocks = append(blocks, anthropic.TextBlockParam{Text: SystemPromptFooter})
	}
	return blocks
}

// runTools executes every tool_use block in msg in parallel and returns
// the matching tool_result content blocks (for the next user turn) plus
// an agent.Message capturing the same turn for [CallResult.Messages].
func (c *caller) runTools(ctx context.Context, msg *anthropic.Message, env agent.Env) ([]anthropic.ContentBlockParamUnion, agent.Message, error) {
	type slot struct {
		idx     int
		useID   string
		name    string
		output  json.RawMessage
		isError bool
	}

	calls := make([]slot, 0)
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			calls = append(calls, slot{useID: block.ID, name: block.Name})
		}
	}
	if len(calls) == 0 {
		return nil, agent.Message{}, nil
	}

	// Map block id -> index so we can pull the input.
	inputs := map[string]json.RawMessage{}
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			inputs[block.ID] = block.Input
		}
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	wg.Add(len(calls))
	for i := range calls {
		i := i
		go func() {
			defer wg.Done()
			calls[i].idx = i
			result, err := c.registry.Execute(ctx, calls[i].name, inputs[calls[i].useID], env)
			payload, isErr := tools.MarshalToolResult(result, err)
			calls[i].output = payload
			calls[i].isError = isErr
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	out := make([]anthropic.ContentBlockParamUnion, 0, len(calls))
	userTurn := agent.Message{Role: agent.RoleUser}
	for _, s := range calls {
		block := anthropic.ToolResultBlockParam{
			ToolUseID: s.useID,
			Content: []anthropic.ToolResultBlockParamContentUnion{
				{OfText: &anthropic.TextBlockParam{Text: string(s.output)}},
			},
		}
		if s.isError {
			block.IsError = anthropic.Bool(true)
		}
		out = append(out, anthropic.ContentBlockParamUnion{OfToolResult: &block})

		userTurn.Content = append(userTurn.Content, agent.ContentBlock{
			Kind: agent.BlockToolResult,
			ToolResult: &agent.ToolResult{
				ToolUseID: s.useID,
				Output:    s.output,
				IsError:   s.isError,
			},
		})
	}
	return out, userTurn, firstErr
}

// Helpers ====================================================================

// agentMessagesToSDK converts the seam-level Message slice into the SDK's
// MessageParam slice. Tool-result blocks become ToolResultBlockParam,
// tool-use blocks become ToolUseBlockParam, plain text becomes TextBlockParam.
func agentMessagesToSDK(in []agent.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(in))
	for _, m := range in {
		var role anthropic.MessageParamRole
		switch m.Role {
		case agent.RoleUser:
			role = anthropic.MessageParamRoleUser
		case agent.RoleAssistant:
			role = anthropic.MessageParamRoleAssistant
		default:
			continue
		}
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Kind {
			case agent.BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case agent.BlockToolUse:
				if b.ToolUse == nil {
					continue
				}
				var input any
				if len(b.ToolUse.Input) > 0 {
					var generic any
					_ = json.Unmarshal(b.ToolUse.Input, &generic)
					input = generic
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ToolUse.ID, input, b.ToolUse.Name))
			case agent.BlockToolResult:
				if b.ToolResult == nil {
					continue
				}
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolResult.ToolUseID, string(b.ToolResult.Output), b.ToolResult.IsError))
			}
		}
		out = append(out, anthropic.MessageParam{Role: role, Content: blocks})
	}
	return out
}

func assistantToAgentMessage(msg *anthropic.Message) agent.Message {
	out := agent.Message{Role: agent.RoleAssistant, ID: msg.ID}
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			out.Content = append(out.Content, agent.ContentBlock{Kind: agent.BlockText, Text: block.Text})
		case "tool_use":
			out.Content = append(out.Content, agent.ContentBlock{
				Kind: agent.BlockToolUse,
				ToolUse: &agent.ToolCall{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				},
			})
		}
	}
	return out
}

func collectFinalText(msg *anthropic.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func extractToolResultText(block anthropic.ToolResultBlockParam) json.RawMessage {
	for _, c := range block.Content {
		if c.OfText != nil {
			return json.RawMessage(c.OfText.Text)
		}
	}
	return nil
}

// safeSend writes to out without blocking forever — the caller's context
// is honored by the channel close path. If the receiver has gone away we
// drop the event rather than panic.
func safeSend(out chan<- agent.StreamEvent, ev agent.StreamEvent) {
	defer func() { _ = recover() }()
	out <- ev
}

// Pricing & telemetry ========================================================

// modelPrice is the per-million-token rate used to estimate cost.
// Numbers are rough public-list-price approximations; the emitter is the
// authoritative source for production accounting.
type modelPrice struct {
	InputPer1M      float64
	OutputPer1M     float64
	CacheWritePer1M float64
	CacheReadPer1M  float64
}

func defaultPricing() map[string]modelPrice {
	// Public first-party list prices (per million tokens). Cache writes are
	// 1.25x input and cache reads 0.1x input on every model here.
	return map[string]modelPrice{
		"claude-fable-5-1":  {InputPer1M: 10, OutputPer1M: 50, CacheWritePer1M: 12.5, CacheReadPer1M: 1.00},
		"claude-fable-5":    {InputPer1M: 10, OutputPer1M: 50, CacheWritePer1M: 12.5, CacheReadPer1M: 1.00},
		"claude-opus-5":     {InputPer1M: 5, OutputPer1M: 25, CacheWritePer1M: 6.25, CacheReadPer1M: 0.50},
		"claude-opus-4-8":   {InputPer1M: 5, OutputPer1M: 25, CacheWritePer1M: 6.25, CacheReadPer1M: 0.50},
		"claude-opus-4-7":   {InputPer1M: 5, OutputPer1M: 25, CacheWritePer1M: 6.25, CacheReadPer1M: 0.50},
		"claude-opus-4-6":   {InputPer1M: 5, OutputPer1M: 25, CacheWritePer1M: 6.25, CacheReadPer1M: 0.50},
		"claude-sonnet-5":   {InputPer1M: 2, OutputPer1M: 10, CacheWritePer1M: 2.50, CacheReadPer1M: 0.20},
		"claude-sonnet-4-6": {InputPer1M: 3, OutputPer1M: 15, CacheWritePer1M: 3.75, CacheReadPer1M: 0.30},
		"claude-sonnet-4-5": {InputPer1M: 3, OutputPer1M: 15, CacheWritePer1M: 3.75, CacheReadPer1M: 0.30},
		"claude-haiku-4-5":  {InputPer1M: 1, OutputPer1M: 5, CacheWritePer1M: 1.25, CacheReadPer1M: 0.10},
	}
}

func (c *caller) accumulateUsage(agg *agent.Usage, msg *anthropic.Message) {
	if agg.Model == "" {
		agg.Model = string(msg.Model)
	}
	agg.InputTokens += msg.Usage.InputTokens
	agg.OutputTokens += msg.Usage.OutputTokens
	agg.CacheCreationTokens += msg.Usage.CacheCreationInputTokens
	agg.CacheReadTokens += msg.Usage.CacheReadInputTokens
	agg.IterationCount++

	price, ok := c.pricing[string(msg.Model)]
	if !ok {
		return
	}
	agg.CostUSDEstimated += float64(msg.Usage.InputTokens)/1_000_000*price.InputPer1M +
		float64(msg.Usage.OutputTokens)/1_000_000*price.OutputPer1M +
		float64(msg.Usage.CacheCreationInputTokens)/1_000_000*price.CacheWritePer1M +
		float64(msg.Usage.CacheReadInputTokens)/1_000_000*price.CacheReadPer1M
}

func (c *caller) emitUsage(ctx context.Context, u *agent.Usage) {
	if c.metrics == nil || u == nil {
		return
	}
	c.metrics.EmitUsage(ctx, *u)
}

// logMetricsEmitter is the default emitter — writes structured-ish lines
// to a stdlib logger. Production deployments override this with a
// telemetry-pipe emitter.
type logMetricsEmitter struct {
	logger *log.Logger
}

func newLogMetricsEmitter(l *log.Logger) agent.MetricsEmitter {
	return &logMetricsEmitter{logger: l}
}

func (e *logMetricsEmitter) EmitUsage(_ context.Context, u agent.Usage) {
	e.logger.Printf("agent.usage model=%s iters=%d input=%d output=%d cache_create=%d cache_read=%d cost_usd=%.6f",
		u.Model, u.IterationCount, u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens, u.CostUSDEstimated)
}

// Assert at compile time that we satisfy the seam.
var _ agent.Caller = (*caller)(nil)
