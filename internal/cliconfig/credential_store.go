// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package cliconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	credentialServiceName = "midnight-cli"
	keyringRefPrefix      = "keyring:"
	fallbackRefPrefix     = "fallback:"
)

type secretBackend interface {
	Set(ref, secret string) error
	Get(ref string) (string, error)
	Delete(ref string) error
}

type keyringBackend struct{}

func (keyringBackend) Set(ref, secret string) error {
	if err := keyring.Set(credentialServiceName, ref, secret); err != nil {
		return fmt.Errorf("%w: keyring set failed", ErrCredentialStore)
	}
	return nil
}

func (keyringBackend) Get(ref string) (string, error) {
	secret, err := keyring.Get(credentialServiceName, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", fmt.Errorf("%w: keyring get failed", ErrCredentialStore)
	}
	return secret, nil
}

func (keyringBackend) Delete(ref string) error {
	err := keyring.Delete(credentialServiceName, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: keyring delete failed", ErrCredentialStore)
	}
	return nil
}

type fallbackCredentialFile struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

type fallbackBackend struct {
	path string
}

func (b fallbackBackend) Set(ref, secret string) error {
	file, err := b.load()
	if err != nil {
		return err
	}
	file.Secrets[ref] = secret
	if err := atomicWriteJSON(b.path, file, 0o600, 0o700); err != nil {
		return fmt.Errorf("%w: headless credential write failed: %v", ErrCredentialStore, redactCredentialError(err))
	}
	return nil
}

func (b fallbackBackend) Get(ref string) (string, error) {
	file, err := b.load()
	if err != nil {
		return "", err
	}
	secret, ok := file.Secrets[ref]
	if !ok {
		return "", os.ErrNotExist
	}
	return secret, nil
}

func (b fallbackBackend) Delete(ref string) error {
	file, err := b.load()
	if err != nil {
		return err
	}
	if _, ok := file.Secrets[ref]; !ok {
		return nil
	}
	delete(file.Secrets, ref)
	if err := atomicWriteJSON(b.path, file, 0o600, 0o700); err != nil {
		return fmt.Errorf("%w: headless credential delete failed: %v", ErrCredentialStore, redactCredentialError(err))
	}
	return nil
}

func (b fallbackBackend) load() (fallbackCredentialFile, error) {
	data, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return fallbackCredentialFile{Version: 1, Secrets: map[string]string{}}, nil
	}
	if err != nil {
		return fallbackCredentialFile{}, fmt.Errorf("%w: headless credential read failed: %v", ErrCredentialStore, redactCredentialError(err))
	}
	var file fallbackCredentialFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fallbackCredentialFile{}, fmt.Errorf("%w: headless credential data is invalid", ErrCredentialStore)
	}
	if file.Version != 1 {
		return fallbackCredentialFile{}, fmt.Errorf("%w: unsupported headless credential version", ErrCredentialStore)
	}
	if file.Secrets == nil {
		file.Secrets = map[string]string{}
	}
	return file, nil
}

func selectedSecretBackend() (secretBackend, string, error) {
	override := strings.TrimSpace(os.Getenv("MIDNIGHT_CONFIG_HOME"))
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return nil, "", fmt.Errorf("%w: resolve headless config directory", ErrCredentialStore)
		}
		return fallbackBackend{path: filepath.Join(abs, FallbackCredentialFileName)}, fallbackRefPrefix, nil
	}
	return keyringBackend{}, keyringRefPrefix, nil
}

func credentialReference(apiBase, slot, prefix string) string {
	digest := sha256.Sum256([]byte(apiBase))
	return prefix + slot + ":" + hex.EncodeToString(digest[:16])
}

func credentialForEntry(entry ServerEntry, recovery bool) (string, error) {
	legacy := strings.TrimSpace(entry.Token)
	ref := entry.CredentialRef
	if recovery {
		legacy = strings.TrimSpace(entry.OperatorRecoveryToken)
		ref = entry.OperatorRecoveryCredentialRef
	}
	if legacy != "" {
		return legacy, nil
	}
	if strings.TrimSpace(ref) == "" {
		return "", os.ErrNotExist
	}
	backend, prefix, err := selectedSecretBackend()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("%w: credential reference backend does not match this configuration", ErrCredentialStore)
	}
	secret, err := backend.Get(ref)
	if errors.Is(err, os.ErrNotExist) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(secret), nil
}

type stagedSecret struct {
	backend  secretBackend
	ref      string
	previous string
	existed  bool
	changed  bool
}

func stageSecret(backend secretBackend, ref, secret string) (stagedSecret, error) {
	stage := stagedSecret{backend: backend, ref: ref}
	previous, err := backend.Get(ref)
	if err == nil {
		stage.previous, stage.existed = previous, true
	} else if !errors.Is(err, os.ErrNotExist) {
		return stagedSecret{}, err
	}
	if stage.existed && previous == secret {
		return stage, nil
	}
	if err := backend.Set(ref, secret); err != nil {
		return stagedSecret{}, err
	}
	stage.changed = true
	return stage, nil
}

func stageSecretDelete(backend secretBackend, ref string) (stagedSecret, error) {
	stage := stagedSecret{backend: backend, ref: ref}
	previous, err := backend.Get(ref)
	if errors.Is(err, os.ErrNotExist) {
		return stage, nil
	}
	if err != nil {
		return stagedSecret{}, err
	}
	stage.previous, stage.existed = previous, true
	if err := backend.Delete(ref); err != nil {
		return stagedSecret{}, err
	}
	stage.changed = true
	return stage, nil
}

func (s stagedSecret) rollback() error {
	if !s.changed {
		return nil
	}
	if s.existed {
		return s.backend.Set(s.ref, s.previous)
	}
	return s.backend.Delete(s.ref)
}

func redactCredentialError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("local storage operation failed")
}
