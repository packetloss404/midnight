package registry

import (
	"testing"
	"time"
)

func TestTransitionPinsMarksActiveAndRollback(t *testing.T) {
	until := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	pins := TransitionPins(PinTransition{
		Repository:     "app",
		PreviousDigest: "sha256:old",
		ActiveDigest:   "sha256:new",
		RollbackUntil:  until,
	})

	if len(pins) != 2 {
		t.Fatalf("TransitionPins() len = %d, want 2: %#v", len(pins), pins)
	}
	if pins[0].Digest != "sha256:new" || pins[0].State != PinStateActive {
		t.Fatalf("active pin = %#v", pins[0])
	}
	if pins[1].Digest != "sha256:old" || pins[1].State != PinStateRollback || !pins[1].RollbackUntil.Equal(until) {
		t.Fatalf("rollback pin = %#v", pins[1])
	}
}

func TestTransitionPinsMarksSupersededWithoutRollbackWindow(t *testing.T) {
	pins := TransitionPins(PinTransition{
		Repository:     "app",
		PreviousDigest: "sha256:old",
		ActiveDigest:   "sha256:new",
	})

	if len(pins) != 2 || pins[1].State != PinStateSuperseded {
		t.Fatalf("TransitionPins() = %#v", pins)
	}
}

func TestProtectedDigestProbeResultRecordsProtectionDecision(t *testing.T) {
	probedAt := time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)
	result := ProtectedDigestProbeResult{
		Repository: "app",
		Digest:     "sha256:new",
		Protected:  true,
		Reason:     "active pin",
		ProbedAt:   probedAt,
	}

	if !result.Protected || result.Repository != "app" || result.Digest != "sha256:new" || result.Reason != "active pin" || !result.ProbedAt.Equal(probedAt) {
		t.Fatalf("ProtectedDigestProbeResult = %#v", result)
	}
}
