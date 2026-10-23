// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package variableintent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBodyMACIsStableKeyedAndNotRawBodySHA(t *testing.T) {
	key := []byte("tenant-variable-intent-test-key-0001")
	body := []byte(`{"name":"TOKEN","value":"guessable"}`)
	first, err := BodyMAC(key, body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BodyMAC(key, body)
	if err != nil {
		t.Fatal(err)
	}
	other, err := BodyMAC([]byte("tenant-variable-intent-test-key-0002"), body)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other || len(first) != 64 {
		t.Fatalf("MAC stability/separation failed: first=%q second=%q other=%q", first, second, other)
	}
	if strings.Contains(first, "guessable") {
		t.Fatal("fingerprint exposed body material")
	}
}

func TestFileMACKeySourceIsBoundedAndTrimsLineEnding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "variable-intent.key")
	if err := os.WriteFile(path, []byte("tenant-variable-intent-test-key-0001\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	key, err := (&FileMACKeySource{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	if got := string(key); got != "tenant-variable-intent-test-key-0001" {
		t.Fatalf("key=%q", got)
	}
}
