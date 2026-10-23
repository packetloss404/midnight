// Package agentapi defines the over-the-wire DTOs and HMAC helpers shared
// by the host agent (cmd/midnight-agent) and the control plane
// (internal/api). It is the only Go-source seam between the two; the
// INTERFACES_AGENT.md panel decision pins agent → control-plane sharing to
// pkg/orailtypes and pkg/agentapi (this package) plus proto/agent/v1
// (Phase 3).
//
// Lane 2 ships the wire shapes in this package; Lane 1 wires the storage
// and migration; cmd/midnight-agent and internal/api both import from
// here.
package agentapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Endpoints. The §1.16 endpoint set is normative.
const (
	PathRegister  = "/api/v1alpha/agents/register"
	PathHeartbeat = "/api/v1alpha/agents/%s/heartbeat"
	PathDesired   = "/api/v1alpha/agents/%s/desired"
	PathStatus    = "/api/v1alpha/agents/%s/status"

	HeaderAgentID      = "X-Midnight-Agent-ID"
	HeaderAgentVersion = "X-Midnight-Agent-Version"
	HeaderETag         = "If-None-Match"
)

// RegistrationRequest is sent when an agent first announces itself.
type RegistrationRequest struct {
	AgentID   string            `json:"agent_id"`
	Version   string            `json:"version"`
	Hostname  string            `json:"hostname"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// RegistrationResponse contains control-plane registration metadata.
type RegistrationResponse struct {
	AgentID           string        `json:"agent_id"`
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`
	DesiredStateURL   string        `json:"desired_state_url,omitempty"`
}

// HeartbeatRequest reports agent liveness and local runtime status.
type HeartbeatRequest struct {
	AgentID         string    `json:"agent_id"`
	Sequence        uint64    `json:"sequence"`
	Status          string    `json:"status"`
	Runtime         string    `json:"runtime"`
	SentAt          time.Time `json:"sent_at"`
	MaintenanceMode bool      `json:"maintenance_mode,omitempty"`
	DrainMode       bool      `json:"drain_mode,omitempty"`
	HMAC            string    `json:"hmac"`
}

// HeartbeatResponse acknowledges the latest accepted heartbeat sequence.
type HeartbeatResponse struct {
	AcceptedSequence uint64 `json:"accepted_sequence"`
	DesiredStateETag string `json:"desired_state_etag,omitempty"`
	WarmupActive     bool   `json:"warmup_active,omitempty"`
}

// DesiredStateResponse is the Phase 1 placeholder for desired-state polling.
type DesiredStateResponse struct {
	ETag        string
	NotModified bool
	Raw         []byte
}

// InstanceStatus mirrors the runtime.InstanceStatus shape. The agent ships
// a list of these on each StatusReport.
type InstanceStatus struct {
	DeploymentID string    `json:"deployment_id"`
	InstanceID   string    `json:"instance_id"`
	State        string    `json:"state"`
	ObservedAt   time.Time `json:"observed_at"`
	Reason       string    `json:"reason,omitempty"`
}

// RouteTargetObservation is the agent-owned ingress target discovery shape
// kept for backward compatibility with cmd/midnight-agent's existing
// supervisor.
type RouteTargetObservation struct {
	ProjectID           string    `json:"project_id"`
	EnvironmentID       string    `json:"environment_id"`
	RouteID             string    `json:"route_id,omitempty"`
	RouteName           string    `json:"route_name,omitempty"`
	DeploymentID        string    `json:"deployment_id"`
	InstanceID          string    `json:"instance_id"`
	ContainerID         string    `json:"container_id"`
	ContainerName       string    `json:"container_name,omitempty"`
	ContainerIP         string    `json:"container_ip"`
	TargetProtocol      string    `json:"target_protocol,omitempty"`
	InternalServicePort int       `json:"internal_service_port"`
	MappedHostPort      int       `json:"mapped_host_port,omitempty"`
	ReadinessStatus     string    `json:"readiness_status"`
	LastObservedAt      time.Time `json:"last_observed_at"`
}

