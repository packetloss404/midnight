package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestContractCheckAcceptsValidOpenAPI verifies that a synthetic OpenAPI
// document whose error codes are all present in a synthetic registry passes
// the contract check (exit 0, empty MissingCodes).
func TestContractCheckAcceptsValidOpenAPI(t *testing.T) {
	openapi := []byte(`openapi: 3.1.0
info:
  title: Synthetic
  version: v1alpha
paths:
  /widgets:
    get:
      summary: List widgets.
      operationId: listWidgets
      responses:
        '200':
          description: ok
        '400':
          description: bad
  /widgets/{id}:
    get:
      summary: Get widget.
      operationId: getWidget
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
        '500':
          description: internal
components:
  schemas:
    Issue:
      type: object
      properties:
        code:
          type: string
          enum:
            - WIDGET_INVALID
            - WIDGET_NOT_FOUND
        message:
          type: string
`)
	registry := []byte(`# Error Codes

| Code | Owner | Notes | Recovery hint |
|---|---|---|---|
| ` + "`WIDGET_INVALID`" + ` | S08 | Widget validation failed. | Fix widget input. |
| ` + "`WIDGET_NOT_FOUND`" + ` | S08 | Widget id missing. | Verify widget id. |
`)
	report, err := CheckOpenAPIErrorCodes(openapi, registry)
	if err != nil {
		t.Fatalf("CheckOpenAPIErrorCodes() error = %v", err)
	}
	if len(report.MissingCodes) != 0 {
		t.Fatalf("MissingCodes = %+v, want empty", report.MissingCodes)
	}
	if len(report.Operations) != 2 {
		t.Fatalf("Operations = %d, want 2", len(report.Operations))
	}
	if report.Operations[0].OperationID != "listWidgets" {
		t.Fatalf("Operations[0].OperationID = %q, want listWidgets", report.Operations[0].OperationID)
	}
	if !contains(report.Operations[0].StatusCodes, "400") {
		t.Fatalf("Operations[0].StatusCodes = %v, want 400 included", report.Operations[0].StatusCodes)
	}
	rendered := report.Render()
	if !strings.Contains(rendered, "Operations: 2") {
		t.Fatalf("Render() missing operations count: %s", rendered)
	}
}

func TestContractCheckRejectsUnregisteredCode(t *testing.T) {
	openapi := []byte(`openapi: 3.1.0
paths:
  /a:
    get:
      operationId: getA
      responses:
        '200':
          description: ok
components:
  schemas:
    Issue:
      type: object
      properties:
        code:
          type: string
          enum:
            - WIDGET_OK
            - WIDGET_BOGUS_CODE
`)
	registry := []byte(`# Error Codes

| Code | Owner | Notes | Recovery hint |
|---|---|---|---|
| ` + "`WIDGET_OK`" + ` | S08 | n | n |
`)
	report, err := CheckOpenAPIErrorCodes(openapi, registry)
	if err != nil {
		t.Fatalf("CheckOpenAPIErrorCodes() error = %v", err)
	}
	if len(report.MissingCodes) != 1 || report.MissingCodes[0].Code != "WIDGET_BOGUS_CODE" { // not-a-failure-code (test fixture)
		t.Fatalf("MissingCodes = %+v, want WIDGET_BOGUS_CODE", report.MissingCodes) // not-a-failure-code (test fixture)
	}
}

func TestContractCheckRejectsEmptyOpenAPI(t *testing.T) {
	if _, err := CheckOpenAPIErrorCodes(nil, []byte("| `X` |")); err == nil {
		t.Fatal("CheckOpenAPIErrorCodes() error = nil, want empty payload error")
	}
}

func TestContractCheckRejectsRegistryWithoutCodes(t *testing.T) {
	if _, err := CheckOpenAPIErrorCodes([]byte("paths:\n  /x:\n    get: {}\n"), []byte("# header only\n")); err == nil {
		t.Fatal("CheckOpenAPIErrorCodes() error = nil, want registry-without-codes error")
	}
}

func TestOpenAPISecurityUsesGlobalDefaultAndOperationOverrides(t *testing.T) {
	openapi := []byte(`openapi: 3.1.0
security:
  - BearerAuth: []
paths:
  /protected:
    get:
      operationId: getProtected
      responses:
        '200':
          description: ok
        '401':
          description: unauthorized
  /human:
    post:
      security:
        - BearerAuth: []
        - HumanSession: []
      operationId: createHumanResource
      responses:
        '201':
          description: created
        '401':
          description: unauthorized
  /public:
    get:
      security: []
      operationId: getPublic
      responses:
        '200':
          description: ok
components:
  schemas:
    Issue:
      properties:
        code:
          enum:
            - TEST_ERROR
`)
	registry := []byte("| `TEST_ERROR` | owner | notes | hint |\n")
	report, err := CheckOpenAPIErrorCodes(openapi, registry)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Operations[0].Security; len(got) != 1 || got[0] != "BearerAuth" {
		t.Fatalf("global security = %v, want BearerAuth", got)
	}
	if got := report.Operations[1].Security; len(got) != 2 ||
		got[0] != "BearerAuth" ||
		got[1] != "HumanSession" {
		t.Fatalf("explicit security = %v, want BearerAuth,HumanSession", got)
	}
	if got := report.Operations[2].Security; len(got) != 0 {
		t.Fatalf("public security = %v, want empty", got)
	}
	securityReport := CheckOperationSecurity(report.Operations)
	if !securityReport.Pass() || securityReport.Protected != 2 {
		t.Fatalf("security report = %+v, want two protected operations and pass", securityReport)
	}
}

