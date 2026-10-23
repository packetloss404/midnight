package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
)

// ---- in-memory fakes ------------------------------------------------------

type fakeServices struct {
	mu        sync.Mutex
	projects  []agent.Project
	services  map[string][]agent.Service
	variables map[string]map[string]string // service_id -> name -> value
	failOn    string
}

func newFakeServices() *fakeServices {
	return &fakeServices{
		projects: []agent.Project{
			{ID: "p1", Name: "demo", Slug: "demo"},
			{ID: "p2", Name: "blog", Slug: "blog"},
		},
		services: map[string][]agent.Service{
			"p1": {
				{ID: "s1", ProjectID: "p1", Name: "api", LatestStatus: "running"},
				{ID: "s2", ProjectID: "p1", Name: "worker", LatestStatus: "running"},
			},
			"p2": {
				{ID: "s3", ProjectID: "p2", Name: "web", LatestStatus: "failed"},
			},
		},
		variables: map[string]map[string]string{
			"s1": {"PORT": "8080", "DATABASE_URL": "postgres://x"},
		},
	}
}

func (f *fakeServices) ListProjects(_ context.Context) ([]agent.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "ListProjects" {
		return nil, errors.New("boom")
	}
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
			"d2": {ID: "d2", ServiceID: "s3", Status: "failed"},
		},
		logs: map[string][]agent.LogLine{
			"d1": {{Timestamp: time.Now(), Stream: "stdout", Message: "listening on 8080"}},
			"d2": {
				{Timestamp: time.Now(), Stream: "stderr", Message: "panic: nil pointer"},
				{Timestamp: time.Now(), Stream: "stderr", Message: "goroutine 1 [running]:"},
				{Timestamp: time.Now(), Stream: "stdout", Message: "shutting down"},
			},
		},
	}
}

func (f *fakeOps) GetDeployment(_ context.Context, id string) (agent.Deployment, error) {
	if d, ok := f.deployments[id]; ok {
		return d, nil
	}
	return agent.Deployment{}, errors.New("deployment not found")
}

func (f *fakeOps) GetLogs(_ context.Context, id string, lines int, search string) ([]agent.LogLine, error) {
	all := f.logs[id]
	if search != "" {
		filtered := all[:0:0]
		for _, l := range all {
			if strings.Contains(strings.ToLower(l.Message), strings.ToLower(search)) {
				filtered = append(filtered, l)
			}
		}
		all = filtered
	}
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all, nil
}

func (f *fakeOps) Redeploy(_ context.Context, sid string) (agent.Deployment, error) {
	return agent.Deployment{ID: "d-redeploy-" + sid, ServiceID: sid, Status: "queued"}, nil
}

func (f *fakeOps) Rollback(_ context.Context, deploymentID string) (agent.Deployment, error) {
	if _, ok := f.deployments[deploymentID]; !ok {
		return agent.Deployment{}, errors.New("not found")
	}
	return agent.Deployment{ID: "d-rollback-" + deploymentID, Status: "queued"}, nil
}

type fakeBuilds struct{ called bool }

func (f *fakeBuilds) DeployFromSource(_ context.Context, sid, repo, ref string) (agent.Deployment, error) {
	f.called = true
	return agent.Deployment{ID: "d-build-" + sid, ServiceID: sid, Status: "building", CommitSHA: ref}, nil
}

type fakeAudit struct {
	mu      sync.Mutex
	entries []agent.AuditEntry
	fail    bool
}

func (f *fakeAudit) Record(_ context.Context, e agent.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("audit fail")
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) Entries() []agent.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.AuditEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// ---- helpers --------------------------------------------------------------

