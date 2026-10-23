// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileInvitationMACKeySourceLoadsTrimmedKeyAndHonorsOverride(t *testing.T) {
	const envName = "MIDNIGHT_INVITATION_KEY_TEST" // not-a-failure-code
	primary := filepath.Join(t.TempDir(), "primary")
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(primary, []byte(strings.Repeat("p", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 48)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envName, override)
	key, err := (&FileInvitationMACKeySource{Path: primary, EnvVar: envName}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("o", 48) {
		t.Fatalf("key length/content mismatch: len=%d", len(key))
	}
}

func TestFileInvitationMACKeySourceFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", []byte(" \n"), ErrInvitationMACKeyUnavailable},
		{"weak", []byte("too-short"), ErrInvitationMACKeyWeak},
		{"oversized", []byte(strings.Repeat("x", maxInvitationMACKeyFileBytes+1)), ErrInvitationMACKeyUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, tt.data, 0o400); err != nil {
				t.Fatal(err)
			}
			_, err := (&FileInvitationMACKeySource{Path: path}).Load()
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if len(tt.data) > 0 && strings.Contains(err.Error(), string(tt.data)) {
				t.Fatalf("error leaked key material: %v", err)
			}
		})
	}
}

func TestFileInvitationMACKeySourceExplicitPathDoesNotReapplyDefaultEnvironment(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit")
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(explicit, []byte(strings.Repeat("e", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvInvitationMACKeyPath, override)
	key, err := (&FileInvitationMACKeySource{Path: explicit}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("e", 40) {
		t.Fatal("explicit startup path was unexpectedly replaced by the environment")
	}
}

func TestNewFileInvitationMACKeySourceHonorsDefaultEnvironment(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	if err := os.WriteFile(override, []byte(strings.Repeat("o", 40)), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvInvitationMACKeyPath, override)
	key, err := NewFileInvitationMACKeySource().Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("o", 40) {
		t.Fatal("default source did not honor the configured environment path")
	}
}
