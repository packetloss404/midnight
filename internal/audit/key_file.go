// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultSigningKeyFile = "/var/lib/midnight/audit/signing-key.pem"

var ErrSigningKeyPermissions = errors.New("audit: signing key requires a private directory and mode 0600 regular file")
var ErrVerifyingKeyPermissions = errors.New("audit: trusted public key directory and files must not be symlinked or writable by group/others")

// LoadOrCreateSigningKey never overwrites an existing key. allowCreate must be
// false once signed history exists: a missing key then requires explicit
// recovery/provisioning rather than an accidental, untrusted replacement.
func LoadOrCreateSigningKey(ctx context.Context, path string, allowCreate bool) (SigningKey, error) {
	if err := ctx.Err(); err != nil {
		return SigningKey{}, err
	}
	if !filepath.IsAbs(path) {
		return SigningKey{}, errors.New("audit: signing key path must be absolute")
	}
	dir := filepath.Dir(path)
	if allowCreate {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return SigningKey{}, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return SigningKey{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return SigningKey{}, ErrSigningKeyPermissions
	}
	key, err := loadPrivateKeyFile(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !allowCreate {
		return SigningKey{}, err
	}
	key, err = GenerateSigningKey()
	if err != nil {
		return SigningKey{}, err
	}
	encoded := EncodeSigningKeyPEM(key)
	defer clear(encoded)
	if err := publishKeyFile(ctx, path, encoded, 0o600); err != nil && !errors.Is(err, os.ErrExist) {
		return SigningKey{}, err
	}
	// A concurrent creator may have won; always return the durable key.
	return loadPrivateKeyFile(path)
}

func loadPrivateKeyFile(path string) (SigningKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return SigningKey{}, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		return SigningKey{}, ErrSigningKeyPermissions
	}
	file, err := os.Open(path)
	if err != nil {
		return SigningKey{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return SigningKey{}, err
	}
	if !os.SameFile(info, opened) || opened.Size() > 4096 {
		return SigningKey{}, ErrSigningKeyMalformed
	}
	data := make([]byte, opened.Size())
	defer clear(data)
	if _, err := file.ReadAt(data, 0); err != nil {
		return SigningKey{}, err
	}
	return LoadSigningKeyPEM(data)
}

// PublishVerifyingKey stores only public bytes in a fingerprint-named file.
// This directory is distribution material, not an automatically trusted keyring.
func PublishVerifyingKey(ctx context.Context, privatePath string, key SigningKey) (string, error) {
	dir := filepath.Join(filepath.Dir(privatePath), "public-keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, key.Fingerprint+".pem")
	encoded := EncodeVerifyingKeyPEM(key)
	if err := publishKeyFile(ctx, path, encoded, 0o644); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(existing, encoded) {
			return "", errors.New("audit: public key file does not match signing key")
		}
	}
	return path, nil
}

func publishKeyFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".audit-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Linking is atomic and fails if the destination already exists. Rename
	// would overwrite another worker's key on Unix; O_EXCL+Write exposes a
	// partially written key to readers. Both are avoided here.
	if err := os.Link(temp.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer dir.Close()
		if err := dir.Sync(); err != nil {
			return err
		}
	}
	return nil
}

type VerifyingKeyProvider interface {
	VerifyingKeys(context.Context) ([]VerifyingKey, error)
}
type DirectoryKeyring struct{ Path string }

func (d DirectoryKeyring) VerifyingKeys(ctx context.Context) ([]VerifyingKey, error) {
	if strings.TrimSpace(d.Path) == "" {
		return nil, nil
	}
	info, err := os.Lstat(d.Path)
	if err != nil {
		return nil, fmt.Errorf("audit: read trusted public key directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0) {
		return nil, ErrVerifyingKeyPermissions
	}
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		return nil, fmt.Errorf("audit: read trusted public key directory: %w", err)
	}
	var keys []VerifyingKey
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".pem") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, ErrVerifyingKeyPermissions
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
			return nil, ErrVerifyingKeyPermissions
		}
		key, err := LoadVerifyingKeyFile(filepath.Join(d.Path, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("audit: invalid trusted public key file: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
