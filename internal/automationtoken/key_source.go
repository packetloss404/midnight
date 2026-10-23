// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package automationtoken

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultMACKeyPath is the installer-managed project automation-token
	// verifier key. It is deliberately distinct from cursor, session, agent,
	// operator, and envelope-encryption key material.
	DefaultMACKeyPath = "/etc/midnight/automation-token.key"
	// EnvMACKeyPath overrides DefaultMACKeyPath at server startup.
	EnvMACKeyPath      = "MIDNIGHT_AUTOMATION_TOKEN_KEY_PATH" // not-a-failure-code
	maxMACKeyFileBytes = 4096
)

var (
	ErrMACKeyUnavailable = errors.New("automation token MAC key unavailable")
	ErrMACKeyWeak        = errors.New("automation token MAC key is too short")
)

// MACKeySource loads the raw HMAC key used to derive verifier-only token
// hashes. Production sources must be stable across restarts and backups.
type MACKeySource interface {
	Load() ([]byte, error)
}

// FileMACKeySource reads a bounded, whitespace-trimmed key file. It reports
// only the resolved path and byte count; key material is never included in an
// error.
type FileMACKeySource struct {
	Path   string
	EnvVar string
}

func NewFileMACKeySource() *FileMACKeySource {
	return &FileMACKeySource{Path: DefaultMACKeyPath, EnvVar: EnvMACKeyPath}
}

func (s *FileMACKeySource) Load() ([]byte, error) {
	path := strings.TrimSpace(s.Path)
	envVar := strings.TrimSpace(s.EnvVar)
	if envVar != "" {
		if override := strings.TrimSpace(os.Getenv(envVar)); override != "" {
			path = override
		}
	}
	if path == "" {
		return nil, fmt.Errorf("%w: no path configured", ErrMACKeyUnavailable)
	}
	displayPath, err := filepath.Abs(path)
	if err != nil {
		displayPath = path
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s does not exist", ErrMACKeyUnavailable, displayPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read automation token MAC key %s: %w", displayPath, err)
	}
	if len(data) > maxMACKeyFileBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrMACKeyUnavailable, displayPath, maxMACKeyFileBytes)
	}
	trimmed := strings.TrimRight(string(data), " \t\r\n")
	for index := range data {
		data[index] = 0
	}
	if trimmed == "" {
		return nil, fmt.Errorf("%w: %s is empty after trim", ErrMACKeyUnavailable, displayPath)
	}
	if len(trimmed) < MinMACKeyBytes {
		return nil, fmt.Errorf("%w: %s yields %d bytes, want at least %d", ErrMACKeyWeak, displayPath, len(trimmed), MinMACKeyBytes)
	}
	return []byte(trimmed), nil
}

// StaticMACKeySource is a deterministic test/development seam.
type StaticMACKeySource struct {
	Key []byte
}

func (s StaticMACKeySource) Load() ([]byte, error) {
	if len(s.Key) < MinMACKeyBytes {
		return nil, ErrMACKeyWeak
	}
	return append([]byte(nil), s.Key...), nil
}
