// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"path/filepath"
	"testing"
)

func TestServerAuditTrustConfigurationRequiresExplicitAbsoluteDirectory(t *testing.T) {
	t.Setenv("MIDNIGHT_AUDIT_TRUSTED_PUBLIC_KEY_DIR", "") // not-a-failure-code
	cfg, err := parseServeConfig(nil)
	if err != nil || cfg.AuditKeyDir != "" {
		t.Fatalf("implicit audit trust dir=%q err=%v", cfg.AuditKeyDir, err)
	}
	path := filepath.Join(t.TempDir(), "pinned-public-keys")
	t.Setenv("MIDNIGHT_AUDIT_TRUSTED_PUBLIC_KEY_DIR", path) // not-a-failure-code
	cfg, err = parseServeConfig(nil)
	if err != nil || cfg.AuditKeyDir != path {
		t.Fatalf("configured audit trust dir=%q err=%v", cfg.AuditKeyDir, err)
	}
	if _, err := parseServeConfig([]string{"--audit-trusted-public-key-dir", "relative"}); err == nil {
		t.Fatal("relative trust directory accepted")
	}
}
