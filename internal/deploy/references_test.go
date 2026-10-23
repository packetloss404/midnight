package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestExecutorResolvesVariablesAndPropagatesEnvMap(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	observer := &recordingDeployObserver{}
	lookup := varrefs.NewMemoryLookup()
	lookup.SetServiceName("prj-1", "prod", "cache", "svc_cache")
	lookup.SetService("prj-1", "prod", "svc_cache", "HOST", "10.0.0.5")
	lookup.SetService("prj-1", "prod", "svc_cache", "PORT", "6379")
	lookup.SetService("prj-1", "prod", "api", "REDIS_URL", "redis://${{cache.HOST}}:${{cache.PORT}}/0")
	lookup.SetService("prj-1", "prod", "api", "SERVICE_TAG", "${{this.MIDNIGHT_SERVICE_NAME}}")
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Observer:   observer,
		Variables:  varrefs.NewResolver(lookup),
	}
	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %#v, want success", out)
	}
	if len(rt.scheduleRequests) != 1 {
		t.Fatalf("schedule requests = %d, want 1", len(rt.scheduleRequests))
	}
	env := envSliceToMap(rt.scheduleRequests[0].Env)
	if env["REDIS_URL"] != "redis://10.0.0.5:6379/0" {
		t.Fatalf("REDIS_URL = %q", env["REDIS_URL"])
	}
	if env["SERVICE_TAG"] != "api" {
		t.Fatalf("SERVICE_TAG = %q, want resolved service id 'api'", env["SERVICE_TAG"])
	}
	// Built-in private domain should be auto-injected even though no
	// user variable references it.
	if env[varrefs.BuiltinPrivateDomain] != "api.midnight.internal" {
		t.Fatalf("private domain = %q", env[varrefs.BuiltinPrivateDomain])
	}
	// Assert the executor surfaced a redaction-safe snapshot via the
	// resolve-variables step event.
	snapshotEvent := findDeployEvent(observer.events, DeployStepResolveVariables, DeployStepStateSucceeded)
	if snapshotEvent.Variables == nil {
		t.Fatalf("event = %#v, want resolved-variables snapshot", snapshotEvent)
	}
	names := make([]string, 0, len(snapshotEvent.Variables.Items))
	for _, item := range snapshotEvent.Variables.Items {
		names = append(names, item.Name)
		if item.LengthBucket == "" {
			t.Fatalf("snapshot item missing bucket: %#v", item)
		}
	}
	wantNames := []string{varrefs.BuiltinPrivateDomain, varrefs.BuiltinServiceName, "REDIS_URL", "SERVICE_TAG"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("snapshot names = %v, want %v", names, wantNames)
	}
}

func TestExecutorAttributesAuditedDecryptToExactLeasedAttempt(t *testing.T) {
	ctx := context.Background()
	provider, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.NewVault(provider, secrets.AllowAll{})
	if _, err := vault.Set(ctx, secrets.VariableInput{
		Name:        "TOKEN",
		Workspace:   "wrk-1",
		Project:     "prj-1",
		Environment: "prod",
		Service:     "api",
		Scope:       secrets.ScopeSecret,
		Plaintext:   []byte("runtime-only"),
		ValueKind:   "literal",
	}); err != nil {
		t.Fatal(err)
	}
	chain := audit.NewMemoryChain()
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &[]string{}, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &[]string{}},
		Operations: &fakeOperationUpdater{},
		Variables: varrefs.NewResolver(secrets.NewAuditedRuntimeLookup(
			vault,
			nil,
			secrets.NewPostgresDecryptAuditSink(chain, "deploy-worker-one"),
		)),
	}
	job := deployJob()
	if _, err := executor.Execute(ctx, job); err != nil {
		t.Fatal(err)
	}
	rows, err := chain.Read(ctx, audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 ||
		rows[0].Attrs["execution_kind"] != secrets.DecryptExecutionDeploy ||
		rows[0].Attrs["execution_id"] != job.ID ||
		rows[0].Attrs["deployment_id"] != "dep-2" ||
		rows[0].Attrs["operation_id"] != "op-1" ||
		rows[0].Attrs["attempt"] != job.Attempt {
		t.Fatalf("decrypt audit row=%#v", rows)
	}
}

func TestExecutorFailsWithUnresolvedReferenceCode(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	observer := &recordingDeployObserver{}
	lookup := varrefs.NewMemoryLookup()
	lookup.SetService("prj-1", "prod", "api", "REDIS_URL", "redis://${{ghost.HOST}}:6379/0")
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
		Observer:   observer,
		Variables:  varrefs.NewResolver(lookup),
	}
	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatalf("Execute err = nil, want error")
	}
	if out.Succeeded {
		t.Fatalf("outcome = %#v, want failure", out)
	}
	if out.FailureCode != failurecodes.VariableReferenceUnresolved {
		t.Fatalf("failure code = %q, want %q", out.FailureCode, failurecodes.VariableReferenceUnresolved)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.VariableReferenceUnresolved) {
		t.Fatalf("ops failure = %#v, want %s", ops, failurecodes.VariableReferenceUnresolved)
	}
	var unknown varrefs.ErrUnknownReference
	if !errors.As(err, &unknown) || unknown.Service != "ghost" {
		t.Fatalf("err = %v, want ErrUnknownReference for ghost.HOST", err)
	}
	// The executor must NOT have reached the schedule step.
	if len(rt.scheduleRequests) != 0 {
		t.Fatalf("schedule requests = %d, want 0 when resolver fails", len(rt.scheduleRequests))
	}
	failedEvent := findDeployEvent(observer.events, DeployStepResolveVariables, DeployStepStateFailed)
	if failedEvent.FailureCode != failurecodes.VariableReferenceUnresolved {
		t.Fatalf("event failure code = %q, want %q", failedEvent.FailureCode, failurecodes.VariableReferenceUnresolved)
	}
}

type variableInfrastructureFailure struct {
	detail string
}

func (e variableInfrastructureFailure) Error() string                  { return e.detail }
func (variableInfrastructureFailure) IsVariableInfrastructureFailure() {}

func TestVariableFailureCodeKeepsInfrastructureFailureHonest(t *testing.T) {
	err := variableInfrastructureFailure{detail: "open /etc/midnight/kek: permission denied"}
	if got := variableFailureCode(err); got != failurecodes.InternalError {
		t.Fatalf("failure code=%q, want %q", got, failurecodes.InternalError)
	}
	if got := variableFailureReason(err); got != "variable resolution infrastructure is unavailable" {
		t.Fatalf("failure reason=%q", got)
	}
}

func TestExecutorFailsWithCycleCode(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	observer := &recordingDeployObserver{}
	lookup := varrefs.NewMemoryLookup()
	lookup.SetService("prj-1", "prod", "api", "X", "${{Y}}")
	lookup.SetService("prj-1", "prod", "api", "Y", "${{X}}")
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
		Observer:   observer,
		Variables:  varrefs.NewResolver(lookup),
	}
	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatalf("Execute err = nil, want error")
	}
	if out.FailureCode != failurecodes.VariableReferenceCycle {
		t.Fatalf("failure code = %q, want %q", out.FailureCode, failurecodes.VariableReferenceCycle)
	}
	if ops.failCode != orailtypes.FailureCode(failurecodes.VariableReferenceCycle) {
		t.Fatalf("ops failure code = %q, want %q", ops.failCode, failurecodes.VariableReferenceCycle)
	}
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				out[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return out
}
