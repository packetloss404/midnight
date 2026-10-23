package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/pkg/agentapi"
)

func newAgentMux(t *testing.T) (*http.ServeMux, *AgentRegistry) {
	t.Helper()
	registry := NewAgentRegistry([]byte("phase1-test-secret"), func() time.Time { return time.Unix(1700_000_000, 0).UTC() })
	mux := http.NewServeMux()
	RegisterAgentHandlers(mux, registry)
	return mux, registry
}

func wrapWithRequestID(handler http.Handler) http.Handler { return middleware.RequestID(handler) }

func authorizeAgentRequest(req *http.Request, credential string) {
	req.Header.Set("Authorization", "Bearer "+credential)
}

func TestAgentRegisterHappyPath(t *testing.T) {
	mux, _ := newAgentMux(t)
	body := agentapi.RegistrationRequest{
		AgentID:   "agent-1",
		Version:   version.Version,
		Hostname:  "host-1",
		OS:        "linux",
		Arch:      "amd64",
		Timestamp: time.Unix(1700_000_000, 0).UTC(),
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, bytes.NewReader(payload))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp agentapi.RegistrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AgentID != "agent-1" {
		t.Fatalf("agent id = %q", resp.AgentID)
	}
	if resp.HeartbeatInterval == 0 {
		t.Fatal("expected heartbeat interval")
	}
}

func TestAgentRegisterRejectsVersionSkew(t *testing.T) {
	mux, _ := newAgentMux(t)
	body := agentapi.RegistrationRequest{
		AgentID:   "agent-1",
		Version:   "999.0.0-from-mars",
		Timestamp: time.Unix(1700_000_000, 0).UTC(),
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, bytes.NewReader(payload))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != failurecodes.VersionSkewRejected {
		t.Fatalf("expected VERSION_SKEW_REJECTED, got %s", env.Error.Code)
	}
}

func TestAgentHeartbeatVerifiesHMAC(t *testing.T) {
	mux, registry := newAgentMux(t)
	regBody, _ := json.Marshal(agentapi.RegistrationRequest{AgentID: "agent-2", Version: version.Version, Timestamp: time.Unix(1700_000_000, 0).UTC()})
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, bytes.NewReader(regBody))
	authorizeAgentRequest(req, "phase1-test-secret")
	wrapWithRequestID(mux).ServeHTTP(httptest.NewRecorder(), req)

	hb := agentapi.HeartbeatRequest{
		AgentID:  "agent-2",
		Sequence: 1,
		Status:   "ok",
		Runtime:  "docker",
		SentAt:   time.Unix(1700_000_000, 0).UTC(),
	}
	hb.HMAC = agentapi.HeartbeatRequestMAC([]byte("phase1-test-secret"), hb)
	hbBody, _ := json.Marshal(hb)
	req = httptest.NewRequest(http.MethodPost, "/api/v1alpha/agents/agent-2/heartbeat", bytes.NewReader(hbBody))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if last, ok := registry.LastHeartbeat("agent-2"); !ok || last.IsZero() {
		t.Fatal("registry should record last heartbeat")
	}
}

func TestAgentHeartbeatRejectsTamperedHMAC(t *testing.T) {
	mux, _ := newAgentMux(t)
	hb := agentapi.HeartbeatRequest{
		AgentID:  "agent-2",
		Sequence: 1,
		HMAC:     "deadbeef",
		SentAt:   time.Unix(1700_000_000, 0).UTC(),
	}
	hbBody, _ := json.Marshal(hb)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/agents/agent-2/heartbeat", bytes.NewReader(hbBody))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var env ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != failurecodes.AgentHeartbeatTampered {
		t.Fatalf("expected AGENT_HEARTBEAT_TAMPERED, got %s", env.Error.Code)
	}
}

