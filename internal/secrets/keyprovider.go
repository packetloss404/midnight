package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// kekLength is the locked KEK material length: AES-256 plus the size used
// for HKDF expansion in any future provider.
const kekLength = 32

// ErrKEKNotFound is returned when no KEK material is available.
var ErrKEKNotFound = errors.New("midnight: KEK material not found")

// ErrKEKWrongMode is returned when the on-disk KEK file is not 0400.
var ErrKEKWrongMode = errors.New("midnight: KEK file mode unexpected (want 0400)")

// ErrKEKLength is returned when the KEK material does not match the
// expected length.
var ErrKEKLength = errors.New("midnight: KEK material has wrong length")

// ErrKEKUnknown is returned when a referenced KEK id is not known by the
// provider — typically after the grace period has expired.
var ErrKEKUnknown = errors.New("midnight: KEK id is not known to the provider")

// KEKID identifies a single KEK generation. Format is "kek_<hex8>" derived
// from a stable fingerprint of the key material so that rotation produces
// a new id without exposing the secret.
type KEKID string

// KeyProvider is the panel-locked Phase 1 boundary between the Lane 4
// substrate and the eventual cloud-KMS / Vault adapters that Phase 4
// teams will introduce. Implementations MUST never return raw key bytes
// for a wrapped DEK — they only wrap and unwrap.
//
// Implementations are expected to be safe for concurrent use.
type KeyProvider interface {
	// ActiveKEK returns the id of the KEK that NEW DEKs should be wrapped
	// under. Decrypt MUST still accept envelopes wrapped under any non-
	// expired KEK id (rotation grace).
	ActiveKEK(ctx context.Context) (KEKID, error)

	// Wrap encrypts a freshly-generated DEK under the active KEK and
	// returns the (kek_id, wrapped_bytes) pair the caller stores alongside
	// the ciphertext.
	Wrap(ctx context.Context, dek []byte) (KEKID, []byte, error)

	// Unwrap decrypts a DEK previously wrapped under the supplied KEK id.
	// Returns ErrKEKUnknown if the KEK has been purged.
	Unwrap(ctx context.Context, kek KEKID, wrapped []byte) ([]byte, error)

	// Fingerprint returns a stable, non-secret identifier for the active
	// KEK. Used by midnight doctor and the install manifest.
	Fingerprint(ctx context.Context) (string, error)

	// Rotate generates a new KEK, makes it the active KEK, and retains
	// the previous KEK for the supplied grace window. The previous id is
	// returned so callers can drive a re-wrap loop.
	Rotate(ctx context.Context) (newID KEKID, previousID KEKID, err error)

	// PurgeExpired drops any KEK whose grace window has elapsed. The
	// caller is expected to have re-wrapped every DEK off the expiring
	// KEK before calling. Returns the ids that were purged.
	PurgeExpired(ctx context.Context) ([]KEKID, error)
}

// generateKEKMaterial returns 32 fresh random bytes via crypto/rand.
func generateKEKMaterial() ([]byte, error) {
	buf := make([]byte, kekLength)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("midnight: read kek material: %w", err)
	}
	return buf, nil
}

// kekIDFromMaterial derives a deterministic id from the KEK material so
// rotation produces a new id without leaking secrets. The id is the first
// 8 hex characters (32 bits) of SHA-256 over the material plus a domain
// label, which is sufficient given the small per-instance KEK count.
func kekIDFromMaterial(material []byte) KEKID {
	h := sha256.New()
	_, _ = h.Write([]byte("midnight-kek-id-v1"))
	_, _ = h.Write(material)
	sum := h.Sum(nil)
	return KEKID("kek_" + hex.EncodeToString(sum[:4]))
}

// MemoryKeyProvider is an in-memory KeyProvider used by tests, the CLI,
// and any caller that has resolved KEK material itself (e.g. read from
// disk). It maintains a small ring of previous KEKs to support rotation
// grace windows.
type MemoryKeyProvider struct {
	mu         sync.RWMutex
	keys       map[KEKID][]byte
	active     KEKID
	expiringAt map[KEKID]int64 // generation counter, not wall clock
	gen        int64
	graceTicks int64
}

// NewMemoryKeyProvider returns a MemoryKeyProvider seeded with the
// supplied KEK material. graceTicks is the number of rotations the
// previous KEK is kept around. graceTicks == 0 means the previous KEK is
// retained for one rotation only.
func NewMemoryKeyProvider(material []byte, graceTicks int64) (*MemoryKeyProvider, error) {
	if len(material) != kekLength {
		return nil, ErrKEKLength
	}
	id := kekIDFromMaterial(material)
	p := &MemoryKeyProvider{
		keys:       map[KEKID][]byte{id: append([]byte(nil), material...)},
		active:     id,
		expiringAt: map[KEKID]int64{},
		graceTicks: graceTicks,
	}
	return p, nil
}

// ActiveKEK returns the active KEK id.
func (p *MemoryKeyProvider) ActiveKEK(_ context.Context) (KEKID, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.active == "" {
		return "", ErrKEKNotFound
	}
	return p.active, nil
}

