// Command api-contract-metadata maintains deterministic tags and representative
// examples on the canonical OpenAPI operations. It edits only missing metadata
// and preserves the hand-authored YAML layout.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/midnight/midnight/internal/failurecodes"
	"gopkg.in/yaml.v3"
)

var operationMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

var mutationMethods = map[string]bool{"put": true, "post": true, "delete": true, "patch": true}

type audienceManifest struct {
	Audiences map[string]struct {
		PathPrefixes []string `yaml:"path_prefixes"`
	} `yaml:"audiences"`
}

type edit struct {
	line    int
	endLine int
	indent  int
	text    string
	mode    string
}

func main() {
	var sourcePath, manifestPath string
	var check bool
	flag.StringVar(&sourcePath, "source", "api/openapi/v1alpha/midnight.yaml", "canonical OpenAPI document")
	flag.StringVar(&manifestPath, "manifest", "api/openapi/v1alpha/audiences.yaml", "audience manifest")
	flag.BoolVar(&check, "check", false, "fail when deterministic metadata is missing")
	flag.Parse()
	if err := run(sourcePath, manifestPath, check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sourcePath, manifestPath string, check bool) error {
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return fmt.Errorf("decode source: %w", err)
	}
	var manifest audienceManifest
	if err := yaml.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if len(document.Content) != 1 || len(manifest.Audiences) == 0 {
		return fmt.Errorf("canonical document or audience manifest is empty")
	}
	edits, err := metadataEdits(document.Content[0], manifest, source)
	if err != nil {
		return err
	}
	if registryEdits := problemRegistryEdits(document.Content[0], source); len(registryEdits) > 0 {
		edits = append(edits, registryEdits...)
	}
	if len(edits) == 0 {
		return nil
	}
	if check {
		return fmt.Errorf("canonical OpenAPI enrichment is stale: %d deterministic edits missing", len(edits))
	}
	updated, err := applyEdits(source, edits)
	if err != nil {
		return err
	}
	if err := os.WriteFile(sourcePath, updated, 0o644); err != nil {
		return fmt.Errorf("write source: %w", err)
	}
	return nil
}

func problemRegistryEdits(root *yaml.Node, source []byte) []edit {
	registry := mappingValue(root, "x-problem-types")
	errorSchema := resolveLocalReference(root, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "$ref"}, {Kind: yaml.ScalarNode, Value: "#/components/schemas/ErrorResponse"},
	}})
	codeSchema := mappingValue(mappingValue(errorSchema, "properties"), "code")
	// Small fixtures and partial documents may not model RFC 9457 at all. The
	// registry is generated only when the canonical ErrorResponse.code anchor
	// exists, so enrichment does not invent a disconnected taxonomy.
	if codeSchema == nil {
		return nil
	}
	if problemRegistryMatches(registry, codeSchema) {
		return nil
	}
	lines := bytes.Split(source, []byte("\n"))
	definitions := failurecodes.APIProblemTypes()
	var block strings.Builder
	block.WriteString("x-problem-types:\n")
	for _, definition := range definitions {
		fmt.Fprintf(&block, "  %s:\n    type: %s\n    title: %s\n    status: %d\n    statuses: [%s]\n",
			definition.Code, definition.Type, definition.Title, definition.Status, joinInts(definition.Statuses))
	}
	var edits []edit
	if registry != nil {
		keyLine := registry.Line
		if keyLine > 1 && strings.TrimSpace(string(lines[keyLine-2])) == "x-problem-types:" {
			keyLine--
		}
		edits = append(edits, edit{line: keyLine, endLine: maxNodeLine(registry), indent: 0, text: strings.TrimSuffix(block.String(), "\n"), mode: "replace"})
	} else {
		// Insert immediately after the first complete root scalar. yaml.Node line
		// ranges do not include block-scalar continuation lines, so inserting at
		// the apparent end of `info` can split its description text.
		edits = append(edits, edit{line: root.Line, indent: 0, text: strings.TrimSuffix(block.String(), "\n"), mode: "insert"})
	}
	if codeSchema != nil {
		enum := mappingValue(codeSchema, "enum")
		text := "enum: [" + joinCodes(definitions) + "]"
		if enum == nil {
			edits = append(edits, edit{line: maxNodeLine(codeSchema), indent: lineIndent(lines, codeSchema.Line), text: text, mode: "insert"})
		} else {
			keyLine := enum.Line
			if keyLine > 1 && strings.TrimSpace(string(lines[keyLine-2])) == "enum:" {
				keyLine--
			}
			edits = append(edits, edit{line: keyLine, endLine: maxNodeLine(enum), indent: lineIndent(lines, keyLine), text: text, mode: "replace"})
		}
	}
	return edits
}

