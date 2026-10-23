package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestCursorCodecRoundTripsSignedToken(t *testing.T) {
	codec := NewCursorCodec([]byte("test-secret"))

	raw, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 12})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "s.") {
		t.Fatalf("signed token = %q, want s. prefix", raw)
	}

	got, err := codec.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != CursorTokenSigned || got.Offset != 12 {
		t.Fatalf("Decode() = %#v, want signed offset 12", got)
	}
}

func TestCursorCodecRejectsUnsignedTokenWhenSecretConfigured(t *testing.T) {
	codec := NewCursorCodec([]byte("test-secret"))

	if _, err := codec.Encode(CursorToken{Kind: CursorTokenUnsigned, Offset: 3}); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Encode(unsigned) error = %v, want ErrInvalidCursorPage", err)
	}
	unsignedCodec := NewCursorCodec(nil)
	raw, err := unsignedCodec.Encode(CursorToken{Kind: CursorTokenUnsigned, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(raw); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode(unsigned with signing secret) error = %v, want ErrInvalidCursorPage", err)
	}
	got, err := unsignedCodec.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != CursorTokenUnsigned || got.Offset != 3 {
		t.Fatalf("Decode() = %#v, want unsigned offset 3", got)
	}
}

func TestCursorCodecRejectsTamperedSignedToken(t *testing.T) {
	codec := NewCursorCodec([]byte("test-secret"))
	raw, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 12})
	if err != nil {
		t.Fatal(err)
	}
	tampered := raw[:len(raw)-1] + "x"

	if _, err := codec.Decode(tampered); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("Decode(tampered) error = %v, want ErrInvalidCursorPage", err)
	}
}

func TestOperationCursorIsSignedBoundAndExpires(t *testing.T) {
	codec := NewCursorCodec([]byte("operation-cursor-test-secret"))
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	want := OperationListCursor{
		CreatedAt: now.Add(-time.Minute),
		ID:        "op_cursor",
	}
	raw, err := codec.EncodeOperationCursor(want, "binding-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "op1.") {
		t.Fatalf("operation cursor = %q, want signed op1 prefix", raw)
	}
	got, err := codec.DecodeOperationCursor(raw, "binding-a", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DecodeOperationCursor() = %#v, want %#v", got, want)
	}
	if _, err := codec.DecodeOperationCursor(raw, "binding-b", now.Add(time.Minute)); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("cross-query decode error = %v, want ErrInvalidCursorPage", err)
	}
	if _, err := codec.DecodeOperationCursor(raw, "binding-a", now.Add(operationCursorTTL)); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("expired decode error = %v, want ErrInvalidCursorPage", err)
	}
}

func TestOperationCursorUnsignedCompatibilityIsKeylessOnly(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	position := OperationListCursor{CreatedAt: now, ID: "op_dev"}
	keyless := NewCursorCodec(nil)
	raw, err := keyless.EncodeOperationCursor(position, "dev-binding", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "ou1.") {
		t.Fatalf("keyless operation cursor = %q, want ou1 prefix", raw)
	}
	if _, err := NewCursorCodec([]byte("configured-secret")).DecodeOperationCursor(raw, "dev-binding", now); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("signed codec accepted unsigned operation cursor: %v", err)
	}
}
