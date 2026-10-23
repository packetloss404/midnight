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
	"github.com/midnight/midnight/internal/router"
)

const resourceCollectionDomains = "/domains"

type CreateDomainRequest struct {
	ProjectID     string            `json:"project_id"`
	EnvironmentID string            `json:"environment_id"`
	ServiceID     string            `json:"service_id"`
	Kind          router.DomainKind `json:"kind"`
	Hostname      string            `json:"hostname,omitempty"`
	PathPrefix    string            `json:"path_prefix,omitempty"`
	TargetPort    int               `json:"target_port,omitempty"`
	Public        bool              `json:"public"`
	TLS           bool              `json:"tls"`
}

type DomainChallengeView struct {
	RecordName  string                   `json:"record_name"`
	RecordValue string                   `json:"record_value"`
	State       router.DNSChallengeState `json:"state"`
	Attempts    int64                    `json:"attempts"`
	LastError   string                   `json:"last_error,omitempty"`
	CheckedAt   *time.Time               `json:"checked_at,omitempty"`
}

type DomainCertificateView struct {
	Issuer    string                       `json:"issuer"`
	State     router.CertificateOrderState `json:"state"`
	ExpiresAt *time.Time                   `json:"expires_at,omitempty"`
	LastError string                       `json:"last_error,omitempty"`
}

type DomainRouteView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PathPrefix string `json:"path_prefix"`
	TargetPort int    `json:"target_port"`
	Public     bool   `json:"public"`
}

