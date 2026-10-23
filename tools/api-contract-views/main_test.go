package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCanonicalAudienceManifestCoversEveryPath(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	source, err := os.ReadFile(filepath.Join(root, defaultSource))
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, defaultManifest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest audienceManifest
	if err := yaml.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := validateAudienceCoverage(source, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestAudienceCoverageRejectsUnassignedCanonicalPath(t *testing.T) {
	source := []byte("openapi: 3.1.2\npaths:\n  /covered: {}\n  /missing: {}\n")
	manifest := audienceManifest{Audiences: map[string]audience{
		"product": {PathPrefixes: []string{"/covered"}},
	}}
	err := validateAudienceCoverage(source, manifest)
	if err == nil || !strings.Contains(err.Error(), "/missing") {
		t.Fatalf("validateAudienceCoverage() error = %v, want /missing", err)
	}
}

func TestStableCandidateOmitsOnlyMarkedAlphaCompatibilityEnvelope(t *testing.T) {
	source := []byte(`openapi: 3.1.2
info:
  title: Fixture
  version: v1alpha
  description: Authoritative v1alpha fixture contract.
x-source-contract: alpha
servers: [{url: /api/v1alpha, description: Same-origin v1alpha API root.}]
paths:
  /mixed:
    get:
      operationId: promotedRead
      responses: {'200': {description: OK.}}
    delete:
      operationId: excludedDelete
      x-stability: alpha
      x-stability-reason: No atomic If-Match implementation.
      responses: {'204': {description: Deleted.}}
  /alpha-only:
    post:
      operationId: excludedCreate
      x-stability: alpha
      x-stability-reason: Development-only operation.
      responses: {'501': {description: Not implemented.}}
components:
  schemas:
    ErrorEnvelope: {type: object}
    ErrorResponse:
      type: object
      required: [type, error]
      properties:
        type: {type: string}
        error:
          deprecated: true
          x-stable-v1-omit: true
          $ref: '#/components/schemas/ErrorEnvelope'
`)
	generated, err := renderStableCandidate(source)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	root := document.Content[0]
	if got := mappingValue(mappingValue(root, "info"), "version").Value; got != "v1" {
		t.Fatalf("version = %q, want v1", got)
	}
	if got := mappingValue(mappingValue(root, "info"), "description").Value; strings.Contains(got, "v1alpha") || !strings.Contains(got, "stable v1") {
		t.Fatalf("stable info description = %q", got)
	}
	if got := mappingValue(mappingValue(root, "servers").Content[0], "url").Value; got != "/api/v1" {
		t.Fatalf("server URL = %q, want /api/v1", got)
	}
	if got := mappingValue(mappingValue(root, "servers").Content[0], "description").Value; strings.Contains(got, "v1alpha") || !strings.Contains(got, "stable v1") {
		t.Fatalf("stable server description = %q", got)
	}
	paths := mappingValue(root, "paths")
	if mappingValue(paths, "/alpha-only") != nil {
		t.Fatal("stable candidate retained path containing only excluded operations")
	}
	mixed := mappingValue(paths, "/mixed")
	if mappingValue(mixed, "get") == nil || mappingValue(mixed, "delete") != nil {
		t.Fatal("stable candidate did not retain only the promoted operation on a mixed path")
	}
	errorSchema := localReference(root, "#/components/schemas/ErrorResponse")
	if mappingValue(mappingValue(errorSchema, "properties"), "error") != nil {
		t.Fatal("stable candidate retained compatibility error property")
	}
	for _, required := range mappingValue(errorSchema, "required").Content {
		if required.Value == "error" {
			t.Fatal("stable candidate retained compatibility error requirement")
		}
	}
}

func TestCanonicalStableCandidateHasExactPromotedOperationInventory(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	source, err := os.ReadFile(filepath.Join(root, defaultSource))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := renderStableCandidate(source)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	canonicalCount, promotedCount := 0, 0
	var sourceDocument yaml.Node
	if err := yaml.Unmarshal(source, &sourceDocument); err != nil {
		t.Fatal(err)
	}
	sourcePaths := mappingValue(sourceDocument.Content[0], "paths")
	for index := 1; index < len(sourcePaths.Content); index += 2 {
		pathItem := sourcePaths.Content[index]
		for operationIndex := 0; operationIndex+1 < len(pathItem.Content); operationIndex += 2 {
			if !isOperationMethod(pathItem.Content[operationIndex].Value) {
				continue
			}
			canonicalCount++
			if scalarValue(mappingValue(pathItem.Content[operationIndex+1], "x-stability")) != "alpha" {
				promotedCount++
			}
		}
	}
	count := 0
	paths := mappingValue(document.Content[0], "paths")
	for index := 1; index < len(paths.Content); index += 2 {
		pathItem := paths.Content[index]
		for operationIndex := 0; operationIndex+1 < len(pathItem.Content); operationIndex += 2 {
			if !isOperationMethod(pathItem.Content[operationIndex].Value) {
				continue
			}
			count++
			if scalarValue(mappingValue(pathItem.Content[operationIndex+1], "x-stability")) == "alpha" {
				t.Fatal("stable candidate retained explicitly excluded operation")
			}
		}
	}
	if count != promotedCount {
		t.Fatalf("stable candidate operations = %d, want %d promoted of %d canonical operations", count, promotedCount, canonicalCount)
	}
}

func TestStableCandidateRejectsUnmarkedCompatibilityEnvelope(t *testing.T) {
	source := []byte(`openapi: 3.1.2
info: {title: Fixture, version: v1alpha}
paths: {}
components:
  schemas:
    ErrorResponse:
      properties:
        error: {deprecated: true}
`)
	if _, err := renderStableCandidate(source); err == nil || !strings.Contains(err.Error(), "not marked") {
		t.Fatalf("error = %v, want unmarked omission rejection", err)
	}
}
