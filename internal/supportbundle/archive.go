package supportbundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// ArchiveFileMode is the stable file-mode bit pattern used for every regular file in a support bundle archive.
//
// Read-only ownership keeps redactor outputs immutable and avoids leaking host umask state into shipped bundles.
const ArchiveFileMode = 0o400

// ArchiveDirMode is the stable file-mode bit pattern used for any synthetic directory entry.
const ArchiveDirMode = 0o500

// ManifestArchivePath is the canonical path the embedded manifest.json is written to inside the archive.
const ManifestArchivePath = "manifest.json"

// MaxArchiveEntryBytes caps the per-entry size accepted on the read path; anything
// larger trips a `SUPPORT_BUNDLE_VERIFY_FAILED` and aborts. Sized so legitimate
// support bundles fit (largest known entry types are SQL dumps + journald excerpts)
// while a tar bomb stops well before exhausting host memory.
const MaxArchiveEntryBytes int64 = 256 * 1024 * 1024 // 256 MiB

// MaxArchiveTotalBytes caps the cumulative read-side bytes consumed by every
// entry combined; trips a `SUPPORT_BUNDLE_VERIFY_FAILED` and aborts.
const MaxArchiveTotalBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB

// MaxArchiveEntryCount caps the number of tar entries the verifier will read.
const MaxArchiveEntryCount = 4096

// ArchiveEntry is one logical file written into the archive: deterministic path, file kind, and raw bytes.
type ArchiveEntry struct {
	Path  string
	Kind  string
	Bytes []byte
}

