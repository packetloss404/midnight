package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	openapiv1alpha "github.com/midnight/midnight/api/openapi/v1alpha"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
	"gopkg.in/yaml.v3"
)

type openAPIRouteOperation struct {
	Path         string
	Method       string
	OperationID  string
	Security     []string
	Experimental bool
}

// TestOpenAPIRouteContract deliberately asks net/http's matcher which
// production handler owns each canonical OpenAPI path. It avoids maintaining a
// second route list or scraping router.go, both of which can agree with stale
// source while disagreeing with the mux that actually serves requests.
func TestOpenAPIRouteContract(t *testing.T) {
	base, operations := parseOpenAPIRouteOperations(t)
	if len(operations) == 0 {
		t.Fatal("embedded OpenAPI contract contains no operations")
	}

	// These routes are intentionally registered only when the human identity
	// authority needed to serve them is present. The production fixture below
	// supplies that authority and therefore still requires every one to match.
	identityDependencyPaths := map[string]bool{
		"/workspaces":                                                         true,
		"/workspaces/{workspace_id}/members":                                  true,
		"/workspaces/{workspace_id}/members/{user_id}":                        true,
		"/projects/{project_id}/members":                                      true,
		"/projects/{project_id}/members/{user_id}":                            true,
		"/projects/{project_id}/access":                                       true,
		"/projects/{project_id}/github/repositories":                          true,
		"/projects/{project_id}/github/repositories/{repository_id}/branches": true,
		"/services/{service_id}/github":                                       true,
		"/github/installations/{installation_id}/workspace-grant":             true,
		"/github/setup/callback":                                              true,
		"/workspaces/{workspace_id}/github/setup":                             true,
	}

	t.Setenv(experimentalEnv, "")
	defaultMux, _ := buildRouterWithDataStack(DataStack{})
	productionMux, productionHandler := buildRouterWithDataStack(productionRouteContractStack(t))

	t.Setenv(experimentalEnv, "true")
	experimentalMux, _ := buildRouterWithDataStack(productionRouteContractStack(t))

	counts := map[string]int{}
	for _, operation := range operations {
		fullPath := base + operation.Path
		defaultClass, defaultPattern := classifyServeMuxMatch(defaultMux, operation.Method, fullPath)
		productionClass, productionPattern := classifyServeMuxMatch(productionMux, operation.Method, fullPath)
		experimentalClass, experimentalPattern := classifyServeMuxMatch(experimentalMux, operation.Method, fullPath)

		if operation.Experimental {
			if defaultClass != "missing" || productionClass != "missing" {
				t.Errorf("%s %s: experimental route registered while disabled (default=%q production=%q)",
					operation.Method, fullPath, defaultPattern, productionPattern)
			}
			requireContractRouteMatch(t, operation, fullPath, experimentalClass, experimentalPattern)
			counts["conditional"]++
			continue
		}

		if !identityDependencyPaths[operation.Path] {
			requireContractRouteMatch(t, operation, fullPath, defaultClass, defaultPattern)
		} else {
			counts["dependency-gated"]++
		}
		requireContractRouteMatch(t, operation, fullPath, productionClass, productionPattern)
		requireContractRouteMatch(t, operation, fullPath, experimentalClass, experimentalPattern)
		counts[productionClass]++
	}

	probeProductionAuthContract(t, productionHandler, base, operations)
	t.Logf("checked %d OpenAPI operations: %d exact, %d dispatcher, %d experimental, %d dependency-gated",
		len(operations), counts["exact"], counts["dispatcher"], counts["conditional"], counts["dependency-gated"])
}

