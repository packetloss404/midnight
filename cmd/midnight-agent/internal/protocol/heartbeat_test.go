package protocol

import (
	"testing"
	"time"
)

func TestHeartbeatSequencerStartsAtOne(t *testing.T) {
	var sequencer HeartbeatSequencer

	if got := sequencer.Next(); got != 1 {
		t.Fatalf("first sequence = %d, want 1", got)
	}
	if got := sequencer.Next(); got != 2 {
		t.Fatalf("second sequence = %d, want 2", got)
	}
}

func TestHeartbeatMACIsDeterministic(t *testing.T) {
	secret := []byte("test-secret")
	first := HeartbeatMAC(secret, " agent-a ", 42)
	second := HeartbeatMAC(secret, "agent-a", 42)

	if first != second {
		t.Fatalf("MAC mismatch: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("MAC length = %d, want 64 hex chars", len(first))
	}
}

func TestVerifyHeartbeatMAC(t *testing.T) {
	secret := []byte("test-secret")
	mac := HeartbeatMAC(secret, "agent-a", 7)

	if !VerifyHeartbeatMAC(secret, "agent-a", 7, mac) {
		t.Fatal("VerifyHeartbeatMAC rejected valid MAC")
	}
	if VerifyHeartbeatMAC(secret, "agent-a", 8, mac) {
		t.Fatal("VerifyHeartbeatMAC accepted wrong sequence")
	}
}

func TestHeartbeatVerifierAcceptsFreshHeartbeat(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, 30*time.Second, time.Minute)
	now := time.Unix(1_700_000_000, 0).UTC()
	req := HeartbeatRequest{AgentID: "agent-a", Sequence: 1, Status: "ok", Runtime: "docker", SentAt: now}
	req.HMAC = HeartbeatRequestMAC(secret, req)

	result, err := verifier.Verify(req, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result != HeartbeatVerificationOK {
		t.Fatalf("expected ok, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsTamperedHeartbeat(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, time.Minute, time.Minute)
	now := time.Unix(1_700_000_000, 0).UTC()
	req := HeartbeatRequest{AgentID: "agent-a", Sequence: 1, Status: "ok", Runtime: "docker", SentAt: now}
	req.HMAC = HeartbeatRequestMAC(secret, req)
	req.Status = "draining"

	result, err := verifier.Verify(req, now)
	if err == nil {
		t.Fatal("expected error for tampered heartbeat")
	}
	if result != HeartbeatVerificationTampered {
		t.Fatalf("expected tampered, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsExpiredHeartbeat(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, time.Second, time.Minute)
	sent := time.Unix(1_700_000_000, 0).UTC()
	now := sent.Add(2 * time.Second)
	req := HeartbeatRequest{AgentID: "agent-a", Sequence: 1, SentAt: sent}
	req.HMAC = HeartbeatRequestMAC(secret, req)

	result, err := verifier.Verify(req, now)
	if err == nil {
		t.Fatal("expected error for expired heartbeat")
	}
	if result != HeartbeatVerificationExpired {
		t.Fatalf("expected expired, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsReplayedHeartbeat(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, time.Minute, time.Minute)
	now := time.Unix(1_700_000_000, 0).UTC()
	req := HeartbeatRequest{AgentID: "agent-a", Sequence: 5, SentAt: now}
	req.HMAC = HeartbeatRequestMAC(secret, req)

	if _, err := verifier.Verify(req, now); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	result, err := verifier.Verify(req, now.Add(time.Second))
	if err == nil {
		t.Fatal("expected replay error")
	}
	if result != HeartbeatVerificationReplay {
		t.Fatalf("expected replay, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsBackwardSequence(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, time.Minute, time.Minute)
	now := time.Unix(1_700_000_000, 0).UTC()
	first := HeartbeatRequest{AgentID: "agent-a", Sequence: 5, SentAt: now}
	first.HMAC = HeartbeatRequestMAC(secret, first)
	if _, err := verifier.Verify(first, now); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	older := HeartbeatRequest{AgentID: "agent-a", Sequence: 3, SentAt: now}
	older.HMAC = HeartbeatRequestMAC(secret, older)
	result, err := verifier.Verify(older, now)
	if err == nil {
		t.Fatal("expected replay error for backward sequence")
	}
	if result != HeartbeatVerificationReplay {
		t.Fatalf("expected replay, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsMissingAgent(t *testing.T) {
	verifier := NewHeartbeatVerifier(nil, time.Minute, time.Minute)
	result, err := verifier.Verify(HeartbeatRequest{}, time.Now())
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if result != HeartbeatVerificationMalform {
		t.Fatalf("expected malformed, got %s", result)
	}
}

func TestHeartbeatVerifierRejectsMissingTimestamp(t *testing.T) {
	secret := []byte("verify-secret")
	verifier := NewHeartbeatVerifier(secret, time.Minute, time.Minute)
	req := HeartbeatRequest{AgentID: "agent-a", Sequence: 1}
	req.HMAC = HeartbeatRequestMAC(secret, req)

	result, err := verifier.Verify(req, time.Now())
	if err == nil {
		t.Fatal("expected error for missing timestamp")
	}
	if result != HeartbeatVerificationMalform {
		t.Fatalf("expected malformed, got %s", result)
	}
}
