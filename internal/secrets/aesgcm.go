package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// nonceLen is the AES-GCM nonce length in bytes (96 bits, panel-locked).
const nonceLen = 12

// dekLength is the panel-locked DEK length: AES-256.
const dekLength = 32

// errAESGCMShort is returned for malformed or truncated ciphertexts.
var errAESGCMShort = errors.New("midnight: aes-gcm payload too short")

// generateDEK returns a fresh 256-bit DEK from crypto/rand.
func generateDEK() ([]byte, error) {
	buf := make([]byte, dekLength)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("midnight: read dek bytes: %w", err)
	}
	return buf, nil
}

// generateNonce returns a fresh AES-GCM nonce. Callers MUST never reuse a
// nonce for a given key — Seal panics on misuse via the cipher package's
// internal check, but we additionally generate via crypto/rand which is
// CSPRNG-backed.
func generateNonce() ([]byte, error) {
	buf := make([]byte, nonceLen)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("midnight: read nonce: %w", err)
	}
	return buf, nil
}

// aesGCMSeal seals plaintext under the supplied 32-byte key using the
// supplied additional-authenticated-data (typically the kek_id or a
// scope-tag). Output layout: nonce || ciphertext || tag.
func aesGCMSeal(key, aad, plaintext []byte) ([]byte, error) {
	if len(key) != dekLength {
		return nil, fmt.Errorf("midnight: aes-gcm key len=%d want=%d", len(key), dekLength)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("midnight: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("midnight: aes-gcm new: %w", err)
	}
	nonce, err := generateNonce()
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, len(nonce)+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// aesGCMOpen opens a payload produced by aesGCMSeal.
func aesGCMOpen(key, aad, payload []byte) ([]byte, error) {
	if len(key) != dekLength {
		return nil, fmt.Errorf("midnight: aes-gcm key len=%d want=%d", len(key), dekLength)
	}
	if len(payload) < nonceLen+16 { // 16 = GCM tag
		return nil, errAESGCMShort
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("midnight: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("midnight: aes-gcm new: %w", err)
	}
	nonce := payload[:nonceLen]
	ciphertext := payload[nonceLen:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("midnight: aes-gcm open: %w", err)
	}
	return plaintext, nil
}
