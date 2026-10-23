// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/idempotency"
)

// fakeCaller is a tiny agent.Caller that scripts a deterministic stream
// for the chat-backend wire-surface tests. Each test case constructs
// one with the events it wants to replay.
//
// Channel discipline: Stream closes `out` before returning so the
// Service's `for ev := range events` loop terminates cleanly — that is
// the contract documented on agent.Caller.
type fakeCaller struct {
	mu     sync.Mutex
	script []agent.StreamEvent
	// streamErr is returned by Stream after every scripted event has
	// been delivered. nil mimics a clean end-of-loop.
	streamErr error
	// sendCalls counts how many times Send/Stream were invoked.
	streamCalls int
}

func (c *fakeCaller) Send(ctx context.Context, req agent.CallRequest) (agent.CallResult, error) {
	return agent.CallResult{}, errors.New("fakeCaller.Send: not used in these tests")
}

func (c *fakeCaller) Stream(ctx context.Context, req agent.CallRequest, out chan<- agent.StreamEvent) error {
	c.mu.Lock()
	c.streamCalls++
	script := append([]agent.StreamEvent(nil), c.script...)
	streamErr := c.streamErr
	c.mu.Unlock()

	defer close(out)
	for _, ev := range script {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- ev:
		}
	}
	return streamErr
}

// denyAuthorizer returns an error for the configured actor + action
// combo and allows everything else. Used to lock down the 403 path.
type denyAuthorizer struct {
	actor  string
	action string
}

func (d denyAuthorizer) Authorize(ctx context.Context, scope conversation.Scope) error {
	if scope.ActorID == d.actor && scope.Action == d.action {
		return errors.New("forbidden")
	}
	return nil
}

// helloWorldCaller is the canonical happy-path script: "Hello "/"world"
// text deltas, then a done event with usage.
func helloWorldCaller() *fakeCaller {
	return &fakeCaller{
		script: []agent.StreamEvent{
			{Kind: agent.EventTextDelta, Delta: "Hello "},
			{Kind: agent.EventTextDelta, Delta: "world"},
			{Kind: agent.EventDone, Usage: &agent.Usage{InputTokens: 12, OutputTokens: 4}, StopReason: "end_turn"},
		},
	}
}

// startServer wires a NewRouterWithDataStack with the supplied store +
// caller and returns the httptest.Server. The caller is responsible
// for Close().
func startServer(t *testing.T, stack DataStack) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewRouterWithDataStack(stack))
	t.Cleanup(srv.Close)
	return srv
}

// doJSON issues an HTTP request and returns (status, body bytes). It
// fails the test on transport errors.
func doJSON(t *testing.T, method, url string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, raw
}

// sseFrame is a parsed SSE event-stream frame ("event:" + "data:").
type sseFrame struct {
	Event string
	Data  string
}

type writeHeaderHookRecorder struct {
	*httptest.ResponseRecorder
	hook func()
}

func (recorder *writeHeaderHookRecorder) WriteHeader(status int) {
	recorder.ResponseRecorder.WriteHeader(status)
	hook := recorder.hook
	recorder.hook = nil
	if hook != nil {
		hook()
	}
}

// readSSEFrames scans the supplied reader and emits frames into out.
// Each frame is bounded by a blank line; comment lines (starting with
// ':') and trailing lines after the data: are ignored for these tests.
// readSSEFrames stops when r returns EOF or the supplied done channel
// closes.
func readSSEFrames(r io.Reader, out chan<- sseFrame, done <-chan struct{}) {
	defer close(out)
	scanner := bufio.NewScanner(r)
	// Allow generous buffer for JSON payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var current sseFrame
	for scanner.Scan() {
		select {
		case <-done:
			return
		default:
		}
		line := scanner.Text()
		switch {
		case line == "":
			// End of frame.
			if current.Event != "" || current.Data != "" {
				select {
				case out <- current:
				case <-done:
					return
				}
			}
			current = sseFrame{}
		case strings.HasPrefix(line, ":"):
			// Heartbeat / comment — ignore.
		case strings.HasPrefix(line, "event:"):
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(payload, " ") {
				payload = payload[1:]
			}
			if current.Data == "" {
				current.Data = payload
			} else {
				current.Data += "\n" + payload
			}
		}
	}
}

// waitFrame consumes a single frame with a 2-second guard.
func waitFrame(t *testing.T, ch <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case frame, ok := <-ch:
		if !ok {
			t.Fatalf("sse stream closed before a frame arrived")
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for sse frame")
	}
	return sseFrame{}
}

