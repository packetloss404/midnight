// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package metering

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu           sync.Mutex
	events       []LedgerEvent
	dedupe       map[string]int64
	reservations map[string]Reservation
	intents      map[string]string
	limits       map[Resource]int64
	now          func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		dedupe: map[string]int64{}, reservations: map[string]Reservation{},
		intents: map[string]string{}, limits: map[Resource]int64{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (store *MemoryStore) Append(_ context.Context, event LedgerEvent) (AppendResult, error) {
	if err := event.Validate(); err != nil {
		return AppendResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := event.Source + "\x00" + event.SourceInstance + "\x00" + event.SourceEventID
	if sequence, ok := store.dedupe[key]; ok {
		if sequence < 1 || sequence > int64(len(store.events)) || !sameLedgerEventIntent(store.events[sequence-1], event) {
			return AppendResult{}, ErrEventConflict
		}
		return AppendResult{Sequence: sequence}, nil
	}
	for _, existing := range store.events {
		if existing.EventID == event.EventID {
			return AppendResult{}, ErrEventConflict
		}
	}
	event.Sequence = int64(len(store.events) + 1)
	event.RecordedAt = store.now()
	store.events = append(store.events, event)
	store.dedupe[key] = event.Sequence
	return AppendResult{Sequence: event.Sequence, Stored: true}, nil
}

func (store *MemoryStore) Summary(_ context.Context, query SummaryQuery) (Summary, error) {
	if strings.TrimSpace(query.WorkspaceID) == "" || strings.TrimSpace(query.ActorUserID) == "" {
		return Summary{}, ErrNotAuthorized
	}
	if !query.WindowEnd.After(query.WindowStart) {
		return Summary{}, ErrInvalidWindow
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lines := make([]UsageLine, 0, 10)
	resources := []Resource{ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount,
		ResourceEnvironmentCount, ResourceVolumeCount, ResourceBuildSeconds, ResourceCPUSeconds, ResourceMemoryByteSeconds,
		ResourcePublicHTTPRequests, ResourcePublicHTTPResponseBytes}
	var replay int64
	for _, resource := range resources {
		var used int64
		var last *time.Time
		for i := range store.events {
			event := store.events[i]
			if event.WorkspaceID != query.WorkspaceID || event.Resource != resource || event.ObservedAt.After(query.WindowEnd) {
				continue
			}
			if event.Kind == ValueIncrement && event.ObservedAt.Before(query.WindowStart) {
				continue
			}
			if event.Sequence > replay {
				replay = event.Sequence
			}
			observed := event.ObservedAt
			if last == nil || observed.After(*last) {
				last = &observed
				if event.Kind == ValueGauge {
					used = event.Quantity
				}
			}
			if event.Kind == ValueIncrement {
				if used > math.MaxInt64-event.Quantity {
					return Summary{}, ErrQuantityOverflow
				}
				used += event.Quantity
			}
		}
		coverage := Coverage{Status: CoverageComplete, Source: "memory", LastObservedAt: last}
		if last == nil {
			coverage.Status = CoverageUnavailable
			coverage.Note = "no evidence observed in this window"
		}
		if resource == ResourceBuildSeconds {
			coverage.Status = CoveragePartial
			coverage.Source = "deployments_build_lifecycle"
			coverage.Note = "exact for source builds with canonical lifecycle markers; pre-marker deployments are excluded"
		}
		if last != nil && (resource == ResourcePublicHTTPRequests || resource == ResourcePublicHTTPResponseBytes) {
			coverage.Status = CoveragePartial
			coverage.Note = "derived from retained Traefik access records; bounded ingestion cannot prove a complete request ledger"
		}
		var limit *int64
		if value, ok := store.limits[resource]; ok {
			copy := value
			limit = &copy
		}
		lines = append(lines, UsageLine{Resource: resource, Used: used, Limit: limit,
			Enforceable: limit != nil, Unit: unitFor(resource), Coverage: coverage})
	}
	return Summary{WorkspaceID: query.WorkspaceID, PolicyProfile: "self_hosted", WindowStart: query.WindowStart,
		WindowEnd: query.WindowEnd, AsOf: store.now(), ReplayAfter: replay, Usage: lines, Disclosures: Disclosures()}, nil
}

func (store *MemoryStore) Reserve(_ context.Context, request ReservationRequest) (Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.ActorUserID) == "" ||
		strings.TrimSpace(request.IdempotencyKey) == "" || !reservationIntentRE.MatchString(request.IntentHash) || request.Quantity <= 0 {
		return Reservation{}, errors.New("metering: invalid reservation request")
	}
	switch request.Resource {
	case ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount, ResourceEnvironmentCount, ResourceVolumeCount:
	default:
		return Reservation{}, ErrUnavailable
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl > time.Hour {
		return Reservation{}, errors.New("metering: reservation TTL exceeds one hour")
	}
	key := request.WorkspaceID + "\x00" + request.ActorUserID + "\x00" + request.IdempotencyKey
	if reservation, ok := store.reservations[key]; ok {
		if store.intents[key] != request.IntentHash || reservation.Resource != request.Resource || reservation.Quantity != request.Quantity {
			return Reservation{}, ErrReservationConflict
		}
		reservation.IdempotentReplay = true
		return reservation, nil
	}
	limit, enforceable := store.limits[request.Resource]
	if !enforceable || request.Quantity <= 0 {
		return Reservation{}, ErrUnavailable
	}
	var used, reserved, usedSequence int64
	for _, event := range store.events {
		if event.WorkspaceID == request.WorkspaceID && event.Resource == request.Resource && event.Kind == ValueGauge && event.Sequence > usedSequence {
			used = event.Quantity
			usedSequence = event.Sequence
		}
	}
	for _, reservation := range store.reservations {
		if reservation.WorkspaceID == request.WorkspaceID && reservation.Resource == request.Resource && reservation.State == "active" && reservation.ExpiresAt.After(store.now()) {
			reserved += reservation.Quantity
		}
	}
	if reserved > limit || used > limit-reserved || request.Quantity > limit-used-reserved {
		return Reservation{}, &LimitError{Resource: request.Resource, Used: used, Reserved: reserved, Requested: request.Quantity, Limit: limit}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Reservation{}, err
	}
	reservation := Reservation{ID: "usrsv_" + hex.EncodeToString(raw[:]), WorkspaceID: request.WorkspaceID,
		Resource: request.Resource, Quantity: request.Quantity, State: "active", ExpiresAt: store.now().Add(ttl)}
	store.reservations[key], store.intents[key] = reservation, request.IntentHash
	return reservation, nil
}

func sortUsage(lines []UsageLine) {
	sort.Slice(lines, func(i, j int) bool { return lines[i].Resource < lines[j].Resource })
}
