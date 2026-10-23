// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package idempotency

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func scopedMemoryContext(actor Actor, tenant Tenant) context.Context {
	return WithTenant(WithActor(context.Background(), actor), tenant)
}

func memoryFingerprint(method, path, body string) RequestFingerprint {
	return RequestFingerprint{
		Method:     method,
		Path:       path,
		BodySHA256: BodyHashSHA256([]byte(body)),
	}
}

func TestMemoryReserveCreatesScopedPendingReservation(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/api/v1alpha/projects", `{"slug":"api"}`)

	reservation, err := store.Reserve(ctx, Key("demo-1"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if reservation.State != StateReserved {
		t.Fatalf("state = %q, want %q", reservation.State, StateReserved)
	}
	if reservation.Actor != UserActor("usr_alice") || reservation.Tenant != Tenant("wrk_acme") {
		t.Fatalf("scope = actor %q tenant %q", reservation.Actor, reservation.Tenant)
	}
	if reservation.Generation == "" {
		t.Fatal("reservation generation is empty")
	}
	if reservation.Reference() != (Reference{
		Key:        "demo-1",
		Actor:      UserActor("usr_alice"),
		Tenant:     Tenant("wrk_acme"),
		Method:     fingerprint.Method,
		Path:       fingerprint.Path,
		BodySHA256: fingerprint.BodySHA256,
		Generation: reservation.Generation,
	}) {
		t.Fatalf("reference = %#v", reservation.Reference())
	}
	if reservation.HasOperation() {
		t.Fatal("new reservation unexpectedly has an operation")
	}
}

func TestMemoryReserveInFlightPreservesGeneration(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)

	first, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("first Reserve() = %v", err)
	}
	second, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if !errors.Is(err, ErrRequestInProgress) {
		t.Fatalf("second Reserve() error = %v, want ErrRequestInProgress", err)
	}
	if second.Generation != first.Generation || second.Reference() != first.Reference() {
		t.Fatalf("in-flight replay changed generation: first=%#v second=%#v", first.Reference(), second.Reference())
	}
}

func TestMemorySameClientKeyIsIndependentAcrossCompleteScope(t *testing.T) {
	store := NewMemoryStore()
	key := Key("shared-key")
	base := memoryFingerprint(http.MethodPost, "/api/v1alpha/projects", `{}`)
	scopes := []struct {
		name        string
		ctx         context.Context
		fingerprint RequestFingerprint
	}{
		{
			name:        "alice-acme-projects",
			ctx:         scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme")),
			fingerprint: base,
		},
		{
			name:        "bob-acme-projects",
			ctx:         scopedMemoryContext(UserActor("usr_bob"), Tenant("wrk_acme")),
			fingerprint: base,
		},
		{
			name:        "alice-other-projects",
			ctx:         scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_other")),
			fingerprint: base,
		},
		{
			name:        "alice-acme-services",
			ctx:         scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme")),
			fingerprint: memoryFingerprint(http.MethodPost, "/api/v1alpha/services", `{}`),
		},
		{
			name:        "alice-acme-get-projects",
			ctx:         scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme")),
			fingerprint: memoryFingerprint(http.MethodPatch, "/api/v1alpha/projects", `{}`),
		},
	}

	reservations := make([]Reservation, len(scopes))
	for i, scope := range scopes {
		reservation, err := store.Reserve(scope.ctx, key, scope.fingerprint, time.Hour)
		if err != nil {
			t.Fatalf("%s Reserve() = %v", scope.name, err)
		}
		reservations[i] = reservation
	}

	if _, err := store.CompleteSuccess(
		scopes[0].ctx,
		reservations[0].Reference(),
		StoredResponse{StatusCode: http.StatusCreated},
	); err != nil {
		t.Fatalf("CompleteSuccess() = %v", err)
	}
	for i := 1; i < len(scopes); i++ {
		loaded, err := store.Load(scopes[i].ctx, key, scopes[i].fingerprint)
		if err != nil {
			t.Fatalf("%s Load() = %v", scopes[i].name, err)
		}
		if loaded.State != StateReserved || loaded.Response != nil {
			t.Fatalf("%s was changed through another scope: %#v", scopes[i].name, loaded)
		}
	}
}

