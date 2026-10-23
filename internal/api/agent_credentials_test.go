package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentCredentialFileSupportsRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	document := []byte(`{"version":1,"agents":{"agent-a":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}}`)
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := LoadAgentCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := matchAgentCredential(resolver, "agent-a", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !ok {
		t.Fatal("rotated credential was not accepted")
	}
	if _, ok := matchAgentCredential(resolver, "agent-b", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); ok {
		t.Fatal("credential crossed agent scope")
	}
}

func TestLoadAgentCredentialFileRejectsWeakMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"agents":{"agent-a":["too-short"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentCredentialFile(path); err == nil {
		t.Fatal("weak credential was accepted")
	}
}

func TestStaticAgentCredentialsRejectsCrossAgentReuse(t *testing.T) {
	_, err := NewStaticAgentCredentials(map[string][]string{
		"agent-a": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"agent-b": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if err == nil {
		t.Fatal("cross-agent credential reuse was accepted")
	}
}
