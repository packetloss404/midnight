// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"gopkg.in/yaml.v3"
)

const blockingContract = `openapi: 3.1.2
info: {title: Fixture, version: v1alpha}
paths:
  /widgets:
    post:
      operationId: createWidget
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Widget'
      responses:
        '201':
          description: Created.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Widget'
        '501':
          description: Not implemented.
components:
  schemas:
    Widget:
      type: object
      properties:
        id: {type: string}
    ErrorEnvelope:
      type: object
    ErrorResponse:
      type: object
      required: [type, title, status, error]
      properties:
        type: {type: string}
        title: {type: string}
        status: {type: integer}
        error:
          deprecated: true
          $ref: '#/components/schemas/ErrorEnvelope'
`

const readyContract = `openapi: 3.1.2
info: {title: Fixture, version: v1alpha}
x-problem-types:
  RESOURCE_NOT_FOUND:
    type: https://docs.example.com/errors/resource-not-found
    title: Resource not found
    status: 404
paths:
  /widgets:
    post:
      operationId: createWidget
      tags: [Widgets]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/WidgetCreate'
      responses:
        '201':
          description: Created.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Widget'
components:
  schemas:
    WidgetCreate:
      type: object
      example: {name: demo}
      properties:
        name: {type: string}
    Widget:
      type: object
      example: {id: wid_demo, name: demo}
      properties:
        id: {type: string}
        name: {type: string}
    ErrorResponse:
      type: object
      required: [type, title, status, code]
      properties:
        type: {type: string}
        title: {type: string}
        status: {type: integer}
        code:
          type: string
          enum: [RESOURCE_NOT_FOUND]
`

const fixtureManifest = `audiences:
  product:
    description: Product API.
    path_prefixes: [/widgets]
`

func TestEvaluateReportsEveryRequiredGraduationBlocker(t *testing.T) {
	sourcePath, manifestPath, viewsPath, handlersPath := writeFixture(t, blockingContract, false)
	result, err := evaluate(sourcePath, manifestPath, viewsPath, handlersPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.BlockerCount != 7 || result.OperationCount != 1 {
		t.Fatalf("result=%#v, want not ready with seven blockers and one operation", result)
	}
	want := map[string]int{
		"stable-candidate-scope":       0,
		"no-501-responses":             1,
		"operation-tags":               1,
		"mutation-examples":            2,
		"conditional-mutations":        0,
		"compatibility-error-envelope": 1,
		"problem-type-registry":        1,
		"bounded-json-boundary":        0,
		"generated-view-freshness":     1,
	}
	for _, check := range result.Checks {
		wantStatus := "pass"
		if want[check.ID] > 0 {
			wantStatus = "fail"
		}
		if check.Blockers != want[check.ID] || check.Status != wantStatus {
			t.Fatalf("check %s=%#v, want %d blockers", check.ID, check, want[check.ID])
		}
		delete(want, check.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing checks: %v", want)
	}
}

func TestEvaluateAcceptsReadyContractAndFreshView(t *testing.T) {
	sourcePath, manifestPath, viewsPath, handlersPath := writeFixture(t, withRuntimeProblemTypes(t, readyContract), true)
	result, err := evaluate(sourcePath, manifestPath, viewsPath, handlersPath)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.BlockerCount != 0 {
		t.Fatalf("result=%#v, want ready", result)
	}
	for _, check := range result.Checks {
		if check.Status != "pass" || check.Blockers != 0 {
			t.Fatalf("check=%#v, want pass", check)
		}
	}
}

func TestNo501GateAllowsExplicitAlphaOperationOnly(t *testing.T) {
	contract := `openapi: 3.1.2
paths:
  /alpha:
    post:
      operationId: alphaOperation
      x-stability: alpha
      responses:
        '501': {description: Not implemented.}
  /stable:
    post:
      operationId: stableOperation
      responses:
        '501': {description: Not implemented.}
`
	_, operations := decodeOperations(t, contract)
	result := checkNoNotImplemented(operations)
	if len(result.Findings) != 1 || result.Findings[0].OperationID != "stableOperation" {
		t.Fatalf("findings=%#v, want only stable operation", result.Findings)
	}
}

func TestStableCandidateScopeRequiresReasonForEveryAlphaExclusion(t *testing.T) {
	contract := `openapi: 3.1.2
paths:
  /justified:
    post:
      operationId: justifiedAlpha
      x-stability: alpha
      x-stability-reason: The mutation has no atomic compare-and-swap implementation.
      responses: {'202': {description: Accepted.}}
  /unjustified:
    post:
      operationId: unjustifiedAlpha
      x-stability: alpha
      responses: {'202': {description: Accepted.}}
`
	_, operations := decodeOperations(t, contract)
	result := checkStableCandidateScope(operations)
	if len(result.Findings) != 1 || result.Findings[0].OperationID != "unjustifiedAlpha" {
		t.Fatalf("findings=%#v, want only unjustified alpha operation", result.Findings)
	}
	if got := promotedOperations(operations); len(got) != 0 {
		t.Fatalf("promoted operations=%#v, want both explicitly excluded", got)
	}
}

func TestEvaluateDetectsGeneratedViewStaleness(t *testing.T) {
	sourcePath, manifestPath, viewsPath, handlersPath := writeFixture(t, withRuntimeProblemTypes(t, readyContract), true)
	viewPath := filepath.Join(viewsPath, "product.openapi.yaml")
	if err := os.WriteFile(viewPath, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := evaluate(sourcePath, manifestPath, viewsPath, handlersPath)
	if err != nil {
		t.Fatal(err)
	}
	viewCheck := findCheck(t, result, "generated-view-freshness")
	if result.Ready || viewCheck.Blockers != 1 || viewCheck.Findings[0].Message != "generated audience view is stale" {
		t.Fatalf("result=%#v viewCheck=%#v", result, viewCheck)
	}
}

func TestRunIsNonBlockingUnlessRequireReady(t *testing.T) {
	sourcePath, manifestPath, viewsPath, handlersPath := writeFixture(t, blockingContract, false)
	args := []string{"--format=json", "--source=" + sourcePath, "--audiences=" + manifestPath, "--views=" + viewsPath, "--handler-source=" + handlersPath}
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != exitOK {
		t.Fatalf("report exit=%d stderr=%s", exit, stderr.String())
	}
	var result report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode report: %v output=%s", err, stdout.String())
	}
	if result.SchemaVersion != reportSchemaVersion || result.Ready {
		t.Fatalf("report=%#v", result)
	}

	stdout.Reset()
	stderr.Reset()
	args = append(args, "--require-ready")
	if exit := run(args, &stdout, &stderr); exit != exitBlockers {
		t.Fatalf("strict exit=%d stderr=%s, want %d", exit, stderr.String(), exitBlockers)
	}
}

func TestRunRejectsUnsupportedFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"--format=xml"}, &stdout, &stderr); exit != exitInvocation {
		t.Fatalf("exit=%d stderr=%s, want invocation error", exit, stderr.String())
	}
}