func problemRegistryMatches(registry, codeSchema *yaml.Node) bool {
	definitions := failurecodes.APIProblemTypes()
	if registry == nil || registry.Kind != yaml.MappingNode || len(registry.Content)/2 != len(definitions) {
		return false
	}
	for _, definition := range definitions {
		entry := mappingValue(registry, string(definition.Code))
		if scalarValue(mappingValue(entry, "type")) != definition.Type ||
			scalarValue(mappingValue(entry, "title")) != definition.Title ||
			scalarValue(mappingValue(entry, "status")) != strconv.Itoa(definition.Status) ||
			!nodeIntsEqual(mappingValue(entry, "statuses"), definition.Statuses) {
			return false
		}
	}
	enum := mappingValue(codeSchema, "enum")
	if enum == nil || len(enum.Content) != len(definitions) {
		return false
	}
	for index, definition := range definitions {
		if enum.Content[index].Value != string(definition.Code) {
			return false
		}
	}
	return true
}

func nodeIntsEqual(node *yaml.Node, values []int) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) != len(values) {
		return false
	}
	for index, value := range values {
		if node.Content[index].Value != strconv.Itoa(value) {
			return false
		}
	}
	return true
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}

func joinCodes(definitions []failurecodes.ProblemType) string {
	parts := make([]string, len(definitions))
	for index, definition := range definitions {
		parts[index] = string(definition.Code)
	}
	return strings.Join(parts, ", ")
}

func metadataEdits(root *yaml.Node, manifest audienceManifest, source []byte) ([]edit, error) {
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("canonical OpenAPI has no paths map")
	}
	lines := bytes.Split(source, []byte("\n"))
	var edits []edit
	for pathIndex := 0; pathIndex+1 < len(paths.Content); pathIndex += 2 {
		path := paths.Content[pathIndex].Value
		pathItem := paths.Content[pathIndex+1]
		for operationIndex := 0; operationIndex+1 < len(pathItem.Content); operationIndex += 2 {
			method := strings.ToLower(pathItem.Content[operationIndex].Value)
			if !operationMethods[method] {
				continue
			}
			operation := pathItem.Content[operationIndex+1]
			operationID := scalarValue(mappingValue(operation, "operationId"))
			edits = append(edits, stableCapabilityResponseEdits(operation, lines)...)
			if mappingValue(operation, "tags") == nil {
				indent := lineIndent(lines, operation.Line)
				tags := inferredTags(path, manifest)
				encoded, _ := json.Marshal(tags)
				// A mapping node's line is its first child. Insert before that child
				// so a sequence-valued first property (for example, security) is
				// never split from its entries.
				edits = append(edits, edit{line: operation.Line - 1, indent: indent, text: "tags: " + string(encoded), mode: "insert"})
			}
			if !mutationMethods[method] {
				continue
			}
			requestBody := resolveLocalReference(root, mappingValue(operation, "requestBody"))
			edits = append(edits, contentExampleEdits(root, requestBody, lines, operationID, "request")...)
			responses := mappingValue(operation, "responses")
			if responses == nil {
				continue
			}
			for index := 0; index+1 < len(responses.Content); index += 2 {
				if !isSuccessStatus(responses.Content[index].Value) {
					continue
				}
				response := resolveLocalReference(root, responses.Content[index+1])
				edits = append(edits, contentExampleEdits(root, response, lines, operationID, "response")...)
			}
		}
	}
	return edits, nil
}

