// Command api-v1-graduation reports whether the canonical v1alpha OpenAPI
// contract satisfies Midnight's machine-checkable stable-v1 promotion rules.
// Reporting is non-blocking by default; --require-ready turns blockers into a
// non-zero conformance exit for release automation.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"gopkg.in/yaml.v3"
)

const (
	defaultSourcePath   = "api/openapi/v1alpha/midnight.yaml"
	defaultManifestPath = "api/openapi/v1alpha/audiences.yaml"
	defaultViewsPath    = "api/openapi/v1alpha/views"
	defaultHandlersPath = "internal/api"
	reportSchemaVersion = "midnight.api.v1-graduation.v1"

	exitOK         = 0
	exitInvocation = 2
	exitBlockers   = 3
)

var operationMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

var mutationMethods = map[string]bool{
	"put": true, "post": true, "delete": true, "patch": true,
}

type report struct {
	SchemaVersion           string  `json:"schema_version" yaml:"schema_version"`
	SourceContract          string  `json:"source_contract" yaml:"source_contract"`
	Ready                   bool    `json:"ready" yaml:"ready"`
	OperationCount          int     `json:"operation_count" yaml:"operation_count"`
	CanonicalOperationCount int     `json:"canonical_operation_count" yaml:"canonical_operation_count"`
	ExcludedOperationCount  int     `json:"excluded_operation_count" yaml:"excluded_operation_count"`
	BlockerCount            int     `json:"blocker_count" yaml:"blocker_count"`
	Checks                  []check `json:"checks" yaml:"checks"`
}

type check struct {
	ID       string    `json:"id" yaml:"id"`
	Title    string    `json:"title" yaml:"title"`
	Status   string    `json:"status" yaml:"status"`
	Blockers int       `json:"blockers" yaml:"blockers"`
	Findings []finding `json:"findings,omitempty" yaml:"findings,omitempty"`
}

type finding struct {
	Location    string `json:"location" yaml:"location"`
	Path        string `json:"path,omitempty" yaml:"path,omitempty"`
	Method      string `json:"method,omitempty" yaml:"method,omitempty"`
	OperationID string `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	Message     string `json:"message" yaml:"message"`
}

type operation struct {
	Path        string
	Method      string
	OperationID string
	Node        *yaml.Node
	PathItem    *yaml.Node
}

type audienceManifest struct {
	Audiences map[string]audience `yaml:"audiences"`
}

type audience struct {
	Description  string   `yaml:"description"`
	PathPrefixes []string `yaml:"path_prefixes"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("api-v1-graduation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sourcePath := flags.String("source", defaultSourcePath, "canonical v1alpha OpenAPI document")
	manifestPath := flags.String("audiences", defaultManifestPath, "audience-view manifest")
	viewsPath := flags.String("views", defaultViewsPath, "generated audience-view directory")
	handlersPath := flags.String("handler-source", defaultHandlersPath, "Go API handler source directory")
	format := flags.String("format", "text", "report format: text or json")
	requireReady := flags.Bool("require-ready", false, "exit non-zero when graduation blockers remain")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "api-v1-graduation: %v\n", err)
		return exitInvocation
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "api-v1-graduation: unsupported format %q\n", *format)
		return exitInvocation
	}

	result, err := evaluate(*sourcePath, *manifestPath, *viewsPath, *handlersPath)
	if err != nil {
		fmt.Fprintf(stderr, "api-v1-graduation: %v\n", err)
		return exitInvocation
	}
	if *format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "api-v1-graduation: encode report: %v\n", err)
			return exitInvocation
		}
	} else {
		fmt.Fprint(stdout, renderText(result))
	}
	if *requireReady && !result.Ready {
		return exitBlockers
	}
	return exitOK
}