func TestAgentDesiredReturnsETagAndPayload(t *testing.T) {
	mux, registry := newAgentMux(t)
	registry.SetDesiredState("agent-3", `"etag-a"`, []byte(`{"instances":[{"deployment_id":"dep1"}]}`))
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/agents/agent-3/desired", nil)
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("ETag") != `"etag-a"` {
		t.Fatalf("etag = %q", rec.Header().Get("ETag"))
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`dep1`)) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAgentDesiredHonorsIfNoneMatch(t *testing.T) {
	mux, registry := newAgentMux(t)
	registry.SetDesiredState("agent-4", `"etag-b"`, []byte(`{}`))
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/agents/agent-4/desired", nil)
	authorizeAgentRequest(req, "phase1-test-secret")
	req.Header.Set("If-None-Match", `"etag-b"`)
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAgentStatusRecordsReport(t *testing.T) {
	mux, registry := newAgentMux(t)
	report := agentapi.StatusReport{
		AgentID:    "agent-5",
		Sequence:   1,
		ObservedAt: time.Unix(1700_000_000, 0).UTC(),
		Instances: []agentapi.InstanceStatus{{
			DeploymentID: "dep1",
			InstanceID:   "inst1",
			State:        "RUNNING",
		}},
	}
	report.HMAC = agentapi.StatusReportMAC([]byte("phase1-test-secret"), report)
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/agents/agent-5/status", bytes.NewReader(body))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got, ok := registry.LastStatusReport("agent-5"); !ok || len(got.Instances) != 1 {
		t.Fatalf("registry = %+v ok=%t", got, ok)
	}
}

