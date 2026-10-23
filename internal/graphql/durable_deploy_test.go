// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	apiMiddleware "github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type recordingGraphQLDeployCoordinator struct {
	mu           sync.Mutex
	calls        int
	actors       []idempotency.Actor
	requests     []api.GraphQLDeployRequest
	fingerprints map[idempotency.Key]idempotency.RequestFingerprint
	results      map[idempotency.Key][]byte
}

func (c *recordingGraphQLDeployCoordinator) DeployGraphQL(ctx context.Context, request api.GraphQLDeployRequest) (api.GraphQLDeployResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.actors = append(c.actors, idempotency.ActorFromContext(ctx))
	c.requests = append(c.requests, request)
	if c.fingerprints == nil {
		c.fingerprints = map[idempotency.Key]idempotency.RequestFingerprint{}
		c.results = map[idempotency.Key][]byte{}
	}
	if previous, ok := c.fingerprints[request.Key]; ok {
		if previous != request.Fingerprint {
			return api.GraphQLDeployResult{}, idempotency.ErrKeyReusedDifferentBody
		}
		return api.GraphQLDeployResult{Body: append([]byte(nil), c.results[request.Key]...), Replayed: true}, nil
	}
	now := time.Date(2026, 8, 13, 15, 4, 5, 0, time.UTC)
	accepted := api.GraphQLDeployAccepted{
		Operation: orailtypes.Operation{ID: "op_graphql_durable", Kind: "deploy", State: orailtypes.OperationPending, CreatedAt: now, UpdatedAt: now},
		Deployment: api.DeploymentResource{
			ID: "dep_graphql_durable", ServiceID: string(request.ServiceID), ProjectID: "prj_graphql_durable",
			EnvironmentID: request.Intent.EnvironmentID, Image: request.Intent.Image,
			OperationID: "op_graphql_durable", OperationState: orailtypes.OperationPending,
			Status: "queued", LastEvent: "accepted into durable deploy outbox", LastEventAt: now,
		},
		InitialEvent: api.OperationEvent{OperationID: "op_graphql_durable", State: orailtypes.OperationPending, Message: "accepted into durable deploy outbox", CreatedAt: now},
	}
	body, err := request.EncodeResult(accepted)
	if err != nil {
		return api.GraphQLDeployResult{}, err
	}
	c.fingerprints[request.Key] = request.Fingerprint
	c.results[request.Key] = append([]byte(nil), body...)
	return api.GraphQLDeployResult{Body: append([]byte(nil), body...)}, nil
}

