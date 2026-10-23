// Package openai wires the OpenAI Responses API to the platform
// [agent.Caller] seam.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/claude"
	"github.com/midnight/midnight/internal/agent/tools"
)

const (
	EnvAPIKey    = "OPENAI_API_KEY"
	EnvBaseURL   = "MIDNIGHT_OPENAI_BASE_URL"
	EnvChatModel = "MIDNIGHT_OPENAI_CHAT_MODEL"

	DefaultBaseURL   = "https://api.openai.com/v1"
	DefaultChatModel = "gpt-5.1"
)

// Options configures [NewCaller]. The zero value is enough for production
// use as long as OPENAI_API_KEY is set in the environment.
type Options struct {
	APIKey       string
	BaseURL      string
	HTTPClient   *http.Client
	Registry     *tools.Registry
	DefaultModel string
	Metrics      agent.MetricsEmitter
	Logger       *log.Logger
}

// NewCaller constructs an [agent.Caller] backed by the OpenAI Responses API.
func NewCaller(opts Options) (agent.Caller, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(EnvAPIKey)
	}
	if apiKey == "" {
		return nil, agent.AgentAPIKeyMissing
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv(EnvBaseURL)
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

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

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	metrics := opts.Metrics
	if metrics == nil {
		metrics = agent.MetricsEmitterFunc(func(context.Context, agent.Usage) {})
	}

	return &caller{
		apiKey:       apiKey,
		baseURL:      strings.TrimRight(baseURL, "/"),
		httpClient:   httpClient,
		registry:     registry,
		defaultModel: defaultModel,
		metrics:      metrics,
		logger:       logger,
	}, nil
}

type caller struct {
	apiKey       string
	baseURL      string
	httpClient   *http.Client
	registry     *tools.Registry
	defaultModel string
	metrics      agent.MetricsEmitter
	logger       *log.Logger
}

type loopState struct {
	ctx          context.Context
	cancel       context.CancelFunc
	instructions string
	input        []any
	tools        []responseTool
	model        string
	maxTokens    int
	maxIter      int
	iter         int
}

func (c *caller) Send(ctx context.Context, req agent.CallRequest) (agent.CallResult, error) {
	loop, err := c.prepareLoop(ctx, req)
	if err != nil {
		return agent.CallResult{}, err
	}
	defer loop.cancel()

	var result agent.CallResult
	result.Usage.Model = loop.model

	for loop.iter < loop.maxIter {
		loop.iter++
		resp, err := c.createResponse(loop.ctx, loop)
		if err != nil {
			c.emitUsage(ctx, &result.Usage)
			return result, err
		}
		c.accumulateUsage(&result.Usage, resp)

		assistant, calls, finalText := responseToAgentMessage(resp)
		if len(assistant.Content) > 0 {
			result.Messages = append(result.Messages, assistant)
		}
		loop.input = appendRawItems(loop.input, resp.Output)

		if len(calls) == 0 {
			result.FinalText = finalText
			result.StopReason = stopReason(resp)
			result.Iterations = loop.iter
			c.emitUsage(ctx, &result.Usage)
			return result, nil
		}

		toolOutputs, userTurn, runErr := c.runTools(loop.ctx, calls, req.Env)
		if len(userTurn.Content) > 0 {
			result.Messages = append(result.Messages, userTurn)
		}
		for _, output := range toolOutputs {
			loop.input = append(loop.input, output)
		}
		if runErr != nil {
			c.logger.Printf("agent: OpenAI tool errors in iteration %d: %v", loop.iter, runErr)
		}
	}

	result.Iterations = loop.iter
	result.StopReason = "max_iterations"
	c.emitUsage(ctx, &result.Usage)
	return result, agent.ErrMaxIterations
}

