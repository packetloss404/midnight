// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedArchiveRoundTripProducesRealEvidence(t *testing.T) {
	root := t.TempDir()
	destination, err := NewLocalDestination(root)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.Repeat([]byte("durable-volume-record\n"), 20_000)
	passphrase := []byte("correct horse battery staple")

	backupResult, err := PutEncryptedArchive(
		context.Background(),
		destination,
		"volumes/vol_test/archive.mbkp",
		bytes.NewReader(source),
		passphrase,
	)
	if err != nil {
		t.Fatalf("PutEncryptedArchive() error = %v", err)
	}
	wantSourceHash := sha256.Sum256(source)
	if backupResult.SourceBytes != int64(len(source)) ||
		backupResult.SourceSHA256 != hex.EncodeToString(wantSourceHash[:]) {
		t.Fatalf("backup evidence = %#v", backupResult)
	}
	raw, err := os.ReadFile(filepath.Join(root, "volumes", "vol_test", "archive.mbkp"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("durable-volume-record")) {
		t.Fatal("stored archive contains plaintext source")
	}
	archiveHash := sha256.Sum256(raw)
	if backupResult.Object.Bytes != int64(len(raw)) ||
		backupResult.Object.SHA256 != hex.EncodeToString(archiveHash[:]) {
		t.Fatalf("object evidence = %#v, raw bytes=%d", backupResult.Object, len(raw))
	}

	var restored bytes.Buffer
	restoreResult, err := GetEncryptedArchive(
		context.Background(),
		destination,
		backupResult.Object.Key,
		&restored,
		passphrase,
		backupResult.Object.SHA256,
	)
	if err != nil {
		t.Fatalf("GetEncryptedArchive() error = %v", err)
	}
	if !bytes.Equal(restored.Bytes(), source) {
		t.Fatal("restored bytes differ from source")
	}
	if restoreResult.RestoredBytes != int64(len(source)) ||
		restoreResult.RestoredSHA256 != backupResult.SourceSHA256 ||
		restoreResult.Object.SHA256 != backupResult.Object.SHA256 {
		t.Fatalf("restore evidence = %#v", restoreResult)
	}
}

func TestEncryptedArchiveWrongPassphraseAndTamperingFailClosed(t *testing.T) {
	root := t.TempDir()
	destination, err := NewLocalDestination(root)
	if err != nil {
		t.Fatal(err)
	}
	passphrase := []byte("correct horse battery staple")
	result, err := PutEncryptedArchive(
		context.Background(),
		destination,
		"archive.mbkp",
		strings.NewReader(strings.Repeat("sensitive\n", 1000)),
		passphrase,
	)
	if err != nil {
		t.Fatal(err)
	}

	var wrong bytes.Buffer
	if _, err := GetEncryptedArchive(
		context.Background(),
		destination,
		result.Object.Key,
		&wrong,
		[]byte("incorrect horse battery staple"),
		result.Object.SHA256,
	); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong passphrase error = %v", err)
	}

	path := filepath.Join(root, "archive.mbkp")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0x80
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var tampered bytes.Buffer
	if _, err := GetEncryptedArchive(
		context.Background(),
		destination,
		result.Object.Key,
		&tampered,
		passphrase,
		result.Object.SHA256,
	); err == nil {
		t.Fatal("tampered archive restored successfully")
	}
}

func TestEncryptedArchiveTruncationAndChecksumSubstitutionFailClosed(t *testing.T) {
	root := t.TempDir()
	destination, err := NewLocalDestination(root)
	if err != nil {
		t.Fatal(err)
	}
	passphrase := []byte("correct horse battery staple")
	result, err := PutEncryptedArchive(
		context.Background(),
		destination,
		"archive.mbkp",
		bytes.NewReader(bytes.Repeat([]byte{0x42}, 200_000)),
		passphrase,
	)
	if err != nil {
		t.Fatal(err)
	}
	var substituted bytes.Buffer
	if _, err := GetEncryptedArchive(
		context.Background(),
		destination,
		result.Object.Key,
		&substituted,
		passphrase,
		strings.Repeat("0", sha256.Size*2),
	); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("substitution error = %v", err)
	}

	path := filepath.Join(root, "archive.mbkp")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	var truncated bytes.Buffer
	if _, err := GetEncryptedArchive(
		context.Background(),
		destination,
		result.Object.Key,
		&truncated,
		passphrase,
		result.Object.SHA256,
	); err == nil || !strings.Contains(err.Error(), "footer missing") {
		t.Fatalf("truncation error = %v", err)
	}
}
