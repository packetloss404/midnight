// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

// ---- in-memory fakes (parallel to internal/agent/tools/registry_test.go,
//      slimmed down to what the MCP server tests exercise) ---------------

type fakeServices struct {
	mu        sync.Mutex
	projects  []agent.Project
	services  map[string][]agent.Service
	variables map[string]map[string]string
}

func newFakeServices() *fakeServices {
	return &fakeServices{
		projects: []agent.Project{
			{ID: "p1", Name: "demo", Slug: "demo"},
			{ID: "p2", Name: "blog", Slug: "blog"},
		},
		services: map[string][]agent.Service{
			"p1": {{ID: "s1", ProjectID: "p1", Name: "api", LatestStatus: "running"}},
			"p2": {{ID: "s3", ProjectID: "p2", Name: "web", LatestStatus: "failed"}},
		},
		variables: map[string]map[string]string{
			"s1": {"PORT": "8080"},
		},
	}
}

func (f *fakeServices) ListProjects(_ context.Context) ([]agent.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.Project, len(f.projects))
	copy(out, f.projects)
	return out, nil
}
func (f *fakeServices) ListServices(_ context.Context, projectID string) ([]agent.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Service{}, f.services[projectID]...), nil
}
func (f *fakeServices) GetService(_ context.Context, serviceID string) (agent.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range f.services {
		for _, s := range list {
			if s.ID == serviceID {
				vars := make(map[string]string)
				for k, v := range f.variables[serviceID] {
					vars[k] = v
				}
				s.Variables = vars
				return s, nil
			}
		}
	}
	return agent.Service{}, errors.New("not found")
}
func (f *fakeServices) SetVariable(_ context.Context, req agent.VariableWrite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.variables[req.ServiceID]; !ok {
		f.variables[req.ServiceID] = map[string]string{}
	}
	f.variables[req.ServiceID][req.Name] = req.Value
	return nil
}
func (f *fakeServices) UnsetVariable(_ context.Context, req agent.VariableWrite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.variables[req.ServiceID], req.Name)
	return nil
}

type fakeOps struct {
	deployments map[string]agent.Deployment
	logs        map[string][]agent.LogLine
}

func newFakeOps() *fakeOps {
	return &fakeOps{
		deployments: map[string]agent.Deployment{
			"d1": {ID: "d1", ServiceID: "s1", Status: "running"},
		},
		logs: map[string][]agent.LogLine{
			"d1": {{Timestamp: time.Unix(0, 0), Stream: "stdout", Message: "ready"}},
		},
	}
}

func (f *fakeOps) GetDeployment(_ context.Context, id string) (agent.Deployment, error) {
	if d, ok := f.deployments[id]; ok {
		return d, nil
	}
	return agent.Deployment{}, errors.New("not found")
}
func (f *fakeOps) GetLogs(_ context.Context, id string, lines int, _ string) ([]agent.LogLine, error) {
	all := f.logs[id]
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all, nil
}
func (f *fakeOps) Redeploy(_ context.Context, sid string) (agent.Deployment, error) {
	return agent.Deployment{ID: "d-redeploy-" + sid, ServiceID: sid, Status: "queued"}, nil
}
func (f *fakeOps) Rollback(_ context.Context, deploymentID string) (agent.Deployment, error) {
	return agent.Deployment{ID: "d-rollback-" + deploymentID, Status: "queued"}, nil
}

type fakeBuilds struct{}

func (fakeBuilds) DeployFromSource(_ context.Context, sid, _, ref string) (agent.Deployment, error) {
	return agent.Deployment{ID: "d-build-" + sid, ServiceID: sid, Status: "building", CommitSHA: ref}, nil
}

type fakeAudit struct {
	mu      sync.Mutex
	entries []agent.AuditEntry
}

