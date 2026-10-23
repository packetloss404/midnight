// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
//
// Command midnight-github-status-worker drains the unposted GitHub commit
// status queue and posts each row to GitHub's statuses API with retry/backoff.
//
// It is the runtime counterpart to the enqueue path (deploy start -> pending,
// terminal deploy state -> success/failure). The enqueue transaction and
// terminal-state trigger are owned by the API/database layer. This binary
// depends only on:
//
//   - the redacting publisher in internal/integrations/github
//     (AppClient.PublishInstallationCommitStatus), and
//   - the leased Postgres StatusStore.
//
// Environment:
//
//	MIDNIGHT_DB_DSN / MIDNIGHT_DB_DSN_FILE     Postgres DSN (via internal/db/pg)
//	MIDNIGHT_GITHUB_APP_ID                     GitHub App id
//	MIDNIGHT_GITHUB_PRIVATE_KEY_PATH           PEM private key path
//	MIDNIGHT_GITHUB_API_BASE_URL               optional API base override
//	MIDNIGHT_GITHUB_STATUS_POLL_INTERVAL       idle poll interval (default 5s)
//	MIDNIGHT_GITHUB_STATUS_BATCH_SIZE          rows per pass (default 25)
//	MIDNIGHT_GITHUB_STATUS_LEASE_DURATION      per-attempt lease (default 30s)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	github "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/version"
)

const (
	envPollInterval  = "MIDNIGHT_GITHUB_STATUS_POLL_INTERVAL"
	envBatchSize     = "MIDNIGHT_GITHUB_STATUS_BATCH_SIZE"
	envLeaseDuration = "MIDNIGHT_GITHUB_STATUS_LEASE_DURATION" // not-a-failure-code
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.Version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	loaded, err := pg.LoadDSN()
	if err != nil {
		return fmt.Errorf("load dsn: %w", err)
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: loaded.DSN, ConnectTimeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer pool.Close()

	cfg, err := github.LoadTokenMintingAppConfig(os.Getenv, os.ReadFile)
	if err != nil {
		return fmt.Errorf("load github app config: %w", err)
	}
	client, err := github.NewAppClient(cfg)
	if err != nil {
		return fmt.Errorf("build github app client: %w", err)
	}

	worker := github.StatusDrainWorker{
		Store:         github.NewPostgresStatusStore(pool),
		Publisher:     client,
		BatchSize:     intFromEnv(envBatchSize, 25),
		PollInterval:  durationFromEnv(envPollInterval, 5*time.Second),
		LeaseDuration: durationFromEnv(envLeaseDuration, 30*time.Second),
		Logf:          log.Printf,
	}
	commentWorker := github.PreviewCommentDrainWorker{
		Store:         &github.PostgresPreviewCommentStore{Pool: pool},
		Publisher:     client,
		BatchSize:     intFromEnv(envBatchSize, 25),
		PollInterval:  durationFromEnv(envPollInterval, 5*time.Second),
		LeaseDuration: durationFromEnv(envLeaseDuration, 30*time.Second),
		Logf:          log.Printf,
	}
	log.Printf("midnight-github-status-worker: draining statuses and preview comments (dsn source=%s)", loaded.Source)
	errCh := make(chan error, 2)
	go func() { errCh <- worker.Run(ctx) }()
	go func() { errCh <- commentWorker.Run(ctx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func intFromEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
