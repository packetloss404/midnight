package identity

import "strings"

// WorkspaceRole is a workspace-wide grant. The first three values mirror
// Railway's current Admin/Member/Deployer model. Viewer is a Midnight
// read-only extension retained for safe migration from the original schema.
type WorkspaceRole string

const (
	WorkspaceRoleAdmin    WorkspaceRole = "admin"
	WorkspaceRoleMember   WorkspaceRole = "member"
	WorkspaceRoleDeployer WorkspaceRole = "deployer"
	WorkspaceRoleViewer   WorkspaceRole = "viewer"
)

// ProjectRole is a direct project grant for users who do not otherwise have
// sufficient workspace-wide access.
type ProjectRole string

const (
	ProjectRoleOwner  ProjectRole = "owner"
	ProjectRoleEditor ProjectRole = "editor"
	ProjectRoleViewer ProjectRole = "viewer"
)

// Capability is a stable authorization decision input. HTTP verbs and UI
// controls map to capabilities; role names never appear in request handlers.
type Capability string

const (
	CapabilityWorkspaceRead          Capability = "workspace.read"
	CapabilityWorkspaceMembersManage Capability = "workspace.members.manage"
	CapabilityWorkspaceAuditRead     Capability = "workspace.audit.read"
	CapabilityProjectCreate          Capability = "project.create"
	CapabilityProjectRead            Capability = "project.read"
	CapabilityProjectManage          Capability = "project.manage"
	CapabilityProjectDelete          Capability = "project.delete"
	CapabilityProjectMembersManage   Capability = "project.members.manage"
	CapabilityServiceRead            Capability = "service.read"
	CapabilityServiceWrite           Capability = "service.write"
	CapabilityServiceDelete          Capability = "service.delete"
	CapabilityEnvironmentWrite       Capability = "environment.write"
	CapabilityDeploymentTriggerCLI   Capability = "deployment.trigger.cli"
	CapabilityDeploymentTriggerGit   Capability = "deployment.trigger.git"
	CapabilityLogsRead               Capability = "logs.read"
	CapabilityMetricsRead            Capability = "metrics.read"
	CapabilityVariablesRead          Capability = "variables.read"
	CapabilityVariablesWrite         Capability = "variables.write"
	CapabilityDomainsRead            Capability = "domains.read"
	CapabilityDomainsWrite           Capability = "domains.write"
	CapabilityVolumeWrite            Capability = "volume.write"
	CapabilityVolumeDelete           Capability = "volume.delete"
	CapabilityEnvironmentRestrict    Capability = "environment.restrict"
)

var allCapabilities = []Capability{
	CapabilityWorkspaceRead,
	CapabilityWorkspaceMembersManage,
	CapabilityWorkspaceAuditRead,
	CapabilityProjectCreate,
	CapabilityProjectRead,
	CapabilityProjectManage,
	CapabilityProjectDelete,
	CapabilityProjectMembersManage,
	CapabilityServiceRead,
	CapabilityServiceWrite,
	CapabilityServiceDelete,
	CapabilityEnvironmentWrite,
	CapabilityDeploymentTriggerCLI,
	CapabilityDeploymentTriggerGit,
	CapabilityLogsRead,
	CapabilityMetricsRead,
	CapabilityVariablesRead,
	CapabilityVariablesWrite,
	CapabilityDomainsRead,
	CapabilityDomainsWrite,
	CapabilityVolumeWrite,
	CapabilityVolumeDelete,
	CapabilityEnvironmentRestrict,
}

// Capabilities returns the declared capability vocabulary in stable order.
// Callers receive a copy so transport code cannot mutate the policy registry.
func Capabilities() []Capability {
	return append([]Capability(nil), allCapabilities...)
}

// WorkspaceRolesForCapability returns the canonical workspace roles that
// grant capability. Durable query adapters use this to keep SQL predicates in
// lockstep with EvaluateAccess rather than copying role policy by hand.
func WorkspaceRolesForCapability(capability Capability) []WorkspaceRole {
	ordered := []WorkspaceRole{
		WorkspaceRoleAdmin,
		WorkspaceRoleMember,
		WorkspaceRoleDeployer,
		WorkspaceRoleViewer,
	}
	out := make([]WorkspaceRole, 0, len(ordered))
	for _, role := range ordered {
		if roleHasCapability(workspaceRoleCapabilities[role], capability) {
			out = append(out, role)
		}
	}
	return out
}

// ProjectRolesForCapability returns the canonical direct-project roles that
// grant capability.
func ProjectRolesForCapability(capability Capability) []ProjectRole {
	ordered := []ProjectRole{ProjectRoleOwner, ProjectRoleEditor, ProjectRoleViewer}
	out := make([]ProjectRole, 0, len(ordered))
	for _, role := range ordered {
		if roleHasCapability(projectRoleCapabilities[role], capability) {
			out = append(out, role)
		}
	}
	return out
}

