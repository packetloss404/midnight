package tenancy

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	LabelWorkspace   = "midnight.workspace"
	LabelProject     = "midnight.project"
	LabelEnvironment = "midnight.environment"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,127}$`)

// Scope identifies the public tenant boundary a resource belongs to. Workspace
// is the authorization boundary; project and environment scope runtime/build
// resources inside that workspace.
type Scope struct {
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
}

func (s Scope) Validate() error {
	if err := validateID("workspace_id", s.WorkspaceID); err != nil {
		return err
	}
	if err := validateID("project_id", s.ProjectID); err != nil {
		return err
	}
	return validateID("environment_id", s.EnvironmentID)
}

func (s Scope) Labels() map[string]string {
	return map[string]string{
		LabelWorkspace:   s.WorkspaceID,
		LabelProject:     s.ProjectID,
		LabelEnvironment: s.EnvironmentID,
	}
}

func (s Scope) Key() string {
	return s.WorkspaceID + "\x00" + s.ProjectID + "\x00" + s.EnvironmentID
}

// ValidateLabels fails closed when a resource is missing tenant labels or when
// any supplied label crosses the expected tenant boundary.
func (s Scope) ValidateLabels(labels map[string]string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	for key, want := range s.Labels() {
		got := strings.TrimSpace(labels[key])
		if got == "" {
			return fmt.Errorf("tenant label %q is required", key)
		}
		if got != want {
			return fmt.Errorf("tenant label %q = %q, want %q", key, got, want)
		}
	}
	return nil
}

func SameScope(a, b Scope) bool {
	return a == b && a.Validate() == nil
}

func validateID(name, value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("%s is required and must not contain surrounding whitespace", name)
	}
	if !safeID.MatchString(value) {
		return fmt.Errorf("%s %q contains unsupported characters", name, value)
	}
	return nil
}
