// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type tenantDeploySafeRetryError struct{}

func (tenantDeploySafeRetryError) Error() string     { return "connection write did not start" }
func (tenantDeploySafeRetryError) SafeToRetry() bool { return true }

func TestTenantDeployCoordinatorClassifiesOnlyDependencyFailuresAsUnavailable(t *testing.T) {
	cases := []struct {
		name            string
		err             error
		wantUnavailable bool
	}{
		{name: "safe connection retry", err: tenantDeploySafeRetryError{}, wantUnavailable: true},
		{name: "closed pool", err: puddle.ErrClosedPool, wantUnavailable: true},
		{name: "connect error", err: &pgconn.ConnectError{Config: &pgconn.Config{}}, wantUnavailable: true},
		{name: "server cannot connect", err: &pgconn.PgError{Code: "57P03"}, wantUnavailable: true},
		{name: "server connection exception", err: &pgconn.PgError{Code: "08006"}, wantUnavailable: true},
		{name: "exhausted serialization", err: &pgconn.PgError{Code: "40001"}, wantUnavailable: true},
		{name: "constraint invariant", err: &pgconn.PgError{Code: "23505"}, wantUnavailable: false},
		{name: "internal invariant", err: errors.New("active pointer invariant"), wantUnavailable: false},
		{name: "authorization", err: identity.ErrAuthorizationDenied, wantUnavailable: false},
		{name: "idempotency conflict", err: idempotency.ErrKeyReused, wantUnavailable: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classified := classifyTenantDeployCoordinatorError(tc.err)
			if got := errors.Is(classified, ErrTenantDeployIntakeUnavailable); got != tc.wantUnavailable {
				t.Fatalf("classified error=%v unavailable=%v, want %v", classified, got, tc.wantUnavailable)
			}
			if !errors.Is(classified, tc.err) {
				t.Fatalf("classified error %v did not preserve cause %v", classified, tc.err)
			}
		})
	}
}

func TestTenantDeployCoordinatorReturnsUnavailableForClosedProductionPool(t *testing.T) {
	pool, err := pg.Open(t.Context(), pg.PoolConfig{
		DSN: "postgres://midnight:midnight@127.0.0.1:1/midnight?sslmode=disable",
	})
	if err != nil {
		t.Fatalf("open lazy postgres pool: %v", err)
	}
	pool.Close()
	coordinator := NewPostgresTenantDeployIntakeCoordinator(
		pool,
		audit.NewPostgresChain(pool),
		func() time.Time { return time.Now().UTC() },
	)
	_, err = coordinator.DeployImmutableImage(t.Context(), validTenantImageDeployRequest())
	if !errors.Is(err, ErrTenantDeployIntakeUnavailable) ||
		!errors.Is(err, puddle.ErrClosedPool) {
		t.Fatalf("closed pool error = %v, want unavailable wrapping closed-pool cause", err)
	}
}

func TestTenantDeployIntakeFailsClosedAcrossPostgresAuthorities(t *testing.T) {
	authorityPool := &pg.Pool{}
	outboxPool := &pg.Pool{}
	coordinator := &PostgresTenantDeployIntakeCoordinator{
		Pool:                authorityPool,
		Idempotency:         idempotency.NewPostgresStore(authorityPool),
		Audit:               audit.NewPostgresChain(authorityPool),
		Outbox:              NewPostgresDeployOutbox(outboxPool),
		Now:                 func() time.Time { return time.Now().UTC() },
		OperationIDFactory:  func() orailtypes.ID { return "op_must_not_be_minted" },
		DeploymentIDFactory: func() string { return "dep_must_not_be_minted" },
		DomainIDFactory:     func() string { return "dom_must_not_be_minted" },
		OutboxIDFactory:     func() string { return "dox_must_not_be_minted" },
		EventIDFactory:      func() string { return "dev_must_not_be_minted" },
	}

	_, err := coordinator.DeployImmutableImage(context.Background(), validTenantImageDeployRequest())
	if err == nil || !strings.Contains(err.Error(), "does not share one postgres authority") {
		t.Fatalf("DeployImmutableImage error = %v, want fail-closed authority error", err)
	}
}