var workspaceRoleCapabilities = map[WorkspaceRole]map[Capability]struct{}{
	WorkspaceRoleAdmin: capabilitySet(allCapabilities...),
	WorkspaceRoleMember: capabilitySet(
		CapabilityWorkspaceRead,
		CapabilityProjectCreate,
		CapabilityProjectRead,
		CapabilityProjectManage,
		CapabilityServiceRead,
		CapabilityServiceWrite,
		CapabilityEnvironmentWrite,
		CapabilityDeploymentTriggerCLI,
		CapabilityDeploymentTriggerGit,
		CapabilityLogsRead,
		CapabilityMetricsRead,
		CapabilityVariablesRead,
		CapabilityVariablesWrite,
		CapabilityDomainsRead,
		CapabilityDomainsWrite,
		CapabilityVolumeWrite,
	),
	WorkspaceRoleDeployer: capabilitySet(
		CapabilityWorkspaceRead,
		CapabilityProjectRead,
		CapabilityDeploymentTriggerGit,
	),
	WorkspaceRoleViewer: capabilitySet(
		CapabilityWorkspaceRead,
		CapabilityProjectRead,
		CapabilityServiceRead,
		CapabilityMetricsRead,
		CapabilityDomainsRead,
	),
}

var projectRoleCapabilities = map[ProjectRole]map[Capability]struct{}{
	ProjectRoleOwner: capabilitySet(
		CapabilityProjectRead,
		CapabilityProjectManage,
		CapabilityProjectDelete,
		CapabilityProjectMembersManage,
		CapabilityServiceRead,
		CapabilityServiceWrite,
		CapabilityServiceDelete,
		CapabilityEnvironmentWrite,
		CapabilityDeploymentTriggerCLI,
		CapabilityDeploymentTriggerGit,
		CapabilityLogsRead,
		CapabilityMetricsRead,
		CapabilityVariablesRead,
		CapabilityVariablesWrite,
		CapabilityDomainsRead,
		CapabilityDomainsWrite,
		CapabilityVolumeWrite,
		CapabilityVolumeDelete,
	),
	ProjectRoleEditor: capabilitySet(
		CapabilityProjectRead,
		CapabilityProjectManage,
		CapabilityProjectMembersManage,
		CapabilityServiceRead,
		CapabilityServiceWrite,
		CapabilityEnvironmentWrite,
		CapabilityDeploymentTriggerCLI,
		CapabilityDeploymentTriggerGit,
		CapabilityLogsRead,
		CapabilityMetricsRead,
		CapabilityVariablesRead,
		CapabilityVariablesWrite,
		CapabilityDomainsRead,
		CapabilityDomainsWrite,
		CapabilityVolumeWrite,
	),
	ProjectRoleViewer: capabilitySet(
		CapabilityProjectRead,
		CapabilityServiceRead,
		CapabilityLogsRead,
		CapabilityMetricsRead,
		CapabilityDomainsRead,
	),
}

func capabilitySet(capabilities ...Capability) map[Capability]struct{} {
	out := make(map[Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		out[capability] = struct{}{}
	}
	return out
}

// AccessRequest is the complete, transport-independent authorization input.
// Workspace and project grants are additive; a direct viewer grant never
// downgrades an existing workspace member. Restricted environments are a hard
// overlay and deny every human capability except admin access and the
// separately authenticated Git-trigger capability.
type AccessRequest struct {
	WorkspaceRole         WorkspaceRole
	ProjectRole           ProjectRole
	Capability            Capability
	EnvironmentRestricted bool
	GitAutomation         bool
}

// AccessDecision is safe to audit. It intentionally contains no credential or
// resource secrets.
type AccessDecision struct {
	Allowed    bool       `json:"allowed"`
	Reason     string     `json:"reason"`
	Capability Capability `json:"capability"`
}

// EvaluateAccess applies the canonical role matrix and restriction overlay.
func EvaluateAccess(request AccessRequest) AccessDecision {
	decision := AccessDecision{
		Allowed:    false,
		Reason:     "no-matching-grant",
		Capability: request.Capability,
	}
	if !ValidCapability(request.Capability) {
		decision.Reason = "invalid-capability"
		return decision
	}
	if request.EnvironmentRestricted && request.WorkspaceRole != WorkspaceRoleAdmin {
		if request.GitAutomation && request.Capability == CapabilityDeploymentTriggerGit {
			decision.Allowed = true
			decision.Reason = "restricted-environment-git-trigger"
			return decision
		}
		decision.Reason = "environment-restricted"
		return decision
	}
	if roleHasCapability(workspaceRoleCapabilities[request.WorkspaceRole], request.Capability) {
		decision.Allowed = true
		decision.Reason = "workspace-role"
		return decision
	}
	if roleHasCapability(projectRoleCapabilities[request.ProjectRole], request.Capability) {
		decision.Allowed = true
		decision.Reason = "project-role"
		return decision
	}
	if request.WorkspaceRole != "" || request.ProjectRole != "" {
		decision.Reason = "role-denied"
	}
	return decision
}

func roleHasCapability(set map[Capability]struct{}, capability Capability) bool {
	_, ok := set[capability]
	return ok
}

// ValidCapability prevents typoed or undeclared route capabilities from
// accidentally falling through to a permissive default.
func ValidCapability(capability Capability) bool {
	for _, candidate := range allCapabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func ParseWorkspaceRole(value string) (WorkspaceRole, bool) {
	role := WorkspaceRole(strings.ToLower(strings.TrimSpace(value)))
	_, ok := workspaceRoleCapabilities[role]
	return role, ok
}

func ParseProjectRole(value string) (ProjectRole, bool) {
	role := ProjectRole(strings.ToLower(strings.TrimSpace(value)))
	_, ok := projectRoleCapabilities[role]
	return role, ok
}