func TestDurableGraphQLDeployUsesAuthenticatedActorAndExactReplay(t *testing.T) {
	coordinator := &recordingGraphQLDeployCoordinator{}
	executor, err := NewExecutor(&api.ServicesAPI{GraphQLDeployIntake: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	const token = "midn_admin_graphql-durable-test-token"
	handler := apiMiddleware.RequestID(NewHandler(HandlerOptions{Executor: executor, BearerToken: token, ProductionAuthRequired: true}))
	requestBody := []byte(`{"query":"mutation Deploy($input: ServiceInstanceDeployInput!) { serviceInstanceDeployV2(input: $input, idempotencyKey: \"graphql-durable-key\") { id serviceId operationId status logs { message severity } } }","operationName":"Deploy","variables":{"input":{"serviceId":"svc_graphql_durable","environmentId":"env_graphql_durable","image":"registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)

	serve := func(body []byte, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/graphql/v2", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bearer)
		request.Header.Set(apiMiddleware.HeaderRequestID, "req-graphql-durable")
		request.Header.Set("User-Agent", "midnight-graphql-test")
		request.RemoteAddr = "192.0.2.44:443"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := serve(requestBody, token)
	second := serve(requestBody, token)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses first=%d second=%d; bodies first=%s second=%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("full GraphQL HTTP replay changed bytes:\nfirst  %q\nsecond %q", first.Body.Bytes(), second.Body.Bytes())
	}
	var firstResponse, secondResponse Response
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	if len(firstResponse.Errors) != 0 || len(secondResponse.Errors) != 0 {
		t.Fatalf("errors first=%+v second=%+v", firstResponse.Errors, secondResponse.Errors)
	}
	firstResult, err := json.Marshal(firstResponse.Data["serviceInstanceDeployV2"])
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := json.Marshal(secondResponse.Data["serviceInstanceDeployV2"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstResult, secondResult) {
		t.Fatalf("replayed selected result changed:\nfirst  %s\nsecond %s", firstResult, secondResult)
	}
	selected, ok := firstResponse.Data["serviceInstanceDeployV2"].(map[string]interface{})
	if !ok {
		t.Fatalf("selected durable result=%#v", firstResponse.Data["serviceInstanceDeployV2"])
	}
	logs, ok := selected["logs"].([]interface{})
	if !ok || len(logs) != 1 || logs[0].(map[string]interface{})["message"] != "accepted into durable deploy outbox" {
		t.Fatalf("transaction-local log snapshot=%#v", selected["logs"])
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.calls != 2 || len(coordinator.actors) != 2 {
		t.Fatalf("coordinator calls=%d actors=%v", coordinator.calls, coordinator.actors)
	}
	for _, actor := range coordinator.actors {
		if actor != idempotency.ActorRecoveryOperator {
			t.Fatalf("actor=%q, want authenticated recovery operator", actor)
		}
	}
	if got := coordinator.requests[0].Fingerprint; got.Method != http.MethodPost || got.Path != api.GraphQLDeployFingerprintPath || got.BodySHA256 == "" {
		t.Fatalf("fingerprint=%+v", got)
	}
	if got := coordinator.requests[0]; got.RequestID != "req-graphql-durable" || got.SourceIP != "192.0.2.44:443" || got.UserAgent != "midnight-graphql-test" {
		t.Fatalf("request audit evidence=%+v", got)
	}
	if !bytes.Equal(firstResult, coordinator.results["graphql-durable-key"]) {
		t.Fatalf("transport result=%s, committed selected result=%s", firstResult, coordinator.results["graphql-durable-key"])
	}
}

func TestDurableGraphQLDeployMustBeSoleTopLevelMutation(t *testing.T) {
	queries := map[string]string{
		"direct sibling": `mutation {
			serviceInstanceDeployV2(
				input: {serviceId: "svc_solo", environmentId: "env_solo", image: "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				idempotencyKey: "graphql-solo-key"
			) { id }
			projectCreate(input: {name: "must-not-run"}) { id }
		}`,
		"named fragment sibling": `mutation {
			...DurableDeploy
			projectCreate(input: {name: "must-not-run"}) { id }
		}
		fragment DurableDeploy on Mutation {
			serviceInstanceDeployV2(
				input: {serviceId: "svc_solo", environmentId: "env_solo", image: "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				idempotencyKey: "graphql-solo-key"
			) { id }
		}`,
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			coordinator := &recordingGraphQLDeployCoordinator{}
			executor, err := NewExecutor(&api.ServicesAPI{GraphQLDeployIntake: coordinator})
			if err != nil {
				t.Fatal(err)
			}
			response := executor.Execute(idempotency.WithActor(context.Background(), idempotency.ActorRecoveryOperator), Request{Query: query})
			if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != string(failurecodes.GraphQLDeployMustBeSoleMutation) {
				t.Fatalf("response=%+v, want sole-mutation rejection", response)
			}
			coordinator.mu.Lock()
			defer coordinator.mu.Unlock()
			if coordinator.calls != 0 {
				t.Fatalf("rejected compound mutation reached coordinator %d times", coordinator.calls)
			}
		})
	}
}

func TestDurableGraphQLDeployRejectsInvalidAuthAndDifferentRequestReplay(t *testing.T) {
	coordinator := &recordingGraphQLDeployCoordinator{}
	executor, err := NewExecutor(&api.ServicesAPI{GraphQLDeployIntake: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	const token = "midn_admin_graphql-durable-test-token"
	handler := NewHandler(HandlerOptions{Executor: executor, BearerToken: token, ProductionAuthRequired: true})
	query := `mutation Deploy($input: ServiceInstanceDeployInput!) { serviceInstanceDeployV2(input: $input, idempotencyKey: "graphql-scope-key") { id operationId } }`
	body := func(image string) []byte {
		t.Helper()
		value, err := json.Marshal(Request{Query: query, OperationName: "Deploy", Variables: map[string]interface{}{"input": map[string]interface{}{
			"serviceId": "svc_graphql_scope", "environmentId": "env_graphql_scope", "image": image,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	serve := func(payload []byte, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/graphql/v2", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	invalid := serve(body("registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "wrong-token")
	if invalid.Code != http.StatusUnauthorized {
		t.Fatalf("invalid auth status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	coordinator.mu.Lock()
	if coordinator.calls != 0 {
		coordinator.mu.Unlock()
		t.Fatalf("invalid auth reached coordinator %d times", coordinator.calls)
	}
	coordinator.mu.Unlock()

	first := serve(body("registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), token)
	conflict := serve(body("registry.example.test/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), token)
	if first.Code != http.StatusOK || conflict.Code != http.StatusOK {
		t.Fatalf("GraphQL transport statuses first=%d conflict=%d", first.Code, conflict.Code)
	}
	var response Response
	if err := json.Unmarshal(conflict.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY" {
		t.Fatalf("conflict response=%+v body=%s", response, conflict.Body.String())
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.requests[0].Fingerprint.BodySHA256 == coordinator.requests[1].Fingerprint.BodySHA256 {
		t.Fatalf("different GraphQL variables produced the same fingerprint: %+v", coordinator.requests[0].Fingerprint)
	}
}

func TestMemoryGraphQLDeployCompatibilityHasNoDurableCoordinator(t *testing.T) {
	stack := &api.DataStack{}
	services := api.BuildServicesAPI(stack)
	if services.GraphQLDeployIntake != nil {
		t.Fatalf("memory compatibility unexpectedly installed durable coordinator %T", services.GraphQLDeployIntake)
	}
}
