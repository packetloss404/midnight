// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCursorKeyPath is the operator-baked cursor signing key path.
const DefaultCursorKeyPath = "/etc/midnight/cursor.key"

// EnvCursorKeyPath overrides DefaultCursorKeyPath at startup.
const EnvCursorKeyPath = "MIDNIGHT_CURSOR_KEY_PATH"

// MinCursorSecretBytes is the minimum acceptable byte length of the
// loaded cursor secret. 32 bytes (= 256 bits) matches the panel-locked
// "256-bit base32 token class" entropy budget for cursor signing keys.
const MinCursorSecretBytes = 32

// ErrCursorSecretUnavailable is returned when the loader cannot find a
// usable secret. Callers MUST treat this as fatal at startup; serving
// requests with the Phase 0 placeholder is not acceptable in Phase 1.
var ErrCursorSecretUnavailable = errors.New("cursor signing secret unavailable")

// ErrCursorSecretWeak is returned when the loaded secret is too short.
var ErrCursorSecretWeak = errors.New("cursor signing secret is too short")

// CursorSecretSource is the seam for loading the HMAC signing key used
// by the v1alpha cursor codec. It is intentionally minimal so Lane 4
// can later swap in an envelope-encrypted variant without touching the
// router.
//
// TODO(lane-4): wrap the file-backed implementation with envelope
// decryption per ADR-014 ("variables/secrets envelope encryption"). The
// cursor signing key is one of the platform-scope secrets that joins
// the wider KEK-rotation flow once Lane 4 ships.
type CursorSecretSource interface {
	// Load returns the raw HMAC key bytes. Implementations SHOULD avoid
	// caching the bytes across calls; the wrapping CursorCodec keeps a
	// single copy after construction.
	Load() ([]byte, error)
}

// FileCursorSecretSource reads the signing key from disk. The key file
// MUST be at most 4 KiB and at least MinCursorSecretBytes after trimming
// trailing whitespace; an operator-friendly mode 0400 is recommended.
type FileCursorSecretSource struct {
	Path   string
	EnvVar string
}

// NewFileCursorSecretSource constructs the default file-backed source.
func NewFileCursorSecretSource() *FileCursorSecretSource {
	return &FileCursorSecretSource{Path: DefaultCursorKeyPath, EnvVar: EnvCursorKeyPath}
}

// Load implements CursorSecretSource.
func (s *FileCursorSecretSource) Load() ([]byte, error) {
	path := s.Path
	envVar := s.EnvVar
	if envVar == "" {
		envVar = EnvCursorKeyPath
	}
	if override := strings.TrimSpace(os.Getenv(envVar)); override != "" {
		path = override
	}
	if path == "" {
		return nil, fmt.Errorf("%w: no path configured", ErrCursorSecretUnavailable)
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s does not exist", ErrCursorSecretUnavailable, abs)
	}
	if err != nil {
		return nil, fmt.Errorf("read cursor key %s: %w", abs, err)
	}
	if len(data) > 4096 {
		return nil, fmt.Errorf("%w: %s is larger than 4 KiB", ErrCursorSecretUnavailable, abs)
	}
	trimmed := strings.TrimRight(string(data), " \t\r\n")
	if trimmed == "" {
		return nil, fmt.Errorf("%w: %s is empty after trim", ErrCursorSecretUnavailable, abs)
	}
	if len(trimmed) < MinCursorSecretBytes {
		return nil, fmt.Errorf("%w: %s yields %d bytes, want >= %d", ErrCursorSecretWeak, abs, len(trimmed), MinCursorSecretBytes)
	}
	return []byte(trimmed), nil
}

// StaticCursorSecretSource is the test/dev seam. Production code MUST
// NOT use this — the placeholder is preserved only so existing unit
// tests that built routers without an explicit secret path continue to
// work.
type StaticCursorSecretSource struct {
	Secret []byte
}

// Load implements CursorSecretSource.
func (s StaticCursorSecretSource) Load() ([]byte, error) {
	if len(s.Secret) == 0 {
		return nil, fmt.Errorf("%w: static secret is empty", ErrCursorSecretUnavailable)
	}
	return append([]byte(nil), s.Secret...), nil
}
