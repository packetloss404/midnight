// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/agent"
)

// Resource is the MCP resource descriptor as advertised over
// resources/list. The shape mirrors the spec; we keep our own type
// rather than re-using the MCP SDK so the hand-rolled wire stays
// self-contained.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// Content is one slice of a resources/read response.
type Content struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// ResourceProvider is the seam the MCP server consumes to enumerate
// and read addressable Midnight resources. Implementations may return
// ErrResourceNotFound to signal a clean 404; any other error is
// propagated as a JSON-RPC internal error (-32603).
type ResourceProvider interface {
	List(ctx context.Context) ([]Resource, error)
	Read(ctx context.Context, uri string) (Content, error)
}

// ErrResourceNotFound is returned by Read when the URI is unknown.
var ErrResourceNotFound = errors.New("mcp: resource not found")

// DefaultResources is the v1 in-memory implementation. It enumerates:
//
//   - midnight://projects                                            (list of projects)
//   - midnight://projects/{id}/services                              (services in a project)
//   - midnight://services/{id}/deployments/{deploy_id}/logs          (log slice)
//   - midnight://services/{id}/variables                             (metadata only)
//
// The full set of (project, service, deployment) tuples isn't
// pre-enumerated in List — only a handful of "templated" entries are
// returned so MCP clients can discover the shape. Reads against any
// matching URI resolve against the ServicesAPI / OperationStore.
type DefaultResources struct {
	Services   agent.ServicesAPI
	Operations agent.OperationStore
}

// NewDefaultResources constructs the v1 provider. Either field may be
// nil — the matching URI patterns will return ErrResourceNotFound.
func NewDefaultResources(services agent.ServicesAPI, ops agent.OperationStore) *DefaultResources {
	return &DefaultResources{Services: services, Operations: ops}
}

// List returns the canonical resource catalog. The list is intentionally
// short — clients are expected to follow URIs into the parameterised
// shapes (e.g. resolve a service id from list_services, then read the
// service's variables resource).
func (d *DefaultResources) List(ctx context.Context) ([]Resource, error) {
	return []Resource{
		{
			URI:         "midnight://projects",
			Name:        "projects",
			Description: "All Midnight projects visible to this session.",
			MIMEType:    "application/json",
		},
		{
			URI:         "midnight://projects/{project_id}/services",
			Name:        "services",
			Description: "Services that belong to a Midnight project. Substitute {project_id}.",
			MIMEType:    "application/json",
		},
		{
			URI:         "midnight://services/{service_id}/deployments/{deployment_id}/logs",
			Name:        "deployment_logs",
			Description: "Recent log lines for a deployment. Substitute both ids.",
			MIMEType:    "application/json",
		},
		{
			URI:         "midnight://services/{service_id}/variables",
			Name:        "service_variables",
			Description: "Variable metadata for a service (values are redacted).",
			MIMEType:    "application/json",
		},
	}, nil
}

// Read resolves a midnight:// URI against the underlying APIs. The
// return shape is a JSON text body — clients are expected to parse it.
func (d *DefaultResources) Read(ctx context.Context, uri string) (Content, error) {
	rest, ok := strings.CutPrefix(uri, "midnight://")
	if !ok {
		return Content{}, ErrResourceNotFound
	}
	switch {
	case rest == "projects":
		if d.Services == nil {
			return Content{}, ErrResourceNotFound
		}
		projects, err := d.Services.ListProjects(ctx)
		if err != nil {
			return Content{}, err
		}
		return jsonContent(uri, map[string]any{"projects": projects, "count": len(projects)})
	case strings.HasPrefix(rest, "projects/"):
		// projects/{id}/services
		tail := strings.TrimPrefix(rest, "projects/")
		parts := strings.Split(tail, "/")
		if len(parts) != 2 || parts[1] != "services" {
			return Content{}, ErrResourceNotFound
		}
		if d.Services == nil {
			return Content{}, ErrResourceNotFound
		}
		services, err := d.Services.ListServices(ctx, parts[0])
		if err != nil {
			return Content{}, err
		}
		return jsonContent(uri, map[string]any{"services": services, "count": len(services)})
	case strings.HasPrefix(rest, "services/"):
		// services/{id}/variables
		// services/{id}/deployments/{deploy_id}/logs
		tail := strings.TrimPrefix(rest, "services/")
		parts := strings.Split(tail, "/")
		switch {
		case len(parts) == 2 && parts[1] == "variables":
			if d.Services == nil {
				return Content{}, ErrResourceNotFound
			}
			svc, err := d.Services.GetService(ctx, parts[0])
			if err != nil {
				return Content{}, err
			}
			// Strip values — only metadata flows over MCP.
			meta := make(map[string]any, len(svc.Variables))
			for k := range svc.Variables {
				meta[k] = "<redacted>"
			}
			return jsonContent(uri, map[string]any{"service_id": svc.ID, "variables": meta})
		case len(parts) == 4 && parts[1] == "deployments" && parts[3] == "logs":
			if d.Operations == nil {
				return Content{}, ErrResourceNotFound
			}
			lines, err := d.Operations.GetLogs(ctx, parts[2], 200, "")
			if err != nil {
				return Content{}, err
			}
			return jsonContent(uri, map[string]any{
				"service_id":    parts[0],
				"deployment_id": parts[2],
				"lines":         lines,
				"count":         len(lines),
			})
		}
	}
	return Content{}, fmt.Errorf("%w: %s", ErrResourceNotFound, uri)
}

func jsonContent(uri string, payload any) (Content, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Content{}, err
	}
	return Content{URI: uri, MIMEType: "application/json", Text: string(b)}, nil
}