func TestCanonicalEnvironmentMetadataPatchContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi", "v1alpha", "midnight.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	paths := contractMap(t, document, "paths")
	environmentPath := contractMap(t, paths, "/environments/{id}")
	patch := contractMap(t, environmentPath, "patch")
	parameters := contractSlice(t, patch, "parameters")
	if len(parameters) != 3 {
		t.Fatalf("PATCH parameters = %d, want path, optional idempotency, and If-Match", len(parameters))
	}
	idempotency := parameters[1].(map[string]any)
	if got := idempotency["$ref"]; got != "#/components/parameters/OptionalIdempotencyKey" {
		t.Fatalf("PATCH idempotency parameter = %v", got)
	}
	ifMatch := parameters[2].(map[string]any)
	if got := ifMatch["$ref"]; got != "#/components/parameters/IfMatch" {
		t.Fatalf("PATCH conditional parameter = %v", got)
	}

	requestBody := contractMap(t, patch, "requestBody")
	content := contractMap(t, requestBody, "content")
	jsonBody := contractMap(t, content, "application/json")
	bodySchema := contractMap(t, jsonBody, "schema")
	alternatives := contractSlice(t, bodySchema, "anyOf")
	refs := make(map[string]bool, len(alternatives))
	for _, alternative := range alternatives {
		refs[alternative.(map[string]any)["$ref"].(string)] = true
	}
	for _, ref := range []string{
		"#/components/schemas/EnvironmentMetadataUpdateRequest",
		"#/components/schemas/EnvironmentMutationRequest",
	} {
		if !refs[ref] {
			t.Fatalf("PATCH request union missing %s", ref)
		}
	}

	components := contractMap(t, document, "components")
	parametersByName := contractMap(t, components, "parameters")
	optionalKey := contractMap(t, parametersByName, "OptionalIdempotencyKey")
	if required, ok := optionalKey["required"].(bool); !ok || required {
		t.Fatalf("OptionalIdempotencyKey.required = %v, want false", optionalKey["required"])
	}

	schemas := contractMap(t, components, "schemas")
	human := contractMap(t, schemas, "EnvironmentMetadataUpdateRequest")
	if additional, ok := human["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("human additionalProperties = %v, want false", human["additionalProperties"])
	}
	if !contractStringSet(contractSlice(t, human, "required"))["expected_version"] {
		t.Fatal("human request does not require expected_version")
	}
	metadataChoices := contractSlice(t, human, "anyOf")
	if len(metadataChoices) != 3 {
		t.Fatalf("human request anyOf choices = %d, want 3 singleton choices", len(metadataChoices))
	}
	metadataFields := make(map[string]bool, len(metadataChoices))
	for _, choice := range metadataChoices {
		required := contractSlice(t, choice.(map[string]any), "required")
		if len(required) != 1 {
			t.Fatalf("human request anyOf required = %v, want one field", required)
		}
		metadataFields[required[0].(string)] = true
	}
	for _, field := range []string{"slug", "name", "tier"} {
		if !metadataFields[field] {
			t.Fatalf("human request anyOf does not require %s", field)
		}
	}
	humanProperties := contractMap(t, human, "properties")
	if len(humanProperties) != 4 {
		t.Fatalf("human request properties = %v, want exact metadata allowlist", humanProperties)
	}
	for _, field := range []string{"expected_version", "slug", "name", "tier"} {
		if _, ok := humanProperties[field]; !ok {
			t.Fatalf("human request properties missing %s", field)
		}
	}
	expectedVersion := contractMap(t, humanProperties, "expected_version")
	if got := expectedVersion["minimum"]; got != 1 {
		t.Fatalf("expected_version.minimum = %v, want 1", got)
	}
	if got := expectedVersion["type"]; got != "integer" {
		t.Fatalf("expected_version.type = %v, want integer", got)
	}
	if got := expectedVersion["format"]; got != "int64" {
		t.Fatalf("expected_version.format = %v, want int64", got)
	}

	responses := contractMap(t, patch, "responses")
	for _, status := range []string{"412", "428"} {
		if _, ok := responses[status]; !ok {
			t.Fatalf("PATCH responses missing conditional status %s", status)
		}
	}
	success := contractMap(t, responses, "200")
	successContent := contractMap(t, success, "content")
	successJSON := contractMap(t, successContent, "application/json")
	successSchema := contractMap(t, successJSON, "schema")
	if got := successSchema["$ref"]; got != "#/components/schemas/Environment" {
		t.Fatalf("PATCH 200 schema = %v, want Environment", got)
	}

	environment := contractMap(t, schemas, "Environment")
	if !contractStringSet(contractSlice(t, environment, "required"))["resource_version"] {
		t.Fatal("Environment does not require resource_version")
	}
	environmentProperties := contractMap(t, environment, "properties")
	resourceVersion := contractMap(t, environmentProperties, "resource_version")
	if got := resourceVersion["minimum"]; got != 1 {
		t.Fatalf("resource_version.minimum = %v, want 1", got)
	}
	if got := resourceVersion["type"]; got != "integer" {
		t.Fatalf("resource_version.type = %v, want integer", got)
	}
	if got := resourceVersion["format"]; got != "int64" {
		t.Fatalf("resource_version.format = %v, want int64", got)
	}
}

func TestCanonicalTenantVariableHumanContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi", "v1alpha", "midnight.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	paths := contractMap(t, document, "paths")
	collection := contractMap(t, paths, "/services/{service_id}/environments/{environment_id}/variables")
	item := contractMap(t, paths, "/services/{service_id}/environments/{environment_id}/variables/{variable_id}")
	operations := []struct {
		name        string
		parent      map[string]any
		method      string
		operation   string
		statuses    []string
		mutation    bool
		item        bool
		requestRef  string
		success     string
		headers     []string
		conditional bool
	}{
		{
			name: "list", parent: collection, method: "get",
			operation: "listTenantServiceEnvironmentVariables",
			statuses:  []string{"200", "401", "403", "404", "503"},
			success:   "200",
		},
		{
			name: "create", parent: collection, method: "post",
			operation: "createTenantServiceEnvironmentVariable",
			statuses:  []string{"201", "400", "401", "403", "404", "409", "413", "500", "503"},
			mutation:  true, requestRef: "#/components/schemas/TenantVariableCreateRequest",
			success: "201", headers: []string{"ETag", "Location", "Idempotent-Replayed"},
		},
		{
			name: "get", parent: item, method: "get",
			operation: "getTenantServiceEnvironmentVariable",
			statuses:  []string{"200", "401", "403", "404", "503"},
			item:      true, success: "200", headers: []string{"ETag"},
		},
		{
			name: "update", parent: item, method: "patch",
			operation: "updateTenantServiceEnvironmentVariable",
			statuses:  []string{"200", "400", "401", "403", "404", "409", "412", "413", "428", "500", "503"},
			mutation:  true, item: true, requestRef: "#/components/schemas/TenantVariableUpdateRequest",
			success: "200", headers: []string{"ETag", "Idempotent-Replayed"}, conditional: true,
		},
		{
			name: "delete", parent: item, method: "delete",
			operation: "deleteTenantServiceEnvironmentVariable",
			statuses:  []string{"204", "400", "401", "403", "404", "409", "412", "413", "428", "500", "503"},
			mutation:  true, item: true, requestRef: "#/components/schemas/TenantVariableDeleteRequest",
			success: "204", headers: []string{"Idempotent-Replayed"}, conditional: true,
		},
	}
	for _, expectation := range operations {
		t.Run(expectation.name, func(t *testing.T) {
			operation := contractMap(t, expectation.parent, expectation.method)
			if got := operation["operationId"]; got != expectation.operation {
				t.Fatalf("operationId = %v, want %s", got, expectation.operation)
			}
			security := contractSlice(t, operation, "security")
			if len(security) != 1 {
				t.Fatalf("security = %#v, want HumanSession only", security)
			}
			requirement := security[0].(map[string]any)
			if len(requirement) != 1 {
				t.Fatalf("security requirement = %#v, want HumanSession only", requirement)
			}
			if _, ok := requirement["HumanSession"]; !ok {
				t.Fatalf("security requirement = %#v, want HumanSession", requirement)
			}
			if _, ok := requirement["BearerAuth"]; ok {
				t.Fatalf("nested tenant variable operation admits recovery BearerAuth: %#v", requirement)
			}
			description, _ := operation["description"].(string)
			description = strings.ToLower(description)
			capability := "variables.read"
			forbiddenCapability := "service.read"
			if expectation.mutation {
				capability = "variables.write"
				forbiddenCapability = "service.write"
			}
			if !strings.Contains(description, capability) {
				t.Fatalf("description missing exact %s capability: %q", capability, description)
			}
			if strings.Contains(description, forbiddenCapability) {
				t.Fatalf("description retains incorrect %s capability: %q", forbiddenCapability, description)
			}
			if expectation.name == "update" {
				for _, marker := range []string{
					"between `plain` and `secret`",
					"`sealed` variable cannot be downgraded",
				} {
					if !strings.Contains(description, marker) {
						t.Fatalf("update description missing scope-transition contract %q: %q", marker, description)
					}
				}
			}

			parameters := contractSlice(t, operation, "parameters")
			wantParameters := 2
			if expectation.item {
				wantParameters++
			}
			if expectation.mutation {
				wantParameters++
			}
			if expectation.conditional {
				wantParameters++
			}
			if len(parameters) != wantParameters {
				t.Fatalf("parameters = %d, want %d", len(parameters), wantParameters)
			}
			if expectation.mutation {
				idempotencyIndex := len(parameters) - 1
				if expectation.conditional {
					idempotencyIndex--
				}
				idempotency := parameters[idempotencyIndex].(map[string]any)
				if got := idempotency["$ref"]; got != "#/components/parameters/TenantVariableIdempotencyKey" {
					t.Fatalf("idempotency parameter = %v", got)
				}
				if expectation.conditional {
					ifMatch := parameters[len(parameters)-1].(map[string]any)
					if got := ifMatch["$ref"]; got != "#/components/parameters/IfMatch" {
						t.Fatalf("conditional parameter = %v", got)
					}
				}
				requestBody := contractMap(t, operation, "requestBody")
				if required, ok := requestBody["required"].(bool); !ok || !required {
					t.Fatalf("requestBody.required = %v, want true", requestBody["required"])
				}
				content := contractMap(t, requestBody, "content")
				jsonBody := contractMap(t, content, "application/json")
				schema := contractMap(t, jsonBody, "schema")
				if got := schema["$ref"]; got != expectation.requestRef {
					t.Fatalf("request schema = %v, want %s", got, expectation.requestRef)
				}
				for _, marker := range []string{"does not deploy", "restart"} {
					if !strings.Contains(description, marker) {
						t.Fatalf("mutation description does not preserve %q honesty: %q", marker, description)
					}
				}
			}

			responses := contractMap(t, operation, "responses")
			gotStatuses := contractKeySet(responses)
			wantStatuses := contractStringSetAny(expectation.statuses)
			if len(gotStatuses) != len(wantStatuses) {
				t.Fatalf("statuses = %v, want exactly %v", gotStatuses, wantStatuses)
			}
			for status := range wantStatuses {
				if !gotStatuses[status] {
					t.Fatalf("statuses = %v, missing %s", gotStatuses, status)
				}
			}
			forbidden := contractMap(t, responses, "403")
			forbiddenDescription, _ := forbidden["description"].(string)
			if !strings.Contains(strings.ToLower(forbiddenDescription), capability) {
				t.Fatalf("403 description missing exact %s capability: %q", capability, forbiddenDescription)
			}
			if !expectation.mutation {
				unavailable := contractMap(t, responses, "503")
				unavailableDescription, _ := unavailable["description"].(string)
				if strings.Contains(strings.ToLower(unavailableDescription), "audit") {
					t.Fatalf("read-side 503 incorrectly claims an audit dependency: %q", unavailableDescription)
				}
			}
			success := contractMap(t, responses, expectation.success)
			if expectation.name != "delete" {
				content := contractMap(t, success, "content")
				jsonBody := contractMap(t, content, "application/json")
				schema := contractMap(t, jsonBody, "schema")
				if expectation.name == "list" {
					items := contractMap(t, schema, "items")
					if got := items["$ref"]; got != "#/components/schemas/TenantVariableMeta" {
						t.Fatalf("list item schema = %v", got)
					}
				} else if got := schema["$ref"]; got != "#/components/schemas/TenantVariableMeta" {
					t.Fatalf("success schema = %v", got)
				}
			}
			if len(expectation.headers) > 0 {
				headers := contractMap(t, success, "headers")
				if len(headers) != len(expectation.headers) {
					t.Fatalf("headers = %v, want exactly %v", contractKeySet(headers), expectation.headers)
				}
				for _, name := range expectation.headers {
					if _, ok := headers[name]; !ok {
						t.Fatalf("headers = %v, missing %s", contractKeySet(headers), name)
					}
				}
			}
		})
	}

	components := contractMap(t, document, "components")
	parameters := contractMap(t, components, "parameters")
	idempotency := contractMap(t, parameters, "TenantVariableIdempotencyKey")
	if required, ok := idempotency["required"].(bool); !ok || !required {
		t.Fatalf("TenantVariableIdempotencyKey.required = %v, want true", idempotency["required"])
	}
	idempotencyDescription, _ := idempotency["description"].(string)
	for _, marker := range []string{"exactly one", "repeated", "proxy-coalesced", "revalidates"} {
		if !strings.Contains(strings.ToLower(idempotencyDescription), marker) {
			t.Fatalf("idempotency description missing %q: %q", marker, idempotencyDescription)
		}
	}

	schemas := contractMap(t, components, "schemas")
	meta := contractMap(t, schemas, "TenantVariableMeta")
	requireStrictObject(t, meta)
	metaRequired := contractStringSet(contractSlice(t, meta, "required"))
	if !metaRequired["version"] {
		t.Fatal("TenantVariableMeta does not require version")
	}
	metaProperties := contractMap(t, meta, "properties")
	for _, forbidden := range []string{"value", "plaintext", "ciphertext", "wrapped_dek", "kek_id"} {
		if _, ok := metaProperties[forbidden]; ok {
			t.Fatalf("TenantVariableMeta exposes forbidden property %s", forbidden)
		}
	}
	requirePositiveInt64(t, contractMap(t, metaProperties, "version"), "TenantVariableMeta.version")
	metaName := contractMap(t, metaProperties, "name")
	if _, narrowed := metaName["pattern"]; narrowed {
		t.Fatal("TenantVariableMeta.name must retain legacy-name visibility")
	}
	if _, narrowed := metaName["maxLength"]; narrowed {
		t.Fatal("TenantVariableMeta.name must not reject legacy recovery-created names")
	}
	requireStrictObject(t, contractMap(t, schemas, "TenantVariableReference"))

	requestExpectations := []struct {
		name       string
		required   []string
		properties []string
		hasValue   bool
	}{
		{
			name:     "TenantVariableCreateRequest",
			required: []string{"name", "scope", "value"}, properties: []string{"name", "scope", "value"},
			hasValue: true,
		},
		{
			name:     "TenantVariableUpdateRequest",
			required: []string{"expected_version", "value"}, properties: []string{"expected_version", "scope", "value"},
			hasValue: true,
		},
		{
			name:     "TenantVariableDeleteRequest",
			required: []string{"expected_version"}, properties: []string{"expected_version"},
		},
	}
	for _, expectation := range requestExpectations {
		schema := contractMap(t, schemas, expectation.name)
		requireStrictObject(t, schema)
		required := contractStringSet(contractSlice(t, schema, "required"))
		if len(required) != len(expectation.required) {
			t.Fatalf("%s required = %v, want %v", expectation.name, required, expectation.required)
		}
		for _, name := range expectation.required {
			if !required[name] {
				t.Fatalf("%s does not require %s", expectation.name, name)
			}
		}
		properties := contractMap(t, schema, "properties")
		if len(properties) != len(expectation.properties) {
			t.Fatalf("%s properties = %v, want %v", expectation.name, contractKeySet(properties), expectation.properties)
		}
		for _, name := range expectation.properties {
			if _, ok := properties[name]; !ok {
				t.Fatalf("%s properties missing %s", expectation.name, name)
			}
		}
		if expected, ok := properties["expected_version"].(map[string]any); ok {
			requirePositiveInt64(t, expected, expectation.name+".expected_version")
		}
		if expectation.hasValue {
			value := contractMap(t, properties, "value")
			if writeOnly, ok := value["writeOnly"].(bool); !ok || !writeOnly {
				t.Fatalf("%s.value.writeOnly = %v, want true", expectation.name, value["writeOnly"])
			}
		}
	}
	createProperties := contractMap(t, contractMap(t, schemas, "TenantVariableCreateRequest"), "properties")
	createName := contractMap(t, createProperties, "name")
	if got := createName["pattern"]; got != "^[A-Za-z_][A-Za-z0-9_]*$" {
		t.Fatalf("TenantVariableCreateRequest.name.pattern = %v", got)
	}
	if got := createName["maxLength"]; got != 255 {
		t.Fatalf("TenantVariableCreateRequest.name.maxLength = %v, want 255", got)
	}

	// The compatibility routes deliberately retain document-default
	// BearerAuth and their original schemas/operation IDs.
	legacyCollection := contractMap(t, paths, "/variables")
	if got := contractMap(t, legacyCollection, "get")["operationId"]; got != "listVariables" {
		t.Fatalf("legacy GET /variables operationId = %v", got)
	}
	if got := contractMap(t, legacyCollection, "post")["operationId"]; got != "createVariable" {
		t.Fatalf("legacy POST /variables operationId = %v", got)
	}
	for _, method := range []string{"get", "post"} {
		if _, overridden := contractMap(t, legacyCollection, method)["security"]; overridden {
			t.Fatalf("legacy %s /variables must retain document-default BearerAuth", method)
		}
	}
	legacyItem := contractMap(t, paths, "/variables/{id}")
	for method, operationID := range map[string]string{
		"get": "getVariable", "patch": "updateVariable", "delete": "deleteVariable",
	} {
		operation := contractMap(t, legacyItem, method)
		if got := operation["operationId"]; got != operationID {
			t.Fatalf("legacy %s /variables/{id} operationId = %v", method, got)
		}
		if _, overridden := operation["security"]; overridden {
			t.Fatalf("legacy %s /variables/{id} must retain document-default BearerAuth", method)
		}
	}
}

