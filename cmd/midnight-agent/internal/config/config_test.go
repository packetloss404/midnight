package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromLookupAppliesDefaults(t *testing.T) {
	cfg, err := LoadFromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.AgentID != DefaultAgentID {
		t.Fatalf("AgentID = %q, want %q", cfg.AgentID, DefaultAgentID)
	}
	if cfg.ServerURL != DefaultServerURL {
		t.Fatalf("ServerURL = %q, want %q", cfg.ServerURL, DefaultServerURL)
	}
	if cfg.ShutdownDeadline != DefaultShutdownDeadline {
		t.Fatalf("ShutdownDeadline = %v, want %v", cfg.ShutdownDeadline, DefaultShutdownDeadline)
	}
	if cfg.RuntimeRoot == "" {
		t.Fatal("RuntimeRoot is empty")
	}
}

func TestLoadFromLookupOverridesValues(t *testing.T) {
	values := map[string]string{
		EnvAgentID:          " agent-a ",
		EnvServerURL:        " https://midnight.example ",
		EnvRuntimeRoot:      filepath.Join("tmp", "midnight-agent"),
		EnvShutdownDeadline: "5s",
	}

	cfg, err := LoadFromLookup(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.AgentID != "agent-a" {
		t.Fatalf("AgentID = %q", cfg.AgentID)
	}
	if cfg.ServerURL != "https://midnight.example" {
		t.Fatalf("ServerURL = %q", cfg.ServerURL)
	}
	if cfg.RuntimeRoot != filepath.Clean(values[EnvRuntimeRoot]) {
		t.Fatalf("RuntimeRoot = %q", cfg.RuntimeRoot)
	}
	if cfg.ShutdownDeadline != 5*time.Second {
		t.Fatalf("ShutdownDeadline = %v", cfg.ShutdownDeadline)
	}
}

func TestLoadFromLookupRejectsInvalidDeadline(t *testing.T) {
	_, err := LoadFromLookup(func(key string) (string, bool) {
		if key == EnvShutdownDeadline {
			return "soon", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