func contentExampleEdits(root, owner *yaml.Node, lines [][]byte, operationID, role string) []edit {
	content := mappingValue(owner, "content")
	if content == nil || content.Kind != yaml.MappingNode {
		return nil
	}
	var edits []edit
	for index := 0; index+1 < len(content.Content); index += 2 {
		media := content.Content[index+1]
		schema := mappingValue(media, "schema")
		if schema == nil || hasExample(media) || schemaHasExample(root, schema, map[*yaml.Node]bool{}) {
			continue
		}
		value := schemaExample(root, schema, operationID, role, "", 0, map[*yaml.Node]bool{})
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		// yaml.Node.Line for the media mapping points at its first child
		// (normally `schema:`). Keep the example at that same indentation and
		// place it after the complete schema subtree so it is always a media-
		// type sibling, including for block and inline schemas.
		indent := lineIndent(lines, media.Line)
		edits = append(edits, edit{line: maxNodeLine(schema), indent: indent, text: "example: " + string(encoded), mode: "insert"})
	}
	return edits
}

func stableCapabilityResponseEdits(operation *yaml.Node, lines [][]byte) []edit {
	if scalarValue(mappingValue(operation, "x-stability")) == "alpha" {
		return nil
	}
	responses := mappingValue(operation, "responses")
	if responses == nil {
		return nil
	}
	var key501, value501 *yaml.Node
	has503 := false
	for index := 0; index+1 < len(responses.Content); index += 2 {
		switch responses.Content[index].Value {
		case "501":
			key501, value501 = responses.Content[index], responses.Content[index+1]
		case "503":
			has503 = true
		}
	}
	if key501 == nil {
		return nil
	}
	endLine := maxNodeLine(value501)
	if has503 {
		return []edit{{line: key501.Line, endLine: endLine, mode: "delete"}}
	}
	edits := []edit{{line: key501.Line, endLine: key501.Line, indent: lineIndent(lines, key501.Line), text: "'503':", mode: "replace"}}
	for line := key501.Line + 1; line <= endLine && line <= len(lines); line++ {
		current := string(lines[line-1])
		if !strings.Contains(current, "NOT_IMPLEMENTED") {
			continue
		}
		edits = append(edits, edit{
			line: line, endLine: line, indent: lineIndent(lines, line),
			text: strings.ReplaceAll(strings.TrimSpace(current), "NOT_IMPLEMENTED", "CAPABILITY_UNAVAILABLE"), mode: "replace",
		})
	}
	return edits
}

func maxNodeLine(node *yaml.Node) int {
	if node == nil {
		return 0
	}
	maximum := node.Line
	for _, child := range node.Content {
		if line := maxNodeLine(child); line > maximum {
			maximum = line
		}
	}
	return maximum
}

func inferredTags(path string, manifest audienceManifest) []string {
	var tags []string
	for name, config := range manifest.Audiences {
		for _, prefix := range config.PathPrefixes {
			if strings.HasPrefix(path, prefix) {
				tags = append(tags, title(name))
				break
			}
		}
	}
	sort.Strings(tags)
	resource := resourceTag(path)
	found := false
	for _, tag := range tags {
		found = found || tag == resource
	}
	if !found {
		tags = append(tags, resource)
	}
	return tags
}

