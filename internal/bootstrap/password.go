package bootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// PasswordHashScheme is the scheme tag at the start of every stored
// password hash. We use PBKDF2-SHA-256 with 600_000 iterations, the
// 2023 OWASP recommendation. Argon2id would be better but adding the
// `golang.org/x/crypto/argon2` import pulls in C bindings on some
// platforms; PBKDF2 is stdlib-friendly and adequate for the alpha
// admin password (rate-limited at the API surface anyway).
const PasswordHashScheme = "pbkdf2-sha256"

// PasswordHashIterations is the panel-locked KDF iteration count.
const PasswordHashIterations = 600000

// PasswordHashKeyLength is the panel-locked derived-key length (32
// bytes / 256 bits — matches the SHA-256 output).
const PasswordHashKeyLength = 32

// PasswordHashSaltLength is the panel-locked salt length (16 bytes).
const PasswordHashSaltLength = 16

// MinAdminPasswordLength is the panel-locked minimum admin password
// length. We refuse anything shorter at the API surface so the
// bootstrap flow cannot enroll a weak password silently.
const MinAdminPasswordLength = 12

// ErrPasswordTooShort is returned by HashPassword when the supplied
// password is below the minimum length.
var ErrPasswordTooShort = errors.New("password is shorter than the minimum length")

// ErrPasswordMismatch is returned by VerifyPassword when the stored
// hash does not match the supplied password.
var ErrPasswordMismatch = errors.New("password does not match the stored hash")

// HashPassword returns a self-describing PHC-style hash:
//
//	pbkdf2-sha256$<iterations>$<base64-salt>$<base64-key>
//
// The hash is intentionally version-tagged so a future scheme bump
// (Argon2id) keeps stored values verifiable.
func HashPassword(password string) (string, error) {
	if len(password) < MinAdminPasswordLength {
		return "", ErrPasswordTooShort
	}
	salt := make([]byte, PasswordHashSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt entropy: %w", err)
	}
	key := pbkdf2.Key([]byte(password), salt, PasswordHashIterations, PasswordHashKeyLength, sha256.New)
	return fmt.Sprintf("%s$%d$%s$%s",
		PasswordHashScheme,
		PasswordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword compares password against a stored hash. The
// comparison is constant-time (subtle.ConstantTimeCompare).
func VerifyPassword(password, stored string) error {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return errors.New("password hash format is invalid")
	}
	if parts[0] != PasswordHashScheme {
		return fmt.Errorf("unsupported password hash scheme %q", parts[0])
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter <= 0 {
		return fmt.Errorf("password hash iteration count is invalid: %s", parts[1])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return fmt.Errorf("decode key: %w", err)
	}
	candidate := pbkdf2.Key([]byte(password), salt, iter, len(expected), sha256.New)
	if subtle.ConstantTimeCompare(candidate, expected) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}
