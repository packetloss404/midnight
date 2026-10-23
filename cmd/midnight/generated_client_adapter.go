package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/midnight/midnight/pkg/midnightctl"
)

// generatedCLIReadClient is the incremental migration seam from cliAPIClient
// to the generated OpenAPI client. The generated client owns route and query
// construction; the existing CLI transport still owns endpoint validation,
// redirect refusal, deadlines, response caps, and User-Agent behavior.
type generatedCLIReadClient struct {
	client *midnightctl.Client
}

func newGeneratedCLIReadClient(cfg *apiCommandConfig) (*generatedCLIReadClient, error) {
	legacy, err := newCLIAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(legacy.baseURL, "/") + "/api/v1alpha"
	client, err := midnightctl.NewClient(
		baseURL,
		midnightctl.WithHTTPClient(generatedCLIBoundedDoer{client: legacy.client}),
		midnightctl.WithBearerToken(legacy.token),
		midnightctl.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			request.Header.Set("Accept", "application/json")
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}
	return &generatedCLIReadClient{client: client}, nil
}

// generatedCLIBoundedDoer adapts executeCLIRequest to the generated client's
// http.Doer contract. executeCLIRequest consumes and closes the network body;
// this returns a bounded in-memory body for the generated response layer.
type generatedCLIBoundedDoer struct {
	client *http.Client
}

func (d generatedCLIBoundedDoer) Do(request *http.Request) (*http.Response, error) {
	response, body, err := executeCLIRequest(d.client, request, maxCLIResponseBytes)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

func executeGeneratedCLIRead(
	call func() (*http.Response, error),
	method string,
	path string,
	out any,
) ([]byte, error) {
	response, err := call()
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("API response is unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return body, apiStatusError(method, path, response.Status, body)
	}
	if out != nil {
		if len(body) == 0 {
			return body, errors.New("empty response body")
		}
		if err := json.Unmarshal(body, out); err != nil {
			return body, &cliResponseDecodeFailure{Err: err}
		}
	}
	return body, nil
}

func generatedProjectsForTable(values []midnightctl.Project) []cliProject {
	projects := make([]cliProject, 0, len(values))
	for _, value := range values {
		projects = append(projects, cliProject{
			ID:          value.Id,
			WorkspaceID: value.WorkspaceId,
			Slug:        value.Slug,
			DisplayName: value.DisplayName,
			CreatedAt:   value.CreatedAt,
			UpdatedAt:   value.UpdatedAt,
		})
	}
	return projects
}

func generatedServicesForTable(values []midnightctl.Service) []cliService {
	services := make([]cliService, 0, len(values))
	for _, value := range values {
		services = append(services, cliService{
			ID:          value.Id,
			ProjectID:   value.ProjectId,
			Slug:        value.Slug,
			DisplayName: value.DisplayName,
			CreatedAt:   value.CreatedAt,
			UpdatedAt:   value.UpdatedAt,
		})
	}
	return services
}

func generatedString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
