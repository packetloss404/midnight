// Package config loads midnight-agent runtime configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	EnvAgentID          = "MIDNIGHT_AGENT_ID"
	EnvServerURL        = "MIDNIGHT_SERVER_URL"
	EnvRuntimeRoot      = "MIDNIGHT_RUNTIME_ROOT"
	EnvShutdownDeadline = "MIDNIGHT_SHUTDOWN_DEADLINE"

	DefaultAgentID          = "local-agent"
	DefaultServerURL        = "http://127.0.0.1:8080"
	DefaultShutdownDeadline = 30 * time.Second
)

// Config contains the minimal settings needed to start the agent skeleton.
type Config struct {
	AgentID          string
	ServerURL        string
	RuntimeRoot      string
	ShutdownDeadline time.Duration
}

// Load reads configuration from environment variables and applies local defaults.
func Load() (Config, error) {
	return LoadFromLookup(os.LookupEnv)
}

// LoadFromLookup loads configuration using lookup, which keeps tests deterministic.
func LoadFromLookup(lookup func(string) (string, bool)) (Config, error) {
	runtimeRoot, err := defaultRuntimeRoot()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		AgentID:          DefaultAgentID,
		ServerURL:        DefaultServerURL,
		RuntimeRoot:      runtimeRoot,
		ShutdownDeadline: DefaultShutdownDeadline,
	}

	if value, ok := lookup(EnvAgentID); ok && strings.TrimSpace(value) != "" {
		cfg.AgentID = strings.TrimSpace(value)
	}
	if value, ok := lookup(EnvServerURL); ok && strings.TrimSpace(value) != "" {
		cfg.ServerURL = strings.TrimSpace(value)
	}
	if value, ok := lookup(EnvRuntimeRoot); ok && strings.TrimSpace(value) != "" {
		cfg.RuntimeRoot = filepath.Clean(strings.TrimSpace(value))
	}
	if value, ok := lookup(EnvShutdownDeadline); ok && strings.TrimSpace(value) != "" {
		deadline, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvShutdownDeadline, err)
		}
		if deadline <= 0 {
			return Config{}, fmt.Errorf("%s must be positive", EnvShutdownDeadline)
		}
		cfg.ShutdownDeadline = deadline
	}

	return cfg, nil
}

func defaultRuntimeRoot() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(dir, "midnight", "agent"), nil
}
