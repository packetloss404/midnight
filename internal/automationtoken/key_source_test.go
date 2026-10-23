// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package automationtoken

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMACKeySourceLoadsTrimmedKeyAndHonorsOverride(t *testing.T) {
	const testKeyEnv = "MIDNIGHT_AUTOMATION_KEY_TEST" // not-a-failure-code
	primary := filepath.Join(t.TempDir(), "primary")
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(primary, []byte(strings.Repeat("p", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 48)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testKeyEnv, override)
	key, err := (&FileMACKeySource{Path: primary, EnvVar: testKeyEnv}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(key); got != strings.Repeat("o", 48) {
		t.Fatalf("key length/content mismatch: len=%d", len(key))
	}
}

func TestFileMACKeySourceFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", []byte(" \n"), ErrMACKeyUnavailable},
		{"weak", []byte("too-short"), ErrMACKeyWeak},
		{"oversized", []byte(strings.Repeat("x", maxMACKeyFileBytes+1)), ErrMACKeyUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, tt.data, 0o400); err != nil {
				t.Fatal(err)
			}
			_, err := (&FileMACKeySource{Path: path}).Load()
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), string(tt.data)) && len(tt.data) > 0 {
				t.Fatalf("error leaked key material: %v", err)
			}
		})
	}
}

func TestFileMACKeySourceRejectsMissingFile(t *testing.T) {
	_, err := (&FileMACKeySource{Path: filepath.Join(t.TempDir(), "missing")}).Load()
	if !errors.Is(err, ErrMACKeyUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestFileMACKeySourceExplicitPathDoesNotReapplyDefaultEnvironment(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit")
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(explicit, []byte(strings.Repeat("e", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMACKeyPath, override)
	key, err := (&FileMACKeySource{Path: explicit}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("e", 40) {
		t.Fatal("explicit startup path was unexpectedly replaced by the environment")
	}
}

func TestNewFileMACKeySourceHonorsDefaultEnvironment(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMACKeyPath, override)
	key, err := NewFileMACKeySource().Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("o", 40) {
		t.Fatal("default source did not honor the configured environment path")
	}
}
