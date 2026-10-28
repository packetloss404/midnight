package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateCredentialRejectsWritableOrSymlinkedParentPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory ownership/mode boundary; command blocks Windows")
	}
	root := privateTempDir(t)
	unsafe := filepath.Join(root, "unsafe")
	_ = os.Mkdir(unsafe, 0700)
	_ = os.Chmod(unsafe, 0777)
	child := filepath.Join(unsafe, "private")
	_ = os.Mkdir(child, 0700)
	path := filepath.Join(child, "credential")
	_ = os.WriteFile(path, []byte("fixture-secret"), 0600)
	if _, err := readPrivate(path, 4096); err == nil {
		t.Fatal("writable ancestor can replace private credential directory")
	}
	direct := filepath.Join(unsafe, "credential")
	_ = os.WriteFile(direct, []byte("fixture-secret"), 0600)
	if _, err := readPrivate(direct, 4096); err == nil {
		t.Fatal("0600 credential under world-writable parent accepted")
	}
	private := filepath.Join(root, "private")
	_ = os.Mkdir(private, 0700)
	path = filepath.Join(private, "credential")
	_ = os.WriteFile(path, []byte("fixture-secret"), 0600)
	link := filepath.Join(root, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(filepath.Join(link, "credential"), 4096); err == nil {
		t.Fatal("symlinked parent accepted")
	}
	if _, err := readPrivate(link+"/../private/credential", 4096); err == nil {
		t.Fatal("dot-dot path bypassed symlink ancestry validation")
	}
	if _, err := readPrivate(path, 4096); err != nil {
		t.Fatal("private owned child under trusted sticky temp ancestor rejected")
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if _, err := readPrivate(path, 4096); err == nil {
			t.Fatal("root trusted an attacker-owned credential")
		}
	}
}

func TestDNSChildUsesValidatedConfigurationSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private configuration snapshot requires Unix file permissions")
	}
	dir := privateTempDir(t)
	path := filepath.Join(dir, "qa.env")
	original := "TARGET_CLASS=disposable-qa\nDNS_ZONE_ID=" + strings.Repeat("0", 32) + "\nDNS_ZONE_NAME=example.invalid\nDNS_QA_SUFFIX=qa.example.invalid\nCLOUDFLARE_TOKEN_FILE=provider.token\nACME_EMAIL=qa@example.invalid\nACME_STAGING_ACCOUNT_DIR=account\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path, "dns-acme")
	if err != nil {
		t.Fatal(err)
	}
	// A later file replacement must not alter the child's validated values.
	_ = os.WriteFile(path, []byte(strings.ReplaceAll(original, "DNS_QA_SUFFIX=qa.example.invalid", "DNS_QA_SUFFIX=changed.example.invalid")), 0600)
	child := privateTempDir(t)
	snapshotPath := filepath.Join(child, "configuration.env")
	if err := os.WriteFile(snapshotPath, snapshotConfiguration(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(snapshotPath, "dns-acme")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.values["DNS_QA_SUFFIX"] != "qa.example.invalid" || loaded.path("CLOUDFLARE_TOKEN_FILE") != filepath.Join(dir, "provider.token") || loaded.path("ACME_STAGING_ACCOUNT_DIR") != filepath.Join(dir, "account") {
		t.Fatal("child reopened changed original config or resolved credentials relative to wrong directory")
	}
}
