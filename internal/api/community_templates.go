// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type communityDiscoverRequest struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

type communityForkCreateRequest struct {
	ProjectID              string `json:"project_id"`
	Source                 string `json:"source"`
	Ref                    string `json:"ref"`
	ExpectedManifestSHA256 string `json:"expected_manifest_sha256"`
}

type communityForkUpdateRequest struct {
	ProjectID string                      `json:"project_id"`
	Draft     templates.CommunityManifest `json:"draft"`
}

type communityForkChangesetRequest struct {
	ProjectID           string            `json:"project_id"`
	EnvironmentID       string            `json:"environment_id"`
	ExpectedRevision    *int64            `json:"expected_revision"`
	ExpectedForkVersion int64             `json:"expected_fork_version"`
	ServiceIDs          map[string]string `json:"service_ids"`
}

func communityTemplatesHandler(w http.ResponseWriter, r *http.Request, rest string, deps templateDeps) {
	if rest == "community/discover" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		discoverCommunityTemplate(w, r, deps)
		return
	}
	if rest == "community/forks" {
		switch r.Method {
		case http.MethodGet:
			listCommunityTemplateForks(w, r, deps)
		case http.MethodPost:
			createCommunityTemplateFork(w, r, deps)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
		return
	}
	if strings.HasPrefix(rest, "community/forks/") {
		forkPath := strings.TrimSpace(strings.TrimPrefix(rest, "community/forks/"))
		id, suffix, hasSuffix := strings.Cut(forkPath, "/")
		if id == "" || (hasSuffix && suffix != "changeset") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template fork not found", nil)
			return
		}
		if suffix == "changeset" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
			stageCommunityTemplateForkChangeset(w, r, id, deps)
			return
		}
		switch r.Method {
		case http.MethodGet:
			getCommunityTemplateFork(w, r, id, deps)
		case http.MethodPatch:
			updateCommunityTemplateFork(w, r, id, deps)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
		return
	}
	writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template route not found", nil)
}

