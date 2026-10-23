package protocol

import (
	"time"

	"github.com/midnight/midnight/pkg/agentapi"
)

// HeartbeatSequencer issues monotonically increasing heartbeat sequence
// numbers. Implementation lives in pkg/agentapi.
type HeartbeatSequencer = agentapi.HeartbeatSequencer

// HeartbeatMAC returns the canonical HMAC-SHA256 over (agentID || sequence).
func HeartbeatMAC(secret []byte, agentID string, sequence uint64) string {
	return agentapi.HeartbeatMAC(secret, agentID, sequence)
}

// VerifyHeartbeatMAC verifies the supplied HMAC matches HeartbeatMAC.
func VerifyHeartbeatMAC(secret []byte, agentID string, sequence uint64, supplied string) bool {
	return agentapi.VerifyHeartbeatMAC(secret, agentID, sequence, supplied)
}

// HeartbeatRequestMAC authenticates every field of a heartbeat request.
func HeartbeatRequestMAC(secret []byte, req HeartbeatRequest) string {
	return agentapi.HeartbeatRequestMAC(secret, req)
}

// VerifyHeartbeatRequestMAC verifies a complete heartbeat request.
func VerifyHeartbeatRequestMAC(secret []byte, req HeartbeatRequest, supplied string) bool {
	return agentapi.VerifyHeartbeatRequestMAC(secret, req, supplied)
}

// StatusReportMAC authenticates every mutable field of a status report.
func StatusReportMAC(secret []byte, report StatusReport) string {
	return agentapi.StatusReportMAC(secret, report)
}

// VerifyStatusReportMAC verifies a complete status report.
func VerifyStatusReportMAC(secret []byte, report StatusReport, supplied string) bool {
	return agentapi.VerifyStatusReportMAC(secret, report, supplied)
}

// HeartbeatVerificationResult enumerates verifier outcomes.
type HeartbeatVerificationResult = agentapi.HeartbeatVerificationResult

const (
	HeartbeatVerificationOK       = agentapi.HeartbeatVerificationOK
	HeartbeatVerificationTampered = agentapi.HeartbeatVerificationTampered
	HeartbeatVerificationExpired  = agentapi.HeartbeatVerificationExpired
	HeartbeatVerificationReplay   = agentapi.HeartbeatVerificationReplay
	HeartbeatVerificationMalform  = agentapi.HeartbeatVerificationMalform
)

// HeartbeatVerifier accepts/rejects incoming heartbeats. Constructed once
// per process; safe for concurrent use.
type HeartbeatVerifier = agentapi.HeartbeatVerifier

// NewHeartbeatVerifier returns a verifier with sensible Phase 1 defaults.
func NewHeartbeatVerifier(secret []byte, skewWindow, replayCache time.Duration) *HeartbeatVerifier {
	return agentapi.NewHeartbeatVerifier(secret, skewWindow, replayCache)
}