// Wrap wraps the supplied DEK under the active KEK using AES-GCM.
func (p *MemoryKeyProvider) Wrap(_ context.Context, dek []byte) (KEKID, []byte, error) {
	p.mu.RLock()
	id := p.active
	material, ok := p.keys[id]
	p.mu.RUnlock()
	if !ok {
		return "", nil, ErrKEKNotFound
	}
	wrapped, err := aesGCMSeal(material, []byte(id), dek)
	if err != nil {
		return "", nil, fmt.Errorf("midnight: wrap dek: %w", err)
	}
	return id, wrapped, nil
}

// Unwrap unwraps a DEK previously wrapped by Wrap.
func (p *MemoryKeyProvider) Unwrap(_ context.Context, id KEKID, wrapped []byte) ([]byte, error) {
	p.mu.RLock()
	material, ok := p.keys[id]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrKEKUnknown
	}
	dek, err := aesGCMOpen(material, []byte(id), wrapped)
	if err != nil {
		return nil, fmt.Errorf("midnight: unwrap dek: %w", err)
	}
	return dek, nil
}

// Fingerprint returns a stable identifier for the active KEK.
func (p *MemoryKeyProvider) Fingerprint(_ context.Context) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	material, ok := p.keys[p.active]
	if !ok {
		return "", ErrKEKNotFound
	}
	sum := sha256.Sum256(append([]byte("midnight-kek-fp-v1"), material...))
	return hex.EncodeToString(sum[:]), nil
}

// Rotate creates a fresh KEK, makes it active, and schedules the previous
// KEK for purge after graceTicks rotations.
func (p *MemoryKeyProvider) Rotate(_ context.Context) (KEKID, KEKID, error) {
	material, err := generateKEKMaterial()
	if err != nil {
		return "", "", err
	}
	newID := kekIDFromMaterial(material)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.keys[newID]; exists {
		// Astronomically unlikely; regenerate to avoid id collision.
		material, err = generateKEKMaterial()
		if err != nil {
			return "", "", err
		}
		newID = kekIDFromMaterial(material)
	}
	previousID := p.active
	p.keys[newID] = material
	p.active = newID
	p.gen++
	if previousID != "" && previousID != newID {
		p.expiringAt[previousID] = p.gen + p.graceTicks
	}
	return newID, previousID, nil
}

// PurgeExpired drops KEKs whose grace window has elapsed.
func (p *MemoryKeyProvider) PurgeExpired(_ context.Context) ([]KEKID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var purged []KEKID
	for id, expiresAt := range p.expiringAt {
		if expiresAt <= p.gen {
			delete(p.keys, id)
			delete(p.expiringAt, id)
			purged = append(purged, id)
		}
	}
	sort.Slice(purged, func(i, j int) bool { return purged[i] < purged[j] })
	return purged, nil
}

// FileKEKConfig configures FileKeyProvider.
type FileKEKConfig struct {
	// Path is the on-disk KEK file. Defaults to /etc/midnight/kek when
	// empty (or the value of MIDNIGHT_KEK_PATH if set in the environment
	// at construction time).
	Path string
	// AllowAnyMode disables the 0400 mode-check. Tests use this on
	// Windows where POSIX mode bits are not honored.
	AllowAnyMode bool
	// GraceTicks is the number of rotations a previous KEK is retained
	// for re-wrap. 1 by default.
	GraceTicks int64
}

// DefaultKEKPath resolves the default on-disk KEK path, honoring the
// MIDNIGHT_KEK_PATH override when present.
func DefaultKEKPath() string {
	if env := os.Getenv("MIDNIGHT_KEK_PATH"); env != "" {
		return env
	}
	return filepath.FromSlash("/etc/midnight/kek")
}

// LoadKEKMaterial reads the on-disk KEK material, validates the mode (on
// non-Windows hosts), and returns the bytes.
func LoadKEKMaterial(cfg FileKEKConfig) ([]byte, error) {
	path := cfg.Path
	if path == "" {
		path = DefaultKEKPath()
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrKEKNotFound, path)
		}
		return nil, fmt.Errorf("midnight: stat KEK file %s: %w", path, err)
	}
	if !cfg.AllowAnyMode {
		mode := info.Mode().Perm()
		// On Windows file modes are emulated; skip the strict check
		// when the OS does not advertise POSIX bits we recognize.
		if mode != 0 && mode != 0o400 {
			return nil, fmt.Errorf("%w: %s mode=%o", ErrKEKWrongMode, path, mode)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("midnight: read KEK file %s: %w", path, err)
	}
	if len(data) != kekLength {
		return nil, fmt.Errorf("%w: %s len=%d want=%d", ErrKEKLength, path, len(data), kekLength)
	}
	return data, nil
}

// NewFileKeyProvider loads the on-disk KEK material and returns a
// MemoryKeyProvider initialised with it. KEK rotation produces in-memory
// generations only; the on-disk file is never updated by this package
// directly — operators run a separate rotation runbook (panel: line 118).
func NewFileKeyProvider(cfg FileKEKConfig) (*MemoryKeyProvider, error) {
	material, err := LoadKEKMaterial(cfg)
	if err != nil {
		return nil, err
	}
	grace := cfg.GraceTicks
	if grace == 0 {
		grace = 1
	}
	return NewMemoryKeyProvider(material, grace)
}
