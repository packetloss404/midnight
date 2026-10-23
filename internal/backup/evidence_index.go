package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/supportbundle"
)

const (
	EvidenceIndexStatusReady       = "ready"
	EvidenceIndexStatusNeedsReview = "needs-review"
	EvidenceIndexStatusBlocked     = "blocked"

	EvidenceReferenceSupportBundleManifest  = "support-bundle-manifest"
	EvidenceReferenceBackupExportManifest   = "backup-export-manifest"
	EvidenceReferenceBackupReleaseEvidence  = "backup-release-evidence"
	EvidenceReferenceAlertPackagingEvidence = "alert-packaging-evidence"
)

// EvidenceIndexInput combines already-built Phase 0 evidence with publish paths.
type EvidenceIndexInput struct {
	SupportBundleManifestPath  string
	SupportBundleManifest      supportbundle.Manifest
	BackupExportManifestPath   string
	BackupExportManifest       ExportManifest
	BackupReleaseEvidencePath  string
	BackupReleaseEvidence      ReleaseEvidencePacket
	AlertPackagingEvidencePath string
	AlertPackagingEvidence     observability.AlertPackagingEvidence
}

// EvidenceIndexReference points at one evidence document and its review status.
type EvidenceIndexReference struct {
	Name                   string `json:"name"`
	Path                   string `json:"path"`
	ID                     string `json:"id,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
	Status                 string `json:"status"`
	OperatorReviewRequired bool   `json:"operatorReviewRequired"`
}

// EvidenceIndex is the deterministic support/backup evidence rollup.
type EvidenceIndex struct {
	Status                 string                   `json:"status"`
	OperatorReviewRequired bool                     `json:"operatorReviewRequired"`
	OperatorReviewDecision OperatorReviewDecision   `json:"operatorReviewDecision"`
	References             []EvidenceIndexReference `json:"references"`
}

// BuildEvidenceIndex returns a deterministic evidence index without filesystem I/O.
func BuildEvidenceIndex(input EvidenceIndexInput) (EvidenceIndex, error) {
	if err := input.SupportBundleManifest.Validate(); err != nil {
		return EvidenceIndex{}, fmt.Errorf("support bundle manifest: %w", err)
	}
	if err := input.BackupExportManifest.Validate(); err != nil {
		return EvidenceIndex{}, fmt.Errorf("backup export manifest: %w", err)
	}
	if err := input.BackupReleaseEvidence.Validate(); err != nil {
		return EvidenceIndex{}, fmt.Errorf("backup release evidence: %w", err)
	}
	if err := input.AlertPackagingEvidence.Validate(); err != nil {
		return EvidenceIndex{}, fmt.Errorf("alert packaging evidence: %w", err)
	}

	references := []EvidenceIndexReference{
		{Name: EvidenceReferenceSupportBundleManifest, Path: input.SupportBundleManifestPath, ID: input.SupportBundleManifest.ID, Status: EvidenceIndexStatusReady},
		{Name: EvidenceReferenceBackupExportManifest, Path: input.BackupExportManifestPath, ID: input.BackupExportManifest.ID, Status: backupExportManifestEvidenceStatus(input.BackupExportManifest)},
		{Name: EvidenceReferenceBackupReleaseEvidence, Path: input.BackupReleaseEvidencePath, Status: input.BackupReleaseEvidence.Status, OperatorReviewRequired: input.BackupReleaseEvidence.OperatorReviewRequired},
		{Name: EvidenceReferenceAlertPackagingEvidence, Path: input.AlertPackagingEvidencePath, Status: alertPackagingEvidenceIndexStatus(input.AlertPackagingEvidence)},
	}
	for i := range references {
		references[i].OperatorReviewRequired = references[i].OperatorReviewRequired || references[i].Status != EvidenceIndexStatusReady
	}

	index := EvidenceIndex{OperatorReviewDecision: OperatorReviewDecision{Decision: OperatorReviewDecisionPending}, References: references}
	index.Status = evidenceIndexStatus(references)
	index.OperatorReviewRequired = index.Status != EvidenceIndexStatusReady
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

// MarshalEvidenceIndexJSON validates and renders stable evidence index JSON.
func MarshalEvidenceIndexJSON(index EvidenceIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(index, "", "  ")
}

// WriteEvidenceIndexJSON validates and writes a stable evidence index JSON file.
func WriteEvidenceIndexJSON(path string, index EvidenceIndex) error {
	if err := validateEvidenceIndexJSONPath(path); err != nil {
		return err
	}
	data, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadEvidenceIndexJSON reads and validates one strict evidence index JSON document.
func ReadEvidenceIndexJSON(path string) (EvidenceIndex, error) {
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
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return EvidenceIndex{}, errors.New("evidence index JSON must contain one document")
	}
	if err := index.Validate(); err != nil {
		return EvidenceIndex{}, err
	}
	return index, nil
}

// Validate checks path safety, deterministic references, and status rollup.
func (i EvidenceIndex) Validate() error {
	if !validEvidenceIndexStatus(i.Status) {
		return fmt.Errorf("evidence index status %q is invalid", i.Status)
	}
	wantNames := []string{
		EvidenceReferenceSupportBundleManifest,
		EvidenceReferenceBackupExportManifest,
		EvidenceReferenceBackupReleaseEvidence,
		EvidenceReferenceAlertPackagingEvidence,
	}
	if len(i.References) != len(wantNames) {
		return errors.New("evidence index must include four deterministic references")
	}
	for n, reference := range i.References {
		if reference.Name != wantNames[n] {
			return fmt.Errorf("references[%d]: unexpected evidence reference %q", n, reference.Name)
		}
		if err := reference.Validate(); err != nil {
			return fmt.Errorf("references[%d]: %w", n, err)
		}
		if reference.Name == EvidenceReferenceAlertPackagingEvidence && !validAlertPackagingEvidenceIndexStatus(reference.Status) {
			return fmt.Errorf("references[%d]: alert packaging evidence status %q is invalid", n, reference.Status)
		}
	}
	if got := evidenceIndexStatus(i.References); i.Status != got {
		return fmt.Errorf("evidence index status %q does not match references status %q", i.Status, got)
	}
	if i.OperatorReviewRequired != (i.Status != EvidenceIndexStatusReady) {
		return errors.New("evidence index operatorReviewRequired must match non-ready status")
	}
	if err := i.OperatorReviewDecision.ValidateForStatus(i.Status); err != nil {
		return err
	}
	want := append([]EvidenceIndexReference(nil), i.References...)
	for n := range want {
		want[n].OperatorReviewRequired = want[n].OperatorReviewRequired || want[n].Status != EvidenceIndexStatusReady
	}
	if !reflect.DeepEqual(i.References, want) {
		return errors.New("evidence index reference operatorReviewRequired must match non-ready status")
	}
	return nil
}

// Validate checks that an evidence reference is safe to publish.
func (r EvidenceIndexReference) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("evidence reference name is required")
	}
	if err := validateEvidenceIndexPath(r.Path); err != nil {
		return err
	}
	if !validEvidenceIndexStatus(r.Status) {
		return fmt.Errorf("evidence reference status %q is invalid", r.Status)
	}
	if r.SHA256 != "" {
		if err := supportbundle.ValidateSHA256(r.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func backupExportManifestEvidenceStatus(manifest ExportManifest) string {
	if manifest.PassphraseStatus == PassphraseStatusMissing {
		return EvidenceIndexStatusNeedsReview
	}
	return EvidenceIndexStatusReady
}

func alertPackagingEvidenceIndexStatus(evidence observability.AlertPackagingEvidence) string {
	if evidence.Status == observability.AlertPackagingEvidenceStatusBlocked {
		return EvidenceIndexStatusBlocked
	}
	return EvidenceIndexStatusReady
}

func evidenceIndexStatus(references []EvidenceIndexReference) string {
	status := EvidenceIndexStatusReady
	for _, reference := range references {
		if reference.Status == EvidenceIndexStatusBlocked {
			return EvidenceIndexStatusBlocked
		}
		if reference.Status == EvidenceIndexStatusNeedsReview {
			status = EvidenceIndexStatusNeedsReview
		}
	}
	return status
}

func validEvidenceIndexStatus(status string) bool {
	switch status {
	case EvidenceIndexStatusReady, EvidenceIndexStatusNeedsReview, EvidenceIndexStatusBlocked:
		return true
	default:
		return false
	}
}

func validAlertPackagingEvidenceIndexStatus(status string) bool {
	switch status {
	case observability.AlertPackagingEvidenceStatusReady, observability.AlertPackagingEvidenceStatusBlocked:
		return true
	default:
		return false
	}
}

func validateEvidenceIndexJSONPath(path string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return errors.New("evidence index JSON path is required and must be trimmed")
	}
	if filepath.Ext(path) != ".json" {
		return errors.New("evidence index path must end in .json")
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("evidence index parent directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("evidence index parent path must be a directory")
	}
	return nil
}

func validateEvidenceIndexPath(path string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return errors.New("evidence reference path is required and must be trimmed")
	}
	if filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.Contains(path, "*") {
		return errors.New("evidence reference path must be a safe relative slash path")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return errors.New("evidence reference path must be a safe relative slash path")
	}
	return nil
}
