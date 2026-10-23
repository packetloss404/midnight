// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/xid"

	platformauth "github.com/midnight/midnight/internal/auth"
)

// session is the per-connection state the MCP server keeps. A session
// is established on the first authenticated HTTP request and is keyed
// by its xid (sortable, ULID-equivalent for our purposes — 12 bytes,
// monotonically increasing, base32 encoded).
//
// AllowMutations is set from the X-Midnight-Allow-Mutations header on
// `initialize` (or from initialize.params._meta.allow_mutations) and
// applies for the lifetime of the connection.
type session struct {
	ID                 string
	ProjectAccessToken string
	AllowMutations     bool
	// Initialized flips to true after `initialize` succeeds. tools/call
	// and other surface methods refuse to run before initialize.
	Initialized bool
}

// sessionStore caches per-connection sessions. The MCP wire keys
// sessions by the Mcp-Session-Id response header (echoed by the
// client on subsequent requests); we use a fresh xid per inbound
// request that has no session id.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*session{}}
}

// get returns the session for id, or nil when unknown.
func (s *sessionStore) get(id string) *session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

// create installs a new session and returns it.
func (s *sessionStore) create() *session {
	id := xid.New().String()
	sess := &session{ID: id}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess
}

// drop removes a session (e.g. on close).
func (s *sessionStore) drop(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// authResult is the parsed Authorization headers off one request.
// Token is the bearer token. ProjectAccessToken is the optional
// Project-Access-Token header forwarded straight through (the
// authorizer chain in the API layer interprets it).
type authResult struct {
	Token              string
	ProjectAccessToken string
	AllowMutations     bool
}

// extractAuth parses the bearer + project-scope headers off r. The
// caller is responsible for comparing Token against the configured
// operator token (via [authorize]).
func extractAuth(r *http.Request) authResult {
	out := authResult{}
	if values := r.Header.Values("Authorization"); len(values) == 1 {
		fields := strings.Fields(values[0])
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			out.Token = strings.TrimSpace(fields[1])
		}
	}
	out.ProjectAccessToken = strings.TrimSpace(r.Header.Get("Project-Access-Token"))
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Midnight-Allow-Mutations")), "true") {
		out.AllowMutations = true
	}
	return out
}

// authorize compares the request's bearer against the configured
// operator token. An empty configured token disables auth entirely
// (the developer / test default — matches optionalBearerTokenMiddleware
// in internal/api/router.go).
func authorize(configured, received string) bool {
	configured = strings.TrimSpace(configured)
	if platformauth.ValidateOperatorRecoveryToken(configured) != nil {
		return false
	}
	if configured == "" {
		return true
	}
	if received == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(received)) == 1
}