// DomainView never equates desired intent or file publication with externally
// reachable traffic. Reachable is true only after durable Traefik
// acknowledgement and an ingress probe advanced the group to ready.
type DomainView struct {
	ID            string                 `json:"id"`
	ProjectID     string                 `json:"project_id"`
	EnvironmentID string                 `json:"environment_id"`
	ServiceID     string                 `json:"service_id"`
	Kind          router.DomainKind      `json:"kind"`
	Hostname      string                 `json:"hostname"`
	TLSDesired    bool                   `json:"tls_desired"`
	TLSState      router.DomainTLSState  `json:"tls_state"`
	ApplyState    router.RouteApplyState `json:"apply_state"`
	Reachable     bool                   `json:"reachable"`
	ArtifactHash  string                 `json:"artifact_hash,omitempty"`
	ErrorCode     string                 `json:"error_code,omitempty"`
	ErrorDetail   string                 `json:"error_detail,omitempty"`
	Routes        []DomainRouteView      `json:"routes"`
	Challenge     *DomainChallengeView   `json:"dns_challenge,omitempty"`
	Certificate   *DomainCertificateView `json:"certificate,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

func domainsHandler(store router.DomainWorkflowStore, appsDomain string, idem idempotency.Store, emitter *auditEmitter, scopeResolver MutationScopeResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "public domain workflow is not configured on this server", nil)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionDomains {
			switch r.Method {
			case http.MethodGet:
				listDomains(w, r, store, scopeResolver)
			case http.MethodPost:
				createDomain(w, r, store, strings.TrimSpace(appsDomain), idem, emitter, scopeResolver)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}
		id := strings.TrimPrefix(path, resourceCollectionDomains+"/")
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "domain not found", nil)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			deleteDomain(w, r, store, idem, emitter, scopeResolver, id)
		default:
			w.Header().Set("Allow", "DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"domain_id": id})
		}
	}
}

func listDomains(w http.ResponseWriter, r *http.Request, store router.DomainWorkflowStore, scopeResolver MutationScopeResolver) {
	scope, ok := resolveDomainScope(w, r, scopeResolver, router.RouteScope{
		ProjectID:     r.URL.Query().Get("project_id"),
		EnvironmentID: r.URL.Query().Get("environment_id"),
		ServiceID:     r.URL.Query().Get("service_id"),
	})
	if !ok {
		return
	}
	states, err := store.ListDomainRouteStates(r.Context(), scope)
	if err != nil {
		writeDomainStoreError(w, r, "domain list failed", err)
		return
	}
	out := make([]DomainView, 0, len(states))
	for _, state := range states {
		out = append(out, domainView(state))
	}
	WriteJSON(w, http.StatusOK, out)
}

func createDomain(w http.ResponseWriter, r *http.Request, store router.DomainWorkflowStore, appsDomain string, idem idempotency.Store, emitter *auditEmitter, scopeResolver MutationScopeResolver) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid domain request body")
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
	var req CreateDomainRequest
	if !decodeStrictJSONRequestBytes(w, r, body, &req, "invalid domain request body") {
		return
	}
	scope, ok := resolveDomainScope(w, r, scopeResolver, router.RouteScope{
		ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID, ServiceID: req.ServiceID,
	})
	if !ok {
		return
	}
	if req.Kind == "" {
		if strings.TrimSpace(req.Hostname) == "" {
			req.Kind = router.DomainKindGenerated
		} else {
			req.Kind = router.DomainKindCustom
		}
	}
	if req.Kind == router.DomainKindCustom && strings.TrimSpace(req.Hostname) == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "hostname is required for a custom domain", map[string]any{"field": "hostname"})
		return
	}
	if req.Kind == router.DomainKindGenerated && appsDomain == "" {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "generated domains require the server apps domain to be configured", map[string]any{"setting": "MIDNIGHT_APPS_DOMAIN"}) // not-a-failure-code
		return
	}
	targetPort := req.TargetPort
	if targetPort == 0 {
		targetPort = 8080
	}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
		Scope: scope, Kind: req.Kind, Hostname: req.Hostname, AppsDomain: appsDomain,
		PathPrefix: req.PathPrefix, TargetPort: targetPort, Public: req.Public,
		TLSDesired: req.TLS,
	})
	if err != nil {
		writeDomainStoreError(w, r, "invalid domain intent", err)
		return
	}
	domainValue, err = store.PutDomain(r.Context(), domainValue)
	if err != nil {
		writeDomainStoreError(w, r, "domain create failed", err)
		return
	}
	if err := store.EnsureDomainLifecycle(r.Context(), domainValue); err != nil {
		writeDomainStoreError(w, r, "domain lifecycle initialization failed", err)
		return
	}
	routeValue, err = store.PutHTTPRoute(r.Context(), routeValue)
	if err != nil {
		writeDomainStoreError(w, r, "domain route create failed", err)
		return
	}
	// Bind the route to the service exactly as the tenant coordinator does.
	// Deploy intake resolves a public route intent against an existing
	// service binding, so a domain created without one can never be attached
	// to a deployment: the route stays blocked on a missing backend and every
	// deploy carrying a route intent is refused as an unmatched selector.
	binder, ok := store.(router.ServiceDomainBinder)
	if !ok {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "this installation cannot bind a domain to its service, so the route would never receive traffic", map[string]any{"resource": "domain"})
		return
	}
	if err := binder.BindServiceDomain(r.Context(), domainValue.ID, routeValue.ID, scope, time.Time{}); err != nil {
		writeDomainStoreError(w, r, "domain route binding failed", err)
		return
	}
	states, err := store.ListDomainRouteStates(r.Context(), scope)
	if err != nil {
		writeDomainStoreError(w, r, "domain lookup failed", err)
		return
	}
	var created DomainView
	for _, state := range states {
		if state.Domain.ID == domainValue.ID {
			created = domainView(state)
			break
		}
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "domain", ResourceID: domainValue.ID, ProjectID: domainValue.ProjectID,
			Action: "domain.create",
			After:  map[string]any{"hostname": domainValue.NormalizedHostname, "kind": domainValue.Kind, "public": req.Public, "tls_desired": req.TLS},
		})
	}
	w.Header().Set("Location", v1alphaPrefix+resourceCollectionDomains+"/"+domainValue.ID)
	WriteJSON(w, http.StatusCreated, created)
}

func deleteDomain(w http.ResponseWriter, r *http.Request, store router.DomainWorkflowStore, idem idempotency.Store, emitter *auditEmitter, scopeResolver MutationScopeResolver, id string) {
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, nil)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	before, err := store.GetDomain(r.Context(), id)
	if err != nil {
		writeDomainStoreError(w, r, "domain lookup failed", err)
		return
	}
	if _, ok := resolveDomainScope(w, r, scopeResolver, before.RouteScope); !ok {
		return
	}
	if err := store.DeleteDomain(r.Context(), id); err != nil {
		writeDomainStoreError(w, r, "domain delete failed", err)
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "domain", ResourceID: before.ID, ProjectID: before.ProjectID,
			Action: "domain.delete", Before: map[string]any{"hostname": before.NormalizedHostname},
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func resolveDomainScope(w http.ResponseWriter, r *http.Request, resolver MutationScopeResolver, scope router.RouteScope) (router.RouteScope, bool) {
	if strings.TrimSpace(scope.ProjectID) == "" || strings.TrimSpace(scope.EnvironmentID) == "" || strings.TrimSpace(scope.ServiceID) == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id, environment_id, and service_id are required", nil)
		return router.RouteScope{}, false
	}
	if resolver == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "domain scope validation is not configured", nil)
		return router.RouteScope{}, false
	}
	resolved, err := resolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID,
		RequireProject: true, RequireEnvironment: true, RequireService: true,
	})
	if err != nil {
		writeMutationScopeError(w, r, err)
		return router.RouteScope{}, false
	}
	return router.RouteScope{ProjectID: resolved.ProjectID, EnvironmentID: resolved.EnvironmentID, ServiceID: resolved.ServiceID}, true
}

func writeDomainStoreError(w http.ResponseWriter, r *http.Request, message string, err error) {
	switch {
	case errors.Is(err, router.ErrDurableRouteNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "domain not found", nil)
	case errors.Is(err, router.ErrDurableHostnameOwned):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "hostname is already owned", nil)
	case errors.Is(err, router.ErrDurableRouteInvalid), errors.Is(err, router.ErrDurableRouteScopeMismatch):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, message, nil)
	}
}

func domainView(state router.DomainRouteState) DomainView {
	out := DomainView{
		ID: state.Domain.ID, ProjectID: state.Domain.ProjectID,
		EnvironmentID: state.Domain.EnvironmentID, ServiceID: state.Domain.ServiceID,
		Kind: state.Domain.Kind, Hostname: state.Domain.NormalizedHostname,
		TLSDesired: state.Domain.TLSDesired, TLSState: state.Domain.TLSState,
		ApplyState: state.ApplyState, Reachable: state.ApplyState == router.RouteApplyReady,
		ArtifactHash: state.ArtifactHash, ErrorCode: state.ErrorCode, ErrorDetail: state.ErrorDetail,
		Routes:    make([]DomainRouteView, 0, len(state.Routes)),
		CreatedAt: state.Domain.CreatedAt, UpdatedAt: state.Domain.UpdatedAt,
	}
	for _, routeValue := range state.Routes {
		out.Routes = append(out.Routes, DomainRouteView{
			ID: routeValue.ID, Name: routeValue.Name, PathPrefix: routeValue.PathPrefix,
			TargetPort: routeValue.TargetPort, Public: routeValue.Public,
		})
	}
	if state.Challenge != nil {
		out.Challenge = &DomainChallengeView{
			RecordName: state.Challenge.RecordName, RecordValue: state.Challenge.RecordValue,
			State: state.Challenge.State, Attempts: state.Challenge.Attempts,
			LastError: state.Challenge.LastError, CheckedAt: state.Challenge.CheckedAt,
		}
	}
	if state.Certificate != nil {
		out.Certificate = &DomainCertificateView{
			Issuer: state.Certificate.Issuer, State: state.Certificate.State,
			ExpiresAt: state.Certificate.ExpiresAt, LastError: state.Certificate.LastError,
		}
	}
	return out
}
