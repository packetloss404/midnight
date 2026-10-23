// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/tcpproxy"
)

const resourceCollectionTCPProxies = "/tcp-proxies"

type CreateTCPProxyRequest struct {
	ProjectID             string   `json:"project_id"`
	EnvironmentID         string   `json:"environment_id"`
	ServiceID             string   `json:"service_id"`
	Hostname              string   `json:"hostname,omitempty"`
	RequestedExternalPort int      `json:"external_port,omitempty"`
	BackendPort           int      `json:"backend_port"`
	AllowedSourceCIDRs    []string `json:"allowed_source_cidrs"`
}

// TCPProxyView keeps local execution evidence separate from external
// reachability. Midnight does not mutate TCP DNS or operator firewall/NAT
// rules, so those claims remain false even when the listener is active.
type TCPProxyView struct {
	ID                         string              `json:"id"`
	ProjectID                  string              `json:"project_id"`
	EnvironmentID              string              `json:"environment_id"`
	ServiceID                  string              `json:"service_id"`
	Hostname                   string              `json:"hostname"`
	ExternalPort               int                 `json:"external_port"`
	PublicAddress              string              `json:"public_address"`
	BackendPort                int                 `json:"backend_port"`
	AllowedSourceCIDRs         []string            `json:"allowed_source_cidrs"`
	DesiredEnabled             bool                `json:"desired_enabled"`
	ApplyState                 tcpproxy.ApplyState `json:"apply_state"`
	ListenerBound              bool                `json:"listener_bound"`
	BackendReachable           bool                `json:"backend_reachable"`
	DNSProvisioned             bool                `json:"dns_provisioned"`
	PublicReachabilityVerified bool                `json:"public_reachability_verified"`
	ControllerID               string              `json:"controller_id,omitempty"`
	ErrorCode                  string              `json:"error_code,omitempty"`
	ErrorDetail                string              `json:"error_detail,omitempty"`
	ObservedAt                 *time.Time          `json:"observed_at,omitempty"`
	CreatedAt                  time.Time           `json:"created_at"`
	UpdatedAt                  time.Time           `json:"updated_at"`
}

func tcpProxiesHandler(
	store tcpproxy.Store,
	backendResolver tcpproxy.BackendResolver,
	baseDomain string,
	idem idempotency.Store,
	emitter *auditEmitter,
	scopeResolver MutationScopeResolver,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "TCP proxy workflow is not configured on this server", nil)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionTCPProxies {
			switch r.Method {
			case http.MethodGet:
				listTCPProxies(w, r, store, scopeResolver)
			case http.MethodPost:
				createTCPProxy(w, r, store, backendResolver, strings.TrimSpace(baseDomain), idem, emitter, scopeResolver)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}
		id := strings.TrimPrefix(path, resourceCollectionTCPProxies+"/")
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "TCP proxy endpoint not found", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			getTCPProxy(w, r, store, scopeResolver, id)
		case http.MethodDelete:
			disableTCPProxy(w, r, store, idem, emitter, scopeResolver, id)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func listTCPProxies(w http.ResponseWriter, r *http.Request, store tcpproxy.Store, resolver MutationScopeResolver) {
	scope, ok := resolveTCPProxyScope(w, r, resolver, tcpproxy.Scope{
		ProjectID: r.URL.Query().Get("project_id"), EnvironmentID: r.URL.Query().Get("environment_id"),
		ServiceID: r.URL.Query().Get("service_id"),
	})
	if !ok {
		return
	}
	values, err := store.ListEndpoints(r.Context(), scope)
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy list failed", err)
		return
	}
	out := make([]TCPProxyView, 0, len(values))
	for _, value := range values {
		out = append(out, tcpProxyView(value))
	}
	WriteJSON(w, http.StatusOK, out)
}

func createTCPProxy(
	w http.ResponseWriter,
	r *http.Request,
	store tcpproxy.Store,
	backendResolver tcpproxy.BackendResolver,
	baseDomain string,
	idem idempotency.Store,
	emitter *auditEmitter,
	resolver MutationScopeResolver,
) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid TCP proxy request body")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	var request CreateTCPProxyRequest
	if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid TCP proxy request body") {
		return
	}
	scope, ok := resolveTCPProxyScope(w, r, resolver, tcpproxy.Scope{
		ProjectID: request.ProjectID, EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID,
	})
	if !ok {
		return
	}
	if backendResolver == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "TCP proxy active-backend resolution is not configured", nil)
		return
	}
	backends, err := backendResolver.ResolveBackends(r.Context(), scope)
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy active backend resolution failed", err)
		return
	}
	value, err := store.CreateEndpoint(r.Context(), tcpproxy.CreateEndpointInput{
		Scope: scope, BaseDomain: baseDomain, RequestedHost: request.Hostname,
		RequestedExternalPort: request.RequestedExternalPort,
		BackendPort:           request.BackendPort,
		AllowedCIDRs:          append([]string{}, request.AllowedSourceCIDRs...),
	})
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy create failed", err)
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "tcp_proxy", ResourceID: value.ID, ProjectID: value.ProjectID,
			Action: "tcp_proxy.create",
			After: map[string]any{
				"hostname": value.Host, "external_port": value.ExternalPort,
				"backend_port": value.BackendPort, "active_backend_count": len(backends),
				"allowed_source_cidr_count": len(value.AllowedCIDRs),
			},
		})
	}
	w.Header().Set("Location", v1alphaPrefix+resourceCollectionTCPProxies+"/"+value.ID)
	WriteJSON(w, http.StatusCreated, tcpProxyView(value))
}

