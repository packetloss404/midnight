// Package bootstrap implements the panel-locked admin first-login
// flow: one-shot bootstrap-token, mandatory password set + TOTP
// enrollment per RFC 6238 / RFC 4226, and the audit hooks every step
// emits.
//
// The TOTP implementation here is a stdlib-only RFC 6238 (HOTP +
// 30-second window) using crypto/hmac + crypto/sha1. We deliberately
// do not pull a heavy dep just for TOTP; the algorithm is small and
// well-understood.
//
//	T = floor((Time - T0) / X)        // counter
//	HOTP(K, T) = Truncate(HMAC-SHA-1(K, T))
//
// where Truncate is the dynamic-truncation function from RFC 4226.
package bootstrap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTPDigits is the panel-locked code length (6 digits).
const TOTPDigits = 6

// TOTPPeriod is the panel-locked code period (30 seconds).
const TOTPPeriod = 30 * time.Second

// TOTPSkew is the panel-locked allowed clock skew window (±1 step).
// Per RFC 6238 §5.2 verifiers should accept the previous step to
// tolerate a one-time drift between the user and the server.
const TOTPSkew = 1

// TOTPSecretBytes is the panel-locked secret length (160 bits / 20
// bytes per RFC 4226 §4 recommendation).
const TOTPSecretBytes = 20

// ErrInvalidTOTP is returned when a supplied code does not match the
// expected HOTP for the current window (or any window inside the
// skew tolerance).
var ErrInvalidTOTP = errors.New("totp code is invalid")

// GenerateTOTPSecret returns a fresh 160-bit secret base32 encoded
// without padding (the otpauth:// URI shape RFC 6238 §3 expects).
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, TOTPSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="), nil
}

// DecodeTOTPSecret returns the raw 20 bytes for a base32-encoded
// secret. Padding is optional. Returns an error for an empty or
// short secret.
func DecodeTOTPSecret(secret string) ([]byte, error) {
	clean := strings.ToUpper(strings.TrimSpace(secret))
	clean = strings.ReplaceAll(clean, " ", "")
	if clean == "" {
		return nil, errors.New("totp secret is empty")
	}
	// Re-pad the base32 secret to a multiple of 8.
	if rem := len(clean) % 8; rem != 0 {
		clean += strings.Repeat("=", 8-rem)
	}
	raw, err := base32.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("decode base32 secret: %w", err)
	}
	if len(raw) < 16 {
		return nil, errors.New("totp secret is shorter than 128 bits")
	}
	return raw, nil
}

// GenerateTOTP returns the 6-digit code for secret at instant t.
// Implements HOTP (RFC 4226) on top of the (T - T0) / 30s counter.
func GenerateTOTP(secret []byte, t time.Time) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("totp secret is empty")
	}
	counter := uint64(t.Unix()) / uint64(TOTPPeriod.Seconds())
	return generateHOTP(secret, counter), nil
}

// VerifyTOTP returns nil if code matches GenerateTOTP(secret, t)
// or any value of GenerateTOTP at +/-skew steps. Comparison uses
// hmac.Equal so a constant-time path is taken.
func VerifyTOTP(secret []byte, code string, t time.Time) error {
	want := strings.TrimSpace(code)
	if len(want) != TOTPDigits {
		return ErrInvalidTOTP
	}
	if len(secret) == 0 {
		return ErrInvalidTOTP
	}
	counter := uint64(t.Unix()) / uint64(TOTPPeriod.Seconds())
	for delta := int64(-TOTPSkew); delta <= int64(TOTPSkew); delta++ {
		candidate := generateHOTP(secret, counter+uint64(delta))
		if hmac.Equal([]byte(candidate), []byte(want)) {
			return nil
		}
	}
	return ErrInvalidTOTP
}

func generateHOTP(secret []byte, counter uint64) string {
	mac := hmac.New(sha1.New, secret)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac.Write(buf)
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	binCode := (uint32(digest[offset]&0x7f) << 24) |
		(uint32(digest[offset+1]&0xff) << 16) |
		(uint32(digest[offset+2]&0xff) << 8) |
		uint32(digest[offset+3]&0xff)
	mod := uint32(1)
	for i := 0; i < TOTPDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", TOTPDigits, binCode%mod)
}

// OTPAuthURI returns the otpauth:// URI an authenticator app reads
// to provision the TOTP secret. The shape is locked to RFC 6238 §3
// canonical:
//
//	otpauth://totp/<issuer>:<account>?secret=<base32>&issuer=<issuer>&algorithm=SHA1&digits=6&period=30
//
// We percent-encode the label component aggressively (account-name @
// → %40) so the URI is safe inside QR-code text payloads.
func OTPAuthURI(issuer, account, secretBase32 string) string {
	if issuer == "" {
		issuer = "Midnight"
	}
	label := url.QueryEscape(issuer + ":" + account)
	// QueryEscape converts ":" to "%3A"; revert that single character
	// so the canonical "issuer:account" separator stays readable.
	label = strings.Replace(label, "%3A", ":", 1)
	// QueryEscape uses "+" for spaces; otpauth labels conventionally
	// use "%20", so flip them.
	label = strings.ReplaceAll(label, "+", "%20")
	q := url.Values{}
	q.Set("secret", strings.TrimRight(secretBase32, "="))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", TOTPDigits))
	q.Set("period", fmt.Sprintf("%d", int(TOTPPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
