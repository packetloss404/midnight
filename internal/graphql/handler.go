// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/idempotency"
)

// HandlerOptions configures the GraphQL HTTP handler.
type HandlerOptions struct {
	// Executor runs validated queries.
	Executor *Executor
	// BearerToken is the optional shared-secret token. When empty,
	// auth is treated as in development mode and every request passes.
	// Matches the v1alpha REST behavior (see optionalBearerTokenMiddleware
	// in internal/api/router.go).
	BearerToken string
	// ProductionAuthRequired fails closed for graph requests when the
	// router is configured for production but no token is set.
	ProductionAuthRequired bool
}

// NewHandler returns the HTTP handler mounted at /graphql/v2. It accepts
// JSON POST per the GraphQL-over-HTTP spec.
//
// Authentication accepts either the installation recovery bearer or an
// installation-administrator human session already verified by the outer API
// router. Midnight does not advertise Railway's Project-Access-Token header
// until it can mint and enforce a genuinely project-scoped credential.
func NewHandler(opts HandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			writeCORSHeaders(w, r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost && !(r.Method == http.MethodGet && r.URL.Query().Get("query") != "") {
			w.Header().Set("Allow", "POST")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed; POST a GraphQL request to /graphql/v2")
			return
		}

		if !authorize(r, opts) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}

		req, err := readRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid graphql request body: "+err.Error())
			return
		}

		actor := idempotency.ActorRecoveryOperator
		if api.IsInstanceAdminRequest(r.Context()) {
			actor = idempotency.ActorFromContext(r.Context())
		} else if strings.TrimSpace(opts.BearerToken) == "" {
			actor = idempotency.ActorLocalDeveloper
		}
		ctx := idempotency.WithActor(r.Context(), actor)
		ctx = context.WithValue(ctx, requestTransportContextKey{}, requestTransport{
			Method: r.Method, RequestID: middleware.FromContext(r.Context()),
			SourceIP: r.RemoteAddr, UserAgent: r.UserAgent(),
		})
		resp := opts.Executor.Execute(ctx, req)
		writeJSON(w, http.StatusOK, resp)
	})
}

type requestTransportContextKey struct{}

type requestTransport struct {
	Method    string
	RequestID string
	SourceIP  string
	UserAgent string
}

func readRequest(r *http.Request) (Request, error) {
	var req Request
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		req.Query = q.Get("query")
		req.OperationName = q.Get("operationName")
		if vraw := q.Get("variables"); vraw != "" {
			if err := json.Unmarshal([]byte(vraw), &req.Variables); err != nil {
				return Request{}, err
			}
		}
		return req, nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // 4 MiB ceiling
	if err != nil {
		return Request{}, err
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" || strings.Contains(ct, "application/json") || strings.Contains(ct, "application/graphql+json") {
		if err := json.Unmarshal(body, &req); err != nil {
			return Request{}, err
		}
		return req, nil
	}
	if strings.Contains(ct, "application/graphql") {
		req.Query = string(body)
		return req, nil
	}
	// Unknown content-type: try JSON as a fallback.
	if err := json.Unmarshal(body, &req); err != nil {
		return Request{}, err
	}
	return req, nil
}

func authorize(r *http.Request, opts HandlerOptions) bool {
	if api.IsInstanceAdminRequest(r.Context()) {
		return true
	}
	expected := strings.TrimSpace(opts.BearerToken)
	if auth.ValidateOperatorRecoveryToken(expected) != nil {
		return false
	}
	if expected == "" {
		return !opts.ProductionAuthRequired
	}
	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(bearer), []byte(expected)) == 1
}

func extractBearer(authorization string) string {
	authorization = strings.TrimSpace(authorization)
	if authorization == "" {
		return ""
	}
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{Errors: []ResponseError{{Message: msg}}})
}

func writeCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
}

// Ensure context is referenced (executor.go uses it) to keep the import
// honest when this file is read in isolation.
var _ = context.Background