func TestAgentConversationsCreateGetList(t *testing.T) {
	t.Parallel()
	store := conversation.NewMemoryStore(nil)
	idemStore := idempotency.NewMemoryStore()
	srv := startServer(t, DataStack{
		ConversationStore: store,
		IdempotencyStore:  idemStore,
		AgentCaller:       helloWorldCaller(),
	})

	createBody := map[string]any{
		"title":      "first chat",
		"project_id": "prj_demo",
	}

	status, raw := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix, createBody, map[string]string{
		"Idempotency-Key": "00000000-0000-4000-8000-000000000001",
	})
	if status != http.StatusCreated {
		t.Fatalf("POST /conversations status = %d body=%s", status, raw)
	}
	var created conversation.Conversation
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode create: %v body=%s", err, raw)
	}
	if !strings.HasPrefix(created.ID, conversation.ConversationIDPrefix+"_") {
		t.Fatalf("conversation id = %q, want conv_* prefix", created.ID)
	}
	if created.Title != "first chat" || created.ProjectID != "prj_demo" {
		t.Fatalf("conversation = %#v, missing title/project", created)
	}

	// Idempotency replay: same key returns the same id.
	status2, raw2 := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix, createBody, map[string]string{
		"Idempotency-Key": "00000000-0000-4000-8000-000000000001",
	})
	if status2 != http.StatusCreated {
		t.Fatalf("idempotent replay status = %d body=%s", status2, raw2)
	}
	var replayed conversation.Conversation
	if err := json.Unmarshal(raw2, &replayed); err != nil {
		t.Fatalf("decode replay: %v body=%s", err, raw2)
	}
	if replayed.ID != created.ID {
		t.Fatalf("replay id = %q, want %q", replayed.ID, created.ID)
	}
	replayBody, err := json.Marshal(createBody)
	if err != nil {
		t.Fatal(err)
	}
	replayReq, err := http.NewRequest(http.MethodPost, srv.URL+AgentConversationsPrefix, bytes.NewReader(replayBody))
	if err != nil {
		t.Fatal(err)
	}
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000001")
	replayResponse, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	defer replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusCreated ||
		replayResponse.Header.Get("Idempotent-Replayed") != "true" ||
		replayResponse.Header.Get("Location") != AgentConversationsPrefix+"/"+created.ID {
		t.Fatalf("replay transport = %d/%q/%q, want 201/true/%q", replayResponse.StatusCode, replayResponse.Header.Get("Idempotent-Replayed"), replayResponse.Header.Get("Location"), AgentConversationsPrefix+"/"+created.ID)
	}

	// GET /conversations/{id}.
	status, raw = doJSON(t, http.MethodGet, srv.URL+AgentConversationsPrefix+"/"+created.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /conversations/{id} = %d body=%s", status, raw)
	}
	var fetched conversation.Conversation
	if err := json.Unmarshal(raw, &fetched); err != nil {
		t.Fatalf("decode get: %v body=%s", err, raw)
	}
	if fetched.ID != created.ID || fetched.Title != created.Title {
		t.Fatalf("fetched = %#v, want id+title to match %#v", fetched, created)
	}

	// LIST /conversations.
	status, raw = doJSON(t, http.MethodGet, srv.URL+AgentConversationsPrefix, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /conversations = %d body=%s", status, raw)
	}
	var page conversationsPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode list: %v body=%s", err, raw)
	}
	found := false
	for _, c := range page.Data {
		if c.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("list missing %q: %#v", created.ID, page.Data)
	}
}

