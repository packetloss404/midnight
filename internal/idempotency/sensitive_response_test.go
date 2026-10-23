// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package idempotency

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/secrets"
)

func sensitiveTestKeyProvider(t *testing.T, fill byte, grace int64) *secrets.MemoryKeyProvider {
	t.Helper()
	provider, err := secrets.NewMemoryKeyProvider(bytes.Repeat([]byte{fill}, 32), grace)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider() = %v", err)
	}
	return provider
}

func sensitiveTestReference() Reference {
	return Reference{
		Key:        Key("mint-request-1"),
		Actor:      UserActor("usr_alice"),
		Tenant:     Tenant("project:prj_api"),
		Method:     http.MethodPost,
		Path:       "/api/v1alpha/projects/{project_id}/automation-tokens",
		BodySHA256: BodyHashSHA256([]byte(`{"name":"deploy"}`)),
		Generation: "0123456789abcdef0123456789abcdef",
	}
}

func TestSensitiveResponseEnvelopeRoundTripAndFenceAuthentication(t *testing.T) {
	ctx := context.Background()
	provider := sensitiveTestKeyProvider(t, 0x11, 1)
	ref := sensitiveTestReference()
	want := StoredResponse{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type":   {"application/json"},
			"X-Secret-Proof": {"header-secret"},
		},
		Body: []byte(`{"access_token":"midn_sa_one_time_secret"}`),
	}

	sealed, err := sealSensitiveResponse(ctx, provider, ref, StateSucceeded, want)
	if err != nil {
		t.Fatalf("sealSensitiveResponse() = %v", err)
	}
	persisted := string(sealed.Envelope.Ciphertext) +
		string(sealed.Envelope.WrappedDEK) +
		string(sealed.Envelope.KEKID) +
		sealed.Envelope.CanonicalForm +
		sealed.AADSHA256
	for _, secret := range []string{"midn_sa_one_time_secret", "header-secret"} {
		if strings.Contains(persisted, secret) {
			t.Fatalf("persisted envelope contains plaintext %q", secret)
		}
	}

	got, err := openSensitiveResponse(ctx, provider, ref, StateSucceeded, sealed)
	if err != nil {
		t.Fatalf("openSensitiveResponse() = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("open response = %#v, want %#v", got, want)
	}

	mutations := map[string]func(*Reference){
		"actor":  func(value *Reference) { value.Actor = UserActor("usr_bob") },
		"tenant": func(value *Reference) { value.Tenant = Tenant("project:prj_other") },
		"method": func(value *Reference) { value.Method = http.MethodPut },
		"path":   func(value *Reference) { value.Path += "/other" },
		"key":    func(value *Reference) { value.Key = Key("mint-request-2") },
		"body digest": func(value *Reference) {
			value.BodySHA256 = BodyHashSHA256([]byte(`{"name":"other"}`))
		},
		"generation": func(value *Reference) { value.Generation = "fedcba9876543210fedcba9876543210" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := ref
			mutate(&changed)
			if _, err := openSensitiveResponse(ctx, provider, changed, StateSucceeded, sealed); !errors.Is(err, ErrSensitiveResponseUnavailable) {
				t.Fatalf("open with changed %s = %v, want ErrSensitiveResponseUnavailable", name, err)
			}
		})
	}
	if _, err := openSensitiveResponse(ctx, provider, ref, StateFailed, sealed); !errors.Is(err, ErrSensitiveResponseUnavailable) {
		t.Fatalf("open with changed terminal state = %v, want ErrSensitiveResponseUnavailable", err)
	}

	tampered := sealed
	tampered.Envelope.Ciphertext = append([]byte(nil), sealed.Envelope.Ciphertext...)
	tampered.Envelope.Ciphertext[len(tampered.Envelope.Ciphertext)-1] ^= 0xff
	if _, err := openSensitiveResponse(ctx, provider, ref, StateSucceeded, tampered); !errors.Is(err, ErrSensitiveResponseUnavailable) {
		t.Fatalf("open tampered envelope = %v, want ErrSensitiveResponseUnavailable", err)
	}
}

