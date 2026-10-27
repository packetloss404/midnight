// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
)

func startWorkerAuditCheckpoints(ctx context.Context, pool *pg.Pool, cfg workerConfig) (func(), error) {
	if !cfg.AuditCheckpoints {
		fmt.Fprintln(os.Stderr, "audit checkpoint scheduling disabled by configuration")
		return func() {}, nil
	}
	setupCtx, cancelSetup := context.WithTimeout(ctx, audit.DefaultCheckpointTimeout)
	defer cancelSetup()
	chain := audit.NewPostgresChain(pool)
	key, err := chain.InitializeSigningKey(setupCtx, cfg.AuditKeyPath)
	if err != nil {
		return nil, fmt.Errorf("audit signing key unavailable (restore the configured private key; signed history must not regenerate it): %w", err)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			clear(key.Private)
		}
	}()
	if _, err := audit.PublishVerifyingKey(setupCtx, cfg.AuditKeyPath, key); err != nil {
		return nil, fmt.Errorf("audit public key publication: %w", err)
	}
	fmt.Fprintf(os.Stderr, "audit checkpoint signer fingerprint=%s\n", key.Fingerprint)
	checkpointer := audit.NewCheckpointer(chain, key)
	if _, _, err := checkpointer.SignDue(setupCtx); err != nil && !errors.Is(err, audit.ErrEmptyChain) {
		clear(key.Private)
		return nil, fmt.Errorf("initial audit checkpoint: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer clear(key.Private)
		err := (audit.CheckpointSchedule{Checkpointer: checkpointer, Report: func(_ audit.Checkpoint, _ bool, err error) {
			if err != nil && !errors.Is(err, audit.ErrEmptyChain) {
				fmt.Fprintf(os.Stderr, "audit checkpoint failed: %v\n", err)
			}
		}}).Run(runCtx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit checkpoint scheduler stopped: %v\n", err)
		}
	}()
	handedOff = true
	return func() { cancel(); <-done }, nil
}
