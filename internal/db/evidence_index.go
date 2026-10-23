package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

const (
	APISchemaEvidenceIndexStatusReady       = "ready"
	APISchemaEvidenceIndexStatusNeedsReview = "needs-review"
	APISchemaEvidenceIndexStatusBlocked     = "blocked"

	APISchemaEvidenceRefSchemaEvidence  = "schema-evidence"
	APISchemaEvidenceRefMigrationPlan   = "migration-plan"
	APISchemaEvidenceRefReleaseEvidence = "release-evidence"
	APISchemaEvidenceRefOpenAPISpec     = "openapi-spec"
)

var ErrInvalidAPISchemaEvidenceIndex = errors.New("invalid api schema evidence index")

type APISchemaEvidenceIndexInput struct {
	SchemaEvidencePath  string
	SchemaEvidence      SchemaEvidence
	MigrationPlanPath   string
	MigrationPlan       MigrationExecutionPlan
	ReleaseEvidencePath string
	ReleaseEvidence     ReleaseEvidencePacket
	OpenAPISpecPath     string
}

type APISchemaEvidenceIndex struct {
	SchemaVersion int                         `json:"schema_version"`
	LatestVersion int                         `json:"latest_version"`
	Status        string                      `json:"status"`
	References    []APISchemaEvidenceIndexRef `json:"references"`
}

type APISchemaEvidenceIndexRef struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

func BuildAPISchemaEvidenceIndex(input APISchemaEvidenceIndexInput) (APISchemaEvidenceIndex, error) {
	refs := []APISchemaEvidenceIndexRef{
		{Name: APISchemaEvidenceRefSchemaEvidence, Path: input.SchemaEvidencePath, Status: input.SchemaEvidence.Status},
		{Name: APISchemaEvidenceRefMigrationPlan, Path: input.MigrationPlanPath, Status: input.MigrationPlan.Status},
		{Name: APISchemaEvidenceRefReleaseEvidence, Path: input.ReleaseEvidencePath, Status: input.ReleaseEvidence.Status},
		{Name: APISchemaEvidenceRefOpenAPISpec, Path: input.OpenAPISpecPath, Status: APISchemaEvidenceIndexStatusReady},
	}
	index := APISchemaEvidenceIndex{
		SchemaVersion: input.SchemaEvidence.SchemaVersion,
		LatestVersion: input.SchemaEvidence.LatestVersion,
		Status:        rollupAPISchemaEvidenceIndexStatus(refs),
		References:    refs,
	}
	if input.MigrationPlan.SchemaVersion > index.SchemaVersion {
		index.SchemaVersion = input.MigrationPlan.SchemaVersion
	}
	if input.ReleaseEvidence.SchemaVersion > index.SchemaVersion {
		index.SchemaVersion = input.ReleaseEvidence.SchemaVersion
	}
	if input.MigrationPlan.LatestVersion > index.LatestVersion {
		index.LatestVersion = input.MigrationPlan.LatestVersion
	}
	if input.ReleaseEvidence.LatestVersion > index.LatestVersion {
		index.LatestVersion = input.ReleaseEvidence.LatestVersion
	}
	if err := index.Validate(); err != nil {
		return APISchemaEvidenceIndex{}, err
	}
	return index, nil
}

func (index APISchemaEvidenceIndex) Validate() error {
	if !validAPISchemaEvidenceIndexStatus(index.Status) {
		return fmt.Errorf("%w: status %q is invalid", ErrInvalidAPISchemaEvidenceIndex, index.Status)
	}
	wantNames := []string{
		APISchemaEvidenceRefSchemaEvidence,
		APISchemaEvidenceRefMigrationPlan,
		APISchemaEvidenceRefReleaseEvidence,
		APISchemaEvidenceRefOpenAPISpec,
	}
	if len(index.References) != len(wantNames) {
		return fmt.Errorf("%w: must include four deterministic references", ErrInvalidAPISchemaEvidenceIndex)
	}
	for i, ref := range index.References {
		if ref.Name != wantNames[i] {
			return fmt.Errorf("%w: references[%d] name %q is invalid", ErrInvalidAPISchemaEvidenceIndex, i, ref.Name)
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: references[%d]: %v", ErrInvalidAPISchemaEvidenceIndex, i, err)
		}
	}
	if want := rollupAPISchemaEvidenceIndexStatus(index.References); index.Status != want {
		return fmt.Errorf("%w: status %q does not match rollup %q", ErrInvalidAPISchemaEvidenceIndex, index.Status, want)
	}
	return nil
}

