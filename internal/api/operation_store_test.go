package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestMemoryOperationStoreCreateGetAppendAndListEvents(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()

	operation, err := store.Create(ctx, orailtypes.Operation{State: orailtypes.OperationPending})
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID == "" || operation.State != orailtypes.OperationPending {
		t.Fatalf("unexpected operation: %#v", operation)
	}

	got, err := store.Get(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != operation {
		t.Fatalf("Get() = %#v, want %#v", got, operation)
	}

	event, err := store.AppendEvent(ctx, OperationEvent{OperationID: operation.ID, State: operation.State, Message: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if event.Seq != 1 || event.ID == "" || event.CreatedAt.IsZero() {
		t.Fatalf("unexpected event: %#v", event)
	}

	events, err := store.ListEvents(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != event {
		t.Fatalf("ListEvents() = %#v, want %#v", events, []OperationEvent{event})
	}

	events[0].Message = "changed"
	again, err := store.ListEvents(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Message != "queued" {
		t.Fatalf("ListEvents returned mutable backing slice: %#v", again)
	}
}

func TestMemoryOperationStoreConcurrentAppendAllocatesMonotonicIDs(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-concurrent", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}

	const writers = 64
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.AppendEvent(ctx, OperationEvent{
				OperationID: operation.ID,
				State:       orailtypes.OperationRunning,
				Message:     "concurrent",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := store.ListEvents(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("events = %d, want %d", len(events), writers)
	}
	for i, event := range events {
		wantSeq := i + 1
		if event.Seq != wantSeq {
			t.Fatalf("event[%d].Seq = %d, want %d", i, event.Seq, wantSeq)
		}
		if wantID := fmt.Sprintf("%s-%d", operation.ID, wantSeq); event.ID != wantID {
			t.Fatalf("event[%d].ID = %q, want %q", i, event.ID, wantID)
		}
	}
}

func TestMemoryOperationStoreListReturnsCreateOrderCopy(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()

	first, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", State: orailtypes.OperationPending})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, orailtypes.Operation{ID: "op-2", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}

	operations, err := store.List(ctx, OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0] != first || operations[1] != second {
		t.Fatalf("List() = %#v, want create order", operations)
	}

	operations[0].State = orailtypes.OperationFailed
	again, err := store.List(ctx, OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].State != orailtypes.OperationPending {
		t.Fatalf("List returned mutable backing slice: %#v", again)
	}
}

func TestMemoryOperationStoreListFiltersStatusAndKind(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()

	for _, operation := range []orailtypes.Operation{
		{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op-2", Kind: "backup", State: orailtypes.OperationRunning},
		{ID: "op-3", Kind: "deploy", State: orailtypes.OperationRunning},
	} {
		if _, err := store.Create(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}

	status := orailtypes.OperationRunning
	operations, err := store.List(ctx, OperationListFilter{Status: &status, Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].ID != "op-3" {
		t.Fatalf("List() = %#v, want filtered op-3", operations)
	}
}

func TestMemoryOperationStoreListPageUsesCreatedAtIDKeyset(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	for _, operation := range []orailtypes.Operation{
		{ID: "op_b", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op_a", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op_c", Kind: "backup", State: orailtypes.OperationQueued},
	} {
		if _, err := store.Create(ctx, operation); err != nil {
			t.Fatal(err)
		}
		store.createdAt[operation.ID] = createdAt
	}

	first, err := store.ListPage(ctx, OperationListFilter{Kind: "deploy"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || len(first.Records) != 1 || first.Records[0].Operation.ID != "op_a" {
		t.Fatalf("first page = %#v, want op_a with more", first)
	}
	after := OperationListCursor{CreatedAt: first.Records[0].CreatedAt, ID: first.Records[0].Operation.ID}
	second, err := store.ListPage(ctx, OperationListFilter{Kind: "deploy"}, &after, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || len(second.Records) != 1 || second.Records[0].Operation.ID != "op_b" {
		t.Fatalf("second page = %#v, want only op_b", second)
	}
}

func TestMemoryOperationStoreTransitionValidatesState(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}

	canceled, err := store.Transition(ctx, operation.ID, orailtypes.OperationCanceled)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != orailtypes.OperationCanceled {
		t.Fatalf("state = %q, want %q", canceled.State, orailtypes.OperationCanceled)
	}
	if _, err := store.Transition(ctx, operation.ID, orailtypes.OperationRunning); !errors.Is(err, ErrOperationInvalidTransition) {
		t.Fatalf("Transition() error = %v, want ErrOperationInvalidTransition", err)
	}
}

func TestMemoryOperationStoreFailStoresFailureMetadata(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}

	failed, err := store.Fail(ctx, operation.ID, "WORKER_ERROR", "worker failed")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != orailtypes.OperationFailed || failed.Code != "WORKER_ERROR" || failed.Reason != "worker failed" {
		t.Fatalf("operation = %#v, want failed metadata", failed)
	}
	if _, err := store.Fail(ctx, operation.ID, "AGAIN", "again"); !errors.Is(err, ErrOperationInvalidTransition) {
		t.Fatalf("Fail() error = %v, want ErrOperationInvalidTransition", err)
	}
}

func TestCreateQueuedOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := CreateQueuedOperation(ctx, store, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID == "" || operation.Kind != "demo" || operation.State != orailtypes.OperationQueued {
		t.Fatalf("unexpected operation: %#v", operation)
	}
	events, err := store.ListEvents(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].OperationID != operation.ID || events[0].Seq != 1 || events[0].State != orailtypes.OperationQueued || events[0].Message != "queued" {
		t.Fatalf("events = %#v, want queued creation event", events)
	}
}

func TestMemoryOperationStoreMissingOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("Get() error = %v, want ErrOperationNotFound", err)
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{OperationID: "missing"}); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("AppendEvent() error = %v, want ErrOperationNotFound", err)
	}
	if _, err := store.ListEvents(ctx, "missing"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("ListEvents() error = %v, want ErrOperationNotFound", err)
	}
}
