// api-client-contract is a dependency-free drift gate for the generated Go
// client. It complements the pinned oapi-codegen regeneration check with fast,
// offline assertions over the canonical contract and generated declarations.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type paths struct {
	spec   string
	types  string
	client string
}

func main() {
	check := flag.Bool("check", false, "verify the canonical and generated client contracts")
	flag.Parse()
	if !*check || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/api-client-contract --check")
		os.Exit(64)
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	err = verify(paths{
		spec:   filepath.Join(root, "api/openapi/v1alpha/midnight.yaml"),
		types:  filepath.Join(root, "pkg/midnightctl/types.gen.go"),
		client: filepath.Join(root, "pkg/midnightctl/client.gen.go"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("generated API client contract: ok")
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("api-client-contract: go.mod not found")
		}
		directory = parent
	}
}

func verify(files paths) error {
	spec, err := os.ReadFile(files.spec)
	if err != nil {
		return err
	}
	if err := verifySpec(string(spec)); err != nil {
		return fmt.Errorf("canonical OpenAPI contract: %w", err)
	}
	if err := verifyGenerated(files.types, files.client); err != nil {
		return fmt.Errorf("generated Go client: %w", err)
	}
	return nil
}

func verifySpec(spec string) error {
	checks := []struct {
		name     string
		marker   string
		required []string
	}{
		{"ErrorEnvelope", "    ErrorEnvelope:", []string{"required: [code, message, request_id, docs_url]", "details:", "additionalProperties: true"}},
		{"ErrorResponse", "    ErrorResponse:", []string{"additionalProperties: false", "required: [type, title, status, detail, code, request_id, docs_url, error]", "$ref: '#/components/schemas/ErrorEnvelope'"}},
		{"CursorPage", "    CursorPage:", []string{"required: [next_cursor]", "type: [string, 'null']"}},
		{"OperationState", "    OperationState:", []string{"enum: [QUEUED, RUNNING, SUCCEEDED, FAILED, CANCELED]"}},
		{"Operation", "    Operation:", []string{"required: [id, state, created_at, updated_at]", "$ref: '#/components/schemas/OperationState'", "created_at:", "updated_at:", "format: date-time"}},
		{"OperationListResponse", "    OperationListResponse:", []string{"required: [data, page]", "$ref: '#/components/schemas/Operation'", "$ref: '#/components/schemas/CursorPage'"}},
		{"AgentCredential", "    AgentCredential:", []string{"type: http", "scheme: bearer", "bearerFormat: per-agent secret", "x-credential-class: runtime-agent"}},
		{"registerAgent", "  /agents/register:", []string{"operationId: registerAgent", "- AgentCredential: []", "'401':"}},
		{"recordAgentHeartbeat", "  /agents/{id}/heartbeat:", []string{"operationId: recordAgentHeartbeat", "- AgentCredential: []", "'401':"}},
		{"getAgentDesiredState", "  /agents/{id}/desired:", []string{"operationId: getAgentDesiredState", "- AgentCredential: []", "'401':"}},
		{"recordAgentStatus", "  /agents/{id}/status:", []string{"operationId: recordAgentStatus", "- AgentCredential: []", "'401':"}},
	}
	for _, check := range checks {
		block, ok := yamlBlock(spec, check.marker)
		if !ok {
			return fmt.Errorf("missing %s block", check.name)
		}
		for _, required := range check.required {
			if !strings.Contains(block, required) {
				return fmt.Errorf("%s block missing %q", check.name, required)
			}
		}
	}
	operation, _ := yamlBlock(spec, "    Operation:")
	for _, property := range []string{"created_at", "updated_at"} {
		block, ok := yamlBlock(operation, "        "+property+":")
		if !ok || !strings.Contains(block, "type: string") || !strings.Contains(block, "format: date-time") {
			return fmt.Errorf("Operation.%s must remain a date-time string", property)
		}
	}
	return nil
}

func yamlBlock(document, marker string) (string, bool) {
	lines := strings.Split(document, "\n")
	start := -1
	indent := len(marker) - len(strings.TrimLeft(marker, " "))
	for index, line := range lines {
		if line == marker {
			start = index
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			continue
		}
		lineIndent := len(line) - len(strings.TrimLeft(line, " "))
		if lineIndent <= indent {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

func verifyGenerated(typePath, clientPath string) error {
	typeFile, err := parser.ParseFile(token.NewFileSet(), typePath, nil, 0)
	if err != nil {
		return err
	}
	clientFile, err := parser.ParseFile(token.NewFileSet(), clientPath, nil, 0)
	if err != nil {
		return err
	}
	types := declaredTypes(typeFile)
	for _, name := range []string{"ErrorEnvelope", "ErrorResponse", "CursorPage", "Operation", "OperationListResponse", "OperationState"} {
		if _, ok := types[name]; !ok {
			return fmt.Errorf("missing generated type %s", name)
		}
	}
	structChecks := map[string]map[string]fieldContract{
		"ErrorEnvelope": {
			"Code":      {goType: "string", jsonTag: "code"},
			"Details":   {goType: "*map[string]interface{}", jsonTag: "details,omitempty"},
			"DocsUrl":   {goType: "string", jsonTag: "docs_url"},
			"Message":   {goType: "string", jsonTag: "message"},
			"RequestId": {goType: "string", jsonTag: "request_id"},
		},
		"ErrorResponse": {
			"Code":      {goType: "ErrorResponseCode", jsonTag: "code"},
			"Detail":    {goType: "string", jsonTag: "detail"},
			"Details":   {goType: "*map[string]interface{}", jsonTag: "details,omitempty"},
			"DocsUrl":   {goType: "string", jsonTag: "docs_url"},
			"Error":     {goType: "ErrorEnvelope", jsonTag: "error"},
			"RequestId": {goType: "string", jsonTag: "request_id"},
			"Status":    {goType: "int", jsonTag: "status"},
			"Title":     {goType: "string", jsonTag: "title"},
			"Type":      {goType: "string", jsonTag: "type"},
		},
		"CursorPage": {
			"NextCursor": {goType: "*string", jsonTag: "next_cursor"},
		},
		"Operation": {
			"Code":      {goType: "*string", jsonTag: "code,omitempty"},
			"CreatedAt": {goType: "time.Time", jsonTag: "created_at"},
			"Id":        {goType: "string", jsonTag: "id"},
			"Kind":      {goType: "*string", jsonTag: "kind,omitempty"},
			"Reason":    {goType: "*string", jsonTag: "reason,omitempty"},
			"State":     {goType: "OperationState", jsonTag: "state"},
			"UpdatedAt": {goType: "time.Time", jsonTag: "updated_at"},
		},
		"OperationListResponse": {
			"Data": {goType: "[]Operation", jsonTag: "data"},
			"Page": {goType: "CursorPage", jsonTag: "page"},
		},
	}
	for name, expectedFields := range structChecks {
		declaration := types[name]
		structure, ok := declaration.Type.(*ast.StructType)
		if !ok {
			return fmt.Errorf("generated type %s is not a struct", name)
		}
		fields := structFields(structure)
		for fieldName, expected := range expectedFields {
			actual, ok := fields[fieldName]
			if !ok {
				return fmt.Errorf("generated type %s missing field %s", name, fieldName)
			}
			if actual != expected {
				return fmt.Errorf("generated type %s field %s = %#v, want %#v", name, fieldName, actual, expected)
			}
		}
	}
	operationState := types["OperationState"]
	if got := expressionString(operationState.Type); got != "string" {
		return fmt.Errorf("generated OperationState underlying type = %s, want string", got)
	}
	wantStates := map[string]string{
		"OperationStateCANCELED":  "CANCELED",
		"OperationStateFAILED":    "FAILED",
		"OperationStateQUEUED":    "QUEUED",
		"OperationStateRUNNING":   "RUNNING",
		"OperationStateSUCCEEDED": "SUCCEEDED",
	}
	states := stringConstants(typeFile)
	for name, want := range wantStates {
		if got := states[name]; got != want {
			return fmt.Errorf("generated constant %s = %q, want %q", name, got, want)
		}
	}
	clientTypes := declaredTypes(clientFile)
	methods := map[string]map[string]functionContract{
		"ClientInterface": {
			"RegisterAgent":        {parameters: []string{"context.Context", "RegisterAgentJSONRequestBody", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
			"RecordAgentHeartbeat": {parameters: []string{"context.Context", "string", "RecordAgentHeartbeatJSONRequestBody", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
			"GetAgentDesiredState": {parameters: []string{"context.Context", "string", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
			"RecordAgentStatus":    {parameters: []string{"context.Context", "string", "RecordAgentStatusJSONRequestBody", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
			"ListOperations":       {parameters: []string{"context.Context", "*ListOperationsParams", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
			"GetOperation":         {parameters: []string{"context.Context", "OperationID", "...RequestEditorFn"}, results: []string{"*http.Response", "error"}},
		},
		"ClientWithResponsesInterface": {
			"RegisterAgentWithResponse":        {parameters: []string{"context.Context", "RegisterAgentJSONRequestBody", "...RequestEditorFn"}, results: []string{"*RegisterAgentResponse", "error"}},
			"RecordAgentHeartbeatWithResponse": {parameters: []string{"context.Context", "string", "RecordAgentHeartbeatJSONRequestBody", "...RequestEditorFn"}, results: []string{"*RecordAgentHeartbeatResponse", "error"}},
			"GetAgentDesiredStateWithResponse": {parameters: []string{"context.Context", "string", "...RequestEditorFn"}, results: []string{"*GetAgentDesiredStateResponse", "error"}},
			"RecordAgentStatusWithResponse":    {parameters: []string{"context.Context", "string", "RecordAgentStatusJSONRequestBody", "...RequestEditorFn"}, results: []string{"*RecordAgentStatusResponse", "error"}},
			"ListOperationsWithResponse":       {parameters: []string{"context.Context", "*ListOperationsParams", "...RequestEditorFn"}, results: []string{"*ListOperationsResponse", "error"}},
			"GetOperationWithResponse":         {parameters: []string{"context.Context", "OperationID", "...RequestEditorFn"}, results: []string{"*GetOperationResponse", "error"}},
		},
	}
	for interfaceName, expected := range methods {
		declaration, ok := clientTypes[interfaceName]
		if !ok {
			return fmt.Errorf("missing generated interface %s", interfaceName)
		}
		interfaceType, ok := declaration.Type.(*ast.InterfaceType)
		if !ok {
			return fmt.Errorf("generated type %s is not an interface", interfaceName)
		}
		available := interfaceMethods(interfaceType)
		for method, want := range expected {
			got, ok := available[method]
			if !ok {
				return fmt.Errorf("generated interface %s missing %s", interfaceName, method)
			}
			if !slices.Equal(got.parameters, want.parameters) || !slices.Equal(got.results, want.results) {
				return fmt.Errorf("generated interface %s method %s = %#v, want %#v", interfaceName, method, got, want)
			}
		}
	}
	return nil
}

func declaredTypes(file *ast.File) map[string]*ast.TypeSpec {
	result := make(map[string]*ast.TypeSpec)
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, raw := range generic.Specs {
			if specification, ok := raw.(*ast.TypeSpec); ok {
				result[specification.Name.Name] = specification
			}
		}
	}
	return result
}

type fieldContract struct {
	goType  string
	jsonTag string
}

func structFields(structure *ast.StructType) map[string]fieldContract {
	result := make(map[string]fieldContract)
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 || field.Tag == nil {
			continue
		}
		tag := strings.Trim(field.Tag.Value, "`")
		jsonTag := ""
		if marker := strings.Index(tag, `json:"`); marker >= 0 {
			value := tag[marker+len(`json:"`):]
			if end := strings.IndexByte(value, '"'); end >= 0 {
				jsonTag = value[:end]
			}
		}
		for _, name := range field.Names {
			result[name.Name] = fieldContract{goType: expressionString(field.Type), jsonTag: jsonTag}
		}
	}
	return result
}

func expressionString(expression ast.Expr) string {
	var output strings.Builder
	if err := printer.Fprint(&output, token.NewFileSet(), expression); err != nil {
		return "<invalid>"
	}
	return output.String()
}

func stringConstants(file *ast.File) map[string]string {
	result := make(map[string]string)
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.CONST {
			continue
		}
		for _, raw := range generic.Specs {
			specification, ok := raw.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range specification.Names {
				if index >= len(specification.Values) {
					continue
				}
				literal, ok := specification.Values[index].(*ast.BasicLit)
				if ok && literal.Kind == token.STRING {
					result[name.Name] = strings.Trim(literal.Value, `"`)
				}
			}
		}
	}
	return result
}

type functionContract struct {
	parameters []string
	results    []string
}

func interfaceMethods(value *ast.InterfaceType) map[string]functionContract {
	result := make(map[string]functionContract)
	for _, field := range value.Methods.List {
		function, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		contract := functionContract{parameters: fieldTypes(function.Params), results: fieldTypes(function.Results)}
		for _, name := range field.Names {
			result[name.Name] = contract
		}
	}
	return result
}

func fieldTypes(list *ast.FieldList) []string {
	if list == nil {
		return nil
	}
	result := make([]string, 0, len(list.List))
	for _, field := range list.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			result = append(result, expressionString(field.Type))
		}
	}
	return result
}
