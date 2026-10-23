package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EvidenceIndexSchemaVersion is the stable schema for the install/preflight evidence index.
const EvidenceIndexSchemaVersion = "midnight.dev/install-preflight-evidence-index/v1alpha1"

// EvidenceIndexStatus is the rollup status for an install/preflight evidence index.
type EvidenceIndexStatus string

const (
	// EvidenceIndexStatusReady indicates every reference is ready.
	EvidenceIndexStatusReady EvidenceIndexStatus = "ready"
	// EvidenceIndexStatusNeedsReview indicates at least one reference needs review.
	EvidenceIndexStatusNeedsReview EvidenceIndexStatus = "needs-review"
	// EvidenceIndexStatusBlocked indicates at least one reference is blocked.
	EvidenceIndexStatusBlocked EvidenceIndexStatus = "blocked"
)

// EvidenceIndexReferenceKind identifies the document type referenced by an entry.
type EvidenceIndexReferenceKind string

const (
	EvidenceReferenceInstallManifest          EvidenceIndexReferenceKind = "install-manifest"
	EvidenceReferenceInstallReadinessSummary  EvidenceIndexReferenceKind = "install-readiness-summary"
	EvidenceReferencePreflightReleaseEvidence EvidenceIndexReferenceKind = "preflight-release-evidence"
	EvidenceReferenceCommandEvidenceBatch     EvidenceIndexReferenceKind = "command-evidence-batch"
	EvidenceReferenceRemediationPlan          EvidenceIndexReferenceKind = "remediation-plan"
)

// EvidenceIndexReference is one entry in the install/preflight evidence index.
//
// SHA256 is the lowercase hex digest of the referenced artifact bytes. VerifiedAt
// is the UTC RFC 3339 timestamp at which the verifier last confirmed the digest
// matched the on-disk file. Entries without VerifiedAt are treated as
// "not yet verified"; the verifier updates the field on every successful check.
type EvidenceIndexReference struct {
	Kind       EvidenceIndexReferenceKind `json:"kind"`
	Path       string                     `json:"path"`
	Status     EvidenceIndexStatus        `json:"status"`
	SHA256     string                     `json:"sha256,omitempty"`
	VerifiedAt string                     `json:"verified_at,omitempty"`
}

// EvidenceIndex is the deterministic install/preflight evidence index payload.
type EvidenceIndex struct {
	SchemaVersion string                   `json:"schema_version"`
	Status        EvidenceIndexStatus      `json:"status"`
	References    []EvidenceIndexReference `json:"references"`
}

// EvidenceIndexInput holds raw entries to compose into a sorted index.
type EvidenceIndexInput struct {
	References []EvidenceIndexReference
}

// NewEvidenceIndex normalizes references, sorts them deterministically, and
// derives the rollup status using blocked > needs-review > ready.
func NewEvidenceIndex(input EvidenceIndexInput) (EvidenceIndex, error) {
	if len(input.References) == 0 {
		return EvidenceIndex{}, errors.New("install evidence index requires at least one reference")
	}
	references := make([]EvidenceIndexReference, 0, len(input.References))
	seen := make(map[string]struct{}, len(input.References))
	for _, ref := range input.References {
		normalized, err := normalizeEvidenceIndexReference(ref)
		if err != nil {
			return EvidenceIndex{}, err
		}
		key := string(normalized.Kind) + "|" + normalized.Path
		if _, dup := seen[key]; dup {
			return EvidenceIndex{}, fmt.Errorf("duplicate install evidence reference %s", key)
		}
		seen[key] = struct{}{}
		references = append(references, normalized)
	}
	sortEvidenceIndexReferences(references)
	index := EvidenceIndex{
		SchemaVersion: EvidenceIndexSchemaVersion,
		References:    references,
		Status:        deriveEvidenceIndexStatus(references),
	}
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

// Validate enforces the install evidence index invariants.
func (i EvidenceIndex) Validate() error {
	if i.SchemaVersion != EvidenceIndexSchemaVersion {
		return fmt.Errorf("install evidence index schema_version %q is invalid", i.SchemaVersion)
	}
	if !validEvidenceIndexStatus(i.Status) {
		return fmt.Errorf("install evidence index status %q is invalid", i.Status)
	}
	if len(i.References) == 0 {
		return errors.New("install evidence index references are required")
	}
	for index, ref := range i.References {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("install evidence index references[%d]: %w", index, err)
		}
	}
	if !sort.SliceIsSorted(i.References, func(left, right int) bool {
		return evidenceIndexReferenceLess(i.References[left], i.References[right])
	}) {
		return errors.New("install evidence index references must be sorted")
	}
	if got := deriveEvidenceIndexStatus(i.References); got != i.Status {
		return fmt.Errorf("install evidence index status %q does not match references status %q", i.Status, got)
	}
	return nil
}

