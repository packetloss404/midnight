package docker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestDetectCGroupModeV2(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("cpu memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	mode, err := detectCGroupMode(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != CGroupModeV2 {
		t.Fatalf("expected v2, got %s", mode)
	}
}

func TestDetectCGroupModeV1(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cpu"), 0o755); err != nil {
		t.Fatal(err)
	}
	mode, err := detectCGroupMode(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != CGroupModeV1 {
		t.Fatalf("expected v1, got %s", mode)
	}
}

func TestDetectCGroupModeHybrid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	mode, err := detectCGroupMode(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != CGroupModeHybrid {
		t.Fatalf("expected hybrid, got %s", mode)
	}
}

func TestDetectCGroupModeUnknownWhenMissing(t *testing.T) {
	mode, err := detectCGroupMode(filepath.Join(t.TempDir(), "no-such"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != CGroupModeUnknown {
		t.Fatalf("expected unknown, got %s", mode)
	}
}

func TestEnsureCGroupV2(t *testing.T) {
	if err := EnsureCGroupV2(CGroupModeV2); err != nil {
		t.Fatalf("expected v2 to be accepted: %v", err)
	}
	for _, mode := range []CGroupMode{CGroupModeV1, CGroupModeHybrid, CGroupModeUnknown, ""} {
		err := EnsureCGroupV2(mode)
		if err == nil {
			t.Fatalf("expected error for mode %q", mode)
		}
		if CodeOf(err) != failurecodes.CGroupV2Required {
			t.Fatalf("expected CGROUP_V2_REQUIRED for mode %q, got %s", mode, CodeOf(err))
		}
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatal("expected typed *Error")
		}
		if typed.Message != CGroupV2RemediationMessage {
			t.Fatalf("expected panel-locked remediation message, got %q", typed.Message)
		}
	}
}