// TestProductionRuntimeRoutesAreDocumented closes the opposite direction from
// TestOpenAPIRouteContract. The router's production registration recorder is
// the source here: every REST operation it declares must exist in the embedded
// OpenAPI contract, including operations owned by subtree dispatchers.
func TestProductionRuntimeRoutesAreDocumented(t *testing.T) {
	base, documented := parseOpenAPIRouteOperations(t)
	documentedKeys := make(map[string]string, len(documented))
	for _, operation := range documented {
		key := operation.Method + " " + normalizeRuntimeRoutePattern(base+operation.Path)
		documentedKeys[key] = operation.OperationID
	}

	t.Setenv(experimentalEnv, "true")
	_, _, inventory := buildRouterWithDataStackInventory(productionRouteContractStack(t))
	if unclassified := inventory.UnclassifiedRegistrations(); len(unclassified) > 0 {
		t.Errorf("production v1alpha registrations have no explicit REST inventory or documented non-REST exclusion: %v", unclassified)
	}
	operations := inventory.Operations()
	if len(operations) == 0 {
		t.Fatal("production runtime REST inventory is empty")
	}
	for _, operation := range operations {
		key := operation.Method + " " + normalizeRuntimeRoutePattern(operation.Path)
		if _, ok := documentedKeys[key]; !ok {
			t.Errorf("runtime serves undocumented REST operation %s (owner pattern %q)", key, operation.Pattern)
		}
	}
	t.Logf("checked %d production runtime REST operations against canonical OpenAPI", len(operations))
}

