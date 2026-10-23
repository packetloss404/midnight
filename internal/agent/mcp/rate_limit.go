// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// DefaultRateLimitPerMinute is the per-session tool-call cap when the
// caller doesn't supply one. Operators can override via the
// MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE env var.
const DefaultRateLimitPerMinute = 60

// EnvRateLimitPerMinute names the operator override.
const EnvRateLimitPerMinute = "MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE"

// rateLimiter is a simple per-session token bucket. The bucket holds
// `capacity` tokens and refills `capacity` tokens every minute; one
// tools/call consumes one token. Lower-level operations (tools/list,
// resources/list, the manifest) do NOT consume tokens — only the
// surface that can drive load against the platform does.
type rateLimiter struct {
	mu       sync.Mutex
	capacity int
	now      func() time.Time
	buckets  map[string]*bucket
}

type bucket struct {
	remaining  int
	resetAfter time.Time
}

// newRateLimiter constructs a limiter. capacity <= 0 falls back to
// the env override (when set) or DefaultRateLimitPerMinute.
func newRateLimiter(capacity int, now func() time.Time) *rateLimiter {
	if capacity <= 0 {
		capacity = resolveRateLimit()
	}
	if now == nil {
		now = func() time.Time { return time.Now() }
	}
	return &rateLimiter{
		capacity: capacity,
		now:      now,
		buckets:  map[string]*bucket{},
	}
}

// allow consumes a token for sessionID and returns true when the call
// is permitted. The second return is the number of remaining tokens
// (useful for diagnostic logging).
func (r *rateLimiter) allow(sessionID string) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[sessionID]
	now := r.now()
	if !ok || !now.Before(b.resetAfter) {
		b = &bucket{remaining: r.capacity, resetAfter: now.Add(time.Minute)}
		r.buckets[sessionID] = b
	}
	if b.remaining <= 0 {
		return false, 0
	}
	b.remaining--
	return true, b.remaining
}

// capacityFor returns the configured cap (used by the manifest).
func (r *rateLimiter) capacityFor() int {
	return r.capacity
}

func resolveRateLimit() int {
	raw := os.Getenv(EnvRateLimitPerMinute)
	if raw == "" {
		return DefaultRateLimitPerMinute
	}
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		return v
	}
	return DefaultRateLimitPerMinute
}
