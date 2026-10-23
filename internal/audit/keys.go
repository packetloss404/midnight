package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// ErrSigningKeyMissing is returned when a Verify is asked to validate a
// checkpoint whose key fingerprint is not in the verifier's keyring.
var ErrSigningKeyMissing = errors.New("audit: signing key not found")

// ErrSigningKeyMalformed is returned for malformed key files.
var ErrSigningKeyMalformed = errors.New("audit: signing key file malformed")

// SigningKey wraps an Ed25519 keypair plus its fingerprint. Lane 4 uses
// PEM-encoded raw seeds for portability across the CLI + the eventual
// installer.
type SigningKey struct {
	Public      ed25519.PublicKey
	Private     ed25519.PrivateKey
	Fingerprint string
}

// VerifyingKey is the public-only counterpart used by the verifier. The
// CLI ships with the option to load a single trusted key or a directory
// of trusted public keys (PEM files). Only the public key is needed for
// verification.
type VerifyingKey struct {
	Public      ed25519.PublicKey
	Fingerprint string
}

// FingerprintPublic returns the panel-locked stable fingerprint format
// (lowercase hex SHA-256 over the raw 32-byte public key).
func FingerprintPublic(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// GenerateSigningKey returns a freshly generated keypair with its
// panel-locked fingerprint.
func GenerateSigningKey() (SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SigningKey{}, fmt.Errorf("audit: generate signing key: %w", err)
	}
	return SigningKey{Public: pub, Private: priv, Fingerprint: FingerprintPublic(pub)}, nil
}

// EncodeSigningKeyPEM returns a PEM-encoded private key file. Format is
// "MIDNIGHT AUDIT PRIVATE KEY" with the raw 64-byte Ed25519 seed (the
// concatenation of the 32-byte seed + the 32-byte derived public key, as
// produced by ed25519.NewKeyFromSeed).
func EncodeSigningKeyPEM(k SigningKey) []byte {
	block := &pem.Block{
		Type:  "MIDNIGHT AUDIT PRIVATE KEY",
		Bytes: []byte(k.Private),
	}
	return pem.EncodeToMemory(block)
}

// EncodeVerifyingKeyPEM returns a PEM-encoded public key file (raw 32
// bytes).
func EncodeVerifyingKeyPEM(k SigningKey) []byte {
	block := &pem.Block{
		Type:  "MIDNIGHT AUDIT PUBLIC KEY",
		Bytes: []byte(k.Public),
	}
	return pem.EncodeToMemory(block)
}

// LoadSigningKeyPEM parses a PEM file produced by EncodeSigningKeyPEM.
func LoadSigningKeyPEM(data []byte) (SigningKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return SigningKey{}, ErrSigningKeyMalformed
	}
	if block.Type != "MIDNIGHT AUDIT PRIVATE KEY" {
		return SigningKey{}, fmt.Errorf("%w: type=%q", ErrSigningKeyMalformed, block.Type)
	}
	if l := len(block.Bytes); l != ed25519.PrivateKeySize {
		return SigningKey{}, fmt.Errorf("%w: bytes=%d", ErrSigningKeyMalformed, l)
	}
	priv := ed25519.PrivateKey(block.Bytes)
	pub := priv.Public().(ed25519.PublicKey)
	return SigningKey{Public: pub, Private: priv, Fingerprint: FingerprintPublic(pub)}, nil
}

// LoadVerifyingKeyPEM parses a PEM file produced by EncodeVerifyingKeyPEM.
func LoadVerifyingKeyPEM(data []byte) (VerifyingKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return VerifyingKey{}, ErrSigningKeyMalformed
	}
	if block.Type != "MIDNIGHT AUDIT PUBLIC KEY" {
		return VerifyingKey{}, fmt.Errorf("%w: type=%q", ErrSigningKeyMalformed, block.Type)
	}
	if l := len(block.Bytes); l != ed25519.PublicKeySize {
		return VerifyingKey{}, fmt.Errorf("%w: bytes=%d", ErrSigningKeyMalformed, l)
	}
	pub := ed25519.PublicKey(block.Bytes)
	return VerifyingKey{Public: pub, Fingerprint: FingerprintPublic(pub)}, nil
}

// LoadSigningKeyFile is a convenience wrapper around LoadSigningKeyPEM
// that reads the supplied path.
func LoadSigningKeyFile(path string) (SigningKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SigningKey{}, err
	}
	return LoadSigningKeyPEM(data)
}

// LoadVerifyingKeyFile is a convenience wrapper around LoadVerifyingKeyPEM.
func LoadVerifyingKeyFile(path string) (VerifyingKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VerifyingKey{}, err
	}
	return LoadVerifyingKeyPEM(data)
}

// CheckpointSigningInput returns the byte-identical canonical input the
// signature is computed over: a sorted-key JSON envelope of the
// integrity-protected fields per FEATURE_AUDIT_HASH_CHAIN.md §"Verify
// Algorithm" step 4.
func CheckpointSigningInput(cp Checkpoint) ([]byte, error) {
	fields := map[string]any{
		"partition_label":         cp.PartitionLabel,
		"started_at":              cp.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"ended_at":                cp.EndedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"first_row_id":            cp.FirstRowID,
		"last_row_id":             cp.LastRowID,
		"last_row_hash":           hex.EncodeToString(cp.LastRowHash),
		"prev_checkpoint_id":      cp.PrevCheckpointID,
		"signing_key_fingerprint": cp.SigningKeyFingerprint,
	}
	return canonicalEncode(fields)
}

// SignCheckpoint signs the canonical input with the supplied SigningKey
// and returns a Checkpoint with Signature + SigningKeyFingerprint
// populated.
func SignCheckpoint(cp Checkpoint, k SigningKey) (Checkpoint, error) {
	cp.SignatureAlg = "ed25519"
	cp.SigningKeyFingerprint = k.Fingerprint
	input, err := CheckpointSigningInput(cp)
	if err != nil {
		return Checkpoint{}, err
	}
	cp.Signature = ed25519.Sign(k.Private, input)
	return cp, nil
}

// VerifyCheckpoint verifies a Checkpoint signature against the supplied
// VerifyingKey. Returns nil when the signature matches; ErrSignatureBad
// otherwise.
func VerifyCheckpoint(cp Checkpoint, key VerifyingKey) error {
	if cp.SigningKeyFingerprint != key.Fingerprint {
		return ErrSigningKeyMissing
	}
	input, err := CheckpointSigningInput(cp)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key.Public, input, cp.Signature) {
		return ErrCheckpointSignatureBad
	}
	return nil
}

// ErrCheckpointSignatureBad is returned when a checkpoint's signature
// does not match.
var ErrCheckpointSignatureBad = errors.New("audit: checkpoint signature invalid")