func TestCanonicalTenantGeneratedDomainHumanContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi", "v1alpha", "midnight.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	paths := contractMap(t, document, "paths")
	collection := contractMap(t, paths, "/services/{service_id}/environments/{environment_id}/domains")
	item := contractMap(t, paths, "/services/{service_id}/environments/{environment_id}/domains/{domain_id}")
	operations := []struct {
		name        string
		parent      map[string]any
		method      string
		operation   string
		statuses    []string
		mutation    bool
		item        bool
		requestRef  string
		successes   []string
		headers     []string
		conditional bool
	}{
		{
			name: "list", parent: collection, method: "get",
			operation: "listTenantServiceEnvironmentGeneratedDomains",
			statuses:  []string{"200", "401", "403", "404", "503"},
			successes: []string{"200"},
		},
		{
			name: "create", parent: collection, method: "post",
			operation: "createTenantServiceEnvironmentGeneratedDomain",
			statuses:  []string{"200", "201", "400", "401", "403", "404", "409", "413", "500", "503"},
			mutation:  true, requestRef: "#/components/schemas/TenantGeneratedDomainCreateRequest",
			successes: []string{"200", "201"}, headers: []string{"Location", "Idempotent-Replayed"},
		},
		{
			name: "get", parent: item, method: "get",
			operation: "getTenantServiceEnvironmentGeneratedDomain",
			statuses:  []string{"200", "401", "403", "404", "503"},
			item:      true, successes: []string{"200"},
		},
		{
			name: "delete", parent: item, method: "delete",
			operation: "deleteTenantServiceEnvironmentGeneratedDomain",
			statuses:  []string{"204", "400", "401", "403", "404", "409", "412", "413", "428", "500", "503"},
			mutation:  true, item: true,
			requestRef: "#/components/schemas/TenantGeneratedDomainDeleteRequest",
			successes:  []string{"204"}, headers: []string{"Idempotent-Replayed"}, conditional: true,
		},
	}
	for _, expectation := range operations {
		t.Run(expectation.name, func(t *testing.T) {
			operation := contractMap(t, expectation.parent, expectation.method)
			if got := operation["operationId"]; got != expectation.operation {
				t.Fatalf("operationId = %v, want %s", got, expectation.operation)
			}
			security := contractSlice(t, operation, "security")
			if len(security) != 1 {
				t.Fatalf("security = %#v, want HumanSession only", security)
			}
			requirement := security[0].(map[string]any)
			if len(requirement) != 1 {
				t.Fatalf("security requirement = %#v, want HumanSession only", requirement)
			}
			if _, ok := requirement["HumanSession"]; !ok {
				t.Fatalf("security requirement = %#v, want HumanSession", requirement)
			}
			if _, ok := requirement["BearerAuth"]; ok {
				t.Fatalf("nested generated-domain operation admits recovery BearerAuth: %#v", requirement)
			}

			description := strings.ToLower(operation["description"].(string))
			capability := "domains.read"
			if expectation.mutation {
				capability = "domains.write"
			}
			if !strings.Contains(description, capability) {
				t.Fatalf("description missing exact %s capability: %q", capability, description)
			}
			if !strings.Contains(description, "public dns") {
				t.Fatalf("description does not preserve public-DNS denial: %q", description)
			}
			if expectation.mutation {
				for _, marker := range []string{"4 kib", "does not deploy", "restart"} {
					if !strings.Contains(description, marker) {
						t.Fatalf("mutation description missing %q honesty: %q", marker, description)
					}
				}
			}

			parameters := contractSlice(t, operation, "parameters")
			wantParameters := 2
			if expectation.item {
				wantParameters++
			}
			if expectation.mutation {
				wantParameters++
			}
			if expectation.conditional {
				wantParameters++
			}
			if len(parameters) != wantParameters {
				t.Fatalf("parameters = %d, want %d", len(parameters), wantParameters)
			}
			if expectation.mutation {
				idempotencyIndex := len(parameters) - 1
				if expectation.conditional {
					idempotencyIndex--
				}
				idempotency := parameters[idempotencyIndex].(map[string]any)
				if got := idempotency["$ref"]; got != "#/components/parameters/TenantGeneratedDomainIdempotencyKey" {
					t.Fatalf("idempotency parameter = %v", got)
				}
				if expectation.conditional {
					ifMatch := parameters[len(parameters)-1].(map[string]any)
					if got := ifMatch["$ref"]; got != "#/components/parameters/IfMatch" {
						t.Fatalf("conditional parameter = %v", got)
					}
				}
				requestBody := contractMap(t, operation, "requestBody")
				if required, ok := requestBody["required"].(bool); !ok || !required {
					t.Fatalf("requestBody.required = %v, want true", requestBody["required"])
				}
				content := contractMap(t, requestBody, "content")
				jsonBody := contractMap(t, content, "application/json")
				schema := contractMap(t, jsonBody, "schema")
				if got := schema["$ref"]; got != expectation.requestRef {
					t.Fatalf("request schema = %v, want %s", got, expectation.requestRef)
				}
			}

			responses := contractMap(t, operation, "responses")
			gotStatuses := contractKeySet(responses)
			wantStatuses := contractStringSetAny(expectation.statuses)
			if len(gotStatuses) != len(wantStatuses) {
				t.Fatalf("statuses = %v, want exactly %v", gotStatuses, wantStatuses)
			}
			for status := range wantStatuses {
				if !gotStatuses[status] {
					t.Fatalf("statuses = %v, missing %s", gotStatuses, status)
				}
			}
			forbidden := contractMap(t, responses, "403")
			if !strings.Contains(strings.ToLower(forbidden["description"].(string)), capability) {
				t.Fatalf("403 description missing exact %s capability", capability)
			}
			for _, successStatus := range expectation.successes {
				success := contractMap(t, responses, successStatus)
				if successStatus != "204" {
					content := contractMap(t, success, "content")
					jsonBody := contractMap(t, content, "application/json")
					schema := contractMap(t, jsonBody, "schema")
					if expectation.name == "list" {
						items := contractMap(t, schema, "items")
						if got := items["$ref"]; got != "#/components/schemas/TenantGeneratedDomain" {
							t.Fatalf("list item schema = %v", got)
						}
					} else if got := schema["$ref"]; got != "#/components/schemas/TenantGeneratedDomain" {
						t.Fatalf("success schema = %v", got)
					}
				}
				if len(expectation.headers) > 0 {
					headers := contractMap(t, success, "headers")
					if len(headers) != len(expectation.headers) {
						t.Fatalf("headers = %v, want exactly %v", contractKeySet(headers), expectation.headers)
					}
					for _, name := range expectation.headers {
						if _, ok := headers[name]; !ok {
							t.Fatalf("headers = %v, missing %s", contractKeySet(headers), name)
						}
					}
				}
			}
			if expectation.mutation {
				oversize := contractMap(t, responses, "413")
				if !strings.Contains(strings.ToLower(oversize["description"].(string)), "4 kib") {
					t.Fatalf("413 description does not state the 4 KiB cap: %q", oversize["description"])
				}
			}
		})
	}

	components := contractMap(t, document, "components")
	parameters := contractMap(t, components, "parameters")
	idempotency := contractMap(t, parameters, "TenantGeneratedDomainIdempotencyKey")
	if required, ok := idempotency["required"].(bool); !ok || !required {
		t.Fatalf("TenantGeneratedDomainIdempotencyKey.required = %v, want true", idempotency["required"])
	}
	idempotencyDescription := strings.ToLower(idempotency["description"].(string))
	for _, marker := range []string{"exactly one", "repeated", "proxy-coalesced", "revalidates"} {
		if !strings.Contains(idempotencyDescription, marker) {
			t.Fatalf("idempotency description missing %q: %q", marker, idempotencyDescription)
		}
	}

	schemas := contractMap(t, components, "schemas")
	meta := contractMap(t, schemas, "TenantGeneratedDomain")
	requireStrictObject(t, meta)
	metaRequired := contractStringSet(contractSlice(t, meta, "required"))
	metaProperties := contractMap(t, meta, "properties")
	if len(metaRequired) != 16 || len(metaProperties) != 16 {
		t.Fatalf("TenantGeneratedDomain required=%v properties=%v, want exact 16-field projection", metaRequired, contractKeySet(metaProperties))
	}
	for name := range metaProperties {
		if !metaRequired[name] {
			t.Fatalf("TenantGeneratedDomain property %s is not required", name)
		}
	}
	for _, forbidden := range []string{
		"hostname_input", "tls_desired", "tls_state", "artifact_hash",
		"error_code", "error_detail", "routes", "dns_challenge",
		"certificate", "backend", "service_url",
	} {
		if _, ok := metaProperties[forbidden]; ok {
			t.Fatalf("TenantGeneratedDomain exposes forbidden property %s", forbidden)
		}
	}
	requirePositiveInt64(t, contractMap(t, metaProperties, "version"), "TenantGeneratedDomain.version")
	for property, want := range map[string]any{
		"kind":   "generated",
		"path":   "/",
		"public": true,
		"tls":    false,
	} {
		schema := contractMap(t, metaProperties, property)
		values := contractSlice(t, schema, "enum")
		if len(values) != 1 || values[0] != want {
			t.Fatalf("TenantGeneratedDomain.%s enum = %#v, want [%v]", property, values, want)
		}
	}

	create := contractMap(t, schemas, "TenantGeneratedDomainCreateRequest")
	requireStrictObject(t, create)
	if got := create["maxProperties"]; got != 0 {
		t.Fatalf("TenantGeneratedDomainCreateRequest.maxProperties = %v, want 0", got)
	}
	if properties := contractMap(t, create, "properties"); len(properties) != 0 {
		t.Fatalf("TenantGeneratedDomainCreateRequest properties = %v, want empty", contractKeySet(properties))
	}
	if _, hasRequired := create["required"]; hasRequired {
		t.Fatal("TenantGeneratedDomainCreateRequest must not require caller-owned fields")
	}
	deleteRequest := contractMap(t, schemas, "TenantGeneratedDomainDeleteRequest")
	requireStrictObject(t, deleteRequest)
	deleteRequired := contractStringSet(contractSlice(t, deleteRequest, "required"))
	deleteProperties := contractMap(t, deleteRequest, "properties")
	if len(deleteRequired) != 1 || !deleteRequired["expected_version"] ||
		len(deleteProperties) != 1 {
		t.Fatalf("TenantGeneratedDomainDeleteRequest required=%v properties=%v", deleteRequired, contractKeySet(deleteProperties))
	}
	requirePositiveInt64(t, contractMap(t, deleteProperties, "expected_version"), "TenantGeneratedDomainDeleteRequest.expected_version")

	// The broad flat routes deliberately retain document-default BearerAuth,
	// original operation IDs, and explicit recovery-only descriptions.
	legacyCollection := contractMap(t, paths, "/domains")
	for method, operationID := range map[string]string{"get": "listDomains", "post": "createDomain"} {
		operation := contractMap(t, legacyCollection, method)
		if got := operation["operationId"]; got != operationID {
			t.Fatalf("legacy %s /domains operationId = %v", method, got)
		}
		if _, overridden := operation["security"]; overridden {
			t.Fatalf("legacy %s /domains must retain document-default BearerAuth", method)
		}
		if !strings.Contains(strings.ToLower(operation["summary"].(string)), "recovery compatibility") {
			t.Fatalf("legacy %s /domains is not labeled recovery compatibility", method)
		}
	}
	legacyDelete := contractMap(t, contractMap(t, paths, "/domains/{id}"), "delete")
	if got := legacyDelete["operationId"]; got != "deleteDomain" {
		t.Fatalf("legacy DELETE /domains/{id} operationId = %v", got)
	}
	if _, overridden := legacyDelete["security"]; overridden {
		t.Fatal("legacy DELETE /domains/{id} must retain document-default BearerAuth")
	}
	if !strings.Contains(strings.ToLower(legacyDelete["summary"].(string)), "recovery compatibility") {
		t.Fatal("legacy DELETE /domains/{id} is not labeled recovery compatibility")
	}
}

func contractMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map", key, parent[key])
	}
	return value
}

func contractSlice(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want slice", key, parent[key])
	}
	return value
}

func contractStringSet(values []any) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value.(string)] = true
	}
	return result
}

func contractStringSetAny(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func contractKeySet(values map[string]any) map[string]bool {
	result := make(map[string]bool, len(values))
	for key := range values {
		result[key] = true
	}
	return result
}

func requireStrictObject(t *testing.T, schema map[string]any) {
	t.Helper()
	if got := schema["type"]; got != "object" {
		t.Fatalf("schema type = %v, want object", got)
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("schema additionalProperties = %v, want false", schema["additionalProperties"])
	}
}

func requirePositiveInt64(t *testing.T, schema map[string]any, label string) {
	t.Helper()
	if got := schema["type"]; got != "integer" {
		t.Fatalf("%s.type = %v, want integer", label, got)
	}
	if got := schema["format"]; got != "int64" {
		t.Fatalf("%s.format = %v, want int64", label, got)
	}
	if got := schema["minimum"]; got != 1 {
		t.Fatalf("%s.minimum = %v, want 1", label, got)
	}
}

func TestOperationSecurityRejectsProtectedOperationWithout401(t *testing.T) {
	report := CheckOperationSecurity([]OperationSummary{
		{
			Path:        "/widgets",
			Method:      "post",
			OperationID: "createWidget",
			StatusCodes: []string{"201", "400"},
			Security:    []string{"BearerAuth"},
		},
		{
			Path:        "/health",
			Method:      "get",
			OperationID: "getHealth",
			StatusCodes: []string{"200"},
		},
	})
	if report.Pass() {
		t.Fatal("security report passed despite missing protected 401")
	}
	if report.Protected != 1 ||
		len(report.MissingUnauthorized) != 1 ||
		report.MissingUnauthorized[0].OperationID != "createWidget" {
		t.Fatalf("security report = %+v", report)
	}
	if rendered := report.Render(); !strings.Contains(rendered, "POST /widgets") {
		t.Fatalf("rendered report omitted drift: %s", rendered)
	}
}

func TestRunExitInvocationOnMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"--openapi", "missing.yaml"}, &stdout, &stderr)
	if exit != exitInvocation {
		t.Fatalf("run() exit = %d, want %d (invocation)", exit, exitInvocation)
	}
}

