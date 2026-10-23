// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestOperationStreamHubFanOutDeliversToAllSubscribers(t *testing.T) {
	hub := newOperationStreamHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subA := hub.Subscribe(ctx, "op-1", 4)
	subB := hub.Subscribe(ctx, "op-1", 4)

	hub.Publish(OperationEvent{OperationID: "op-1", Seq: 1, State: orailtypes.OperationQueued})
	hub.Publish(OperationEvent{OperationID: "op-1", Seq: 2, State: orailtypes.OperationRunning})

	for _, sub := range []*operationStreamSubscriber{subA, subB} {
		select {
		case event := <-sub.Events():
			if event.Seq != 1 {
				t.Fatalf("first event seq = %d, want 1", event.Seq)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for first event")
		}
		select {
		case event := <-sub.Events():
			if event.Seq != 2 {
				t.Fatalf("second event seq = %d, want 2", event.Seq)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for second event")
		}
	}
}

func TestOperationStreamHubBackpressureDisconnectsSlowSubscriber(t *testing.T) {
	hub := newOperationStreamHub()
	subscriber := hub.Subscribe(nil, "op-1", 2)
	defer hub.Unsubscribe("op-1", subscriber)

	for seq := 1; seq <= 3; seq++ {
		hub.Publish(OperationEvent{OperationID: "op-1", Seq: seq, State: orailtypes.OperationRunning})
	}

	collected := make([]int, 0, 2)
	for event := range subscriber.Events() {
		collected = append(collected, event.Seq)
	}
	if len(collected) != 2 || collected[0] != 1 || collected[1] != 2 {
		t.Fatalf("collected events = %v, want buffered contiguous prefix [1 2]", collected)
	}
	if !subscriber.Overflowed() {
		t.Fatal("expected subscriber to report overflow")
	}
	if dropped := subscriber.DroppedCount(); dropped != 1 {
		t.Fatalf("dropped count = %d, want 1 rejected event", dropped)
	}

	hub.mu.Lock()
	remaining := len(hub.subscribers["op-1"])
	hub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("hub retained %d slow subscribers, want 0", remaining)
	}
}

func TestOperationStreamHubClosedSubscriberCannotReceiveMoreEvents(t *testing.T) {
	hub := newOperationStreamHub()
	subscriber := hub.Subscribe(nil, "op-1", 1)
	hub.Publish(OperationEvent{OperationID: "op-1", Seq: 1})
	hub.Publish(OperationEvent{OperationID: "op-1", Seq: 2})
	hub.Publish(OperationEvent{OperationID: "op-1", Seq: 3})

	select {
	case event, ok := <-subscriber.Events():
		if !ok {
			t.Fatal("buffered event was discarded on overflow")
		}
		if event.Seq != 1 {
			t.Fatalf("buffered event seq = %d, want 1", event.Seq)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for buffered event")
	}
	select {
	case _, ok := <-subscriber.Events():
		if ok {
			t.Fatal("overflowed subscriber received an event after disconnect")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("overflowed subscriber did not close")
	}
}

func TestOperationStreamHubUnsubscribeClosesChannel(t *testing.T) {
	hub := newOperationStreamHub()
	subscriber := hub.Subscribe(nil, "op-1", 1)
	hub.Unsubscribe("op-1", subscriber)

	if _, ok := <-subscriber.Events(); ok {
		t.Fatalf("expected channel to be closed after Unsubscribe")
	}
}

func TestOperationStreamHubConcurrentPublish(t *testing.T) {
	hub := newOperationStreamHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscriber := hub.Subscribe(ctx, "op-1", 64)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				hub.Publish(OperationEvent{OperationID: "op-1", Seq: worker*10 + i, State: orailtypes.OperationRunning})
			}
		}(w)
	}
	wg.Wait()

	count := 0
	deadline := time.After(time.Second)
	for count < 40 {
		select {
		case _, ok := <-subscriber.Events():
			if !ok {
				t.Fatalf("subscriber overflowed after %d events; dropped %d", count, subscriber.DroppedCount())
			}
			count++
		case <-deadline:
			t.Fatalf("only saw %d events; dropped %d", count, subscriber.DroppedCount())
		}
	}
}

