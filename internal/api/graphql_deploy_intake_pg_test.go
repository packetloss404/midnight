// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestGraphQLDeployIntakeRejectsUnauthenticatedActorBeforeMinting(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(pool, audit.NewPostgresChain(pool), func() time.Time {
		return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	})
	var minted atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID { minted.Add(1); return "op_must_not_mint" }
	coordinator.DeploymentIDFactory = func() string { minted.Add(1); return "dep_must_not_mint" }
	coordinator.OutboxIDFactory = func() string { minted.Add(1); return "dox_must_not_mint" }
	coordinator.EventIDFactory = func() string { minted.Add(1); return "dev_must_not_mint" }

	for _, actor := range []idempotency.Actor{idempotency.ActorLegacyOperator, idempotency.ActorLocalDeveloper, idempotency.UserActor("usr_spoofed")} {
		ctx := idempotency.WithActor(context.Background(), actor)
		if _, err := coordinator.DeployGraphQL(ctx, validGraphQLDeployRequest()); err == nil || !strings.Contains(err.Error(), "authenticated recovery-operator") {
			t.Fatalf("actor %q error=%v, want fail-closed authentication", actor, err)
		}
	}
	if minted.Load() != 0 {
		t.Fatalf("unauthenticated requests minted %d durable identities", minted.Load())
	}
}

func TestGraphQLDeployIntakeFailsClosedAcrossPostgresAuthorities(t *testing.T) {
	authorityPool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(authorityPool, audit.NewPostgresChain(authorityPool), time.Now)
	coordinator.Outbox = NewPostgresDeployOutbox(&pg.Pool{})
	ctx := idempotency.WithActor(context.Background(), idempotency.ActorRecoveryOperator)
	if _, err := coordinator.DeployGraphQL(ctx, validGraphQLDeployRequest()); err == nil || !strings.Contains(err.Error(), "does not share one postgres authority") {
		t.Fatalf("mismatched authority error=%v", err)
	}
}

func TestGraphQLDeployIntakeRequiresKeyAndCanonicalPostFingerprintBeforeMinting(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := NewPostgresTenantDeployIntakeCoordinator(pool, audit.NewPostgresChain(pool), time.Now)
	var minted atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID { minted.Add(1); return "op_must_not_mint" }
	coordinator.DeploymentIDFactory = func() string { minted.Add(1); return "dep_must_not_mint" }
	coordinator.OutboxIDFactory = func() string { minted.Add(1); return "dox_must_not_mint" }
	coordinator.EventIDFactory = func() string { minted.Add(1); return "dev_must_not_mint" }
	ctx := idempotency.WithActor(context.Background(), idempotency.ActorRecoveryOperator)

	missingKey := validGraphQLDeployRequest()
	missingKey.Key = ""
	if _, err := coordinator.DeployGraphQL(ctx, missingKey); err == nil {
		t.Fatal("production GraphQL deploy accepted an empty idempotency key")
	}
	getFingerprint := validGraphQLDeployRequest()
	getFingerprint.Fingerprint.Method = http.MethodGet
	if _, err := coordinator.DeployGraphQL(ctx, getFingerprint); err == nil {
		t.Fatal("production GraphQL deploy accepted a GET mutation fingerprint")
	}
	if minted.Load() != 0 {
		t.Fatalf("invalid requests minted %d durable identities", minted.Load())
	}
}

func TestGraphQLDeployServiceRequiresExplicitDurableCapability(t *testing.T) {
	services := &ServicesAPI{}
	_, srvErr := services.EnqueueGraphQLDeployService(context.Background(), validGraphQLDeployRequest())
	if srvErr == nil || srvErr.Status != http.StatusServiceUnavailable || srvErr.Code != "CAPABILITY_UNAVAILABLE" {
		t.Fatalf("service error=%+v, want explicit unavailable durable capability", srvErr)
	}
}

func validGraphQLDeployRequest() GraphQLDeployRequest {
	body := []byte(`{"operation_name":"Deploy","query":"mutation Deploy ...","variables":{"image":"a"}}`)
	return GraphQLDeployRequest{
		ServiceID: "svc_graphql",
		Intent: DeployIntakeRequest{
			EnvironmentID: "env_graphql",
			Image:         "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Key: "graphql-durable-key",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: GraphQLDeployFingerprintPath, BodySHA256: idempotency.BodyHashSHA256(body),
		},
		EncodeResult: func(GraphQLDeployAccepted) ([]byte, error) { return []byte(`{"id":"dep_graphql"}`), nil },
	}
}
