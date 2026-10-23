//go:build integration

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

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresSensitiveResponseReplayEncryptionAndExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		if idempotencyDockerUnavailable(err) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	runner := &dbpg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	provider, err := secrets.NewMemoryKeyProvider(bytes.Repeat([]byte{0x66}, 32), 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewPostgresStoreWithSensitiveResponseKeys(pool, provider)
	store.Now = func() time.Time { return now }
	requestCtx := scopedMemoryContext(
		UserActor("usr_alice"),
		Tenant("project:prj_api"),
	)
	fingerprint := memoryFingerprint(
		http.MethodPost,
		"/api/v1alpha/projects/{project_id}/automation-tokens",
		`{"name":"deploy"}`,
	)
	reserved, err := store.Reserve(
		requestCtx,
		Key("mint-request-1"),
		fingerprint,
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := StoredResponse{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type":   {"application/json"},
			"X-Secret-Proof": {"header-secret"},
		},
		Body: []byte(`{"access_token":"midn_sa_one_time_secret"}`),
	}
	completed, err := store.CompleteSensitiveSuccess(
		requestCtx,
		reserved.Reference(),
		want,
	)
	if err != nil {
		t.Fatalf("CompleteSensitiveSuccess() = %v", err)
	}
	if completed.Response == nil || !reflect.DeepEqual(*completed.Response, want) {
		t.Fatalf("completion response = %#v", completed.Response)
	}

	var (
		storage      string
		statusNull   bool
		bodyNull     bool
		headersNull  bool
		kekID        string
		wrappedDEK   []byte
		ciphertext   []byte
		envelopeForm string
		aadDigest    string
	)
	if err := pool.QueryRow(ctx, `
		SELECT response_storage,
		       response_status IS NULL,
		       response_body IS NULL,
		       response_headers IS NULL,
		       response_kek_id,
		       response_wrapped_dek,
		       response_ciphertext,
		       response_envelope_form,
		       response_aad_sha256
		  FROM idempotency_keys
		 WHERE actor=$1 AND tenant=$2 AND method=$3 AND path_template=$4
		   AND key=$5 AND generation=$6`,
		string(reserved.Actor), string(reserved.Tenant),
		fingerprint.Method, fingerprint.Path, string(reserved.Key),
		reserved.Generation,
	).Scan(
		&storage, &statusNull, &bodyNull, &headersNull, &kekID,
		&wrappedDEK, &ciphertext, &envelopeForm, &aadDigest,
	); err != nil {
		t.Fatal(err)
	}
	if storage != responseStorageEnvelope ||
		!statusNull || !bodyNull || !headersNull ||
		kekID == "" || len(wrappedDEK) == 0 || len(ciphertext) == 0 ||
		envelopeForm != "midnight-envelope-v1" || len(aadDigest) != 64 {
		t.Fatalf(
			"invalid encrypted persistence: storage=%q nulls=%t/%t/%t kek=%q wrapped=%d ciphertext=%d form=%q aad=%q",
			storage, statusNull, bodyNull, headersNull, kekID,
			len(wrappedDEK), len(ciphertext), envelopeForm, aadDigest,
		)
	}
	persistedBytes := string(wrappedDEK) + string(ciphertext)
	for _, secret := range []string{"midn_sa_one_time_secret", "header-secret"} {
		if strings.Contains(persistedBytes, secret) {
			t.Fatalf("encrypted database fields contain plaintext %q", secret)
		}
	}
	down, err := runner.Down(ctx)
	if err != nil || down == nil || down.Version != 44 {
		t.Fatalf("Down() operation deployment lineage migration = %#v, err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 43 {
		t.Fatalf("Down() invitation replacement migration = %#v, err=%v", down, err)
	}
	if _, err := runner.Down(ctx); err == nil ||
		!strings.Contains(err.Error(), "cannot roll back sensitive idempotency responses while encrypted replay history exists") {
		t.Fatalf("Down() with encrypted replay = %v, want guarded refusal", err)
	}

	replay, err := store.Reserve(
		requestCtx,
		reserved.Key,
		fingerprint,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("Reserve(replay) = %v", err)
	}
	if replay.Generation != reserved.Generation ||
		replay.Response == nil ||
		!reflect.DeepEqual(*replay.Response, want) {
		t.Fatalf("replay = %#v", replay)
	}

	unkeyed := NewPostgresStore(pool)
	unkeyed.Now = store.Now
	if _, err := unkeyed.Reserve(
		requestCtx,
		reserved.Key,
		fingerprint,
		time.Hour,
	); !errors.Is(err, ErrSensitiveResponseUnavailable) {
		t.Fatalf("unkeyed replay = %v, want ErrSensitiveResponseUnavailable", err)
	}

	tamperedGeneration := "fedcba9876543210fedcba9876543210"
	if _, err := pool.Exec(ctx, `
		UPDATE idempotency_keys
		   SET generation=$1
		 WHERE actor=$2 AND tenant=$3 AND method=$4 AND path_template=$5
		   AND key=$6 AND generation=$7`,
		tamperedGeneration,
		string(reserved.Actor), string(reserved.Tenant),
		fingerprint.Method, fingerprint.Path, string(reserved.Key),
		reserved.Generation,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(
		requestCtx,
		reserved.Key,
		fingerprint,
		time.Hour,
	); !errors.Is(err, ErrSensitiveResponseUnavailable) {
		t.Fatalf("metadata-tampered replay = %v, want ErrSensitiveResponseUnavailable", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE idempotency_keys
		   SET generation=$1
		 WHERE actor=$2 AND tenant=$3 AND method=$4 AND path_template=$5
		   AND key=$6 AND generation=$7`,
		reserved.Generation,
		string(reserved.Actor), string(reserved.Tenant),
		fingerprint.Method, fingerprint.Path, string(reserved.Key),
		tamperedGeneration,
	); err != nil {
		t.Fatal(err)
	}

	if _, _, err := provider.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(
		requestCtx,
		reserved.Key,
		fingerprint,
		time.Hour,
	); !errors.Is(err, ErrSensitiveResponseUnavailable) {
		t.Fatalf("replay after KEK purge = %v, want ErrSensitiveResponseUnavailable", err)
	}

	now = now.Add(time.Hour)
	fresh, err := store.Reserve(
		requestCtx,
		reserved.Key,
		fingerprint,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("Reserve(after expiry) = %v", err)
	}
	if fresh.Generation == reserved.Generation || fresh.State != StateReserved {
		t.Fatalf("fresh reservation = %#v", fresh)
	}
	var (
		expiredState    string
		expiredStorage  string
		expiredStatusOK bool
		expiredBodyOK   bool
		expiredHeaderOK bool
		expiredKEKOK    bool
		expiredWrapOK   bool
		expiredCipherOK bool
		expiredFormOK   bool
		expiredAADOK    bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT state, response_storage,
		       response_status IS NULL, response_body IS NULL,
		       response_headers IS NULL, response_kek_id IS NULL,
		       response_wrapped_dek IS NULL, response_ciphertext IS NULL,
		       response_envelope_form IS NULL, response_aad_sha256 IS NULL
		  FROM idempotency_keys
		 WHERE actor=$1 AND tenant=$2 AND method=$3 AND path_template=$4
		   AND key=$5 AND generation=$6`,
		string(reserved.Actor), string(reserved.Tenant),
		fingerprint.Method, fingerprint.Path, string(reserved.Key),
		reserved.Generation,
	).Scan(
		&expiredState, &expiredStorage,
		&expiredStatusOK, &expiredBodyOK, &expiredHeaderOK, &expiredKEKOK,
		&expiredWrapOK, &expiredCipherOK, &expiredFormOK, &expiredAADOK,
	); err != nil {
		t.Fatal(err)
	}
	if expiredState != string(StateExpired) ||
		expiredStorage != responseStoragePlaintext ||
		!expiredStatusOK || !expiredBodyOK || !expiredHeaderOK ||
		!expiredKEKOK || !expiredWrapOK || !expiredCipherOK ||
		!expiredFormOK || !expiredAADOK {
		t.Fatalf(
			"expired sensitive envelope retained: state=%q storage=%q nulls=%t/%t/%t/%t/%t/%t/%t/%t",
			expiredState, expiredStorage, expiredStatusOK, expiredBodyOK,
			expiredHeaderOK, expiredKEKOK, expiredWrapOK, expiredCipherOK,
			expiredFormOK, expiredAADOK,
		)
	}
	down, err = runner.Down(ctx)
	if err != nil {
		t.Fatalf("Down() after envelope erasure = %v", err)
	}
	if down == nil || down.Version != 42 {
		t.Fatalf("Down() migration = %#v, want version 42", down)
	}
}

func idempotencyDockerUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "docker") &&
		(strings.Contains(message, "not running") ||
			strings.Contains(message, "cannot connect") ||
			strings.Contains(message, "no such file") ||
			strings.Contains(message, "permission denied"))
}
