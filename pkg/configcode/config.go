// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
// Package configcode defines the shared config-as-code document and diff model.
package configcode

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

const APIVersion = "midnight/v1alpha"

var slugPattern = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

var ErrInvalidDocument = errors.New("invalid config document")

type Document struct {
	APIVersion string    `json:"api_version"`
	Projects   []Project `json:"projects,omitempty"`
	Services   []Service `json:"services,omitempty"`
}

// SyncDocument is the environment-scoped config-as-code envelope. Changes are
// deliberately modeled only far enough for portable CLI validation; the API
// owns strict kind-specific decoding and routes every accepted item through
// the durable environment changeset authority.
type SyncDocument struct {
	APIVersion       string       `json:"api_version"`
	EnvironmentID    string       `json:"environment_id"`
	ExpectedRevision *int64       `json:"expected_revision"`
	Note             string       `json:"note,omitempty"`
	Changes          []SyncChange `json:"changes"`
}

type SyncChange struct {
	Kind            string `json:"kind"`
	Operation       string `json:"operation"`
	ServiceID       string `json:"service_id"`
	TargetID        string `json:"target_id,omitempty"`
	Name            string `json:"name"`
	ExpectedVersion *int64 `json:"expected_version,omitempty"`
}

func ValidateSync(doc SyncDocument) error {
	if doc.APIVersion != APIVersion {
		return fmt.Errorf("%w: api_version must be %q", ErrInvalidDocument, APIVersion)
	}
	if doc.EnvironmentID == "" {
		return fmt.Errorf("%w: environment_id is required", ErrInvalidDocument)
	}
	if doc.ExpectedRevision == nil || *doc.ExpectedRevision < 0 {
		return fmt.Errorf("%w: expected_revision must be non-negative", ErrInvalidDocument)
	}
	if len(doc.Changes) == 0 || len(doc.Changes) > 100 {
		return fmt.Errorf("%w: changes must contain 1-100 items", ErrInvalidDocument)
	}
	for i, change := range doc.Changes {
		if change.ServiceID == "" {
			return fmt.Errorf("%w: changes[%d].service_id is required", ErrInvalidDocument, i)
		}
		switch change.Kind {
		case "variable", "runtime", "networking", "storage":
		default:
			return fmt.Errorf("%w: changes[%d].kind is invalid", ErrInvalidDocument, i)
		}
		switch change.Operation {
		case "add", "edit", "remove":
		default:
			return fmt.Errorf("%w: changes[%d].operation is invalid", ErrInvalidDocument, i)
		}
	}
	return nil
}

type Project struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

type Service struct {
	ProjectSlug string `json:"project_slug"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

type State struct {
	Projects []Project `json:"projects,omitempty"`
	Services []Service `json:"services,omitempty"`
}

type Operation string

const (
	OperationCreate Operation = "create"
	OperationUpdate Operation = "update"
	OperationNoop   Operation = "noop"
)

type Action struct {
	Operation    Operation `json:"operation"`
	ResourceType string    `json:"resource_type"`
	Slug         string    `json:"slug"`
	ProjectSlug  string    `json:"project_slug,omitempty"`
	Reason       string    `json:"reason"`
}

type Summary struct {
	Create int `json:"create"`
	Update int `json:"update"`
	Noop   int `json:"noop"`
	Total  int `json:"total"`
}

type Plan struct {
	APIVersion string   `json:"api_version"`
	Summary    Summary  `json:"summary"`
	Actions    []Action `json:"actions"`
}

func Validate(doc Document) error {
	if doc.APIVersion != APIVersion {
		return fmt.Errorf("%w: api_version must be %q", ErrInvalidDocument, APIVersion)
	}
	projects := make(map[string]struct{}, len(doc.Projects))
	for i, project := range doc.Projects {
		if !validSlug(project.Slug) {
			return fmt.Errorf("%w: projects[%d].slug is invalid", ErrInvalidDocument, i)
		}
		if _, exists := projects[project.Slug]; exists {
			return fmt.Errorf("%w: duplicate project slug %q", ErrInvalidDocument, project.Slug)
		}
		projects[project.Slug] = struct{}{}
	}
	services := make(map[string]struct{}, len(doc.Services))
	for i, service := range doc.Services {
		if !validSlug(service.ProjectSlug) {
			return fmt.Errorf("%w: services[%d].project_slug is invalid", ErrInvalidDocument, i)
		}
		if _, exists := projects[service.ProjectSlug]; !exists {
			return fmt.Errorf("%w: services[%d].project_slug %q has no project", ErrInvalidDocument, i, service.ProjectSlug)
		}
		if !validSlug(service.Slug) {
			return fmt.Errorf("%w: services[%d].slug is invalid", ErrInvalidDocument, i)
		}
		key := service.ProjectSlug + "/" + service.Slug
		if _, exists := services[key]; exists {
			return fmt.Errorf("%w: duplicate service %q", ErrInvalidDocument, key)
		}
		services[key] = struct{}{}
	}
	return nil
}

func Diff(desired Document, observed State) (Plan, error) {
	if err := Validate(desired); err != nil {
		return Plan{}, err
	}
	projects := make(map[string]Project, len(observed.Projects))
	for _, project := range observed.Projects {
		projects[project.Slug] = project
	}
	services := make(map[string]Service, len(observed.Services))
	for _, service := range observed.Services {
		services[service.ProjectSlug+"/"+service.Slug] = service
	}

	actions := make([]Action, 0, len(desired.Projects)+len(desired.Services))
	for _, project := range desired.Projects {
		observedProject, exists := projects[project.Slug]
		actions = append(actions, classify("project", project.Slug, "", project.DisplayName, observedProject.DisplayName, exists))
	}
	for _, service := range desired.Services {
		observedService, exists := services[service.ProjectSlug+"/"+service.Slug]
		actions = append(actions, classify("service", service.Slug, service.ProjectSlug, service.DisplayName, observedService.DisplayName, exists))
	}
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].ResourceType != actions[j].ResourceType {
			return actions[i].ResourceType < actions[j].ResourceType
		}
		if actions[i].ProjectSlug != actions[j].ProjectSlug {
			return actions[i].ProjectSlug < actions[j].ProjectSlug
		}
		return actions[i].Slug < actions[j].Slug
	})
	plan := Plan{APIVersion: desired.APIVersion, Actions: actions}
	for _, action := range actions {
		switch action.Operation {
		case OperationCreate:
			plan.Summary.Create++
		case OperationUpdate:
			plan.Summary.Update++
		case OperationNoop:
			plan.Summary.Noop++
		}
	}
	plan.Summary.Total = len(actions)
	return plan, nil
}

func classify(resourceType, slug, projectSlug, desiredName, observedName string, exists bool) Action {
	action := Action{ResourceType: resourceType, Slug: slug, ProjectSlug: projectSlug}
	if !exists {
		action.Operation = OperationCreate
		action.Reason = "missing"
		return action
	}
	if desiredName != "" && desiredName != observedName {
		action.Operation = OperationUpdate
		action.Reason = "display_name differs"
		return action
	}
	action.Operation = OperationNoop
	action.Reason = "matches"
	return action
}

func validSlug(slug string) bool {
	return slugPattern.MatchString(slug)
}
