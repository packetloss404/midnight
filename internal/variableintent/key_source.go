// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package variableintent owns the independent key used to fingerprint
// value-bearing variable mutations before they enter the idempotency store.
package variableintent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MinMACKeyBytes is the minimum entropy-bearing key length accepted by the
	// variable mutation boundary.
	MinMACKeyBytes = 32
	// DefaultMACKeyPath is installer-managed and deliberately independent from
	// cursor, session, automation-token, invitation, agent, and KEK material.
	DefaultMACKeyPath = "/etc/midnight/variable-intent.key"
	// EnvMACKeyPath overrides DefaultMACKeyPath at server startup.
	EnvMACKeyPath      = "MIDNIGHT_VARIABLE_INTENT_KEY_PATH" // not-a-failure-code
	maxMACKeyFileBytes = 4096
)

var (
	ErrMACKeyUnavailable = errors.New("variable intent MAC key unavailable")
	ErrMACKeyWeak        = errors.New("variable intent MAC key is too short")
)

// FileMACKeySource loads a bounded, stable key without including key material
// in errors.
type FileMACKeySource struct {
	Path   string
	EnvVar string
}

func NewFileMACKeySource() *FileMACKeySource {
	return &FileMACKeySource{Path: DefaultMACKeyPath, EnvVar: EnvMACKeyPath}
}

func (s *FileMACKeySource) Load() ([]byte, error) {
	path := strings.TrimSpace(s.Path)
	if envVar := strings.TrimSpace(s.EnvVar); envVar != "" {
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
		return nil, fmt.Errorf("read variable intent MAC key %s: %w", displayPath, err)
	}
	if len(data) > maxMACKeyFileBytes {
		clear(data)
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrMACKeyUnavailable, displayPath, maxMACKeyFileBytes)
	}
	trimmed := strings.TrimRight(string(data), " \t\r\n")
	clear(data)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: %s is empty after trim", ErrMACKeyUnavailable, displayPath)
	}
	if len(trimmed) < MinMACKeyBytes {
		return nil, fmt.Errorf("%w: %s yields %d bytes, want at least %d", ErrMACKeyWeak, displayPath, len(trimmed), MinMACKeyBytes)
	}
	return []byte(trimmed), nil
}

// BodyMAC returns a SHA-256-sized, domain-separated HMAC suitable for the
// RequestFingerprint.BodySHA256 field. The name of that legacy field describes
// its wire shape; this value is intentionally not an unkeyed body digest.
func BodyMAC(key, body []byte) (string, error) {
	if len(key) < MinMACKeyBytes {
		return "", ErrMACKeyWeak
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("midnight/tenant-variable-intent/v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