func (f *fakeAudit) Record(_ context.Context, e agent.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

// ---- harness --------------------------------------------------------------

type harness struct {
	server *httptest.Server
	client *http.Client
	token  string
	// counters
	rpcID int
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	if opts.Registry == nil {
		opts.Registry = tools.NewDefaultRegistry()
	}
	if opts.Env.Services == nil {
		opts.Env.Services = newFakeServices()
	}
	if opts.Env.Operations == nil {
		opts.Env.Operations = newFakeOps()
	}
	if opts.Env.Builds == nil {
		opts.Env.Builds = fakeBuilds{}
	}
	if opts.Env.Audit == nil {
		opts.Env.Audit = &fakeAudit{}
	}
	if opts.Env.Actor == "" {
		opts.Env.Actor = "mcp:test"
	}
	srv := httptest.NewServer(NewServer(opts))
	t.Cleanup(srv.Close)
	return &harness{
		server: srv,
		client: srv.Client(),
		token:  opts.APIBearerToken,
	}
}

// rpc sends a single JSON-RPC frame and decodes the response envelope.
// It returns the raw rpcResponse so individual tests can inspect either
// Result or Error.
func (h *harness) rpc(t *testing.T, sessionID, method string, params any, headers map[string]string) (string, rpcResponse) {
	t.Helper()
	h.rpcID++
	idBytes, _ := json.Marshal(h.rpcID)
	frame := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idBytes),
		"method":  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		frame["params"] = json.RawMessage(raw)
	}
	body, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, h.server.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("http error: %v", err)
	}
	defer resp.Body.Close()
	respSession := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode == http.StatusUnauthorized {
		return respSession, rpcResponse{Error: &rpcError{Code: 401, Message: "unauthorized"}}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		var snippet [512]byte
		n, _ := resp.Body.Read(snippet[:])
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(snippet[:n]))
	}
	if resp.StatusCode == http.StatusNoContent {
		return respSession, rpcResponse{}
	}
	var parsed rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return respSession, parsed
}

// initialize is the canonical "session bootstrap" the rest of the tests
// reuse. It returns the session id.
func (h *harness) initialize(t *testing.T, allowMutations bool) string {
	t.Helper()
	headers := map[string]string{}
	if allowMutations {
		headers["X-Midnight-Allow-Mutations"] = "true"
	}
	sid, resp := h.rpc(t, "", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]string{"name": "test", "version": "0.0.0"},
		"capabilities":    map[string]any{},
	}, headers)
	if resp.Error != nil {
		t.Fatalf("initialize errored: %+v", resp.Error)
	}
	if sid == "" {
		t.Fatal("server did not set Mcp-Session-Id")
	}
	return sid
}

// ---- tests ----------------------------------------------------------------

func TestInitializeTransitionsSession(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	// Re-using the session id should land on the same session.
	_, resp := h.rpc(t, sid, "ping", nil, nil)
	if resp.Error != nil {
		t.Fatalf("ping after initialize errored: %+v", resp.Error)
	}
}

func TestToolsListHidesMutationsByDefault(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "tools/list", nil, nil)
	if resp.Error != nil {
		t.Fatalf("tools/list errored: %+v", resp.Error)
	}
	asMap := resp.Result.(map[string]any)
	rawTools, _ := asMap["tools"].([]any)
	if len(rawTools) == 0 {
		t.Fatal("expected at least one tool in the read-only list")
	}
	for _, raw := range rawTools {
		tm := raw.(map[string]any)
		name := tm["name"].(string)
		switch name {
		case "set_variable", "unset_variable", "redeploy", "rollback", "deploy_from_source":
			t.Errorf("mutating tool %q must be hidden when allow_mutations=false", name)
		}
	}
}

