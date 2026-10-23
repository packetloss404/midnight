package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	slashpath "path"
	"path/filepath"
	"sort"
	"strings"
)

const EvidenceIndexSchemaVersion = "midnight.dev/build-registry-evidence-index/v1alpha1"

type EvidenceIndexEntryKind string

const (
	EvidenceIndexEntryReleaseEvidence EvidenceIndexEntryKind = "build-release-evidence"
	EvidenceIndexEntryGCManifest      EvidenceIndexEntryKind = "registry-gc-manifest"
	EvidenceIndexEntryProvenance      EvidenceIndexEntryKind = "provenance"
	EvidenceIndexEntrySBOM            EvidenceIndexEntryKind = "sbom"
)

type EvidenceIndexInput struct {
	Entries []EvidenceIndexEntry
}

// EvidenceIndex records the local evidence files and artifact refs that support
// a build/registry promotion decision. Paths are repository-relative safe paths.
type EvidenceIndex struct {
	SchemaVersion string               `json:"schemaVersion"`
	Status        ReviewStatus         `json:"status"`
	Entries       []EvidenceIndexEntry `json:"entries"`
}

type EvidenceIndexEntry struct {
	Kind        EvidenceIndexEntryKind `json:"kind"`
	Path        string                 `json:"path"`
	Status      ReviewStatus           `json:"status"`
	SHA256      string                 `json:"sha256,omitempty"`
	ArtifactRef *ArtifactRef           `json:"artifactRef,omitempty"`
}

// NewEvidenceIndex normalizes and sorts evidence entries, then derives a status
// rollup using blocked > needs-review > clean.
func NewEvidenceIndex(input EvidenceIndexInput) (EvidenceIndex, error) {
	entries := make([]EvidenceIndexEntry, 0, len(input.Entries))
	for _, entry := range input.Entries {
		normalized, err := normalizeEvidenceIndexEntry(entry)
		if err != nil {
			return EvidenceIndex{}, err
		}
		entries = append(entries, normalized)
	}
	sortEvidenceIndexEntries(entries)

	index := EvidenceIndex{
		SchemaVersion: EvidenceIndexSchemaVersion,
		Status:        deriveEvidenceIndexStatus(entries),
		Entries:       entries,
	}
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

func (i EvidenceIndex) Validate() error {
	if i.SchemaVersion != EvidenceIndexSchemaVersion {
		return errors.New("evidence index schemaVersion is invalid")
	}
	if !validReviewStatus(i.Status) {
		return fmt.Errorf("evidence index status %q is invalid", i.Status)
	}
	if len(i.Entries) == 0 {
		return errors.New("evidence index entries are required")
	}
	if !evidenceIndexEntriesSorted(i.Entries) {
		return errors.New("evidence index entries must be sorted")
	}
	for _, entry := range i.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	if want := deriveEvidenceIndexStatus(i.Entries); i.Status != want {
		return fmt.Errorf("evidence index status %q does not match entries status %q", i.Status, want)
	}
	return nil
}

func (e EvidenceIndexEntry) Validate() error {
	if !validEvidenceIndexEntryKind(e.Kind) {
		return fmt.Errorf("evidence index entry kind %q is invalid", e.Kind)
	}
	if err := validateRelativeSafeEvidencePath(e.Path); err != nil {
		return err
	}
	if !validReviewStatus(e.Status) {
		return fmt.Errorf("evidence index entry status %q is invalid", e.Status)
	}
	if err := validateEvidenceSHA256(e.SHA256); err != nil {
		return err
	}
	if e.ArtifactRef != nil {
		if err := validateArtifactRef(string(e.Kind), *e.ArtifactRef); err != nil {
			return err
		}
	}
	return nil
}

func MarshalEvidenceIndexJSON(index EvidenceIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(index)
}

// WriteEvidenceIndexJSONFile validates path and index before writing
// deterministic evidence index JSON. It only checks the explicit parent path.
func WriteEvidenceIndexJSONFile(path string, index EvidenceIndex) error {
	if err := validateEvidenceIndexJSONPath(path, osPathExists); err != nil {
		return err
	}
	data, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadEvidenceIndexJSONFile validates path, decodes JSON, and validates the
// evidence index before returning it.
func ReadEvidenceIndexJSONFile(path string) (EvidenceIndex, error) {
	if err := validateEvidenceIndexJSONPath(path, osPathExists); err != nil {
		return EvidenceIndex{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return EvidenceIndex{}, err
	}
	return unmarshalEvidenceIndexJSON(data)
}

// VerifyEvidenceIndexFiles validates index metadata, then checks each referenced
// evidence file exists under baseDir. Entries with sha256 metadata are hashed and
// compared against that value. This is intentionally separate from Validate so
// default metadata-only validation does not touch the filesystem.
func VerifyEvidenceIndexFiles(baseDir string, index EvidenceIndex) error {
	if err := index.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(baseDir) == "" {
		return errors.New("evidence index verification base directory must not be empty")
	}

	cleanBaseDir := filepath.Clean(baseDir)
	info, err := os.Stat(cleanBaseDir)
	if err != nil {
		return fmt.Errorf("evidence index verification base directory %q: %w", baseDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("evidence index verification base directory %q must be a directory", baseDir)
	}

	for _, entry := range index.Entries {
		path := filepath.Join(cleanBaseDir, filepath.FromSlash(entry.Path))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("evidence index entry %q: %w", entry.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("evidence index entry %q must name a regular file", entry.Path)
		}
		if entry.SHA256 == "" {
			continue
		}

		got, err := ComputeEvidenceFileSHA256(path)
		if err != nil {
			return fmt.Errorf("evidence index entry %q: %w", entry.Path, err)
		}
		if got != entry.SHA256 {
			return fmt.Errorf("evidence index entry %q sha256 mismatch: got %q want %q", entry.Path, got, entry.SHA256)
		}
	}
	return nil
}

// ComputeEvidenceFileSHA256 returns the lowercase hex SHA256 for one explicit
// local evidence file path. It only reads the named file; callers provide temp
// or test evidence paths explicitly.
func ComputeEvidenceFileSHA256(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("evidence file path must not be empty")
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) {
		return "", fmt.Errorf("evidence file path %q must be an explicit file path", path)
	}

	file, err := os.Open(cleaned)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("evidence file path %q must name a regular file", path)
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func unmarshalEvidenceIndexJSON(data []byte) (EvidenceIndex, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var index EvidenceIndex
	if err := decoder.Decode(&index); err != nil {
		return EvidenceIndex{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("evidence index JSON must contain a single object")
		}
		return EvidenceIndex{}, err
	}
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

func validateEvidenceIndexJSONPath(path string, exists func(string) (bool, error)) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evidence index path must not be empty")
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || filepath.Ext(base) != ".json" {
		return fmt.Errorf("evidence index path %q must be an explicit .json file path", path)
	}
	parent := filepath.Dir(cleaned)
	ok, err := exists(parent)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("evidence index path parent %q does not exist", parent)
	}
	return nil
}

func normalizeEvidenceIndexEntry(entry EvidenceIndexEntry) (EvidenceIndexEntry, error) {
	path, err := normalizeRelativeSafeEvidencePath(entry.Path)
	if err != nil {
		return EvidenceIndexEntry{}, err
	}
	entry.Path = path
	entry.SHA256 = strings.ToLower(strings.TrimSpace(entry.SHA256))
	return entry, nil
}

func validateRelativeSafeEvidencePath(path string) error {
	normalized, err := normalizeRelativeSafeEvidencePath(path)
	if err != nil {
		return err
	}
	if normalized != path {
		return fmt.Errorf("evidence index path %q must be normalized", path)
	}
	return nil
}

func normalizeRelativeSafeEvidencePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("evidence index path must not be empty")
	}
	slashPath := strings.ReplaceAll(trimmed, "\\", "/")
	if strings.HasPrefix(slashPath, "/") || filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" || evidencePathHasWindowsDriveVolume(slashPath) {
		return "", fmt.Errorf("evidence index path %q must be relative", raw)
	}
	for _, part := range strings.Split(slashPath, "/") {
		if part == ".." {
			return "", fmt.Errorf("evidence index path %q must not escape its base", raw)
		}
	}
	cleaned := slashpath.Clean(slashPath)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("evidence index path %q must name a file", raw)
	}
	return cleaned, nil
}