// Validate enforces invariants on a single reference.
func (r EvidenceIndexReference) Validate() error {
	if !validEvidenceIndexReferenceKind(r.Kind) {
		return fmt.Errorf("kind %q is invalid", r.Kind)
	}
	if err := validateEvidenceIndexRelativePath(r.Path); err != nil {
		return err
	}
	if !validEvidenceIndexStatus(r.Status) {
		return fmt.Errorf("status %q is invalid", r.Status)
	}
	if r.SHA256 != "" {
		if err := validateSHA256Hex(r.SHA256); err != nil {
			return err
		}
	}
	if r.VerifiedAt != "" {
		if _, err := time.Parse(time.RFC3339, r.VerifiedAt); err != nil {
			return fmt.Errorf("verified_at %q must be RFC3339: %w", r.VerifiedAt, err)
		}
	}
	return nil
}

// MarshalEvidenceIndexJSON validates and emits stable JSON for callers.
func MarshalEvidenceIndexJSON(index EvidenceIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(index, "", "  ")
}

// WriteEvidenceIndexJSONFile validates and writes the index to an explicit .json path.
func WriteEvidenceIndexJSONFile(path string, index EvidenceIndex) error {
	if err := validateEvidenceIndexJSONPath(path); err != nil {
		return err
	}
	data, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// ReadEvidenceIndexJSONFile reads and strictly decodes one evidence index document.
func ReadEvidenceIndexJSONFile(path string) (EvidenceIndex, error) {
	if err := validateEvidenceIndexJSONPath(path); err != nil {
		return EvidenceIndex{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return EvidenceIndex{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var index EvidenceIndex
	if err := decoder.Decode(&index); err != nil {
		return EvidenceIndex{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("install evidence index JSON must contain one document")
		}
		return EvidenceIndex{}, err
	}
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

// VerifyEvidenceIndexFiles checks each reference exists under baseDir and, for
// references with sha256 metadata, that the file digest matches. It does not
// mutate the index; callers needing updated VerifiedAt timestamps should use
// VerifyAndStampEvidenceIndex instead.
func VerifyEvidenceIndexFiles(baseDir string, index EvidenceIndex) error {
	if err := index.Validate(); err != nil {
		return err
	}
	cleanBase, err := evidenceBaseDir(baseDir)
	if err != nil {
		return err
	}
	for _, ref := range index.References {
		if _, err := verifyEvidenceReferenceFile(cleanBase, ref); err != nil {
			return err
		}
	}
	return nil
}

// VerifyAndStampEvidenceIndex verifies every reference and returns an updated
// index whose VerifiedAt fields reflect the supplied verification timestamp.
// References without an SHA256 still have their VerifiedAt stamped if the file
// is present and regular; missing or mismatched files cause the function to
// return an error and leave the input index untouched.
//
// at must be a non-zero time.Time; the helper formats it with RFC3339Nano so
// successive verifications are stable and round-trip cleanly through JSON.
func VerifyAndStampEvidenceIndex(baseDir string, index EvidenceIndex, at time.Time) (EvidenceIndex, error) {
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	if at.IsZero() {
		return EvidenceIndex{}, errors.New("verification timestamp must be non-zero")
	}
	cleanBase, err := evidenceBaseDir(baseDir)
	if err != nil {
		return EvidenceIndex{}, err
	}
	stamped := EvidenceIndex{
		SchemaVersion: index.SchemaVersion,
		Status:        index.Status,
		References:    make([]EvidenceIndexReference, 0, len(index.References)),
	}
	verifiedAt := at.UTC().Format(time.RFC3339Nano)
	for _, ref := range index.References {
		if _, err := verifyEvidenceReferenceFile(cleanBase, ref); err != nil {
			return EvidenceIndex{}, err
		}
		ref.VerifiedAt = verifiedAt
		stamped.References = append(stamped.References, ref)
	}
	if err := stamped.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return stamped, nil
}

func evidenceBaseDir(baseDir string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", errors.New("baseDir is required")
	}
	cleanBase, err := absoluteCleanPath(baseDir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(cleanBase)
	if err != nil {
		return "", fmt.Errorf("baseDir %q: %w", baseDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("baseDir %q must be a directory", baseDir)
	}
	return cleanBase, nil
}

func verifyEvidenceReferenceFile(cleanBase string, ref EvidenceIndexReference) (string, error) {
	path := filepath.Join(cleanBase, filepath.FromSlash(ref.Path))
	fileInfo, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("reference %q: %w", ref.Path, err)
	}
	if !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("reference %q must be a regular file", ref.Path)
	}
	if ref.SHA256 == "" {
		return "", nil
	}
	got, err := computeEvidenceFileSHA256(path)
	if err != nil {
		return "", fmt.Errorf("reference %q: %w", ref.Path, err)
	}
	if got != ref.SHA256 {
		return "", fmt.Errorf("reference %q sha256 mismatch: got %q want %q", ref.Path, got, ref.SHA256)
	}
	return got, nil
}

func computeEvidenceFileSHA256(path string) (string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func deriveEvidenceIndexStatus(references []EvidenceIndexReference) EvidenceIndexStatus {
	status := EvidenceIndexStatusReady
	for _, ref := range references {
		if ref.Status == EvidenceIndexStatusBlocked {
			return EvidenceIndexStatusBlocked
		}
		if ref.Status == EvidenceIndexStatusNeedsReview {
			status = EvidenceIndexStatusNeedsReview
		}
	}
	return status
}

func sortEvidenceIndexReferences(references []EvidenceIndexReference) {
	sort.SliceStable(references, func(left, right int) bool {
		return evidenceIndexReferenceLess(references[left], references[right])
	})
}

func evidenceIndexReferenceLess(left, right EvidenceIndexReference) bool {
	if left.Kind != right.Kind {
		return evidenceIndexReferenceKindRank(left.Kind) < evidenceIndexReferenceKindRank(right.Kind)
	}
	return left.Path < right.Path
}

func evidenceIndexReferenceKindRank(kind EvidenceIndexReferenceKind) int {
	switch kind {
	case EvidenceReferenceInstallManifest:
		return 0
	case EvidenceReferenceInstallReadinessSummary:
		return 1
	case EvidenceReferencePreflightReleaseEvidence:
		return 2
	case EvidenceReferenceCommandEvidenceBatch:
		return 3
	case EvidenceReferenceRemediationPlan:
		return 4
	default:
		return 5
	}
}

func validEvidenceIndexReferenceKind(kind EvidenceIndexReferenceKind) bool {
	switch kind {
	case EvidenceReferenceInstallManifest,
		EvidenceReferenceInstallReadinessSummary,
		EvidenceReferencePreflightReleaseEvidence,
		EvidenceReferenceCommandEvidenceBatch,
		EvidenceReferenceRemediationPlan:
		return true
	default:
		return false
	}
}

func validEvidenceIndexStatus(status EvidenceIndexStatus) bool {
	switch status {
	case EvidenceIndexStatusReady, EvidenceIndexStatusNeedsReview, EvidenceIndexStatusBlocked:
		return true
	default:
		return false
	}
}

func validateEvidenceIndexRelativePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path must not be empty")
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return fmt.Errorf("path %q must be relative", path)
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("path %q must use forward slashes", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("path %q must not escape its base", path)
		}
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned != path {
		return fmt.Errorf("path %q must be normalized", path)
	}
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return fmt.Errorf("path %q must name a file", path)
	}
	return nil
}

func validateEvidenceIndexJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evidence index JSON path is required")
	}
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("evidence index path %q must end with .json", path)
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("evidence index parent directory %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("evidence index parent path %q must be a directory", parent)
	}
	return nil
}

func normalizeEvidenceIndexReference(ref EvidenceIndexReference) (EvidenceIndexReference, error) {
	ref.Path = strings.TrimSpace(ref.Path)
	ref.SHA256 = strings.ToLower(strings.TrimSpace(ref.SHA256))
	if err := ref.Validate(); err != nil {
		return EvidenceIndexReference{}, err
	}
	return ref, nil
}