// Drift carries the agent's daemon.json baseline + cgroup mode summary the
// control plane records on each status report.
type Drift struct {
	APIVersion        string   `json:"api_version,omitempty"`
	EngineVersion     string   `json:"engine_version,omitempty"`
	StorageDriver     string   `json:"storage_driver,omitempty"`
	LiveRestore       bool     `json:"live_restore"`
	LoggingDriver     string   `json:"logging_driver,omitempty"`
	NoNewPrivileges   bool     `json:"no_new_privileges"`
	CGroupVersion     string   `json:"cgroup_version,omitempty"`
	BaselineConflicts []string `json:"baseline_conflicts,omitempty"`
}

// StatusReport is the agent → control-plane observed-state batch sent on
// the §1.16 status endpoint after each reconcile pass.
type StatusReport struct {
	AgentID      string                   `json:"agent_id"`
	Sequence     uint64                   `json:"sequence"`
	ObservedAt   time.Time                `json:"observed_at"`
	Instances    []InstanceStatus         `json:"instances,omitempty"`
	RouteTargets []RouteTargetObservation `json:"route_targets,omitempty"`
	Drift        *Drift                   `json:"drift,omitempty"`
	HMAC         string                   `json:"hmac"`
}

// StatusReportResponse is the control plane's acknowledgement.
type StatusReportResponse struct {
	AcceptedAt time.Time `json:"accepted_at"`
}

// HeartbeatSequencer issues monotonically increasing heartbeat sequence
// numbers. Phase 0's already-locked HMAC helper is preserved here so the
// HMAC payload is compatible with control-plane verifiers built before the
// shared package landed.
type HeartbeatSequencer struct {
	next uint64
}

// Next returns the next heartbeat sequence number, starting at 1.
func (s *HeartbeatSequencer) Next() uint64 { return atomic.AddUint64(&s.next, 1) }

// HeartbeatMAC returns the legacy deterministic HMAC-SHA256 over the
// (agentID || sequence) payload. New callers must use HeartbeatRequestMAC so
// every security-relevant field in the heartbeat is authenticated.
func HeartbeatMAC(secret []byte, agentID string, sequence uint64) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(heartbeatMACPayload(agentID, sequence)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHeartbeatMAC compares supplied against HeartbeatMAC in constant time.
func VerifyHeartbeatMAC(secret []byte, agentID string, sequence uint64, supplied string) bool {
	expected, err := hex.DecodeString(HeartbeatMAC(secret, agentID, sequence))
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimSpace(supplied))
	if err != nil {
		return false
	}
	return hmac.Equal(actual, expected)
}

// HeartbeatRequestMAC returns an HMAC-SHA256 over the complete canonical
// heartbeat body, excluding the HMAC field itself. This prevents an
// authenticated request's runtime, status, timestamp, or drain flags from
// being changed in transit.
func HeartbeatRequestMAC(secret []byte, req HeartbeatRequest) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(heartbeatRequestMACPayload(req)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHeartbeatRequestMAC compares supplied against HeartbeatRequestMAC in
// constant time.
func VerifyHeartbeatRequestMAC(secret []byte, req HeartbeatRequest, supplied string) bool {
	expected, err := hex.DecodeString(HeartbeatRequestMAC(secret, req))
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimSpace(supplied))
	if err != nil {
		return false
	}
	return hmac.Equal(actual, expected)
}

// StatusReportMAC returns an HMAC-SHA256 over the complete status report,
// excluding the HMAC field itself. Sequence and observed_at are included so
// the control plane can reject replayed or stale mutable observations.
func StatusReportMAC(secret []byte, report StatusReport) string {
	report.HMAC = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyStatusReportMAC compares supplied against StatusReportMAC in constant
// time.
func VerifyStatusReportMAC(secret []byte, report StatusReport, supplied string) bool {
	expected, err := hex.DecodeString(StatusReportMAC(secret, report))
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimSpace(supplied))
	if err != nil {
		return false
	}
	return hmac.Equal(actual, expected)
}

// HeartbeatVerificationResult enumerates accept/reject categories.
type HeartbeatVerificationResult string

const (
	HeartbeatVerificationOK       HeartbeatVerificationResult = "ok"
	HeartbeatVerificationTampered HeartbeatVerificationResult = "tampered"
	HeartbeatVerificationExpired  HeartbeatVerificationResult = "expired"
	HeartbeatVerificationReplay   HeartbeatVerificationResult = "replay"
	HeartbeatVerificationMalform  HeartbeatVerificationResult = "malformed"
)