func stageCommunityTemplateForkChangeset(w http.ResponseWriter, r *http.Request, id string, deps templateDeps) {
	if deps.communityForks == nil || deps.serviceStore == nil || deps.serviceConfigs == nil || deps.environmentChangesets == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "community template changeset compilation is unavailable", map[string]any{"capability": "community-template-changeset"})
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	var request communityForkChangesetRequest
	raw, ok := readStrictEnvironmentChangesetBody(w, r, &request)
	if !ok {
		return
	}
	defer clear(raw)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	if request.EnvironmentID == "" || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || request.ExpectedForkVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment_id, expected_revision, and expected_fork_version are required", nil)
		return
	}
	if !requireMatchingExpectedVersion(w, r, *request.ExpectedRevision) {
		return
	}
	key, keyOK := requireTenantVariableIdempotencyKey(w, r)
	if !keyOK {
		return
	}
	projectID, ok := resolveCommunityProject(w, r, deps, request.ProjectID, true)
	if !ok {
		return
	}
	fork, err := deps.communityForks.GetFork(r.Context(), projectID, id)
	switch {
	case errors.Is(err, templates.ErrCommunityForkNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template fork not found", map[string]any{"id": id})
		return
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template fork could not be read", nil)
		return
	case fork.ResourceVersion != request.ExpectedForkVersion:
		writeRequestError(w, r, http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "community template fork changed since it was reviewed", map[string]any{"expected_fork_version": request.ExpectedForkVersion, "current_fork_version": fork.ResourceVersion})
		return
	case !fork.Valid:
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "community template validation failed", map[string]any{"findings": fork.Findings})
		return
	}
	changes, err := compileCommunityTemplateChanges(r, deps, fork, request.EnvironmentID, request.ServiceIDs)
	if errors.Is(err, errEnvironmentChangesetInvalid) || errors.Is(err, templates.ErrTemplateInvalid) {
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template could not be compiled into a changeset", nil)
		return
	}
	result, err := deps.environmentChangesets.PutEnvironmentChangeset(r.Context(), EnvironmentChangesetMutationRequest{
		EnvironmentChangesetReadRequest: EnvironmentChangesetReadRequest{Principal: principal, Session: session, EnvironmentID: request.EnvironmentID},
		ExpectedRevision:                *request.ExpectedRevision,
		Note:                            "Community template " + fork.Draft.Template.Name + " desired state; deploy pinned images separately after review",
		Changes:                         changes,
		Key:                             key,
		RawBody:                         raw,
		Method:                          r.Method,
		Path:                            r.URL.Path,
		RequestID:                       middleware.FromContext(r.Context()),
		SourceIP:                        r.RemoteAddr,
		UserAgent:                       r.UserAgent(),
	})
	if writeEnvironmentChangesetError(w, r, err) {
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func compileCommunityTemplateChanges(r *http.Request, deps templateDeps, fork templates.CommunityFork, environmentID string, serviceIDs map[string]string) ([]EnvironmentChangesetInput, error) {
	templateServices := fork.Draft.Template.Services
	if len(serviceIDs) != len(templateServices) {
		return nil, fmt.Errorf("%w: service_ids must map every template service exactly once", errEnvironmentChangesetInvalid)
	}
	usedIDs := map[string]struct{}{}
	changes := make([]EnvironmentChangesetInput, 0)
	for _, templateService := range templateServices {
		serviceID := strings.TrimSpace(serviceIDs[templateService.Slug])
		if serviceID == "" {
			return nil, fmt.Errorf("%w: service_ids.%s is required", errEnvironmentChangesetInvalid, templateService.Slug)
		}
		if _, duplicate := usedIDs[serviceID]; duplicate {
			return nil, fmt.Errorf("%w: each template service needs a distinct service_id", errEnvironmentChangesetInvalid)
		}
		usedIDs[serviceID] = struct{}{}
		service, err := deps.serviceStore.GetService(r.Context(), orailtypes.ID(serviceID))
		if err != nil || string(service.ProjectID) != fork.ProjectID {
			return nil, fmt.Errorf("%w: service_ids.%s does not identify a service in this project", errEnvironmentChangesetInvalid, templateService.Slug)
		}

		config, err := deps.serviceConfigs.Get(r.Context(), fork.ProjectID, environmentID, serviceID)
		operation := "edit"
		expected := config.Version
		if errors.Is(err, serviceconfig.ErrNotFound) {
			config = serviceconfig.Default(fork.ProjectID, serviceID, environmentID)
			operation, expected = "add", 0
		} else if err != nil {
			return nil, err
		}
		for _, port := range templateService.Ports {
			protocol := strings.ToLower(strings.TrimSpace(port.Protocol))
			if protocol == "" || protocol == "tcp" {
				config.TargetPort = port.Port
				break
			}
		}
		runtimeSpec := environmentRuntimeSpecFromConfig(config)
		runtimeInput := EnvironmentChangesetInput{Kind: "runtime", Operation: operation, ServiceID: serviceID, Name: "service configuration", Runtime: &runtimeSpec}
		if operation == "edit" {
			runtimeInput.TargetID = serviceID
			runtimeInput.ExpectedVersion = &expected
		}
		changes = append(changes, runtimeInput)

		resolved, err := templates.ResolveEnvironmentValues(templateService, nil, service.Slug+".midnight.internal")
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(resolved))
		for name := range resolved {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := resolved[name]
			scope := secrets.ScopePlain
			if declared, exists := templateService.Env[name]; exists && declared.Secret {
				scope = secrets.ScopeSealed
			}
			changes = append(changes, EnvironmentChangesetInput{Kind: "variable", Operation: "add", ServiceID: serviceID, Name: name, Scope: &scope, Value: &value})
		}
		if templateService.Volume != nil {
			storage := EnvironmentStorageSpec{SizeBytes: templateService.Volume.SizeBytes, MountPath: templateService.Volume.MountPath}
			changes = append(changes, EnvironmentChangesetInput{Kind: "storage", Operation: "add", ServiceID: serviceID, Name: templateService.Volume.Name, Storage: &storage})
		}
	}
	if len(changes) == 0 || len(changes) > environmentChangesetMaxItems {
		return nil, fmt.Errorf("%w: compiled template has %d changes; the supported range is 1-%d", errEnvironmentChangesetInvalid, len(changes), environmentChangesetMaxItems)
	}
	return changes, nil
}