func TestAgentConversationSendAndStream(t *testing.T) {
	t.Parallel()
	store := conversation.NewMemoryStore(nil)
	caller := helloWorldCaller()
	srv := startServer(t, DataStack{
		ConversationStore: store,
		AgentCaller:       caller,
	})

	// Start a conversation.
	status, raw := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix, map[string]any{"title": "hello"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create conversation status=%d body=%s", status, raw)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(raw, &conv); err != nil {
		t.Fatalf("decode conv: %v", err)
	}

	// Open the SSE stream BEFORE sending so we capture every frame.
	streamURL := srv.URL + AgentConversationsPrefix + "/" + conv.ID + "/stream"
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("stream req: %v", err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("stream do: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("stream status = %d body=%s", streamResp.StatusCode, body)
	}
	if got := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("stream content-type = %q, want text/event-stream", got)
	}

	frames := make(chan sseFrame, 8)
	readerDone := make(chan struct{})
	go readSSEFrames(streamResp.Body, frames, readerDone)
	defer close(readerDone)

	// Fire the POST /messages in parallel with the stream reader. The
	// handler blocks until Send returns, which only happens after the
	// fake caller has pushed every scripted event.
	sendDone := make(chan struct {
		status int
		body   []byte
	}, 1)
	go func() {
		status, body := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix+"/"+conv.ID+"/messages",
			map[string]any{"content": "say hi"}, nil)
		sendDone <- struct {
			status int
			body   []byte
		}{status, body}
	}()

	// Expect: delta "Hello ", delta "world", done {stop_reason:"end_turn"}.
	first := waitFrame(t, frames)
	if first.Event != "text_delta" {
		t.Fatalf("frame[0] event = %q, want %q", first.Event, "text_delta")
	}
	if !strings.Contains(first.Data, `"delta":"Hello "`) {
		t.Fatalf("frame[0] data missing delta=Hello : %s", first.Data)
	}

	second := waitFrame(t, frames)
	if second.Event != "text_delta" {
		t.Fatalf("frame[1] event = %q, want %q", second.Event, "text_delta")
	}
	if !strings.Contains(second.Data, `"delta":"world"`) {
		t.Fatalf("frame[1] data missing delta=world: %s", second.Data)
	}

	third := waitFrame(t, frames)
	if third.Event != "done" {
		t.Fatalf("frame[2] event = %q, want %q", third.Event, "done")
	}
	if !strings.Contains(third.Data, `"stop_reason":"end_turn"`) {
		t.Fatalf("frame[2] data missing stop_reason=end_turn: %s", third.Data)
	}

	// After done, the stream closes. The readSSEFrames goroutine
	// finishes once the body EOFs; closing frames signals end-of-stream.
	select {
	case _, open := <-frames:
		if open {
			// A late heartbeat / trailing frame is acceptable, but the
			// channel MUST close shortly after.
			select {
			case _, stillOpen := <-frames:
				if stillOpen {
					t.Fatalf("expected sse stream to close after done, but more frames keep arriving")
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("sse stream did not close within 2s after done")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("sse frames channel did not close within 2s after done")
	}

	// Wait for POST /messages to return.
	var sendResult struct {
		status int
		body   []byte
	}
	select {
	case sendResult = <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("POST /messages did not return within 2s")
	}
	if sendResult.status != http.StatusOK {
		t.Fatalf("POST /messages status = %d body=%s", sendResult.status, sendResult.body)
	}

	// GET /messages — expect user + assistant message.
	status, raw = doJSON(t, http.MethodGet, srv.URL+AgentConversationsPrefix+"/"+conv.ID+"/messages", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /messages status = %d body=%s", status, raw)
	}
	var messages messagesPage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatalf("decode messages: %v body=%s", err, raw)
	}
	if len(messages.Data) != 2 {
		t.Fatalf("messages = %d, want 2: %s", len(messages.Data), raw)
	}
	if messages.Data[0].Role != agent.RoleUser {
		t.Fatalf("messages[0].role = %q, want user", messages.Data[0].Role)
	}
	assistant := messages.Data[1]
	if assistant.Role != agent.RoleAssistant {
		t.Fatalf("messages[1].role = %q, want assistant", assistant.Role)
	}
	if len(assistant.Content) == 0 {
		t.Fatalf("assistant message has no content: %#v", assistant)
	}
	if assistant.Content[0].Kind != agent.BlockText {
		t.Fatalf("assistant content[0].kind = %q, want %q", assistant.Content[0].Kind, agent.BlockText)
	}
	if assistant.Content[0].Text != "Hello world" {
		t.Fatalf("assistant content[0].text = %q, want %q", assistant.Content[0].Text, "Hello world")
	}
	if assistant.Usage == nil {
		t.Fatalf("assistant usage is nil; want input_tokens=12")
	}
	if assistant.Usage.InputTokens != 12 {
		t.Fatalf("assistant usage.input_tokens = %d, want 12", assistant.Usage.InputTokens)
	}
	if assistant.Usage.OutputTokens != 4 {
		t.Fatalf("assistant usage.output_tokens = %d, want 4", assistant.Usage.OutputTokens)
	}

	caller.mu.Lock()
	calls := caller.streamCalls
	caller.mu.Unlock()
	if calls != 1 {
		t.Fatalf("caller.Stream invocations = %d, want 1", calls)
	}
}

func TestAgentStreamSubscribesBeforeSuccessfulHeadersAreObservable(t *testing.T) {
	store := conversation.NewMemoryStore(nil)
	service, err := conversation.NewService(conversation.Config{
		Store:  store,
		Caller: helloWorldCaller(),
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := service.Start(context.Background(), conversation.StartInput{
		Title:   "ready handshake",
		ActorID: "user:test",
	})
	if err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(
		http.MethodGet,
		AgentConversationsPrefix+"/"+conv.ID+"/stream",
		nil,
	).WithContext(requestContext)
	deps := AgentConversationHandlerDeps{
		Service: service,
		PrincipalFromRequest: func(*http.Request) string {
			return "user:test"
		},
	}
	var sendErr error
	recorder := &writeHeaderHookRecorder{ResponseRecorder: httptest.NewRecorder()}
	recorder.hook = func() {
		_, sendErr = service.Send(context.Background(), conversation.SendInput{
			ConversationID: conv.ID,
			ActorID:        "user:test",
			Content:        "publish as soon as headers are visible",
		}, nil)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		streamConversation(recorder, request, deps, conv.ID)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("stream did not observe the turn published at header readiness")
	}
	if sendErr != nil {
		t.Fatalf("send at header readiness: %v", sendErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `event: text_delta`) ||
		!strings.Contains(body, `"delta":"Hello "`) ||
		!strings.Contains(body, `"delta":"world"`) ||
		!strings.Contains(body, `event: done`) {
		t.Fatalf("stream missed events published at header readiness: %s", body)
	}
}

func TestAgentConversationSendCallerNotConfigured(t *testing.T) {
	t.Parallel()
	store := conversation.NewMemoryStore(nil)
	srv := startServer(t, DataStack{
		ConversationStore: store,
		// AgentCaller intentionally nil.
	})

	// Create a conversation so we have a valid id to POST to.
	status, raw := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix, map[string]any{"title": "no caller"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", status, raw)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(raw, &conv); err != nil {
		t.Fatalf("decode: %v", err)
	}

	status, raw = doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix+"/"+conv.ID+"/messages",
		map[string]any{"content": "ping"}, nil)
	if status < 400 {
		t.Fatalf("POST /messages with nil caller status = %d body=%s, want 4xx/5xx", status, raw)
	}
	// Lock the contract: the handler emits the underlying error in
	// details.reason, which mentions "caller is not configured" from
	// the Service.Send guard. We assert the substring rather than the
	// exact failure code so handler refactors don't break the test.
	if !strings.Contains(string(raw), "caller is not configured") {
		t.Fatalf("POST /messages body does not mention configuration gap: %s", raw)
	}
}

func TestAgentConversationNotFoundPaths(t *testing.T) {
	t.Parallel()
	store := conversation.NewMemoryStore(nil)
	srv := startServer(t, DataStack{
		ConversationStore: store,
		AgentCaller:       helloWorldCaller(),
	})

	missing := "conv_" + strings.Repeat("0", 20)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"GET conversation", http.MethodGet, AgentConversationsPrefix + "/" + missing, nil},
		// DELETE on a missing conversation is intentionally idempotent (returns 204),
		// not 404 — Midnight follows the "the resource is now absent" REST convention.
		{"POST messages", http.MethodPost, AgentConversationsPrefix + "/" + missing + "/messages", map[string]any{"content": "hi"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			status, raw := doJSON(t, tc.method, srv.URL+tc.path, tc.body, nil)
			if status != http.StatusNotFound {
				t.Fatalf("%s %s status = %d body=%s, want 404", tc.method, tc.path, status, raw)
			}
		})
	}
}

func TestAgentConversationAuthorizationDeniesSend(t *testing.T) {
	t.Parallel()
	store := conversation.NewMemoryStore(nil)
	srv := startServer(t, DataStack{
		ConversationStore:           store,
		AgentCaller:                 helloWorldCaller(),
		ConversationScopeAuthorizer: denyAuthorizer{actor: "alice", action: "send"},
		AgentPrincipalFromRequest: func(r *http.Request) string {
			return r.Header.Get("X-Midnight-Actor-Id")
		},
	})

	// Start a conversation (the authorizer allows "create" through).
	status, raw := doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix, map[string]any{"title": "scoped"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", status, raw)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(raw, &conv); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// POST /messages as alice → forbidden.
	status, raw = doJSON(t, http.MethodPost, srv.URL+AgentConversationsPrefix+"/"+conv.ID+"/messages",
		map[string]any{"content": "denied"}, map[string]string{"X-Midnight-Actor-Id": "alice"})
	if status != http.StatusForbidden {
		t.Fatalf("POST /messages as alice status = %d body=%s, want 403", status, raw)
	}
}

// compile-time check: agent.Caller is satisfied by fakeCaller.
var _ agent.Caller = (*fakeCaller)(nil)

// silence unused-import linters for fmt in case future edits drop it.
var _ = fmt.Sprintf