func TestTenantDeployIntakeRejectsNonImageAndServerOwnedIntentBeforeMinting(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(
		pool,
		audit.NewPostgresChain(pool),
		func() time.Time { return time.Now().UTC() },
	)
	var minted atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID {
		minted.Add(1)
		return "op_must_not_be_minted"
	}
	coordinator.DeploymentIDFactory = func() string {
		minted.Add(1)
		return "dep_must_not_be_minted"
	}
	coordinator.DomainIDFactory = func() string {
		minted.Add(1)
		return "dom_must_not_be_minted"
	}
	coordinator.OutboxIDFactory = func() string {
		minted.Add(1)
		return "dox_must_not_be_minted"
	}
	coordinator.EventIDFactory = func() string {
		minted.Add(1)
		return "dev_must_not_be_minted"
	}

	cases := []struct {
		name   string
		mutate func(*DeployIntakeRequest)
	}{
		{
			name: "structured source",
			mutate: func(intent *DeployIntakeRequest) {
				intent.Image = ""
				intent.Source = &DeploySourceIntent{Repo: "https://example.test/repo.git", Ref: "main"}
			},
		},
		{
			name: "route side effect",
			mutate: func(intent *DeployIntakeRequest) {
				intent.Route = &DeployRouteIntent{Host: "app.example.test", Public: true, TLS: true}
			},
		},
		{
			name: "client deployment id",
			mutate: func(intent *DeployIntakeRequest) {
				intent.DeploymentID = "dep_client"
			},
		},
		{
			name: "client previous active",
			mutate: func(intent *DeployIntakeRequest) {
				intent.PreviousActive = &DeployActiveInstanceRef{DeploymentID: "dep_client_active"}
			},
		},
		{
			name: "client mode",
			mutate: func(intent *DeployIntakeRequest) {
				intent.Mode = "deploy"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validTenantImageDeployRequest()
			tc.mutate(&request.Intent)
			if _, err := coordinator.DeployImmutableImage(context.Background(), request); err == nil {
				t.Fatal("DeployImmutableImage accepted unsupported intent")
			}
		})
	}
	if minted.Load() != 0 {
		t.Fatalf("invalid requests minted %d durable identities, want zero", minted.Load())
	}
}

func TestTenantDeployIntakeRejectsGeneratedRouteWithoutAppsDomainBeforeMinting(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(
		pool,
		audit.NewPostgresChain(pool),
		func() time.Time { return time.Now().UTC() },
	)
	var minted atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID {
		minted.Add(1)
		return "op_must_not_be_minted"
	}
	coordinator.DeploymentIDFactory = func() string {
		minted.Add(1)
		return "dep_must_not_be_minted"
	}
	coordinator.DomainIDFactory = func() string {
		minted.Add(1)
		return "dom_must_not_be_minted"
	}
	coordinator.OutboxIDFactory = func() string {
		minted.Add(1)
		return "dox_must_not_be_minted"
	}
	coordinator.EventIDFactory = func() string {
		minted.Add(1)
		return "dev_must_not_be_minted"
	}
	request := validTenantImageDeployRequest()
	request.Intent.Route = &DeployRouteIntent{Public: true}
	if _, err := coordinator.DeployImmutableImage(context.Background(), request); !errors.Is(err, ErrTenantGeneratedRouteUnavailable) {
		t.Fatalf("error=%v, want generated-route unavailable", err)
	}
	if minted.Load() != 0 {
		t.Fatalf("missing apps domain minted %d durable identities, want zero", minted.Load())
	}
}

func TestTenantDeployAcceptedStoredResponseMatchesOperationTransport(t *testing.T) {
	operation := orailtypes.Operation{
		ID:    "op_exact",
		Kind:  "deploy",
		State: orailtypes.OperationPending,
	}
	response, err := tenantDeployAcceptedStoredResponse(operation)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted ||
		response.Header.Get("Content-Type") != "application/json" ||
		response.Header.Get("Location") != v1alphaPrefix+"/operations/op_exact" ||
		response.Header.Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("stored response = %#v", response)
	}
	var decoded orailtypes.Operation
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != operation || !strings.HasSuffix(string(response.Body), "\n") {
		t.Fatalf("stored body = %q decoded=%#v", response.Body, decoded)
	}
}

func TestCanonicalImmutableImageDeploymentPersistsReplayDigest(t *testing.T) {
	image := "registry.example/web:release@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	record, err := canonicalDeploymentRecord(
		Service{ID: "svc_api", ProjectID: "prj_api"},
		DeployIntakeRequest{EnvironmentID: "env_api", Image: image},
		"op_api",
		"accepted-operation:op_api",
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.ImageRef != image ||
		record.ImageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("canonical deployment = %#v", record)
	}
}

func TestTenantDeployIntakeRequiresConcreteServiceIdempotencyPath(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(
		pool,
		audit.NewPostgresChain(pool),
		func() time.Time { return time.Now().UTC() },
	)
	request := validTenantImageDeployRequest()
	request.Fingerprint.Path = tenantDeployIntakePathTemplate

	if _, err := coordinator.DeployImmutableImage(context.Background(), request); !errors.Is(err, idempotency.ErrInvalidFingerprint) {
		t.Fatalf("templated path error = %v, want invalid fingerprint", err)
	}
}

func validTenantImageDeployRequest() TenantImageDeployRequest {
	body := []byte(`{"environment_id":"env_api","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	return TenantImageDeployRequest{
		Principal: identity.Principal{
			Kind:       "user",
			UserID:     "usr_alice",
			SessionID:  "ses_alice",
			AuthMethod: "password",
		},
		Session: identity.Session{
			ID:                "ses_alice",
			UserID:            "usr_alice",
			SessionGeneration: 1,
		},
		ServiceID: "svc_api",
		Intent: DeployIntakeRequest{
			EnvironmentID: "env_api",
			Image:         "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Key: "tenant-image-deploy-1",
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       tenantDeployIdempotencyPath("svc_api"),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: "req_tenant_image_deploy",
		SourceIP:  "192.0.2.50:443",
		UserAgent: "midnight-test",
	}
}
