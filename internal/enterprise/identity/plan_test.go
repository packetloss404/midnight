package identity

import (
	"errors"
	"testing"
)

const testCertPEM = `-----BEGIN CERTIFICATE-----
AQID
-----END CERTIFICATE-----`

func TestBuildPlanAllowsSAMLWithSCIMFoundation(t *testing.T) {
	plan, err := BuildPlan(PlanRequest{
		WorkspaceID: "wrk_acme",
		Provider: ProviderConfig{Kind: ProviderSAML, SAML: &SAMLConfig{
			IDPEntityID:            "https://idp.example.com/saml",
			SPEntityID:             "https://midnight.example.com/saml/metadata",
			SSOURL:                 "https://idp.example.com/sso",
			ACSURL:                 "https://midnight.example.com/api/v1alpha/auth/saml/callback",
			SigningCertificatesPEM: []string{testCertPEM},
		}},
		SCIM: &SCIMConfig{
			BaseURL:              "https://idp.example.com/scim/v2",
			BearerTokenSecretRef: "secretref_scim_token",
			UserNameAttribute:    "userName",
		},
		GroupRoleMappings: []GroupRoleMapping{{ExternalGroup: " Platform Admins ", Role: "admin"}, {ExternalGroup: "Developers", Role: "developer"}},
		JITProvisioning:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProviderKind != ProviderSAML || !plan.SCIMEnabled || !plan.JITProvisioning || plan.CallbackPaths[0] != "/api/v1alpha/auth/saml/callback" {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.AuditActions) != 2 || plan.AuditActions[0] != "enterprise.identity.plan.created" {
		t.Fatalf("audit actions = %#v", plan.AuditActions)
	}
	if len(plan.GroupRoleMappings) != 2 || plan.GroupRoleMappings[0].ExternalGroup != "Developers" || plan.GroupRoleMappings[1].ExternalGroup != "Platform Admins" {
		t.Fatalf("group role mappings = %#v", plan.GroupRoleMappings)
	}
}

func TestBuildPlanRejectsUnsafeSAMLAndSCIMURLs(t *testing.T) {
	_, err := BuildPlan(PlanRequest{
		WorkspaceID: "wrk_acme",
		Provider: ProviderConfig{Kind: ProviderSAML, SAML: &SAMLConfig{
			IDPEntityID:            "https://idp.example.com/saml",
			SPEntityID:             "https://midnight.example.com/saml/metadata",
			SSOURL:                 "http://idp.example.com/sso",
			ACSURL:                 "https://midnight.example.com/api/v1alpha/auth/saml/callback",
			SigningCertificatesPEM: []string{testCertPEM},
		}},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}

	_, err = BuildPlan(PlanRequest{
		WorkspaceID: "wrk_acme",
		Provider: ProviderConfig{Kind: ProviderOIDC, OIDC: &OIDCConfig{
			IssuerURL:   "https://idp.example.com",
			ClientID:    "midnight",
			RedirectURL: "https://midnight.example.com/api/v1alpha/auth/oidc/callback",
		}},
		SCIM: &SCIMConfig{BaseURL: "https://idp.example.com/scim?tenant=acme", BearerTokenSecretRef: "secretref_scim_token", UserNameAttribute: "userName"},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestBuildPlanRejectsInvalidGroupRoleMapping(t *testing.T) {
	_, err := BuildPlan(PlanRequest{
		WorkspaceID: "wrk_acme",
		Provider: ProviderConfig{Kind: ProviderOIDC, OIDC: &OIDCConfig{
			IssuerURL:   "https://idp.example.com",
			ClientID:    "midnight",
			RedirectURL: "https://midnight.example.com/api/v1alpha/auth/oidc/callback",
		}},
		GroupRoleMappings: []GroupRoleMapping{{ExternalGroup: "Engineering", Role: "superuser"}},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}