func TestRouterOperationLiveSSEReplaysTerminalBeforeSubscribe(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	if _, err := store.Create(ctx, orailtypes.Operation{ID: "op-terminal", State: orailtypes.OperationSucceeded}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []OperationEvent{
		{OperationID: "op-terminal", State: orailtypes.OperationQueued, Message: "queued"},
		{OperationID: "op-terminal", State: orailtypes.OperationSucceeded, Message: "done"},
	} {
		if _, err := store.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-terminal/events?stream=sse", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Count(body, "id: 1\n") != 1 || strings.Count(body, "id: 2\n") != 1 {
		t.Fatalf("terminal replay IDs = %q, want one each of 1 and 2", body)
	}
	if !strings.Contains(body, `"state":"SUCCEEDED"`) {
		t.Fatalf("terminal replay missing success event: %q", body)
	}
}

func TestRouterOperationLiveSSEReconnectUsesLastEventIDWithoutDuplicates(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	if _, err := store.Create(ctx, orailtypes.Operation{ID: "op-reconnect", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []OperationEvent{
		{OperationID: "op-reconnect", State: orailtypes.OperationQueued, Message: "queued"},
		{OperationID: "op-reconnect", State: orailtypes.OperationRunning, Message: "running"},
	} {
		if _, err := store.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(NewRouterWithOperationStore(store))
	defer server.Close()

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstReq, err := http.NewRequestWithContext(firstCtx, http.MethodGet, server.URL+"/api/v1alpha/operations/op-reconnect/events?stream=sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := readSSEEventIDs(t, bufio.NewScanner(firstResp.Body), 2)
	cancelFirst()
	_ = firstResp.Body.Close()
	if got := intsString(firstIDs); got != "1,2" {
		t.Fatalf("first stream IDs = %s, want 1,2", got)
	}

	if _, err := store.AppendEvent(ctx, OperationEvent{OperationID: "op-reconnect", State: orailtypes.OperationSucceeded, Message: "done"}); err != nil {
		t.Fatal(err)
	}
	secondReq, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1alpha/operations/op-reconnect/events?stream=sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	secondReq.Header.Set("Last-Event-ID", "2")
	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	defer secondResp.Body.Close()
	secondBody, err := io.ReadAll(secondResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(secondBody)
	if strings.Contains(body, "id: 1\n") || strings.Contains(body, "id: 2\n") || strings.Count(body, "id: 3\n") != 1 {
		t.Fatalf("resumed stream body = %q, want only event id 3", body)
	}
}

func TestRouterOperationLiveSSERejectsMalformedLastEventID(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-cursor", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-cursor/events?stream=sse", nil)
	req.Header.Set("Last-Event-ID", "not-a-sequence")
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), string(failurecodes.CursorInvalid)) {
		t.Fatalf("body missing CURSOR_INVALID: %s", rr.Body.String())
	}
}

type snapshotBlockingOperationStore struct {
	OperationStore
	snapshotTaken chan struct{}
	release       chan struct{}
	once          sync.Once
}

func (s *snapshotBlockingOperationStore) ListEvents(ctx context.Context, operationID orailtypes.ID) ([]OperationEvent, error) {
	events, err := s.OperationStore.ListEvents(ctx, operationID)
	s.once.Do(func() { close(s.snapshotTaken) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return events, err
}

func TestOperationLiveSSEClosesSubscribeReplayGap(t *testing.T) {
	inner := NewMemoryOperationStore()
	if _, err := inner.Create(context.Background(), orailtypes.Operation{ID: "op-race", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	blocking := &snapshotBlockingOperationStore{
		OperationStore: inner,
		snapshotTaken:  make(chan struct{}),
		release:        make(chan struct{}),
	}
	hub := newOperationStreamHub()
	store := newPublishingOperationStore(blocking, hub)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOperationLiveEvents(w, r, store, hub, "op-race")
	}))
	defer server.Close()

	type result struct {
		response *http.Response
		err      error
	}
	response := make(chan result, 1)
	go func() {
		resp, err := http.Get(server.URL)
		response <- result{response: resp, err: err}
	}()

	select {
	case <-blocking.snapshotTaken:
	case <-time.After(time.Second):
		t.Fatal("handler did not take its durable snapshot")
	}
	if _, err := store.AppendEvent(context.Background(), OperationEvent{
		OperationID: "op-race",
		State:       orailtypes.OperationSucceeded,
		Message:     "completed during replay handoff",
	}); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)

	var got result
	select {
	case got = <-response:
	case <-time.After(time.Second):
		t.Fatal("stream did not deliver the event captured during replay handoff")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.response.Body.Close()
	body, err := io.ReadAll(got.response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "id: 1\n") != 1 || !strings.Contains(string(body), "completed during replay handoff") {
		t.Fatalf("race-handoff stream = %q, want the event exactly once", body)
	}
}

func readSSEEventIDs(t *testing.T, scanner *bufio.Scanner, count int) []int {
	t.Helper()
	ids := make([]int, 0, count)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "id: ") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(line, "id: "))
		if err != nil {
			t.Fatalf("parse SSE id %q: %v", line, err)
		}
		ids = append(ids, id)
		if len(ids) == count {
			return ids
		}
	}
	t.Fatalf("stream ended after IDs %v; scanner err=%v", ids, scanner.Err())
	return nil
}

func intsString(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func TestRouterOperationLiveSSEStreamsAppendedEvents(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-live", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithOperationStore(store)

	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1alpha/operations/op-live/events?stream=sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	// Cancel the request — we have proven the handshake.
	cancel()
}

func TestRouterOperationLiveSSEReturnsNotFoundEnvelope(t *testing.T) {
	router := NewRouterWithOperationStore(NewMemoryOperationStore())
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-missing/events?stream=sse", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), string(failurecodes.OperationNotFound)) {
		t.Fatalf("body missing OPERATION_NOT_FOUND: %s", rr.Body.String())
	}
}
