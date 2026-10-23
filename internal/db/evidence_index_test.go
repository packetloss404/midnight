package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAPISchemaEvidenceIndexRollsUpAndRendersDeterministicJSON(t *testing.T) {
	schema := SchemaEvidence{SchemaVersion: 2, LatestVersion: 2, Status: SchemaEvidenceStatusReady}
	plan := MigrationExecutionPlan{SchemaVersion: 2, LatestVersion: 2, Status: MigrationPlanStatusNeedsReview}
	packet := BuildReleaseEvidencePacket(schema, plan)

	index, err := BuildAPISchemaEvidenceIndex(APISchemaEvidenceIndexInput{
		SchemaEvidencePath:  "artifacts/phase0/schema-evidence.json",
		SchemaEvidence:      schema,
		MigrationPlanPath:   "artifacts/phase0/migration-plan.json",
		MigrationPlan:       plan,
		ReleaseEvidencePath: "artifacts/phase0/release-evidence.json",
		ReleaseEvidence:     packet,
		OpenAPISpecPath:     "api/openapi/v1alpha/midnight.yaml",
	})
	if err != nil {
		t.Fatalf("BuildAPISchemaEvidenceIndex() error = %v", err)
	}
	if index.Status != APISchemaEvidenceIndexStatusNeedsReview || index.SchemaVersion != 2 || index.LatestVersion != 2 {
		t.Fatalf("index summary = %#v", index)
	}
	gotRefs := make([]string, 0, len(index.References))
	for _, ref := range index.References {
		gotRefs = append(gotRefs, ref.Name+":"+ref.Path+":"+ref.Status)
	}
	wantRefs := []string{
		"schema-evidence:artifacts/phase0/schema-evidence.json:ready",
		"migration-plan:artifacts/phase0/migration-plan.json:needs-review",
		"release-evidence:artifacts/phase0/release-evidence.json:needs-review",
		"openapi-spec:api/openapi/v1alpha/midnight.yaml:ready",
	}
	if strings.Join(gotRefs, "\n") != strings.Join(wantRefs, "\n") {
		t.Fatalf("refs = %#v, want %#v", gotRefs, wantRefs)
	}

	first, err := RenderAPISchemaEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("RenderAPISchemaEvidenceIndexJSON() error = %v", err)
	}
	second, err := RenderAPISchemaEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("RenderAPISchemaEvidenceIndexJSON() second error = %v", err)
	}
	if string(first) != string(second) || !strings.HasSuffix(string(first), "\n") {
		t.Fatalf("render was not deterministic with trailing newline: %q vs %q", string(first), string(second))
	}
}

func TestAPISchemaEvidenceIndexValidateRejectsUnsafePathsAndBadRollup(t *testing.T) {
	index := validAPISchemaEvidenceIndexForTest(t)
	index.References[0].Path = "../schema-evidence.json"
	if err := index.Validate(); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("Validate() error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}

	index = validAPISchemaEvidenceIndexForTest(t)
	index.References[0].Path = `artifacts\phase0\schema-evidence.json`
	if err := index.Validate(); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("Validate() error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}

	index = validAPISchemaEvidenceIndexForTest(t)
	index.Status = APISchemaEvidenceIndexStatusReady
	if err := index.Validate(); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("Validate() error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}
}

func TestAPISchemaEvidenceIndexJSONRoundTripIsStrict(t *testing.T) {
	index := validAPISchemaEvidenceIndexForTest(t)
	path := filepath.Join(t.TempDir(), "api-schema-evidence-index.json")
	if err := WriteAPISchemaEvidenceIndexJSON(path, index); err != nil {
		t.Fatalf("WriteAPISchemaEvidenceIndexJSON() error = %v", err)
	}
	got, err := ReadAPISchemaEvidenceIndexJSON(path)
	if err != nil {
		t.Fatalf("ReadAPISchemaEvidenceIndexJSON() error = %v", err)
	}
	if got.Status != index.Status || len(got.References) != len(index.References) || got.References[3].Name != APISchemaEvidenceRefOpenAPISpec {
		t.Fatalf("round trip = %#v, want %#v", got, index)
	}
	if err := WriteAPISchemaEvidenceIndexJSON(filepath.Join(t.TempDir(), "api-schema-evidence-index.txt"), index); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("WriteAPISchemaEvidenceIndexJSON() error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}

	unknown := []byte(`{"schema_version":1,"latest_version":1,"status":"ready","references":[],"extra":true}`)
	if _, err := DecodeAPISchemaEvidenceIndexJSON(unknown); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("DecodeAPISchemaEvidenceIndexJSON() error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}
	encoded, err := RenderAPISchemaEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("RenderAPISchemaEvidenceIndexJSON() error = %v", err)
	}
	if _, err := DecodeAPISchemaEvidenceIndexJSON(append(encoded, []byte(` {}`)...)); !errors.Is(err, ErrInvalidAPISchemaEvidenceIndex) {
		t.Fatalf("DecodeAPISchemaEvidenceIndexJSON() trailing error = %v, want ErrInvalidAPISchemaEvidenceIndex", err)
	}
}

func validAPISchemaEvidenceIndexForTest(t *testing.T) APISchemaEvidenceIndex {
	t.Helper()
	schema := SchemaEvidence{SchemaVersion: 1, LatestVersion: 1, Status: SchemaEvidenceStatusReady}
	plan := MigrationExecutionPlan{SchemaVersion: 1, LatestVersion: 1, Status: MigrationPlanStatusNeedsReview}
	packet := BuildReleaseEvidencePacket(schema, plan)
	index, err := BuildAPISchemaEvidenceIndex(APISchemaEvidenceIndexInput{
		SchemaEvidencePath:  "artifacts/phase0/schema-evidence.json",
		SchemaEvidence:      schema,
		MigrationPlanPath:   "artifacts/phase0/migration-plan.json",
		MigrationPlan:       plan,
		ReleaseEvidencePath: "artifacts/phase0/release-evidence.json",
		ReleaseEvidence:     packet,
		OpenAPISpecPath:     "api/openapi/v1alpha/midnight.yaml",
	})
	if err != nil {
		t.Fatalf("BuildAPISchemaEvidenceIndex() error = %v", err)
	}
	return index
}