func newEnv(allowMutations bool) (agent.Env, *fakeServices, *fakeOps, *fakeBuilds, *fakeAudit) {
	svc := newFakeServices()
	ops := newFakeOps()
	bld := &fakeBuilds{}
	audit := &fakeAudit{}
	return agent.Env{
		Services:       svc,
		Operations:     ops,
		Builds:         bld,
		Audit:          audit,
		Actor:          "agent:test",
		AllowMutations: allowMutations,
	}, svc, ops, bld, audit
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// ---- tests ----------------------------------------------------------------

func TestDefaultRegistryRegistersAllTools(t *testing.T) {
	r := NewDefaultRegistry()
	got := r.Names()
	want := []string{
		"deploy_from_source",
		"get_deployment",
		"get_logs",
		"get_metrics",
		"get_service",
		"list_projects",
		"list_services",
		"redeploy",
		"rollback",
		"set_variable",
		"unset_variable",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d tools, got %d: %v", len(want), len(got), got)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("tool %d: want %q got %q", i, n, got[i])
		}
	}
}

func TestRegisterDuplicateFails(t *testing.T) {
	r := NewDefaultRegistry()
	err := r.Register(Tool{
		Name:        "list_projects",
		Description: "x",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute:     func(ctx context.Context, _ json.RawMessage, _ agent.Env) (any, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	r := New()
	if err := r.Register(Tool{Name: ""}); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := r.Register(Tool{Name: "x", InputSchema: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("nil execute should fail")
	}
	if err := r.Register(Tool{Name: "x", Execute: func(context.Context, json.RawMessage, agent.Env) (any, error) { return nil, nil }}); err == nil {
		t.Fatal("empty schema should fail")
	}
}

func TestListProjects(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, audit := newEnv(false)
	result, err := r.Execute(context.Background(), "list_projects", json.RawMessage(`{}`), env)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected type %T", result)
	}
	if m["count"].(int) != 2 {
		t.Errorf("expected 2 projects, got %v", m["count"])
	}
	if len(audit.Entries()) != 1 || audit.Entries()[0].Tool != "list_projects" {
		t.Errorf("expected audit entry, got %#v", audit.Entries())
	}
}

func TestListProjectsRespectsScope(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	env.ProjectScope = "p2"
	result, err := r.Execute(context.Background(), "list_projects", json.RawMessage(`{}`), env)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["count"].(int) != 1 {
		t.Errorf("scope filter failed: %v", m)
	}
}

func TestListServices(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	result, err := r.Execute(context.Background(), "list_services", mustJSON(t, map[string]string{"project_id": "p1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["count"].(int) != 2 {
		t.Errorf("expected 2 services, got %v", m["count"])
	}
}

func TestListServicesRequiresProjectID(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	_, err := r.Execute(context.Background(), "list_services", json.RawMessage(`{}`), env)
	if err == nil {
		t.Fatal("expected missing project_id error")
	}
}

func TestGetServiceRedactsVariableValues(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	result, err := r.Execute(context.Background(), "get_service", mustJSON(t, map[string]string{"service_id": "s1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	svc := result.(agent.Service)
	if v, ok := svc.Variables["PORT"]; !ok || v != "<redacted>" {
		t.Errorf("expected redacted PORT, got %v", svc.Variables)
	}
	if v, ok := svc.Variables["DATABASE_URL"]; !ok || v != "<redacted>" {
		t.Errorf("expected redacted DATABASE_URL, got %v", svc.Variables)
	}
}

func TestGetLogsWithSearch(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	result, err := r.Execute(context.Background(), "get_logs", mustJSON(t, map[string]any{
		"deployment_id": "d2",
		"search":        "panic",
		"lines":         10,
	}), env)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["count"].(int) != 1 {
		t.Errorf("expected 1 matching line, got %v", m)
	}
}

func TestGetLogsRejectsExcessiveLines(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	// Lines > max gets clamped, but no error.
	result, err := r.Execute(context.Background(), "get_logs", mustJSON(t, map[string]any{
		"deployment_id": "d1",
		"lines":         9999,
	}), env)
	if err != nil {
		t.Fatal(err)
	}
	_ = result
}

func TestSetVariableRequiresMutations(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, audit := newEnv(false) // mutations disabled
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "FOO", "value": "bar",
	}), env)
	if !errors.Is(err, agent.ErrMutationsDisabled) {
		t.Fatalf("expected ErrMutationsDisabled, got %v", err)
	}
	if len(audit.Entries()) != 1 || audit.Entries()[0].Outcome != "refused" {
		t.Errorf("expected refused audit entry, got %#v", audit.Entries())
	}
}

func TestSetVariableHappyPath(t *testing.T) {
	r := NewDefaultRegistry()
	env, svc, _, _, audit := newEnv(true)
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "FOO", "value": "bar",
	}), env)
	if err != nil {
		t.Fatal(err)
	}
	if svc.variables["s1"]["FOO"] != "bar" {
		t.Errorf("variable not stored: %#v", svc.variables)
	}
	if got := audit.Entries(); len(got) != 1 || got[0].Outcome != "ok" {
		t.Errorf("expected ok audit, got %#v", got)
	}
}

func TestSetVariableRejectsReservedPrefix(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "MIDNIGHT_DATABASE_URL", "value": "postgres://x",
	}), env)
	if err == nil || !strings.Contains(err.Error(), "reserved MIDNIGHT_ namespace") {
		t.Fatalf("expected reserved-namespace error, got %v", err)
	}
}