func discoverCommunityTemplate(w http.ResponseWriter, r *http.Request, deps templateDeps) {
	if deps.communityDiscoverer == nil {
		writeCommunityCapabilityUnavailable(w, r)
		return
	}
	body, ok := readBoundedJSONRequestBody(w, r, "invalid community template discovery body")
	if !ok {
		return
	}
	var request communityDiscoverRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid community template discovery body") {
		return
	}
	discovery, err := deps.communityDiscoverer.Discover(r.Context(), request.Source, request.Ref)
	if err != nil {
		writeCommunityDiscoveryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, discovery)
}

func createCommunityTemplateFork(w http.ResponseWriter, r *http.Request, deps templateDeps) {
	if deps.communityDiscoverer == nil || deps.communityForks == nil {
		writeCommunityCapabilityUnavailable(w, r)
		return
	}
	body, ok := readBoundedJSONRequestBody(w, r, "invalid community template fork body")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, deps.idem, body)
	if !ok {
		return
	}
	capture := newIdempotencyResponseCapture(w)
	w = capture
	defer capture.Complete(r, deps.idem, reservation.Reference())

	var request communityForkCreateRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid community template fork body") {
		return
	}
	projectID, ok := resolveCommunityProject(w, r, deps, request.ProjectID, true)
	if !ok {
		return
	}
	if !isLowerSHA256(request.ExpectedManifestSHA256) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_manifest_sha256 must be a lowercase SHA-256 digest", map[string]any{"field": "expected_manifest_sha256"})
		return
	}
	discovery, err := deps.communityDiscoverer.Discover(r.Context(), request.Source, request.Ref)
	if err != nil {
		writeCommunityDiscoveryError(w, r, err)
		return
	}
	if discovery.Provenance.ManifestSHA256 != request.ExpectedManifestSHA256 {
		writeRequestError(w, r, http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "the immutable template manifest does not match the discovery digest", map[string]any{
			"expected_manifest_sha256": request.ExpectedManifestSHA256,
			"actual_manifest_sha256":   discovery.Provenance.ManifestSHA256,
		})
		return
	}
	if !discovery.Valid {
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "community template validation failed", map[string]any{"findings": discovery.Findings})
		return
	}
	fork := templates.CommunityFork{
		ID: mintCommunityForkID(projectID, discovery.Provenance), ProjectID: projectID,
		TrustClass: "local", UpstreamManifest: discovery.Manifest, Draft: discovery.Manifest,
		Provenance: discovery.Provenance, Readme: discovery.Readme,
		Findings: discovery.Findings, Valid: discovery.Valid,
	}
	fork, err = deps.communityForks.CreateFork(r.Context(), fork)
	if errors.Is(err, templates.ErrCommunityForkConflict) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "this immutable community template is already forked into the project", map[string]any{"project_id": projectID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template fork could not be persisted", nil)
		return
	}
	deps.emitter.emit(r.Context(), audit.Row{
		ResourceKind: "template", ResourceID: fork.ID, ProjectID: projectID, Action: "community_template.fork",
		After: map[string]any{"trust_class": fork.TrustClass, "source": fork.Provenance.Source, "ref": fork.Provenance.Ref, "manifest_sha256": fork.Provenance.ManifestSHA256, "valid": fork.Valid},
	})
	w.Header().Set("ETag", strongVersionETag(fork.ResourceVersion))
	w.Header().Set("Location", v1alphaPrefix+"/templates/community/forks/"+fork.ID+"?project_id="+projectID)
	WriteJSON(w, http.StatusCreated, fork)
}

func listCommunityTemplateForks(w http.ResponseWriter, r *http.Request, deps templateDeps) {
	if deps.communityForks == nil {
		writeCommunityCapabilityUnavailable(w, r)
		return
	}
	projectID, ok := resolveCommunityProject(w, r, deps, r.URL.Query().Get("project_id"), false)
	if !ok {
		return
	}
	forks, err := deps.communityForks.ListForks(r.Context(), projectID)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template forks could not be listed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"forks": forks})
}

func getCommunityTemplateFork(w http.ResponseWriter, r *http.Request, id string, deps templateDeps) {
	if deps.communityForks == nil {
		writeCommunityCapabilityUnavailable(w, r)
		return
	}
	projectID, ok := resolveCommunityProject(w, r, deps, r.URL.Query().Get("project_id"), false)
	if !ok {
		return
	}
	fork, err := deps.communityForks.GetFork(r.Context(), projectID, id)
	if errors.Is(err, templates.ErrCommunityForkNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template fork not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template fork could not be read", nil)
		return
	}
	w.Header().Set("ETag", strongVersionETag(fork.ResourceVersion))
	WriteJSON(w, http.StatusOK, fork)
}

