// Command api-contract-views produces audience-specific OpenAPI views from the
// canonical v1alpha bundle. The views are generated artifacts; midnight.yaml
// remains the single source of truth until the modular source migration lands.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultSource   = "api/openapi/v1alpha/midnight.yaml"
	defaultManifest = "api/openapi/v1alpha/audiences.yaml"
	defaultOutput   = "api/openapi/v1alpha/views"
)

type audienceManifest struct {
	Audiences map[string]audience `yaml:"audiences"`
}

type audience struct {
	Description  string   `yaml:"description"`
	PathPrefixes []string `yaml:"path_prefixes"`
}

func main() {
	var source, manifest, output, stableOutput string
	var check bool
	flag.StringVar(&source, "source", defaultSource, "canonical OpenAPI document")
	flag.StringVar(&manifest, "manifest", defaultManifest, "audience manifest")
	flag.StringVar(&output, "output", defaultOutput, "generated view directory")
	flag.StringVar(&stableOutput, "stable-output", "", "optional generated stable-v1 candidate path")
	flag.BoolVar(&check, "check", false, "fail when generated files differ")
	flag.Parse()
	if err := run(source, manifest, output, check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if stableOutput != "" {
		if err := writeStableCandidate(source, stableOutput, check); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func run(sourcePath, manifestPath, outputDir string, check bool) error {
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest audienceManifest
	if err := yaml.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if len(manifest.Audiences) == 0 {
		return fmt.Errorf("audience manifest is empty")
	}
	if err := validateAudienceCoverage(source, manifest); err != nil {
		return err
	}
	if !check {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	names := make([]string, 0, len(manifest.Audiences))
	for name := range manifest.Audiences {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		generated, err := renderView(source, name, manifest.Audiences[name])
		if err != nil {
			return err
		}
		path := filepath.Join(outputDir, name+".openapi.yaml")
		if check {
			current, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(current, generated) {
				return fmt.Errorf("generated contract view is stale: %s", path)
			}
			continue
		}
		if err := os.WriteFile(path, generated, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func writeStableCandidate(sourcePath, outputPath string, check bool) error {
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read stable candidate source: %w", err)
	}
	generated, err := renderStableCandidate(source)
	if err != nil {
		return err
	}
	if check {
		current, readErr := os.ReadFile(outputPath)
		if readErr != nil || !bytes.Equal(current, generated) {
			return fmt.Errorf("generated stable candidate is stale: %s", outputPath)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create stable candidate directory: %w", err)
	}
	if err := os.WriteFile(outputPath, generated, 0o644); err != nil {
		return fmt.Errorf("write stable candidate: %w", err)
	}
	return nil
}

func renderStableCandidate(source []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("decode canonical OpenAPI: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("canonical OpenAPI root is invalid")
	}
	root := document.Content[0]
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("canonical OpenAPI has no paths map")
	}
	for pathIndex := len(paths.Content) - 2; pathIndex >= 0; pathIndex -= 2 {
		pathItem := paths.Content[pathIndex+1]
		for operationIndex := len(pathItem.Content) - 2; operationIndex >= 0; operationIndex -= 2 {
			method := strings.ToLower(pathItem.Content[operationIndex].Value)
			if !isOperationMethod(method) {
				continue
			}
			operation := pathItem.Content[operationIndex+1]
			if scalarValue(mappingValue(operation, "x-stability")) == "alpha" {
				pathItem.Content = append(pathItem.Content[:operationIndex], pathItem.Content[operationIndex+2:]...)
			}
		}
		if !pathItemHasOperation(pathItem) {
			paths.Content = append(paths.Content[:pathIndex], paths.Content[pathIndex+2:]...)
		}
	}
	info := mappingValue(root, "info")
	setScalar(info, "version", "v1")
	setScalar(info, "description", "Authoritative stable v1 REST promotion candidate for the currently supported Midnight control-plane surface. Midnight is Railway-inspired, but this is not a Railway-compatible or drop-in API. This generated file is not served until stable-v1 promotion is complete.")
	setScalar(root, "x-source-contract", "api/openapi/v1alpha/midnight.yaml")
	servers := mappingValue(root, "servers")
	if servers != nil && servers.Kind == yaml.SequenceNode {
		for _, server := range servers.Content {
			url := mappingValue(server, "url")
			if url != nil {
				url.Value = strings.ReplaceAll(url.Value, "/api/v1alpha", "/api/v1")
			}
			setScalar(server, "description", "Same-origin stable v1 API root after promotion.")
		}
	}
	errorSchema := localReference(root, "#/components/schemas/ErrorResponse")
	if errorSchema == nil {
		return nil, fmt.Errorf("canonical OpenAPI has no ErrorResponse schema")
	}
	properties := mappingValue(errorSchema, "properties")
	compatibility := mappingValue(properties, "error")
	if compatibility != nil {
		if !scalarBool(mappingValue(compatibility, "deprecated")) || !scalarBool(mappingValue(compatibility, "x-stable-v1-omit")) {
			return nil, fmt.Errorf("ErrorResponse.error is not marked for stable-v1 omission")
		}
		removeMappingValue(properties, "error")
		removeSequenceValue(mappingValue(errorSchema, "required"), "error")
	}

	var buffer bytes.Buffer
	buffer.WriteString("# Code generated by tools/api-contract-views --stable-output; DO NOT EDIT.\n")
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, fmt.Errorf("encode stable candidate: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func isOperationMethod(method string) bool {
	switch method {
	case "get", "put", "post", "delete", "patch", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func pathItemHasOperation(pathItem *yaml.Node) bool {
	for index := 0; index+1 < len(pathItem.Content); index += 2 {
		if isOperationMethod(strings.ToLower(pathItem.Content[index].Value)) {
			return true
		}
	}
	return false
}

func scalarValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return node.Value
}

// validateAudienceCoverage keeps audience views a complete projection of the
// canonical REST surface. A path may deliberately appear in more than one
// audience, but silently appearing in none makes that operation undiscoverable
// to every audience-specific consumer.
func validateAudienceCoverage(source []byte, manifest audienceManifest) error {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return fmt.Errorf("decode canonical OpenAPI: %w", err)
	}
	if len(document.Content) != 1 {
		return fmt.Errorf("canonical OpenAPI root is invalid")
	}
	paths := mappingValue(document.Content[0], "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return fmt.Errorf("canonical OpenAPI has no paths map")
	}
	for index := 0; index+1 < len(paths.Content); index += 2 {
		path := paths.Content[index].Value
		covered := false
		for _, config := range manifest.Audiences {
			if matchesAnyPrefix(path, config.PathPrefixes) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("canonical path is absent from every audience: %s", path)
		}
	}
	return nil
}

func renderView(source []byte, name string, config audience) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("decode canonical OpenAPI: %w", err)
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

func removeMappingValue(mapping *yaml.Node, key string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return
		}
	}
}

func removeSequenceValue(sequence *yaml.Node, value string) {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return
	}
	for index := 0; index < len(sequence.Content); index++ {
		if sequence.Content[index].Value == value {
			sequence.Content = append(sequence.Content[:index], sequence.Content[index+1:]...)
			return
		}
	}
}

func scalarBool(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && strings.EqualFold(node.Value, "true")
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
