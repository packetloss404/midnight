package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentCredentialPrefersScopedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.credential")
	if err := os.WriteFile(path, []byte("agent-scoped-credential-0000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentCredential("", path, "legacy-shared")
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-scoped-credential-0000000000" {
		t.Fatalf("credential = %q", got)
	}
}

func TestLoadAgentCredentialDoesNotReadRecoveryBearer(t *testing.T) {
	t.Setenv("MIDNIGHT_API_BEARER_TOKEN", "operator-recovery")
	if _, err := loadAgentCredential("", "", ""); err == nil {
		t.Fatal("operator recovery bearer was accepted as an agent credential")
	}
}
