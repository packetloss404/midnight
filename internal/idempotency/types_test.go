package idempotency

import (
	"errors"
	"testing"
)

func TestStatesUseStableWireValues(t *testing.T) {
	if StateReserved != "RESERVED" || StateSucceeded != "SUCCEEDED" {
		t.Fatalf("unexpected states: %q %q", StateReserved, StateSucceeded)
	}
}

func TestValidateKeyAcceptsOpaqueToken(t *testing.T) {
	if err := ValidateKey(Key("deploy-01HZX_opaque.token")); err != nil {
		t.Fatalf("ValidateKey() error = %v", err)
	}
}

func TestValidateKeyRejectsEmptyWhitespaceAndTooLong(t *testing.T) {
	for _, key := range []Key{"", "has space", Key(string(make([]byte, 256)))} {
		if err := ValidateKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("ValidateKey(%q) error = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestBodyHashSHA256HashesRawBytes(t *testing.T) {
	got := BodyHashSHA256([]byte(`{"b":2,"a":1}`))
	const want = "3fb75453225c732a76b7899ea2096dda1455189c89817239732182f73fe5a09f"
	if got != want {
		t.Fatalf("BodyHashSHA256() = %q, want %q", got, want)
	}
}
