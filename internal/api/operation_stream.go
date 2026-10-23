// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"sync"

	"github.com/midnight/midnight/pkg/orailtypes"
)

// publishingOperationStore wraps an OperationStore so every appended event
// is fanned out to the supplied hub. The wrapper is transparent to callers
// that do not need the live SSE feed.
type publishingOperationStore struct {
	OperationStore
	hub *operationStreamHub
}

// NewPublishingOperationStore returns a wrapper that publishes appended
// events to the supplied hub.
func newPublishingOperationStore(inner OperationStore, hub *operationStreamHub) OperationStore {
	if inner == nil || hub == nil {
		return inner
	}
	return &publishingOperationStore{OperationStore: inner, hub: hub}
}

// AppendEvent forwards the event to the wrapped store and then publishes
// the (now-stamped) event to the hub.
func (s *publishingOperationStore) AppendEvent(ctx context.Context, event OperationEvent) (OperationEvent, error) {
	stored, err := s.OperationStore.AppendEvent(ctx, event)
	if err == nil {
		s.hub.Publish(stored)
	}
	return stored, err
}

func (s *publishingOperationStore) ListPage(ctx context.Context, filter OperationListFilter, after *OperationListCursor, limit int) (OperationListPage, error) {
	store, ok := s.OperationStore.(OperationPageStore)
	if !ok {
		return OperationListPage{}, ErrOperationPaginationUnsupported
	}
	return store.ListPage(ctx, filter, after, limit)
}

// operationStreamHub is the in-memory pub/sub adapter for live operation
// event streaming. It is intentionally bounded so a slow subscriber cannot
// stall publishers. A subscriber that exhausts its buffer is disconnected:
// silently dropping an event would make the stream look complete even though
// it contains a gap. Clients can reconnect with Last-Event-ID and replay the
// missing durable events instead.
type operationStreamHub struct {
	mu          sync.Mutex
	subscribers map[orailtypes.ID]map[*operationStreamSubscriber]struct{}
}

// operationStreamSubscriber receives a fan-out copy of every appended
// OperationEvent for a single operation id. The channel is buffered to
// keep publishers from blocking on a single slow reader.
type operationStreamSubscriber struct {
	ch         chan OperationEvent
	dropped    int
	closed     bool
	overflowed bool
	closeMu    sync.Mutex
	capacity   int
}

// newOperationStreamHub constructs an empty hub.
func newOperationStreamHub() *operationStreamHub {
	return &operationStreamHub{subscribers: make(map[orailtypes.ID]map[*operationStreamSubscriber]struct{})}
}

// Subscribe registers a new subscriber for the given operation id. The
// channel buffer size bounds the per-subscriber memory usage. A subscriber
// whose buffer fills is closed and removed from the hub so its consumer can
// resume from durable storage. Callers must call Unsubscribe when finished.
func (h *operationStreamHub) Subscribe(ctx context.Context, operationID orailtypes.ID, capacity int) *operationStreamSubscriber {
	if capacity <= 0 {
		capacity = 16
	}
	subscriber := &operationStreamSubscriber{ch: make(chan OperationEvent, capacity), capacity: capacity}

	h.mu.Lock()
	subscribers, ok := h.subscribers[operationID]
	if !ok {
		subscribers = make(map[*operationStreamSubscriber]struct{})
		h.subscribers[operationID] = subscribers
	}
	subscribers[subscriber] = struct{}{}
	h.mu.Unlock()

	if ctx != nil {
		go func() {
			<-ctx.Done()
			h.Unsubscribe(operationID, subscriber)
		}()
	}
	return subscriber
}

// Unsubscribe removes the subscriber and closes its channel.
func (h *operationStreamHub) Unsubscribe(operationID orailtypes.ID, subscriber *operationStreamSubscriber) {
	h.mu.Lock()
	if subscribers, ok := h.subscribers[operationID]; ok {
		delete(subscribers, subscriber)
		if len(subscribers) == 0 {
			delete(h.subscribers, operationID)
		}
	}
	h.mu.Unlock()

	subscriber.closeMu.Lock()
	if !subscriber.closed {
		subscriber.closed = true
		close(subscriber.ch)
	}
	subscriber.closeMu.Unlock()
}

// Publish fans out the event to every subscriber of the operation id. The
// publish is non-blocking: a subscriber whose buffer is full is disconnected
// instead of being allowed to observe a silently incomplete stream.
func (h *operationStreamHub) Publish(event OperationEvent) {
	h.mu.Lock()
	subscribers := h.subscribers[event.OperationID]
	targets := make([]*operationStreamSubscriber, 0, len(subscribers))
	for subscriber := range subscribers {
		targets = append(targets, subscriber)
	}
	h.mu.Unlock()

	for _, subscriber := range targets {
		if !subscriber.deliver(event) {
			h.Unsubscribe(event.OperationID, subscriber)
		}
	}
}

// deliver enqueues an event with bounded backpressure handling.
func (s *operationStreamSubscriber) deliver(event OperationEvent) bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return true
	}
	select {
	case s.ch <- event:
		return true
	default:
		s.dropped++
		s.overflowed = true
		s.closed = true
		close(s.ch)
		return false
	}
}

// Events exposes the read-only event channel.
func (s *operationStreamSubscriber) Events() <-chan OperationEvent { return s.ch }

// DroppedCount returns the number of events the subscriber lost due to
// backpressure.
func (s *operationStreamSubscriber) DroppedCount() int {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.dropped
}

// Overflowed reports whether bounded backpressure forced this subscriber to
// disconnect. The event that did not fit remains durable and is recoverable
// by reconnecting after the last successfully received sequence.
func (s *operationStreamSubscriber) Overflowed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.overflowed
}
