//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/build"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	runtimedocker "github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// This is a real BuildKit push and real Docker HTTP journey, starting at the
// tenant chat HTTP route. Only the model Caller is a local tool-loop double.
// It proves neither an external provider nor GitHub/DNS/public TLS.
func TestTenantAgentSourceChatBuildKitRuntimeHTTP(t *testing.T) {
	buildctl, registry := os.Getenv("MIDNIGHT_BUILDCTL"), os.Getenv("MIDNIGHT_BUILDKIT_REGISTRY")
	if testing.Short() || buildctl == "" || registry == "" || os.Getenv("BUILDKIT_HOST") == "" { // not-a-failure-code
		t.Skip("requires disposable BuildKit/registry live-runtime gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	engine, err := runtimedocker.NewEngineClient("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine client cleanup: %v", err)
		}
	})
	if err := engine.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	container, dsn := startSourcePipelinePostgres(t, ctx)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(cleanup); err != nil {
			t.Errorf("PostgreSQL cleanup: %v", err)
		}
	})
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
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	// Unique project/environment identifiers ensure label-scoped cleanup cannot
	// touch an earlier or concurrent test's resources on the shared daemon.
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	projectID, environmentID, serviceID := "prj_agent_"+suffix, "env_agent_"+suffix, "svc_agent_"+suffix
	const password = "agent source HTTP correct horse battery staple"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation) VALUES('usr_agent_source','agent-source@example.test','Source Editor',$1,1)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces(id,slug,display_name) VALUES('wrk_agent_source','agent-source','Agent Source')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO projects(id,workspace_id,slug,display_name) VALUES($1,'wrk_agent_source','agent-source','Agent Source')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO environments(id,project_id,slug,name) VALUES($1,$2,'main','Main')`, environmentID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO services(id,project_id,slug,display_name) VALUES($1,$2,'web','Web')`, serviceID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role,accepted_at) VALUES($1,'usr_agent_source','editor',CURRENT_TIMESTAMP)`, projectID); err != nil {
		t.Fatal(err)
	}
	artifactRoot := t.TempDir()
	keys, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := api.NewPostgresDataStack(api.PostgresDataStackConfig{Pool: pool, MigrationsFS: migrations.FS, ManifestPath: "manifest.yaml", CursorSecret: []byte("agent-source-cursor-secret-000001"), AgentHMACSecret: []byte("agent-source-agent-secret-000001"), AutomationTokenMACKey: []byte("agent-source-automation-key-00001"), InvitationMACKey: []byte("agent-source-invitation-key-00001"), VariableIntentMACKey: []byte("agent-source-variable-key-000001"), VariableKeyProvider: keys, SourceArtifactRoot: artifactRoot})
	if err != nil {
		t.Fatal(err)
	}
	marker := "agent-source-built-http-" + suffix
	caller := &sourceRuntimeCaller{request: agent.SourceDeployRequest{ServiceID: serviceID, EnvironmentID: environmentID, RequestKey: "actual-build", Files: map[string]string{"Dockerfile": "FROM busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e\nCOPY index.html /www/index.html\nEXPOSE 8080\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n", "index.html": marker}}}
	stack.AgentCaller = caller
	stack.AgentMutationGate = sourceRuntimeGate{}
	stack.ConversationStore = conversation.NewPostgresStore(pool)
	stack.ConversationScopeAuthorizer = api.NewTenantConversationPolicy(stack)
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "agent-source@example.test", Password: password, ClientKind: "api", RemoteAddr: "192.0.2.81:443"})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewRouterWithDataStack(stack)
	request := func(path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+login.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("chat %s status=%d want=%d body=%s", path, response.Code, want, response.Body.String())
		}
		return response.Body.Bytes()
	}
	create, _ := json.Marshal(map[string]string{"project_id": projectID, "service_id": serviceID, "environment_id": environmentID})
	var conv conversation.Conversation
	if err := json.Unmarshal(request(api.AgentConversationsPrefix, string(create), http.StatusCreated), &conv); err != nil {
		t.Fatal(err)
	}
	// The actual message's AllowMutations=false must refuse the proposed tool
	// while leaving the database and artifact store untouched.
	request(api.AgentConversationsPrefix+"/"+conv.ID+"/messages", `{"content":"build the bounded source fixture","allow_mutations":false}`, http.StatusOK)
	if caller.accepted.ID != "" || !errors.Is(caller.toolError, agent.ErrMutationsDisabled) {
		t.Fatalf("read-only turn mutated source: %#v %v", caller.accepted, caller.toolError)
	}
	var workCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM deploy_intake_outbox`).Scan(&workCount); err != nil || workCount != 0 {
		t.Fatalf("read-only work=%d err=%v", workCount, err)
	}
	request(api.AgentConversationsPrefix+"/"+conv.ID+"/messages", `{"content":"build the bounded source fixture","allow_mutations":true}`, http.StatusOK)
	if caller.toolError != nil || caller.accepted.ID == "" {
		t.Fatalf("source tool: %#v %v", caller.accepted, caller.toolError)
	}
	deploymentID := caller.accepted.ID
	deploymentStore := api.NewPostgresDeploymentStore(pool)
	var operationID string
	if err := pool.QueryRow(ctx, `SELECT operation_id FROM deployments WHERE id=$1`, deploymentID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	record, err := deploymentStore.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectID != projectID || record.EnvironmentID != environmentID || record.ServiceID != serviceID || record.State != deploy.DeploymentQueued {
		t.Fatalf("queued canonical lineage: %#v", record)
	}
	// HTTP retry exercises production conversation-scoped idempotency, not a
	// cached adapter response. Exactly one source operation/outbox may exist.
	request(api.AgentConversationsPrefix+"/"+conv.ID+"/messages", `{"content":"retry the same source request","allow_mutations":true}`, http.StatusOK)
	if caller.toolError != nil || caller.accepted.ID != deploymentID {
		t.Fatalf("chat source replay: %v", caller.toolError)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM deploy_intake_outbox`).Scan(&workCount); err != nil || workCount != 1 {
		t.Fatalf("retry work=%d err=%v", workCount, err)
	}
	references := sourceartifact.NewPostgresStore(pool)
	reference, found, err := references.GetReference(ctx, operationID)
	if err != nil || !found || reference.DeploymentID != deploymentID {
		t.Fatalf("source reference: %#v %v", reference, err)
	}
	outbox := stack.DeployIntakeOutbox.(*api.PostgresDeployOutbox)
	outbox.WorkerID = "agent-source-dispatch"
	if delivered, err := outbox.DispatchDeployIntake(ctx, 1); err != nil || delivered != 1 {
		t.Fatalf("source dispatch=%d %v", delivered, err)
	}
	queueStore := queue.NewPostgresStore(pool)
	handoff := api.NewPostgresStagedWorkOutbox(pool)
	worker := &buildJobExecutor{cfg: workerConfig{WorkerID: "agent-source-builder", OutputRoot: filepath.Join(t.TempDir(), "output"), CheckoutRoot: filepath.Join(t.TempDir(), "checkouts"), UploadRoot: artifactRoot, CacheRoot: filepath.Join(t.TempDir(), "cache"), BuildctlPath: buildctl, BuildkitAddr: os.Getenv("BUILDKIT_HOST"), Registry: registry, RegistryInsec: true}, operationStore: stack.OperationStore, deploymentStore: deploymentStore, queueStore: queueStore, handoffStore: handoff, sourceArtifacts: references, observability: observability.NewPostgresStore(pool), stdout: io.Discard, stderr: io.Discard, redactor: build.NewRedactor(nil), serviceConfig: serviceconfig.NewStore(pool)} // not-a-failure-code
	processed, err := processOneBuildJob(ctx, queueStore, "agent-source-builder", 2*time.Minute, 4*time.Minute, time.Now().UTC(), worker)
	if err != nil || !processed {
		events, _ := stack.OperationStore.ListEvents(ctx, orailtypes.ID(record.OperationID))
		t.Fatalf("real build processed=%t err=%v events=%v", processed, err, events)
	}
	record, err = deploymentStore.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(record.ImageRef, registry+"/") || !strings.HasPrefix(record.ImageDigest, "sha256:") {
		t.Fatalf("build did not record local immutable image: %#v", record)
	}
	ownedImage := strings.Split(record.ImageRef, "@")[0] + "@" + record.ImageDigest
	if dispatched, err := handoff.DispatchStagedWork(ctx, 1); err != nil || dispatched != 1 {
		t.Fatalf("build handoff=%d %v", dispatched, err)
	}
	job, err := queueStore.Lease(ctx, queue.LeaseRequest{Queue: queue.Deploy, WorkerID: "agent-source-runtime", LeaseTime: 2 * time.Minute, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := deploymentStore.MarkDeploymentDeploying(ctx, deploymentID, record.State); err != nil {
		t.Fatal(err)
	}
	adapter, _, err := runtimedocker.New(ctx, runtimedocker.Config{AgentID: "agent-source-fixture", HostID: "host-agent-source-fixture", Engine: engine, BaselineMode: runtimedocker.BaselineWarn, StopGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	adapter.EnableSignatureEnforcement(runtimedocker.LocalRegistryDigestVerifier{Endpoint: registry})
	// Cleanup is independently bounded and asserted, including stopped residue.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		labels := map[string]string{"midnight.project_id": projectID, "midnight.environment_id": environmentID}
		containers, err := engine.ContainerList(cleanup, labels)
		if err != nil {
			t.Errorf("runtime cleanup list: %v", err)
			return
		}
		for _, c := range containers {
			if err := engine.ContainerRemove(cleanup, c.ID, true); err != nil {
				t.Errorf("runtime cleanup remove: %v", err)
			}
		}
		remaining, err := engine.ContainerList(cleanup, labels)
		if err != nil || len(remaining) != 0 {
			t.Errorf("runtime cleanup unverified remaining=%d err=%v", len(remaining), err)
		}
		networks, err := engine.NetworkList(cleanup, labels)
		if err != nil {
			t.Errorf("network cleanup list: %v", err)
			return
		}
		for _, n := range networks {
			if err := engine.NetworkRemove(cleanup, n.ID); err != nil {
				t.Errorf("network cleanup remove: %v", err)
			}
		}
		remainingNetworks, err := engine.NetworkList(cleanup, labels)
		if err != nil || len(remainingNetworks) != 0 {
			t.Errorf("network cleanup unverified remaining=%d err=%v", len(remainingNetworks), err)
		}
		// This exact immutable reference was built by this test into its
		// disposable registry; no wildcard/image prune is permitted.
		if output, err := exec.CommandContext(cleanup, "docker", "image", "rm", ownedImage).CombinedOutput(); err != nil {
			t.Errorf("owned image cleanup: %v %s", err, output)
		}
	})
	executionCtx, stop, err := startBuildLeaseHeartbeat(ctx, queueStore, job, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	executor := deploy.Executor{Runtime: adapter, Promoter: deploy.NoopRoutePromoter{}, Operations: stack.OperationStore, Configuration: serviceconfig.NewStore(pool), Instances: deploy.NewInstanceStore(pool), CanonicalCommitter: sourceRuntimeCommitter{deploymentStore}, Readiness: deploy.ReadinessConfig{Timeout: 30 * time.Second, Interval: 100 * time.Millisecond}}
	outcome, executionErr := executor.Execute(executionCtx, job)
	heartbeatErr := stop()
	if executionErr != nil || heartbeatErr != nil || !outcome.Succeeded {
		t.Fatalf("real runtime outcome=%#v execution=%v lease=%v", outcome, executionErr, heartbeatErr)
	}
	if err := queueStore.Complete(ctx, job.ID, job.LeaseID); err != nil {
		t.Fatal(err)
	}
	record, err = deploymentStore.GetDeploymentByOperation(ctx, operationID)
	if err != nil || record.State != deploy.DeploymentActive {
		t.Fatalf("canonical activation=%#v err=%v", record, err)
	}
	// GetDeploymentByOperation deliberately projects only the worker fields;
	// independently read active truth and its canonical service/env pointer.
	var active bool
	var activeID, desiredState, observedState, operationState string
	if err := pool.QueryRow(ctx, `SELECT d.active,COALESCE(ses.active_deployment_id,''),d.runtime_desired_state,d.runtime_observed_state,o.state FROM deployments d JOIN operations o ON o.id=d.operation_id LEFT JOIN service_environment_state ses ON ses.service_id=d.service_id AND ses.environment_id=d.environment_id WHERE d.id=$1`, deploymentID).Scan(&active, &activeID, &desiredState, &observedState, &operationState); err != nil {
		t.Fatal(err)
	}
	if !active || activeID != deploymentID || desiredState != "running" || observedState != "running" || operationState != "SUCCEEDED" {
		t.Fatalf("canonical activation active=%t pointer=%q runtime=%s/%s operation=%s", active, activeID, desiredState, observedState, operationState)
	}
	target, err := adapter.ResolveRoutingTarget(ctx, deploymentID, deploy.ReplicaInstanceID(deploymentID, 0))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://%s:%d/", target.ContainerIP, target.InternalPort))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != marker {
		t.Fatalf("actual source HTTP status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
	released, found, err := references.GetReference(ctx, operationID)
	if err != nil || !found || released.ReleasedAt == nil {
		t.Fatalf("terminal build reference not released: %#v %v", released, err)
	}
	t.Log("real tenant chat -> scoped source intake -> leased BuildKit push -> durable deploy handoff -> Docker HTTP marker; local Caller only")
}

type sourceRuntimeGate struct{}

func (sourceRuntimeGate) AllowMutations(context.Context, agentauth.SessionContext) bool { return true }

type sourceRuntimeCaller struct {
	request   agent.SourceDeployRequest
	accepted  agent.Deployment
	toolError error
}

func (c *sourceRuntimeCaller) Send(context.Context, agent.CallRequest) (agent.CallResult, error) {
	return agent.CallResult{}, errors.New("unexpected nonstream call")
}
func (c *sourceRuntimeCaller) Stream(ctx context.Context, request agent.CallRequest, out chan<- agent.StreamEvent) error {
	defer close(out)
	registry := tools.NewDefaultRegistry()
	// Obtain versions through the model-visible read tool; the test never
	// supplies fabricated optimistic versions to the mutation.
	contextInput, _ := json.Marshal(map[string]string{"service_id": c.request.ServiceID, "environment_id": c.request.EnvironmentID})
	value, err := registry.Execute(ctx, "get_source_deployment_context", contextInput, request.Env)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(value)
	var snapshot agent.SourceDeploymentContext
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return err
	}
	c.request.ExpectedServiceVersion = snapshot.ExpectedServiceVersion
	c.request.ExpectedEnvironmentVersion = snapshot.ExpectedEnvironmentVersion
	c.request.ExpectedConfigVersion = snapshot.ExpectedConfigVersion
	input, _ := json.Marshal(c.request)
	call := &agent.ToolCall{ID: "source-fixture-call", Name: "deploy_from_source", Input: input}
	out <- agent.StreamEvent{Kind: agent.EventToolCall, ToolCall: call}
	value, c.toolError = registry.Execute(ctx, call.Name, input, request.Env)
	var result json.RawMessage
	if c.toolError == nil {
		result, _ = json.Marshal(value)
		if err := json.Unmarshal(result, &c.accepted); err != nil {
			return err
		}
	} else {
		result, _ = json.Marshal(map[string]string{"error": c.toolError.Error()})
	}
	out <- agent.StreamEvent{Kind: agent.EventToolResult, ToolResult: &agent.ToolResult{ToolUseID: call.ID, Output: result, IsError: c.toolError != nil}}
	out <- agent.StreamEvent{Kind: agent.EventTextDelta, Delta: "local fixture tool result recorded"}
	out <- agent.StreamEvent{Kind: agent.EventDone, StopReason: "end_turn"}
	return nil
}

// This adapter invokes the production canonical promotion transaction at the
// executor's commit boundary; it never writes active state itself.
type sourceRuntimeCommitter struct{ store api.DeploymentStore }

func (c sourceRuntimeCommitter) CommitCanonicalDeployment(ctx context.Context, operationID string) error {
	record, err := c.store.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		return err
	}
	return c.store.PromoteDeployment(ctx, record.ID, record.ExpectedActiveID)
}
