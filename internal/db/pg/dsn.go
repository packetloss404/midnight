// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDSNFile is the operator-baked DSN path the .deb install lays
// down at first boot. The path matches the panel-locked
// /var/lib/midnight layout for control-plane state.
const DefaultDSNFile = "/etc/midnight/db.dsn"

// EnvDSN is the environment variable that, when set, overrides the
// file-based DSN source. Operators / CI / dev all use the same
// taxonomy.
const EnvDSN = "MIDNIGHT_DB_DSN"

// EnvDSNFile is the override path for the file-backed DSN source.
const EnvDSNFile = "MIDNIGHT_DB_DSN_FILE"

// ErrDSNUnavailable is returned when neither the environment override
// nor the file-backed source provide a non-empty DSN. Callers MUST
// treat this as fatal at startup.
var ErrDSNUnavailable = errors.New("midnight db dsn unavailable")

// DSNSource describes where the loaded DSN came from. It is recorded on
// startup logs and exported on /metrics so operators can confirm the
// configured source without leaking the DSN bytes.
type DSNSource string

const (
	// DSNSourceEnv indicates the DSN was read from MIDNIGHT_DB_DSN.
	DSNSourceEnv DSNSource = "env"
	// DSNSourceFile indicates the DSN was read from a file on disk.
	DSNSourceFile DSNSource = "file"
)

// LoadedDSN is the result of LoadDSN. The DSN bytes themselves are
// never logged; only Source and (when DSNSourceFile) Path are safe to
// log.
type LoadedDSN struct {
	DSN    string
	Source DSNSource
	Path   string
}

// DSNLoader is the indirection used by tests and by Lane 4's envelope
// encryption add-on. The default LoadDSN function is the file/env
// composite; Lane 4 will wrap it to wrap the on-disk DSN with KMS-
// equivalent unwrap logic.
type DSNLoader interface {
	Load() (LoadedDSN, error)
}

// FileDSNLoader is the default DSNLoader. EnvVar wins over Path; an
// empty Path falls back to DefaultDSNFile when EnvVar is unset.
type FileDSNLoader struct {
	EnvVar    string
	EnvVarRef string
	Path      string
}

// NewFileDSNLoader returns a loader that consults MIDNIGHT_DB_DSN first
// (via os.Getenv), then MIDNIGHT_DB_DSN_FILE (also env-backed), then
// /etc/midnight/db.dsn as the operator default.
func NewFileDSNLoader() *FileDSNLoader {
	return &FileDSNLoader{EnvVar: EnvDSN, EnvVarRef: EnvDSNFile, Path: DefaultDSNFile}
}

// Load implements DSNLoader.
func (l *FileDSNLoader) Load() (LoadedDSN, error) {
	envVar := l.EnvVar
	if envVar == "" {
		envVar = EnvDSN
	}
	if dsn := strings.TrimSpace(os.Getenv(envVar)); dsn != "" {
		return LoadedDSN{DSN: dsn, Source: DSNSourceEnv}, nil
	}

	path := l.Path
	if pathRef := l.EnvVarRef; pathRef != "" {
		if override := strings.TrimSpace(os.Getenv(pathRef)); override != "" {
			path = override
		}
	}
	if path == "" {
		return LoadedDSN{}, fmt.Errorf("%w: no env override and no file path configured", ErrDSNUnavailable)
	}

	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return LoadedDSN{}, fmt.Errorf("%w: %s does not exist", ErrDSNUnavailable, abs)
	}
	if err != nil {
		return LoadedDSN{}, fmt.Errorf("read dsn file %s: %w", abs, err)
	}
	dsn := strings.TrimSpace(string(data))
	if dsn == "" {
		return LoadedDSN{}, fmt.Errorf("%w: %s is empty", ErrDSNUnavailable, abs)
	}
	return LoadedDSN{DSN: dsn, Source: DSNSourceFile, Path: abs}, nil
}

// LoadDSN is the package-level convenience for callers that don't need
// to inject a DSNLoader (most do not).
func LoadDSN() (LoadedDSN, error) {
	return NewFileDSNLoader().Load()
}
