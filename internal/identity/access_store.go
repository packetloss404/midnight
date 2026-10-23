package identity

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAccessNotFound            = errors.New("tenant resource not found")
	ErrAuthorizationDenied       = errors.New("authorization denied")
	ErrWorkspaceClaimed          = errors.New("default workspace is already claimed")
	ErrLastWorkspaceAdmin        = errors.New("workspace must retain an active administrator")
	ErrLastProjectOwner          = errors.New("project must retain an active owner")
	ErrMembershipNotFound        = errors.New("membership not found")
	ErrMembershipVersionConflict = errors.New("membership version conflict")
	ErrIdentityConflict          = errors.New("identity record conflicts with an existing record")
)

type Workspace struct {
	ID          string        `json:"id"`
	Slug        string        `json:"slug"`
	DisplayName string        `json:"display_name"`
	Role        WorkspaceRole `json:"role,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type WorkspaceMember struct {
	WorkspaceID string        `json:"workspace_id"`
	User        User          `json:"user"`
	Role        WorkspaceRole `json:"role"`
	InvitedBy   string        `json:"invited_by,omitempty"`
	InvitedAt   time.Time     `json:"invited_at"`
	AcceptedAt  *time.Time    `json:"accepted_at,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Version     int64         `json:"resource_version"`
}

type ProjectMember struct {
	ProjectID  string      `json:"project_id"`
	User       User        `json:"user"`
	Role       ProjectRole `json:"role"`
	InvitedBy  string      `json:"invited_by,omitempty"`
	InvitedAt  time.Time   `json:"invited_at"`
	AcceptedAt *time.Time  `json:"accepted_at,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Version    int64       `json:"resource_version"`
}

type AccessTarget struct {
	WorkspaceID   string
	ProjectID     string
	EnvironmentID string
}

type AccessGrant struct {
	Target                AccessTarget
	WorkspaceRole         WorkspaceRole
	ProjectRole           ProjectRole
	EnvironmentRestricted bool
}

type ClaimDefaultWorkspaceRequest struct {
	UserID               string
	Email                string
	DisplayName          string
	PasswordHash         string
	TOTPSecretEnvelope   []byte
	TOTPEnrolledAt       time.Time
	WorkspaceSlug        string
	WorkspaceDisplayName string
	Now                  time.Time
}

type ClaimDefaultWorkspaceResult struct {
	User      User      `json:"user"`
	Workspace Workspace `json:"workspace"`
}

// AccessStore is the durable tenant membership and scope boundary.
type AccessStore interface {
	ClaimDefaultWorkspace(context.Context, ClaimDefaultWorkspaceRequest) (ClaimDefaultWorkspaceResult, error)
	ListWorkspaces(context.Context, string) ([]Workspace, error)
	ListWorkspaceMembers(context.Context, string) ([]WorkspaceMember, error)
	GetWorkspaceMember(context.Context, string, string) (WorkspaceMember, error)
	// PutWorkspaceMember changes an existing membership. Creating a new
	// membership requires the separately verified invitation/acceptance flow;
	// callers must not treat this method as an invite or user-provisioning API.
	PutWorkspaceMember(context.Context, string, string, WorkspaceRole, string, time.Time) (WorkspaceMember, error)
	PutWorkspaceMemberCAS(context.Context, string, string, WorkspaceRole, string, int64, time.Time) (WorkspaceMember, error)
	RemoveWorkspaceMember(context.Context, string, string, string, time.Time) error
	RemoveWorkspaceMemberCAS(context.Context, string, string, string, int64, time.Time) error
	ListProjectMembers(context.Context, string) ([]ProjectMember, error)
	GetProjectMember(context.Context, string, string) (ProjectMember, error)
	// PutProjectMember changes an existing membership and never provisions or
	// silently attaches an arbitrary user.
	PutProjectMember(context.Context, string, string, ProjectRole, string, time.Time) (ProjectMember, error)
	PutProjectMemberCAS(context.Context, string, string, ProjectRole, string, int64, time.Time) (ProjectMember, error)
	RemoveProjectMember(context.Context, string, string, string, time.Time) error
	RemoveProjectMemberCAS(context.Context, string, string, string, int64, time.Time) error
	ResolveAccess(context.Context, string, AccessTarget) (AccessGrant, error)
}

// Authorize resolves server-owned resource lineage and evaluates the canonical
// role matrix. Caller-supplied workspace IDs never substitute for project or
// environment ownership.
func Authorize(ctx context.Context, store AccessStore, userID string, target AccessTarget, capability Capability, gitAutomation bool) (AccessGrant, AccessDecision, error) {
	if store == nil {
		return AccessGrant{}, AccessDecision{}, errors.New("identity access store is required")
	}
	grant, err := store.ResolveAccess(ctx, userID, target)
	if err != nil {
		return AccessGrant{}, AccessDecision{}, err
	}
	decision := EvaluateAccess(AccessRequest{
		WorkspaceRole:         grant.WorkspaceRole,
		ProjectRole:           grant.ProjectRole,
		Capability:            capability,
		EnvironmentRestricted: grant.EnvironmentRestricted,
		GitAutomation:         gitAutomation,
	})
	if !decision.Allowed {
		return grant, decision, ErrAuthorizationDenied
	}
	return grant, decision, nil
}
