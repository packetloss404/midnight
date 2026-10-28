//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/migrations"
)

// Only the upstream Caller is a double. The HTTP router, human session,
// conversation/tool scope, source publication, admission locks, ownership,
// idempotency, deployment and outbox all use their production stores.
func TestTenantAgentSourceCanonicalIntakeAuthorityReplayAndVersionFences(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	if os.Getenv("MIDNIGHT_TESTCONTAINERS_DIRECT_IP") == "1" { // not-a-failure-code
		ip, err := container.ContainerIP(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Host = net.JoinHostPort(ip, "5432")
		dsn = parsed.String()
	}
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	password := "source agent integration correct horse battery staple"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"owner", "editor", "viewer", "peer"} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation) VALUES($1,$2,$3,$4,1)`, "usr_source_"+role, role+"-source@example.test", role, hash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO workspaces(id,slug,display_name) VALUES('wrk_source_agent','source-agent','Source Agent');
INSERT INTO projects(id,workspace_id,slug,display_name) VALUES('prj_source_agent','wrk_source_agent','source-agent','Source Agent');
INSERT INTO environments(id,project_id,slug,name) VALUES('env_source_agent','prj_source_agent','main','Main'),('env_source_other','prj_source_agent','other','Other');
INSERT INTO services(id,project_id,slug,display_name) VALUES('svc_source_agent','prj_source_agent','web','Web'),('svc_source_other','prj_source_agent','other','Other');
INSERT INTO project_members(project_id,user_id,role,accepted_at) VALUES('prj_source_agent','usr_source_owner','owner',CURRENT_TIMESTAMP),('prj_source_agent','usr_source_editor','editor',CURRENT_TIMESTAMP),('prj_source_agent','usr_source_peer','editor',CURRENT_TIMESTAMP),('prj_source_agent','usr_source_viewer','viewer',CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}
	stack := postgresIdentityIntegrationStack(t, pool, now)
	fs, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	coordinator.SourceArtifactFS = fs
	coordinator.SourceArtifactQuota = sourceartifact.NewQuotaStore(pool, sourceartifact.DefaultQuotaConfig)
	stack.TenantUpIntake = coordinator
	staging := &sourceStageHook{TenantUpArtifactStore: fs}
	stack.SourceArtifactStore = staging
	stack.AgentMutationGate = tenantChatOpenGate{}
	stack.ConversationStore = conversation.NewPostgresStore(pool)
	stack.ConversationScopeAuthorizer = NewTenantConversationPolicy(stack)
	captured := make(chan agent.Env, 16)
	stack.AgentCaller = tenantConversationCaller{captured: captured}
	handler := NewRouterWithDataStack(stack)
	tokens := map[string]string{}
	for _, role := range []string{"owner", "editor", "viewer", "peer"} {
		login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: role + "-source@example.test", Password: password, ClientKind: "api", RemoteAddr: "192.0.2.18:443"})
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = login.AccessToken
	}
	request := func(role, method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[role])
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("%s %s as %s status=%d want=%d body=%s", method, path, role, recorder.Code, want, recorder.Body.String())
		}
		return recorder.Body.Bytes()
	}
	create := func(role string) conversation.Conversation {
		t.Helper()
		var conv conversation.Conversation
		if err := json.Unmarshal(request(role, "POST", AgentConversationsPrefix, `{"project_id":"prj_source_agent","service_id":"svc_source_agent","environment_id":"env_source_agent"}`, http.StatusCreated), &conv); err != nil {
			t.Fatal(err)
		}
		return conv
	}
	envFor := func(role string, conv conversation.Conversation, mutations bool) agent.Env {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"content": "check inline source capability", "allow_mutations": mutations})
		request(role, "POST", AgentConversationsPrefix+"/"+conv.ID+"/messages", string(payload), http.StatusOK)
		return <-captured
	}
	owner, editor, viewer, peer := create("owner"), create("editor"), create("viewer"), create("peer")
	ownerEnv, editorEnv, viewerEnv := envFor("owner", owner, true), envFor("editor", editor, true), envFor("viewer", viewer, true)
	readOnly := envFor("editor", editor, false)
	registry := tools.NewDefaultRegistry()
	// Read-only users can discover versions for their exact owned scope; a
	// snapshot is neither deploy authority nor a source mutation.
	for _, env := range []agent.Env{viewerEnv, editorEnv} {
		value, err := registry.Execute(ctx, "get_source_deployment_context", json.RawMessage(`{"service_id":"svc_source_agent","environment_id":"env_source_agent"}`), env)
		if err != nil {
			t.Fatalf("discover source versions: %v", err)
		}
		snapshot, ok := value.(agent.SourceDeploymentContext)
		if !ok || snapshot.ExpectedServiceVersion != 1 || snapshot.ExpectedEnvironmentVersion != 1 || snapshot.ExpectedConfigVersion != 0 {
			t.Fatalf("source context=%#v", value)
		}
	}
	if _, err := registry.Execute(ctx, "get_source_deployment_context", json.RawMessage(`{"service_id":"svc_source_other","environment_id":"env_source_agent"}`), editorEnv); err == nil {
		t.Fatal("source context escaped bound service")
	}
	source := agent.SourceDeployRequest{ServiceID: "svc_source_agent", EnvironmentID: "env_source_agent", RequestKey: "first", ExpectedServiceVersion: 1, ExpectedEnvironmentVersion: 1, Files: map[string]string{"Dockerfile": "FROM scratch\n", "index.html": "private-code-audit-marker"}}
	execute := func(env agent.Env, input agent.SourceDeployRequest) (agent.Deployment, error) {
		body, _ := json.Marshal(input)
		result, err := registry.Execute(context.Background(), "deploy_from_source", body, env)
		if err != nil {
			return agent.Deployment{}, err
		}
		encoded, _ := json.Marshal(result)
		var deployment agent.Deployment
		err = json.Unmarshal(encoded, &deployment)
		return deployment, err
	}
	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM deploy_intake_outbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, env := range []agent.Env{viewerEnv, readOnly} {
		if _, err := execute(env, source); !errors.Is(err, agent.ErrMutationsDisabled) {
			t.Fatalf("read-only source mutation: %v", err)
		}
	}
	for _, field := range []string{"service", "environment"} {
		attack := source
		if field == "service" {
			attack.ServiceID = "svc_source_other"
		} else {
			attack.EnvironmentID = "env_source_other"
		}
		if _, err := execute(editorEnv, attack); err == nil {
			t.Fatalf("source escaped %s scope", field)
		}
	}
	if count() != 0 {
		t.Fatal("refusal created work")
	}
	if err := editorEnv.Services.SetVariable(ctx, agent.VariableWrite{ServiceID: source.ServiceID, EnvironmentID: source.EnvironmentID, Name: "BUILD_MARKER", Value: "accepted-variable-fixture"}); err != nil { // not-a-failure-code
		t.Fatalf("seed encrypted variable: %v", err)
	}
	first, err := execute(editorEnv, source)
	if err != nil || first.ID == "" {
		t.Fatalf("editor source accept: %#v %v", first, err)
	}
	second, err := execute(editorEnv, source)
	if err != nil || second.ID != first.ID || count() != 1 {
		t.Fatalf("replay duplicated work: %#v %v count=%d", second, err, count())
	}
	changed := source
	changed.Files = map[string]string{"Dockerfile": "FROM scratch\n", "index.html": "different body"}
	if _, err := execute(editorEnv, changed); err == nil {
		t.Fatal("same-key different source accepted")
	}
	if count() != 1 {
		t.Fatal("same-key conflict created work")
	}
	source.RequestKey = "owner-same-key"
	if _, err := execute(ownerEnv, source); err != nil {
		t.Fatalf("owner source refused: %v", err)
	}
	// A second conversation cannot alias this conversation's request key.
	peerEnv := envFor("peer", peer, true)
	if _, err := execute(peerEnv, source); err != nil || count() != 3 {
		t.Fatalf("conversation key collision: %v count=%d", err, count())
	}
	var operationID string
	if err := pool.QueryRow(ctx, `SELECT operation_id FROM deployments WHERE id=$1`, first.ID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	reference, found, err := sourceartifact.NewPostgresStore(pool).GetReference(ctx, operationID)
	if err != nil || !found || reference.DeploymentID != first.ID || reference.ServiceID != source.ServiceID || reference.EnvironmentID != source.EnvironmentID || reference.ProjectID != "prj_source_agent" {
		t.Fatalf("artifact ownership: %#v %v", reference, err)
	}
	// An accepted source request binds config/variable metadata immediately,
	// rather than picking up whatever desired config exists when a worker
	// eventually leases it.
	configs := serviceconfig.NewStore(pool)
	before, err := configs.ResolveDeployment(ctx, first.ID, "prj_source_agent", "env_source_agent", "svc_source_agent")
	if err != nil || before.ConfigVersion != 0 || len(before.VariableVersions) != 1 || before.VariableVersions[0].Version != 1 {
		t.Fatalf("accepted snapshot=%#v err=%v", before, err)
	}
	desired := serviceconfig.Default("prj_source_agent", "svc_source_agent", "env_source_agent")
	desired.TargetPort = 8099
	if _, err := configs.Put(ctx, desired, 0); err != nil {
		t.Fatal(err)
	}
	if err := editorEnv.Services.SetVariable(ctx, agent.VariableWrite{ServiceID: source.ServiceID, EnvironmentID: source.EnvironmentID, Name: "BUILD_MARKER", Value: "rotated-variable-fixture"}); err != nil { // not-a-failure-code
		t.Fatalf("rotate encrypted variable: %v", err)
	}
	after, err := configs.ResolveDeployment(ctx, first.ID, "prj_source_agent", "env_source_agent", "svc_source_agent")
	if err != nil || after.ConfigVersion != 0 || after.Config.TargetPort == 8099 || after.SHA256 != before.SHA256 || len(after.VariableVersions) != 1 || after.VariableVersions[0].Version != 1 {
		t.Fatalf("accepted source config drifted=%#v err=%v", after, err)
	}
	// Restore only this fixture's desired row so the subsequent stale-config
	// and staging-window assertions continue to test the zero-version case.
	if _, err := pool.Exec(ctx, `DELETE FROM service_environment_configs WHERE service_id='svc_source_agent' AND environment_id='env_source_agent'`); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"service", "environment", "config"} {
		stale := source
		stale.RequestKey = "stale-" + field
		if field == "service" {
			stale.ExpectedServiceVersion++
		} else if field == "environment" {
			stale.ExpectedEnvironmentVersion++
		} else {
			stale.ExpectedConfigVersion++
		}
		if _, err := execute(editorEnv, stale); !errors.Is(err, ErrResourcePreconditionFailed) {
			t.Fatalf("stale %s accepted: %v", field, err)
		}
	}
	// The publication transaction must repeat version checks after bytes were
	// staged, rather than trusting the preflight snapshot.
	staging.after = func() {
		if _, err := pool.Exec(ctx, `UPDATE services SET resource_version=resource_version+1 WHERE id='svc_source_agent'`); err != nil {
			t.Fatal(err)
		}
	}
	source.RequestKey = "changed-during-stage"
	if _, err := execute(editorEnv, source); !errors.Is(err, ErrResourcePreconditionFailed) {
		t.Fatalf("staging race accepted: %v", err)
	}
	staging.after = nil
	if count() != 3 {
		t.Fatal("version failures created work")
	}
	// Previously captured authority must be checked again inside intake.
	source.ExpectedServiceVersion = 2
	source.RequestKey = "revoked-member"
	if _, err := pool.Exec(ctx, `DELETE FROM project_members WHERE user_id='usr_source_editor'`); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(editorEnv, source); err == nil {
		t.Fatal("captured source authority survived membership revocation")
	}
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE user_id='usr_source_owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(ownerEnv, source); err == nil {
		t.Fatal("captured source authority survived session revocation")
	}
	if count() != 3 {
		t.Fatal("revocation created work")
	}
	// The transcript intentionally contains supplied source; the audit chain
	// must contain only a source digest/redaction, never the file body.
	redacted := redactAgentToolInput(json.RawMessage(`{"files":{"index.html":"private-code-audit-marker"},"request_key":"audit"}`))
	encoded, _ := json.Marshal(redacted)
	if strings.Contains(string(encoded), "private-code-audit-marker") || !strings.Contains(string(encoded), "sha256") {
		t.Fatalf("source audit leaked plaintext or lost digest: %s", encoded)
	}
}

type sourceStageHook struct {
	TenantUpArtifactStore
	after func()
}

func (s *sourceStageHook) Stage(ctx context.Context, r io.Reader, limits sourceartifact.Limits) (sourceartifact.Staged, error) {
	staged, err := s.TenantUpArtifactStore.Stage(ctx, r, limits)
	if err == nil && s.after != nil {
		s.after()
	}
	return staged, err
}