func resourceTag(path string) string {
	special := []struct{ marker, tag string }{
		{"/automation-tokens", "AutomationTokens"}, {"/cron-occurrences", "Cron"},
		{"/deployments", "Deployments"}, {"/variables", "Variables"},
		{"/custom-domains", "Domains"}, {"/domains", "Domains"},
		{"/github", "GitHub"}, {"/members", "Identity"},
		{"/backup-policies", "VolumeBackups"}, {"/backups", "VolumeBackups"},
		{"/restores", "VolumeRestores"},
	}
	for _, candidate := range special {
		if strings.Contains(path, candidate.marker) {
			return candidate.tag
		}
	}
	segment := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
	if strings.HasPrefix(segment, "monitor-") || segment == "monitors" {
		return "Monitors"
	}
	if strings.HasPrefix(segment, "volume-") || segment == "volumes" {
		return "Volumes"
	}
	if strings.HasPrefix(segment, "webhook") {
		return "Webhooks"
	}
	if known := map[string]string{
		"_demo": "Demonstrations", "agent": "Conversations", "agents": "RuntimeAgents",
		"auth": "Identity", "backup": "Backups", "billing": "Billing", "compat": "Compatibility",
		"config": "Configuration", "control-plane": "ControlPlane", "dashboard": "Dashboard",
		"enterprise": "Enterprise", "environments": "Environments", "github": "GitHub",
		"invitations": "Invitations", "isolation": "Isolation", "networking": "Networking",
		"observability": "Observability", "openapi.yaml": "Contracts", "asyncapi.yaml": "Contracts",
		"operations": "Operations", "projects": "Projects", "release": "Release",
		"services": "Services", "tcp-proxies": "TCPProxies", "templates": "Templates",
		"variables": "Variables", "workspaces": "Identity",
	}[segment]; known != "" {
		return known
	}
	return title(strings.ReplaceAll(segment, "-", ""))
}

func schemaExample(root, schema *yaml.Node, operationID, role, property string, depth int, visited map[*yaml.Node]bool) any {
	if schema == nil || depth > 8 {
		return nil
	}
	resolved := resolveLocalReference(root, schema)
	if resolved != schema {
		return schemaExample(root, resolved, operationID, role, property, depth+1, visited)
	}
	if visited[schema] {
		return nil
	}
	visited[schema] = true
	defer delete(visited, schema)
	if example := mappingValue(schema, "example"); example != nil {
		var value any
		if example.Decode(&value) == nil {
			return value
		}
	}
	if enum := mappingValue(schema, "enum"); enum != nil && len(enum.Content) > 0 {
		return scalarExample(enum.Content[0])
	}
	if constant := mappingValue(schema, "const"); constant != nil {
		return scalarExample(constant)
	}
	for _, combinator := range []string{"oneOf", "anyOf"} {
		if choices := mappingValue(schema, combinator); choices != nil && len(choices.Content) > 0 {
			return schemaExample(root, choices.Content[0], operationID, role, property, depth+1, visited)
		}
	}
	if allOf := mappingValue(schema, "allOf"); allOf != nil {
		merged := map[string]any{}
		for _, part := range allOf.Content {
			if object, ok := schemaExample(root, part, operationID, role, property, depth+1, visited).(map[string]any); ok {
				for key, value := range object {
					merged[key] = value
				}
			}
		}
		return merged
	}
	typeName := scalarValue(mappingValue(schema, "type"))
	if typeName == "" && mappingValue(schema, "properties") != nil {
		typeName = "object"
	}
	switch typeName {
	case "object":
		properties := mappingValue(schema, "properties")
		required := stringSet(mappingValue(schema, "required"))
		result := map[string]any{}
		if properties != nil {
			for index := 0; index+1 < len(properties.Content); index += 2 {
				name := properties.Content[index].Value
				if len(required) > 0 && !required[name] {
					continue
				}
				result[name] = schemaExample(root, properties.Content[index+1], operationID, role, name, depth+1, visited)
			}
			if len(result) == 0 {
				for index := 0; index+1 < len(properties.Content) && len(result) < 3; index += 2 {
					name := properties.Content[index].Value
					result[name] = schemaExample(root, properties.Content[index+1], operationID, role, name, depth+1, visited)
				}
			}
		}
		if len(result) == 0 {
			result["example"] = operationID + " " + role
		}
		return result
	case "array":
		return []any{schemaExample(root, mappingValue(schema, "items"), operationID, role, property, depth+1, visited)}
	case "integer", "number":
		if minimum := mappingValue(schema, "minimum"); minimum != nil {
			return scalarExample(minimum)
		}
		return 1
	case "boolean":
		return false
	default:
		return stringExample(property, scalarValue(mappingValue(schema, "format")), operationID, role)
	}
}

