package api

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestLogCursorRoundTripIsOpaqueAndScopeBound(t *testing.T) {
	codec := NewCursorCodec([]byte("test cursor secret"))
	const file = "/var/lib/midnight/logs/runtime-000042.ndjson"

	raw, err := codec.EncodeLogCursor("op_1", "runtime", "ready", logrecords.Cursor{File: file, Offset: 42})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, logCursorPrefix) {
		t.Fatalf("cursor = %q, want %q prefix", raw, logCursorPrefix)
	}
	if strings.Contains(raw, "runtime") || strings.Contains(raw, "000042") {
		t.Fatalf("cursor exposes storage details: %q", raw)
	}
	encoded := strings.TrimPrefix(raw, logCursorPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoded), file) {
		t.Fatalf("decoded token exposes host filename %q", file)
	}

	got, err := codec.DecodeLogCursor(raw, "op_1", "runtime", "ready")
	if err != nil {
		t.Fatal(err)
	}
	if got.File != file || got.Offset != 42 {
		t.Fatalf("DecodeLogCursor() = %#v", got)
	}

	for _, mismatch := range []struct {
		operationID string
		surface     string
		query       string
	}{
		{operationID: "op_2", surface: "runtime", query: "ready"},
		{operationID: "op_1", surface: "build", query: "ready"},
		{operationID: "op_1", surface: "runtime", query: "different"},
	} {
		if _, err := codec.DecodeLogCursor(raw, mismatch.operationID, mismatch.surface, mismatch.query); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
			t.Fatalf("DecodeLogCursor(%#v) err = %v, want invalid cursor", mismatch, err)
		}
	}
}

func TestLogCursorRejectsTamperingAndGenericCursorConfusion(t *testing.T) {
	codec := NewCursorCodec([]byte("test cursor secret"))
	raw, err := codec.EncodeLogCursor("op_1", "runtime", "", logrecords.Cursor{File: "runtime.ndjson", Offset: 7})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, logCursorPrefix))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0x01
	tampered := logCursorPrefix + base64.RawURLEncoding.EncodeToString(sealed)
	if _, err := codec.DecodeLogCursor(tampered, "op_1", "runtime", ""); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("tampered log cursor err = %v", err)
	}
	if _, err := codec.Decode(raw); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("generic cursor accepted log cursor: %v", err)
	}

	generic, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.DecodeLogCursor(generic, "op_1", "runtime", ""); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("log cursor accepted generic cursor: %v", err)
	}
}
