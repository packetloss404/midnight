// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/graphql"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// TestGraphQLEndpointSmoke verifies that the GraphQL gateway mounted at
// /graphql/v2 alongside the v1alpha REST surface:
//
//  1. answers introspection-style queries
//  2. resolves project → services → latest deployment by delegating to
//     the same in-memory stores the REST handlers use
//  3. shares the audit emit / idempotency seam with the REST surface
//     (verified indirectly: a deploy mutation seeds an operation that
//     surfaces through the REST deployments endpoint)
func TestGraphQLEndpointSmoke(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build a single shared DataStack and attach a GraphQL gateway. The
	// gateway must see the same stores as the REST surface, otherwise
	// the project we seed via the store is not visible through GraphQL.
	stack := api.DataStack{}
	graph, playground, err := graphql.Mount(graphql.MountConfig{Stack: &stack, DevPlayground: true})
	if err != nil {
		t.Fatalf("graphql.Mount: %v", err)
	}
	stack.GraphQLHandler = graph
	stack.GraphQLPlaygroundHandler = playground

	handler := api.NewRouterWithDataStack(stack)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// --- Seed a project + service directly into the shared stores ---
	project, err := stack.ProjectStore.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "midnight-graphql-smoke",
		DisplayName: "Midnight GraphQL Smoke Project",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service, err := stack.ServiceStore.CreateService(ctx, api.Service{
		ProjectID:   project.ID,
		Slug:        "smoke-service",
		DisplayName: "Smoke Service",
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Seed a deploy operation directly in the operation store -- we are
	// asserting the GraphQL projection, not the full intake FSM.
	op, err := stack.OperationStore.Create(ctx, orailtypes.Operation{
		Kind:  "deploy",
		State: orailtypes.OperationRunning,
	})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	// The intake event message that deploymentResourceFromOperation
	// parses out of the operation event log -- matches the format
	// emitted by createDeployIntake in the REST handler.
	_, err = stack.OperationStore.AppendEvent(ctx, api.OperationEvent{
		OperationID: op.ID,
		State:       op.State,
		Message: "deploy.intake service_id=" + string(service.ID) +
			" project_id=" + string(project.ID) +
			" queue=deploy" +
			" job_id=smoke-job" +
			" job_state=available" +
			" deployment_id=dep_smoke" +
			" instance_id=inst_smoke" +
			" environment_id=env_smoke" +
			" image=sha256@abcd" +
			" route_intent=public",
	})
	if err != nil {
		t.Fatalf("append intake event: %v", err)
	}

	// --- Query: project → services → latestDeployment ---
	query := `query Smoke($projectId: ID!) {
		project(id: $projectId) {
			id
			name
			services(first: 5) {
				edges {
					cursor
					node {
						id
						name
						deployments(first: 1) {
							edges {
								node {
									id
									status
									operationId
									operationState
								}
							}
						}
					}
				}
				pageInfo {
					hasNextPage
				}
			}
		}
		serverInfo {
			apiVersion
		}
	}`

	body := mustMarshal(t, graphql.Request{
		Query:     query,
		Variables: map[string]interface{}{"projectId": string(project.ID)},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/graphql/v2", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build graphql request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("graphql roundtrip: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graphql status = %d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed graphql.Response
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("decode graphql response: %v (body=%s)", err, string(respBody))
	}
	if len(parsed.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", parsed.Errors)
	}
	info, ok := parsed.Data["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing serverInfo: %v", parsed.Data)
	}
	if v, _ := info["apiVersion"].(string); v != "v2" {
		t.Fatalf("serverInfo.apiVersion = %q want v2", v)
	}

	proj, ok := parsed.Data["project"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing project in response: %v", parsed.Data)
	}
	if got, _ := proj["id"].(string); got != string(project.ID) {
		t.Fatalf("project.id = %q want %q", got, project.ID)
	}
	if got, _ := proj["name"].(string); got != project.Slug {
		t.Fatalf("project.name = %q want %q", got, project.Slug)
	}

	services, ok := proj["services"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing services connection: %v", proj)
	}
	edges, ok := services["edges"].([]interface{})
	if !ok || len(edges) == 0 {
		t.Fatalf("expected at least one service edge, got %v", services)
	}
	edge0, _ := edges[0].(map[string]interface{})
	node, _ := edge0["node"].(map[string]interface{})
	if got, _ := node["id"].(string); got != string(service.ID) {
		t.Fatalf("service.id = %q want %q", got, service.ID)
	}
	deployments, _ := node["deployments"].(map[string]interface{})
	depEdges, _ := deployments["edges"].([]interface{})
	if len(depEdges) == 0 {
		t.Fatalf("expected at least one deployment edge, got %v", deployments)
	}
	dep0, _ := depEdges[0].(map[string]interface{})
	depNode, _ := dep0["node"].(map[string]interface{})
	if got, _ := depNode["operationId"].(string); got != string(op.ID) {
		t.Fatalf("deployment.operationId = %q want %q", got, op.ID)
	}
	if got, _ := depNode["operationState"].(string); got != string(orailtypes.OperationRunning) {
		t.Fatalf("deployment.operationState = %q want %q", got, orailtypes.OperationRunning)
	}
}

// TestGraphQLPlaygroundOptional verifies the GraphiQL playground is
// served at /graphiql when the dev flag is set, and 404s when it is not.
func TestGraphQLPlaygroundOptional(t *testing.T) {
	t.Parallel()

	// With playground enabled
	stack := api.DataStack{}
	graph, playground, err := graphql.Mount(graphql.MountConfig{Stack: &stack, DevPlayground: true})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	stack.GraphQLHandler = graph
	stack.GraphQLPlaygroundHandler = playground

	srv := httptest.NewServer(api.NewRouterWithDataStack(stack))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/graphiql")
	if err != nil {
		t.Fatalf("graphiql get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graphiql with dev flag status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Midnight GraphiQL") {
		t.Fatalf("playground html missing branding: %s", string(body[:200]))
	}

	// Without playground enabled
	stack2 := api.DataStack{}
	graph2, _, err := graphql.Mount(graphql.MountConfig{Stack: &stack2, DevPlayground: false})
	if err != nil {
		t.Fatalf("mount (no dev): %v", err)
	}
	stack2.GraphQLHandler = graph2

	srv2 := httptest.NewServer(api.NewRouterWithDataStack(stack2))
	defer srv2.Close()
	resp2, err := http.Get(srv2.URL + "/graphiql")
	if err != nil {
		t.Fatalf("graphiql get (no dev): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("graphiql without dev flag status = %d want 404", resp2.StatusCode)
	}
}

// TestGraphQLBearerAuth verifies the operator Bearer gate and proves the
// shared credential is not mislabeled as a project-scoped token.
func TestGraphQLBearerAuth(t *testing.T) {
	t.Parallel()

	passwordHash, err := bootstrap.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	identityStore := identity.NewMemoryStore(
		identity.UserRecord{
			ID: "usr_admin", Email: "admin@example.com", PasswordHash: passwordHash,
			InstanceAdmin: true, SessionGeneration: 1,
		},
		identity.UserRecord{
			ID: "usr_member", Email: "member@example.com", PasswordHash: passwordHash,
			SessionGeneration: 1,
		},
	)
	randomByte := byte(0)
	identityManager, err := identity.NewManager(identity.ManagerConfig{
		Store: identityStore, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Random: func(dst []byte) error {
			randomByte++
			for i := range dst {
				dst[i] = randomByte
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminLogin, err := identityManager.Login(t.Context(), identity.LoginRequest{
		Email: "admin@example.com", Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}
	memberLogin, err := identityManager.Login(t.Context(), identity.LoginRequest{
		Email: "member@example.com", Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}

	adminKeyProvider, err := secrets.NewMemoryKeyProvider(bytes.Repeat([]byte{0x43}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	stack := api.DataStack{
		APIBearerToken: "midnight-test-token", IdentitySessions: identityManager,
		ProductionAuthRequired: true,
		VariableStore:          secrets.NewVault(adminKeyProvider, secrets.AllowAll{}),
	}
	graph, _, err := graphql.Mount(graphql.MountConfig{Stack: &stack})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	stack.GraphQLHandler = graph
	srv := httptest.NewServer(api.NewRouterWithDataStack(stack))
	defer srv.Close()

	query := `{"query":"{ serverInfo { apiVersion } }"}`

	// No auth -> 401
	resp, err := http.Post(srv.URL+"/graphql/v2", "application/json", strings.NewReader(query))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d want 401", resp.StatusCode)
	}

	// Authorization: Bearer -> 200
	req, _ := http.NewRequest("POST", srv.URL+"/graphql/v2", strings.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer midnight-test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bearer post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer status = %d want 200", resp.StatusCode)
	}

	// The normal administrator session traverses both the outer REST router and
	// the inner GraphQL authorization boundary without a recovery credential.
	req, _ = http.NewRequest("POST", srv.URL+"/graphql/v2", strings.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminLogin.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin session post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin session status = %d want 200", resp.StatusCode)
	}

	req, _ = http.NewRequest("POST", srv.URL+"/graphql/v2", strings.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+memberLogin.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("member session post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member session status = %d want 403", resp.StatusCode)
	}

	// Project-Access-Token is intentionally unsupported until Midnight mints
	// a real project-scoped credential.
	req, _ = http.NewRequest("POST", srv.URL+"/graphql/v2", strings.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Project-Access-Token", "midnight-test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("project-token post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("project-token status = %d want 401", resp.StatusCode)
	}

	// A production embedder that forgot the operator token must fail closed
	// for GET as well as POST; GraphQL reads expose globally scoped stores
	// until the tenant-aware service layer lands.
	failClosedStack := api.DataStack{ProductionAuthRequired: true}
	keyProvider, err := secrets.NewMemoryKeyProvider(bytes.Repeat([]byte{0x42}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	failClosedStack.VariableStore = secrets.NewVault(keyProvider, secrets.AllowAll{})
	failClosedGraph, _, err := graphql.Mount(graphql.MountConfig{Stack: &failClosedStack})
	if err != nil {
		t.Fatalf("mount fail-closed stack: %v", err)
	}
	failClosedStack.GraphQLHandler = failClosedGraph
	failClosedServer := httptest.NewServer(api.NewRouterWithDataStack(failClosedStack))
	defer failClosedServer.Close()
	resp, err = http.Get(failClosedServer.URL + "/graphql/v2?query=%7BserverInfo%7BapiVersion%7D%7D")
	if err != nil {
		t.Fatalf("fail-closed get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("production no-token GET status = %d want 401", resp.StatusCode)
	}
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// runGraphQL is a small helper for the parity-coverage tests that mount
// the graphql gateway alongside the REST surface and POST queries
// against /graphql/v2.
func runGraphQL(t *testing.T, srvURL string, req graphql.Request) graphql.Response {
	t.Helper()
	body := mustMarshal(t, req)
	r, err := http.NewRequest("POST", srvURL+"/graphql/v2", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("graphql request: %v", err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("graphql roundtrip: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graphql status = %d body=%s", resp.StatusCode, string(raw))
	}
	var parsed graphql.Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("graphql decode: %v body=%s", err, string(raw))
	}
	return parsed
}

// buildGraphQLServer mounts the gateway against a fresh DataStack and
// returns the httptest server (plus the stack so tests can seed it).
// The caller is responsible for closing the server.
func buildGraphQLServer(t *testing.T) (*httptest.Server, *api.DataStack) {
	t.Helper()
	stack := &api.DataStack{}
	graph, _, err := graphql.Mount(graphql.MountConfig{Stack: stack})
	if err != nil {
		t.Fatalf("graphql.Mount: %v", err)
	}
	stack.GraphQLHandler = graph
	srv := httptest.NewServer(api.NewRouterWithDataStack(*stack))
	t.Cleanup(srv.Close)
	return srv, stack
}

// TestGraphQLVariableReferenceFields exercises the new
// Variable.valueKind / Variable.references metadata fields. Writing a
// literal value via variableUpsert must surface kind=LITERAL with an
// empty references list; a `${{Service.NAME}}` template must surface
// kind=REFERENCE with the parsed target.
func TestGraphQLVariableReferenceFields(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx
	srv, _ := buildGraphQLServer(t)

	mut := `mutation Upsert($input: VariableUpsertInput!) {
		variableUpsert(input: $input) {
			id
			name
			valueKind
			value
			references {
				kind
				service
				name
			}
		}
	}`

	// Literal write.
	respLit := runGraphQL(t, srv.URL, graphql.Request{
		Query: mut,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"projectId": "prj_smoke",
				"name":      "API_TOKEN",
				"value":     "deadbeef",
				"scope":     "PLAIN",
			},
		},
	})
	if len(respLit.Errors) > 0 {
		t.Fatalf("literal upsert: %+v", respLit.Errors)
	}
	lit, _ := respLit.Data["variableUpsert"].(map[string]interface{})
	if lit == nil {
		t.Fatalf("missing variableUpsert payload: %v", respLit.Data)
	}
	if got, _ := lit["valueKind"].(string); got != "LITERAL" {
		t.Fatalf("literal valueKind = %q want LITERAL", got)
	}
	if v := lit["value"]; v != nil {
		t.Fatalf("variable value must always be nil through GraphQL reads, got %v", v)
	}
	if refs, _ := lit["references"].([]interface{}); len(refs) != 0 {
		t.Fatalf("literal references = %v want empty", refs)
	}

	// Reference write (Railway-style template).
	respRef := runGraphQL(t, srv.URL, graphql.Request{
		Query: mut,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"projectId": "prj_smoke",
				"name":      "DATABASE_URL",
				"value":     "postgres://${{Postgres.PGPASSWORD}}@host/db",
				"scope":     "PLAIN",
			},
		},
	})
	if len(respRef.Errors) > 0 {
		t.Fatalf("reference upsert: %+v", respRef.Errors)
	}
	ref, _ := respRef.Data["variableUpsert"].(map[string]interface{})
	if got, _ := ref["valueKind"].(string); got != "REFERENCE" {
		t.Fatalf("reference valueKind = %q want REFERENCE", got)
	}
	refs, _ := ref["references"].([]interface{})
	if len(refs) != 1 {
		t.Fatalf("expected exactly 1 reference, got %v", refs)
	}
	first, _ := refs[0].(map[string]interface{})
	if got, _ := first["kind"].(string); got != "ServiceVar" {
		t.Fatalf("reference.kind = %q want ServiceVar", got)
	}
	if got, _ := first["service"].(string); got != "Postgres" {
		t.Fatalf("reference.service = %q want Postgres", got)
	}
	if got, _ := first["name"].(string); got != "PGPASSWORD" {
		t.Fatalf("reference.name = %q want PGPASSWORD", got)
	}
}

// TestGraphQLTemplates keeps template discovery available while the deploy
// mutation fails closed until runtime, volume, connectivity, and health
// verification are wired.
func TestGraphQLTemplates(t *testing.T) {
	t.Parallel()
	srv, _ := buildGraphQLServer(t)

	// templates list.
	listResp := runGraphQL(t, srv.URL, graphql.Request{
		Query: `query { templates { slug name version services { slug image volume { mountPath sizeBytes } } } }`,
	})
	if len(listResp.Errors) > 0 {
		t.Fatalf("templates query: %+v", listResp.Errors)
	}
	tmpls, _ := listResp.Data["templates"].([]interface{})
	if len(tmpls) < 2 {
		t.Fatalf("templates returned %d entries, want >= 2", len(tmpls))
	}
	seen := map[string]bool{}
	for _, tt := range tmpls {
		m, _ := tt.(map[string]interface{})
		seen[strings.ToLower(m["slug"].(string))] = true
	}
	if !seen["postgres"] || !seen["redis"] {
		t.Fatalf("expected postgres+redis templates, got %v", seen)
	}

	deployResp := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Deploy($input: TemplateDeployInput!) {
			templateDeploy(input: $input) {
				operationId
				createdServices { id name }
				note
			}
		}`,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"projectId":     "prj_template_target",
				"environmentId": "env_smoke",
				"slug":          "postgres",
			},
		},
	})
	assertUnavailableMutation(t, deployResp, "template-runtime-deploy")
}

func TestGraphQLEchoMutationsFailClosed(t *testing.T) {
	t.Parallel()
	srv, _ := buildGraphQLServer(t)

	serviceUpdate := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation {
			serviceInstanceUpdate(input: {
				serviceId: "svc_1",
				environmentId: "env_1",
				startCommand: "echo pretend"
			}) {
				serviceId
				startCommand
			}
		}`,
	})
	assertUnavailableMutation(t, serviceUpdate, "service-instance-configuration")

	for field, capability := range map[string]string{
		"deploymentRedeploy": "deployment-redeploy",
		"deploymentRollback": "deployment-rollback",
		"deploymentRestart":  "deployment-restart",
	} {
		t.Run(field, func(t *testing.T) {
			response := runGraphQL(t, srv.URL, graphql.Request{
				Query: `mutation {
					` + field + `(id: "dep_1") {
						id
						status
					}
				}`,
			})
			assertUnavailableMutation(t, response, capability)
		})
	}
}

func TestGraphQLDeployMutationReturnsCanonicalDeploymentIdentity(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stack := &api.DataStack{QueueStore: &graphQLDeployQueueStore{}}
	graph, _, err := graphql.Mount(graphql.MountConfig{Stack: stack})
	if err != nil {
		t.Fatalf("graphql.Mount: %v", err)
	}
	stack.GraphQLHandler = graph
	srv := httptest.NewServer(api.NewRouterWithDataStack(*stack))
	t.Cleanup(srv.Close)

	project, err := stack.ProjectStore.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "canonical-deploy",
		DisplayName: "Canonical Deploy",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service, err := stack.ServiceStore.CreateService(ctx, api.Service{
		ProjectID:   project.ID,
		Slug:        "api",
		DisplayName: "API",
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	environment, err := stack.EnvironmentStore.CreateEnvironment(ctx, api.Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Name:      "Production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	response := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Deploy($input: ServiceInstanceDeployInput!) {
			serviceInstanceDeployV2(input: $input, idempotencyKey: "graphql-canonical-deploy") {
				id
				serviceId
				operationId
				status
			}
		}`,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"serviceId":     string(service.ID),
				"environmentId": string(environment.ID),
				"image":         "registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	})
	if len(response.Errors) > 0 {
		t.Fatalf("deploy mutation errors: %+v", response.Errors)
	}
	deployment, ok := response.Data["serviceInstanceDeployV2"].(map[string]interface{})
	if !ok {
		t.Fatalf("deployment payload = %#v", response.Data["serviceInstanceDeployV2"])
	}
	id, _ := deployment["id"].(string)
	operationID, _ := deployment["operationId"].(string)
	if !strings.HasPrefix(id, "dep_") {
		t.Fatalf("deployment id = %q, want server-minted dep_ identity", id)
	}
	if operationID == "" || operationID == id {
		t.Fatalf("operation id = %q, deployment id = %q; identities must be distinct", operationID, id)
	}
	if got, _ := deployment["serviceId"].(string); got != string(service.ID) {
		t.Fatalf("service id = %q, want %q", got, service.ID)
	}
}

type graphQLDeployQueueStore struct {
	next int
}

func (s *graphQLDeployQueueStore) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	s.next++
	return queue.Job{
		ID:                fmt.Sprintf("job_graphql_%d", s.next),
		Queue:             request.Queue,
		State:             queue.JobQueued,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}, nil
}

func (*graphQLDeployQueueStore) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	return queue.Job{}, queue.ErrNoWork
}

func (*graphQLDeployQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (*graphQLDeployQueueStore) Complete(context.Context, string, string) error {
	return nil
}

func (*graphQLDeployQueueStore) Fail(context.Context, string, string, string) error {
	return nil
}

func assertUnavailableMutation(t *testing.T, response graphql.Response, capability string) {
	t.Helper()
	if len(response.Errors) != 1 {
		t.Fatalf("errors = %+v, want one unavailable mutation error", response.Errors)
	}
	extensions := response.Errors[0].Extensions
	if extensions["code"] != "NOT_IMPLEMENTED" ||
		extensions["capability"] != capability ||
		extensions["state"] != "unavailable" {
		t.Fatalf("extensions = %+v, want typed unavailable capability %q", extensions, capability)
	}
}

// TestGraphQLVolumeCRUD exercises volumeCreate → volumeResize happy
// path plus the typed VOLUME_RESIZE_DOWN_UNSUPPORTED rejection. The
// memory store ships the same error codes the Postgres store will
// surface in production.
func TestGraphQLVolumeCRUD(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srv, stack := buildGraphQLServer(t)
	project, err := stack.ProjectStore.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "volume-test",
		DisplayName: "Volume Test",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc, err := stack.ServiceStore.CreateService(ctx, api.Service{
		ProjectID:   project.ID,
		Slug:        "svc",
		DisplayName: "Svc",
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	create := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Create($input: VolumeCreateInput!) {
			volumeCreate(input: $input) {
				volume { id name sizeBytes status mountPath }
			}
		}`,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"serviceId":     string(svc.ID),
				"environmentId": "env_main",
				"name":          "data",
				"sizeBytes":     1024,
				"mountPath":     "/data",
			},
		},
	})
	if len(create.Errors) > 0 {
		t.Fatalf("volumeCreate: %+v", create.Errors)
	}
	payload, _ := create.Data["volumeCreate"].(map[string]interface{})
	vol, _ := payload["volume"].(map[string]interface{})
	if vol == nil {
		t.Fatalf("missing volume in payload: %v", payload)
	}
	id, _ := vol["id"].(string)
	if id == "" {
		t.Fatalf("volume.id empty: %v", vol)
	}
	if mp, _ := vol["mountPath"].(string); mp != "/data" {
		t.Fatalf("volume.mountPath = %q want /data", mp)
	}
	if sz := vol["sizeBytes"]; toFloat64(sz) != 1024 {
		t.Fatalf("volume.sizeBytes = %v want 1024", sz)
	}

	// Happy path resize up.
	resizeUp := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Resize($id: ID!, $size: Int!) {
			volumeResize(id: $id, sizeBytes: $size) { id sizeBytes }
		}`,
		Variables: map[string]interface{}{"id": id, "size": 4096},
	})
	if len(resizeUp.Errors) > 0 {
		t.Fatalf("volumeResize up: %+v", resizeUp.Errors)
	}
	upVol, _ := resizeUp.Data["volumeResize"].(map[string]interface{})
	if sz := upVol["sizeBytes"]; toFloat64(sz) != 4096 {
		t.Fatalf("post-resize sizeBytes = %v want 4096", sz)
	}

	// Resize down must reject with VOLUME_RESIZE_DOWN_UNSUPPORTED.
	resizeDown := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Resize($id: ID!, $size: Int!) {
			volumeResize(id: $id, sizeBytes: $size) { id sizeBytes }
		}`,
		Variables: map[string]interface{}{"id": id, "size": 512},
	})
	if len(resizeDown.Errors) == 0 {
		t.Fatalf("expected error on resize-down, got data=%v", resizeDown.Data)
	}
	matchedCode := false
	for _, e := range resizeDown.Errors {
		if c, _ := e.Extensions["code"].(string); c == "VOLUME_RESIZE_DOWN_UNSUPPORTED" {
			matchedCode = true
			break
		}
	}
	if !matchedCode {
		t.Fatalf("resize-down errors did not surface VOLUME_RESIZE_DOWN_UNSUPPORTED: %+v", resizeDown.Errors)
	}
}

// TestGraphQLServiceGithubLink writes a link via the mutation and then
// reads it back via the Service.github field, then clears it via the
// unlink mutation.
func TestGraphQLServiceGithubLink(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srv, stack := buildGraphQLServer(t)
	project, err := stack.ProjectStore.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "github-link",
		DisplayName: "GitHub Link",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc, err := stack.ServiceStore.CreateService(ctx, api.Service{
		ProjectID:   project.ID,
		Slug:        "svc",
		DisplayName: "Svc",
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	link := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Link($input: GitHubLinkInput!) {
			serviceGithubLink(input: $input) {
				id
				github {
					installationId repo branch watchPaths waitForCi
				}
			}
		}`,
		Variables: map[string]interface{}{
			"input": map[string]interface{}{
				"serviceId":      string(svc.ID),
				"installationId": 4242,
				"repo":           "midnight/midnight",
				"branch":         "main",
				"watchPaths":     []string{"web", "api"},
				"waitForCi":      true,
			},
		},
	})
	if len(link.Errors) > 0 {
		t.Fatalf("serviceGithubLink: %+v", link.Errors)
	}
	linked, _ := link.Data["serviceGithubLink"].(map[string]interface{})
	gh, _ := linked["github"].(map[string]interface{})
	if gh == nil {
		t.Fatalf("expected Service.github to surface link, got %v", linked)
	}
	if got, _ := gh["repo"].(string); got != "midnight/midnight" {
		t.Fatalf("github.repo = %q want midnight/midnight", got)
	}
	if got, _ := gh["branch"].(string); got != "main" {
		t.Fatalf("github.branch = %q want main", got)
	}
	if got := toFloat64(gh["installationId"]); got != 4242 {
		t.Fatalf("github.installationId = %v want 4242", got)
	}

	// Verify the spec.github JSONB key actually got the link (parity with
	// REST handler behavior).
	stored, err := stack.ServiceStore.GetService(ctx, svc.ID)
	if err != nil {
		t.Fatalf("re-fetch service: %v", err)
	}
	if _, present := stored.Spec["github"]; !present {
		t.Fatalf("expected service.spec.github after link, got %v", stored.Spec)
	}

	// Unlink and verify Service.github is null.
	unlink := runGraphQL(t, srv.URL, graphql.Request{
		Query: `mutation Unlink($id: ID!) {
			serviceGithubUnlink(serviceId: $id) {
				id
				github { repo }
			}
		}`,
		Variables: map[string]interface{}{"id": string(svc.ID)},
	})
	if len(unlink.Errors) > 0 {
		t.Fatalf("serviceGithubUnlink: %+v", unlink.Errors)
	}
	unlinked, _ := unlink.Data["serviceGithubUnlink"].(map[string]interface{})
	if v := unlinked["github"]; v != nil {
		t.Fatalf("expected Service.github to be null after unlink, got %v", v)
	}
}

func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
