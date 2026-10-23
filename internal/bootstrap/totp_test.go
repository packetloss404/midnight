package bootstrap

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func TestGenerateTOTPSecretLengthAndUniqueness(t *testing.T) {
	t.Parallel()

	a, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("first GenerateTOTPSecret: %v", err)
	}
	b, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("second GenerateTOTPSecret: %v", err)
	}
	if a == b {
		t.Fatal("expected two GenerateTOTPSecret calls to differ")
	}

	raw, err := base32.StdEncoding.DecodeString(a + strings.Repeat("=", (8-len(a)%8)%8))
	if err != nil {
		t.Fatalf("base32 decode: %v", err)
	}
	if len(raw) != TOTPSecretBytes {
		t.Fatalf("secret bytes = %d, want %d", len(raw), TOTPSecretBytes)
	}
}

// TestGenerateTOTPRFC6238Vectors exercises the canonical RFC 6238
// test vectors (Appendix B). The reference uses the ASCII secret
// "12345678901234567890" — base32-encoded as
// "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ".
func TestGenerateTOTPRFC6238Vectors(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	tests := []struct {
		when time.Time
		want string
	}{
		{when: time.Unix(59, 0), want: "287082"},
		{when: time.Unix(1111111109, 0), want: "081804"},
		{when: time.Unix(1111111111, 0), want: "050471"},
		{when: time.Unix(1234567890, 0), want: "005924"},
		{when: time.Unix(2000000000, 0), want: "279037"},
	}
	for _, tt := range tests {
		got, err := GenerateTOTP(secret, tt.when)
		if err != nil {
			t.Fatalf("GenerateTOTP(%d): %v", tt.when.Unix(), err)
		}
		if got != tt.want {
			t.Fatalf("GenerateTOTP(%d) = %q, want %q (RFC 6238 Appendix B)", tt.when.Unix(), got, tt.want)
		}
	}
}

func TestVerifyTOTPHonorsSkewWindow(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	prev := now.Add(-30 * time.Second)
	next := now.Add(30 * time.Second)

	prevCode, _ := GenerateTOTP(secret, prev)
	curCode, _ := GenerateTOTP(secret, now)
	nextCode, _ := GenerateTOTP(secret, next)

	if err := VerifyTOTP(secret, prevCode, now); err != nil {
		t.Fatalf("expected previous-step code %q to verify within skew, got %v", prevCode, err)
	}
	if err := VerifyTOTP(secret, curCode, now); err != nil {
		t.Fatalf("expected current code %q to verify, got %v", curCode, err)
	}
	if err := VerifyTOTP(secret, nextCode, now); err != nil {
		t.Fatalf("expected next-step code %q to verify within skew, got %v", nextCode, err)
	}
}

func TestVerifyTOTPRejectsOutOfWindow(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	farPast := now.Add(-2 * time.Minute)
	farPastCode, _ := GenerateTOTP(secret, farPast)
	if err := VerifyTOTP(secret, farPastCode, now); err == nil {
		t.Fatalf("expected verify to fail for code outside the skew window")
	}
}

func TestVerifyTOTPRejectsBadShape(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	for _, in := range []string{"", "12345", "1234567", "abcdef"} {
		if err := VerifyTOTP(secret, in, time.Unix(1234567890, 0)); err == nil {
			t.Fatalf("expected ErrInvalidTOTP for %q", in)
		}
	}
}

func TestDecodeTOTPSecretRoundTrip(t *testing.T) {
	t.Parallel()

	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	raw, err := DecodeTOTPSecret(secret)
	if err != nil {
		t.Fatalf("DecodeTOTPSecret: %v", err)
	}
	if len(raw) != TOTPSecretBytes {
		t.Fatalf("decoded length = %d, want %d", len(raw), TOTPSecretBytes)
	}
}

func TestOTPAuthURIShape(t *testing.T) {
	t.Parallel()

	uri := OTPAuthURI("Midnight", "admin@midnight.local", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/Midnight:admin%40midnight.local?") {
		t.Fatalf("uri does not match expected prefix: %s", uri)
	}
	for _, want := range []string{"secret=JBSWY3DPEHPK3PXP", "issuer=Midnight", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("uri missing %q: %s", want, uri)
		}
	}
}
