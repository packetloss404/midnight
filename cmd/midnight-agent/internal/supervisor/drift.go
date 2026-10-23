package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// DriftClassification enumerates the deterministic drift outcomes the
// supervisor reports to evidence packets.
type DriftClassification string

const (
	DriftConverged DriftClassification = "converged"
	DriftDetected  DriftClassification = "detected"
	DriftUnknown   DriftClassification = "unknown"
)

// DriftEvent is one drift detector observation. It is JSON-friendly and
// rolls up into the AgentReleaseEvidencePacket.
type DriftEvent struct {
	Classification DriftClassification `json:"classification"`
	ExpectedHash   string              `json:"expected_hash"`
	ObservedHash   string              `json:"observed_hash"`
	Reason         string              `json:"reason,omitempty"`
	At             time.Time           `json:"at"`
}

// StateInput is one resource fact contributing to the desired/observed
// hash. Inputs MUST be sorted by Key before hashing; DriftDetector takes
// care of that internally.
type StateInput struct {
	Key   string
	Value string
}

// DriftDetector is a pure desired-vs-observed hash comparator. It performs
// no I/O and is safe for concurrent use only when callers serialize the
// SetExpected/Observe calls externally.
type DriftDetector struct{}

// HashState returns the deterministic SHA-256 digest of a StateInput slice.
func (DriftDetector) HashState(inputs []StateInput) string {
	cloned := append([]StateInput(nil), inputs...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Key < cloned[j].Key })
	hasher := sha256.New()
	for _, input := range cloned {
		key := strings.TrimSpace(input.Key)
		value := strings.TrimSpace(input.Value)
		hasher.Write([]byte(key))
		hasher.Write([]byte("="))
		hasher.Write([]byte(value))
		hasher.Write([]byte("\n"))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// Compare returns a deterministic DriftEvent at observation time at.
// An empty expected hash classifies the observation as Unknown.
func (d DriftDetector) Compare(at time.Time, expected, observed []StateInput) DriftEvent {
	expectedHash := d.HashState(expected)
	observedHash := d.HashState(observed)
	event := DriftEvent{
		ExpectedHash: expectedHash,
		ObservedHash: observedHash,
		At:           at,
	}
	switch {
	case len(expected) == 0:
		event.Classification = DriftUnknown
		event.Reason = "no expected state"
	case expectedHash == observedHash:
		event.Classification = DriftConverged
	default:
		event.Classification = DriftDetected
		event.Reason = "hash mismatch"
	}
	return event
}

// SelfHealDecision enumerates restart-attempt outcomes from the cooldown
// gate.
type SelfHealDecision string

const (
	SelfHealAllow      SelfHealDecision = "allow"
	SelfHealCooldownOn SelfHealDecision = "cooldown"
)

// SelfHealEvent is one cooldown-gate observation. It JSON-marshals into
// the runtime evidence packet.
type SelfHealEvent struct {
	InstanceID string           `json:"instance_id"`
	Decision   SelfHealDecision `json:"decision"`
	Reason     string           `json:"reason,omitempty"`
	WaitFor    time.Duration    `json:"wait_for,omitempty"`
	NextEarly  time.Time        `json:"next_earliest,omitempty"`
	At         time.Time        `json:"at"`
}

// SelfHealCooldown enforces the minimum wait between restart attempts on
// the same instance. The cooldown is a pure data structure; callers feed
// it timestamps and receive deterministic SelfHealEvents.
type SelfHealCooldown struct {
	Min       time.Duration
	lastTried map[string]time.Time
}

// NewSelfHealCooldown constructs a cooldown gate with the given minimum
// inter-attempt window. A non-positive min defaults to 30 seconds.
func NewSelfHealCooldown(min time.Duration) *SelfHealCooldown {
	if min <= 0 {
		min = 30 * time.Second
	}
	return &SelfHealCooldown{Min: min, lastTried: make(map[string]time.Time)}
}

// Try records an attempt against the cooldown and returns whether it is
// allowed. A SelfHealEvent is emitted in either case.
func (s *SelfHealCooldown) Try(instanceID string, at time.Time) SelfHealEvent {
	if s.lastTried == nil {
		s.lastTried = make(map[string]time.Time)
	}
	id := strings.TrimSpace(instanceID)
	if id == "" {
		return SelfHealEvent{Decision: SelfHealCooldownOn, Reason: "missing instance id", At: at}
	}
	last, ok := s.lastTried[id]
	if !ok || at.Sub(last) >= s.Min {
		s.lastTried[id] = at
		return SelfHealEvent{InstanceID: id, Decision: SelfHealAllow, At: at}
	}
	wait := s.Min - at.Sub(last)
	if wait < 0 {
		wait = 0
	}
	return SelfHealEvent{
		InstanceID: id,
		Decision:   SelfHealCooldownOn,
		Reason:     "minimum cooldown not reached",
		WaitFor:    wait,
		NextEarly:  last.Add(s.Min),
		At:         at,
	}
}