func evaluate(sourcePath, manifestPath, viewsPath, handlersPath string) (report, error) {
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return report{}, fmt.Errorf("read source contract %s: %w", sourcePath, err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return report{}, fmt.Errorf("read audience manifest %s: %w", manifestPath, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return report{}, fmt.Errorf("decode source contract: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return report{}, fmt.Errorf("source contract root must be a mapping")
	}
	var manifest audienceManifest
	if err := yaml.Unmarshal(manifestBytes, &manifest); err != nil {
		return report{}, fmt.Errorf("decode audience manifest: %w", err)
	}
	if len(manifest.Audiences) == 0 {
		return report{}, fmt.Errorf("audience manifest is empty")
	}

	canonicalOperations, err := enumerateOperations(document.Content[0])
	if err != nil {
		return report{}, err
	}
	operations := promotedOperations(canonicalOperations)
	checks := []check{
		checkStableCandidateScope(canonicalOperations),
		checkNoNotImplemented(operations),
		checkOperationTags(operations),
		checkMutationExamples(document.Content[0], operations),
		checkConditionalMutations(document.Content[0], operations),
		checkCompatibilityEnvelope(document.Content[0]),
		checkProblemTypeRegistry(document.Content[0]),
		checkBoundedJSONBoundary(handlersPath),
		checkGeneratedViews(source, manifest, viewsPath),
	}
	blockers := 0
	for index := range checks {
		sortFindings(checks[index].Findings)
		checks[index].Blockers = len(checks[index].Findings)
		if checks[index].Blockers == 0 {
			checks[index].Status = "pass"
		} else {
			checks[index].Status = "fail"
		}
		blockers += checks[index].Blockers
	}
	return report{
		SchemaVersion:           reportSchemaVersion,
		SourceContract:          filepath.ToSlash(sourcePath),
		Ready:                   blockers == 0,
		OperationCount:          len(operations),
		CanonicalOperationCount: len(canonicalOperations),
		ExcludedOperationCount:  len(canonicalOperations) - len(operations),
		BlockerCount:            blockers,
		Checks:                  checks,
	}, nil
}

func promotedOperations(operations []operation) []operation {
	promoted := make([]operation, 0, len(operations))
	for _, operation := range operations {
		if scalarValue(mappingValue(operation.Node, "x-stability")) != "alpha" {
			promoted = append(promoted, operation)
		}
	}
	return promoted
}

func checkStableCandidateScope(operations []operation) check {
	result := check{ID: "stable-candidate-scope", Title: "Every stable-v1 exclusion is explicit and justified"}
	for _, operation := range operations {
		stability := scalarValue(mappingValue(operation.Node, "x-stability"))
		switch stability {
		case "":
			continue
		case "alpha":
			if strings.TrimSpace(scalarValue(mappingValue(operation.Node, "x-stability-reason"))) == "" {
				result.Findings = append(result.Findings, operationFinding(operation,
					operationLocation(operation)+".x-stability-reason",
					"alpha operation has no concrete reason for exclusion from stable v1"))
			}
		default:
			result.Findings = append(result.Findings, operationFinding(operation,
				operationLocation(operation)+".x-stability",
				"x-stability must be omitted for promoted operations or equal alpha"))
		}
	}
	return result
}

func enumerateOperations(root *yaml.Node) ([]operation, error) {
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("source contract has no paths mapping")
	}
	var operations []operation
	for pathIndex := 0; pathIndex+1 < len(paths.Content); pathIndex += 2 {
		path := paths.Content[pathIndex].Value
		pathItem := paths.Content[pathIndex+1]
		if pathItem.Kind != yaml.MappingNode {
			continue
		}
		for operationIndex := 0; operationIndex+1 < len(pathItem.Content); operationIndex += 2 {
			method := strings.ToLower(pathItem.Content[operationIndex].Value)
			if !operationMethods[method] {
				continue
			}
			node := pathItem.Content[operationIndex+1]
			operations = append(operations, operation{
				Path:        path,
				Method:      method,
				OperationID: scalarValue(mappingValue(node, "operationId")),
				Node:        node,
				PathItem:    pathItem,
			})
		}
	}
	return operations, nil
}

func checkNoNotImplemented(operations []operation) check {
	result := check{ID: "no-501-responses", Title: "Stable operations have no advertised 501 response"}
	for _, operation := range operations {
		if scalarValue(mappingValue(operation.Node, "x-stability")) == "alpha" {
			continue
		}
		responses := mappingValue(operation.Node, "responses")
		if mappingValue(responses, "501") != nil {
			result.Findings = append(result.Findings, operationFinding(
				operation,
				operationLocation(operation)+".responses.501",
				"operation advertises NOT_IMPLEMENTED and is not stable-v1 ready",
			))
		}
	}
	return result
}

func checkOperationTags(operations []operation) check {
	result := check{ID: "operation-tags", Title: "Every operation has at least one audience/resource tag"}
	for _, operation := range operations {
		tags := mappingValue(operation.Node, "tags")
		if tags == nil || tags.Kind != yaml.SequenceNode || len(tags.Content) == 0 {
			result.Findings = append(result.Findings, operationFinding(
				operation,
				operationLocation(operation)+".tags",
				"operation has no tags",
			))
		}
	}
	return result
}

func checkMutationExamples(root *yaml.Node, operations []operation) check {
	result := check{ID: "mutation-examples", Title: "Mutating request and success-response schemas have representation examples"}
	for _, operation := range operations {
		if !mutationMethods[operation.Method] {
			continue
		}
		requestBody := resolveLocalReference(root, mappingValue(operation.Node, "requestBody"))
		checkContentExamples(root, requestBody, operation, operationLocation(operation)+".requestBody", "mutating request", &result)

		responses := mappingValue(operation.Node, "responses")
		if responses == nil || responses.Kind != yaml.MappingNode {
			continue
		}
		for index := 0; index+1 < len(responses.Content); index += 2 {
			status := responses.Content[index].Value
			if !isSuccessStatus(status) {
				continue
			}
			response := resolveLocalReference(root, responses.Content[index+1])
			checkContentExamples(root, response, operation, operationLocation(operation)+".responses."+status, "mutating success response", &result)
		}
	}
	return result
}

func checkContentExamples(
	root, owner *yaml.Node,
	operation operation,
	location, label string,
	result *check,
) {
	content := mappingValue(owner, "content")
	if content == nil || content.Kind != yaml.MappingNode {
		return
	}
	for index := 0; index+1 < len(content.Content); index += 2 {
		mediaType := content.Content[index].Value
		media := content.Content[index+1]
		schema := mappingValue(media, "schema")
		if schema == nil {
			continue
		}
		if hasExample(media) || schemaHasExample(root, schema, map[*yaml.Node]bool{}) {
			continue
		}
		result.Findings = append(result.Findings, operationFinding(
			operation,
			location+".content."+mediaType,
			label+" schema has no example or examples",
		))
	}
}

func schemaHasExample(root, schema *yaml.Node, visited map[*yaml.Node]bool) bool {
	if schema == nil || visited[schema] {
		return false
	}
	visited[schema] = true
	if hasExample(schema) {
		return true
	}
	resolved := resolveLocalReference(root, schema)
	return resolved != schema && schemaHasExample(root, resolved, visited)
}

func hasExample(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	if mappingValue(node, "example") != nil {
		return true
	}
	examples := mappingValue(node, "examples")
	return examples != nil && ((examples.Kind == yaml.MappingNode || examples.Kind == yaml.SequenceNode) && len(examples.Content) > 0)
}

func checkCompatibilityEnvelope(root *yaml.Node) check {
	result := check{ID: "compatibility-error-envelope", Title: "Stable projection omits the v1alpha nested error compatibility envelope"}
	errorSchema := localReference(root, "#/components/schemas/ErrorResponse")
	if errorSchema == nil {
		result.Findings = append(result.Findings, finding{
			Location: "components.schemas.ErrorResponse",
			Message:  "canonical ErrorResponse schema is missing",
		})
		return result
	}
	properties := mappingValue(errorSchema, "properties")
	required := mappingValue(errorSchema, "required")
	compatibilityProperty := mappingValue(properties, "error")
	if compatibilityProperty != nil && scalarBool(mappingValue(compatibilityProperty, "deprecated")) &&
		scalarBool(mappingValue(compatibilityProperty, "x-stable-v1-omit")) {
		return result
	}
	if compatibilityProperty != nil || sequenceContains(required, "error") {
		result.Findings = append(result.Findings, finding{
			Location: "components.schemas.ErrorResponse.properties.error",
			Message:  "v1alpha compatibility envelope is not marked deprecated and x-stable-v1-omit for the generated stable projection",
		})
	}
	return result
}

func checkProblemTypeRegistry(root *yaml.Node) check {
	result := check{
		ID:    "problem-type-registry",
		Title: "RFC 9457 problem types use published URIs with stable title/status metadata",
	}
	registry := mappingValue(root, "x-problem-types")
	if registry == nil || registry.Kind != yaml.MappingNode || len(registry.Content) == 0 {
		result.Findings = append(result.Findings, finding{
			Location: "x-problem-types",
			Message:  "canonical contract has no problem-type registry; stable v1 must publish each code's type URI, title, and status",
		})
		return result
	}
	expected := make(map[string]failurecodes.ProblemType)
	for _, definition := range failurecodes.APIProblemTypes() {
		expected[string(definition.Code)] = definition
	}
	registryCodes := make(map[string]bool, len(registry.Content)/2)
	for index := 0; index+1 < len(registry.Content); index += 2 {
		code := registry.Content[index].Value
		registryCodes[code] = true
		entry := registry.Content[index+1]
		location := "x-problem-types." + code
		definition, known := expected[code]
		if !known {
			result.Findings = append(result.Findings, finding{
				Location: location,
				Message:  "problem-type entry is not registered by the runtime HTTP problem taxonomy",
			})
		}
		typeURI := scalarValue(mappingValue(entry, "type"))
		if !publicProblemTypeURI(typeURI) {
			result.Findings = append(result.Findings, finding{
				Location: location + ".type",
				Message:  "problem type must be about:blank or a public canonical HTTPS URI; .local and relative URIs are not stable",
			})
		}
		if strings.TrimSpace(scalarValue(mappingValue(entry, "title"))) == "" {
			result.Findings = append(result.Findings, finding{
				Location: location + ".title",
				Message:  "problem type has no stable title",
			})
		}
		status := scalarInt(mappingValue(entry, "status"))
		if status < 400 || status > 599 {
			result.Findings = append(result.Findings, finding{
				Location: location + ".status",
				Message:  "problem type has no stable HTTP error status",
			})
		}
		statuses := intSequence(mappingValue(entry, "statuses"))
		if !strictlyIncreasing(statuses) || !containsInt(statuses, status) {
			result.Findings = append(result.Findings, finding{
				Location: location + ".statuses",
				Message:  "runtime-permitted statuses must be a sorted unique list containing the recommended status",
			})
		}
		if known {
			if typeURI != definition.Type || scalarValue(mappingValue(entry, "title")) != definition.Title ||
				status != definition.Status || !equalInts(statuses, definition.Statuses) {
				result.Findings = append(result.Findings, finding{
					Location: location,
					Message:  "problem metadata differs from the runtime HTTP problem taxonomy",
				})
			}
			delete(expected, code)
		}
	}
	for code := range expected {
		result.Findings = append(result.Findings, finding{
			Location: "x-problem-types." + code,
			Message:  "runtime HTTP problem code has no canonical OpenAPI registry entry",
		})
	}
	errorSchema := localReference(root, "#/components/schemas/ErrorResponse")
	codeSchema := mappingValue(mappingValue(errorSchema, "properties"), "code")
	codeEnum := mappingValue(codeSchema, "enum")
	if codeEnum == nil || codeEnum.Kind != yaml.SequenceNode || len(codeEnum.Content) == 0 {
		result.Findings = append(result.Findings, finding{
			Location: "components.schemas.ErrorResponse.properties.code.enum",
			Message:  "problem code schema has no closed stable enum binding responses to x-problem-types",
		})
		return result
	}
	for _, codeNode := range codeEnum.Content {
		if !registryCodes[codeNode.Value] {
			result.Findings = append(result.Findings, finding{
				Location: "x-problem-types." + codeNode.Value,
				Message:  "stable problem code has no published type, title, and status entry",
			})
		}
		delete(registryCodes, codeNode.Value)
	}
	for code := range registryCodes {
		result.Findings = append(result.Findings, finding{
			Location: "x-problem-types." + code,
			Message:  "problem-type entry is not included in the stable ErrorResponse code enum",
		})
	}
	return result
}

func intSequence(node *yaml.Node) []int {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	values := make([]int, 0, len(node.Content))
	for _, item := range node.Content {
		values = append(values, scalarInt(item))
	}
	return values
}

func strictlyIncreasing(values []int) bool {
	if len(values) == 0 {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func containsInt(values []int, value int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func publicProblemTypeURI(value string) bool {
	if value == "about:blank" {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host != "localhost" && !strings.HasSuffix(host, ".local")
}

func checkBoundedJSONBoundary(handlerPath string) check {
	result := check{
		ID:    "bounded-json-boundary",
		Title: "JSON handlers use the shared bounded exact-one-document decoder",
	}
	files, err := filepath.Glob(filepath.Join(handlerPath, "*.go"))
	if err != nil {
		result.Findings = append(result.Findings, finding{Location: filepath.ToSlash(handlerPath), Message: "enumerate handler source: " + err.Error()})
		return result
	}
	if len(files) == 0 {
		result.Findings = append(result.Findings, finding{Location: filepath.ToSlash(handlerPath), Message: "no Go handler sources found"})
		return result
	}
	fileSet := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "request_json.go" {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			result.Findings = append(result.Findings, finding{Location: filepath.ToSlash(path), Message: "parse handler source: " + parseErr.Error()})
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || !directRequestBody(call.Args[0]) {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			message := ""
			switch packageName.Name + "." + selector.Sel.Name {
			case "json.NewDecoder":
				message = "direct json.NewDecoder(request.Body) bypasses the shared exact-one-document decoder"
			case "io.ReadAll":
				message = "direct io.ReadAll(request.Body) bypasses the shared request-size bound"
			}
			if message == "" {
				return true
			}
			position := fileSet.Position(call.Pos())
			result.Findings = append(result.Findings, finding{
				Location: filepath.ToSlash(path) + ":" + strconv.Itoa(position.Line),
				Message:  message,
			})
			return true
		})
	}
	return result
}

func directRequestBody(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Body"
}

func checkConditionalMutations(root *yaml.Node, operations []operation) check {
	result := check{
		ID:    "conditional-mutations",
		Title: "Replace, update, and delete operations use strong ETag and If-Match preconditions",
	}
	reads := make(map[string]operation)
	for _, operation := range operations {
		if operation.Method == "get" {
			reads[operation.Path] = operation
		}
	}
	for _, operation := range operations {
		if operation.Method != "put" && operation.Method != "patch" && operation.Method != "delete" {
			continue
		}
		if !hasRequiredHeaderParameter(root, operation, "If-Match") {
			message := "operation does not require the If-Match conditional request header"
			if mutationUsesExpectedVersion(root, operation) {
				message += "; body expected_version is v1alpha compatibility debt, not a stable-v1 precondition"
			}
			result.Findings = append(result.Findings, operationFinding(
				operation,
				operationLocation(operation)+".parameters.If-Match",
				message,
			))
		}
		responses := mappingValue(operation.Node, "responses")
		for _, requiredStatus := range []string{"412", "428"} {
			if mappingValue(responses, requiredStatus) == nil {
				result.Findings = append(result.Findings, operationFinding(
					operation,
					operationLocation(operation)+".responses."+requiredStatus,
					"conditional mutation does not document HTTP "+requiredStatus,
				))
			}
		}
		read, ok := reads[operation.Path]
		if !ok {
			result.Findings = append(result.Findings, operationFinding(
				operation,
				"paths."+operation.Path+".get",
				"conditional mutation has no same-resource GET that supplies the current strong ETag",
			))
			continue
		}
		if !operationHasStrongETag(root, read) {
			result.Findings = append(result.Findings, operationFinding(
				operation,
				operationLocation(read)+".responses.2xx.headers.ETag",
				"same-resource GET does not document a provably strong ETag",
			))
		}
	}
	return result
}

func hasRequiredHeaderParameter(root *yaml.Node, operation operation, name string) bool {
	for _, parameters := range []*yaml.Node{
		mappingValue(operation.PathItem, "parameters"),
		mappingValue(operation.Node, "parameters"),
	} {
		if parameters == nil || parameters.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range parameters.Content {
			parameter := resolveLocalReference(root, item)
			if !strings.EqualFold(scalarValue(mappingValue(parameter, "name")), name) ||
				scalarValue(mappingValue(parameter, "in")) != "header" {
				continue
			}
			return scalarBool(mappingValue(parameter, "required"))
		}
	}
	return false
}

func mutationUsesExpectedVersion(root *yaml.Node, operation operation) bool {
	requestBody := resolveLocalReference(root, mappingValue(operation.Node, "requestBody"))
	content := mappingValue(requestBody, "content")
	if content == nil || content.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(content.Content); index += 2 {
		schema := resolveLocalReference(root, mappingValue(content.Content[index+1], "schema"))
		if mappingValue(mappingValue(schema, "properties"), "expected_version") != nil ||
			sequenceContains(mappingValue(schema, "required"), "expected_version") {
			return true
		}
	}
	return false
}

func operationHasStrongETag(root *yaml.Node, operation operation) bool {
	responses := mappingValue(operation.Node, "responses")
	if responses == nil || responses.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(responses.Content); index += 2 {
		if !isSuccessStatus(responses.Content[index].Value) {
			continue
		}
		response := resolveLocalReference(root, responses.Content[index+1])
		headers := mappingValue(response, "headers")
		etag := resolveLocalReference(root, mappingValueCaseInsensitive(headers, "ETag"))
		if strongETagHeader(etag) {
			return true
		}
	}
	return false
}

func strongETagHeader(header *yaml.Node) bool {
	if header == nil {
		return false
	}
	if strings.EqualFold(scalarValue(mappingValue(header, "x-etag-strength")), "strong") {
		return true
	}
	schema := mappingValue(header, "schema")
	pattern := scalarValue(mappingValue(schema, "pattern"))
	if strings.HasPrefix(pattern, `^"`) && !strings.Contains(pattern, "W/") {
		return true
	}
	example := scalarValue(mappingValue(schema, "example"))
	return strings.HasPrefix(example, `"`) && !strings.HasPrefix(example, `W/`)
}

func checkGeneratedViews(source []byte, manifest audienceManifest, viewsPath string) check {
	result := check{ID: "generated-view-freshness", Title: "Audience-specific generated views match the canonical contract"}
	names := make([]string, 0, len(manifest.Audiences))
	for name := range manifest.Audiences {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		expected, err := renderView(source, name, manifest.Audiences[name])
		path := filepath.Join(viewsPath, name+".openapi.yaml")
		location := filepath.ToSlash(path)
		if err != nil {
			result.Findings = append(result.Findings, finding{Location: location, Message: err.Error()})
			continue
		}
		current, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			result.Findings = append(result.Findings, finding{Location: location, Message: "generated audience view is missing"})
		case err != nil:
			result.Findings = append(result.Findings, finding{Location: location, Message: "read generated audience view: " + err.Error()})
		case !bytes.Equal(current, expected):
			result.Findings = append(result.Findings, finding{Location: location, Message: "generated audience view is stale"})
		}
	}
	return result
}

func renderView(source []byte, name string, config audience) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("decode canonical OpenAPI: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("canonical OpenAPI root is invalid")
	}
	root := document.Content[0]
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("canonical OpenAPI has no paths map")
	}
	filtered := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for index := 0; index+1 < len(paths.Content); index += 2 {
		path := paths.Content[index].Value
		if matchesAnyPrefix(path, config.PathPrefixes) {
			filtered.Content = append(filtered.Content, paths.Content[index], paths.Content[index+1])
		}
	}
	if len(filtered.Content) == 0 {
		return nil, fmt.Errorf("audience %q matched no paths", name)
	}
	replaceMappingValue(root, "paths", filtered)
	setScalar(root, "x-generated-audience", name)
	setScalar(root, "x-generated-description", config.Description)

	var buffer bytes.Buffer
	buffer.WriteString("# Code generated by tools/api-contract-views; DO NOT EDIT.\n")
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, fmt.Errorf("encode %s view: %w", name, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func resolveLocalReference(root, node *yaml.Node) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return node
	}
	reference := scalarValue(mappingValue(node, "$ref"))
	if reference == "" {
		return node
	}
	resolved := localReference(root, reference)
	if resolved == nil {
		return node
	}
	return resolved
}

func localReference(root *yaml.Node, reference string) *yaml.Node {
	if !strings.HasPrefix(reference, "#/") {
		return nil
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		current = mappingValue(current, part)
		if current == nil {
			return nil
		}
	}
	return current
}

func operationFinding(operation operation, location, message string) finding {
	return finding{
		Location: location, Path: operation.Path, Method: strings.ToUpper(operation.Method),
		OperationID: operation.OperationID, Message: message,
	}
}

func operationLocation(operation operation) string {
	return "paths." + operation.Path + "." + operation.Method
}

func isSuccessStatus(status string) bool {
	if len(status) != 3 || status[0] != '2' {
		return false
	}
	_, err := strconv.Atoi(status)
	return err == nil
}

func sequenceContains(sequence *yaml.Node, value string) bool {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range sequence.Content {
		if item.Value == value {
			return true
		}
	}
	return false
}

func matchesAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func mappingValueCaseInsensitive(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if strings.EqualFold(mapping.Content[index].Value, key) {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func replaceMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value,
	)
}

func setScalar(mapping *yaml.Node, key, value string) {
	replaceMappingValue(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func scalarBool(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.ScalarNode {
		return false
	}
	value, err := strconv.ParseBool(node.Value)
	return err == nil && value
}

func scalarInt(node *yaml.Node) int {
	if node == nil || node.Kind != yaml.ScalarNode {
		return 0
	}
	value, err := strconv.Atoi(node.Value)
	if err != nil {
		return 0
	}
	return value
}

func sortFindings(findings []finding) {
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].Location != findings[right].Location {
			return findings[left].Location < findings[right].Location
		}
		return findings[left].Message < findings[right].Message
	})
}

func renderText(result report) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Stable v1 graduation: ready=%t blockers=%d promoted_operations=%d canonical_operations=%d excluded_operations=%d\n",
		result.Ready, result.BlockerCount, result.OperationCount, result.CanonicalOperationCount, result.ExcludedOperationCount)
	fmt.Fprintf(&output, "Source: %s\n", result.SourceContract)
	for _, check := range result.Checks {
		fmt.Fprintf(&output, "[%s] %s (%d blockers)\n", strings.ToUpper(check.Status), check.Title, check.Blockers)
		for _, finding := range check.Findings {
			fmt.Fprintf(&output, "  - %s: %s\n", finding.Location, finding.Message)
		}
	}
	return output.String()
}
