// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type environmentChangesetCoordinatorStub struct {
	getView                                                           EnvironmentChangesetView
	getReq                                                            EnvironmentChangesetReadRequest
	putReq                                                            EnvironmentChangesetMutationRequest
	applyReq                                                          EnvironmentChangesetMutationRequest
	applyAndDeployReq                                                 EnvironmentChangesetMutationRequest
	discardReq                                                        EnvironmentChangesetMutationRequest
	getCalls, putCalls, applyCalls, applyAndDeployCalls, discardCalls int
	replayed                                                          bool
}

func TestAggregateEnvironmentOperationGroupStateWaitsForEveryAdmittedSibling(t *testing.T) {
	item := func(intake, operation, deployment string) EnvironmentOperationGroupItem {
		return EnvironmentOperationGroupItem{
			ServiceID:       "svc_test",
			RequiresDeploy:  true,
			IntakeState:     intake,
			OperationState:  operation,
			DeploymentState: deployment,
		}
	}
	tests := []struct {
		name   string
		stored string
		items  []EnvironmentOperationGroupItem
		want   string
	}{
		{
			name:   "intake failure waits for running sibling",
			stored: "partial",
			items: []EnvironmentOperationGroupItem{
				item("failed", "", ""),
				item("queued", string(orailtypes.OperationRunning), string(deploy.DeploymentDeploying)),
			},
			want: "running",
		},
		{
			name:   "intake failure becomes partial after siblings succeed",
			stored: "partial",
			items: []EnvironmentOperationGroupItem{
				item("failed", "", ""),
				item("queued", string(orailtypes.OperationSucceeded), string(deploy.DeploymentActive)),
			},
			want: "partial",
		},
		{
			name:   "intake partial outranks terminal runtime failure",
			stored: "partial",
			items: []EnvironmentOperationGroupItem{
				item("failed", "", ""),
				item("queued", string(orailtypes.OperationFailed), string(deploy.DeploymentFailed)),
			},
			want: "partial",
		},
		{
			name:  "runtime failure without intake failure fails",
			items: []EnvironmentOperationGroupItem{item("queued", string(orailtypes.OperationCanceled), string(deploy.DeploymentCanceled))},
			want:  "failed",
		},
		{
			name:  "active deployment waits for operation terminal state",
			items: []EnvironmentOperationGroupItem{item("queued", string(orailtypes.OperationRunning), string(deploy.DeploymentActive))},
			want:  "running",
		},
		{
			name:  "completed rollout remains succeeded after supersession",
			items: []EnvironmentOperationGroupItem{item("queued", string(orailtypes.OperationSucceeded), string(deploy.DeploymentSuperseded))},
			want:  "succeeded",
		},
		{
			name:  "no deployment required succeeds",
			items: []EnvironmentOperationGroupItem{{ServiceID: "svc_test", RequiresDeploy: false, IntakeState: "not_required"}},
			want:  "succeeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := aggregateEnvironmentOperationGroupState(test.stored, test.items); got != test.want {
				t.Fatalf("state=%q want %q", got, test.want)
			}
		})
	}
}

func (s *environmentChangesetCoordinatorStub) GetEnvironmentChangeset(_ context.Context, req EnvironmentChangesetReadRequest) (EnvironmentChangesetView, error) {
	s.getCalls++
	s.getReq = req
	return s.getView, nil
}
func (s *environmentChangesetCoordinatorStub) GetEnvironmentOperationGroup(_ context.Context, req EnvironmentChangesetReadRequest, groupID string) (EnvironmentOperationGroup, error) {
	s.getReq = req
	return EnvironmentOperationGroup{ID: groupID, EnvironmentID: req.EnvironmentID, State: "running", Items: []EnvironmentOperationGroupItem{}}, nil
}
func (s *environmentChangesetCoordinatorStub) ValidateEnvironmentChangeset(_ context.Context, req EnvironmentChangesetReadRequest, _ int64) (EnvironmentChangesetView, error) {
	s.getReq = req
	return s.getView, nil
}
func (s *environmentChangesetCoordinatorStub) PutEnvironmentChangeset(_ context.Context, req EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	s.putCalls++
	req.RawBody = append([]byte(nil), req.RawBody...)
	s.putReq = req
	response, _ := environmentChangesetStoredResponse(http.StatusOK, s.getView)
	return TenantMutationResult{Response: response, Replayed: s.replayed}, nil
}

