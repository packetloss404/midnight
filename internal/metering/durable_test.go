// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package metering

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryLedgerDeduplicatesProducerReplayAndReportsGauge(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	event := LedgerEvent{EventID: "event-one", Source: "resource_reconciler", SourceInstance: "wrk_one",
		SourceEventID: "project-count:1", WorkspaceID: "wrk_one", Resource: ResourceProjectCount,
		Quantity: 7, Kind: ValueGauge, ObservedAt: now.Add(-time.Minute)}
	first, err := store.Append(context.Background(), event)
	if err != nil || !first.Stored || first.Sequence != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := store.Append(context.Background(), event)
	if err != nil || replay.Stored || replay.Sequence != first.Sequence {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	summary, err := store.Summary(context.Background(), SummaryQuery{WorkspaceID: "wrk_one",
		ActorUserID: "usr_one", WindowStart: now.Add(-time.Hour), WindowEnd: now})
	if err != nil {
		t.Fatal(err)
	}
	if summary.PolicyProfile != "self_hosted" || summary.ReplayAfter != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	var projects UsageLine
	for _, line := range summary.Usage {
		if line.Resource == ResourceProjectCount {
			projects = line
		}
	}
	if projects.Used != 7 || projects.Limit != nil || projects.Enforceable || projects.Coverage.Status != CoverageComplete {
		t.Fatalf("projects=%+v", projects)
	}
}

func TestMemoryReservationAdmissionIsAtomic(t *testing.T) {
	store := NewMemoryStore()
	store.limits[ResourceProjectCount] = 100
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	_, err := store.Append(context.Background(), LedgerEvent{EventID: "count", Source: "resource_reconciler", SourceInstance: "one",
		SourceEventID: "count", WorkspaceID: "wrk_one", Resource: ResourceProjectCount,
		Quantity: 20, Kind: ValueGauge, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	requests := []ReservationRequest{
		{WorkspaceID: "wrk_one", ActorUserID: "usr_one", Resource: ResourceProjectCount, Quantity: 50, IdempotencyKey: "one", IntentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{WorkspaceID: "wrk_one", ActorUserID: "usr_two", Resource: ResourceProjectCount, Quantity: 50, IdempotencyKey: "two", IntentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	var wg sync.WaitGroup
	results := make(chan error, len(requests))
	for _, request := range requests {
		wg.Add(1)
		go func(request ReservationRequest) {
			defer wg.Done()
			_, err := store.Reserve(context.Background(), request)
			results <- err
		}(request)
	}
	wg.Wait()
	close(results)
	var admitted, denied int
	for err := range results {
		if err == nil {
			admitted++
		} else if errors.Is(err, ErrLimitExceeded) {
			denied++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 1 || denied != 1 {
		t.Fatalf("admitted=%d denied=%d", admitted, denied)
	}
}

func TestMemoryLedgerRejectsConflictingReplayAndPIIMetadata(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	event := LedgerEvent{EventID: "event-one", Source: "resource_reconciler", SourceInstance: "wrk_one",
		SourceEventID: "project-count:1", WorkspaceID: "wrk_one", Resource: ResourceProjectCount,
		Quantity: 1, Kind: ValueGauge, ObservedAt: now}
	if _, err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	event.Quantity = 2
	if _, err := store.Append(context.Background(), event); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
	event.SourceEventID = "project-count:2"
	event.EventID = "event-two"
	event.Attributes = map[string]any{"request_path": "/private/customer"}
	if _, err := store.Append(context.Background(), event); err == nil || !strings.Contains(err.Error(), "redaction-safe") {
		t.Fatalf("PII metadata err=%v", err)
	}
}

func TestMemorySummaryRejectsMissingActorInvalidWindowAndOverflow(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if _, err := store.Summary(context.Background(), SummaryQuery{WorkspaceID: "wrk_one", WindowStart: now.Add(-time.Hour), WindowEnd: now}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("missing actor err=%v", err)
	}
	if _, err := store.Summary(context.Background(), SummaryQuery{WorkspaceID: "wrk_one", ActorUserID: "usr_one", WindowStart: now, WindowEnd: now}); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("invalid window err=%v", err)
	}
	for index, quantity := range []int64{math.MaxInt64, 1} {
		_, err := store.Append(context.Background(), LedgerEvent{
			EventID: fmt.Sprintf("overflow-%d", index), Source: "runtime_metrics", SourceInstance: "svc:replica",
			SourceEventID: fmt.Sprintf("sample-%d:cpu", index), WorkspaceID: "wrk_one", Resource: ResourceCPUSeconds,
			Quantity: quantity, Kind: ValueIncrement, ObservedAt: now.Add(time.Duration(index) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Summary(context.Background(), SummaryQuery{WorkspaceID: "wrk_one", ActorUserID: "usr_one", WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute)}); !errors.Is(err, ErrQuantityOverflow) {
		t.Fatalf("overflow err=%v", err)
	}
}

func TestDisclosuresExcludeUntrustworthyEntitlementAndEgressClaims(t *testing.T) {
	joined := ""
	for _, disclosure := range Disclosures() {
		joined += disclosure + " "
	}
	for _, required := range []string{"Docker network counters are excluded", "Volume capacity is requested", "donations do not change limits"} {
		if !strings.Contains(joined, required) {
			t.Errorf("disclosures missing %q: %s", required, joined)
		}
	}
}

func TestGaugeTransitionIdentityPreservesReturnToEarlierValue(t *testing.T) {
	one := gaugeTransitionIdentity(0, 1)
	two := gaugeTransitionIdentity(12, 2)
	backToOne := gaugeTransitionIdentity(13, 1)
	if one == backToOne || two == backToOne {
		t.Fatalf("identities must preserve transitions: %q %q %q", one, two, backToOne)
	}
}

func TestNilPostgresStoreScheduledReconciliationFailsClosed(t *testing.T) {
	var store *PostgresStore
	_, err := store.ReconcileScheduled(t.Context(), ScheduledReconcileRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v, want unavailable", err)
	}
}

func TestBoundedReconcileStartUsesRecentTailAndOverlappingCheckpoint(t *testing.T) {
	until := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	since := until.Add(-14 * 24 * time.Hour)
	start, bounded := boundedReconcileStart(since, until, nil, 10*time.Minute, 2*time.Minute)
	if !bounded || !start.Equal(until.Add(-10*time.Minute)) {
		t.Fatalf("initial start=%v bounded=%v", start, bounded)
	}
	checkpoint := until.Add(-2 * time.Minute)
	start, bounded = boundedReconcileStart(since, until, &checkpoint, 10*time.Minute, 2*time.Minute)
	if !bounded || !start.Equal(checkpoint.Add(-2*time.Minute)) {
		t.Fatalf("checkpoint start=%v bounded=%v", start, bounded)
	}
	afterWindow := until.Add(time.Minute)
	start, bounded = boundedReconcileStart(since, until, &afterWindow, 10*time.Minute, 2*time.Minute)
	if !bounded || !start.Equal(until.Add(-10*time.Minute)) {
		t.Fatalf("advanced checkpoint start=%v bounded=%v", start, bounded)
	}
	recentSince := until.Add(-time.Minute)
	start, bounded = boundedReconcileStart(recentSince, until, nil, 10*time.Minute, 2*time.Minute)
	if bounded || !start.Equal(recentSince) {
		t.Fatalf("recent start=%v bounded=%v", start, bounded)
	}
}

func TestAppendCoverageNotePreservesExistingDisclosure(t *testing.T) {
	got := appendCoverageNote("derived from retained records.", "recent tail only")
	if got != "derived from retained records; recent tail only" {
		t.Fatalf("note=%q", got)
	}
}