func TestProjectAutomationTokenOpenAPIContractIsClosed(t *testing.T) {
	_, operations := parseOpenAPIRouteOperations(t)
	byOperationID := make(map[string]openAPIRouteOperation, len(operations))
	for _, operation := range operations {
		byOperationID[operation.OperationID] = operation
	}

	humanManagement := map[string]string{
		"listProjectAutomationTokens":  "GET /projects/{project_id}/automation-tokens",
		"createProjectAutomationToken": "POST /projects/{project_id}/automation-tokens",
		"revokeProjectAutomationToken": "DELETE /projects/{project_id}/automation-tokens/{token_id}",
	}
	for operationID, wantRoute := range humanManagement {
		operation, ok := byOperationID[operationID]
		if !ok {
			t.Fatalf("missing management operation %s", operationID)
		}
		if got := operation.Method + " " + operation.Path; got != wantRoute {
			t.Fatalf("%s route = %q, want %q", operationID, got, wantRoute)
		}
		if len(operation.Security) != 1 || operation.Security[0] != "HumanSession" {
			t.Fatalf("%s security = %v, want HumanSession only", operationID, operation.Security)
		}
	}

	automationAllowed := []string{
		"getProject",
		"listServices",
		"getService",
		"listEnvironments",
		"getEnvironment",
		"createServiceUp",
		"listServiceDeployments",
		"createServiceDeployment",
		"getLatestActiveServiceDeployment",
		"getServiceDeployment",
		"getOperation",
		"streamOperationEvents",
		"getOperationLogs",
	}
	for _, operationID := range automationAllowed {
		operation, ok := byOperationID[operationID]
		if !ok {
			t.Fatalf("missing automation operation %s", operationID)
		}
		if !routeContractContains(operation.Security, "ProjectAutomationToken") {
			t.Fatalf("%s security = %v, want ProjectAutomationToken", operationID, operation.Security)
		}
	}

	automationDenied := []string{
		"listProjects",
		"createProject",
		"createService",
		"createEnvironment",
		"updateEnvironment",
		"getProjectAccess",
		"cancelOperation",
		"failOperation",
		"createDeploymentAction",
		"listTenantServiceEnvironmentVariables",
		"createTenantServiceEnvironmentVariable",
		"getTenantServiceEnvironmentVariable",
		"updateTenantServiceEnvironmentVariable",
		"deleteTenantServiceEnvironmentVariable",
		"listTenantServiceEnvironmentGeneratedDomains",
		"createTenantServiceEnvironmentGeneratedDomain",
		"getTenantServiceEnvironmentGeneratedDomain",
		"deleteTenantServiceEnvironmentGeneratedDomain",
		"listServicePreviewEnvironments",
		"listProjectAutomationTokens",
		"createProjectAutomationToken",
		"revokeProjectAutomationToken",
	}
	for _, operationID := range automationDenied {
		operation, ok := byOperationID[operationID]
		if !ok {
			t.Fatalf("missing denied operation %s", operationID)
		}
		if routeContractContains(operation.Security, "ProjectAutomationToken") {
			t.Fatalf("%s unexpectedly declares ProjectAutomationToken: %v", operationID, operation.Security)
		}
	}

	var document struct {
		Components struct {
			Parameters      map[string]yaml.Node `yaml:"parameters"`
			Schemas         map[string]yaml.Node `yaml:"schemas"`
			SecuritySchemes map[string]yaml.Node `yaml:"securitySchemes"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatalf("parse automation OpenAPI components: %v", err)
	}
	for _, schema := range []string{
		"ProjectAutomationTokenProfile",
		"ProjectAutomationToken",
		"ProjectAutomationTokenList",
		"ProjectAutomationTokenCreateRequest",
		"ProjectAutomationTokenCreateResult",
	} {
		if _, ok := document.Components.Schemas[schema]; !ok {
			t.Errorf("missing schema %s", schema)
		}
	}
	if _, ok := document.Components.Parameters["AutomationTokenID"]; !ok {
		t.Error("missing AutomationTokenID parameter")
	}
	if _, ok := document.Components.SecuritySchemes["ProjectAutomationToken"]; !ok {
		t.Error("missing ProjectAutomationToken security scheme")
	}
}

func TestHumanSessionOpenAPIContractIsClosed(t *testing.T) {
	_, operations := parseOpenAPIRouteOperations(t)
	byOperationID := make(map[string]openAPIRouteOperation, len(operations))
	for _, operation := range operations {
		byOperationID[operation.OperationID] = operation
	}

	humanAllowed := map[string]string{
		"updateEnvironment":                             "PATCH /environments/{id}",
		"createService":                                 "POST /services",
		"deleteService":                                 "DELETE /services/{id}",
		"listServicePreviewEnvironments":                "GET /services/{id}/preview-environments",
		"listServiceDeployments":                        "GET /services/{id}/deployments",
		"createServiceDeployment":                       "POST /services/{id}/deployments",
		"createServiceUp":                               "POST /services/{id}/up",
		"getLatestActiveServiceDeployment":              "GET /services/{id}/deployments/latest-active",
		"getServiceDeployment":                          "GET /services/{id}/deployments/{deployment_id}",
		"createDeploymentAction":                        "POST /services/{service_id}/deployments/{deployment_id}/actions/{action}",
		"cancelOperation":                               "POST /operations/{id}/cancel",
		"getOperation":                                  "GET /operations/{id}",
		"streamOperationEvents":                         "GET /operations/{id}/events",
		"getOperationLogs":                              "GET /operations/{id}/logs",
		"listProjectGitHubRepositories":                 "GET /projects/{project_id}/github/repositories",
		"listProjectGitHubRepositoryBranches":           "GET /projects/{project_id}/github/repositories/{repository_id}/branches",
		"getServiceGitHubSourceLink":                    "GET /services/{service_id}/github",
		"putServiceGitHubSourceLink":                    "PUT /services/{service_id}/github",
		"deleteServiceGitHubSourceLink":                 "DELETE /services/{service_id}/github",
		"listTenantServiceEnvironmentVariables":         "GET /services/{service_id}/environments/{environment_id}/variables",
		"createTenantServiceEnvironmentVariable":        "POST /services/{service_id}/environments/{environment_id}/variables",
		"getTenantServiceEnvironmentVariable":           "GET /services/{service_id}/environments/{environment_id}/variables/{variable_id}",
		"updateTenantServiceEnvironmentVariable":        "PATCH /services/{service_id}/environments/{environment_id}/variables/{variable_id}",
		"deleteTenantServiceEnvironmentVariable":        "DELETE /services/{service_id}/environments/{environment_id}/variables/{variable_id}",
		"listTenantServiceEnvironmentGeneratedDomains":  "GET /services/{service_id}/environments/{environment_id}/domains",
		"createTenantServiceEnvironmentGeneratedDomain": "POST /services/{service_id}/environments/{environment_id}/domains",
		"getTenantServiceEnvironmentGeneratedDomain":    "GET /services/{service_id}/environments/{environment_id}/domains/{domain_id}",
		"deleteTenantServiceEnvironmentGeneratedDomain": "DELETE /services/{service_id}/environments/{environment_id}/domains/{domain_id}",
	}
	for operationID, wantRoute := range humanAllowed {
		operation, ok := byOperationID[operationID]
		if !ok {
			t.Fatalf("missing human-session operation %s", operationID)
		}
		if got := operation.Method + " " + operation.Path; got != wantRoute {
			t.Fatalf("%s route = %q, want %q", operationID, got, wantRoute)
		}
		if !routeContractContains(operation.Security, "HumanSession") {
			t.Fatalf("%s security = %v, want HumanSession", operationID, operation.Security)
		}
	}

	humanDenied := []string{
		"listOperations",
		"getGitHubInstallationWorkspaceGrant",
		"putGitHubInstallationWorkspaceGrant",
		"deleteGitHubInstallationWorkspaceGrant",
		"failOperation",
	}
	for _, operationID := range humanDenied {
		operation, ok := byOperationID[operationID]
		if !ok {
			t.Fatalf("missing human-session-denied operation %s", operationID)
		}
		if routeContractContains(operation.Security, "HumanSession") {
			t.Fatalf("%s unexpectedly declares HumanSession: %v", operationID, operation.Security)
		}
	}
}

func TestHumanImageDeployOpenAPIContractIsNarrow(t *testing.T) {
	type schemaRef struct {
		Ref string `yaml:"$ref"`
	}
	type requestSchema struct {
		AnyOf []schemaRef `yaml:"anyOf"`
	}
	type operation struct {
		RequestBody struct {
			Required bool `yaml:"required"`
			Content  map[string]struct {
				Schema requestSchema `yaml:"schema"`
			} `yaml:"content"`
		} `yaml:"requestBody"`
	}
	type tenantSchema struct {
		Type                 string               `yaml:"type"`
		AdditionalProperties bool                 `yaml:"additionalProperties"`
		Required             []string             `yaml:"required"`
		Properties           map[string]yaml.Node `yaml:"properties"`
	}
	var document struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatalf("parse human deploy OpenAPI contract: %v", err)
	}
	path := document.Paths["/services/{id}/deployments"]
	postNode := mappingValue(&path, "post")
	if postNode == nil {
		t.Fatal("missing POST /services/{id}/deployments")
	}
	var post operation
	if err := postNode.Decode(&post); err != nil {
		t.Fatalf("decode human deploy operation: %v", err)
	}
	media, ok := post.RequestBody.Content["application/json"]
	if !post.RequestBody.Required || !ok {
		t.Fatal("human deploy request body must be required JSON")
	}
	refs := make(map[string]bool, len(media.Schema.AnyOf))
	for _, candidate := range media.Schema.AnyOf {
		refs[candidate.Ref] = true
	}
	for _, expected := range []string{
		"#/components/schemas/TenantImageDeployRequest",
		"#/components/schemas/DeployIntakeRequest",
	} {
		if !refs[expected] {
			t.Fatalf("deploy request anyOf = %v, missing %s", refs, expected)
		}
	}
	node, ok := document.Components.Schemas["TenantImageDeployRequest"]
	if !ok {
		t.Fatal("missing TenantImageDeployRequest schema")
	}
	var tenant tenantSchema
	if err := node.Decode(&tenant); err != nil {
		t.Fatalf("decode TenantImageDeployRequest: %v", err)
	}
	if tenant.Type != "object" || tenant.AdditionalProperties {
		t.Fatalf("TenantImageDeployRequest type=%q additionalProperties=%v", tenant.Type, tenant.AdditionalProperties)
	}
	if len(tenant.Required) != 2 || tenant.Required[0] != "environment_id" || tenant.Required[1] != "image" {
		t.Fatalf("TenantImageDeployRequest required=%v", tenant.Required)
	}
	if len(tenant.Properties) != 3 ||
		tenant.Properties["environment_id"].Kind == 0 ||
		tenant.Properties["image"].Kind == 0 ||
		tenant.Properties["route"].Kind == 0 {
		t.Fatalf("TenantImageDeployRequest properties=%v", tenant.Properties)
	}
}

func TestHumanUpOpenAPIContractIsNarrowAndHonest(t *testing.T) {
	type parameter struct {
		Ref      string `yaml:"$ref"`
		Name     string `yaml:"name"`
		In       string `yaml:"in"`
		Required bool   `yaml:"required"`
		Schema   struct {
			Type      string `yaml:"type"`
			MaxLength int    `yaml:"maxLength"`
		} `yaml:"schema"`
	}
	type operation struct {
		Description string      `yaml:"description"`
		Parameters  []parameter `yaml:"parameters"`
		RequestBody struct {
			Required bool                 `yaml:"required"`
			Content  map[string]yaml.Node `yaml:"content"`
		} `yaml:"requestBody"`
		Responses map[string]yaml.Node `yaml:"responses"`
	}
	var document struct {
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatalf("parse human up OpenAPI contract: %v", err)
	}
	path := document.Paths["/services/{id}/up"]
	postNode := mappingValue(&path, "post")
	if postNode == nil {
		t.Fatal("missing POST /services/{id}/up")
	}
	var post operation
	if err := postNode.Decode(&post); err != nil {
		t.Fatalf("decode human up operation: %v", err)
	}
	if !post.RequestBody.Required {
		t.Fatal("up archive request body must be required")
	}
	for _, mediaType := range []string{
		"application/gzip",
		"application/x-gzip",
		"application/x-tar+gzip",
		"application/x-compressed-tar",
	} {
		if _, ok := post.RequestBody.Content[mediaType]; !ok {
			t.Fatalf("up request body missing %s", mediaType)
		}
	}
	var environment, message *parameter
	for index := range post.Parameters {
		switch post.Parameters[index].Name {
		case "X-Midnight-Environment":
			environment = &post.Parameters[index]
		case "X-Midnight-Commit-Message":
			message = &post.Parameters[index]
		}
	}
	if environment == nil ||
		environment.In != "header" ||
		!environment.Required ||
		environment.Schema.Type != "string" {
		t.Fatalf("environment header=%#v", environment)
	}
	if message == nil ||
		message.In != "header" ||
		message.Required ||
		message.Schema.Type != "string" ||
		message.Schema.MaxLength != tenantUpMessageMaxBytes {
		t.Fatalf("message header=%#v", message)
	}
	for _, status := range []string{"202", "400", "401", "403", "404", "409", "413", "415", "500", "503"} {
		if _, ok := post.Responses[status]; !ok {
			t.Fatalf("up responses missing %s", status)
		}
	}
	if !strings.Contains(post.Description, "For human sessions, the API never extracts") ||
		!strings.Contains(post.Description, "retain the legacy staged-extraction compatibility path") {
		t.Fatalf("up description does not distinguish human and compatibility behavior: %q", post.Description)
	}
}

func TestDeploymentReadOpenAPIQueryContract(t *testing.T) {
	type parameter struct {
		Ref      string `yaml:"$ref"`
		Name     string `yaml:"name"`
		In       string `yaml:"in"`
		Required bool   `yaml:"required"`
		Schema   struct {
			Type      string `yaml:"type"`
			MinLength int    `yaml:"minLength"`
			Minimum   int    `yaml:"minimum"`
			Maximum   int    `yaml:"maximum"`
			Default   int    `yaml:"default"`
		} `yaml:"schema"`
	}
	type operation struct {
		Parameters []parameter          `yaml:"parameters"`
		Responses  map[string]yaml.Node `yaml:"responses"`
	}
	var document struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Parameters map[string]parameter `yaml:"parameters"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatalf("parse deployment-read OpenAPI contract: %v", err)
	}

	const environmentRef = "#/components/parameters/DeploymentEnvironmentFilter"
	const limitRef = "#/components/parameters/DeploymentPageLimit"
	const cursorRef = "#/components/parameters/DeploymentPageCursor"
	for _, path := range []string{
		"/services/{id}/deployments",
		"/services/{id}/deployments/latest-active",
		"/services/{id}/deployments/{deployment_id}",
	} {
		pathNode, ok := document.Paths[path]
		if !ok {
			t.Fatalf("missing GET %s", path)
		}
		getNode := mappingValue(&pathNode, "get")
		if getNode == nil {
			t.Fatalf("missing GET %s", path)
		}
		var get operation
		if err := getNode.Decode(&get); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		hasEnvironment := false
		hasLimit := false
		hasCursor := false
		for _, value := range get.Parameters {
			if value.Ref == environmentRef {
				hasEnvironment = true
			}
			if value.Ref == limitRef {
				hasLimit = true
			}
			if value.Ref == cursorRef {
				hasCursor = true
			}
		}
		if !hasEnvironment {
			t.Fatalf("GET %s does not declare %s", path, environmentRef)
		}
		if _, ok := get.Responses["400"]; !ok {
			t.Fatalf("GET %s does not document invalid environment filters", path)
		}
		if path == "/services/{id}/deployments" {
			if !hasLimit || !hasCursor {
				t.Fatalf(
					"GET %s pagination refs limit=%t cursor=%t",
					path,
					hasLimit,
					hasCursor,
				)
			}
		} else if hasLimit || hasCursor {
			t.Fatalf("GET %s incorrectly declares collection pagination", path)
		}
	}

	environment, ok := document.Components.Parameters["DeploymentEnvironmentFilter"]
	if !ok {
		t.Fatal("missing DeploymentEnvironmentFilter parameter")
	}
	if environment.Name != "environment_id" ||
		environment.In != "query" ||
		environment.Required ||
		environment.Schema.Type != "string" ||
		environment.Schema.MinLength != 1 {
		t.Fatalf("DeploymentEnvironmentFilter = %#v", environment)
	}
	limit, ok := document.Components.Parameters["DeploymentPageLimit"]
	if !ok ||
		limit.Name != "limit" ||
		limit.In != "query" ||
		limit.Schema.Type != "integer" ||
		limit.Schema.Minimum != 1 ||
		limit.Schema.Maximum != maxDeploymentPageLimit ||
		limit.Schema.Default != defaultDeploymentPageLimit {
		t.Fatalf("DeploymentPageLimit = %#v", limit)
	}
	cursor, ok := document.Components.Parameters["DeploymentPageCursor"]
	if !ok ||
		cursor.Name != "cursor" ||
		cursor.In != "query" ||
		cursor.Schema.Type != "string" ||
		cursor.Schema.MinLength != 1 {
		t.Fatalf("DeploymentPageCursor = %#v", cursor)
	}
}

func requireContractRouteMatch(t *testing.T, operation openAPIRouteOperation, fullPath, class, pattern string) {
	t.Helper()
	if class == "exact" || class == "dispatcher" {
		return
	}
	t.Errorf("%s %s (%s): canonical operation is not served by an exact or explicit subtree route (class=%s pattern=%q)",
		operation.Method, fullPath, operation.OperationID, class, pattern)
}

func classifyServeMuxMatch(mux *http.ServeMux, method, pathTemplate string) (string, string) {
	concretePath := materializeOpenAPIPath(pathTemplate)
	request := httptest.NewRequest(method, concretePath, nil)
	_, pattern := mux.Handler(request)
	if pattern == "" || pattern == "/" {
		return "missing", pattern
	}
	if normalizePathTemplate(pattern) == normalizePathTemplate(pathTemplate) {
		return "exact", pattern
	}
	// A trailing-slash registration is net/http's explicit subtree dispatch
	// mechanism. These handlers perform the remaining operation selection
	// themselves (agents, webhooks, and legacy item/sub-resource families).
	if strings.HasSuffix(pattern, "/") && strings.HasPrefix(concretePath, pattern) {
		return "dispatcher", pattern
	}
	return "mismatch", pattern
}

func materializeOpenAPIPath(pathTemplate string) string {
	var result strings.Builder
	for cursor := 0; cursor < len(pathTemplate); {
		open := strings.IndexByte(pathTemplate[cursor:], '{')
		if open < 0 {
			result.WriteString(pathTemplate[cursor:])
			break
		}
		open += cursor
		result.WriteString(pathTemplate[cursor:open])
		closeOffset := strings.IndexByte(pathTemplate[open:], '}')
		if closeOffset < 0 {
			result.WriteString(pathTemplate[open:])
			break
		}
		closeIndex := open + closeOffset
		parameter := pathTemplate[open+1 : closeIndex]
		switch parameter {
		case "provider":
			result.WriteString("github")
		case "action":
			result.WriteString("restart")
		default:
			result.WriteString("contract-id")
		}
		cursor = closeIndex + 1
	}
	return result.String()
}

func normalizePathTemplate(pathTemplate string) string {
	var result strings.Builder
	for cursor := 0; cursor < len(pathTemplate); {
		open := strings.IndexByte(pathTemplate[cursor:], '{')
		if open < 0 {
			result.WriteString(pathTemplate[cursor:])
			break
		}
		open += cursor
		result.WriteString(pathTemplate[cursor:open])
		closeOffset := strings.IndexByte(pathTemplate[open:], '}')
		if closeOffset < 0 {
			result.WriteString(pathTemplate[open:])
			break
		}
		result.WriteString("{}")
		cursor = open + closeOffset + 1
	}
	return result.String()
}

func parseOpenAPIRouteOperations(t *testing.T) (string, []openAPIRouteOperation) {
	t.Helper()
	var document struct {
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
		Security yaml.Node            `yaml:"security"`
		Paths    map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapiv1alpha.MidnightYAML, &document); err != nil {
		t.Fatalf("parse embedded OpenAPI contract: %v", err)
	}
	if len(document.Servers) != 1 || strings.TrimSpace(document.Servers[0].URL) == "" {
		t.Fatalf("OpenAPI contract must declare one non-empty server URL, got %#v", document.Servers)
	}
	globalSecurity := securitySchemeNames(document.Security)
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"patch": true, "head": true, "options": true, "trace": true,
	}
	var operations []openAPIRouteOperation
	for path, pathNode := range document.Paths {
		experimental := nodeBool(mappingValue(&pathNode, "x-midnight-experimental"))
		for index := 0; index+1 < len(pathNode.Content); index += 2 {
			method := strings.ToLower(pathNode.Content[index].Value)
			if !httpMethods[method] {
				continue
			}
			operationNode := pathNode.Content[index+1]
			operationIDNode := mappingValue(operationNode, "operationId")
			if operationIDNode == nil || strings.TrimSpace(operationIDNode.Value) == "" {
				t.Fatalf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			security := append([]string(nil), globalSecurity...)
			if securityNode := mappingValue(operationNode, "security"); securityNode != nil {
				security = securitySchemeNames(*securityNode)
			}
			operations = append(operations, openAPIRouteOperation{
				Path: path, Method: strings.ToUpper(method), OperationID: operationIDNode.Value,
				Security: security, Experimental: experimental || nodeBool(mappingValue(operationNode, "x-midnight-experimental")),
			})
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Path == operations[j].Path {
			return operations[i].Method < operations[j].Method
		}
		return operations[i].Path < operations[j].Path
	})
	return strings.TrimSuffix(document.Servers[0].URL, "/"), operations
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

func nodeBool(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	var value bool
	return node.Decode(&value) == nil && value
}

func securitySchemeNames(node yaml.Node) []string {
	seen := map[string]bool{}
	for _, requirement := range node.Content {
		if requirement.Kind != yaml.MappingNode {
			continue
		}
		for index := 0; index+1 < len(requirement.Content); index += 2 {
			seen[requirement.Content[index].Value] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func productionRouteContractStack(t *testing.T) DataStack {
	t.Helper()
	sessionStore, accessStore := identity.NewMemoryAuthority()
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store:  sessionStore,
		MACKey: []byte("router-contract-session-mac-key-32"),
	})
	if err != nil {
		t.Fatalf("create identity manager: %v", err)
	}
	keyProvider, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatalf("create variable key provider: %v", err)
	}
	return DataStack{
		APIBearerToken:          "router-contract-operator-token",
		ProductionAuthRequired:  true,
		IdentitySessions:        manager,
		IdentityAccess:          accessStore,
		VariableStore:           secrets.NewVault(keyProvider, secrets.AllowAll{}),
		TenantGitHubSourceLinks: &PostgresTenantGitHubSourceLinkCoordinator{},
		GitHubGrantRecovery:     &PostgresGitHubGrantRecoveryCoordinator{},
		GitHubSetup:             &PostgresGitHubSetupCoordinator{},
	}
}

func probeProductionAuthContract(t *testing.T, handler http.Handler, base string, operations []openAPIRouteOperation) {
	t.Helper()
	for _, operation := range operations {
		if operation.Experimental {
			continue
		}
		body := "{}"
		if operation.OperationID == "registerAgent" {
			body = `{"agent_id":"contract-id"}`
		}
		request := httptest.NewRequest(operation.Method, materializeOpenAPIPath(base+operation.Path), strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		var envelope struct {
			Error struct {
				Code failurecodes.Code `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		operatorRejected := envelope.Error.Code == failurecodes.AuthRequiredMissing ||
			envelope.Error.Code == failurecodes.AuthTokenHeaderInvalid
		declaresOperator := routeContractContains(operation.Security, "BearerAuth")
		declaresHuman := routeContractContains(operation.Security, "HumanSession")
		declaresAgent := routeContractContains(operation.Security, "AgentCredential")
		switch {
		case declaresOperator && (!operatorRejected || response.Code != http.StatusUnauthorized):
			t.Errorf("%s %s (%s): BearerAuth operation was not stopped by the production operator gate: status=%d code=%q",
				operation.Method, operation.Path, operation.OperationID, response.Code, envelope.Error.Code)
		case declaresAgent && (!operatorRejected || response.Code != http.StatusUnauthorized):
			t.Errorf("%s %s (%s): AgentCredential operation did not reject a missing per-agent credential: status=%d code=%q",
				operation.Method, operation.Path, operation.OperationID, response.Code, envelope.Error.Code)
		case !declaresOperator && !declaresHuman && !declaresAgent && operatorRejected:
			t.Errorf("%s %s (%s): operation without BearerAuth drifted behind the operator gate: status=%d code=%q security=%v",
				operation.Method, operation.Path, operation.OperationID, response.Code, envelope.Error.Code, operation.Security)
		case (declaresOperator || declaresAgent) && !strings.HasPrefix(response.Header().Get("WWW-Authenticate"), "Bearer"):
			t.Errorf("%s %s (%s): operator rejection omitted Bearer challenge", operation.Method, operation.Path, operation.OperationID)
		}
	}
}

func routeContractContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