func TestEnvironmentChangesetHandlerMarksExactReplay(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_change", SessionID: "ses_change"}
	session := identity.Session{ID: "ses_change", UserID: "usr_change", SessionGeneration: 1}
	stub := &environmentChangesetCoordinatorStub{replayed: true, getView: emptyEnvironmentChangesetView()}
	req := environmentChangesetRequest(http.MethodPut, "", `{"expected_revision":0,"changes":[{"kind":"variable","operation":"add","service_id":"svc_one","name":"TOKEN","scope":"secret","value":"hidden"}]}`, principal, session)
	req.Header.Set("If-Match", `"0"`)
	req.Header.Set("Idempotency-Key", "changeset-replay-one")
	response := httptest.NewRecorder()
	environmentChangesetHandler(stub)(response, req)
	if response.Code != http.StatusOK || response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status=%d replay=%q body=%s", response.Code, response.Header().Get("Idempotent-Replayed"), response.Body.String())
	}
}

func TestEnvironmentChangesetHandlerReturnsInitialStrongETag(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_change", SessionID: "ses_change"}
	session := identity.Session{ID: "ses_change", UserID: "usr_change", SessionGeneration: 1}
	stub := &environmentChangesetCoordinatorStub{getView: emptyEnvironmentChangesetView()}
	request := environmentChangesetRequest(http.MethodGet, "", "", principal, session)
	response := httptest.NewRecorder()
	environmentChangesetHandler(stub)(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"0"` {
		t.Fatalf("status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
}

func TestEnvironmentChangesetHandlerBindsApplyAndDeployIntent(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_change", SessionID: "ses_change"}
	session := identity.Session{ID: "ses_change", UserID: "usr_change", SessionGeneration: 1}
	stub := &environmentChangesetCoordinatorStub{}
	request := environmentChangesetRequest(http.MethodPost, "apply-and-deploy", `{"expected_revision":7}`, principal, session)
	request.Header.Set("If-Match", `"7"`)
	request.Header.Set("Idempotency-Key", "changeset-apply-deploy-seven")
	response := httptest.NewRecorder()
	environmentChangesetHandler(stub)(response, request)
	if response.Code != http.StatusAccepted || stub.applyAndDeployCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.applyAndDeployCalls, response.Body.String())
	}
	if stub.applyAndDeployReq.ExpectedRevision != 7 || stub.applyAndDeployReq.EnvironmentID != "env_one" || stub.applyAndDeployReq.Key != idempotency.Key("changeset-apply-deploy-seven") {
		t.Fatalf("request=%#v", stub.applyAndDeployReq)
	}
}

func (s *environmentChangesetCoordinatorStub) ApplyEnvironmentChangeset(_ context.Context, req EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	s.applyCalls++
	s.applyReq = req
	response, _ := environmentChangesetStoredResponse(http.StatusOK, s.getView)
	return TenantMutationResult{Response: response}, nil
}
func (s *environmentChangesetCoordinatorStub) ApplyAndDeployEnvironmentChangeset(_ context.Context, req EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	s.applyAndDeployCalls++
	s.applyAndDeployReq = req
	response, _ := environmentApplyAndDeployStoredResponse(EnvironmentApplyAndDeployResponse{AppliedRevision: req.ExpectedRevision, OperationGroupID: "opg_test", AffectedServices: []string{}, OperationGroup: EnvironmentOperationGroup{ID: "opg_test", State: "accepted", Items: []EnvironmentOperationGroupItem{}}})
	return TenantMutationResult{Response: response}, nil
}
func (s *environmentChangesetCoordinatorStub) DiscardEnvironmentChangeset(_ context.Context, req EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	s.discardCalls++
	s.discardReq = req
	response, _ := environmentChangesetStoredResponse(http.StatusOK, s.getView)
	return TenantMutationResult{Response: response}, nil
}

func TestEnvironmentChangesetHandlerBindsHumanEnvironmentAndRedactsValues(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_change", SessionID: "ses_change", AuthMethod: "password+totp"}
	session := identity.Session{ID: "ses_change", UserID: "usr_change", SessionGeneration: 2}
	changeset := EnvironmentChangeset{ID: "chg_one", WorkspaceID: "wrk_one", ProjectID: "prj_one", EnvironmentID: "env_one", CreatedBy: "usr_change", State: "draft", Revision: 3, Items: []EnvironmentChangesetItem{{ID: "csi_one", Kind: "variable", Operation: "edit", ServiceID: "svc_one", TargetID: "var_one", Name: "API_TOKEN", Scope: "secret", ValueChange: "value replaced"}}}
	stub := &environmentChangesetCoordinatorStub{getView: environmentChangesetView(changeset, EnvironmentChangesetValidation{Valid: true, AffectedServices: []string{"svc_one"}, Errors: []string{}, Warnings: []string{}})}

	get := environmentChangesetRequest(http.MethodGet, "", "", principal, session)
	response := httptest.NewRecorder()
	environmentChangesetHandler(stub)(response, get)
	if response.Code != http.StatusOK || stub.getCalls != 1 || stub.getReq.EnvironmentID != "env_one" {
		t.Fatalf("status=%d calls=%d request=%#v body=%s", response.Code, stub.getCalls, stub.getReq, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "plaintext") || !strings.Contains(response.Body.String(), "value replaced") || response.Header().Get("ETag") != "\"3\"" {
		t.Fatalf("unsafe or incomplete review response: %s headers=%v", response.Body.String(), response.Header())
	}

	body := `{"expected_revision":3,"note":"rotate token","changes":[{"kind":"variable","operation":"edit","service_id":"svc_one","target_id":"var_one","name":"API_TOKEN","scope":"secret","expected_version":4,"value":"never-echo-this"}]}`
	put := environmentChangesetRequest(http.MethodPut, "", body, principal, session)
	put.Header.Set("If-Match", "\"3\"")
	put.Header.Set("Idempotency-Key", "changeset-put-one")
	putResponse := httptest.NewRecorder()
	environmentChangesetHandler(stub)(putResponse, put)
	if putResponse.Code != http.StatusOK || stub.putCalls != 1 || stub.putReq.ExpectedRevision != 3 || stub.putReq.Key != idempotency.Key("changeset-put-one") || len(stub.putReq.Changes) != 1 || stub.putReq.Changes[0].Value == nil || *stub.putReq.Changes[0].Value != "never-echo-this" {
		t.Fatalf("status=%d request=%#v body=%s", putResponse.Code, stub.putReq, putResponse.Body.String())
	}
	if strings.Contains(putResponse.Body.String(), "never-echo-this") {
		t.Fatalf("response leaked proposed value: %s", putResponse.Body.String())
	}
}

func TestEnvironmentChangesetHandlerRejectsNonHumanAndNonCanonicalMutation(t *testing.T) {
	stub := &environmentChangesetCoordinatorStub{}
	operator := httptest.NewRequest(http.MethodGet, "/api/v1alpha/environments/env_one/changeset", nil)
	operator.SetPathValue("environment_id", "env_one")
	operator = operator.WithContext(withProductPrincipal(operator.Context(), productPrincipal{Kind: productPrincipalOperator}))
	response := httptest.NewRecorder()
	environmentChangesetHandler(stub)(response, operator)
	if response.Code != http.StatusNotFound || stub.getCalls != 0 {
		t.Fatalf("operator status=%d calls=%d", response.Code, stub.getCalls)
	}
	principal := identity.Principal{Kind: "user", UserID: "usr_change", SessionID: "ses_change"}
	session := identity.Session{ID: "ses_change", UserID: "usr_change", SessionGeneration: 1}
	for _, body := range []string{`{"expected_revision":0,"expected_revision":1,"changes":[]}`, `{"expected_revision":0,"changes":[],"workspace_id":"forged"}`, `[]`} {
		req := environmentChangesetRequest(http.MethodPut, "", body, principal, session)
		req.Header.Set("If-Match", "\"0\"")
		req.Header.Set("Idempotency-Key", "changeset-invalid")
		got := httptest.NewRecorder()
		environmentChangesetHandler(stub)(got, req)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, got.Code, got.Body.String())
		}
	}
	if stub.putCalls != 0 {
		t.Fatalf("invalid bodies reached coordinator %d times", stub.putCalls)
	}
}

func TestEnvironmentRuntimeDraftPersistsTheNormalizedReview(t *testing.T) {
	coordinator := &PostgresTenantMutationCoordinator{IDFactory: func(prefix orailtypes.IDPrefix) orailtypes.ID {
		return orailtypes.ID(string(prefix) + "_test")
	}}
	prepared, err := coordinator.prepareEnvironmentRuntimeItem(EnvironmentChangesetInput{
		Kind:      "runtime",
		Operation: "add",
		ServiceID: "svc_one",
		Name:      "service configuration",
		Runtime:   &EnvironmentRuntimeSpec{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.View.Runtime == nil {
		t.Fatal("runtime review is missing")
	}
	if prepared.View.Runtime.TargetPort != 8080 || prepared.View.Runtime.Builder != "auto" || prepared.View.Runtime.Replicas != 1 {
		t.Fatalf("runtime review was not normalized: %#v", prepared.View.Runtime)
	}
	if !strings.Contains(string(prepared.Payload), `"target_port":8080`) || !strings.Contains(string(prepared.Payload), `"builder":"auto"`) {
		t.Fatalf("persisted payload differs from normalized review: %s", prepared.Payload)
	}
}

func TestEnvironmentChangesetVolumeDeltaUsesPositiveNetAdds(t *testing.T) {
	for _, test := range []struct {
		name    string
		items   []EnvironmentChangesetItem
		delta   int64
		changed bool
	}{
		{name: "none"},
		{name: "add", items: []EnvironmentChangesetItem{{Kind: "storage", Operation: "add"}}, delta: 1, changed: true},
		{name: "neutral swap", items: []EnvironmentChangesetItem{{Kind: "storage", Operation: "add"}, {Kind: "storage", Operation: "remove"}}, changed: true},
		{name: "net two", items: []EnvironmentChangesetItem{{Kind: "storage", Operation: "add"}, {Kind: "runtime", Operation: "add"}, {Kind: "storage", Operation: "add"}}, delta: 2, changed: true},
		{name: "remove only", items: []EnvironmentChangesetItem{{Kind: "storage", Operation: "remove"}}, changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			delta, changed := environmentChangesetVolumeDelta(test.items)
			if delta != test.delta || changed != test.changed {
				t.Fatalf("delta=%d changed=%t, want %d/%t", delta, changed, test.delta, test.changed)
			}
		})
	}
}

func TestEnvironmentRuntimeBuildImpactUsesActualBeforeAfterDiffs(t *testing.T) {
	base := serviceconfig.Default("prj_one", "svc_one", "env_one")
	runtimeOnly := base
	runtimeOnly.StartCommand = "./midnight serve"
	if environmentRuntimeBuildFieldsChanged(base, runtimeOnly) {
		t.Fatal("runtime-only start command change was classified as a rebuild")
	}
	buildCommand := base
	buildCommand.BuildCommand = "go build ./cmd/server"
	if !environmentRuntimeBuildFieldsChanged(base, buildCommand) {
		t.Fatal("build_command change was not classified as a rebuild")
	}
	rootDirectory := base
	rootDirectory.RootDirectory = "apps/api"
	if !environmentRuntimeBuildFieldsChanged(base, rootDirectory) {
		t.Fatal("root_directory change was not classified as a rebuild")
	}
	if got := higherEnvironmentDeploymentAction(environmentDeploymentActionRedeploy, environmentDeploymentActionRebuild); got != environmentDeploymentActionRebuild {
		t.Fatalf("impact precedence=%q, want rebuild", got)
	}
	if got := higherEnvironmentDeploymentAction(environmentDeploymentActionRebuild, environmentDeploymentActionRedeploy); got != environmentDeploymentActionRebuild {
		t.Fatalf("impact downgrade=%q, want rebuild", got)
	}
}

func environmentChangesetRequest(method, action, body string, principal identity.Principal, session identity.Session) *http.Request {
	target := "/api/v1alpha/environments/env_one/changeset"
	if action != "" {
		target += "/" + action
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetPathValue("environment_id", "env_one")
	request.SetPathValue("changeset_action", action)
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	return request.WithContext(ctx)
}
