package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/agent"
)

type scopedSourceRecorder struct {
	calls   int
	request agent.SourceDeployRequest
}

func (s *scopedSourceRecorder) DeployFromSource(context.Context, string, string, string) (agent.Deployment, error) {
	return agent.Deployment{}, errors.New("legacy invocation is forbidden")
}
func (s *scopedSourceRecorder) DeployScopedSource(_ context.Context, request agent.SourceDeployRequest) (agent.Deployment, error) {
	s.calls++
	s.request = request
	return agent.Deployment{ID: "dep_source", ServiceID: request.ServiceID, Status: "PENDING"}, nil
}
func (s *scopedSourceRecorder) SourceDeploymentContext(_ context.Context, serviceID, environmentID string) (agent.SourceDeploymentContext, error) {
	return agent.SourceDeploymentContext{ServiceID: serviceID, EnvironmentID: environmentID, ExpectedServiceVersion: 4, ExpectedEnvironmentVersion: 3, ExpectedConfigVersion: 2}, nil
}

func TestGetSourceDeploymentContextIsReadOnlyAndReturnsExactVersions(t *testing.T) {
	env := agent.Env{Builds: &scopedSourceRecorder{}, AllowMutations: false}
	result, err := NewDefaultRegistry().Execute(context.Background(), "get_source_deployment_context", json.RawMessage(`{"service_id":"svc_source","environment_id":"env_source"}`), env)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := result.(agent.SourceDeploymentContext)
	if !ok || snapshot.ExpectedServiceVersion != 4 || snapshot.ExpectedEnvironmentVersion != 3 || snapshot.ExpectedConfigVersion != 2 || snapshot.ServiceID != "svc_source" || snapshot.EnvironmentID != "env_source" {
		t.Fatalf("snapshot=%#v", result)
	}
	for _, raw := range []string{`{"service_id":"svc_source"}`, `{"service_id":"svc_source","environment_id":"env_source","repo":"https://example.test"}`, `{"service_id":"svc_source","environment_id":"env_source"}{}`} {
		if _, err := NewDefaultRegistry().Execute(context.Background(), "get_source_deployment_context", json.RawMessage(raw), env); err == nil {
			t.Fatal("invalid source snapshot request accepted")
		}
	}
}

func sourceRequestFixture() agent.SourceDeployRequest {
	return agent.SourceDeployRequest{ServiceID: "svc_source", EnvironmentID: "env_source", RequestKey: "source-request-1", ExpectedServiceVersion: 1, ExpectedEnvironmentVersion: 1, ExpectedConfigVersion: 0, Files: map[string]string{"Dockerfile": "FROM scratch\n", "public/index.html": "source proof"}}
}

func TestDeployScopedSourceUsesBoundedExplicitTreeAndVersions(t *testing.T) {
	builds := &scopedSourceRecorder{}
	registry := NewDefaultRegistry()
	request := sourceRequestFixture()
	result, err := registry.Execute(context.Background(), "deploy_from_source", mustJSON(t, request), agent.Env{Builds: builds, AllowMutations: true})
	if err != nil || builds.calls != 1 || builds.request.EnvironmentID != request.EnvironmentID || builds.request.ExpectedConfigVersion != 0 || result.(agent.Deployment).Status != "PENDING" {
		t.Fatalf("scoped intake failed: %v", err)
	}
	if _, err := registry.Execute(context.Background(), "deploy_from_source", mustJSON(t, request), agent.Env{Builds: builds}); !errors.Is(err, agent.ErrMutationsDisabled) || builds.calls != 1 {
		t.Fatal("mutation-off dispatched source")
	}
}

func TestDeployScopedSourceRejectsPathsAuthorityAndOversizedPayloadBeforeDispatch(t *testing.T) {
	for _, path := range []string{"../outside", "/host/file", "C:/file", "safe\\file", "nested/.git/config", ".env", ".ssh/id_rsa", ".midnight/state", "a/../../b", "a//b", "a\u202eb"} {
		t.Run(path, func(t *testing.T) {
			request := sourceRequestFixture()
			request.Files = map[string]string{path: "text"}
			builds := &scopedSourceRecorder{}
			_, err := NewDefaultRegistry().Execute(context.Background(), "deploy_from_source", mustJSON(t, request), agent.Env{Builds: builds, AllowMutations: true})
			if err == nil || builds.calls != 0 {
				t.Fatal("unsafe path reached intake")
			}
		})
	}
	for _, raw := range []string{`{"service_id":"svc_source","repo":"https://169.254.169.254/","ref":"main"}`, `{"service_id":"svc_source","environment_id":"env_source","request_key":"k","files":{"file":"text"},"installation_id":1}`, `{} {}`} {
		builds := &scopedSourceRecorder{}
		if _, err := NewDefaultRegistry().Execute(context.Background(), "deploy_from_source", json.RawMessage(raw), agent.Env{Builds: builds, AllowMutations: true}); err == nil || builds.calls != 0 {
			t.Fatal("unbounded/provider authority input accepted")
		}
	}
	for _, config := range []string{"", `,"expected_config_version":null`} {
		raw := json.RawMessage(`{"service_id":"svc_source","environment_id":"env_source","request_key":"k","expected_service_version":1,"expected_environment_version":1,"files":{"Dockerfile":"FROM scratch"}` + config + `}`)
		builds := &scopedSourceRecorder{}
		if _, err := NewDefaultRegistry().Execute(context.Background(), "deploy_from_source", raw, agent.Env{Builds: builds, AllowMutations: true}); err == nil || builds.calls != 0 {
			t.Fatal("missing/null config snapshot reached intake")
		}
	}
	for _, kind := range []string{"file-size", "tree-size", "file-count", "binary", "stale-shape"} {
		request := sourceRequestFixture()
		switch kind {
		case "file-size":
			request.Files = map[string]string{"file": strings.Repeat("x", agent.MaxSourceFileBytes+1)}
		case "tree-size":
			request.Files = map[string]string{}
			for _, name := range []string{"a", "b", "c", "d", "e"} {
				request.Files[name] = strings.Repeat("x", agent.MaxSourceFileBytes)
			}
		case "file-count":
			request.Files = map[string]string{}
			for i := 0; i < agent.MaxSourceFiles+1; i++ {
				request.Files[strings.Repeat("x", i+1)] = "x"
			}
		case "binary":
			request.Files["bin"] = "a\x00b"
		case "stale-shape":
			request.ExpectedEnvironmentVersion = 0
		}
		builds := &scopedSourceRecorder{}
		if _, err := NewDefaultRegistry().Execute(context.Background(), "deploy_from_source", mustJSON(t, request), agent.Env{Builds: builds, AllowMutations: true}); err == nil || builds.calls != 0 {
			t.Fatalf("invalid %s reached intake", kind)
		}
	}
}