func TestToolsListExposesMutationsWhenAllowed(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, true)
	_, resp := h.rpc(t, sid, "tools/list", nil, nil)
	if resp.Error != nil {
		t.Fatalf("tools/list errored: %+v", resp.Error)
	}
	asMap := resp.Result.(map[string]any)
	rawTools := asMap["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range rawTools {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"set_variable", "unset_variable", "redeploy", "rollback", "deploy_from_source"} {
		if !names[want] {
			t.Errorf("expected mutating tool %q to be visible when allow_mutations=true", want)
		}
	}
	for _, want := range []string{"list_projects", "list_services", "get_service", "get_deployment", "get_logs", "get_metrics"} {
		if !names[want] {
			t.Errorf("expected read-only tool %q to be visible", want)
		}
	}
}

func TestToolsCallReadOnlyReturnsTextBlock(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "list_projects",
		"arguments": map[string]any{},
	}, nil)
	if resp.Error != nil {
		t.Fatalf("tools/call errored: %+v", resp.Error)
	}
	r := resp.Result.(map[string]any)
	content := r["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected exactly one content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("expected text block, got %v", block["type"])
	}
	if !strings.Contains(block["text"].(string), `"count":2`) {
		t.Errorf("expected JSON-encoded projects payload, got %q", block["text"])
	}
}

func TestToolsCallMutatingRefusedWithoutAllow(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "set_variable",
		"arguments": map[string]any{"service_id": "s1", "name": "FOO", "value": "bar"},
	}, nil)
	if resp.Error == nil {
		t.Fatal("expected mutation refusal error, got success")
	}
	if resp.Error.Code != rpcErrMutationDisallowed {
		t.Errorf("expected code %d (MIDNIGHT_MUTATION_DISALLOWED), got %d", rpcErrMutationDisallowed, resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "MIDNIGHT_MUTATION_DISALLOWED") {
		t.Errorf("expected refusal sentinel in message, got %q", resp.Error.Message)
	}
}

func TestToolsCallMutatingAllowedWhenFlagSet(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, true)
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "set_variable",
		"arguments": map[string]any{"service_id": "s1", "name": "FOO", "value": "bar"},
	}, nil)
	if resp.Error != nil {
		t.Fatalf("expected success with allow_mutations=true, got error %+v", resp.Error)
	}
}

func TestRateLimitKicksIn(t *testing.T) {
	// Set the cap to 3 calls per minute and confirm the 4th is refused
	// with code -32029.
	h := newHarness(t, Options{
		RateLimitPerMinute: 3,
	})
	sid := h.initialize(t, false)
	for i := 0; i < 3; i++ {
		_, resp := h.rpc(t, sid, "tools/call", map[string]any{
			"name":      "list_projects",
			"arguments": map[string]any{},
		}, nil)
		if resp.Error != nil {
			t.Fatalf("call %d errored: %+v", i, resp.Error)
		}
	}
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "list_projects",
		"arguments": map[string]any{},
	}, nil)
	if resp.Error == nil {
		t.Fatal("expected rate limit error after 3 successful calls")
	}
	if resp.Error.Code != rpcErrRateLimited {
		t.Errorf("expected code %d, got %d (%q)", rpcErrRateLimited, resp.Error.Code, resp.Error.Message)
	}
}

func TestBadBearerReturns401BeforeJSONRPC(t *testing.T) {
	h := newHarness(t, Options{APIBearerToken: "real-token"})
	// Override the harness token with garbage so it sends the wrong
	// bearer.
	h.token = "wrong-token"
	req, _ := http.NewRequest(http.MethodPost, h.server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}
}

func TestAuthorizeRejectsScopedMidnightRecoveryClasses(t *testing.T) {
	for _, token := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			if authorize(token, token) {
				t.Fatalf("MCP accepted scoped token %q as recovery operator", token)
			}
		})
	}
}

func TestAuthorizePreservesLegacyAndAdminRecoveryCompatibility(t *testing.T) {
	for _, token := range []string{
		"legacy-operator-secret",
		"midn_admin_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			if !authorize(token, token) {
				t.Fatalf("MCP rejected compatible recovery token %q", token)
			}
		})
	}
}

