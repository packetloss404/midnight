//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestWorkerInstalledAuditCheckpointsPersistAndRefuseLostHistoryKey(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("midnight"), tcpostgres.WithUsername("midnight_test"), tcpostgres.WithPassword("midnight_test"), tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MIDNIGHT_TESTCONTAINERS_DIRECT_IP") == "1" { // not-a-failure-code
		ip, err := container.ContainerIP(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Host = net.JoinHostPort(ip, "5432")
		dsn = parsed.String()
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	chain := audit.NewPostgresChain(pool)
	if _, err := chain.Append(ctx, audit.Row{ActorType: "system", ResourceKind: "audit", Action: "worker.installed.fixture"}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private", "signing-key.pem")
	cfg := workerConfig{AuditCheckpoints: true, AuditKeyPath: keyPath}
	stop, err := startWorkerAuditCheckpoints(ctx, pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	first, err := chain.LatestCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, err := audit.LoadVerifyingKeyFile(filepath.Join(filepath.Dir(keyPath), "public-keys", first.SigningKeyFingerprint+".pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.VerifyCheckpoint(first, key); err != nil {
		t.Fatal(err)
	}
	// Explicitly pin the public key outside the worker's distribution folder,
	// then exercise authenticated HTTP evidence/export against the real chain.
	trustDir := t.TempDir()
	publicBytes, err := os.ReadFile(filepath.Join(filepath.Dir(keyPath), "public-keys", first.SigningKeyFingerprint+".pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trustDir, "pinned.pem"), publicBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	const operatorToken = "integration-audit-operator-recovery-token"
	server := httptest.NewServer(api.NewRouterWithDataStack(api.DataStack{APIBearerToken: operatorToken, AuditChain: chain, AuditVerifyingKeys: audit.DirectoryKeyring{Path: trustDir}}))
	defer server.Close()
	get := func(path string) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+operatorToken)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("audit HTTP status=%d body=%s", response.StatusCode, body)
		}
		return body
	}
	var evidence audit.Evidence
	if err := json.Unmarshal(get("/api/v1alpha/audit/evidence"), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.CheckpointCount != 1 || evidence.Status != "ready" || evidence.Verification == nil || !evidence.Verification.Valid {
		t.Fatalf("installed HTTP signed evidence=%#v", evidence)
	}
	exported := get("/api/v1alpha/audit/export")
	source, err := audit.NewJSONLSourceFromReader(bytes.NewReader(exported))
	if err != nil {
		t.Fatal(err)
	}
	report, err := audit.Verify(ctx, source, []audit.VerifyingKey{key})
	if err != nil || !report.Valid || report.CheckpointsChecked != 1 {
		t.Fatalf("installed HTTP export report=%#v err=%v", report, err)
	}
	stop, err = startWorkerAuditCheckpoints(ctx, pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	checkpoints, err := chain.ListCheckpoints(ctx)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("restart duplicated checkpoint count=%d err=%v", len(checkpoints), err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkerAuditCheckpoints(ctx, pool, cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost signed-history key not refused: %v", err)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost key silently recreated: %v", err)
	}
}
