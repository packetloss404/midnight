package protocol

import (
	"testing"
	"time"
)

type fixedRandom float64

func (r fixedRandom) Float64() float64 { return float64(r) }

func TestHeartbeatRetryDelayExponentialCap(t *testing.T) {
	policy := RetryBackoff{Base: time.Second, Max: 5 * time.Second, JitterFraction: -1}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: time.Second},
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 5 * time.Second},
		{attempt: 10, want: 5 * time.Second},
	}
	for _, test := range tests {
		got := HeartbeatRetryDelay(test.attempt, policy, nil)
		if got != test.want {
			t.Fatalf("attempt %d delay = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestHeartbeatRetryDelayAppliesDeterministicJitter(t *testing.T) {
	policy := RetryBackoff{Base: 10 * time.Second, Max: time.Minute, JitterFraction: 0.2}

	if got := HeartbeatRetryDelay(0, policy, fixedRandom(0)); got != 8*time.Second {
		t.Fatalf("low jitter delay = %s, want 8s", got)
	}
	if got := HeartbeatRetryDelay(0, policy, fixedRandom(0.5)); got != 10*time.Second {
		t.Fatalf("mid jitter delay = %s, want 10s", got)
	}
	if got := HeartbeatRetryDelay(0, policy, fixedRandom(1)); got != 12*time.Second {
		t.Fatalf("high jitter delay = %s, want 12s", got)
	}
}

func TestHeartbeatRetryDelayUsesDefaults(t *testing.T) {
	got := HeartbeatRetryDelay(-1, RetryBackoff{}, nil)
	if got != DefaultHeartbeatRetryBase {
		t.Fatalf("delay = %s, want %s", got, DefaultHeartbeatRetryBase)
	}
}
