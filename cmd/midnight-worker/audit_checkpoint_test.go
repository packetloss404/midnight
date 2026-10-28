// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/audit"
)

func TestWorkerAuditCheckpointConfiguration(t *testing.T) {
	t.Setenv("MIDNIGHT_AUDIT_CHECKPOINT_ENABLED", "") // not-a-failure-code
	t.Setenv("MIDNIGHT_AUDIT_SIGNING_KEY_PATH", "")   // not-a-failure-code
	cfg, err := parseWorkerConfig(nil)
	if err != nil || !cfg.AuditCheckpoints || cfg.AuditKeyPath != audit.DefaultSigningKeyFile {
		t.Fatalf("checkpoint defaults=%#v err=%v", cfg, err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	t.Setenv("MIDNIGHT_AUDIT_SIGNING_KEY_PATH", path) // not-a-failure-code
	cfg, err = parseWorkerConfig(nil)
	if err != nil || cfg.AuditKeyPath != path {
		t.Fatalf("configured checkpoint key=%s err=%v", cfg.AuditKeyPath, err)
	}
	t.Setenv("MIDNIGHT_AUDIT_CHECKPOINT_ENABLED", "invalid") // not-a-failure-code
	if _, err := parseWorkerConfig(nil); err == nil {
		t.Fatal("invalid scheduler boolean accepted")
	}
	t.Setenv("MIDNIGHT_AUDIT_CHECKPOINT_ENABLED", "false") // not-a-failure-code
	cfg, err = parseWorkerConfig([]string{"--audit-signing-key-file", "relative"})
	if err != nil || cfg.AuditCheckpoints {
		t.Fatalf("explicit disable err=%v", err)
	}
	stop, err := startWorkerAuditCheckpoints(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
