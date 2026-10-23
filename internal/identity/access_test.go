package identity

import "testing"

func TestWorkspaceRoleCapabilityMatrix(t *testing.T) {
	tests := []struct {
		name       string
		role       WorkspaceRole
		capability Capability
		allowed    bool
	}{
		{"admin destructive", WorkspaceRoleAdmin, CapabilityProjectDelete, true},
		{"member creates", WorkspaceRoleMember, CapabilityProjectCreate, true},
		{"member creates ordinary environments", WorkspaceRoleMember, CapabilityEnvironmentWrite, true},
		{"member cannot delete", WorkspaceRoleMember, CapabilityProjectDelete, false},
		{"deployer git only", WorkspaceRoleDeployer, CapabilityDeploymentTriggerGit, true},
		{"deployer cannot cli", WorkspaceRoleDeployer, CapabilityDeploymentTriggerCLI, false},
		{"deployer cannot logs", WorkspaceRoleDeployer, CapabilityLogsRead, false},
		{"deployer cannot read domains", WorkspaceRoleDeployer, CapabilityDomainsRead, false},
		{"member reads domains", WorkspaceRoleMember, CapabilityDomainsRead, true},
		{"member writes domains", WorkspaceRoleMember, CapabilityDomainsWrite, true},
		{"viewer reads domains", WorkspaceRoleViewer, CapabilityDomainsRead, true},
		{"viewer cannot write domains", WorkspaceRoleViewer, CapabilityDomainsWrite, false},
		{"viewer cannot secrets", WorkspaceRoleViewer, CapabilityVariablesRead, false},
		{"viewer cannot create environments", WorkspaceRoleViewer, CapabilityEnvironmentWrite, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateAccess(AccessRequest{WorkspaceRole: tt.role, Capability: tt.capability})
			if got.Allowed != tt.allowed {
				t.Fatalf("decision = %#v, want allowed=%v", got, tt.allowed)
			}
		})
	}
}

func TestProjectGrantAddsButNeverDowngradesWorkspaceGrant(t *testing.T) {
	got := EvaluateAccess(AccessRequest{
		WorkspaceRole: WorkspaceRoleMember,
		ProjectRole:   ProjectRoleViewer,
		Capability:    CapabilityVariablesWrite,
	})
	if !got.Allowed || got.Reason != "workspace-role" {
		t.Fatalf("decision = %#v", got)
	}

	got = EvaluateAccess(AccessRequest{
		ProjectRole: ProjectRoleEditor,
		Capability:  CapabilityProjectDelete,
	})
	if got.Allowed {
		t.Fatalf("editor received destructive access: %#v", got)
	}

	got = EvaluateAccess(AccessRequest{
		ProjectRole: ProjectRoleEditor,
		Capability:  CapabilityEnvironmentWrite,
	})
	if !got.Allowed {
		t.Fatalf("editor cannot create an ordinary environment: %#v", got)
	}

	for _, test := range []struct {
		role       ProjectRole
		capability Capability
		allowed    bool
	}{
		{ProjectRoleOwner, CapabilityDomainsRead, true},
		{ProjectRoleOwner, CapabilityDomainsWrite, true},
		{ProjectRoleEditor, CapabilityDomainsRead, true},
		{ProjectRoleEditor, CapabilityDomainsWrite, true},
		{ProjectRoleViewer, CapabilityDomainsRead, true},
		{ProjectRoleViewer, CapabilityDomainsWrite, false},
	} {
		got = EvaluateAccess(AccessRequest{
			ProjectRole: test.role,
			Capability:  test.capability,
		})
		if got.Allowed != test.allowed {
			t.Fatalf("project role %q capability %q decision = %#v, want allowed=%v", test.role, test.capability, got, test.allowed)
		}
	}
}

func TestRestrictedEnvironmentIsHardOverlay(t *testing.T) {
	for _, role := range []WorkspaceRole{WorkspaceRoleMember, WorkspaceRoleDeployer, WorkspaceRoleViewer} {
		got := EvaluateAccess(AccessRequest{
			WorkspaceRole:         role,
			ProjectRole:           ProjectRoleOwner,
			Capability:            CapabilityServiceRead,
			EnvironmentRestricted: true,
		})
		if got.Allowed || got.Reason != "environment-restricted" {
			t.Fatalf("role %q decision = %#v", role, got)
		}
	}

	admin := EvaluateAccess(AccessRequest{
		WorkspaceRole:         WorkspaceRoleAdmin,
		Capability:            CapabilityVariablesRead,
		EnvironmentRestricted: true,
	})
	if !admin.Allowed {
		t.Fatalf("admin decision = %#v", admin)
	}

	restrictedDomain := EvaluateAccess(AccessRequest{
		WorkspaceRole:         WorkspaceRoleMember,
		ProjectRole:           ProjectRoleOwner,
		Capability:            CapabilityDomainsWrite,
		EnvironmentRestricted: true,
	})
	if restrictedDomain.Allowed || restrictedDomain.Reason != "environment-restricted" {
		t.Fatalf("restricted domain decision = %#v", restrictedDomain)
	}

	git := EvaluateAccess(AccessRequest{
		WorkspaceRole:         WorkspaceRoleDeployer,
		Capability:            CapabilityDeploymentTriggerGit,
		EnvironmentRestricted: true,
		GitAutomation:         true,
	})
	if !git.Allowed || git.Reason != "restricted-environment-git-trigger" {
		t.Fatalf("git decision = %#v", git)
	}
}

func TestInvalidCapabilityFailsClosed(t *testing.T) {
	got := EvaluateAccess(AccessRequest{
		WorkspaceRole: WorkspaceRoleAdmin,
		Capability:    Capability("typo.superuser"),
	})
	if got.Allowed || got.Reason != "invalid-capability" {
		t.Fatalf("decision = %#v", got)
	}
}