func updateCommunityTemplateFork(w http.ResponseWriter, r *http.Request, id string, deps templateDeps) {
	if deps.communityForks == nil {
		writeCommunityCapabilityUnavailable(w, r)
		return
	}
	expectedVersion, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	body, ok := readBoundedJSONRequestBody(w, r, "invalid community template fork update body")
	if !ok {
		return
	}
	var request communityForkUpdateRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid community template fork update body") {
		return
	}
	projectID, ok := resolveCommunityProject(w, r, deps, request.ProjectID, true)
	if !ok {
		return
	}
	findings := templates.ValidateCommunityManifest(request.Draft)
	fork, err := deps.communityForks.UpdateFork(r.Context(), projectID, id, expectedVersion, request.Draft, findings)
	switch {
	case errors.Is(err, templates.ErrCommunityForkNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template fork not found", map[string]any{"id": id})
		return
	case errors.Is(err, templates.ErrCommunityForkConflict):
		writeIfMatchFailed(w, r, "If-Match does not identify the current community template fork version", map[string]any{"id": id})
		return
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template fork could not be updated", nil)
		return
	}
	deps.emitter.emit(r.Context(), audit.Row{
		ResourceKind: "template", ResourceID: fork.ID, ProjectID: projectID, Action: "community_template.update",
		After: map[string]any{"trust_class": fork.TrustClass, "resource_version": fork.ResourceVersion, "valid": fork.Valid, "finding_count": len(fork.Findings)},
	})
	w.Header().Set("ETag", strongVersionETag(fork.ResourceVersion))
	WriteJSON(w, http.StatusOK, fork)
}

func resolveCommunityProject(w http.ResponseWriter, r *http.Request, deps templateDeps, projectID string, write bool) (string, bool) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return "", false
	}
	if deps.scopeResolver == nil {
		return projectID, authorizeCommunityProject(w, r, deps.identityAccess, projectID, write)
	}
	resolved, err := deps.scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{ProjectID: projectID, RequireProject: true})
	if err != nil {
		writeMutationScopeError(w, r, err)
		return "", false
	}
	return resolved.ProjectID, authorizeCommunityProject(w, r, deps.identityAccess, resolved.ProjectID, write)
}

func authorizeCommunityProject(w http.ResponseWriter, r *http.Request, access identity.AccessStore, projectID string, write bool) bool {
	principal, ok := productPrincipalFromContext(r.Context())
	if !ok || principal.Kind != productPrincipalHuman {
		return true
	}
	if access == nil || principal.Human.UserID == "" {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "community template project authorization is unavailable", nil)
		return false
	}
	capability := identity.CapabilityProjectRead
	if write {
		capability = identity.CapabilityProjectManage
	}
	_, _, err := identity.Authorize(
		r.Context(), access, principal.Human.UserID,
		identity.AccessTarget{ProjectID: projectID}, capability, false,
	)
	if err == nil {
		return true
	}
	if tenantReadDenied(err) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "community template project scope not found", nil)
		return false
	}
	writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "community template project authorization is unavailable", nil)
	return false
}

func writeCommunityDiscoveryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, templates.ErrCommunitySourceInvalid), errors.Is(err, templates.ErrCommunityManifest):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "community template source is invalid", nil)
	case errors.Is(err, templates.ErrCommunityFetchFailed):
		writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "the immutable GitHub template assets could not be fetched", nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "community template discovery failed", nil)
	}
}

func writeCommunityCapabilityUnavailable(w http.ResponseWriter, r *http.Request) {
	writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "community template discovery and project forks require the durable Postgres-backed API", map[string]any{
		"capability": "community-template-forks", "state": "unavailable",
	})
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mintCommunityForkID(projectID string, provenance templates.CommunityProvenance) string {
	digest := sha256.Sum256([]byte(projectID + "\x00" + provenance.Source + "\x00" + provenance.Ref + "\x00" + provenance.ManifestSHA256))
	return "tpl_" + hex.EncodeToString(digest[:16])
}
