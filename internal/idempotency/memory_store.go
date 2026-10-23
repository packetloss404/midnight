// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package idempotency

import (
	"context"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/secrets"
)

// nowFunc is the indirection used by tests to freeze time. Production code
// passes time.Now().
type nowFunc func() time.Time

type reservationKey struct {
	actor  Actor
	tenant Tenant
	method string
	path   string
	key    Key
}

// MemoryStore is the in-memory reference implementation of Store. The full
// Phase 0 state matrix lives here.
type MemoryStore struct {
	mu                 sync.Mutex
	reservations       map[reservationKey]Reservation
	sensitiveResponses map[reservationKey]sealedResponse
	responseKeys       secrets.KeyProvider
	now                nowFunc
}

// NewMemoryStore constructs a MemoryStore that uses time.Now for TTL math.
func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithClock(time.Now)
}

// NewMemoryStoreWithClock allows tests to inject a deterministic clock.
func NewMemoryStoreWithClock(now nowFunc) *MemoryStore {
	return NewMemoryStoreWithSensitiveResponseKeys(now, nil)
}

// NewMemoryStoreWithSensitiveResponseKeys constructs the reference store with
// an explicit key provider for encrypted terminal responses. Ordinary response
// completion remains available when keys is nil.
func NewMemoryStoreWithSensitiveResponseKeys(
	now nowFunc,
	keys secrets.KeyProvider,
) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		reservations:       make(map[reservationKey]Reservation),
		sensitiveResponses: make(map[reservationKey]sealedResponse),
		responseKeys:       keys,
		now:                now,
	}
}

// Get is a thin alias for Load that lets callers reach for the S03 Phase 1.5
// `Get/Put/CompleteSuccess/CompleteFailure` vocabulary while preserving the
// original `Reserve/Load/AttachOperation` panel-locked transition names.
func (s *MemoryStore) Get(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error) {
	return s.Load(ctx, key, fingerprint)
}

// Put is a thin alias for Reserve that lets callers reach for the S03 Phase 1.5
// `Get/Put/CompleteSuccess/CompleteFailure` vocabulary.
func (s *MemoryStore) Put(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error) {
	return s.Reserve(ctx, key, fingerprint, ttl)
}