func stringExample(property, format, operationID, role string) string {
	name := strings.ToLower(property)
	switch {
	case format == "date-time":
		return "2026-01-02T03:04:05Z"
	case format == "uri" || strings.Contains(name, "url"):
		return "https://api.midnight.example/" + operationID
	case format == "binary":
		return "H4sIAAAAAAAA"
	case strings.Contains(name, "email"):
		return "operator@example.com"
	case strings.Contains(name, "hmac"):
		return strings.Repeat("a", 64)
	case strings.HasSuffix(name, "_id") || name == "id":
		prefix := map[string]string{"project_id": "prj", "service_id": "svc", "environment_id": "env", "workspace_id": "wrk", "agent_id": "agent", "deployment_id": "dep", "conversation_id": "conv"}[name]
		if prefix == "" {
			prefix = "id"
		}
		return prefix + "_demo"
	case strings.Contains(name, "slug") || name == "name":
		return "demo"
	case strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "credential"):
		return "redacted-example"
	case name == "content" || name == "detail" || name == "reason":
		return "Representative " + role
	case name != "":
		return "example-" + strings.ReplaceAll(name, "_", "-")
	default:
		return operationID + " " + role
	}
}

func applyEdits(source []byte, edits []edit) ([]byte, error) {
	lines := bytes.Split(source, []byte("\n"))
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].line == edits[j].line {
			return edits[i].mode != "insert" && edits[j].mode == "insert"
		}
		return edits[i].line > edits[j].line
	})
	for _, candidate := range edits {
		if candidate.line < 1 || candidate.line > len(lines) {
			return nil, fmt.Errorf("invalid edit line %d", candidate.line)
		}
		endLine := candidate.endLine
		if endLine == 0 {
			endLine = candidate.line
		}
		switch candidate.mode {
		case "delete":
			lines = append(lines[:candidate.line-1], lines[endLine:]...)
		case "replace":
			replacement := []byte(strings.Repeat(" ", candidate.indent) + candidate.text)
			lines = append(lines[:candidate.line-1], append([][]byte{replacement}, lines[endLine:]...)...)
		default:
			insert := []byte(strings.Repeat(" ", candidate.indent) + candidate.text)
			index := candidate.line
			lines = append(lines, nil)
			copy(lines[index+1:], lines[index:])
			lines[index] = insert
		}
	}
	return bytes.Join(lines, []byte("\n")), nil
}

func lineIndent(lines [][]byte, oneBased int) int {
	if oneBased < 1 || oneBased > len(lines) {
		return 0
	}
	return len(lines[oneBased-1]) - len(bytes.TrimLeft(lines[oneBased-1], " "))
}

func title(value string) string {
	runes := []rune(value)
	if len(runes) > 0 {
		runes[0] = unicode.ToUpper(runes[0])
	}
	return string(runes)
}

func isSuccessStatus(status string) bool { return len(status) == 3 && status[0] == '2' }

func scalarExample(node *yaml.Node) any {
	var value any
	if node != nil && node.Decode(&value) == nil {
		return value
	}
	return nil
}

func stringSet(node *yaml.Node) map[string]bool {
	result := map[string]bool{}
	if node != nil {
		for _, child := range node.Content {
			result[child.Value] = true
		}
	}
	return result
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
	return mappingValue(node, "example") != nil || mappingValue(node, "examples") != nil
}

func resolveLocalReference(root, node *yaml.Node) *yaml.Node {
	ref := scalarValue(mappingValue(node, "$ref"))
	if !strings.HasPrefix(ref, "#/") {
		return node
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		current = mappingValue(current, part)
		if current == nil {
			return node
		}
	}
	return current
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return node.Value
}
