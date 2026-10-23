package api

import (
	"strings"
	"testing"

	openapiv1alpha "github.com/midnight/midnight/api/openapi/v1alpha"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/midnightctl"
	"gopkg.in/yaml.v3"
)

// These literals are a compile-time gate: changing either the live or
// generated client problem model away from RFC 9457 fields breaks the build.
var _ = ErrorResponse{
	Type: "", Title: "", Status: 400, Detail: "", Code: failurecodes.InternalError,
	RequestID: "", DocsURL: "", Error: ErrorEnvelope{},
}

func TestOpenAPIErrorResponsesUseProblemMediaType(t *testing.T) {
	type operation struct {
		Responses map[string]yaml.Node `yaml:"responses"`
	}
	var document struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Responses map[string]yaml.Node `yaml:"responses"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatal(err)
	}
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"patch": true, "head": true, "options": true, "trace": true,
	}
	for path, item := range document.Paths {
		for index := 0; index+1 < len(item.Content); index += 2 {
			method := item.Content[index].Value
			if !httpMethods[method] {
				continue
			}
			var operation operation
			if err := item.Content[index+1].Decode(&operation); err != nil {
				t.Fatalf("decode %s %s: %v", strings.ToUpper(method), path, err)
			}
			for status, responseNode := range operation.Responses {
				if !strings.HasPrefix(status, "4") && !strings.HasPrefix(status, "5") {
					continue
				}
				if reference := scalarYAMLField(&responseNode, "$ref"); strings.HasPrefix(reference, "#/components/responses/") {
					name := strings.TrimPrefix(reference, "#/components/responses/")
					responseNode = document.Components.Responses[name]
				}
				var response struct {
					Content map[string]yaml.Node `yaml:"content"`
				}
				if err := responseNode.Decode(&response); err != nil {
					t.Fatalf("decode %s %s response %s: %v", strings.ToUpper(method), path, status, err)
				}
				if len(response.Content) != 1 || response.Content["application/problem+json"].Kind == 0 {
					t.Errorf("%s %s response %s media types = %v, want application/problem+json only", strings.ToUpper(method), path, status, mapKeys(response.Content))
				}
			}
		}
	}
}

func TestOpenAPIConversationSemanticsMatchRuntime(t *testing.T) {
	var document struct {
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatal(err)
	}
	conversationPath := document.Paths["/agent/conversations"]
	create := mappingValue(&conversationPath, "post")
	if create == nil {
		t.Fatal("missing POST /agent/conversations")
	}
	parameters := mappingValue(create, "parameters")
	if parameters == nil || parameters.Kind != yaml.SequenceNode || len(parameters.Content) != 1 ||
		scalarYAMLField(parameters.Content[0], "$ref") != "#/components/parameters/OptionalIdempotencyKey" {
		t.Fatalf("conversation create parameters = %v, want OptionalIdempotencyKey", parameters)
	}
	streamPath := document.Paths["/agent/conversations/{conversation_id}/stream"]
	stream := mappingValue(&streamPath, "get")
	responses := mappingValue(stream, "responses")
	okResponse := mappingValue(responses, "200")
	content := mappingValue(okResponse, "content")
	if mappingValue(content, "text/event-stream") == nil {
		t.Fatal("conversation stream 200 response does not advertise text/event-stream")
	}
}

func TestOpenAPIAgentBoundaryResponsesMatchRuntime(t *testing.T) {
	var document struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatal(err)
	}
	for path, expectation := range map[string]struct {
		method        string
		requestSchema string
		statuses      []string
	}{
		"/agents/register":       {method: "post", requestSchema: "AgentRegistrationRequest", statuses: []string{"200", "400", "401", "413", "426", "429"}},
		"/agents/{id}/heartbeat": {method: "post", requestSchema: "AgentHeartbeatRequest", statuses: []string{"200", "400", "401", "409", "413", "429"}},
		"/agents/{id}/desired":   {method: "get", statuses: []string{"200", "304", "401", "429"}},
		"/agents/{id}/status":    {method: "post", requestSchema: "AgentStatusReport", statuses: []string{"200", "400", "401", "409", "413", "429"}},
	} {
		pathNode, ok := document.Paths[path]
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		operation := mappingValue(&pathNode, expectation.method)
		if expectation.requestSchema != "" {
			requestBody := mappingValue(operation, "requestBody")
			content := mappingValue(requestBody, "content")
			media := mappingValue(content, "application/json")
			schema := mappingValue(media, "schema")
			wantReference := "#/components/schemas/" + expectation.requestSchema
			if got := scalarYAMLField(schema, "$ref"); got != wantReference {
				t.Errorf("%s %s request schema = %q, want %q", strings.ToUpper(expectation.method), path, got, wantReference)
			}
			var closed struct {
				AdditionalProperties bool `yaml:"additionalProperties"`
			}
			schemaNode := document.Components.Schemas[expectation.requestSchema]
			if err := schemaNode.Decode(&closed); err != nil {
				t.Fatal(err)
			}
			if closed.AdditionalProperties {
				t.Errorf("request schema %s permits unknown properties", expectation.requestSchema)
			}
		}
		responses := mappingValue(operation, "responses")
		for _, status := range expectation.statuses {
			if mappingValue(responses, status) == nil {
				t.Errorf("%s %s missing response %s", strings.ToUpper(expectation.method), path, status)
			}
		}
	}
}

func scalarYAMLField(node *yaml.Node, field string) string {
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

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

var _ = midnightctl.ErrorResponse{
	Type: "", Title: "", Status: 400, Detail: "", Code: "", RequestId: "",
	DocsUrl: "", Error: midnightctl.ErrorEnvelope{},
}

func TestOpenAPISecurityAndProblemModelsAreClosed(t *testing.T) {
	var document struct {
		Components struct {
			SecuritySchemes map[string]struct {
				Type   string `yaml:"type"`
				Scheme string `yaml:"scheme"`
				In     string `yaml:"in"`
				Name   string `yaml:"name"`
			} `yaml:"securitySchemes"`
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatal(err)
	}
	wantBearer := []string{"BearerAuth", "HumanSession", "ProjectAutomationToken", "AgentCredential"}
	for _, name := range wantBearer {
		scheme, ok := document.Components.SecuritySchemes[name]
		if !ok || scheme.Type != "http" || scheme.Scheme != "bearer" {
			t.Errorf("security scheme %s = %#v, want HTTP bearer", name, scheme)
		}
	}
	for name, header := range map[string]string{
		"GitHubWebhookSignature":   "X-Hub-Signature-256",
		"MidnightWebhookSignature": "Midnight-Signature",
	} {
		scheme, ok := document.Components.SecuritySchemes[name]
		if !ok || scheme.Type != "apiKey" || scheme.In != "header" || scheme.Name != header {
			t.Errorf("security scheme %s = %#v, want header apiKey %s", name, scheme, header)
		}
	}

	problemNode, ok := document.Components.Schemas["ErrorResponse"]
	if !ok {
		t.Fatal("missing ErrorResponse schema")
	}
	var problem struct {
		Type                 string               `yaml:"type"`
		AdditionalProperties bool                 `yaml:"additionalProperties"`
		Required             []string             `yaml:"required"`
		Properties           map[string]yaml.Node `yaml:"properties"`
	}
	if err := problemNode.Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Type != "object" || problem.AdditionalProperties {
		t.Fatalf("ErrorResponse type=%q additionalProperties=%t", problem.Type, problem.AdditionalProperties)
	}
	required := make(map[string]bool, len(problem.Required))
	for _, name := range problem.Required {
		required[name] = true
	}
	for _, name := range []string{"type", "title", "status", "detail", "code", "request_id", "docs_url", "error"} {
		if !required[name] || problem.Properties[name].Kind == 0 {
			t.Errorf("ErrorResponse is missing required RFC 9457/extension field %q", name)
		}
	}
}
