package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/previews"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type PreviewEnvironmentResource struct {
	ID            string    `json:"id"`
	PullRequest   int       `json:"pull_request"`
	EnvironmentID string    `json:"environment_id"`
	DeploymentID  string    `json:"deployment_id,omitempty"`
	State         string    `json:"state"`
	URL           string    `json:"url,omitempty"`
	HeadSHA       string    `json:"head_sha"`
	FromFork      bool      `json:"from_fork"`
	ExpiresAt     time.Time `json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type PreviewEnvironmentList struct {
	ServiceID  string                       `json:"service_id"`
	ProjectID  string                       `json:"project_id"`
	Items      []PreviewEnvironmentResource `json:"items"`
	Capability previews.LifecycleCapability `json:"capability"`
}

func servicePreviewEnvironmentsHandler(
	store ServiceStore,
	lifecycle GitHubPreviewLifecycle,
) func(http.ResponseWriter, *http.Request, orailtypes.ID) {
	return func(w http.ResponseWriter, r *http.Request, serviceID orailtypes.ID) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if r.URL.RawQuery != "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "preview environment capability does not accept query parameters", nil)
			return
		}
		service, err := store.GetService(r.Context(), serviceID)
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "preview capability lookup failed", nil)
			return
		}
		items := []PreviewEnvironmentResource{}
		if lifecycle != nil {
			values, err := lifecycle.ListServicePreviews(r.Context(), string(service.ID))
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "preview environment lookup failed", nil)
				return
			}
			items = make([]PreviewEnvironmentResource, 0, len(values))
			for _, value := range values {
				items = append(items, PreviewEnvironmentResource{
					ID: value.ID, PullRequest: value.PullRequest, EnvironmentID: value.EnvironmentID,
					DeploymentID: value.DeploymentID, State: value.State, URL: value.URL,
					HeadSHA: value.HeadSHA, FromFork: value.FromFork,
					ExpiresAt: value.ExpiresAt, UpdatedAt: value.UpdatedAt,
				})
			}
		}
		WriteJSON(w, http.StatusOK, PreviewEnvironmentList{
			ServiceID:  string(service.ID),
			ProjectID:  string(service.ProjectID),
			Items:      items,
			Capability: previews.LifecycleCapabilityFor(lifecycle != nil),
		})
	}
}
