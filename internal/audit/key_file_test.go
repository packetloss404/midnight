// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestSigningKeyFileConcurrentBootstrapIsStableAndPublicOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "signing-key.pem")
	const workers = 16
	results := make(chan SigningKey, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := LoadOrCreateSigningKey(context.Background(), path, true)
			if err != nil {
				failures <- err
				return
			}
			results <- key
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var durable SigningKey
	for key := range results {
		if durable.Fingerprint != "" && durable.Fingerprint != key.Fingerprint {
			t.Fatal("concurrent creators returned different keys")
		}
		durable = key
	}
	reloaded, err := LoadOrCreateSigningKey(context.Background(), path, false)
	if err != nil || reloaded.Fingerprint != durable.Fingerprint {
		t.Fatalf("restart key=%s err=%v", reloaded.Fingerprint, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private mode=%o", info.Mode().Perm())
	}
	publicPath, err := PublishVerifyingKey(context.Background(), path, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	public, err := LoadVerifyingKeyFile(publicPath)
	if err != nil || public.Fingerprint != durable.Fingerprint {
		t.Fatalf("public key err=%v", err)
	}
	encoded, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("PRIVATE")) {
		t.Fatal("public distribution contains private bytes")
	}
	serialized, _ := json.Marshal(durable)
	if bytes.Contains(serialized, []byte("Private")) {
		t.Fatal("signing key JSON exposes private field")
	}
}

func TestSigningKeyFileRejectsMissingHistoryKeyUnsafeModesAndMalformedMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "signing-key.pem")
	if _, err := LoadOrCreateSigningKey(context.Background(), path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing history key=%v", err)
	}
	key, err := LoadOrCreateSigningKey(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateSigningKey(context.Background(), path, true); !errors.Is(err, ErrSigningKeyPermissions) {
			t.Fatalf("insecure key=%v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "key.pem")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateSigningKey(context.Background(), link, true); !errors.Is(err, ErrSigningKeyPermissions) {
			t.Fatalf("symlink key=%v", err)
		}
	}
	key.Private[63] ^= 1
	if _, err := LoadSigningKeyPEM(EncodeSigningKeyPEM(key)); !errors.Is(err, ErrSigningKeyMalformed) {
		t.Fatalf("inconsistent private/public material=%v", err)
	}
	if _, err := SignCheckpoint(Checkpoint{}, SigningKey{}); !errors.Is(err, ErrSigningKeyMalformed) {
		t.Fatalf("invalid signer=%v", err)
	}
	if err := VerifyCheckpoint(Checkpoint{SignatureAlg: "ed25519"}, VerifyingKey{}); !errors.Is(err, ErrCheckpointSignatureBad) {
		t.Fatalf("invalid verifier=%v", err)
	}
	if err := os.WriteFile(path, []byte("broken private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateSigningKey(context.Background(), path, true); err == nil {
		t.Fatal("malformed existing key was replaced")
	}
}

func TestDirectoryKeyringRequiresExplicitPublicPEMs(t *testing.T) {
	dir := t.TempDir()
	first, _ := GenerateSigningKey()
	second, _ := GenerateSigningKey()
	for _, key := range []SigningKey{first, second} {
		if err := os.WriteFile(filepath.Join(dir, key.Fingerprint+".pem"), EncodeVerifyingKeyPEM(key), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := (DirectoryKeyring{Path: dir}).VerifyingKeys(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatalf("rotation overlap keyring count=%d err=%v", len(keys), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private.pem"), EncodeSigningKeyPEM(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectoryKeyring{Path: dir}).VerifyingKeys(context.Background()); !errors.Is(err, ErrSigningKeyMalformed) {
		t.Fatalf("private key imported as trust=%v", err)
	}
}

func TestDirectoryKeyringRejectsWritableAndSymlinkedTrust(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix trust-directory mode and symlink contract")
	}
	dir := t.TempDir()
	key, _ := GenerateSigningKey()
	path := filepath.Join(dir, "pinned.pem")
	if err := os.WriteFile(path, EncodeVerifyingKeyPEM(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectoryKeyring{Path: dir}).VerifyingKeys(context.Background()); !errors.Is(err, ErrVerifyingKeyPermissions) {
		t.Fatalf("writable trust directory accepted: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectoryKeyring{Path: dir}).VerifyingKeys(context.Background()); !errors.Is(err, ErrVerifyingKeyPermissions) {
		t.Fatalf("writable trust file accepted: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "trusted")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectoryKeyring{Path: link}).VerifyingKeys(context.Background()); !errors.Is(err, ErrVerifyingKeyPermissions) {
		t.Fatalf("symlink trust directory accepted: %v", err)
	}
	other := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(other, EncodeVerifyingKeyPEM(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectoryKeyring{Path: dir}).VerifyingKeys(context.Background()); !errors.Is(err, ErrVerifyingKeyPermissions) {
		t.Fatalf("symlink trust file accepted: %v", err)
	}
}