func TestUnknownToolReturnsMethodNotFound(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "nonexistent",
		"arguments": map[string]any{},
	}, nil)
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != rpcErrMethodNotFound {
		t.Errorf("expected %d, got %d", rpcErrMethodNotFound, resp.Error.Code)
	}
}

func TestInvalidToolArgsReturnsInvalidParams(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	// list_services requires project_id; this is a tool-level error
	// that surfaces as isError=true content (NOT a JSON-RPC error).
	_, resp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "list_services",
		"arguments": map[string]any{},
	}, nil)
	if resp.Error != nil {
		t.Fatalf("expected isError=true envelope, got rpc error: %+v", resp.Error)
	}
	r := resp.Result.(map[string]any)
	if !r["isError"].(bool) {
		t.Errorf("expected isError=true on tool execution failure, got %#v", r)
	}
}

func TestPromptsListAndGet(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, listResp := h.rpc(t, sid, "prompts/list", nil, nil)
	if listResp.Error != nil {
		t.Fatalf("prompts/list errored: %+v", listResp.Error)
	}
	prompts := listResp.Result.(map[string]any)["prompts"].([]any)
	if len(prompts) != 4 {
		t.Errorf("expected 4 prompts, got %d", len(prompts))
	}
	_, getResp := h.rpc(t, sid, "prompts/get", map[string]any{
		"name":      "diagnose-deploy",
		"arguments": map[string]string{"deployment_id": "d1"},
	}, nil)
	if getResp.Error != nil {
		t.Fatalf("prompts/get errored: %+v", getResp.Error)
	}
	msgs := getResp.Result.(map[string]any)["messages"].([]any)
	body := msgs[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if !strings.Contains(body, "deployment d1") {
		t.Errorf("template not substituted: %q", body)
	}
}

func TestPromptsGetRequiredArg(t *testing.T) {
	h := newHarness(t, Options{})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "prompts/get", map[string]any{
		"name": "diagnose-deploy",
		// missing deployment_id
	}, nil)
	if resp.Error == nil {
		t.Fatal("expected missing-required-arg error")
	}
	if resp.Error.Code != rpcErrInvalidParams {
		t.Errorf("expected %d, got %d", rpcErrInvalidParams, resp.Error.Code)
	}
}