// Reserve implements the Store contract.
func (s *MemoryStore) Reserve(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if err := ValidateKey(key); err != nil {
		return Reservation{}, err
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		return Reservation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey := requestReservationKey(ctx, key, fingerprint)
	now := s.now().UTC()
	if existing, ok := s.reservations[scopedKey]; ok {
		if !existing.ExpiresAt.IsZero() && !now.Before(existing.ExpiresAt) {
			delete(s.reservations, scopedKey)
			delete(s.sensitiveResponses, scopedKey)
		} else {
			return s.handleExistingReservation(ctx, scopedKey, existing, fingerprint)
		}
	}

	generation, err := newGeneration()
	if err != nil {
		return Reservation{}, err
	}
	reservation := Reservation{
		Key:         key,
		Actor:       scopedKey.actor,
		Tenant:      scopedKey.tenant,
		Generation:  generation,
		State:       StateReserved,
		Fingerprint: fingerprint,
		CreatedAt:   now,
	}
	if ttl > 0 {
		reservation.ExpiresAt = now.Add(ttl)
	}
	s.reservations[scopedKey] = reservation
	return cloneReservation(reservation), nil
}

// handleExistingReservation maps an existing reservation against the
// state-matrix in OPERATION_AND_IDEMPOTENCY_CONTRACT.md section 5.
func (s *MemoryStore) handleExistingReservation(
	ctx context.Context,
	scopedKey reservationKey,
	existing Reservation,
	fingerprint RequestFingerprint,
) (Reservation, error) {
	switch existing.State {
	case StateReserved, StateRunning:
		// Pending row. Both same-body and different-body callers see
		// IDEMPOTENCY_REQUEST_IN_PROGRESS — the in-flight lock takes
		// precedence over the body-hash compare.
		if !existing.Fingerprint.Equal(fingerprint) {
			return cloneReservation(existing), ErrRequestInProgress
		}
		return cloneReservation(existing), ErrRequestInProgress
	case StateSucceeded, StateFailed:
		// Terminal row. Same body replays verbatim; different body returns
		// IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY.
		if !existing.Fingerprint.Equal(fingerprint) {
			return cloneReservation(existing), ErrKeyReusedDifferentBody
		}
		return s.materializeResponseLocked(ctx, scopedKey, existing)
	case StateExpired:
		// Treat as a fresh request — the caller must have observed an
		// expired row that was not yet GC'd; the reservation has been
		// removed in Reserve.
		return Reservation{}, ErrReservationNotFound
	default:
		return Reservation{}, ErrReservationNotFound
	}
}

// Load returns the current reservation, if any.
func (s *MemoryStore) Load(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if err := ValidateKey(key); err != nil {
		return Reservation{}, err
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		return Reservation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey := requestReservationKey(ctx, key, fingerprint)
	reservation, err := s.loadLocked(scopedKey)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.Fingerprint.BodySHA256 != fingerprint.BodySHA256 {
		return cloneReservation(reservation), ErrKeyReusedDifferentBody
	}
	return s.materializeResponseLocked(ctx, scopedKey, reservation)
}

func (s *MemoryStore) loadLocked(scopedKey reservationKey) (Reservation, error) {
	reservation, ok := s.reservations[scopedKey]
	if !ok {
		return Reservation{}, ErrReservationNotFound
	}
	if !reservation.ExpiresAt.IsZero() && !s.now().UTC().Before(reservation.ExpiresAt) {
		delete(s.reservations, scopedKey)
		delete(s.sensitiveResponses, scopedKey)
		return Reservation{}, ErrReservationNotFound
	}
	return reservation, nil
}

// AttachOperation transitions RESERVED -> RUNNING and binds an operation id.
func (s *MemoryStore) AttachOperation(ctx context.Context, ref Reference, operationID string) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if operationID == "" {
		return Reservation{}, ErrReservationNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey, reservation, err := s.loadReferenceLocked(ctx, ref)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.IsTerminal() {
		return s.materializeResponseLocked(ctx, scopedKey, reservation)
	}
	if reservation.State == StateRunning && reservation.OperationID != "" && reservation.OperationID != operationID {
		return cloneReservation(reservation), ErrKeyReused
	}
	reservation.State = StateRunning
	reservation.OperationID = operationID
	s.reservations[scopedKey] = reservation
	return cloneReservation(reservation), nil
}

// CompleteSuccess stores the terminal 2xx response.
func (s *MemoryStore) CompleteSuccess(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error) {
	return s.complete(ctx, ref, StateSucceeded, response)
}

// CompleteFailure stores the terminal 4xx/5xx response.
func (s *MemoryStore) CompleteFailure(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error) {
	return s.complete(ctx, ref, StateFailed, response)
}

// CompleteSensitiveSuccess stores an encrypted terminal 2xx response.
func (s *MemoryStore) CompleteSensitiveSuccess(
	ctx context.Context,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	return s.completeSensitive(ctx, ref, StateSucceeded, response)
}

// CompleteSensitiveFailure stores an encrypted terminal 4xx/5xx response.
func (s *MemoryStore) CompleteSensitiveFailure(
	ctx context.Context,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	return s.completeSensitive(ctx, ref, StateFailed, response)
}

func (s *MemoryStore) complete(ctx context.Context, ref Reference, state State, response StoredResponse) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey, reservation, err := s.loadReferenceLocked(ctx, ref)
	if err != nil {
		return Reservation{}, err
	}
	// Terminal results are immutable. A duplicate completion is an idempotent
	// read of the first committed result, never an opportunity to replace its
	// state, body, headers, or status code.
	if reservation.IsTerminal() {
		return s.materializeResponseLocked(ctx, scopedKey, reservation)
	}
	clone := response.Clone()
	reservation.State = state
	reservation.Response = &clone
	s.reservations[scopedKey] = reservation
	delete(s.sensitiveResponses, scopedKey)
	return cloneReservation(reservation), nil
}

func (s *MemoryStore) completeSensitive(
	ctx context.Context,
	ref Reference,
	state State,
	response StoredResponse,
) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if err := validateTerminalResponse(state, response); err != nil {
		return Reservation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey, reservation, err := s.loadReferenceLocked(ctx, ref)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.IsTerminal() {
		return s.materializeResponseLocked(ctx, scopedKey, reservation)
	}
	sealed, err := sealSensitiveResponse(ctx, s.responseKeys, ref, state, response)
	if err != nil {
		return Reservation{}, err
	}
	reservation.State = state
	reservation.Response = nil
	s.reservations[scopedKey] = reservation
	s.sensitiveResponses[scopedKey] = sealed
	out := cloneReservation(reservation)
	replayed := response.Clone()
	out.Response = &replayed
	return out, nil
}

// Expire forcibly transitions the reservation to StateExpired (used by tests
// or out-of-band reapers).
func (s *MemoryStore) Expire(ctx context.Context, ref Reference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	scopedKey, _, err := s.loadReferenceLocked(ctx, ref)
	if err != nil {
		return err
	}
	delete(s.reservations, scopedKey)
	delete(s.sensitiveResponses, scopedKey)
	return nil
}

func (s *MemoryStore) materializeResponseLocked(
	ctx context.Context,
	scopedKey reservationKey,
	reservation Reservation,
) (Reservation, error) {
	if sealed, ok := s.sensitiveResponses[scopedKey]; ok {
		response, err := openSensitiveResponse(
			ctx,
			s.responseKeys,
			reservation.Reference(),
			reservation.State,
			sealed,
		)
		if err != nil {
			return Reservation{}, err
		}
		out := cloneReservation(reservation)
		out.Response = &response
		return out, nil
	}
	return cloneReservation(reservation), nil
}

func requestReservationKey(ctx context.Context, key Key, fingerprint RequestFingerprint) reservationKey {
	return reservationKey{
		actor:  ActorFromContext(ctx),
		tenant: TenantFromContext(ctx),
		method: fingerprint.Method,
		path:   fingerprint.Path,
		key:    key,
	}
}

func referenceReservationKey(ref Reference) reservationKey {
	return reservationKey{
		actor:  ref.Actor,
		tenant: ref.Tenant,
		method: ref.Method,
		path:   ref.Path,
		key:    ref.Key,
	}
}

// loadReferenceLocked resolves a transition reference and verifies every
// immutable field, including the generation. The context check prevents a
// reference issued to one principal/tenant from being used under another
// principal even if an internal caller accidentally passes it across requests.
func (s *MemoryStore) loadReferenceLocked(ctx context.Context, ref Reference) (reservationKey, Reservation, error) {
	if err := ValidateReference(ctx, ref); err != nil {
		return reservationKey{}, Reservation{}, err
	}
	scopedKey := referenceReservationKey(ref)
	reservation, err := s.loadLocked(scopedKey)
	if err != nil {
		return reservationKey{}, Reservation{}, err
	}
	if reservation.Reference() != ref {
		return reservationKey{}, Reservation{}, ErrReservationNotFound
	}
	return scopedKey, reservation, nil
}

func cloneReservation(reservation Reservation) Reservation {
	if reservation.Response == nil {
		return reservation
	}
	response := reservation.Response.Clone()
	reservation.Response = &response
	return reservation
}
