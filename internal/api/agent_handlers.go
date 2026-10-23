package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/pkg/agentapi"
)

// Agent endpoints (Phase 1, Lane 2). The router that owns the v1alpha mux
// (Lane 1's internal/api/router.go) MUST call RegisterAgentHandlers from a
// new file when wiring this lane in. Lane 2 does NOT modify router.go to
// preserve lane ownership.
const (
	AgentRegisterPath  = "/api/v1alpha/agents/register"
	AgentHeartbeatPath = "/api/v1alpha/agents/" // base — sub-routed by handler
)

// AgentRegistry stores in-memory bookkeeping for the §1.16 agent endpoints.
// Lane 1 owns the durable storage (migrations/0002_agents.up.sql); for the
// Phase 1 slice this in-memory store lets server-tier integration tests
// exercise the loop without depending on the full persistence stack.
type AgentRegistry struct {
	mu          sync.Mutex
	now         func() time.Time
	credentials AgentCredentialResolver
	agents      map[string]*registeredAgent
}

type registeredAgent struct {
	id            string
	hostname      string
	agentVersion  string
	registeredAt  time.Time
	lastHeartbeat time.Time
	lastSequence  uint64
	lastStatusSeq uint64
	lastStatus    string
	lastRuntime   string
	maintenance   bool
	drain         bool
	desired       []byte
	desiredETag   string
	statusReport  agentapi.StatusReport
	warmupActive  bool
}

type AgentSnapshot struct {
	AgentID         string                `json:"agent_id"`
	Hostname        string                `json:"hostname,omitempty"`
	Version         string                `json:"version,omitempty"`
	RegisteredAt    time.Time             `json:"registered_at"`
	LastHeartbeat   time.Time             `json:"last_heartbeat,omitempty"`
	LastSequence    uint64                `json:"last_sequence,omitempty"`
	Status          string                `json:"status,omitempty"`
	Runtime         string                `json:"runtime,omitempty"`
	MaintenanceMode bool                  `json:"maintenance_mode,omitempty"`
	DrainMode       bool                  `json:"drain_mode,omitempty"`
	DesiredETag     string                `json:"desired_etag,omitempty"`
	StatusReport    agentapi.StatusReport `json:"status_report,omitempty"`
}

// NewAgentRegistry constructs an in-memory registry using the explicit shared
// development credential compatibility path. Production startup must use
// NewAgentRegistryWithCredentials with a per-agent resolver.
func NewAgentRegistry(hmacSecret []byte, now func() time.Time) *AgentRegistry {
	return NewAgentRegistryWithCredentials(NewDevelopmentAgentCredentials(hmacSecret), now)
}

// NewAgentRegistryWithCredentials constructs a registry whose routes are
// authenticated by credentials scoped to the target agent ID.
func NewAgentRegistryWithCredentials(credentials AgentCredentialResolver, now func() time.Time) *AgentRegistry {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &AgentRegistry{
		now:         now,
		credentials: credentials,
		agents:      make(map[string]*registeredAgent),
	}
}

// RegisterAgentHandlers attaches the §1.16 agent endpoints to mux. Lane 5
// wires this from internal/api/router.go's newRouterWithDataStack so every
// router built via NewRouter / NewRouterWithOperationStore /
// NewRouterWithDataStack exposes the four agent endpoints below.
//
//   - POST /api/v1alpha/agents/register
//   - POST /api/v1alpha/agents/{id}/heartbeat
//   - GET  /api/v1alpha/agents/{id}/desired
//   - POST /api/v1alpha/agents/{id}/status
//
// The function is intentionally side-effect free apart from mux registration
// so it can be tested in isolation.
func RegisterAgentHandlers(mux handlerRegistrar, registry *AgentRegistry, wrappers ...func(http.Handler) http.Handler) {
	registerAgentHandlers(mux, registry, nil, wrappers...)
}

// RegisterAgentHandlersWithAdmission preserves the existing handler registrar
// API while giving the production router a two-stage agent admission path:
// direct-IP pre-authentication protection followed by an authenticated
// agent-identity-plus-IP budget.
func RegisterAgentHandlersWithAdmission(mux handlerRegistrar, registry *AgentRegistry, admission *APIAdmissionController, wrappers ...func(http.Handler) http.Handler) {
	registerAgentHandlers(mux, registry, admission, wrappers...)
}

