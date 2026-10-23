package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAddsAndChecksDeterministicMetadata(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "openapi.yaml")
	manifestPath := filepath.Join(directory, "audiences.yaml")
	source := `openapi: 3.1.2
paths:
  /widgets:
    post:
      operationId: createWidget
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
      responses:
        '201':
          description: Created.
          content:
            application/json:
              schema:
                type: object
                required: [id]
                properties:
                  id: {type: string}
`
	manifest := "audiences:\n  product:\n    path_prefixes: [/widgets]\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(sourcePath, manifestPath, true); err == nil {
		t.Fatal("check accepted missing metadata")
	}
	if err := run(sourcePath, manifestPath, false); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(first)
	for _, want := range []string{`tags: ["Product","Widgets"]`, `example: {"name":"demo"}`, `example: {"id":"id_demo"}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated contract missing %q:\n%s", want, text)
		}
	}
	if err := run(sourcePath, manifestPath, true); err != nil {
		t.Fatal(err)
	}
	if err := run(sourcePath, manifestPath, false); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(sourcePath)
	if string(second) != text {
		t.Fatal("metadata generation is not idempotent")
	}
}

func TestRunKeepsTagsAheadOfSequencePropertyAndInlineExampleAtMediaLevel(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "openapi.yaml")
	manifestPath := filepath.Join(directory, "audiences.yaml")
	source := `openapi: 3.1.2
paths:
  /widgets:
    post:
      security:
        - WidgetCredential: []
      operationId: createWidget
      requestBody:
        content:
          application/json:
            schema: {type: string}
      responses:
        '204':
          description: Accepted.
`
	manifest := "audiences:\n  product:\n    path_prefixes: [/widgets]\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(sourcePath, manifestPath, false); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{
		"    post:\n      tags: [\"Product\",\"Widgets\"]\n      security:\n        - WidgetCredential: []",
		"          application/json:\n            schema: {type: string}\n            example: \"createWidget request\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated contract missing structurally safe fragment %q:\n%s", want, text)
		}
	}
	if err := run(sourcePath, manifestPath, true); err != nil {
		t.Fatal(err)
	}
}
