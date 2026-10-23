// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package idempotency

import (
	"context"
	"errors"
	"testing"
	"time"
)

// _ = Store assignments fail to build if either durable or in-memory
// implementations drift from the panel-locked idempotency seam.
var _ Store = (*MemoryStore)(nil)
var _ Store = (*PostgresStore)(nil)
var _ SensitiveResponseStore = (*MemoryStore)(nil)
var _ SensitiveResponseStore = (*PostgresStore)(nil)

func TestPutAliasMirrorsReserve(t *testing.T) {
	store := NewMemoryStore()
	fingerprint := RequestFingerprint{Method: "POST", Path: "/p", BodySHA256: BodyHashSHA256([]byte(`{}`))}

	first, err := store.Put(context.Background(), Key("alias"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if first.State != StateReserved {
		t.Fatalf("Put() state = %q, want %q", first.State, StateReserved)
	}
	loaded, err := store.Get(context.Background(), Key("alias"), fingerprint)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Key != Key("alias") {
		t.Fatalf("Get() key drift: %q", loaded.Key)
	}
}

func TestPutSurfacesReplaySemantics(t *testing.T) {
	store := NewMemoryStore()
	fingerprint := RequestFingerprint{Method: "POST", Path: "/p", BodySHA256: BodyHashSHA256([]byte(`{}`))}

	if _, err := store.Put(context.Background(), Key("k"), fingerprint, time.Hour); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := store.Put(context.Background(), Key("k"), fingerprint, time.Hour); !errors.Is(err, ErrRequestInProgress) {
		t.Fatalf("Put() second call error = %v, want ErrRequestInProgress", err)
	}
}

func TestGetReturnsErrReservationNotFoundForUnknownKey(t *testing.T) {
	store := NewMemoryStore()
	fingerprint := RequestFingerprint{Method: "POST", Path: "/p", BodySHA256: BodyHashSHA256([]byte(`{}`))}
	if _, err := store.Get(context.Background(), Key("missing"), fingerprint); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrReservationNotFound", err)
	}
}

func TestStoreInterfaceLifecycle(t *testing.T) {
	var s Store = NewMemoryStore()
	fingerprint := RequestFingerprint{Method: "POST", Path: "/p", BodySHA256: BodyHashSHA256([]byte(`{}`))}

	reserved, err := s.Put(context.Background(), Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if _, err := s.AttachOperation(context.Background(), reserved.Reference(), "op_01"); err != nil {
		t.Fatalf("AttachOperation() = %v", err)
	}
	if _, err := s.CompleteSuccess(context.Background(), reserved.Reference(), StoredResponse{StatusCode: 202}); err != nil {
		t.Fatalf("CompleteSuccess() = %v", err)
	}
	reservation, err := s.Get(context.Background(), Key("k"), fingerprint)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if reservation.State != StateSucceeded {
		t.Fatalf("state = %q, want SUCCEEDED", reservation.State)
	}
}