func registerAgentHandlers(mux handlerRegistrar, registry *AgentRegistry, admission *APIAdmissionController, wrappers ...func(http.Handler) http.Handler) {
	if mux == nil || registry == nil {
		return
	}
	wrap := func(handler http.Handler) http.Handler { return handler }
	for _, candidate := range wrappers {
		if candidate != nil {
			previous := wrap
			wrap = func(handler http.Handler) http.Handler {
				return candidate(previous(handler))
			}
		}
	}
	mux.Handle(AgentRegisterPath, wrap(registry.registerHandler(admission)))
	mux.Handle(AgentHeartbeatPath, wrap(registry.subRouter(admission)))
}

func (r *AgentRegistry) registerHandler(admission *APIAdmissionController) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqID := middleware.FromContext(req.Context())
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WriteError(w, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "register requires POST", reqID, nil)
			return
		}
		if !admission.admitAgentPreAuthentication(w, req) {
			return
		}
		var body agentapi.RegistrationRequest
		if !decodeStrictJSONRequest(w, req, &body, "invalid registration body") {
			return
		}
		if body.AgentID == "" || strings.TrimSpace(body.AgentID) != body.AgentID || strings.Contains(body.AgentID, "/") {
			WriteError(w, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "agent_id must be a canonical path-safe identifier", reqID, nil)
			return
		}
		if _, ok := r.authenticate(req, body.AgentID); !ok {
			r.writeUnauthorized(w, reqID)
			return
		}
		if !admission.admitAuthenticatedAgent(w, req, body.AgentID) {
			return
		}
		if err := EnforceAgentVersion(body.Version); err != nil {
			WriteError(w, http.StatusUpgradeRequired, failurecodes.VersionSkewRejected, err.Error(), reqID, map[string]any{
				"server_version": version.Version,
				"agent_version":  body.Version,
			})
			return
		}
		now := r.now()
		if !body.Timestamp.IsZero() {
			drift := now.Sub(body.Timestamp)
			if drift < 0 {
				drift = -drift
			}
			if drift > 30*time.Second {
				WriteError(w, http.StatusBadRequest, failurecodes.AgentClockSkew, "agent clock skew exceeds 30s", reqID, map[string]any{
					"drift_seconds": drift.Seconds(),
				})
				return
			}
		}

		r.mu.Lock()
		ra, ok := r.agents[body.AgentID]
		if !ok {
			ra = &registeredAgent{id: body.AgentID, registeredAt: now}
			r.agents[body.AgentID] = ra
		}
		ra.hostname = body.Hostname
		ra.agentVersion = body.Version
		r.mu.Unlock()

		WriteJSON(w, http.StatusOK, agentapi.RegistrationResponse{
			AgentID:           body.AgentID,
			HeartbeatInterval: 5 * time.Second,
			DesiredStateURL:   "/api/v1alpha/agents/" + body.AgentID + "/desired",
		})
	})
}

func (r *AgentRegistry) subRouter(admission *APIAdmissionController) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/api/v1alpha/agents/")
		segments := strings.Split(path, "/")
		if len(segments) != 2 || segments[0] == "" || strings.TrimSpace(segments[0]) != segments[0] || segments[1] == "" {
			WriteError(w, http.StatusNotFound, failurecodes.ResourceNotFound, "unknown agent endpoint", middleware.FromContext(req.Context()), nil)
			return
		}
		agentID := segments[0]
		switch segments[1] {
		case "heartbeat":
			r.heartbeatHandler(agentID, admission).ServeHTTP(w, req)
		case "desired":
			r.desiredHandler(agentID, admission).ServeHTTP(w, req)
		case "status":
			r.statusHandler(agentID, admission).ServeHTTP(w, req)
		default:
			WriteError(w, http.StatusNotFound, failurecodes.ResourceNotFound, "unknown agent endpoint", middleware.FromContext(req.Context()), nil)
		}
	})
}

