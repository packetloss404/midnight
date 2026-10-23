package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalGeneratedClientContract(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	if err := verify(paths{
		spec:   filepath.Join(root, "api/openapi/v1alpha/midnight.yaml"),
		types:  filepath.Join(root, "pkg/midnightctl/types.gen.go"),
		client: filepath.Join(root, "pkg/midnightctl/client.gen.go"),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSpecGateRejectsOperationTimestampDrift(t *testing.T) {
	path := filepath.Join("..", "..", "api/openapi/v1alpha/midnight.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(body), "required: [id, state, created_at, updated_at]", "required: [id, state]", 1)
	if err := verifySpec(mutated); err == nil || !strings.Contains(err.Error(), "Operation block") {
		t.Fatalf("verifySpec() error = %v, want operation drift failure", err)
	}
}

func TestSpecGateRejectsUpdatedAtFormatDrift(t *testing.T) {
	path := filepath.Join("..", "..", "api/openapi/v1alpha/midnight.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := yamlBlock(string(body), "    Operation:")
	if !ok {
		t.Fatal("canonical Operation block missing")
	}
	mutatedOperation := strings.Replace(operation, "        updated_at:\n          type: string\n          format: date-time", "        updated_at:\n          type: string", 1)
	if mutatedOperation == operation {
		t.Fatal("test mutation did not change Operation.updated_at")
	}
	mutated := strings.Replace(string(body), operation, mutatedOperation, 1)
	if err := verifySpec(mutated); err == nil || !strings.Contains(err.Error(), "Operation.updated_at") {
		t.Fatalf("verifySpec() error = %v, want updated_at format drift failure", err)
	}
}

func TestGeneratedGateRejectsRequiredFieldTypeDrift(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	typesPath := filepath.Join(root, "pkg/midnightctl/types.gen.go")
	body, err := os.ReadFile(typesPath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(
		string(body),
		"// CreatedAt Immutable creation timestamp used with id as the operation-list keyset order.\n\tCreatedAt time.Time",
		"// CreatedAt Immutable creation timestamp used with id as the operation-list keyset order.\n\tCreatedAt *time.Time",
		1,
	)
	if mutated == string(body) {
		t.Fatal("test mutation did not change generated Operation.CreatedAt")
	}
	temporaryTypes := filepath.Join(t.TempDir(), "types.gen.go")
	if err := os.WriteFile(temporaryTypes, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyGenerated(temporaryTypes, filepath.Join(root, "pkg/midnightctl/client.gen.go"))
	if err == nil || !strings.Contains(err.Error(), "Operation field CreatedAt") {
		t.Fatalf("verifyGenerated() error = %v, want required field type drift failure", err)
	}
}

func TestGeneratedGateRejectsEnumValueDrift(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	typesPath := filepath.Join(root, "pkg/midnightctl/types.gen.go")
	body, err := os.ReadFile(typesPath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(body), `OperationStateQUEUED    OperationState = "QUEUED"`, `OperationStateQUEUED    OperationState = "PENDING"`, 1)
	if mutated == string(body) {
		t.Fatal("test mutation did not change generated OperationStateQUEUED")
	}
	temporaryTypes := filepath.Join(t.TempDir(), "types.gen.go")
	if err := os.WriteFile(temporaryTypes, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyGenerated(temporaryTypes, filepath.Join(root, "pkg/midnightctl/client.gen.go"))
	if err == nil || !strings.Contains(err.Error(), "OperationStateQUEUED") {
		t.Fatalf("verifyGenerated() error = %v, want enum value drift failure", err)
	}
}

func TestGeneratedGateRejectsClientMethodSignatureDrift(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	clientPath := filepath.Join(root, "pkg/midnightctl/client.gen.go")
	body, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(
		string(body),
		"RegisterAgent(ctx context.Context, body RegisterAgentJSONRequestBody, reqEditors ...RequestEditorFn) (*http.Response, error)",
		"RegisterAgent(ctx context.Context, body map[string]interface{}, reqEditors ...RequestEditorFn) (*http.Response, error)",
		1,
	)
	if mutated == string(body) {
		t.Fatal("test mutation did not change generated ClientInterface.RegisterAgent")
	}
	temporaryClient := filepath.Join(t.TempDir(), "client.gen.go")
	if err := os.WriteFile(temporaryClient, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyGenerated(filepath.Join(root, "pkg/midnightctl/types.gen.go"), temporaryClient)
	if err == nil || !strings.Contains(err.Error(), "method RegisterAgent") {
		t.Fatalf("verifyGenerated() error = %v, want method signature drift failure", err)
	}
}