func (ref APISchemaEvidenceIndexRef) Validate() error {
	if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Name) != ref.Name {
		return errors.New("reference name is required and must be trimmed")
	}
	if err := validateAPISchemaEvidenceIndexRefPath(ref.Path); err != nil {
		return err
	}
	if !validAPISchemaEvidenceIndexStatus(ref.Status) {
		return fmt.Errorf("reference status %q is invalid", ref.Status)
	}
	return nil
}

func RenderAPISchemaEvidenceIndexJSON(index APISchemaEvidenceIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(index); err != nil {
		return nil, fmt.Errorf("render api schema evidence index: %w", err)
	}
	return out.Bytes(), nil
}

func WriteAPISchemaEvidenceIndexJSON(path string, index APISchemaEvidenceIndex) error {
	if err := validateAPISchemaEvidenceIndexJSONPath(path); err != nil {
		return err
	}
	data, err := RenderAPISchemaEvidenceIndexJSON(index)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ReadAPISchemaEvidenceIndexJSON(path string) (APISchemaEvidenceIndex, error) {
	if err := validateAPISchemaEvidenceIndexJSONPath(path); err != nil {
		return APISchemaEvidenceIndex{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return APISchemaEvidenceIndex{}, err
	}
	return DecodeAPISchemaEvidenceIndexJSON(data)
}

func DecodeAPISchemaEvidenceIndexJSON(data []byte) (APISchemaEvidenceIndex, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var index APISchemaEvidenceIndex
	if err := decoder.Decode(&index); err != nil {
		return APISchemaEvidenceIndex{}, fmt.Errorf("%w: %v", ErrInvalidAPISchemaEvidenceIndex, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return APISchemaEvidenceIndex{}, fmt.Errorf("%w: trailing JSON content", ErrInvalidAPISchemaEvidenceIndex)
	}
	if err := index.Validate(); err != nil {
		return APISchemaEvidenceIndex{}, err
	}
	return index, nil
}

func rollupAPISchemaEvidenceIndexStatus(refs []APISchemaEvidenceIndexRef) string {
	status := APISchemaEvidenceIndexStatusReady
	for _, ref := range refs {
		if ref.Status == APISchemaEvidenceIndexStatusBlocked {
			return APISchemaEvidenceIndexStatusBlocked
		}
		if ref.Status == APISchemaEvidenceIndexStatusNeedsReview {
			status = APISchemaEvidenceIndexStatusNeedsReview
		}
	}
	return status
}

func validAPISchemaEvidenceIndexStatus(status string) bool {
	switch status {
	case APISchemaEvidenceIndexStatusReady, APISchemaEvidenceIndexStatusNeedsReview, APISchemaEvidenceIndexStatusBlocked:
		return true
	default:
		return false
	}
}

func validateAPISchemaEvidenceIndexRefPath(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return errors.New("reference path is required and must be trimmed")
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.Contains(value, "\\") || strings.Contains(value, "*") {
		return errors.New("reference path must be a safe relative slash path")
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." || cleaned != value {
		return errors.New("reference path must be a safe relative slash path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return errors.New("reference path must be a safe relative slash path")
		}
	}
	return nil
}

func validateAPISchemaEvidenceIndexJSONPath(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: path is required and must be trimmed", ErrInvalidAPISchemaEvidenceIndex)
	}
	if filepath.Ext(value) != ".json" || filepath.Base(value) == ".json" {
		return fmt.Errorf("%w: path must end with .json", ErrInvalidAPISchemaEvidenceIndex)
	}
	return nil
}