func TestMemoryDifferentBodyInFlightReturnsRequestInProgress(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	firstFingerprint := memoryFingerprint(http.MethodPost, "/p", `{"a":1}`)
	secondFingerprint := memoryFingerprint(http.MethodPost, "/p", `{"a":2}`)

	first, err := store.Reserve(ctx, Key("k"), firstFingerprint, time.Hour)
	if err != nil {
		t.Fatalf("first Reserve() = %v", err)
	}
	existing, err := store.Reserve(ctx, Key("k"), secondFingerprint, time.Hour)
	if !errors.Is(err, ErrRequestInProgress) {
		t.Fatalf("second Reserve() error = %v, want ErrRequestInProgress", err)
	}
	if existing.Generation != first.Generation || existing.Fingerprint != firstFingerprint {
		t.Fatalf("conflicting in-flight request replaced reservation: %#v", existing)
	}
}

func TestMemoryTerminalReplayPreservesGenerationAndResponse(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{"a":1}`)

	reservation, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	running, err := store.AttachOperation(ctx, reservation.Reference(), "op_01")
	if err != nil {
		t.Fatalf("AttachOperation() = %v", err)
	}
	completed, err := store.CompleteSuccess(ctx, running.Reference(), StoredResponse{
		StatusCode: http.StatusAccepted,
		Body:       []byte(`{"id":"op_01"}`),
	})
	if err != nil {
		t.Fatalf("CompleteSuccess() = %v", err)
	}

	replay, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("replay Reserve() = %v", err)
	}
	if replay.State != StateSucceeded || replay.Generation != completed.Generation {
		t.Fatalf("replay = %#v, completed = %#v", replay, completed)
	}
	if replay.Response == nil ||
		replay.Response.StatusCode != http.StatusAccepted ||
		string(replay.Response.Body) != `{"id":"op_01"}` {
		t.Fatalf("replay response = %#v", replay.Response)
	}
}

func TestMemoryTerminalDifferentBodyReturnsConflictWithoutMutation(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	firstFingerprint := memoryFingerprint(http.MethodPost, "/p", `{"a":1}`)
	secondFingerprint := memoryFingerprint(http.MethodPost, "/p", `{"a":2}`)

	reservation, err := store.Reserve(ctx, Key("k"), firstFingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteSuccess(ctx, reservation.Reference(), StoredResponse{StatusCode: http.StatusCreated})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.Reserve(ctx, Key("k"), secondFingerprint, time.Hour)
	if !errors.Is(err, ErrKeyReusedDifferentBody) {
		t.Fatalf("Reserve() = %v, want ErrKeyReusedDifferentBody", err)
	}
	if existing.Generation != completed.Generation ||
		existing.State != StateSucceeded ||
		existing.Fingerprint != firstFingerprint {
		t.Fatalf("different body changed terminal reservation: %#v", existing)
	}
	if _, err := store.Load(ctx, Key("k"), secondFingerprint); !errors.Is(err, ErrKeyReusedDifferentBody) {
		t.Fatalf("Load(different body) = %v, want ErrKeyReusedDifferentBody", err)
	}
}

func TestMemoryFailedTerminalReplayReturnsStoredFailure(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodDelete, "/p/{id}", `{}`)
	headers := http.Header{"Content-Type": []string{"application/json"}}

	reservation, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.CompleteFailure(ctx, reservation.Reference(), StoredResponse{
		StatusCode: http.StatusUnprocessableEntity,
		Header:     headers,
		Body:       []byte(`{"error":{"code":"X"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if replay.State != StateFailed ||
		replay.Generation != failed.Generation ||
		replay.Response == nil ||
		replay.Response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("failed replay = %#v", replay)
	}
}

func TestMemoryReferenceCannotCrossActorOrTenant(t *testing.T) {
	store := NewMemoryStore()
	alice := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	bob := scopedMemoryContext(UserActor("usr_bob"), Tenant("wrk_acme"))
	otherTenant := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_other"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)
	reservation, err := store.Reserve(alice, Key("alice-only"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.AttachOperation(bob, reservation.Reference(), "op_bob"); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("bob AttachOperation() = %v, want ErrReservationNotFound", err)
	}
	if _, err := store.CompleteFailure(otherTenant, reservation.Reference(), StoredResponse{StatusCode: http.StatusBadRequest}); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("other tenant CompleteFailure() = %v, want ErrReservationNotFound", err)
	}
	loaded, err := store.Load(alice, reservation.Key, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateReserved || loaded.OperationID != "" || loaded.Response != nil {
		t.Fatalf("cross-scope transition changed reservation: %#v", loaded)
	}
}

func TestMemoryReferenceFencesEveryImmutableField(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p/{id}", `{"a":1}`)
	reservation, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	original := reservation.Reference()
	mutations := map[string]func(*Reference){
		"key":        func(ref *Reference) { ref.Key = "other" },
		"actor":      func(ref *Reference) { ref.Actor = UserActor("usr_bob") },
		"tenant":     func(ref *Reference) { ref.Tenant = "wrk_other" },
		"method":     func(ref *Reference) { ref.Method = http.MethodPatch },
		"path":       func(ref *Reference) { ref.Path = "/different/{id}" },
		"body":       func(ref *Reference) { ref.BodySHA256 = BodyHashSHA256([]byte(`{"a":2}`)) },
		"generation": func(ref *Reference) { ref.Generation = "stale-generation" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			ref := original
			mutate(&ref)
			if _, err := store.AttachOperation(ctx, ref, "op_forged"); !errors.Is(err, ErrReservationNotFound) {
				t.Fatalf("AttachOperation() = %v, want ErrReservationNotFound", err)
			}
		})
	}
	loaded, err := store.Load(ctx, reservation.Key, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateReserved || loaded.OperationID != "" {
		t.Fatalf("forged reference mutated reservation: %#v", loaded)
	}
}

func TestMemoryTerminalCompletionCannotBeOverwritten(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)

	reservation, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	successHeaders := http.Header{"X-Result": []string{"first"}}
	first, err := store.CompleteSuccess(ctx, reservation.Reference(), StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     successHeaders,
		Body:       []byte(`{"result":"first"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CompleteFailure(ctx, reservation.Reference(), StoredResponse{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"X-Result": []string{"second"}},
		Body:       []byte(`{"result":"second"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != StateSucceeded ||
		second.Generation != first.Generation ||
		second.Response == nil ||
		second.Response.StatusCode != http.StatusCreated ||
		second.Response.Header.Get("X-Result") != "first" ||
		string(second.Response.Body) != `{"result":"first"}` {
		t.Fatalf("terminal completion was overwritten: %#v", second)
	}

	// Returned values are detached from storage as well.
	second.Response.Body[0] = 'X'
	second.Response.Header.Set("X-Result", "mutated")
	replay, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Response == nil ||
		replay.Response.Header.Get("X-Result") != "first" ||
		string(replay.Response.Body) != `{"result":"first"}` {
		t.Fatalf("caller mutated stored terminal response: %#v", replay.Response)
	}
}

func TestMemoryRunningOperationCannotBeRebound(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)

	reservation, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.AttachOperation(ctx, reservation.Reference(), "op_first")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.AttachOperation(ctx, running.Reference(), "op_second")
	if !errors.Is(err, ErrKeyReused) {
		t.Fatalf("second AttachOperation() = %v, want ErrKeyReused", err)
	}
	if existing.OperationID != "op_first" || existing.State != StateRunning {
		t.Fatalf("operation binding was overwritten: %#v", existing)
	}
}

func TestMemoryExpiryCreatesNewGenerationAndFencesStaleReference(t *testing.T) {
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	current := base
	store := NewMemoryStoreWithClock(func() time.Time { return current })
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)

	old, err := store.Reserve(ctx, Key("k"), fingerprint, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	current = base.Add(2 * time.Minute)
	fresh, err := store.Reserve(ctx, Key("k"), fingerprint, time.Minute)
	if err != nil {
		t.Fatalf("Reserve() after expiry = %v", err)
	}
	if fresh.Generation == "" || fresh.Generation == old.Generation {
		t.Fatalf("generation was reused across expiry: old=%q fresh=%q", old.Generation, fresh.Generation)
	}

	if _, err := store.AttachOperation(ctx, old.Reference(), "op_stale"); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("stale AttachOperation() = %v, want ErrReservationNotFound", err)
	}
	if _, err := store.CompleteSuccess(ctx, old.Reference(), StoredResponse{StatusCode: http.StatusCreated}); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("stale CompleteSuccess() = %v, want ErrReservationNotFound", err)
	}
	if err := store.Expire(ctx, old.Reference()); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("stale Expire() = %v, want ErrReservationNotFound", err)
	}
	loaded, err := store.Load(ctx, fresh.Key, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != fresh.Generation ||
		loaded.State != StateReserved ||
		loaded.OperationID != "" ||
		loaded.Response != nil {
		t.Fatalf("stale reference changed fresh generation: %#v", loaded)
	}
}

func TestMemoryManualExpiryCannotABADeleteReplacement(t *testing.T) {
	store := NewMemoryStore()
	ctx := scopedMemoryContext(UserActor("usr_alice"), Tenant("wrk_acme"))
	fingerprint := memoryFingerprint(http.MethodPost, "/p", `{}`)

	old, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Expire(ctx, old.Reference()); err != nil {
		t.Fatalf("Expire(old) = %v", err)
	}
	fresh, err := store.Reserve(ctx, Key("k"), fingerprint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation == old.Generation {
		t.Fatalf("manual expiry reused generation %q", fresh.Generation)
	}
	if err := store.Expire(ctx, old.Reference()); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("stale Expire(old) = %v, want ErrReservationNotFound", err)
	}
	loaded, err := store.Load(ctx, fresh.Key, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != fresh.Generation {
		t.Fatalf("stale expiry deleted replacement: %#v", loaded)
	}
}

func TestValidateKeyEnforcesShape(t *testing.T) {
	cases := []struct {
		name    string
		key     Key
		wantErr error
	}{
		{name: "empty", key: "", wantErr: ErrInvalidKey},
		{name: "whitespace", key: "has space", wantErr: ErrInvalidKey},
		{name: "non-printable", key: "tab\there", wantErr: ErrInvalidKey},
		{name: "too long", key: Key(string(make([]byte, 256))), wantErr: ErrInvalidKey},
		{name: "ok", key: "deploy_01HZX_opaque.token-XYZ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKey(tc.key)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateKey(%q) = %v, want %v", tc.key, err, tc.wantErr)
			}
		})
	}
}

func TestStoredResponseCloneIsDeep(t *testing.T) {
	headers := http.Header{}
	headers.Set("Idempotent-Replayed", "true")
	original := StoredResponse{StatusCode: http.StatusOK, Header: headers, Body: []byte(`{"x":1}`)}
	clone := original.Clone()

	original.Body[0] = 'X'
	original.Header.Set("Idempotent-Replayed", "false")

	if clone.Body[0] != '{' {
		t.Fatalf("clone body shares backing storage: %q", string(clone.Body))
	}
	if clone.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("clone header reflects original mutation: %q", clone.Header.Get("Idempotent-Replayed"))
	}
}
