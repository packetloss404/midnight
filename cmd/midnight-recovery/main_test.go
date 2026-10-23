// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/backup"
)

func TestCreateRequiresOfflineConfirmationBeforePostgresExecution(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "config")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	dsnPath := writeRecoveryTestFile(t, root, "dsn", "postgres://secret.example/midnight")
	passphrasePath := writeRecoveryTestFile(t, root, "passphrase", "correct horse battery staple")
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"create",
		"--id", "offline-required",
		"--destination", "file://" + filepath.Join(root, "destination"),
		"--dsn-file", dsnPath,
		"--passphrase-file", passphrasePath,
		"--root", source + "=etc/midnight",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "offline confirmation") {
		t.Fatalf("run(create) error = %v, want offline confirmation failure", err)
	}
	if strings.Contains(stdout.String()+stderr.String()+err.Error(), "secret.example") {
		t.Fatal("CLI output exposed the PostgreSQL DSN")
	}
}

func TestApplyRequiresExactBackupIDConfirmation(t *testing.T) {
	root := t.TempDir()
	stagePath := filepath.Join(root, "stage.json")
	value, err := json.Marshal(backup.PlatformRestoreStage{
		Manifest: backup.PlatformManifest{ID: "backup-expected"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagePath, value, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	err = run(context.Background(), []string{
		"apply",
		"--stage-file", stagePath,
		"--target-root", root,
		"--offline-confirmed",
		"--confirm-restore", "backup-wrong",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("run(apply) error = %v, want exact confirmation failure", err)
	}
}

func TestParseRootsPreservesExplicitMapping(t *testing.T) {
	roots, err := parseRoots([]string{"/etc/midnight=etc/midnight", "/var/lib/registry=registry"})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 ||
		roots[0].Source != "/etc/midnight" || roots[0].ArchivePath != "etc/midnight" ||
		roots[1].Source != "/var/lib/registry" || roots[1].ArchivePath != "registry" {
		t.Fatalf("parseRoots() = %#v", roots)
	}
}

func writeRecoveryTestFile(t *testing.T, root, name, value string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