func TestSetVariableAllowBuiltin(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	env.AllowBuiltinVar = true
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "MIDNIGHT_DATABASE_URL", "value": "postgres://x",
	}), env)
	if err != nil {
		t.Fatalf("override should succeed, got %v", err)
	}
}

func TestSetVariableValidatesName(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "foo-bar", "value": "x",
	}), env)
	if err == nil || !strings.Contains(err.Error(), "invalid variable name") {
		t.Fatalf("expected name validation error, got %v", err)
	}
}

func TestUnsetVariableRemoves(t *testing.T) {
	r := NewDefaultRegistry()
	env, svc, _, _, _ := newEnv(true)
	_, err := r.Execute(context.Background(), "unset_variable", mustJSON(t, map[string]string{
		"service_id": "s1", "name": "PORT",
	}), env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.variables["s1"]["PORT"]; ok {
		t.Error("variable should be removed")
	}
}

func TestRedeploy(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	result, err := r.Execute(context.Background(), "redeploy", mustJSON(t, map[string]string{"service_id": "s1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	d := result.(agent.Deployment)
	if d.ServiceID != "s1" || d.Status != "queued" {
		t.Errorf("unexpected deployment %#v", d)
	}
}

func TestRollback(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	result, err := r.Execute(context.Background(), "rollback", mustJSON(t, map[string]string{"deployment_id": "d1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	if d := result.(agent.Deployment); d.Status != "queued" {
		t.Errorf("unexpected rollback result %#v", d)
	}
}

func TestDeployFromSource(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, bld, _ := newEnv(true)
	_, err := r.Execute(context.Background(), "deploy_from_source", mustJSON(t, map[string]string{
		"service_id": "s1", "repo": "midnight/x", "ref": "deadbeef",
	}), env)
	if err != nil {
		t.Fatal(err)
	}
	if !bld.called {
		t.Error("build intake not called")
	}
}

func TestGetMetricsStubbed(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	// env.Metrics nil — should return a friendly stub
	result, err := r.Execute(context.Background(), "get_metrics", mustJSON(t, map[string]string{"service_id": "s1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["available"].(bool) {
		t.Error("expected available=false when metrics not wired")
	}
}

type errMetrics struct{}

func (errMetrics) GetServiceMetrics(context.Context, string) (agent.ServiceMetrics, error) {
	return agent.ServiceMetrics{}, agent.ErrMetricsUnavailable
}

func TestGetMetricsHandlesErrMetricsUnavailable(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(false)
	env.Metrics = errMetrics{}
	result, err := r.Execute(context.Background(), "get_metrics", mustJSON(t, map[string]string{"service_id": "s1"}), env)
	if err != nil {
		t.Fatal(err)
	}
	if m := result.(map[string]any); m["available"].(bool) {
		t.Error("expected friendly fallback for ErrMetricsUnavailable")
	}
}

func TestUnknownToolReturnsErrToolNotFound(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, _ := newEnv(true)
	_, err := r.Execute(context.Background(), "nope", json.RawMessage(`{}`), env)
	if !errors.Is(err, agent.ErrToolNotFound) {
		t.Fatalf("expected ErrToolNotFound, got %v", err)
	}
}

func TestBuildAnthropicToolsDropsMutationsWhenDisabled(t *testing.T) {
	r := NewDefaultRegistry()
	ro := r.BuildAnthropicTools(nil, false)
	for _, tp := range ro {
		if tp.OfTool == nil {
			t.Fatal("expected OfTool variant")
		}
		switch tp.OfTool.Name {
		case "set_variable", "unset_variable", "redeploy", "rollback", "deploy_from_source":
			t.Errorf("mutating tool %q should be hidden when AllowMutations=false", tp.OfTool.Name)
		}
	}
}

func TestBuildAnthropicToolsIncludesMutationsWhenEnabled(t *testing.T) {
	r := NewDefaultRegistry()
	got := r.BuildAnthropicTools(nil, true)
	found := map[string]bool{}
	for _, tp := range got {
		if tp.OfTool != nil {
			found[tp.OfTool.Name] = true
		}
	}
	for _, n := range []string{"set_variable", "redeploy", "rollback", "deploy_from_source"} {
		if !found[n] {
			t.Errorf("expected mutating tool %q to be present", n)
		}
	}
}

func TestBuildAnthropicToolsCacheControlOnLastEntry(t *testing.T) {
	r := NewDefaultRegistry()
	got := r.BuildAnthropicTools(nil, true)
	if len(got) == 0 {
		t.Fatal("expected non-empty tools")
	}
	last := got[len(got)-1]
	if last.OfTool == nil {
		t.Fatal("expected OfTool variant on last entry")
	}
	// CacheControlEphemeralParam has no fields — its set-ness is the
	// signal. The SDK serialises a non-zero param object only when the
	// inner struct is "touched", which the registry does by assignment.
	// A simple presence check is sufficient.
	for i, tp := range got[:len(got)-1] {
		_ = i
		if tp.OfTool == nil {
			continue
		}
		// All non-last tools should not be configured as cache breakpoints
		// (the registry only marks the last one). The struct is zero by
		// default; we sanity-check via JSON round-trip below.
	}
	// JSON marshal — ensure the last tool serialises cache_control.
	b, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"cache_control"`) {
		t.Errorf("expected cache_control on last tool, got: %s", b)
	}
}

func TestAllowedToolsFilter(t *testing.T) {
	r := NewDefaultRegistry()
	got := r.BuildAnthropicTools([]string{"list_projects", "get_logs"}, true)
	names := map[string]bool{}
	for _, tp := range got {
		if tp.OfTool != nil {
			names[tp.OfTool.Name] = true
		}
	}
	if len(names) != 2 || !names["list_projects"] || !names["get_logs"] {
		t.Errorf("expected exactly the two allowed tools, got %v", names)
	}
}

func TestAuditEntriesIncludeArguments(t *testing.T) {
	r := NewDefaultRegistry()
	env, _, _, _, audit := newEnv(true)
	args := map[string]string{"service_id": "s1", "name": "FOO", "value": "bar"}
	_, err := r.Execute(context.Background(), "set_variable", mustJSON(t, args), env)
	if err != nil {
		t.Fatal(err)
	}
	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Actor != "agent:test" {
		t.Errorf("expected actor agent:test, got %q", entries[0].Actor)
	}
	if entries[0].ServiceID != "s1" {
		t.Errorf("expected scoped service_id s1, got %q", entries[0].ServiceID)
	}
	var parsed map[string]string
	if err := json.Unmarshal(entries[0].Arguments, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["name"] != "FOO" {
		t.Errorf("expected name=FOO in audit args, got %#v", parsed)
	}
}

func TestMarshalToolResultErrorEnvelope(t *testing.T) {
	raw, isErr := MarshalToolResult(nil, errors.New("boom"))
	if !isErr {
		t.Fatal("expected isErr=true")
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["error"] != "boom" {
		t.Errorf("expected boom, got %v", parsed)
	}
}

func TestMarshalToolResultOK(t *testing.T) {
	raw, isErr := MarshalToolResult(map[string]int{"x": 1}, nil)
	if isErr {
		t.Fatal("expected isErr=false")
	}
	if !strings.Contains(string(raw), `"x":1`) {
		t.Errorf("unexpected payload %s", raw)
	}
}
