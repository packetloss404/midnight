// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package diagnosis

import (
	"sync"
	"time"
)

// DefaultRateLimit is the host-wide cap on diagnosis runs in
// DefaultRateWindow. Configurable via MIDNIGHT_AGENT_DIAGNOSIS_RATE.
// Holding the cap deliberately low keeps unsupervised hosts from
// burning agent spend during a flapping deploy.
const DefaultRateLimit = 10

// DefaultRateWindow is the bucket refill window paired with
// DefaultRateLimit.
const DefaultRateWindow = 5 * time.Minute

// DefaultCircuitBreakerThreshold is the consecutive Caller-error count
// that opens the circuit. Once tripped the runner skips work for
// DefaultCircuitBreakerCooldown before allowing another attempt.
const DefaultCircuitBreakerThreshold = 3

// DefaultCircuitBreakerCooldown is the duration the circuit stays open
// after tripping.
const DefaultCircuitBreakerCooldown = 10 * time.Minute

// TokenBucket is a small fixed-window rate limiter. The implementation
// is intentionally simple — diagnosis is bursty but low-rate, so a
// monotonic-window refill is sufficient and avoids the floating-point
// edges of a leaky-bucket. Exported for the runner test suite.
type TokenBucket struct {
	mu        sync.Mutex
	capacity  int
	tokens    int
	window    time.Duration
	resetAt   time.Time
	nowFunc   func() time.Time
	allowedAt time.Time
}

// NewTokenBucket returns a bucket starting at full capacity. capacity
// <= 0 or window <= 0 falls back to defaults so callers can pass through
// env overrides without re-validating.
func NewTokenBucket(capacity int, window time.Duration) *TokenBucket {
	if capacity <= 0 {
		capacity = DefaultRateLimit
	}
	if window <= 0 {
		window = DefaultRateWindow
	}
	return &TokenBucket{
		capacity: capacity,
		tokens:   capacity,
		window:   window,
		nowFunc:  time.Now,
	}
}

// SetClock overrides the clock used by the bucket. Tests inject a
// deterministic clock; production callers leave the default.
func (b *TokenBucket) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nowFunc = now
}

// Allow returns true and consumes a token when capacity is available.
// Refill happens lazily on the next call after the window has elapsed,
// which is cheap and gives steady-state guarantees without a background
// goroutine.
func (b *TokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.nowFunc()
	if b.resetAt.IsZero() {
		b.resetAt = now.Add(b.window)
	}
	if !now.Before(b.resetAt) {
		b.tokens = b.capacity
		b.resetAt = now.Add(b.window)
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	b.allowedAt = now
	return true
}

// CircuitBreaker tracks consecutive Caller errors and opens after a
// configurable threshold for a fixed cooldown. The breaker is a tiny
// state machine — closed, open — without a half-open probe step. The
// next Allow call after cooldown auto-resets to closed; success is
// reported separately via Success.
type CircuitBreaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	failures  int
	openedAt  time.Time
	nowFunc   func() time.Time
}

// NewCircuitBreaker returns a closed breaker. threshold/cooldown <= 0
// fall back to defaults to keep env-driven configuration robust.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = DefaultCircuitBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = DefaultCircuitBreakerCooldown
	}
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		nowFunc:   time.Now,
	}
}

// SetClock overrides the clock used by the breaker.
func (b *CircuitBreaker) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nowFunc = now
}

// Allow returns true when the breaker is closed (or the cooldown has
// elapsed). When cooldown elapses, the breaker resets to closed; a
// subsequent failure starts the counter fresh.
func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.threshold {
		return true
	}
	if b.nowFunc().Sub(b.openedAt) >= b.cooldown {
		b.failures = 0
		b.openedAt = time.Time{}
		return true
	}
	return false
}

// Success resets the consecutive-failure counter. Callers should only
// invoke this on a fully successful diagnosis run (Caller returned, the
// result persisted).
func (b *CircuitBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openedAt = time.Time{}
}

// Failure records one consecutive Caller error. The Nth failure opens
// the circuit; subsequent failures keep it open but do not extend the
// cooldown window.
func (b *CircuitBreaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures == b.threshold {
		b.openedAt = b.nowFunc()
	}
}

// State returns a snapshot of the breaker for observability. The
// returned values are advisory — Allow is the authoritative gate.
func (b *CircuitBreaker) State() (failures int, openedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures, b.openedAt
}