func (r *AgentRegistry) heartbeatHandler(agentID string, admission *APIAdmissionController) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqID := middleware.FromContext(req.Context())
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WriteError(w, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "heartbeat requires POST", reqID, nil)
			return
		}
		if !admission.admitAgentPreAuthentication(w, req) {
			return
		}
		credential, ok := r.authenticate(req, agentID)
		if !ok {
			r.writeUnauthorized(w, reqID)
			return
		}
		if !admission.admitAuthenticatedAgent(w, req, agentID) {
			return
		}
		var body agentapi.HeartbeatRequest
		if !decodeStrictJSONRequest(w, req, &body, "invalid heartbeat body") {
			return
		}
		if body.AgentID == "" {
			body.AgentID = agentID
		}
		if body.AgentID != agentID {
			WriteError(w, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "agent id mismatch", reqID, nil)
			return
		}

		if body.Sequence == 0 || body.SentAt.IsZero() {
			WriteError(w, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "heartbeat malformed", reqID, nil)
			return
		}
		if !agentapi.VerifyHeartbeatRequestMAC(credential, body, body.HMAC) {
			WriteError(w, http.StatusUnauthorized, failurecodes.AgentHeartbeatTampered, "heartbeat hmac verification failed", reqID, nil)
			return
		}
		now := r.now()
		offset := now.Sub(body.SentAt)
		if offset < 0 {
			offset = -offset
		}
		if offset > 30*time.Second {
			WriteError(w, http.StatusBadRequest, failurecodes.AgentHeartbeatExpired, "heartbeat outside clock-skew window", reqID, nil)
			return
		}

		r.mu.Lock()
		ra := r.touchAgentLocked(agentID)
		if body.Sequence <= ra.lastSequence {
			r.mu.Unlock()
			WriteError(w, http.StatusConflict, failurecodes.AgentHeartbeatReplay, "heartbeat sequence was already accepted", reqID, nil)
			return
		}
		ra.lastHeartbeat = now
		ra.lastSequence = body.Sequence
		ra.lastStatus = body.Status
		ra.lastRuntime = body.Runtime
		ra.maintenance = body.MaintenanceMode
		ra.drain = body.DrainMode
		etag := ra.desiredETag
		r.mu.Unlock()

		WriteJSON(w, http.StatusOK, agentapi.HeartbeatResponse{
			AcceptedSequence: body.Sequence,
			DesiredStateETag: etag,
		})
	})
}

func (r *AgentRegistry) desiredHandler(agentID string, admission *APIAdmissionController) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqID := middleware.FromContext(req.Context())
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WriteError(w, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "desired requires GET", reqID, nil)
			return
		}
		if !admission.admitAgentPreAuthentication(w, req) {
			return
		}
		if _, ok := r.authenticate(req, agentID); !ok {
			r.writeUnauthorized(w, reqID)
			return
		}
		if !admission.admitAuthenticatedAgent(w, req, agentID) {
			return
		}
		r.mu.Lock()
		ra := r.touchAgentLocked(agentID)
		etag := ra.desiredETag
		desired := ra.desired
		r.mu.Unlock()

		if ifNoneMatch := strings.TrimSpace(req.Header.Get("If-None-Match")); ifNoneMatch != "" && ifNoneMatch == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag == "" {
			etag = `"empty"`
			desired = []byte(`{"instances":[],"agent_id":"` + agentID + `"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(desired)
	})
}

func (r *AgentRegistry) statusHandler(agentID string, admission *APIAdmissionController) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqID := middleware.FromContext(req.Context())
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WriteError(w, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "status requires POST", reqID, nil)
			return
		}
		if !admission.admitAgentPreAuthentication(w, req) {
			return
		}
		credential, ok := r.authenticate(req, agentID)
		if !ok {
			r.writeUnauthorized(w, reqID)
			return
		}
		if !admission.admitAuthenticatedAgent(w, req, agentID) {
			return
		}
		var body agentapi.StatusReport
		if !decodeStrictJSONRequest(w, req, &body, "invalid status body") {
			return
		}
		if body.AgentID != agentID {
			WriteError(w, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "agent id mismatch", reqID, nil)
			return
		}
		if body.Sequence == 0 || body.ObservedAt.IsZero() {
			WriteError(w, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "status sequence and observed_at are required", reqID, nil)
			return
		}
		if !agentapi.VerifyStatusReportMAC(credential, body, body.HMAC) {
			WriteError(w, http.StatusUnauthorized, failurecodes.AgentStatusReportTampered, "status report hmac verification failed", reqID, nil)
			return
		}
		now := r.now()
		offset := now.Sub(body.ObservedAt)
		if offset < 0 {
			offset = -offset
		}
		if offset > 30*time.Second {
			WriteError(w, http.StatusBadRequest, failurecodes.AgentStatusReportExpired, "status report outside clock-skew window", reqID, nil)
			return
		}
		r.mu.Lock()
		ra := r.touchAgentLocked(agentID)
		if body.Sequence <= ra.lastStatusSeq {
			r.mu.Unlock()
			WriteError(w, http.StatusConflict, failurecodes.AgentStatusReportReplay, "status report sequence was already accepted", reqID, nil)
			return
		}
		ra.lastStatusSeq = body.Sequence
		ra.statusReport = body
		r.mu.Unlock()
		WriteJSON(w, http.StatusOK, agentapi.StatusReportResponse{AcceptedAt: now})
	})
}

func (r *AgentRegistry) authenticate(req *http.Request, agentID string) ([]byte, bool) {
	if r == nil || r.credentials == nil || req == nil {
		return nil, false
	}
	// Resolver implementations normalize lookup input for configuration
	// ergonomics. Route authentication must be stricter so aliases such as a
	// whitespace-padded path cannot mint distinct admission identities for one
	// credential.
	if agentID == "" || strings.TrimSpace(agentID) != agentID || strings.Contains(agentID, "/") {
		return nil, false
	}
	if headerAgentID := strings.TrimSpace(req.Header.Get(agentapi.HeaderAgentID)); headerAgentID != "" && headerAgentID != agentID {
		return nil, false
	}
	values := req.Header.Values("Authorization")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return nil, false
	}
	scheme, credential, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(credential) != credential || credential == "" {
		return nil, false
	}
	return matchAgentCredential(r.credentials, agentID, credential)
}

func (r *AgentRegistry) writeUnauthorized(w http.ResponseWriter, requestID string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="midnight-agent"`)
	WriteError(w, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "agent credentials are invalid", requestID, nil)
}

