// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// GitHubAppAccessor is the boundary the github-link handlers consume
// to enumerate App installations + repos. Wired to a real github.AppClient
// in production; tests inject a stub.
type GitHubAppAccessor interface {
	ListInstallations(ctx context.Context) ([]github.Installation, error)
	ListReposForInstallation(ctx context.Context, installationID int64) ([]github.Repo, error)
}

// githubLinkBody is the JSON body POST /services/{id}/github accepts.
type githubLinkBody struct {
	InstallationID int64    `json:"installation_id"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	WatchPaths     []string `json:"watch_paths,omitempty"`
	RootDirectory  string   `json:"root_directory,omitempty"`
	ConfigPath     string   `json:"config_path,omitempty"`
	CloneURL       string   `json:"clone_url,omitempty"`
	WaitForCI      bool     `json:"wait_for_ci,omitempty"`
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// servicesGitHubHandler dispatches /services/{id}/github and
// /github/installations[/{id}/repos] sub-routes.
func servicesGitHubHandler(serviceStore ServiceStore, app GitHubAppAccessor, emitter *auditEmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		switch {
		case path == "/github/installations":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
			listInstallations(w, r, app)
			return
		case strings.HasPrefix(path, "/github/installations/"):
			rest := strings.TrimPrefix(path, "/github/installations/")
			id, suffix, ok := strings.Cut(rest, "/")
			if !ok || suffix != "repos" || id == "" {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "github installation route not found", nil)
				return
			}
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
			listInstallationRepos(w, r, app, id)
			return
		}
		// /services/{id}/github
		serviceID, ok := parseServiceGitHubPath(path)
		if !ok {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "github route not found", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			getServiceGitHubLink(w, r, serviceStore, serviceID)
		case http.MethodPut, http.MethodPost:
			putServiceGitHubLink(w, r, serviceStore, emitter, serviceID)
		case http.MethodDelete:
			deleteServiceGitHubLink(w, r, serviceStore, emitter, serviceID)
		default:
			w.Header().Set("Allow", "GET, PUT, POST, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"service_id": serviceID})
		}
	}
}

func parseServiceGitHubPath(path string) (orailtypes.ID, bool) {
	rest := strings.TrimPrefix(path, "/services/")
	if rest == path {
		return "", false
	}
	id, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "github" || id == "" {
		return "", false
	}
	return orailtypes.ID(id), true
}

func getServiceGitHubLink(w http.ResponseWriter, r *http.Request, store ServiceStore, serviceID orailtypes.ID) {
	svc, err := store.GetService(r.Context(), serviceID)
	if errors.Is(err, ErrResourceNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service lookup failed", map[string]any{"service_id": serviceID})
		return
	}
	link, present := readGitHubLink(svc)
	if !present {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service has no github link", map[string]any{"service_id": serviceID})
		return
	}
	WriteJSON(w, http.StatusOK, link.PublicView())
}

func putServiceGitHubLink(w http.ResponseWriter, r *http.Request, store ServiceStore, emitter *auditEmitter, serviceID orailtypes.ID) {
	specStore, ok := store.(ServiceSpecStore)
	if !ok {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "service store does not support spec mutations", map[string]any{"service_id": serviceID})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", map[string]any{"service_id": serviceID})
		return
	}
	var req githubLinkBody
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid github link request body", map[string]any{"reason": "json"})
			return
		}
	}
	link := github.ServiceLink{
		InstallationID: req.InstallationID,
		Repo:           req.Repo,
		Branch:         req.Branch,
		WatchPaths:     req.WatchPaths,
		RootDirectory:  req.RootDirectory,
		ConfigPath:     req.ConfigPath,
		CloneURL:       req.CloneURL,
		WaitForCI:      req.WaitForCI,
		RequiredChecks: req.RequiredChecks,
	}.Normalize()
	if err := link.Validate(); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), map[string]any{"service_id": serviceID})
		return
	}
	svc, err := specStore.UpdateServiceSpec(r.Context(), serviceID, func(spec map[string]any) (map[string]any, error) {
		if spec == nil {
			spec = map[string]any{}
		}
		spec["github"] = encodeLink(link)
		return spec, nil
	})
	if errors.Is(err, ErrResourceNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service spec update failed", map[string]any{"service_id": serviceID})
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "service",
			ResourceID:   string(svc.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "service.github.link",
			After: map[string]any{
				"repo":            link.Repo,
				"branch":          link.Branch,
				"installation_id": link.InstallationID,
				"watch_paths":     link.WatchPaths,
				"root_directory":  link.RootDirectory,
				"config_path":     link.ConfigPath,
				"wait_for_ci":     link.WaitForCI,
			},
		})
	}
	WriteJSON(w, http.StatusOK, link.PublicView())
}

func deleteServiceGitHubLink(w http.ResponseWriter, r *http.Request, store ServiceStore, emitter *auditEmitter, serviceID orailtypes.ID) {
	specStore, ok := store.(ServiceSpecStore)
	if !ok {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "service store does not support spec mutations", map[string]any{"service_id": serviceID})
		return
	}
	had := false
	svc, err := specStore.UpdateServiceSpec(r.Context(), serviceID, func(spec map[string]any) (map[string]any, error) {
		if _, present := spec["github"]; present {
			had = true
			delete(spec, "github")
		}
		return spec, nil
	})
	if errors.Is(err, ErrResourceNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service spec update failed", map[string]any{"service_id": serviceID})
		return
	}
	if !had {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service has no github link", map[string]any{"service_id": serviceID})
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "service",
			ResourceID:   string(svc.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "service.github.unlink",
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// encodeLink converts a ServiceLink into the JSON-shape map persisted
// inside service.spec.github. This is the source of truth for the
// JSONB key path lookup index defined in 0011_service_github_index.sql.
func encodeLink(l github.ServiceLink) map[string]any {
	out := map[string]any{
		"installation_id": l.InstallationID,
		"repo":            l.Repo,
		"branch":          l.Branch,
	}
	if l.RepositoryID > 0 {
		out["repository_id"] = l.RepositoryID
	}
	if len(l.WatchPaths) > 0 {
		out["watch_paths"] = l.WatchPaths
	}
	if l.RootDirectory != "" {
		out["root_directory"] = l.RootDirectory
	}
	if l.ConfigPath != "" {
		out["config_path"] = l.ConfigPath
	}
	if l.CloneURL != "" {
		out["clone_url"] = l.CloneURL
	}
	if l.WaitForCI {
		out["wait_for_ci"] = true
	}
	if len(l.RequiredChecks) > 0 {
		out["required_checks"] = l.RequiredChecks
	}
	return out
}

// readGitHubLink decodes a service.spec.github map back into a
// ServiceLink. Returns false when the key is absent or malformed.
func readGitHubLink(svc Service) (github.ServiceLink, bool) {
	if svc.Spec == nil {
		return github.ServiceLink{}, false
	}
	raw, ok := svc.Spec["github"]
	if !ok {
		return github.ServiceLink{}, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return github.ServiceLink{}, false
	}
	l := github.ServiceLink{}
	switch v := m["installation_id"].(type) {
	case float64:
		l.InstallationID = int64(v)
	case int64:
		l.InstallationID = v
	case int:
		l.InstallationID = int64(v)
	}
	switch v := m["repository_id"].(type) {
	case float64:
		l.RepositoryID = int64(v)
	case int64:
		l.RepositoryID = v
	case int:
		l.RepositoryID = int64(v)
	}
	l.Repo, _ = m["repo"].(string)
	l.Branch, _ = m["branch"].(string)
	l.RootDirectory, _ = m["root_directory"].(string)
	l.ConfigPath, _ = m["config_path"].(string)
	if wp, ok := m["watch_paths"].([]any); ok {
		for _, e := range wp {
			if s, ok := e.(string); ok {
				l.WatchPaths = append(l.WatchPaths, s)
			}
		}
	} else if wp, ok := m["watch_paths"].([]string); ok {
		l.WatchPaths = append(l.WatchPaths, wp...)
	}
	if rc, ok := m["required_checks"].([]any); ok {
		for _, e := range rc {
			if s, ok := e.(string); ok {
				l.RequiredChecks = append(l.RequiredChecks, s)
			}
		}
	} else if rc, ok := m["required_checks"].([]string); ok {
		l.RequiredChecks = append(l.RequiredChecks, rc...)
	}
	l.CloneURL, _ = m["clone_url"].(string)
	l.WaitForCI, _ = m["wait_for_ci"].(bool)
	if l.Repo == "" || l.Branch == "" {
		return github.ServiceLink{}, false
	}
	return l, true
}

func listInstallations(w http.ResponseWriter, r *http.Request, app GitHubAppAccessor) {
	if app == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "github app accessor not configured", nil)
		return
	}
	insts, err := app.ListInstallations(r.Context())
	if err != nil {
		writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "github installations fetch failed", map[string]any{"error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, insts)
}

func listInstallationRepos(w http.ResponseWriter, r *http.Request, app GitHubAppAccessor, idStr string) {
	if app == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "github app accessor not configured", nil)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "installation id invalid", map[string]any{"installation_id": idStr})
		return
	}
	repos, err := app.ListReposForInstallation(r.Context(), id)
	if err != nil {
		writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "github installation repos fetch failed", map[string]any{"installation_id": id, "error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, repos)
}