func getTCPProxy(w http.ResponseWriter, r *http.Request, store tcpproxy.Store, resolver MutationScopeResolver, id string) {
	value, err := store.GetEndpoint(r.Context(), id)
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy lookup failed", err)
		return
	}
	if _, ok := resolveTCPProxyScope(w, r, resolver, value.Scope); !ok {
		return
	}
	WriteJSON(w, http.StatusOK, tcpProxyView(value))
}

func disableTCPProxy(
	w http.ResponseWriter,
	r *http.Request,
	store tcpproxy.Store,
	idem idempotency.Store,
	emitter *auditEmitter,
	resolver MutationScopeResolver,
	id string,
) {
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, nil)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	before, err := store.GetEndpoint(r.Context(), id)
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy lookup failed", err)
		return
	}
	if _, ok := resolveTCPProxyScope(w, r, resolver, before.Scope); !ok {
		return
	}
	value, err := store.DisableEndpoint(r.Context(), id)
	if err != nil {
		writeTCPProxyError(w, r, "TCP proxy withdrawal failed", err)
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "tcp_proxy", ResourceID: before.ID, ProjectID: before.ProjectID,
			Action: "tcp_proxy.withdraw",
			Before: map[string]any{"hostname": before.Host, "external_port": before.ExternalPort},
		})
	}
	WriteJSON(w, http.StatusAccepted, tcpProxyView(value))
}

func resolveTCPProxyScope(
	w http.ResponseWriter,
	r *http.Request,
	resolver MutationScopeResolver,
	scope tcpproxy.Scope,
) (tcpproxy.Scope, bool) {
	if err := scope.Validate(); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id, environment_id, and service_id are required", nil)
		return tcpproxy.Scope{}, false
	}
	if resolver == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "TCP proxy scope validation is not configured", nil)
		return tcpproxy.Scope{}, false
	}
	resolved, err := resolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID,
		RequireProject: true, RequireEnvironment: true, RequireService: true,
	})
	if err != nil {
		writeMutationScopeError(w, r, err)
		return tcpproxy.Scope{}, false
	}
	return tcpproxy.Scope{
		ProjectID: resolved.ProjectID, EnvironmentID: resolved.EnvironmentID, ServiceID: resolved.ServiceID,
	}, true
}

func writeTCPProxyError(w http.ResponseWriter, r *http.Request, message string, err error) {
	switch {
	case errors.Is(err, tcpproxy.ErrEndpointNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "TCP proxy endpoint not found", nil)
	case errors.Is(err, tcpproxy.ErrEndpointConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "TCP proxy host, port, or service is already allocated", nil)
	case errors.Is(err, tcpproxy.ErrBackendUnavailable):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "no ready active deployment backend is available for this service environment", nil)
	case errors.Is(err, tcpproxy.ErrEndpointInvalid), errors.Is(err, tcpproxy.ErrInvalidSourceIPPolicy), errors.Is(err, tcpproxy.ErrNoExternalPortsAvailable):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, message, nil)
	}
}

func tcpProxyView(value tcpproxy.Endpoint) TCPProxyView {
	return TCPProxyView{
		ID: value.ID, ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, ServiceID: value.ServiceID,
		Hostname: value.Host, ExternalPort: value.ExternalPort, PublicAddress: value.PublicAddress(),
		BackendPort:        value.BackendPort,
		AllowedSourceCIDRs: append([]string{}, value.AllowedCIDRs...),
		DesiredEnabled:     value.DesiredEnabled, ApplyState: value.ApplyState,
		ListenerBound: value.ListenerBound, BackendReachable: value.BackendReachable,
		// External DNS/firewall/NAT are explicitly operator-owned in this slice.
		DNSProvisioned: false, PublicReachabilityVerified: false,
		ControllerID: value.ControllerID, ErrorCode: value.LastErrorCode, ErrorDetail: value.LastErrorDetail,
		ObservedAt: value.ObservedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}
