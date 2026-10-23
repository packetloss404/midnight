package build

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const ReleaseEvidenceBundleSchemaVersion = "midnight.dev/build-registry-release-evidence-bundle/v1alpha1"

type ReleaseEvidenceBundleInput struct {
	ReleaseEvidence ReleaseEvidencePacket
	EvidenceIndex   EvidenceIndex
}

// ReleaseEvidenceBundle keeps the release evidence packet and its evidence
// index together so a release decision can validate both artifacts agree.
type ReleaseEvidenceBundle struct {
	SchemaVersion   string                `json:"schemaVersion"`
	Status          ReviewStatus          `json:"status"`
	ReleaseEvidence ReleaseEvidencePacket `json:"releaseEvidence"`
	EvidenceIndex   EvidenceIndex         `json:"evidenceIndex"`
}

func NewReleaseEvidenceBundle(input ReleaseEvidenceBundleInput) (ReleaseEvidenceBundle, error) {
	bundle := ReleaseEvidenceBundle{
		SchemaVersion:   ReleaseEvidenceBundleSchemaVersion,
		Status:          input.ReleaseEvidence.Status,
		ReleaseEvidence: input.ReleaseEvidence,
		EvidenceIndex:   input.EvidenceIndex,
	}
	if err := bundle.Validate(); err != nil {
		return ReleaseEvidenceBundle{}, err
	}
	return bundle, nil
}

func (b ReleaseEvidenceBundle) Validate() error {
	if b.SchemaVersion != ReleaseEvidenceBundleSchemaVersion {
		return errors.New("release evidence bundle schemaVersion is invalid")
	}
	if !validReviewStatus(b.Status) {
		return fmt.Errorf("release evidence bundle status %q is invalid", b.Status)
	}
	if err := b.ReleaseEvidence.Validate(); err != nil {
		return fmt.Errorf("release evidence bundle packet: %w", err)
	}
	if err := b.EvidenceIndex.Validate(); err != nil {
		return fmt.Errorf("release evidence bundle index: %w", err)
	}
	if b.ReleaseEvidence.Status != b.EvidenceIndex.Status {
		return fmt.Errorf("release evidence bundle status mismatch: packet %q index %q", b.ReleaseEvidence.Status, b.EvidenceIndex.Status)
	}
	if b.Status != b.ReleaseEvidence.Status {
		return fmt.Errorf("release evidence bundle status %q does not match packet status %q", b.Status, b.ReleaseEvidence.Status)
	}
	return nil
}

func MarshalReleaseEvidenceBundleJSON(bundle ReleaseEvidenceBundle) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(bundle)
}

// WriteReleaseEvidenceBundleJSONFile validates path and bundle before writing
// deterministic release evidence bundle JSON. It only checks the explicit parent path.
func WriteReleaseEvidenceBundleJSONFile(path string, bundle ReleaseEvidenceBundle) error {
	if err := validateReleaseEvidenceBundleJSONPath(path, osPathExists); err != nil {
		return err
	}
	data, err := MarshalReleaseEvidenceBundleJSON(bundle)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadReleaseEvidenceBundleJSONFile validates path, decodes JSON, and validates
// the release evidence bundle before returning it.
func ReadReleaseEvidenceBundleJSONFile(path string) (ReleaseEvidenceBundle, error) {
	if err := validateReleaseEvidenceBundleJSONPath(path, osPathExists); err != nil {
		return ReleaseEvidenceBundle{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseEvidenceBundle{}, err
	}
	return unmarshalReleaseEvidenceBundleJSON(data)
}

func unmarshalReleaseEvidenceBundleJSON(data []byte) (ReleaseEvidenceBundle, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var bundle ReleaseEvidenceBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ReleaseEvidenceBundle{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("release evidence bundle JSON must contain a single object")
		}
		return ReleaseEvidenceBundle{}, err
	}
	if err := bundle.Validate(); err != nil {
		return ReleaseEvidenceBundle{}, err
	}
	return bundle, nil
}

func validateReleaseEvidenceBundleJSONPath(path string, exists func(string) (bool, error)) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("release evidence bundle path must not be empty")
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || filepath.Ext(base) != ".json" {
		return fmt.Errorf("release evidence bundle path %q must be an explicit .json file path", path)
	}
	parent := filepath.Dir(cleaned)
	ok, err := exists(parent)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("release evidence bundle path parent %q does not exist", parent)
	}
	return nil
}