func TestConditionalMutationGateAcceptsStrongETagIfMatchAndPreconditionStatuses(t *testing.T) {
	contract := `openapi: 3.1.2
paths:
  /widgets/{id}:
    get:
      operationId: getWidget
      responses:
        '200':
          description: Widget.
          headers:
            ETag:
              schema:
                type: string
                pattern: '^"[1-9][0-9]*"$'
    patch:
      operationId: updateWidget
      parameters:
        - name: If-Match
          in: header
          required: true
          schema: {type: string}
      responses:
        '200': {description: Updated.}
        '412': {description: Validator did not match.}
        '428': {description: Precondition required.}
`
	root, operations := decodeOperations(t, contract)
	result := checkConditionalMutations(root, operations)
	if len(result.Findings) != 0 {
		t.Fatalf("findings=%#v, want none", result.Findings)
	}
}

func TestConditionalMutationGateCallsExpectedVersionCompatibilityDebt(t *testing.T) {
	contract := `openapi: 3.1.2
paths:
  /widgets/{id}:
    patch:
      operationId: updateWidget
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/UpdateWidget'
      responses:
        '200': {description: Updated.}
components:
  schemas:
    UpdateWidget:
      type: object
      required: [expected_version]
      properties:
        expected_version: {type: integer}
`
	root, operations := decodeOperations(t, contract)
	result := checkConditionalMutations(root, operations)
	if len(result.Findings) != 4 {
		t.Fatalf("findings=%#v, want header, 412, 428, and read-validator blockers", result.Findings)
	}
	foundDebt := false
	for _, finding := range result.Findings {
		foundDebt = foundDebt || strings.Contains(finding.Message, "expected_version is v1alpha compatibility debt")
	}
	if !foundDebt {
		t.Fatalf("findings=%#v, want expected_version compatibility-debt message", result.Findings)
	}
}

func TestCompatibilityEnvelopeGateAcceptsExplicitStableProjectionOmission(t *testing.T) {
	contract := `openapi: 3.1.2
paths: {}
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
`
	root, _ := decodeOperations(t, contract)
	result := checkCompatibilityEnvelope(root)
	if len(result.Findings) != 0 {
		t.Fatalf("findings=%#v, want alpha compatibility field projected out of stable v1", result.Findings)
	}
}