func TestMemorySensitiveCompletionReplaysWithoutPlaintextStorage(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	provider := sensitiveTestKeyProvider(t, 0x22, 1)
	store := NewMemoryStoreWithSensitiveResponseKeys(func() time.Time { return now }, provider)
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("project:prj_api"))
	fingerprint := memoryFingerprint(
		http.MethodPost,
		"/api/v1alpha/projects/{project_id}/automation-tokens",
		`{"name":"deploy"}`,
	)
	reserved, err := store.Reserve(ctx, Key("mint-request-1"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"access_token":"midn_sa_one_time_secret"}`),
	}
	completed, err := store.CompleteSensitiveSuccess(ctx, reserved.Reference(), want)
	if err != nil {
		t.Fatalf("CompleteSensitiveSuccess() = %v", err)
	}
	if completed.Response == nil || !reflect.DeepEqual(*completed.Response, want) {
		t.Fatalf("completion response = %#v", completed.Response)
	}

	scopedKey := requestReservationKey(ctx, reserved.Key, fingerprint)
	persisted := store.reservations[scopedKey]
	if persisted.Response != nil {
		t.Fatal("sensitive response was persisted in the plaintext reservation")
	}
	envelope, ok := store.sensitiveResponses[scopedKey]
	if !ok {
		t.Fatal("sensitive response envelope was not persisted")
	}
	rawPersistence := string(envelope.Envelope.Ciphertext) +
		string(envelope.Envelope.WrappedDEK)
	if strings.Contains(rawPersistence, "midn_sa_one_time_secret") {
		t.Fatal("sensitive response plaintext appears in persisted envelope fields")
	}

	replay, err := store.Reserve(ctx, reserved.Key, fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve(replay) = %v", err)
	}
	if replay.Generation != reserved.Generation ||
		replay.Response == nil ||
		!reflect.DeepEqual(*replay.Response, want) {
		t.Fatalf("replay = %#v", replay)
	}
}

func TestMemorySensitiveResponseFailsClosedForMissingOrPurgedKey(t *testing.T) {
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("project:prj_api"))
	fingerprint := memoryFingerprint(http.MethodPost, "/token-mint", `{}`)

	t.Run("missing provider cannot complete", func(t *testing.T) {
		store := NewMemoryStore()
		reserved, err := store.Reserve(ctx, Key("missing-provider"), fingerprint, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteSensitiveSuccess(
			ctx,
			reserved.Reference(),
			StoredResponse{StatusCode: http.StatusCreated, Body: []byte("secret")},
		); !errors.Is(err, ErrSensitiveResponseUnavailable) {
			t.Fatalf("CompleteSensitiveSuccess() = %v, want ErrSensitiveResponseUnavailable", err)
		}
		loaded, err := store.Load(ctx, reserved.Key, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.State != StateReserved || loaded.Response != nil {
			t.Fatalf("failed seal mutated reservation: %#v", loaded)
		}
	})

	t.Run("purged provider cannot replay", func(t *testing.T) {
		provider := sensitiveTestKeyProvider(t, 0x33, 0)
		store := NewMemoryStoreWithSensitiveResponseKeys(time.Now, provider)
		reserved, err := store.Reserve(ctx, Key("purged-provider"), fingerprint, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteSensitiveSuccess(
			ctx,
			reserved.Reference(),
			StoredResponse{StatusCode: http.StatusCreated, Body: []byte("secret")},
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := provider.Rotate(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.PurgeExpired(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Reserve(ctx, reserved.Key, fingerprint, time.Hour); !errors.Is(err, ErrSensitiveResponseUnavailable) {
			t.Fatalf("Reserve(replay after key purge) = %v, want ErrSensitiveResponseUnavailable", err)
		}
		persisted := store.reservations[requestReservationKey(ctx, reserved.Key, fingerprint)]
		if persisted.State != StateSucceeded || persisted.Generation != reserved.Generation {
			t.Fatalf("unavailable replay became fresh or mutable: %#v", persisted)
		}
	})
}

func TestMemorySensitiveEnvelopeIsDestroyedAtExpiry(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	provider := sensitiveTestKeyProvider(t, 0x44, 1)
	store := NewMemoryStoreWithSensitiveResponseKeys(func() time.Time { return now }, provider)
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("project:prj_api"))
	fingerprint := memoryFingerprint(http.MethodPost, "/token-mint", `{}`)
	reserved, err := store.Reserve(ctx, Key("expires"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteSensitiveSuccess(
		ctx,
		reserved.Reference(),
		StoredResponse{StatusCode: http.StatusCreated, Body: []byte("secret")},
	); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	fresh, err := store.Reserve(ctx, reserved.Key, fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve(after expiry) = %v", err)
	}
	if fresh.Generation == reserved.Generation || fresh.State != StateReserved {
		t.Fatalf("fresh reservation = %#v", fresh)
	}
	if len(store.sensitiveResponses) != 0 {
		t.Fatal("expired sensitive envelope was retained")
	}
}

func TestSensitiveCompletionValidatesTerminalStatus(t *testing.T) {
	provider := sensitiveTestKeyProvider(t, 0x55, 1)
	store := NewMemoryStoreWithSensitiveResponseKeys(time.Now, provider)
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("project:prj_api"))
	fingerprint := memoryFingerprint(http.MethodPost, "/token-mint", `{}`)
	reserved, err := store.Reserve(ctx, Key("bad-status"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteSensitiveSuccess(
		ctx,
		reserved.Reference(),
		StoredResponse{StatusCode: http.StatusBadRequest},
	); !errors.Is(err, ErrReservationFenced) {
		t.Fatalf("CompleteSensitiveSuccess(400) = %v, want ErrReservationFenced", err)
	}
}