func TestAgentCredentialsAreScopedToAgentID(t *testing.T) {
	credentials, err := NewStaticAgentCredentials(map[string][]string{
		"agent-a": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"agent-b": {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistryWithCredentials(credentials, func() time.Time { return time.Unix(1700_000_000, 0).UTC() })
	mux := http.NewServeMux()
	RegisterAgentHandlers(mux, registry)
	body, _ := json.Marshal(agentapi.RegistrationRequest{AgentID: "agent-b", Version: version.Version})
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, bytes.NewReader(body))
	authorizeAgentRequest(req, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-agent credential status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentSubRouterRejectsNonCanonicalPaths(t *testing.T) {
	mux, _ := newAgentMux(t)
	for _, path := range []string{
		"/api/v1alpha/agents/%20/desired",
		"/api/v1alpha/agents/agent-a/desired/extra",
		"/api/v1alpha/agents/agent-a/desired/",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			authorizeAgentRequest(request, "phase1-test-secret")
			response := httptest.NewRecorder()
			wrapWithRequestID(mux).ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d body=%s, want 404", response.Code, response.Body.String())
			}
		})
	}

	// Exercise the dispatcher directly because net/http's ServeMux canonicalizes
	// a double slash before dispatch. The handler itself must never interpret the
	// resulting empty identity as an authentication target.
	registry := NewAgentRegistry([]byte("phase1-test-secret"), nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/agents//desired", nil)
	authorizeAgentRequest(request, "phase1-test-secret")
	response := httptest.NewRecorder()
	registry.subRouter(nil).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("empty agent id status = %d body=%s, want 404", response.Code, response.Body.String())
	}
}

func TestAgentJSONTrustBoundaryRejectsDuplicateTrailingUnknownAndOversizeBodies(t *testing.T) {
	mux, _ := newAgentMux(t)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "register duplicate", path: AgentRegisterPath, body: `{"agent_id":"agent-json","agent_id":"agent-json","version":"` + version.Version + `"}`},
		{name: "register nested duplicate", path: AgentRegisterPath, body: `{"agent_id":"agent-json","version":"` + version.Version + `","labels":{"region":"one","region":"two"}}`},
		{name: "register trailing", path: AgentRegisterPath, body: `{"agent_id":"agent-json","version":"` + version.Version + `"} {}`},
		{name: "register unknown", path: AgentRegisterPath, body: `{"agent_id":"agent-json","version":"` + version.Version + `","unexpected":true}`},
		{name: "heartbeat duplicate", path: "/api/v1alpha/agents/agent-json/heartbeat", body: `{"agent_id":"agent-json","sequence":1,"sequence":2,"sent_at":"2023-11-14T22:13:20Z"}`},
		{name: "heartbeat trailing", path: "/api/v1alpha/agents/agent-json/heartbeat", body: `{"agent_id":"agent-json","sequence":1,"sent_at":"2023-11-14T22:13:20Z"} {}`},
		{name: "heartbeat unknown", path: "/api/v1alpha/agents/agent-json/heartbeat", body: `{"agent_id":"agent-json","sequence":1,"sent_at":"2023-11-14T22:13:20Z","unexpected":true}`},
		{name: "status duplicate", path: "/api/v1alpha/agents/agent-json/status", body: `{"agent_id":"agent-json","sequence":1,"sequence":2,"observed_at":"2023-11-14T22:13:20Z"}`},
		{name: "status trailing", path: "/api/v1alpha/agents/agent-json/status", body: `{"agent_id":"agent-json","sequence":1,"observed_at":"2023-11-14T22:13:20Z"} {}`},
		{name: "status unknown", path: "/api/v1alpha/agents/agent-json/status", body: `{"agent_id":"agent-json","sequence":1,"observed_at":"2023-11-14T22:13:20Z","unexpected":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			authorizeAgentRequest(request, "phase1-test-secret")
			response := httptest.NewRecorder()
			wrapWithRequestID(mux).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
			}
		})
	}

	largeValue := strings.Repeat("x", int(defaultJSONBodyLimitBytes))
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "register", path: AgentRegisterPath, body: `{"agent_id":"agent-json","version":"` + version.Version + `","hostname":"` + largeValue + `"}`},
		{name: "heartbeat", path: "/api/v1alpha/agents/agent-json/heartbeat", body: `{"agent_id":"agent-json","sequence":1,"status":"` + largeValue + `","sent_at":"2023-11-14T22:13:20Z","hmac":""}`},
		{name: "status", path: "/api/v1alpha/agents/agent-json/status", body: `{"agent_id":"agent-json","sequence":1,"observed_at":"2023-11-14T22:13:20Z","instances":[{"reason":"` + largeValue + `"}],"hmac":""}`},
	} {
		t.Run(test.name+" oversize", func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			authorizeAgentRequest(request, "phase1-test-secret")
			response := httptest.NewRecorder()
			wrapWithRequestID(mux).ServeHTTP(response, request)
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d body=%s, want 413", response.Code, response.Body.String())
			}
		})
	}
}

func TestAgentAdmissionUsesAuthenticatedIdentityAndDirectIP(t *testing.T) {
	credentials, err := NewStaticAgentCredentials(map[string][]string{
		"agent-a": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"agent-b": {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistryWithCredentials(credentials, func() time.Time { return time.Unix(1700_000_000, 0).UTC() })
	admission := NewAPIAdmissionController(APIAdmissionLimits{
		RequestsPerWindow:             1,
		AgentPreAuthRequestsPerWindow: 10,
		Window:                        time.Minute,
		SSEConcurrent:                 1,
	}, func() time.Time { return time.Unix(1700_000_000, 0).UTC() })
	mux := http.NewServeMux()
	RegisterAgentHandlersWithAdmission(mux, registry, admission)

	serve := func(agentID, credential, remoteAddr string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/agents/"+agentID+"/desired", nil)
		request.RemoteAddr = remoteAddr
		authorizeAgentRequest(request, credential)
		response := httptest.NewRecorder()
		wrapWithRequestID(mux).ServeHTTP(response, request)
		return response
	}

	// Invalid and cross-agent credentials must not consume an authenticated
	// identity's budget, even when every request comes from the same peer.
	for _, credential := range []string{"invalid", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		response := serve("agent-b", credential, "192.0.2.10:443")
		if response.Code != http.StatusUnauthorized || response.Header().Get("RateLimit-Limit") != "10" {
			t.Fatalf("invalid auth status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	}

	firstA := serve("agent-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "192.0.2.10:443")
	secondA := serve("agent-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "192.0.2.10:443")
	firstB := serve("agent-b", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "192.0.2.10:443")
	otherIPA := serve("agent-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "192.0.2.11:443")
	if firstA.Code != http.StatusOK || secondA.Code != http.StatusTooManyRequests ||
		firstB.Code != http.StatusOK || otherIPA.Code != http.StatusOK {
		t.Fatalf("statuses agent-a=%d/%d agent-b=%d other-ip=%d", firstA.Code, secondA.Code, firstB.Code, otherIPA.Code)
	}
	if secondA.Header().Get("Retry-After") != "60" || !strings.Contains(secondA.Body.String(), `"code":"REQUEST_RATE_LIMITED"`) {
		t.Fatalf("rate rejection headers=%v body=%s", secondA.Header(), secondA.Body.String())
	}
}

func TestAgentAdmissionBoundsInvalidCredentialsByDirectIP(t *testing.T) {
	credentials, err := NewStaticAgentCredentials(map[string][]string{
		"agent-a": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewAgentRegistryWithCredentials(credentials, time.Now)
	admission := NewAPIAdmissionController(APIAdmissionLimits{
		RequestsPerWindow:             10,
		AgentPreAuthRequestsPerWindow: 2,
		Window:                        time.Minute,
		SSEConcurrent:                 1,
	}, func() time.Time { return time.Unix(1700_000_000, 0).UTC() })
	mux := http.NewServeMux()
	RegisterAgentHandlersWithAdmission(mux, registry, admission)

	serve := func(agentID, remoteAddr string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/agents/"+agentID+"/desired", nil)
		request.RemoteAddr = remoteAddr
		request.Header.Set("X-Forwarded-For", agentID+".invalid")
		authorizeAgentRequest(request, "invalid")
		response := httptest.NewRecorder()
		wrapWithRequestID(mux).ServeHTTP(response, request)
		return response
	}
	first := serve("invented-a", "192.0.2.20:443")
	second := serve("invented-b", "192.0.2.20:443")
	third := serve("invented-c", "192.0.2.20:443")
	otherIP := serve("invented-d", "192.0.2.21:443")
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusUnauthorized ||
		third.Code != http.StatusTooManyRequests || otherIP.Code != http.StatusUnauthorized {
		t.Fatalf("invalid-auth statuses = %d/%d/%d other-ip=%d", first.Code, second.Code, third.Code, otherIP.Code)
	}
	if third.Header().Get("Retry-After") != "60" {
		t.Fatalf("invalid-auth rejection headers=%v body=%s", third.Header(), third.Body.String())
	}
}

func TestAgentStatusRejectsTamperingAndReplay(t *testing.T) {
	mux, _ := newAgentMux(t)
	report := agentapi.StatusReport{
		AgentID:    "agent-status",
		Sequence:   1,
		ObservedAt: time.Unix(1700_000_000, 0).UTC(),
	}
	report.HMAC = agentapi.StatusReportMAC([]byte("phase1-test-secret"), report)
	report.Instances = []agentapi.InstanceStatus{{DeploymentID: "tampered"}}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/agents/agent-status/status", bytes.NewReader(body))
	authorizeAgentRequest(req, "phase1-test-secret")
	rec := httptest.NewRecorder()
	wrapWithRequestID(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered status = %d body=%s", rec.Code, rec.Body.String())
	}

	report.Instances = nil
	report.HMAC = agentapi.StatusReportMAC([]byte("phase1-test-secret"), report)
	body, _ = json.Marshal(report)
	for attempt, want := range []int{http.StatusOK, http.StatusConflict} {
		req = httptest.NewRequest(http.MethodPost, "/api/v1alpha/agents/agent-status/status", bytes.NewReader(body))
		authorizeAgentRequest(req, "phase1-test-secret")
		rec = httptest.NewRecorder()
		wrapWithRequestID(mux).ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("attempt %d status = %d body=%s want=%d", attempt, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestEnforceAgentVersion(t *testing.T) {
	if err := EnforceAgentVersion(version.Version); err != nil {
		t.Fatalf("expected own version to pass: %v", err)
	}
	if err := EnforceAgentVersion("0.0.0-different"); err == nil {
		t.Fatal("expected mismatch")
	}
	if err := EnforceAgentVersion(""); err == nil {
		t.Fatal("expected empty mismatch")
	}
}
