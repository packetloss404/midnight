package protocol

import "time"

const (
	DefaultHeartbeatRetryBase   = 500 * time.Millisecond
	DefaultHeartbeatRetryMax    = 30 * time.Second
	DefaultHeartbeatRetryJitter = 0.2
)

// RandomSource supplies deterministic jitter values in tests and callers.
type RandomSource interface {
	Float64() float64
}

// RetryBackoff describes capped exponential retry timing with optional jitter.
type RetryBackoff struct {
	Base           time.Duration
	Max            time.Duration
	JitterFraction float64
}

// HeartbeatRetryDelay returns the retry delay for a failed heartbeat attempt.
// attempt is zero-based: the first retry uses Base before jitter is applied.
func HeartbeatRetryDelay(attempt int, policy RetryBackoff, random RandomSource) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := policy.Base
	if base <= 0 {
		base = DefaultHeartbeatRetryBase
	}
	maxDelay := policy.Max
	if maxDelay <= 0 {
		maxDelay = DefaultHeartbeatRetryMax
	}
	if maxDelay < base {
		maxDelay = base
	}

	delay := base
	for i := 0; i < attempt; i++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if delay > maxDelay || delay < 0 {
		delay = maxDelay
	}

	jitter := policy.JitterFraction
	if jitter == 0 {
		jitter = DefaultHeartbeatRetryJitter
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	if random == nil || jitter == 0 {
		return delay
	}

	factor := 1 + ((clampFloat(random.Float64())*2)-1)*jitter
	jittered := time.Duration(float64(delay) * factor)
	if jittered < 0 {
		return 0
	}
	if jittered > maxDelay {
		return maxDelay
	}
	return jittered
}

func clampFloat(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
