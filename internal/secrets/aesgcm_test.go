package secrets

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestAESGCMRoundTrip(t *testing.T) {
	key := make([]byte, dekLength)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	aad := []byte("var_test_id")
	plain := []byte("hunter2-correct-horse-battery-staple")
	sealed, err := aesGCMSeal(key, aad, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := aesGCMOpen(key, aad, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip got %q want %q", got, plain)
	}
}

func TestAESGCMNonceUniqueness(t *testing.T) {
	key := make([]byte, dekLength)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	plain := []byte("the-same-plaintext-each-call")
	seen := map[string]bool{}
	for i := 0; i < 1024; i++ {
		out, err := aesGCMSeal(key, nil, plain)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		nonce := string(out[:nonceLen])
		if seen[nonce] {
			t.Fatalf("nonce reuse detected at iteration %d", i)
		}
		seen[nonce] = true
	}
}

func TestAESGCMTamperRejected(t *testing.T) {
	key := make([]byte, dekLength)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed, err := aesGCMSeal(key, []byte("aad"), []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Flip a bit in the ciphertext.
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := aesGCMOpen(key, []byte("aad"), tampered); err == nil {
		t.Fatalf("expected tamper rejection")
	}
}

func TestAESGCMWrongAAD(t *testing.T) {
	key := make([]byte, dekLength)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed, err := aesGCMSeal(key, []byte("var_a"), []byte("plain"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := aesGCMOpen(key, []byte("var_b"), sealed); err == nil {
		t.Fatalf("expected aad mismatch rejection")
	}
}

func TestAESGCMRejectsShortKey(t *testing.T) {
	if _, err := aesGCMSeal(make([]byte, 16), nil, nil); err == nil {
		t.Fatalf("expected wrong key length to fail")
	}
}