func TestProblemTypeRegistryRejectsLocalAndUndocumentedType(t *testing.T) {
	contract := `openapi: 3.1.2
x-problem-types:
  INTERNAL_ERROR:
    type: https://docs.midnight.local/errors#INTERNAL_ERROR
paths: {}
`
	root, _ := decodeOperations(t, contract)
	result := checkProblemTypeRegistry(root)
	for _, fragment := range []string{"public canonical HTTPS URI", "stable title", "stable HTTP error status", "closed stable enum"} {
		found := false
		for _, finding := range result.Findings {
			found = found || strings.Contains(finding.Message, fragment)
		}
		if !found {
			t.Fatalf("findings=%#v, want message containing %q", result.Findings, fragment)
		}
	}
}

func TestProblemTypeRegistryRequiresSortedClosedRuntimeStatuses(t *testing.T) {
	contract := withRuntimeProblemTypes(t, readyContract)
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(contract), &document); err != nil {
		t.Fatal(err)
	}
	root := document.Content[0]
	registry := mappingValue(root, "x-problem-types")
	entry := mappingValue(registry, string(failurecodes.InternalError))
	replaceMappingValue(entry, "statuses", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!int", Value: "503"},
		{Kind: yaml.ScalarNode, Tag: "!!int", Value: "500"},
	}})
	result := checkProblemTypeRegistry(root)
	if len(result.Findings) == 0 {
		t.Fatal("unsorted and incomplete runtime status set accepted")
	}
}

func TestBoundedJSONBoundaryFindsDirectRequestBodyReads(t *testing.T) {
	directory := t.TempDir()
	source := `package fixture
import ("encoding/json"; "io"; "net/http")
func bad(r *http.Request) {
  _ = json.NewDecoder(r.Body)
  _, _ = io.ReadAll(r.Body)
}

func TestCanonicalHandlersUseSharedBoundedJSONBoundary(t *testing.T) {
	result := checkBoundedJSONBoundary(filepath.Join("..", "..", "internal", "api"))
	if len(result.Findings) != 0 {
		t.Fatalf("canonical handler findings=%#v, want none", result.Findings)
	}
}
`
	if err := os.WriteFile(filepath.Join(directory, "handler.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	result := checkBoundedJSONBoundary(directory)
	if len(result.Findings) != 2 {
		t.Fatalf("findings=%#v, want decoder and unbounded-read blockers", result.Findings)
	}
}

func writeFixture(t *testing.T, contract string, generateView bool) (string, string, string, string) {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "midnight.yaml")
	manifestPath := filepath.Join(directory, "audiences.yaml")
	viewsPath := filepath.Join(directory, "views")
	handlersPath := filepath.Join(directory, "handlers")
	if err := os.WriteFile(sourcePath, []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(fixtureManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(viewsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(handlersPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlersPath, "safe.go"), []byte("package handlers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if generateView {
		generated, err := renderView([]byte(contract), "product", audience{
			Description: "Product API.", PathPrefixes: []string{"/widgets"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(viewsPath, "product.openapi.yaml"), generated, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sourcePath, manifestPath, viewsPath, handlersPath
}

func findCheck(t *testing.T, result report, id string) check {
	t.Helper()
	for _, check := range result.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %q not found", id)
	return check{}
}

func decodeOperations(t *testing.T, contract string) (*yaml.Node, []operation) {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(contract), &document); err != nil {
		t.Fatal(err)
	}
	root := document.Content[0]
	operations, err := enumerateOperations(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, operations
}

func withRuntimeProblemTypes(t *testing.T, contract string) string {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(contract), &document); err != nil {
		t.Fatal(err)
	}
	root := document.Content[0]
	registry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	codes := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, definition := range failurecodes.APIProblemTypes() {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setScalar(entry, "type", definition.Type)
		setScalar(entry, "title", definition.Title)
		replaceMappingValue(entry, "status", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(definition.Status)})
		statuses := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, status := range definition.Statuses {
			statuses.Content = append(statuses.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(status)})
		}
		replaceMappingValue(entry, "statuses", statuses)
		registry.Content = append(registry.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(definition.Code)}, entry,
		)
		codes.Content = append(codes.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(definition.Code)})
	}
	replaceMappingValue(root, "x-problem-types", registry)
	errorSchema := localReference(root, "#/components/schemas/ErrorResponse")
	codeSchema := mappingValue(mappingValue(errorSchema, "properties"), "code")
	replaceMappingValue(codeSchema, "enum", codes)
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