func (c *caller) Stream(ctx context.Context, req agent.CallRequest, out chan<- agent.StreamEvent) (err error) {
	defer func() {
		if err != nil {
			safeSend(out, agent.StreamEvent{Kind: agent.EventError, Delta: err.Error()})
		}
		close(out)
	}()

	loop, prepErr := c.prepareLoop(ctx, req)
	if prepErr != nil {
		return prepErr
	}
	defer loop.cancel()

	var agg agent.Usage
	agg.Model = loop.model

	for loop.iter < loop.maxIter {
		loop.iter++
		resp, callErr := c.createResponse(loop.ctx, loop)
		if callErr != nil {
			c.emitUsage(ctx, &agg)
			return callErr
		}
		c.accumulateUsage(&agg, resp)

		assistant, calls, _ := responseToAgentMessage(resp)
		for _, block := range assistant.Content {
			switch block.Kind {
			case agent.BlockText:
				if block.Text != "" {
					safeSend(out, agent.StreamEvent{Kind: agent.EventTextDelta, Delta: block.Text})
				}
			case agent.BlockToolUse:
				if block.ToolUse != nil {
					safeSend(out, agent.StreamEvent{Kind: agent.EventToolCall, ToolCall: block.ToolUse})
				}
			}
		}
		loop.input = appendRawItems(loop.input, resp.Output)

		if len(calls) == 0 {
			usageCopy := agg
			usageCopy.IterationCount = loop.iter
			reason := stopReason(resp)
			safeSend(out, agent.StreamEvent{Kind: agent.EventIterationEnd, Usage: &usageCopy, StopReason: reason})
			safeSend(out, agent.StreamEvent{Kind: agent.EventDone, Usage: &usageCopy, StopReason: reason})
			c.emitUsage(ctx, &agg)
			return nil
		}

		toolOutputs, _, runErr := c.runTools(loop.ctx, calls, req.Env)
		for _, output := range toolOutputs {
			tres := output.toToolResult()
			safeSend(out, agent.StreamEvent{Kind: agent.EventToolResult, ToolResult: &tres})
			loop.input = append(loop.input, output)
		}

		usageCopy := agg
		usageCopy.IterationCount = loop.iter
		safeSend(out, agent.StreamEvent{Kind: agent.EventIterationEnd, Usage: &usageCopy})

		if runErr != nil {
			c.logger.Printf("agent: OpenAI tool errors in streaming iteration %d: %v", loop.iter, runErr)
		}
	}

	agg.IterationCount = loop.iter
	safeSend(out, agent.StreamEvent{Kind: agent.EventDone, Usage: &agg, StopReason: "max_iterations"})
	c.emitUsage(ctx, &agg)
	return agent.ErrMaxIterations
}

func (c *caller) prepareLoop(ctx context.Context, req agent.CallRequest) (*loopState, error) {
	if len(req.Conversation) == 0 {
		return nil, errors.New("agent: CallRequest.Conversation is empty")
	}

	model := req.Model
	if model == "" {
		model = c.defaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = agent.DefaultMaxTokens
	}
	maxIter := req.MaxIterations
	if maxIter <= 0 {
		maxIter = agent.DefaultMaxIterations
	}
	deadline := req.Deadline
	if deadline <= 0 {
		deadline = agent.DefaultDeadline
	}

	loopCtx, cancel := context.WithTimeout(ctx, deadline)
	return &loopState{
		ctx:          loopCtx,
		cancel:       cancel,
		instructions: buildInstructions(req.SystemPreamble),
		input:        agentMessagesToResponseInput(req.Conversation),
		tools:        c.buildTools(req.AllowedTools, req.Env.AllowMutations),
		model:        model,
		maxTokens:    maxTokens,
		maxIter:      maxIter,
	}, nil
}

func buildInstructions(extra string) string {
	if strings.TrimSpace(extra) == "" {
		return claude.PlatformSystemPrompt + claude.SystemPromptFooter
	}
	return claude.PlatformSystemPrompt + "\n\n" + extra + claude.SystemPromptFooter
}

