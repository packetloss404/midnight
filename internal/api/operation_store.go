package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

var ErrOperationNotFound = errors.New("operation not found")
var ErrOperationInvalidTransition = errors.New("operation invalid transition")
var ErrOperationPaginationUnsupported = errors.New("operation store does not support keyset pagination")

type OperationEvent struct {
	ID          string                    `json:"id"`
	OperationID orailtypes.ID             `json:"operation_id"`
	Seq         int                       `json:"seq"`
	State       orailtypes.OperationState `json:"state"`
	Message     string                    `json:"message,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
}

type OperationStore interface {
	Create(ctx context.Context, operation orailtypes.Operation) (orailtypes.Operation, error)
	Get(ctx context.Context, id orailtypes.ID) (orailtypes.Operation, error)
	List(ctx context.Context, filter OperationListFilter) ([]orailtypes.Operation, error)
	Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error)
	Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error)
	AppendEvent(ctx context.Context, event OperationEvent) (OperationEvent, error)
	ListEvents(ctx context.Context, operationID orailtypes.ID) ([]OperationEvent, error)
}

// OperationListCursor is the exclusive keyset position used by operation
// collection reads. CreatedAt and ID together form a total, immutable order;
// ID breaks timestamp ties without relying on an unstable row offset.
type OperationListCursor struct {
	CreatedAt time.Time
	ID        orailtypes.ID
}

// OperationListRecord carries the normalized storage ordering key alongside
// the public resource so cursor construction never depends on JSON rendering.
type OperationListRecord struct {
	Operation orailtypes.Operation
	CreatedAt time.Time
}

type OperationListPage struct {
	Records []OperationListRecord
	HasMore bool
}

// OperationPageStore is an additive capability so existing operation-store
// implementations do not need to change their lifecycle API. Production and
// built-in memory stores implement it; wrappers must delegate it explicitly.
type OperationPageStore interface {
	ListPage(context.Context, OperationListFilter, *OperationListCursor, int) (OperationListPage, error)
}

// OperationLifecycleObserver receives best-effort notifications when an
// operation transitions to a terminal lifecycle state. Implementations MUST
// be non-blocking with respect to the FSM (return promptly, do work in a
// goroutine when needed) and MUST NOT panic; panics are recovered by the
// emitter so a misbehaving observer never re-fails the operation. The
// agent build-failure diagnosis runner is one such observer; see
// docs/adr/0025-agent-build-failure-diagnosis.md.
type OperationLifecycleObserver interface {
	OnFailed(ctx context.Context, op orailtypes.Operation, events []OperationEvent)
}

// OperationLifecycleObserverFunc adapts a function to the observer
// interface.
type OperationLifecycleObserverFunc func(ctx context.Context, op orailtypes.Operation, events []OperationEvent)

// OnFailed implements OperationLifecycleObserver.
func (f OperationLifecycleObserverFunc) OnFailed(ctx context.Context, op orailtypes.Operation, events []OperationEvent) {
	if f != nil {
		f(ctx, op, events)
	}
}

// ObservedOperationStore is a decorator that wraps an OperationStore and
// fans out lifecycle transitions to registered observers. The wrapper is
// the canonical hook point for cross-cutting features (build-failure
// diagnosis, alerts, audit emit) that react to FAILED transitions without
// coupling the FSM caller to those features.
//
// Observers fire AFTER the underlying transition has been durably
// persisted. They run in a goroutine with a fresh context so they never
// block the FSM, and panics inside an observer are recovered so the
// operation result is never affected. Failed observer calls MUST be
// surfaced via logging in the observer itself, not propagated.
type ObservedOperationStore struct {
	OperationStore

	mu        sync.RWMutex
	observers []OperationLifecycleObserver
}

// NewObservedOperationStore wraps the supplied store with observer
// fan-out. The store argument MUST be non-nil; nil stores are returned
// as nil so callers can build conditionally.
func NewObservedOperationStore(store OperationStore) *ObservedOperationStore {
	if store == nil {
		return nil
	}
	return &ObservedOperationStore{OperationStore: store}
}

// Register adds an observer. Nil observers are ignored.
func (s *ObservedOperationStore) Register(observer OperationLifecycleObserver) {
	if s == nil || observer == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, observer)
}

func (s *ObservedOperationStore) ListPage(ctx context.Context, filter OperationListFilter, after *OperationListCursor, limit int) (OperationListPage, error) {
	store, ok := s.OperationStore.(OperationPageStore)
	if !ok {
		return OperationListPage{}, ErrOperationPaginationUnsupported
	}
	return store.ListPage(ctx, filter, after, limit)
}

// Transition forwards to the underlying store and emits OnFailed when the
// returned operation is in the failed state. The wrapper does not emit
// for non-failed terminal states because the diagnosis surface is
// failure-only by design.
func (s *ObservedOperationStore) Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	op, err := s.OperationStore.Transition(ctx, id, state)
	if err != nil {
		return op, err
	}
	if op.State == orailtypes.OperationFailed {
		s.emitFailed(ctx, op)
	}
	return op, nil
}

// Fail forwards to the underlying store and emits OnFailed on success.
func (s *ObservedOperationStore) Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	op, err := s.OperationStore.Fail(ctx, id, code, reason)
	if err != nil {
		return op, err
	}
	s.emitFailed(ctx, op)
	return op, nil
}

// emitFailed gathers a snapshot of events for the operation and notifies
// each observer. Observer notifications run synchronously here so that
// dedupe state inside an observer (e.g., the diagnosis runner's
// per-operation guard) cannot miss a fire. Observers themselves are
// required to be non-blocking — the contract is documented on
// OperationLifecycleObserver.
func (s *ObservedOperationStore) emitFailed(ctx context.Context, op orailtypes.Operation) {
	events, err := s.OperationStore.ListEvents(ctx, op.ID)
	if err != nil {
		// Observers still fire with an empty event slice; the diagnosis
		// runner copes with missing context.
		events = nil
	}
	s.mu.RLock()
	observers := append([]OperationLifecycleObserver(nil), s.observers...)
	s.mu.RUnlock()
	for _, observer := range observers {
		safeEmit(ctx, observer, op, events)
	}
}

// safeEmit isolates one observer call so a panic never propagates back
// into the FSM. Observers that want async behaviour (e.g., the diagnosis
// runner, which spawns a goroutine with its own 2-minute deadline) own
// that responsibility.
func safeEmit(ctx context.Context, observer OperationLifecycleObserver, op orailtypes.Operation, events []OperationEvent) {
	defer func() {
		_ = recover()
	}()
	observer.OnFailed(ctx, op, events)
}

type OperationListFilter struct {
	Status *orailtypes.OperationState
	Kind   string
}

type MemoryOperationStore struct {
	mu         sync.Mutex
	nextID     int64
	order      []orailtypes.ID
	operations map[orailtypes.ID]orailtypes.Operation
	createdAt  map[orailtypes.ID]time.Time
	events     map[orailtypes.ID][]OperationEvent
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{
		operations: make(map[orailtypes.ID]orailtypes.Operation),
		createdAt:  make(map[orailtypes.ID]time.Time),
		events:     make(map[orailtypes.ID][]OperationEvent),
	}
}

func (s *MemoryOperationStore) Create(ctx context.Context, operation orailtypes.Operation) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	if operation.State == "" {
		operation.State = orailtypes.OperationPending
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if operation.ID == "" {
		s.nextID++
		operation.ID = orailtypes.ID(fmt.Sprintf("op-%d", s.nextID))
	}
	if err := operation.Validate(); err != nil {
		return orailtypes.Operation{}, err
	}
	now := time.Now().UTC()
	existing, exists := s.operations[operation.ID]
	if exists {
		operation.CreatedAt = existing.CreatedAt
	} else if operation.CreatedAt.IsZero() {
		operation.CreatedAt = now
	}
	operation.UpdatedAt = now
	if !exists {
		s.order = append(s.order, operation.ID)
		s.createdAt[operation.ID] = operation.CreatedAt
	}
	s.operations[operation.ID] = operation
	return operation, nil
}

func (s *MemoryOperationStore) Get(ctx context.Context, id orailtypes.ID) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	operation, ok := s.operations[id]
	if !ok {
		return orailtypes.Operation{}, ErrOperationNotFound
	}
	return operation, nil
}

func (s *MemoryOperationStore) List(ctx context.Context, filter OperationListFilter) ([]orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	operations := make([]orailtypes.Operation, 0, len(s.order))
	for _, id := range s.order {
		operation := s.operations[id]
		if filter.Status != nil && operation.State != *filter.Status {
			continue
		}
		if filter.Kind != "" && operation.Kind != filter.Kind {
			continue
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (s *MemoryOperationStore) ListPage(ctx context.Context, filter OperationListFilter, after *OperationListCursor, limit int) (OperationListPage, error) {
	if err := ctx.Err(); err != nil {
		return OperationListPage{}, err
	}
	if limit <= 0 {
		return OperationListPage{}, orailtypes.ErrInvalidCursorPage
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	records := make([]OperationListRecord, 0, len(s.order))
	for _, id := range s.order {
		operation := s.operations[id]
		if filter.Status != nil && operation.State != *filter.Status {
			continue
		}
		if filter.Kind != "" && operation.Kind != filter.Kind {
			continue
		}
		createdAt := s.createdAt[id]
		if after != nil && (createdAt.Before(after.CreatedAt) ||
			(createdAt.Equal(after.CreatedAt) && id <= after.ID)) {
			continue
		}
		records = append(records, OperationListRecord{Operation: operation, CreatedAt: createdAt})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].Operation.ID < records[j].Operation.ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return OperationListPage{Records: records, HasMore: hasMore}, nil
}

func (s *MemoryOperationStore) Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	operation, ok := s.operations[id]
	if !ok {
		return orailtypes.Operation{}, ErrOperationNotFound
	}
	if err := orailtypes.ValidateOperationTransition(operation.State, state); err != nil || isTerminalOperationState(operation.State) {
		return orailtypes.Operation{}, ErrOperationInvalidTransition
	}
	operation.State = state
	operation.UpdatedAt = time.Now().UTC()
	if err := operation.Validate(); err != nil {
		return orailtypes.Operation{}, err
	}
	s.operations[id] = operation
	return operation, nil
}

func (s *MemoryOperationStore) Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	operation, ok := s.operations[id]
	if !ok {
		return orailtypes.Operation{}, ErrOperationNotFound
	}
	if err := orailtypes.ValidateOperationTransition(operation.State, orailtypes.OperationFailed); err != nil || isTerminalOperationState(operation.State) {
		return orailtypes.Operation{}, ErrOperationInvalidTransition
	}
	operation.State = orailtypes.OperationFailed
	operation.Code = code
	operation.Reason = reason
	operation.UpdatedAt = time.Now().UTC()
	if err := operation.Validate(); err != nil {
		return orailtypes.Operation{}, err
	}
	s.operations[id] = operation
	return operation, nil
}

func (s *MemoryOperationStore) AppendEvent(ctx context.Context, event OperationEvent) (OperationEvent, error) {
	if err := ctx.Err(); err != nil {
		return OperationEvent{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.operations[event.OperationID]; !ok {
		return OperationEvent{}, ErrOperationNotFound
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	event.Seq = len(s.events[event.OperationID]) + 1
	if event.ID == "" {
		event.ID = fmt.Sprintf("%s-%d", event.OperationID, event.Seq)
	}
	s.events[event.OperationID] = append(s.events[event.OperationID], event)
	return event, nil
}

func (s *MemoryOperationStore) ListEvents(ctx context.Context, operationID orailtypes.ID) ([]OperationEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.operations[operationID]; !ok {
		return nil, ErrOperationNotFound
	}
	events := s.events[operationID]
	ordered := append([]OperationEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Seq < ordered[j].Seq
	})
	return ordered, nil
}

func isTerminalOperationState(state orailtypes.OperationState) bool {
	switch state {
	case orailtypes.OperationSucceeded, orailtypes.OperationFailed, orailtypes.OperationCanceled:
		return true
	default:
		return false
	}
}

func CreateQueuedOperation(ctx context.Context, store OperationStore, kind string) (orailtypes.Operation, error) {
	operation := orailtypes.Operation{Kind: kind, State: orailtypes.OperationQueued}
	operation, err := store.Create(ctx, operation)
	if err != nil {
		return orailtypes.Operation{}, err
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{OperationID: operation.ID, State: operation.State, Message: "queued"}); err != nil {
		return orailtypes.Operation{}, err
	}
	return operation, nil
}
