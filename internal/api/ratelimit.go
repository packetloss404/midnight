// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
)

const (
	defaultAPIRequestLimit       = 600
	defaultAPIRequestWindow      = time.Minute
	defaultAPISSEConcurrentLimit = 4
	defaultAgentPreAuthLimit     = 120
	maxAPIAdmissionBuckets       = 65536
	apiAdmissionOverflowKey      = "overflow"
)

// APIAdmissionLimits controls authenticated request admission for one router.
// Zero values select conservative service defaults rather than disabling
// protection. Tests and embedders can set explicit positive values.
type APIAdmissionLimits struct {
	RequestsPerWindow             int
	AgentPreAuthRequestsPerWindow int
	Window                        time.Duration
	SSEConcurrent                 int
}

type apiRateBucket struct {
	count int
	reset time.Time
}

type APIAdmissionController struct {
	mu        sync.Mutex
	limits    APIAdmissionLimits
	now       func() time.Time
	requests  map[string]apiRateBucket
	sseActive map[string]int
}

func NewAPIAdmissionController(limits APIAdmissionLimits, now func() time.Time) *APIAdmissionController {
	if limits.RequestsPerWindow <= 0 {
		limits.RequestsPerWindow = defaultAPIRequestLimit
	}
	if limits.Window <= 0 {
		limits.Window = defaultAPIRequestWindow
	}
	if limits.AgentPreAuthRequestsPerWindow <= 0 {
		limits.AgentPreAuthRequestsPerWindow = defaultAgentPreAuthLimit
	}
	if limits.SSEConcurrent <= 0 {
		limits.SSEConcurrent = defaultAPISSEConcurrentLimit
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &APIAdmissionController{
		limits:    limits,
		now:       now,
		requests:  make(map[string]apiRateBucket),
		sseActive: make(map[string]int),
	}
}

// admitAgentPreAuthentication bounds credential parsing and verification by
// direct peer IP only. It deliberately ignores agent IDs and forwarded headers,
// so invalid credentials cannot manufacture high-cardinality limiter keys.
func (c *APIAdmissionController) admitAgentPreAuthentication(w http.ResponseWriter, r *http.Request) bool {
	if c == nil {
		return true
	}
	key := "agent-preauth:\x00" + directClientIP(r.RemoteAddr)
	decision := c.admitRequestWithLimit(key, c.limits.AgentPreAuthRequestsPerWindow)
	setActualRateLimitHeaders(w, decision)
	if decision.allowed {
		return true
	}
	setRetryAfter(w, decision.retryAfter)
	writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.RequestRateLimited, "agent authentication rate limit exceeded", nil)
	return false
}

type apiAdmissionDecision struct {
	allowed    bool
	limit      int
	remaining  int
	reset      time.Time
	retryAfter time.Duration
}

func (c *APIAdmissionController) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := apiAdmissionKey(r)
		decision := c.admitRequest(key)
		setActualRateLimitHeaders(w, decision)
		if !decision.allowed {
			setRetryAfter(w, decision.retryAfter)
			writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.RequestRateLimited, "request rate limit exceeded", nil)
			return
		}

		if isSSEAdmissionRequest(r) {
			if !c.acquireSSE(key) {
				w.Header().Set("Retry-After", "1")
				writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.RequestRateLimited, "stream concurrency limit exceeded", nil)
				return
			}
			defer c.releaseSSE(key)
		}
		next.ServeHTTP(w, r)
	})
}

// admitAuthenticatedAgent applies the shared request budget only after the
// agent handler has verified a credential scoped to agentID. This prevents an
// unauthenticated caller from consuming a real agent's allowance and keeps
// co-located agents on independent identity-plus-direct-IP budgets.
func (c *APIAdmissionController) admitAuthenticatedAgent(w http.ResponseWriter, r *http.Request, agentID string) bool {
	if c == nil {
		return true
	}
	decision := c.admitRequest(agentAdmissionKey(agentID, r.RemoteAddr))
	setActualRateLimitHeaders(w, decision)
	if decision.allowed {
		return true
	}
	setRetryAfter(w, decision.retryAfter)
	writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.RequestRateLimited, "request rate limit exceeded", nil)
	return false
}

func (c *APIAdmissionController) admitRequest(key string) apiAdmissionDecision {
	return c.admitRequestWithLimit(key, c.limits.RequestsPerWindow)
}

func (c *APIAdmissionController) admitRequestWithLimit(key string, limit int) apiAdmissionDecision {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.requests) >= maxAPIAdmissionBuckets {
		for candidate, existing := range c.requests {
			if candidate != key && !now.Before(existing.reset) {
				delete(c.requests, candidate)
			}
		}
		if _, exists := c.requests[key]; !exists && len(c.requests) >= maxAPIAdmissionBuckets {
			// Fail new high-cardinality tuples into one shared budget. Evicting a
			// live bucket would let its caller immediately reset its allowance.
			key = apiAdmissionOverflowKey
		}
	}
	bucket, ok := c.requests[key]
	if !ok || !now.Before(bucket.reset) {
		bucket = apiRateBucket{reset: now.Add(c.limits.Window)}
	}
	if bucket.count >= limit {
		c.requests[key] = bucket
		return apiAdmissionDecision{
			allowed:    false,
			limit:      limit,
			remaining:  0,
			reset:      bucket.reset,
			retryAfter: bucket.reset.Sub(now),
		}
	}
	bucket.count++
	c.requests[key] = bucket
	return apiAdmissionDecision{
		allowed:   true,
		limit:     limit,
		remaining: limit - bucket.count,
		reset:     bucket.reset,
	}
}

func (c *APIAdmissionController) acquireSSE(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sseActive[key] >= c.limits.SSEConcurrent {
		return false
	}
	c.sseActive[key]++
	return true
}

func (c *APIAdmissionController) releaseSSE(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sseActive[key] <= 1 {
		delete(c.sseActive, key)
		return
	}
	c.sseActive[key]--
}

func apiAdmissionKey(r *http.Request) string {
	return string(idempotency.ActorFromContext(r.Context())) + "\x00" +
		string(idempotency.TenantFromContext(r.Context())) + "\x00" +
		directClientIP(r.RemoteAddr)
}

func agentAdmissionKey(agentID, remoteAddr string) string {
	return "agent:" + agentID + "\x00" + directClientIP(remoteAddr)
}

// directClientIP intentionally ignores Forwarded and X-Forwarded-For. Midnight
// does not yet expose a trusted-proxy network configuration, so accepting
// either header would let a client choose its own limiter key.
func directClientIP(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}

func setActualRateLimitHeaders(w http.ResponseWriter, decision apiAdmissionDecision) {
	w.Header().Set("RateLimit-Limit", strconv.Itoa(decision.limit))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(decision.remaining))
	w.Header().Set("RateLimit-Reset", strconv.FormatInt(decision.reset.Unix(), 10))
}

func setRetryAfter(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

func isSSEAdmissionRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.URL.Path == v1alphaPrefix+"/observability/logs/stream" ||
		strings.HasSuffix(r.URL.Path, "/stream") && strings.HasPrefix(r.URL.Path, AgentConversationsPrefix+"/") {
		return true
	}
	_, action, ok := parseOperationPath(r.URL.Path)
	return ok && action == "events"
}