func evidencePathHasWindowsDriveVolume(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func validateEvidenceSHA256(checksum string) error {
	if checksum == "" {
		return nil
	}
	if len(checksum) != sha256.Size*2 {
		return fmt.Errorf("evidence index entry sha256 %q must be 64 hex characters", checksum)
	}
	for _, r := range checksum {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("evidence index entry sha256 %q must be lowercase hex", checksum)
		}
	}
	return nil
}

func deriveEvidenceIndexStatus(entries []EvidenceIndexEntry) ReviewStatus {
	status := ReviewStatusClean
	for _, entry := range entries {
		if entry.Status == ReviewStatusBlocked {
			return ReviewStatusBlocked
		}
		if entry.Status == ReviewStatusNeedsReview {
			status = ReviewStatusNeedsReview
		}
	}
	return status
}

func validEvidenceIndexEntryKind(kind EvidenceIndexEntryKind) bool {
	switch kind {
	case EvidenceIndexEntryReleaseEvidence, EvidenceIndexEntryGCManifest, EvidenceIndexEntryProvenance, EvidenceIndexEntrySBOM:
		return true
	default:
		return false
	}
}

func sortEvidenceIndexEntries(entries []EvidenceIndexEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return evidenceIndexEntryLess(entries[i], entries[j])
	})
}

func evidenceIndexEntriesSorted(entries []EvidenceIndexEntry) bool {
	return sort.SliceIsSorted(entries, func(i, j int) bool {
		return evidenceIndexEntryLess(entries[i], entries[j])
	})
}

func evidenceIndexEntryLess(left, right EvidenceIndexEntry) bool {
	if left.Kind != right.Kind {
		return evidenceIndexEntryKindRank(left.Kind) < evidenceIndexEntryKindRank(right.Kind)
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.Status != right.Status {
		return left.Status < right.Status
	}
	if left.SHA256 != right.SHA256 {
		return left.SHA256 < right.SHA256
	}
	return evidenceIndexArtifactRefLess(left.ArtifactRef, right.ArtifactRef)
}

func evidenceIndexArtifactRefLess(left, right *ArtifactRef) bool {
	if left == nil || right == nil {
		return left == nil && right != nil
	}
	return artifactRefLess(*left, *right)
}

func evidenceIndexEntryKindRank(kind EvidenceIndexEntryKind) int {
	switch kind {
	case EvidenceIndexEntryReleaseEvidence:
		return 0
	case EvidenceIndexEntryGCManifest:
		return 1
	case EvidenceIndexEntryProvenance:
		return 2
	case EvidenceIndexEntrySBOM:
		return 3
	default:
		return 4
	}
}
