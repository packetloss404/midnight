package cloudevents

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAsyncAPIEventCatalogMatchesRuntimeCatalog(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "api", "asyncapi", "v1", "midnight.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		AsyncAPI   string `yaml:"asyncapi"`
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.AsyncAPI != "3.0.0" {
		t.Fatalf("asyncapi = %q, want 3.0.0", document.AsyncAPI)
	}
	got := document.Components.Schemas["EventType"].Enum
	wantTypes := All()
	if len(got) != len(wantTypes) {
		t.Fatalf("AsyncAPI event types = %d, runtime catalog = %d", len(got), len(wantTypes))
	}
	for index, eventType := range wantTypes {
		if got[index] != string(eventType) {
			t.Fatalf("event type %d = %q, want %q", index, got[index], eventType)
		}
	}
}

func TestAsyncAPIEnvelopeContractMatchesRuntime(t *testing.T) {
	data := readAsyncAPIContract(t)
	var document struct {
		Servers map[string]struct {
			Host     string `yaml:"host"`
			Protocol string `yaml:"protocol"`
		} `yaml:"servers"`
		Operations map[string]struct {
			Action   string `yaml:"action"`
			Messages []struct {
				Ref string `yaml:"$ref"`
			} `yaml:"messages"`
		} `yaml:"operations"`
		Components struct {
			Messages map[string]struct {
				ContentType string `yaml:"contentType"`
			} `yaml:"messages"`
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	server, ok := document.Servers["subscriber"]
	if !ok || server.Host == "" || server.Protocol != "https" {
		t.Fatalf("subscriber server = %#v, want non-empty HTTPS server", server)
	}
	delivery, ok := document.Operations["deliverMidnightEvent"]
	if !ok || delivery.Action != "send" {
		t.Fatalf("deliverMidnightEvent = %#v, want send operation", delivery)
	}
	gotDeliveryMessages := make([]string, 0, len(delivery.Messages))
	for _, message := range delivery.Messages {
		gotDeliveryMessages = append(gotDeliveryMessages, message.Ref)
	}
	wantDeliveryMessages := []string{
		"#/channels/outboundEvents/messages/midnightEvent",
		"#/channels/outboundEvents/messages/monitorNotification",
	}
	if strings.Join(gotDeliveryMessages, "\x00") != strings.Join(wantDeliveryMessages, "\x00") {
		t.Fatalf("deliverMidnightEvent messages = %v, want %v", gotDeliveryMessages, wantDeliveryMessages)
	}
	message, ok := document.Components.Messages["MidnightEvent"]
	if !ok || message.ContentType != ContentType {
		t.Fatalf("MidnightEvent contentType = %q, want %q", message.ContentType, ContentType)
	}
	monitorMessage, ok := document.Components.Messages["MonitorNotification"]
	if !ok || monitorMessage.ContentType != "application/json" {
		t.Fatalf("MonitorNotification contentType = %q, want application/json", monitorMessage.ContentType)
	}

	cloudEvent := decodeAsyncAPISchema(t, document.Components.Schemas, "CloudEvent")
	wantRequired := RequiredFields()
	gotRequired := append([]string(nil), cloudEvent.Required...)
	sort.Strings(gotRequired)
	if strings.Join(gotRequired, "\x00") != strings.Join(wantRequired, "\x00") {
		t.Fatalf("CloudEvent required fields = %v, want %v", gotRequired, wantRequired)
	}
	specVersion := cloudEvent.Properties["specversion"]
	if value := asyncAPIScalarField(&specVersion, "const"); value != SpecVersion {
		t.Fatalf("CloudEvent specversion const = %q, want %q", value, SpecVersion)
	}
	dataContentType := cloudEvent.Properties["datacontenttype"]
	if value := asyncAPIScalarField(&dataContentType, "const"); value != DataContentTypeJSON {
		t.Fatalf("CloudEvent datacontenttype const = %q, want %q", value, DataContentTypeJSON)
	}

	headers := decodeAsyncAPISchema(t, document.Components.Schemas, "CloudEventWebhookHeaders")
	gotHeaders := append([]string(nil), headers.Required...)
	sort.Strings(gotHeaders)
	wantHeaders := []string{"Midnight-Delivery-Id", "Midnight-Event-Id", "Midnight-Event-Type", "Midnight-Signature"}
	if strings.Join(gotHeaders, "\x00") != strings.Join(wantHeaders, "\x00") {
		t.Fatalf("CloudEventWebhookHeaders required = %v, want %v", gotHeaders, wantHeaders)
	}
}

func TestAsyncAPILocalReferencesResolve(t *testing.T) {
	var document yaml.Node
	if err := yaml.Unmarshal(readAsyncAPIContract(t), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Content) != 1 {
		t.Fatal("AsyncAPI root is invalid")
	}
	root := document.Content[0]
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for index := 0; index+1 < len(node.Content); index += 2 {
				key, value := node.Content[index], node.Content[index+1]
				if key.Value == "$ref" {
					if !strings.HasPrefix(value.Value, "#/") || resolveAsyncAPIReference(root, value.Value) == nil {
						t.Errorf("unresolved AsyncAPI reference %q at line %d", value.Value, value.Line)
					}
				}
				visit(value)
			}
			return
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(root)
}

type asyncAPISchema struct {
	Required   []string             `yaml:"required"`
	Properties map[string]yaml.Node `yaml:"properties"`
}

func decodeAsyncAPISchema(t *testing.T, schemas map[string]yaml.Node, name string) asyncAPISchema {
	t.Helper()
	node, ok := schemas[name]
	if !ok {
		t.Fatalf("AsyncAPI schema %s is missing", name)
	}
	var schema asyncAPISchema
	if err := node.Decode(&schema); err != nil {
		t.Fatalf("decode AsyncAPI schema %s: %v", name, err)
	}
	return schema
}

func asyncAPIScalarField(node *yaml.Node, field string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == field {
			return node.Content[index+1].Value
		}
	}
	return ""
}

func resolveAsyncAPIReference(root *yaml.Node, reference string) *yaml.Node {
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		if current == nil || current.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for index := 0; index+1 < len(current.Content); index += 2 {
			if current.Content[index].Value == part {
				next = current.Content[index+1]
				break
			}
		}
		current = next
	}
	return current
}

func readAsyncAPIContract(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "api", "asyncapi", "v1", "midnight.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
