package auth

import (
	"errors"
	"testing"
)

func TestParsePolicyDocumentAllowsTeamRole(t *testing.T) {
	policy, err := ParsePolicyDocument([]byte(`{
		"version": 1,
		"bindings": [
			{
				"subject": {"kind": "team", "id": "team_platform"},
				"role": "developer",
				"scope": {"kind": "workspace", "id": "wrk_acme"}
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	if !policy.Allows(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_ian"},
		TeamIDs: []string{"team_platform"},
		Action:  ActionDeploymentWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	}) {
		t.Fatal("expected team developer to deploy in workspace")
	}
}

func TestPolicyDocumentDeniesUnboundTeamAndHigherPrivilege(t *testing.T) {
	policy := PolicyDocument{
		Version: 1,
		Bindings: []PolicyBinding{{
			Subject: SubjectRef{Kind: SubjectTeam, ID: "team_viewers"},
			Role:    RoleViewer,
			Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
		}},
	}

	if policy.Allows(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_ian"},
		TeamIDs: []string{"team_platform"},
		Action:  ActionServiceRead,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	}) {
		t.Fatal("expected user without matching team to be denied")
	}
	if policy.Allows(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_ian"},
		TeamIDs: []string{"team_viewers"},
		Action:  ActionDeploymentWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	}) {
		t.Fatal("expected viewer to be denied deployment write")
	}
}

func TestPolicyDocumentOwnerCanWritePolicy(t *testing.T) {
	policy := PolicyDocument{
		Version: 1,
		Bindings: []PolicyBinding{{
			Subject: SubjectRef{Kind: SubjectUser, ID: "usr_owner"},
			Role:    RoleOwner,
			Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
		}},
	}

	if !policy.Allows(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_owner"},
		Action:  ActionPolicyWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	}) {
		t.Fatal("expected owner to write policy")
	}
}

func TestPolicyDocumentAdminCannotWritePolicy(t *testing.T) {
	policy := PolicyDocument{
		Version: 1,
		Bindings: []PolicyBinding{{
			Subject: SubjectRef{Kind: SubjectUser, ID: "usr_admin"},
			Role:    RoleAdmin,
			Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
		}},
	}

	if policy.Allows(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_admin"},
		Action:  ActionPolicyWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	}) {
		t.Fatal("expected admin to be denied policy write")
	}
}

func TestPolicyDocumentEvaluateReturnsDecisionEnvelope(t *testing.T) {
	policy := PolicyDocument{
		Version: 1,
		Bindings: []PolicyBinding{{
			Subject: SubjectRef{Kind: SubjectTeam, ID: "team_platform"},
			Role:    RoleDeveloper,
			Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
		}},
	}

	decision := policy.Evaluate(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_ian"},
		TeamIDs: []string{"team_platform"},
		Action:  ActionDeploymentWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	})
	if !decision.Allowed || decision.Effect != DecisionAllow || decision.Reason != "binding-allowed" || decision.RuleID == "" {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.PolicyVersion != 1 || decision.Subject.ID != "usr_ian" || decision.Scope.ID != "wrk_acme" {
		t.Fatalf("decision metadata = %#v", decision)
	}
}

func TestPolicyDocumentEvaluateExplainsRoleDenied(t *testing.T) {
	policy := PolicyDocument{
		Version: 1,
		Bindings: []PolicyBinding{{
			Subject: SubjectRef{Kind: SubjectUser, ID: "usr_viewer"},
			Role:    RoleViewer,
			Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
		}},
	}

	decision := policy.Evaluate(AccessRequest{
		Subject: SubjectRef{Kind: SubjectUser, ID: "usr_viewer"},
		Action:  ActionDeploymentWrite,
		Scope:   ScopeRef{Kind: ScopeWorkspace, ID: "wrk_acme"},
	})
	if decision.Allowed || decision.Effect != DecisionDeny || decision.Reason != "role-denied" || decision.RuleID != "" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestParsePolicyDocumentRejectsUnknownRole(t *testing.T) {
	_, err := ParsePolicyDocument([]byte(`{
		"version": 1,
		"bindings": [
			{
				"subject": {"kind": "user", "id": "usr_ian"},
				"role": "root",
				"scope": {"kind": "workspace", "id": "wrk_acme"}
			}
		]
	}`))
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("err = %v, want ErrInvalidPolicy", err)
	}
}

func TestOIDCProviderConfigValidate(t *testing.T) {
	config := OIDCProviderConfig{
		IssuerURL:   "https://login.example.com/realms/midnight",
		ClientID:    "midnight",
		RedirectURL: "https://midnight.example.com/api/v1alpha/auth/oidc/callback",
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCProviderConfigRejectsUnsafeURLs(t *testing.T) {
	config := OIDCProviderConfig{
		IssuerURL:   "http://login.example.com",
		ClientID:    "midnight",
		RedirectURL: "https://midnight.example.com/api/v1alpha/auth/oidc/callback",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected http issuer to be rejected")
	}

	config.IssuerURL = "https://login.example.com"
	config.RedirectURL = "https://midnight.example.com/callback"
	if err := config.Validate(); err == nil {
		t.Fatal("expected non-auth callback path to be rejected")
	}
}

func TestFederatedCredentialConfigValidate(t *testing.T) {
	config := FederatedCredentialConfig{
		Name:             "ado-prod-deployer",
		Provider:         CIProviderAzureDevOps,
		IssuerURL:        "https://vstoken.dev.azure.com/acme",
		Subject:          "sc://acme/platform/prod-deployer",
		Audience:         "api://midnight/workload-identity",
		WorkspaceID:      "wrk_acme",
		ServiceAccountID: "sa_prod_deployer",
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFederatedCredentialConfigRejectsUnsafeIssuerAndUnknownProvider(t *testing.T) {
	config := FederatedCredentialConfig{
		Name:             "jenkins",
		Provider:         CIProvider("unknown"),
		IssuerURL:        "https://jenkins.example.com/oidc",
		Subject:          "job:deploy",
		Audience:         "api://midnight/workload-identity",
		WorkspaceID:      "wrk_acme",
		ServiceAccountID: "sa_deployer",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}

	config.Provider = CIProviderJenkins
	config.IssuerURL = "http://jenkins.example.com/oidc"
	if err := config.Validate(); err == nil {
		t.Fatal("expected http issuer to be rejected")
	}
}

func TestFederatedCredentialMatchesExactClaims(t *testing.T) {
	config := FederatedCredentialConfig{
		Name:             "bitbucket-main",
		Provider:         CIProviderBitbucket,
		IssuerURL:        "https://api.bitbucket.org/2.0/workspaces/acme/pipelines-config/identity/oidc",
		Subject:          "repo:acme/api:ref:refs/heads/main",
		Audience:         "api://midnight/workload-identity",
		WorkspaceID:      "wrk_acme",
		ServiceAccountID: "sa_deployer",
	}
	if !config.Matches(FederatedClaims{
		Issuer:   config.IssuerURL,
		Subject:  config.Subject,
		Audience: config.Audience,
	}) {
		t.Fatal("expected matching claims")
	}
	if config.Matches(FederatedClaims{Issuer: config.IssuerURL, Subject: "repo:fork/api", Audience: config.Audience}) {
		t.Fatal("expected subject mismatch to be rejected")
	}
}