// WriteDeterministicArchive writes a tar archive into w with deterministic ordering, stable file modes,
// and an embedded `manifest.json` listing every entry plus its SHA-256.
//
// All entries use the same fixed mtime, owner, and mode bits so two consecutive bundles over a quiesced
// data dir produce byte-identical archive bytes. The manifest is written first so partial archives can
// still be inspected. Returned manifest reflects what was written, including content hashes.
func WriteDeterministicArchive(w io.Writer, id string, createdAt time.Time, selector Selector, entries []ArchiveEntry) (Manifest, error) {
	if w == nil {
		return Manifest{}, errors.New("archive writer is required")
	}
	if strings.TrimSpace(id) == "" {
		return Manifest{}, errors.New("archive id is required")
	}
	if createdAt.IsZero() {
		return Manifest{}, errors.New("archive createdAt is required")
	}
	if err := selector.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("selector: %w", err)
	}

	sorted := append([]ArchiveEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	manifest := Manifest{ID: id, CreatedAt: createdAt, Selector: selector, Files: make([]ManifestFile, 0, len(sorted))}
	for i, entry := range sorted {
		if err := validateArchiveEntry(entry); err != nil {
			return Manifest{}, fmt.Errorf("entries[%d]: %w", i, err)
		}
		if class := ClassifyHardBlockedPath(entry.Path); class != BlockedPathNone {
			return Manifest{}, fmt.Errorf("entries[%d]: hard-blocked %s path", i, class)
		}
		sum := sha256.Sum256(entry.Bytes)
		manifest.Files = append(manifest.Files, ManifestFile{
			Path:   entry.Path,
			Kind:   entry.Kind,
			Bytes:  int64(len(entry.Bytes)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}

	manifestJSON, err := MarshalManifestJSON(manifest)
	if err != nil {
		return Manifest{}, err
	}

	tw := tar.NewWriter(w)
	mtime := createdAt.UTC()
	if err := writeTarFile(tw, ManifestArchivePath, manifestJSON, mtime); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	for _, entry := range sorted {
		if err := writeTarFile(tw, entry.Path, entry.Bytes, mtime); err != nil {
			return Manifest{}, fmt.Errorf("entry %q: %w", entry.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ReadArchiveManifest reads back the embedded manifest.json from an archive produced by WriteDeterministicArchive.
//
// It is the inverse of WriteDeterministicArchive's manifest write step and is the canonical way to confirm
// archive contents without re-running redaction. Entry headers and sizes are
// validated against the panel-locked archive caps so a hostile archive cannot
// OOM the verifier before the manifest is parsed.
func ReadArchiveManifest(r io.Reader) (Manifest, error) {
	if r == nil {
		return Manifest{}, errors.New("archive reader is required")
	}
	tr := tar.NewReader(r)
	entryCount := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return Manifest{}, errors.New("archive is missing manifest.json")
		}
		if err != nil {
			return Manifest{}, err
		}
		entryCount++
		if entryCount > MaxArchiveEntryCount {
			return Manifest{}, fmt.Errorf("archive exceeds entry-count cap of %d", MaxArchiveEntryCount)
		}
		if err := validateReadHeader(header); err != nil {
			return Manifest{}, err
		}
		if header.Name != ManifestArchivePath {
			continue
		}
		buf, err := readCappedEntry(tr)
		if err != nil {
			return Manifest{}, err
		}
		var manifest Manifest
		if err := decodeStrictManifestJSON(buf, &manifest); err != nil {
			return Manifest{}, err
		}
		return manifest, manifest.Validate()
	}
}

// VerifyArchive recomputes SHA-256 over every embedded archive entry and returns a stable
// SUPPORT_BUNDLE_VERIFY_FAILED failure error when one or more entries diverge from the
// embedded manifest. Returns nil only when every entry matches its recorded SHA-256.
//
// VerifyArchive is the streaming-friendly entry point intended for callers that hold the
// entire archive in memory. Callers needing the parsed manifest should use
// VerifyArchiveContents directly.
func VerifyArchive(archive []byte) error {
	if len(archive) == 0 {
		return failurecodes.New(failurecodes.SupportBundleVerifyFailed, "support bundle archive is empty")
	}
	manifest, mismatches, err := VerifyArchiveContents(bytes.NewReader(archive))
	if err != nil {
		return failurecodes.Wrap(failurecodes.SupportBundleVerifyFailed, "support bundle archive could not be parsed", err)
	}
	if len(mismatches) == 0 {
		return nil
	}
	sort.Strings(mismatches)
	return failurecodes.New(failurecodes.SupportBundleVerifyFailed, fmt.Sprintf("support bundle archive %q has %d entry mismatch(es): %s", manifest.ID, len(mismatches), strings.Join(mismatches, ", ")))
}

// VerifyArchiveContents reads every entry of the archive and recomputes SHA-256, returning the
// list of paths whose contents do not match the embedded manifest. A nil mismatches slice means
// the archive's bytes are consistent with its manifest.
//
// The verifier enforces three panel-locked caps against tar-bomb / path-traversal
// inputs: per-entry size (MaxArchiveEntryBytes), cumulative bytes
// (MaxArchiveTotalBytes), and entry count (MaxArchiveEntryCount). Header names
// are validated for traversal and absolute paths before any bytes are read.
// Duplicate entry names are rejected.
func VerifyArchiveContents(r io.Reader) (Manifest, []string, error) {
	if r == nil {
		return Manifest{}, nil, errors.New("archive reader is required")
	}
	tr := tar.NewReader(r)
	var manifest Manifest
	manifestSeen := false
	contentHashes := make(map[string]string)
	entryCount := 0
	totalBytes := int64(0)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, nil, err
		}
		entryCount++
		if entryCount > MaxArchiveEntryCount {
			return Manifest{}, nil, fmt.Errorf("archive exceeds entry-count cap of %d", MaxArchiveEntryCount)
		}
		if err := validateReadHeader(header); err != nil {
			return Manifest{}, nil, err
		}
		if _, exists := contentHashes[header.Name]; exists && header.Name != ManifestArchivePath {
			return Manifest{}, nil, fmt.Errorf("archive contains duplicate entry %q", header.Name)
		}
		buf, err := readCappedEntry(tr)
		if err != nil {
			return Manifest{}, nil, err
		}
		totalBytes += int64(len(buf))
		if totalBytes > MaxArchiveTotalBytes {
			return Manifest{}, nil, fmt.Errorf("archive exceeds total-byte cap of %d", MaxArchiveTotalBytes)
		}
		if header.Name == ManifestArchivePath {
			if err := decodeStrictManifestJSON(buf, &manifest); err != nil {
				return Manifest{}, nil, err
			}
			manifestSeen = true
			continue
		}
		sum := sha256.Sum256(buf)
		contentHashes[header.Name] = hex.EncodeToString(sum[:])
	}
	if !manifestSeen {
		return Manifest{}, nil, errors.New("archive is missing manifest.json")
	}
	if err := manifest.Validate(); err != nil {
		return manifest, nil, err
	}
	manifestPaths := make(map[string]struct{}, len(manifest.Files))
	var mismatches []string
	for _, file := range manifest.Files {
		manifestPaths[file.Path] = struct{}{}
		got, ok := contentHashes[file.Path]
		if !ok {
			mismatches = append(mismatches, file.Path)
			continue
		}
		if got != file.SHA256 {
			mismatches = append(mismatches, file.Path)
		}
	}
	// Surface entries that exist in the archive but are NOT listed in the
	// manifest. Without this check an attacker could append unmanifested
	// files past a verified manifest and have them pass verification.
	for path := range contentHashes {
		if _, ok := manifestPaths[path]; !ok {
			mismatches = append(mismatches, path)
		}
	}
	sort.Strings(mismatches)
	return manifest, mismatches, nil
}

// validateReadHeader rejects tar headers whose name is unsafe (absolute,
// traversal, OS-specific separators, or a hard-blocked path). It runs before
// any bytes of the entry are consumed.
func validateReadHeader(header *tar.Header) error {
	if header == nil {
		return errors.New("archive entry header is nil")
	}
	name := header.Name
	if strings.TrimSpace(name) == "" {
		return errors.New("archive entry has empty name")
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("archive entry %q uses backslash", name)
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("archive entry %q is absolute", name)
	}
	cleaned := filepath.ToSlash(filepath.Clean(name))
	if cleaned != name {
		return fmt.Errorf("archive entry %q is not a clean forward-slash relative path", name)
	}
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("archive entry %q escapes the bundle root", name)
	}
	if class := ClassifyHardBlockedPath(name); class != BlockedPathNone {
		return fmt.Errorf("archive entry %q is a hard-blocked %s path", name, class)
	}
	if header.Size < 0 || header.Size > MaxArchiveEntryBytes {
		return fmt.Errorf("archive entry %q size %d exceeds per-entry cap of %d", name, header.Size, MaxArchiveEntryBytes)
	}
	return nil
}

// readCappedEntry copies up to MaxArchiveEntryBytes from tr into a buffer; if
// the entry exceeds the cap (e.g., a header lied about its size or a sparse
// extension expands), it returns an error so the verifier aborts before
// allocating unbounded memory.
func readCappedEntry(tr io.Reader) ([]byte, error) {
	limited := io.LimitReader(tr, MaxArchiveEntryBytes+1)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, limited); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > MaxArchiveEntryBytes {
		return nil, fmt.Errorf("archive entry exceeds per-entry cap of %d bytes", MaxArchiveEntryBytes)
	}
	return buf.Bytes(), nil
}

func validateArchiveEntry(entry ArchiveEntry) error {
	if strings.TrimSpace(entry.Path) == "" {
		return errors.New("path is required")
	}
	if filepath.IsAbs(entry.Path) || strings.HasPrefix(filepath.Clean(entry.Path), "..") {
		return errors.New("path must be relative to the bundle root")
	}
	if filepath.ToSlash(filepath.Clean(entry.Path)) != entry.Path {
		return errors.New("path must be a clean forward-slash relative path")
	}
	if entry.Path == ManifestArchivePath {
		return errors.New("entry path collides with reserved manifest.json")
	}
	if strings.TrimSpace(entry.Kind) == "" {
		return errors.New("kind is required")
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mtime time.Time) error {
	header := &tar.Header{
		Name:     name,
		Mode:     int64(ArchiveFileMode),
		Size:     int64(len(data)),
		ModTime:  mtime,
		Uid:      0,
		Gid:      0,
		Uname:    "",
		Gname:    "",
		Format:   tar.FormatPAX,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return nil
}

func decodeStrictManifestJSON(data []byte, manifest *Manifest) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(manifest); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("manifest.json must contain a single JSON document")
		}
		return err
	}
	return nil
}
