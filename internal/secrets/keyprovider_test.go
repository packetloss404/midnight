package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newTestProvider(t *testing.T) *MemoryKeyProvider {
	t.Helper()
	material := bytes.Repeat([]byte{0xab}, kekLength)
	provider, err := NewMemoryKeyProvider(material, 1)
	if err != nil {
		t.Fatalf("new memory provider: %v", err)
	}
	return provider
}

func TestMemoryKeyProviderWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	dek := bytes.Repeat([]byte{0x01}, dekLength)
	id, wrapped, err := p.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := p.Unwrap(ctx, id, wrapped)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("dek roundtrip mismatch")
	}
}

func TestMemoryKeyProviderRotateGrace(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	dek := bytes.Repeat([]byte{0x42}, dekLength)
	prevID, wrapped, err := p.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	newID, prev, err := p.Rotate(ctx)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if prev != prevID {
		t.Fatalf("rotate previous = %s want %s", prev, prevID)
	}
	if newID == prevID {
		t.Fatalf("rotate new id equals previous")
	}
	// Previous KEK must still unwrap during the grace window.
	if _, err := p.Unwrap(ctx, prevID, wrapped); err != nil {
		t.Fatalf("unwrap during grace: %v", err)
	}
	// A second rotation expires the first generation; PurgeExpired
	// removes it.
	if _, _, err := p.Rotate(ctx); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	purged, err := p.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	found := false
	for _, id := range purged {
		if id == prevID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected prevID %s to be purged, got %v", prevID, purged)
	}
	// After purge the previous KEK must surface ErrKEKUnknown.
	if _, err := p.Unwrap(ctx, prevID, wrapped); !errors.Is(err, ErrKEKUnknown) {
		t.Fatalf("unwrap after purge = %v want ErrKEKUnknown", err)
	}
}

func TestMemoryKeyProviderFingerprintStableWithinGeneration(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	fp1, err := p.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("fp1: %v", err)
	}
	fp2, err := p.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("fp2: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint drifted within generation: %s vs %s", fp1, fp2)
	}
	if _, _, err := p.Rotate(ctx); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	fp3, err := p.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("fp3: %v", err)
	}
	if fp3 == fp1 {
		t.Fatalf("rotation did not change fingerprint")
	}
}

func TestLoadKEKMaterialMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := FileKEKConfig{Path: filepath.Join(dir, "missing"), AllowAnyMode: true}
	_, err := LoadKEKMaterial(cfg)
	if !errors.Is(err, ErrKEKNotFound) {
		t.Fatalf("missing kek: %v", err)
	}
}

func TestLoadKEKMaterialBadLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek")
	if err := os.WriteFile(path, []byte("too-short"), 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := FileKEKConfig{Path: path, AllowAnyMode: true}
	_, err := LoadKEKMaterial(cfg)
	if !errors.Is(err, ErrKEKLength) {
		t.Fatalf("bad length: %v", err)
	}
}

func TestLoadKEKMaterialOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek")
	material := bytes.Repeat([]byte{0x55}, kekLength)
	if err := os.WriteFile(path, material, 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := FileKEKConfig{Path: path}
	if runtime.GOOS == "windows" {
		cfg.AllowAnyMode = true
	}
	got, err := LoadKEKMaterial(cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(got, material) {
		t.Fatalf("material mismatch")
	}
}

func TestNewFileKeyProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek")
	material := bytes.Repeat([]byte{0x77}, kekLength)
	if err := os.WriteFile(path, material, 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := FileKEKConfig{Path: path, AllowAnyMode: true}
	provider, err := NewFileKeyProvider(cfg)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	id, _, err := provider.Wrap(context.Background(), bytes.Repeat([]byte{1}, dekLength))
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if id == "" {
		t.Fatalf("expected non-empty kek id")
	}
}

func TestDefaultKEKPathRespectsEnv(t *testing.T) {
	t.Setenv("MIDNIGHT_KEK_PATH", filepath.FromSlash("/tmp/midnight-test-kek"))
	if got := DefaultKEKPath(); got != filepath.FromSlash("/tmp/midnight-test-kek") {
		t.Fatalf("default kek path = %s want override", got)
	}
}
