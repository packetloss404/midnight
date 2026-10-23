// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultInvitationMACKeyPath is the installer-managed invitation verifier
	// key. It must remain independent from every other signing, verifier, and
	// envelope-encryption key used by Midnight.
	DefaultInvitationMACKeyPath = "/etc/midnight/invitation.key"
	// EnvInvitationMACKeyPath overrides the default at server startup.
	EnvInvitationMACKeyPath = "MIDNIGHT_INVITATION_KEY_PATH" // not-a-failure-code

	maxInvitationMACKeyFileBytes = 4096
)

var (
	ErrInvitationMACKeyUnavailable = errors.New("identity invitation MAC key unavailable")
	ErrInvitationMACKeyWeak        = errors.New("identity invitation MAC key is too short")
)

// FileInvitationMACKeySource reads a bounded, whitespace-trimmed HMAC key.
// An explicit Path does not implicitly reapply the default environment
// override; callers that want that behavior should use the constructor.
type FileInvitationMACKeySource struct {
	Path   string
	EnvVar string
}

func NewFileInvitationMACKeySource() *FileInvitationMACKeySource {
	return &FileInvitationMACKeySource{
		Path:   DefaultInvitationMACKeyPath,
		EnvVar: EnvInvitationMACKeyPath,
	}
}

func (s *FileInvitationMACKeySource) Load() ([]byte, error) {
	path := strings.TrimSpace(s.Path)
	if envVar := strings.TrimSpace(s.EnvVar); envVar != "" {
		if override := strings.TrimSpace(os.Getenv(envVar)); override != "" {
			path = override
		}
	}
	if path == "" {
		return nil, fmt.Errorf("%w: no path configured", ErrInvitationMACKeyUnavailable)
	}
	displayPath, err := filepath.Abs(path)
	if err != nil {
		displayPath = path
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s does not exist", ErrInvitationMACKeyUnavailable, displayPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read identity invitation MAC key %s: %w", displayPath, err)
	}
	if len(data) > maxInvitationMACKeyFileBytes {
		clear(data)
		return nil, fmt.Errorf(
			"%w: %s is larger than %d bytes",
			ErrInvitationMACKeyUnavailable,
			displayPath,
			maxInvitationMACKeyFileBytes,
		)
	}
	trimmed := strings.TrimRight(string(data), " \t\r\n")
	clear(data)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: %s is empty after trim", ErrInvitationMACKeyUnavailable, displayPath)
	}
	if len(trimmed) < InvitationMinMACBytes {
		return nil, fmt.Errorf(
			"%w: %s yields %d bytes, want at least %d",
			ErrInvitationMACKeyWeak,
			displayPath,
			len(trimmed),
			InvitationMinMACBytes,
		)
	}
	return []byte(trimmed), nil
}
