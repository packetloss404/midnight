// Command api-problem-registry emits the canonical OpenAPI problem registry
// block and ErrorResponse code enum from the runtime taxonomy.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/midnight/midnight/internal/failurecodes"
	"gopkg.in/yaml.v3"
)

func main() {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	registry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	enum := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, definition := range failurecodes.APIProblemTypes() {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		addScalar(entry, "type", definition.Type, "!!str")
		addScalar(entry, "title", definition.Title, "!!str")
		addScalar(entry, "status", strconv.Itoa(definition.Status), "!!int")
		statuses := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, status := range definition.Statuses {
			statuses.Content = append(statuses.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(status)})
		}
		addNode(entry, "statuses", statuses)
		addNode(registry, string(definition.Code), entry)
		enum.Content = append(enum.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(definition.Code)})
	}
	addNode(root, "x-problem-types", registry)
	addNode(root, "ErrorResponse-code-enum", enum)
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := encoder.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func addScalar(mapping *yaml.Node, key, value, tag string) {
	addNode(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func addNode(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value,
	)
}