func (c *caller) buildTools(allowed []string, includeMutations bool) []responseTool {
	visible := c.registry.Visible(allowed, includeMutations)
	out := make([]responseTool, 0, len(visible))
	for _, t := range visible {
		schema := json.RawMessage(`{"type":"object","properties":{}}`)
		if json.Valid(t.InputSchema) {
			schema = append(json.RawMessage(nil), t.InputSchema...)
		}
		out = append(out, responseTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}
	return out
}

type responseRequest struct {
	Model           string         `json:"model"`
	Instructions    string         `json:"instructions,omitempty"`
	Input           []any          `json:"input"`
	Tools           []responseTool `json:"tools,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Store           bool           `json:"store"`
}

type responseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responseObject struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Model  string            `json:"model"`
	Output []json.RawMessage `json:"output"`
	Usage  responseUsage     `json:"usage"`
	Error  *responseError    `json:"error,omitempty"`
}

type responseError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type responseUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (c *caller) createResponse(ctx context.Context, loop *loopState) (responseObject, error) {
	payload := responseRequest{
		Model:           loop.model,
		Instructions:    loop.instructions,
		Input:           loop.input,
		Tools:           loop.tools,
		MaxOutputTokens: loop.maxTokens,
		Store:           false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return responseObject{}, fmt.Errorf("agent: marshal OpenAI response request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return responseObject{}, fmt.Errorf("agent: build OpenAI response request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return responseObject{}, fmt.Errorf("agent: OpenAI response call failed: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return responseObject{}, fmt.Errorf("agent: read OpenAI response: %w", err)
	}
	var decoded responseObject
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return responseObject{}, fmt.Errorf("agent: decode OpenAI response status=%d: %w", res.StatusCode, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if decoded.Error != nil && decoded.Error.Message != "" {
			return decoded, fmt.Errorf("agent: OpenAI response status=%d: %s", res.StatusCode, decoded.Error.Message)
		}
		return decoded, fmt.Errorf("agent: OpenAI response status=%d", res.StatusCode)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return decoded, fmt.Errorf("agent: OpenAI response error: %s", decoded.Error.Message)
	}
	return decoded, nil
}

type responseOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Status    string `json:"status,omitempty"`
	Role      string `json:"role,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content,omitempty"`
}

type responseToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

func responseToAgentMessage(resp responseObject) (agent.Message, []responseToolCall, string) {
	assistant := agent.Message{Role: agent.RoleAssistant, ID: resp.ID}
	var calls []responseToolCall
	var final strings.Builder

	for _, raw := range resp.Output {
		var item responseOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Text == "" {
					continue
				}
				if content.Type == "output_text" || content.Type == "text" {
					assistant.Content = append(assistant.Content, agent.ContentBlock{
						Kind: agent.BlockText,
						Text: content.Text,
					})
					if final.Len() > 0 {
						final.WriteString("\n")
					}
					final.WriteString(content.Text)
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			args := normalizeArguments(item.Arguments)
			call := responseToolCall{ID: id, Name: item.Name, Arguments: args}
			calls = append(calls, call)
			assistant.Content = append(assistant.Content, agent.ContentBlock{
				Kind: agent.BlockToolUse,
				ToolUse: &agent.ToolCall{
					ID:    call.ID,
					Name:  call.Name,
					Input: call.Arguments,
				},
			})
		}
	}

	return assistant, calls, final.String()
}

func normalizeArguments(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(map[string]string{"_raw": s})
	return b
}

type functionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
	isErr  bool
}

func (o functionCallOutput) toToolResult() agent.ToolResult {
	return agent.ToolResult{
		ToolUseID: o.CallID,
		Output:    json.RawMessage(o.Output),
		IsError:   o.isErr,
	}
}

func (c *caller) runTools(ctx context.Context, calls []responseToolCall, env agent.Env) ([]functionCallOutput, agent.Message, error) {
	if len(calls) == 0 {
		return nil, agent.Message{}, nil
	}

	out := make([]functionCallOutput, len(calls))
	userTurn := agent.Message{Role: agent.RoleUser}
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
			result, err := c.registry.Execute(ctx, calls[i].Name, calls[i].Arguments, env)
			payload, isErr := tools.MarshalToolResult(result, err)
			out[i] = functionCallOutput{
				Type:   "function_call_output",
				CallID: calls[i].ID,
				Output: string(payload),
				isErr:  isErr,
			}
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

	for _, output := range out {
		userTurn.Content = append(userTurn.Content, agent.ContentBlock{
			Kind: agent.BlockToolResult,
			ToolResult: &agent.ToolResult{
				ToolUseID: output.CallID,
				Output:    json.RawMessage(output.Output),
				IsError:   output.isErr,
			},
		})
	}
	return out, userTurn, firstErr
}

func agentMessagesToResponseInput(messages []agent.Message) []any {
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		var text strings.Builder
		for _, block := range msg.Content {
			if block.Kind == agent.BlockText && block.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			}
		}
		if text.Len() > 0 {
			role := string(msg.Role)
			out = append(out, map[string]any{
				"role":    role,
				"content": text.String(),
			})
		}
		for _, block := range msg.Content {
			switch block.Kind {
			case agent.BlockToolUse:
				if block.ToolUse == nil {
					continue
				}
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   block.ToolUse.ID,
					"name":      block.ToolUse.Name,
					"arguments": string(block.ToolUse.Input),
				})
			case agent.BlockToolResult:
				if block.ToolResult == nil {
					continue
				}
				out = append(out, map[string]any{
					"type":    "function_call_output",
					"call_id": block.ToolResult.ToolUseID,
					"output":  string(block.ToolResult.Output),
				})
			}
		}
	}
	return out
}

func appendRawItems(in []any, rawItems []json.RawMessage) []any {
	for _, raw := range rawItems {
		item := append(json.RawMessage(nil), raw...)
		in = append(in, item)
	}
	return in
}

func stopReason(resp responseObject) string {
	if resp.Status != "" {
		return resp.Status
	}
	return "completed"
}

func (c *caller) accumulateUsage(agg *agent.Usage, resp responseObject) {
	if agg.Model == "" {
		agg.Model = resp.Model
	}
	agg.InputTokens += resp.Usage.InputTokens
	agg.OutputTokens += resp.Usage.OutputTokens
	agg.CacheReadTokens += resp.Usage.InputTokensDetails.CachedTokens
	agg.IterationCount++
}

func (c *caller) emitUsage(ctx context.Context, u *agent.Usage) {
	if c.metrics == nil || u == nil {
		return
	}
	c.metrics.EmitUsage(ctx, *u)
}

func safeSend(out chan<- agent.StreamEvent, ev agent.StreamEvent) {
	defer func() { _ = recover() }()
	out <- ev
}

var _ agent.Caller = (*caller)(nil)