// HeartbeatVerifier accepts/rejects incoming heartbeats with HMAC, monotonic
// sequence, and clock-skew window. The control plane shares this struct
// across handler scopes; the type is safe for concurrent use.
type HeartbeatVerifier struct {
	Secret      []byte
	SkewWindow  time.Duration
	ReplayCache time.Duration

	mu        sync.Mutex
	lastSeq   map[string]uint64
	lastSeen  map[string]time.Time
	cacheKeys map[string]map[uint64]struct{}
}

// NewHeartbeatVerifier returns a verifier with sensible Phase 1 defaults.
func NewHeartbeatVerifier(secret []byte, skewWindow, replayCache time.Duration) *HeartbeatVerifier {
	if skewWindow <= 0 {
		skewWindow = 30 * time.Second
	}
	if replayCache <= 0 {
		replayCache = 5 * time.Minute
	}
	return &HeartbeatVerifier{
		Secret:      append([]byte(nil), secret...),
		SkewWindow:  skewWindow,
		ReplayCache: replayCache,
		lastSeq:     make(map[string]uint64),
		lastSeen:    make(map[string]time.Time),
		cacheKeys:   make(map[string]map[uint64]struct{}),
	}
}

// Verify checks the supplied request against HMAC, monotonic sequence, and
// the configured skew window.
func (v *HeartbeatVerifier) Verify(req HeartbeatRequest, now time.Time) (HeartbeatVerificationResult, error) {
	if v == nil {
		return HeartbeatVerificationMalform, fmt.Errorf("heartbeat verifier is nil")
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return HeartbeatVerificationMalform, fmt.Errorf("heartbeat request missing agent id")
	}
	if req.Sequence == 0 {
		return HeartbeatVerificationMalform, fmt.Errorf("heartbeat request sequence must be positive")
	}
	if req.SentAt.IsZero() {
		return HeartbeatVerificationMalform, fmt.Errorf("heartbeat request timestamp is required")
	}
	if !VerifyHeartbeatRequestMAC(v.Secret, req, req.HMAC) {
		return HeartbeatVerificationTampered, fmt.Errorf("heartbeat hmac verification failed")
	}
	offset := now.Sub(req.SentAt)
	if offset < 0 {
		offset = -offset
	}
	if offset > v.SkewWindow {
		return HeartbeatVerificationExpired, fmt.Errorf("heartbeat outside skew window: %s > %s", offset, v.SkewWindow)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.evictOldLocked(now)
	if cache, ok := v.cacheKeys[agentID]; ok {
		if _, seen := cache[req.Sequence]; seen {
			return HeartbeatVerificationReplay, fmt.Errorf("heartbeat sequence %d replayed", req.Sequence)
		}
	}
	if last, ok := v.lastSeq[agentID]; ok && req.Sequence <= last {
		return HeartbeatVerificationReplay, fmt.Errorf("heartbeat sequence %d not greater than last accepted %d", req.Sequence, last)
	}
	v.lastSeq[agentID] = req.Sequence
	v.lastSeen[agentID] = now
	if v.cacheKeys[agentID] == nil {
		v.cacheKeys[agentID] = make(map[uint64]struct{})
	}
	v.cacheKeys[agentID][req.Sequence] = struct{}{}
	return HeartbeatVerificationOK, nil
}

func (v *HeartbeatVerifier) evictOldLocked(now time.Time) {
	for agentID, seen := range v.lastSeen {
		if now.Sub(seen) > v.ReplayCache {
			delete(v.lastSeen, agentID)
			delete(v.lastSeq, agentID)
			delete(v.cacheKeys, agentID)
		}
	}
}

func heartbeatMACPayload(agentID string, sequence uint64) string {
	return fmt.Sprintf("%s\n%s", strings.TrimSpace(agentID), strconv.FormatUint(sequence, 10))
}

func heartbeatRequestMACPayload(req HeartbeatRequest) string {
	return strings.Join([]string{
		strings.TrimSpace(req.AgentID),
		strconv.FormatUint(req.Sequence, 10),
		req.Status,
		req.Runtime,
		req.SentAt.UTC().Format(time.RFC3339Nano),
		strconv.FormatBool(req.MaintenanceMode),
		strconv.FormatBool(req.DrainMode),
	}, "\n")
}