func (r *AgentRegistry) touchAgentLocked(agentID string) *registeredAgent {
	ra, ok := r.agents[agentID]
	if !ok {
		ra = &registeredAgent{id: agentID, registeredAt: r.now()}
		r.agents[agentID] = ra
	}
	return ra
}

// SetDesiredState records desired state for agentID and bumps the ETag.
// Lane 1 will replace this with a database-backed implementation; the
// in-memory variant is here to make the integration test deterministic.
func (r *AgentRegistry) SetDesiredState(agentID, etag string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ra := r.touchAgentLocked(agentID)
	ra.desired = append([]byte(nil), payload...)
	ra.desiredETag = etag
}

// LastStatusReport returns the most recent StatusReport for agentID. Used by
// integration tests asserting reconcile reached the server.
func (r *AgentRegistry) LastStatusReport(agentID string) (agentapi.StatusReport, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ra, ok := r.agents[agentID]
	if !ok {
		return agentapi.StatusReport{}, false
	}
	return ra.statusReport, true
}

// LastHeartbeat returns the timestamp of the most recent accepted heartbeat
// for agentID, plus a boolean reporting whether the agent is registered.
func (r *AgentRegistry) LastHeartbeat(agentID string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ra, ok := r.agents[agentID]
	if !ok {
		return time.Time{}, false
	}
	return ra.lastHeartbeat, true
}

func (r *AgentRegistry) Snapshot() []AgentSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]AgentSnapshot, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, AgentSnapshot{
			AgentID:         agent.id,
			Hostname:        agent.hostname,
			Version:         agent.agentVersion,
			RegisteredAt:    agent.registeredAt,
			LastHeartbeat:   agent.lastHeartbeat,
			LastSequence:    agent.lastSequence,
			Status:          agent.lastStatus,
			Runtime:         agent.lastRuntime,
			MaintenanceMode: agent.maintenance,
			DrainMode:       agent.drain,
			DesiredETag:     agent.desiredETag,
			StatusReport:    agent.statusReport,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// EnforceAgentVersion returns an error when the agent's reported version is
// outside the supported window. Phase 1 ships a deliberately-narrow window
// that requires byte-equal version strings; Lane 1's compat matrix
// (versions.yaml) will widen it once Phase 1 stabilizes.
func EnforceAgentVersion(agentVersion string) error {
	a := strings.TrimSpace(agentVersion)
	if a == "" {
		return errors.New("agent version is required")
	}
	server := strings.TrimSpace(version.Version)
	if a == server {
		return nil
	}
	return errors.New("agent version " + a + " is outside the supported compatibility window (server " + server + ")")
}
