package registry

import (
	"testing"
	"time"
)

func TestPinStoreIncrRefAndDecrRefRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	store := NewPinStore()

	row, err := store.IncrRef(IncrRefInput{
		Repository:   "midnight/web",
		Digest:       "sha256:active",
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("IncrRef() error = %v", err)
	}
	if row.Digest != "sha256:active" || row.Reason != PinReasonActive || row.Repository != "midnight/web" {
		t.Fatalf("row = %#v", row)
	}
	if got := store.PinCount("sha256:active", now); got != 1 {
		t.Fatalf("PinCount = %d, want 1", got)
	}

	// Inserting the same tuple again must fail (PRIMARY KEY constraint).
	if _, err := store.IncrRef(IncrRefInput{
		Repository:   "midnight/web",
		Digest:       "sha256:active",
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
		CreatedAt:    now,
	}); err == nil {
		t.Fatal("IncrRef(duplicate) error = nil, want refusal")
	}

	if err := store.DecrRef("sha256:active", "svc", "dep-1", PinReasonActive); err != nil {
		t.Fatalf("DecrRef() error = %v", err)
	}
	if got := store.PinCount("sha256:active", now); got != 0 {
		t.Fatalf("PinCount after DecrRef = %d, want 0", got)
	}
}

func TestProtectDuringRollbackWindowPinsThroughWindow(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	store := NewPinStore()

	row, err := store.ProtectDuringRollbackWindow(ProtectDuringRollbackWindowInput{
		Repository:   "midnight/web",
		Digest:       "sha256:rollback",
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Now:          now,
	})
	if err != nil {
		t.Fatalf("ProtectDuringRollbackWindow() error = %v", err)
	}
	if row.Reason != PinReasonRollbackWindow {
		t.Fatalf("reason = %q, want %q", row.Reason, PinReasonRollbackWindow)
	}
	want := now.Add(PanelLockedRollbackWindow)
	if !row.PinnedUntil.Equal(want) {
		t.Fatalf("PinnedUntil = %s, want %s", row.PinnedUntil, want)
	}

	// Probe must show the digest protected during the window and unprotected
	// after.
	probeDuring := store.ProbeProtectedDigest("midnight/web", "sha256:rollback", now.Add(time.Hour))
	if !probeDuring.Protected {
		t.Fatalf("probe during window protected = false, want true")
	}
	probeAfter := store.ProbeProtectedDigest("midnight/web", "sha256:rollback", want.Add(time.Second))
	if probeAfter.Protected {
		t.Fatalf("probe after window protected = true, want false")
	}
}

func TestApplyProtectedDigestProbeRefusesProtected(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	store := NewPinStore()

	if _, err := store.IncrRef(IncrRefInput{
		Repository:   "midnight/web",
		Digest:       "sha256:active",
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("IncrRef() error = %v", err)
	}

	if err := store.ApplyProtectedDigestProbe("midnight/web", "sha256:active", now); err == nil {
		t.Fatal("ApplyProtectedDigestProbe(active) error = nil, want refusal")
	}
	if err := store.ApplyProtectedDigestProbe("midnight/web", "sha256:other", now); err != nil {
		t.Fatalf("ApplyProtectedDigestProbe(other) error = %v, want nil", err)
	}
}

func TestProtectDuringRollbackWindowAllowsCustomWindow(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	store := NewPinStore()

	row, err := store.ProtectDuringRollbackWindow(ProtectDuringRollbackWindowInput{
		Repository:   "midnight/web",
		Digest:       "sha256:rollback",
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Now:          now,
		Window:       3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("ProtectDuringRollbackWindow() error = %v", err)
	}
	want := now.Add(3 * time.Hour)
	if !row.PinnedUntil.Equal(want) {
		t.Fatalf("PinnedUntil = %s, want %s", row.PinnedUntil, want)
	}
}

func TestPinStoreIncrRefRejectsBadInput(t *testing.T) {
	store := NewPinStore()
	if _, err := store.IncrRef(IncrRefInput{Digest: "sha256:abcd"}); err == nil {
		t.Fatal("IncrRef(missing repository) error = nil, want refusal")
	}
	if err := store.DecrRef("", "", "", PinReasonActive); err == nil {
		t.Fatal("DecrRef(empty) error = nil, want refusal")
	}
}
