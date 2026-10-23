package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FilePinStore is a JSON-backed implementation of the deployment_image_pins
// reference-count table. It is the Phase 1 Lane 3 fallback that ships
// before Lane 1 lands the Postgres-backed migration `NNNN_image_pins.up.sql`.
//
// Behaviour notes:
//
//   - All mutations rewrite the entire JSON document via
//     temp-file-plus-rename; partial writes are impossible.
//   - The file is `0600` so the registry process can read it but no other
//     local user can. Operators run the registry under a dedicated UID.
//   - The schema is intentionally a flat array of PinRow values so the file
//     is easy to grep when triaging an "unexpected GC refusal" alert.
//   - A pin store is safe for use from multiple goroutines but NOT from
//     multiple processes. The embedded registry process is the only writer.
//
// TODO(lane1): when Lane 1 ships migrations/NNNN_image_pins.up.sql plus the
// Postgres-backed PinStore, replace FilePinStore with a thin shim that
// delegates to the DB-backed implementation. The on-disk file becomes a
// best-effort cache for offline operations.
type FilePinStore struct {
	path string
	mu   sync.Mutex
	mem  *PinStore
}

// FilePinDocument is the on-disk shape of FilePinStore. It is exported so
// auditors can vendor a fixture into a runbook without importing this
// package.
type FilePinDocument struct {
	Version int      `json:"version"`
	Rows    []PinRow `json:"rows"`
}

const filePinStoreSchemaVersion = 1

// NewFilePinStore opens (or creates) a JSON-backed pin store at path. The
// file is created `0600` if missing.
func NewFilePinStore(path string) (*FilePinStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("registry pin store path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("registry pin store path must be absolute")
	}
	store := &FilePinStore{path: path, mem: NewPinStore()}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create pin-store directory: %w", err)
	}
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store, nil
}

// Path returns the on-disk JSON path. Operators use it to back up the file
// alongside acme.json and the registry blob store.
func (s *FilePinStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FilePinStore) reload() error {
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.persistLocked()
		}
		return fmt.Errorf("open pin store: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var doc FilePinDocument
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode pin store: %w", err)
	}
	if doc.Version != 0 && doc.Version != filePinStoreSchemaVersion {
		return fmt.Errorf("pin store schema version %d not supported", doc.Version)
	}
	mem := NewPinStore()
	for _, row := range doc.Rows {
		if err := mem.Insert(row); err != nil {
			return fmt.Errorf("hydrate pin store: %w", err)
		}
	}
	s.mem = mem
	return nil
}

// Insert adds a pin row and persists the document. Inserting a duplicate
// (digest, service, deployment, reason) tuple returns an error.
func (s *FilePinStore) Insert(row PinRow) error {
	if s == nil {
		return errors.New("file pin store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.Insert(row); err != nil {
		return err
	}
	if err := s.persistLocked(); err != nil {
		// best effort rollback so the in-memory state matches the file
		_ = s.mem.Decrement(row.Digest, row.ServiceID, row.DeploymentID, row.Reason)
		return err
	}
	return nil
}

// Decrement removes a pin row matching the supplied primary key.
func (s *FilePinStore) Decrement(digest, serviceID, deploymentID string, reason PinReason) error {
	if s == nil {
		return errors.New("file pin store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.Decrement(digest, serviceID, deploymentID, reason); err != nil {
		return err
	}
	return s.persistLocked()
}

// PinCount returns the number of currently-effective pins for the supplied
// digest. Rows whose pinnedUntil has elapsed are not counted.
func (s *FilePinStore) PinCount(digest string, now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.PinCount(digest, now)
}

// ReasonsFor returns the deduplicated reason set protecting the supplied digest.
func (s *FilePinStore) ReasonsFor(digest string, now time.Time) []PinReason {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.ReasonsFor(digest, now)
}

// ProbeProtectedDigest returns a probe result describing whether the
// requested digest is protected by any active pin.
func (s *FilePinStore) ProbeProtectedDigest(repository, digest string, now time.Time) ProtectedDigestProbeResult {
	if s == nil {
		return ProtectedDigestProbeResult{Repository: repository, Digest: digest, ProbedAt: now}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.ProbeProtectedDigest(repository, digest, now)
}

// RefuseGCIfReferenced returns an error when the supplied digest is
// referenced by any active pin row.
func (s *FilePinStore) RefuseGCIfReferenced(digest string, now time.Time) error {
	if s == nil {
		return errors.New("file pin store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.RefuseGCIfReferenced(digest, now)
}

// Rows returns a sorted snapshot of the persisted rows.
func (s *FilePinStore) Rows() []PinRow {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.mem.Rows()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Repository != rows[j].Repository {
			return rows[i].Repository < rows[j].Repository
		}
		if rows[i].Digest != rows[j].Digest {
			return rows[i].Digest < rows[j].Digest
		}
		if rows[i].ServiceID != rows[j].ServiceID {
			return rows[i].ServiceID < rows[j].ServiceID
		}
		if rows[i].DeploymentID != rows[j].DeploymentID {
			return rows[i].DeploymentID < rows[j].DeploymentID
		}
		return rows[i].Reason < rows[j].Reason
	})
	return rows
}

func (s *FilePinStore) persistLocked() error {
	rows := s.mem.Rows()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Repository != rows[j].Repository {
			return rows[i].Repository < rows[j].Repository
		}
		if rows[i].Digest != rows[j].Digest {
			return rows[i].Digest < rows[j].Digest
		}
		if rows[i].ServiceID != rows[j].ServiceID {
			return rows[i].ServiceID < rows[j].ServiceID
		}
		if rows[i].DeploymentID != rows[j].DeploymentID {
			return rows[i].DeploymentID < rows[j].DeploymentID
		}
		return rows[i].Reason < rows[j].Reason
	})
	doc := FilePinDocument{Version: filePinStoreSchemaVersion, Rows: rows}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pin store: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create pin store temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write pin store temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod pin store temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync pin store temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close pin store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("rename pin store temp file: %w", err)
	}
	return nil
}
