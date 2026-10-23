package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	key := Key{KID: "whk_active", Material: []byte("super-secret-active-key")}
	body := []byte(`{"hello":"world"}`)
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	header, err := Signer{}.SignBody([]Key{key}, body, ts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.Contains(header, "kid=whk_active") {
		t.Fatalf("header missing kid: %s", header)
	}
	parsed, err := ParseSignature(header)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.KID != "whk_active" {
		t.Fatalf("kid mismatch")
	}
	if err := VerifySignature(parsed, []Key{key}, body, ts); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSignAndVerifyDeliveryRoundTrip(t *testing.T) {
	key := Key{KID: "whk_active", Material: []byte("super-secret-active-key")}
	body := []byte(`{"hello":"world"}`)
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	header, err := Signer{}.SignDelivery([]Key{key}, "dlv_123", body, ts)
	if err != nil {
		t.Fatalf("sign delivery: %v", err)
	}
	if !strings.Contains(header, "v2=") {
		t.Fatalf("header missing v2 signature: %s", header)
	}
	parsed, err := ParseSignature(header)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := VerifyDeliverySignature(parsed, []Key{key}, "dlv_123", body, ts); err != nil {
		t.Fatalf("verify delivery: %v", err)
	}
	if err := VerifyDeliverySignature(parsed, []Key{key}, "dlv_other", body, ts); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("cross-delivery replay = %v, want mismatch", err)
	}
	if err := VerifySignature(parsed, []Key{key}, body, ts); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("body-only verification accepted v2 signature: %v", err)
	}
}

func TestSignDeliveryRejectsInvalidIdentity(t *testing.T) {
	key := Key{KID: "whk_active", Material: []byte("secret")}
	for _, deliveryID := range []string{"", " leading", "line\nbreak", "ambiguous.id", strings.Repeat("x", 257)} {
		if _, err := (Signer{}).SignDelivery([]Key{key}, deliveryID, nil, time.Now()); !errors.Is(err, ErrDeliveryIDInvalid) {
			t.Fatalf("delivery id %q = %v, want invalid", deliveryID, err)
		}
	}
}

func TestVerifyDeliveryAllowsLegacyV1(t *testing.T) {
	key := Key{KID: "whk_active", Material: []byte("secret")}
	body := []byte("payload")
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	header, err := Signer{}.SignBody([]Key{key}, body, ts)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignature(header)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDeliverySignature(parsed, []Key{key}, "dlv_legacy", body, ts); err != nil {
		t.Fatalf("legacy verify: %v", err)
	}
}

func TestVerifyReplayWindow(t *testing.T) {
	key := Key{KID: "whk_a", Material: []byte("k")}
	body := []byte("body")
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	header, _ := Signer{}.SignBody([]Key{key}, body, ts)
	parsed, _ := ParseSignature(header)
	// 301s in the future == reject.
	now := ts.Add(301 * time.Second)
	if err := VerifySignature(parsed, []Key{key}, body, now); !errors.Is(err, ErrReplayRejected) {
		t.Fatalf("replay 301s = %v", err)
	}
	// 299s in the future == accept.
	if err := VerifySignature(parsed, []Key{key}, body, ts.Add(299*time.Second)); err != nil {
		t.Fatalf("replay 299s = %v", err)
	}
}

func TestVerifyRotationOverlap(t *testing.T) {
	old := Key{KID: "whk_old", Material: []byte("old-secret")}
	new := Key{KID: "whk_new", Material: []byte("new-secret")}
	body := []byte("payload")
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	// Sign with both keys (rotation overlap header).
	header, err := Signer{}.SignBody([]Key{new, old}, body, ts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parsed, _ := ParseSignature(header)
	if len(parsed.V1) != 2 {
		t.Fatalf("expected 2 v1 segments, got %d", len(parsed.V1))
	}
	// Subscriber holds only the old key; verification should still
	// pass because the old key was used to compute one v1 segment.
	if err := VerifySignature(parsed, []Key{old}, body, ts); err != nil {
		t.Fatalf("subscriber with old key only: %v", err)
	}
	// Subscriber with new key only.
	if err := VerifySignature(parsed, []Key{new}, body, ts); err != nil {
		t.Fatalf("subscriber with new key only: %v", err)
	}
	// Subscriber with neither key fails.
	if err := VerifySignature(parsed, []Key{{KID: "x", Material: []byte("nope")}}, body, ts); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestParseSignatureMalformed(t *testing.T) {
	cases := []string{
		"",
		"bad",
		"t=abc,v1=00",
		"v1=" + strings.Repeat("0", 64),
	}
	for _, h := range cases {
		if _, err := ParseSignature(h); !errors.Is(err, ErrSignatureMalformed) {
			t.Fatalf("%q expected malformed, got %v", h, err)
		}
	}
}

func TestVerifyGitHubSignature(t *testing.T) {
	key := Key{KID: "ghk_a", Material: []byte("github-secret")}
	body := []byte(`{"action":"opened"}`)
	expected := "sha256=" + hmacSHA256Hex(key.Material, body)
	if err := VerifyGitHubSignature(expected, []Key{key}, body); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyGitHubSignature("sha256=ff", []Key{key}, body); !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("malformed: %v", err)
	}
	if err := VerifyGitHubSignature("md5=abc", []Key{key}, body); !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("wrong algorithm: %v", err)
	}
}