func TestRunExitConformanceOnBadCode(t *testing.T) {
	dir := t.TempDir()
	openapi := filepath.Join(dir, "openapi.yaml")
	registry := filepath.Join(dir, "error-codes.md")
	readme, mvpDoc, mvpRunner := writeMVPReadinessFixtures(t, dir)
	if err := os.WriteFile(openapi, []byte(`paths:
  /backup/runs:
    get:
      operationId: listBackupRuns
      responses:
        '200':
          description: ok
        '400':
          description: missing destination
        '500':
          description: internal
    post:
      operationId: createBackupRun
      responses:
        '201':
          description: created
        '400':
          description: bad request
        '500':
          description: internal
  /backup/restore/status:
    get:
      operationId: getBackupRestoreStatus
      responses:
        '200':
          description: ok
  /backup/restore/plans:
    get:
      operationId: listBackupRestorePlans
      responses:
        '200':
          description: ok
        '400':
          description: missing destination
        '500':
          description: internal
    post:
      operationId: createBackupRestorePlan
      responses:
        '201':
          description: created
        '400':
          description: bad request
        '500':
          description: internal
  /agents/register:
    post:
      operationId: registerAgent
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /agents/{id}/status:
    post:
      operationId: recordAgentStatus
      responses:
        '200':
          description: ok
  /projects:
    get:
      operationId: listProjects
      responses:
        '200':
          description: ok
    post:
      operationId: createProject
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
        '400':
          description: bad request
        '409':
          description: conflict
  /services:
    get:
      operationId: listServices
      responses:
        '200':
          description: ok
    post:
      operationId: createService
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
        '400':
          description: bad request
        '409':
          description: conflict
  /environments/{id}:
    patch:
      operationId: updateEnvironment
      responses:
        '200':
          description: updated
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: operator failure
        '503':
          description: tenant dependency unavailable
  /services/{id}:
    delete:
      operationId: deleteService
      responses:
        '200':
          description: deleted
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '412':
          description: precondition failed
        '428':
          description: precondition required
        '500':
          description: operator failure
        '503':
          description: capability unavailable
  /services/{id}/up:
    post:
      operationId: createServiceUp
      responses:
        '202':
          description: queued
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '415':
          description: wrong media type
        '503':
          description: queue unavailable
        '500':
          description: internal
  /variables:
    get:
      operationId: listVariables
      responses:
        '200':
          description: ok
    post:
      operationId: createVariable
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
  /variables/{id}:
    get:
      operationId: getVariable
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
        '500':
          description: internal
  /services/{id}/deployments:
    get:
      operationId: listServiceDeployments
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
    post:
      operationId: createServiceDeployment
      responses:
        '202':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
        '500':
          description: enqueue failed
  /operations:
    get:
      operationId: listOperations
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /operations/{id}:
    get:
      operationId: getOperation
      responses:
        '200':
          description: ok
        '404':
          description: missing
  /operations/{id}/events:
    get:
      operationId: streamOperationEvents
      responses:
        '200':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
  /operations/{id}/logs:
    get:
      operationId: getOperationLogs
      responses:
        '200':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '500':
          description: internal
  /operations/{id}/cancel:
    post:
      operationId: cancelOperation
      responses:
        '202':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
  /_demo/schema-evidence:
    get:
      operationId: getDemoSchemaEvidence
      responses:
        '200':
          description: ok
  /_demo/migration-plan:
    get:
      operationId: getDemoMigrationPlan
      responses:
        '200':
          description: ok
  /_demo/release-evidence:
    get:
      operationId: getDemoReleaseEvidence
      responses:
        '200':
          description: ok
  /_demo/evidence-index:
    get:
      operationId: getDemoEvidenceIndex
      responses:
        '200':
          description: ok
  /_demo/api-data-readiness:
    get:
      operationId: getDemoAPIDataReadiness
      responses:
        '200':
          description: ok
  /audit/events:
    get:
      operationId: listAuditEvents
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /audit/export:
    get:
      operationId: exportAuditEvents
      responses:
        '200':
          description: ok
  /audit/evidence:
    get:
      operationId: getAuditEvidence
      responses:
        '200':
          description: ok
  /dashboard/runtime-inventory:
    get:
      operationId: getDashboardRuntimeInventory
      responses:
        '200':
          description: ok
  /dashboard/audit-evidence:
    get:
      operationId: getDashboardAuditEvidence
      responses:
        '200':
          description: ok
  /dashboard/template-marketplace:
    get:
      operationId: listDashboardTemplateMarketplace
      responses:
        '200':
          description: ok
  /dashboard/template-deploy:
    post:
      operationId: createDashboardTemplateDeploy
      responses:
        '501':
          description: unavailable
  /services/{id}/deployments/latest-active:
    get:
      operationId: getLatestActiveServiceDeployment
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
  /services/{id}/deployments/{deployment_id}:
    get:
      operationId: getServiceDeployment
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
components:
  schemas:
    Issue:
      properties:
        code:
          type: string
          enum:
            - UNREGISTERED_CODE
`), 0o644); err != nil {
		t.Fatalf("WriteFile(openapi) error = %v", err)
	}
	if err := os.WriteFile(registry, []byte("| `OTHER_CODE` | S08 | n | n |\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(registry) error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"--openapi", openapi,
		"--error-codes", registry,
		"--readme", readme,
		"--mvp-doc", mvpDoc,
		"--mvp-runner", mvpRunner,
	}, &stdout, &stderr)
	if exit != exitNonconformance {
		t.Fatalf("run() exit = %d, want %d (nonconformance); stdout=%s stderr=%s", exit, exitNonconformance, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "UNREGISTERED_CODE") { // not-a-failure-code (test fixture)
		t.Fatalf("stdout missing missing-code entry: %s", stdout.String())
	}
}

func TestRunExitOKOnConformingInput(t *testing.T) {
	dir := t.TempDir()
	openapi := filepath.Join(dir, "openapi.yaml")
	registry := filepath.Join(dir, "error-codes.md")
	readme, mvpDoc, mvpRunner := writeMVPReadinessFixtures(t, dir)
	if err := os.WriteFile(openapi, []byte(`paths:
  /backup/runs:
    get:
      operationId: listBackupRuns
      responses:
        '200':
          description: ok
        '400':
          description: missing destination
        '500':
          description: internal
    post:
      operationId: createBackupRun
      responses:
        '201':
          description: created
        '400':
          description: bad request
        '500':
          description: internal
  /backup/restore/status:
    get:
      operationId: getBackupRestoreStatus
      responses:
        '200':
          description: ok
  /backup/restore/plans:
    get:
      operationId: listBackupRestorePlans
      responses:
        '200':
          description: ok
        '400':
          description: missing destination
        '500':
          description: internal
    post:
      operationId: createBackupRestorePlan
      responses:
        '201':
          description: created
        '400':
          description: bad request
        '500':
          description: internal
  /agents/register:
    post:
      operationId: registerAgent
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /agents/{id}/status:
    post:
      operationId: recordAgentStatus
      responses:
        '200':
          description: ok
  /projects:
    get:
      operationId: listProjects
      responses:
        '200':
          description: ok
    post:
      operationId: createProject
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
  /services:
    get:
      operationId: listServices
      responses:
        '200':
          description: ok
    post:
      operationId: createService
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
  /environments/{id}:
    patch:
      operationId: updateEnvironment
      responses:
        '200':
          description: updated
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: operator failure
        '503':
          description: tenant dependency unavailable
  /services/{id}:
    delete:
      operationId: deleteService
      responses:
        '200':
          description: deleted
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '412':
          description: precondition failed
        '428':
          description: precondition required
        '500':
          description: operator failure
        '503':
          description: capability unavailable
  /services/{id}/up:
    post:
      operationId: createServiceUp
      responses:
        '202':
          description: queued
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '415':
          description: wrong media type
        '503':
          description: queue unavailable
        '500':
          description: internal
  /variables:
    get:
      operationId: listVariables
      responses:
        '200':
          description: ok
    post:
      operationId: createVariable
      responses:
        '201':
          description: ok
        '400':
          description: bad request
        '409':
          description: conflict
  /variables/{id}:
    get:
      operationId: getVariable
      responses:
        '200':
          description: ok
        '404':
          description: missing
  /services/{service_id}/environments/{environment_id}/variables:
    get:
      operationId: listTenantServiceEnvironmentVariables
      responses:
        '200':
          description: ok
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '503':
          description: unavailable
    post:
      operationId: createTenantServiceEnvironmentVariable
      responses:
        '201':
          description: created
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: read failed
        '503':
          description: unavailable
  /services/{service_id}/environments/{environment_id}/variables/{variable_id}:
    get:
      operationId: getTenantServiceEnvironmentVariable
      responses:
        '200':
          description: ok
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '503':
          description: unavailable
    patch:
      operationId: updateTenantServiceEnvironmentVariable
      responses:
        '200':
          description: updated
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: read failed
        '503':
          description: unavailable
    delete:
      operationId: deleteTenantServiceEnvironmentVariable
      responses:
        '204':
          description: deleted
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: read failed
        '503':
          description: unavailable
  /services/{service_id}/environments/{environment_id}/domains:
    get:
      operationId: listTenantServiceEnvironmentGeneratedDomains
      responses:
        '200':
          description: ok
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '503':
          description: unavailable
    post:
      operationId: createTenantServiceEnvironmentGeneratedDomain
      responses:
        '200':
          description: converged
        '201':
          description: created
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: read failed
        '503':
          description: apps domain or durable dependency unavailable
  /services/{service_id}/environments/{environment_id}/domains/{domain_id}:
    get:
      operationId: getTenantServiceEnvironmentGeneratedDomain
      responses:
        '200':
          description: ok
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '503':
          description: unavailable
    delete:
      operationId: deleteTenantServiceEnvironmentGeneratedDomain
      responses:
        '204':
          description: deleted
        '400':
          description: bad request
        '401':
          description: unauthorized
        '403':
          description: forbidden
        '404':
          description: missing
        '409':
          description: conflict
        '413':
          description: too large
        '500':
          description: read failed
        '503':
          description: unavailable
  /services/{id}/deployments:
    get:
      operationId: listServiceDeployments
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
    post:
      operationId: createServiceDeployment
      responses:
        '202':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
        '500':
          description: enqueue failed
  /operations:
    get:
      operationId: listOperations
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /operations/{id}:
    get:
      operationId: getOperation
      responses:
        '200':
          description: ok
        '404':
          description: missing
  /operations/{id}/events:
    get:
      operationId: streamOperationEvents
      responses:
        '200':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
  /operations/{id}/logs:
    get:
      operationId: getOperationLogs
      responses:
        '200':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '500':
          description: internal
  /operations/{id}/cancel:
    post:
      operationId: cancelOperation
      responses:
        '202':
          description: ok
        '400':
          description: bad request
        '404':
          description: missing
        '409':
          description: conflict
  /_demo/schema-evidence:
    get:
      operationId: getDemoSchemaEvidence
      responses:
        '200':
          description: ok
  /_demo/migration-plan:
    get:
      operationId: getDemoMigrationPlan
      responses:
        '200':
          description: ok
  /_demo/release-evidence:
    get:
      operationId: getDemoReleaseEvidence
      responses:
        '200':
          description: ok
  /_demo/evidence-index:
    get:
      operationId: getDemoEvidenceIndex
      responses:
        '200':
          description: ok
  /_demo/api-data-readiness:
    get:
      operationId: getDemoAPIDataReadiness
      responses:
        '200':
          description: ok
  /audit/events:
    get:
      operationId: listAuditEvents
      responses:
        '200':
          description: ok
        '400':
          description: bad request
  /audit/export:
    get:
      operationId: exportAuditEvents
      responses:
        '200':
          description: ok
  /audit/evidence:
    get:
      operationId: getAuditEvidence
      responses:
        '200':
          description: ok
  /dashboard/runtime-inventory:
    get:
      operationId: getDashboardRuntimeInventory
      responses:
        '200':
          description: ok
  /dashboard/audit-evidence:
    get:
      operationId: getDashboardAuditEvidence
      responses:
        '200':
          description: ok
  /dashboard/template-marketplace:
    get:
      operationId: listDashboardTemplateMarketplace
      responses:
        '200':
          description: ok
  /dashboard/template-deploy:
    post:
      operationId: createDashboardTemplateDeploy
      responses:
        '501':
          description: unavailable
  /services/{id}/deployments/latest-active:
    get:
      operationId: getLatestActiveServiceDeployment
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
  /services/{id}/deployments/{deployment_id}:
    get:
      operationId: getServiceDeployment
      responses:
        '200':
          description: ok
        '404':
          description: missing
        '500':
          description: internal
components:
  schemas:
    Issue:
      properties:
        code:
          type: string
          enum:
            - GOOD_CODE
`), 0o644); err != nil {
		t.Fatalf("WriteFile(openapi) error = %v", err)
	}
	if err := os.WriteFile(registry, []byte("| `GOOD_CODE` | S08 | n | n |\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(registry) error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"--openapi", openapi,
		"--error-codes", registry,
		"--readme", readme,
		"--mvp-doc", mvpDoc,
		"--mvp-runner", mvpRunner,
	}, &stdout, &stderr)
	if exit != exitOK {
		t.Fatalf("run() exit = %d, want %d (ok); stdout=%s stderr=%s", exit, exitOK, stdout.String(), stderr.String())
	}
}

func TestWorkflowOperationReportRejectsMissingEndpointAndStatusDrift(t *testing.T) {
	report := CheckWorkflowOperations([]OperationSummary{
		{Path: "/projects", Method: "get", OperationID: "listProjects", StatusCodes: []string{"200"}},
		{Path: "/services/{id}/deployments", Method: "post", OperationID: "createServiceDeployment", StatusCodes: []string{"202"}},
	}, []OperationExpectation{
		{Path: "/projects", Method: "get", OperationID: "listProjects", StatusCodes: []string{"200"}},
		{Path: "/services/{id}/deployments", Method: "post", OperationID: "createServiceDeployment", StatusCodes: []string{"202", "400"}},
		{Path: "/operations/{id}/events", Method: "get", OperationID: "streamOperationEvents", StatusCodes: []string{"200"}},
	})
	if report.Pass() {
		t.Fatal("Pass() = true, want missing workflow endpoint/status drift")
	}
	if len(report.Missing) != 1 || report.Missing[0].OperationID != "streamOperationEvents" {
		t.Fatalf("Missing = %#v, want operation events endpoint", report.Missing)
	}
	if len(report.Drift) != 1 || !contains(report.Drift[0].Missing, "400") {
		t.Fatalf("Drift = %#v, want deploy endpoint missing 400", report.Drift)
	}
	rendered := report.Render()
	if !strings.Contains(rendered, "POST /services/{id}/deployments [createServiceDeployment]") ||
		!strings.Contains(rendered, "GET /operations/{id}/events [streamOperationEvents]") {
		t.Fatalf("rendered workflow report missing actionable entries: %s", rendered)
	}
}

func TestMVPReadinessDocsRequireChecklistAndRunnerModes(t *testing.T) {
	readme := []byte(`# Midnight

## OSS Railway Readiness Checklist

See docs/mvp-acceptance.md and scripts/mvp-acceptance.sh.
`)
	mvpDoc := []byte(`# MVP Acceptance

## Next Acceptance Slice: OSS Railway Happy Path

The slice rejects queued draft/job state, memory-mode diagnostics, and fake/demo-only data.
It requires typed recovery states and Dashboard/API/CLI agree.

## OSS Railway Readiness Checklist

Run local-gate and real-app.
`)
	runner := []byte(`case "$MODE" in
  local-gate) local_gate ;;
  real-app) real_app ;;
esac
`)
	report, err := CheckMVPReadinessDocs(readme, mvpDoc, runner)
	if err != nil {
		t.Fatalf("CheckMVPReadinessDocs() error = %v", err)
	}
	if !report.Pass() {
		t.Fatalf("Pass() = false, report = %+v", report)
	}
}

func TestMVPReadinessDocsRejectModeDrift(t *testing.T) {
	readme := []byte(`# Midnight

## OSS Railway Readiness Checklist

See docs/mvp-acceptance.md and scripts/mvp-acceptance.sh.
`)
	mvpDoc := []byte(`# MVP Acceptance

## Next Acceptance Slice: OSS Railway Happy Path

The slice rejects queued draft/job state, memory-mode diagnostics, and fake/demo-only data.
It requires typed recovery states and Dashboard/API/CLI agree.

## OSS Railway Readiness Checklist

Run local-gate.
`)
	runner := []byte(`case "$MODE" in
  local-gate) local_gate ;;
  real-app) real_app ;;
esac
`)
	report, err := CheckMVPReadinessDocs(readme, mvpDoc, runner)
	if err != nil {
		t.Fatalf("CheckMVPReadinessDocs() error = %v", err)
	}
	if report.Pass() {
		t.Fatal("Pass() = true, want mode drift failure")
	}
	if !contains(report.MissingDocumentedModes, "real-app") {
		t.Fatalf("MissingDocumentedModes = %v, want real-app", report.MissingDocumentedModes)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func writeMVPReadinessFixtures(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	readme := filepath.Join(dir, "README.md")
	mvpDoc := filepath.Join(dir, "mvp-acceptance.md")
	mvpRunner := filepath.Join(dir, "mvp-acceptance.sh")
	if err := os.WriteFile(readme, []byte(`# Midnight

## OSS Railway Readiness Checklist

See docs/mvp-acceptance.md and scripts/mvp-acceptance.sh.
`), 0o644); err != nil {
		t.Fatalf("WriteFile(readme) error = %v", err)
	}
	if err := os.WriteFile(mvpDoc, []byte(`# MVP Acceptance

## Next Acceptance Slice: OSS Railway Happy Path

The slice rejects queued draft/job state, memory-mode diagnostics, and fake/demo-only data.
It requires typed recovery states and Dashboard/API/CLI agree.

## OSS Railway Readiness Checklist

Run local-gate.
`), 0o644); err != nil {
		t.Fatalf("WriteFile(mvpDoc) error = %v", err)
	}
	if err := os.WriteFile(mvpRunner, []byte(`case "$MODE" in
  local-gate) local_gate ;;
esac
`), 0o644); err != nil {
		t.Fatalf("WriteFile(mvpRunner) error = %v", err)
	}
	return readme, mvpDoc, mvpRunner
}