func TestResourcesListAndRead(t *testing.T) {
	svc := newFakeServices()
	ops := newFakeOps()
	h := newHarness(t, Options{
		Env:       agent.Env{Services: svc, Operations: ops, Builds: fakeBuilds{}},
		Resources: NewDefaultResources(svc, ops),
	})
	sid := h.initialize(t, false)
	_, listResp := h.rpc(t, sid, "resources/list", nil, nil)
	if listResp.Error != nil {
		t.Fatalf("resources/list errored: %+v", listResp.Error)
	}
	resources := listResp.Result.(map[string]any)["resources"].([]any)
	if len(resources) != 4 {
		t.Errorf("expected 4 resources in catalog, got %d", len(resources))
	}

	_, readResp := h.rpc(t, sid, "resources/read", map[string]any{
		"uri": "midnight://projects",
	}, nil)
	if readResp.Error != nil {
		t.Fatalf("resources/read errored: %+v", readResp.Error)
	}
	contents := readResp.Result.(map[string]any)["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("expected 1 content slice, got %d", len(contents))
	}
	text := contents[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"count":2`) {
		t.Errorf("unexpected projects payload: %q", text)
	}
}

func TestResourcesReadVariablesRedactsValues(t *testing.T) {
	svc := newFakeServices()
	h := newHarness(t, Options{
		Env:       agent.Env{Services: svc, Operations: newFakeOps(), Builds: fakeBuilds{}},
		Resources: NewDefaultResources(svc, nil),
	})
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "resources/read", map[string]any{
		"uri": "midnight://services/s1/variables",
	}, nil)
	if resp.Error != nil {
		t.Fatalf("errored: %+v", resp.Error)
	}
	contents := resp.Result.(map[string]any)["contents"].([]any)
	text := contents[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "8080") {
		t.Errorf("variable values must be redacted, got %q", text)
	}
	// json.Marshal escapes "<" and ">" as < / > by default
	// (HTML escaping); accept either rendering of the redaction marker.
	if !strings.Contains(text, "<redacted>") && !strings.Contains(text, "\\u003credacted\\u003e") {
		t.Errorf("expected <redacted> marker (literal or escaped), got %q", text)
	}
}

func TestManifestShape(t *testing.T) {
	opts := Options{
		Registry:           tools.NewDefaultRegistry(),
		Endpoint:           "/mcp/v1",
		RateLimitPerMinute: 42,
		DocsURL:            "https://midnight.example/docs/mcp",
		Resources:          NewDefaultResources(newFakeServices(), newFakeOps()),
	}
	srv := httptest.NewServer(NewManifestHandler(opts))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Name != "midnight-mcp" {
		t.Errorf("name mismatch: %q", m.Name)
	}
	if len(m.Transports) != 1 || m.Transports[0].Kind != "http" || m.Transports[0].URL != "/mcp/v1" {
		t.Errorf("transports mismatch: %#v", m.Transports)
	}
	if m.RateLimits.ToolCallsPerMinute != 42 {
		t.Errorf("rate limit mismatch: %d", m.RateLimits.ToolCallsPerMinute)
	}
	if m.MutationGate.RefusalErrorCode != rpcErrMutationDisallowed {
		t.Errorf("mutation gate code mismatch: %d", m.MutationGate.RefusalErrorCode)
	}
	if m.MutationGate.MetadataKey != "allow_mutations" {
		t.Errorf("metadata key mismatch: %q", m.MutationGate.MetadataKey)
	}
	if !m.Capabilities.Tools || !m.Capabilities.Prompts || !m.Capabilities.Resources {
		t.Errorf("capabilities mismatch: %#v", m.Capabilities)
	}
	if m.Docs == "" {
		t.Error("docs URL should be propagated")
	}
}

func TestInitializeMetaAllowMutations(t *testing.T) {
	// Confirm that initialize._meta.allow_mutations also flips the
	// flag (in addition to the header).
	h := newHarness(t, Options{})
	sid, resp := h.rpc(t, "", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]string{"name": "test", "version": "0.0.0"},
		"capabilities":    map[string]any{},
		"_meta":           map[string]any{"allow_mutations": true},
	}, nil)
	if resp.Error != nil {
		t.Fatalf("initialize errored: %+v", resp.Error)
	}
	_, callResp := h.rpc(t, sid, "tools/call", map[string]any{
		"name":      "set_variable",
		"arguments": map[string]any{"service_id": "s1", "name": "FOO", "value": "bar"},
	}, nil)
	if callResp.Error != nil {
		t.Errorf("expected mutation to succeed via _meta.allow_mutations, got %+v", callResp.Error)
	}
}

func TestNoBearerNeededWhenUnconfigured(t *testing.T) {
	// When opts.APIBearerToken is empty (dev default), no auth header
	// is required.
	h := newHarness(t, Options{})
	h.token = ""
	sid := h.initialize(t, false)
	_, resp := h.rpc(t, sid, "ping", nil, nil)
	if resp.Error != nil {
		t.Fatalf("expected ping to succeed with no auth, got %+v", resp.Error)
	}
}

func TestSessionsNotInitializedRefuseToolsCall(t *testing.T) {
	h := newHarness(t, Options{})
	// Forge a session id by pinging an unknown id — the server
	// allocates a fresh uninitialized session for the unknown id.
	_, resp := h.rpc(t, "", "tools/list", nil, nil)
	if resp.Error == nil {
		t.Fatal("expected refusal because session not initialized")
	}
	if resp.Error.Code != rpcErrInvalidRequest {
		t.Errorf("expected %d, got %d", rpcErrInvalidRequest, resp.Error.Code)
	}
}
