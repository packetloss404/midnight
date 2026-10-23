// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func writeKeyFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.key")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func TestFileCursorSecretSource_LoadsValidKey(t *testing.T) {
	path := writeKeyFile(t, strings.Repeat("a", 64))
	src := &FileCursorSecretSource{Path: path, EnvVar: "MIDNIGHT_CURSOR_KEY_PATH_TEST"}
	t.Setenv("MIDNIGHT_CURSOR_KEY_PATH_TEST", "")
	got, err := src.Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if string(got) != strings.Repeat("a", 64) {
		t.Fatalf("Load() = %q, want repeated a", string(got))
	}
}

func TestFileCursorSecretSource_TrimsTrailingWhitespace(t *testing.T) {
	path := writeKeyFile(t, strings.Repeat("b", 32)+"\n")
	src := &FileCursorSecretSource{Path: path}
	got, err := src.Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if string(got) != strings.Repeat("b", 32) {
		t.Fatalf("Load() did not trim trailing newline: %q", string(got))
	}
}

func TestFileCursorSecretSource_EnvOverrideTakesPrecedence(t *testing.T) {
	primary := writeKeyFile(t, strings.Repeat("c", 32))
	override := writeKeyFile(t, strings.Repeat("d", 64))
	src := &FileCursorSecretSource{Path: primary, EnvVar: "MIDNIGHT_CURSOR_KEY_PATH_TEST"}
	t.Setenv("MIDNIGHT_CURSOR_KEY_PATH_TEST", override)
	got, err := src.Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if string(got) != strings.Repeat("d", 64) {
		t.Fatalf("Load() did not pick up env override: %q", string(got))
	}
}

func TestFileCursorSecretSource_RejectsMissingFile(t *testing.T) {
	src := &FileCursorSecretSource{Path: filepath.Join(t.TempDir(), "does-not-exist"), EnvVar: "MIDNIGHT_CURSOR_KEY_PATH_TEST"}
	t.Setenv("MIDNIGHT_CURSOR_KEY_PATH_TEST", "")
	_, err := src.Load()
	if !errors.Is(err, ErrCursorSecretUnavailable) {
		t.Fatalf("Load() err = %v, want ErrCursorSecretUnavailable", err)
	}
}

func TestFileCursorSecretSource_RejectsShortKey(t *testing.T) {
	path := writeKeyFile(t, "short")
	src := &FileCursorSecretSource{Path: path, EnvVar: "MIDNIGHT_CURSOR_KEY_PATH_TEST"}
	t.Setenv("MIDNIGHT_CURSOR_KEY_PATH_TEST", "")
	_, err := src.Load()
	if !errors.Is(err, ErrCursorSecretWeak) {
		t.Fatalf("Load() err = %v, want ErrCursorSecretWeak", err)
	}
}

func TestFileCursorSecretSource_RejectsEmptyAfterTrim(t *testing.T) {
	path := writeKeyFile(t, "   \n\n")
	src := &FileCursorSecretSource{Path: path, EnvVar: "MIDNIGHT_CURSOR_KEY_PATH_TEST"}
	t.Setenv("MIDNIGHT_CURSOR_KEY_PATH_TEST", "")
	_, err := src.Load()
	if !errors.Is(err, ErrCursorSecretUnavailable) {
		t.Fatalf("Load() err = %v, want ErrCursorSecretUnavailable", err)
	}
}

func TestCursorCodec_DecodeFailsWithDifferentSecret(t *testing.T) {
	codec := NewCursorCodec([]byte(strings.Repeat("a", 32)))
	signed, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 7})
	if err != nil {
		t.Fatalf("Encode() err = %v", err)
	}

	other := NewCursorCodec([]byte(strings.Repeat("b", 32)))
	if _, err := other.Decode(signed); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode() with foreign secret err = %v, want ErrInvalidCursorPage", err)
	}
}

func TestCursorCodec_DecodeFailsOnTamperedSignature(t *testing.T) {
	codec := NewCursorCodec([]byte(strings.Repeat("a", 32)))
	signed, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 3})
	if err != nil {
		t.Fatalf("Encode() err = %v", err)
	}
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	parts[2] = "0000000000000000000000000000000000000000000"
	tampered := strings.Join(parts, ".")
	if _, err := codec.Decode(tampered); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode() of tampered cursor err = %v, want ErrInvalidCursorPage", err)
	}
}

func TestCursorCodec_DecodeFailsOnGarbagePayload(t *testing.T) {
	codec := NewCursorCodec([]byte(strings.Repeat("a", 32)))
	if _, err := codec.Decode("u.@@@@"); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode() err = %v, want ErrInvalidCursorPage", err)
	}
	if _, err := codec.Decode(""); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode() empty err = %v, want ErrInvalidCursorPage", err)
	}
}
