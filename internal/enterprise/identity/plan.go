package identity

import (
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid enterprise identity config")

type ProviderKind string

const (
	ProviderOIDC ProviderKind = "oidc"
	ProviderSAML ProviderKind = "saml"
)

type ProviderConfig struct {
	Kind ProviderKind `json:"kind"`
	OIDC *OIDCConfig  `json:"oidc,omitempty"`
	SAML *SAMLConfig  `json:"saml,omitempty"`
}

type OIDCConfig struct {
	IssuerURL   string `json:"issuer_url"`
	ClientID    string `json:"client_id"`
	RedirectURL string `json:"redirect_url"`
}

type SAMLConfig struct {
	IDPEntityID            string   `json:"idp_entity_id"`
	SPEntityID             string   `json:"sp_entity_id"`
	SSOURL                 string   `json:"sso_url"`
	ACSURL                 string   `json:"acs_url"`
	SigningCertificatesPEM []string `json:"signing_certificates_pem"`
}

type SCIMConfig struct {
	BaseURL              string   `json:"base_url"`
	BearerTokenSecretRef string   `json:"bearer_token_secret_ref"`
	UserNameAttribute    string   `json:"user_name_attribute"`
	RequiredSchemas      []string `json:"required_schemas,omitempty"`
}

type GroupRoleMapping struct {
	ExternalGroup string `json:"external_group"`
	Role          string `json:"role"`
}

type PlanRequest struct {
	WorkspaceID       string             `json:"workspace_id"`
	Provider          ProviderConfig     `json:"provider"`
	SCIM              *SCIMConfig        `json:"scim,omitempty"`
	GroupRoleMappings []GroupRoleMapping `json:"group_role_mappings,omitempty"`
	JITProvisioning   bool               `json:"jit_provisioning"`
}

type Plan struct {
	WorkspaceID       string             `json:"workspace_id"`
	ProviderKind      ProviderKind       `json:"provider_kind"`
	SCIMEnabled       bool               `json:"scim_enabled"`
	JITProvisioning   bool               `json:"jit_provisioning"`
	GroupRoleMappings []GroupRoleMapping `json:"group_role_mappings"`
	CallbackPaths     []string           `json:"callback_paths"`
	AuditActions      []string           `json:"audit_actions"`
}

func BuildPlan(request PlanRequest) (Plan, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return Plan{}, fmt.Errorf("%w: workspace_id is required", ErrInvalidConfig)
	}
	if err := request.Provider.Validate(); err != nil {
		return Plan{}, err
	}
	if request.SCIM != nil {
		if err := request.SCIM.Validate(); err != nil {
			return Plan{}, err
		}
	}
	mappings := make([]GroupRoleMapping, len(request.GroupRoleMappings))
	copy(mappings, request.GroupRoleMappings)
	for i, mapping := range mappings {
		if strings.TrimSpace(mapping.ExternalGroup) == "" || !validRole(mapping.Role) {
			return Plan{}, fmt.Errorf("%w: group_role_mappings[%d] is invalid", ErrInvalidConfig, i)
		}
		mappings[i].ExternalGroup = strings.TrimSpace(mapping.ExternalGroup)
	}
	sort.SliceStable(mappings, func(i, j int) bool {
		if mappings[i].ExternalGroup != mappings[j].ExternalGroup {
			return mappings[i].ExternalGroup < mappings[j].ExternalGroup
		}
		return mappings[i].Role < mappings[j].Role
	})

	paths := []string{"/api/v1alpha/auth/oidc/callback"}
	if request.Provider.Kind == ProviderSAML {
		paths = []string{"/api/v1alpha/auth/saml/callback"}
	}
	return Plan{
		WorkspaceID:       request.WorkspaceID,
		ProviderKind:      request.Provider.Kind,
		SCIMEnabled:       request.SCIM != nil,
		JITProvisioning:   request.JITProvisioning,
		GroupRoleMappings: mappings,
		CallbackPaths:     paths,
		AuditActions:      []string{"enterprise.identity.plan.created", "enterprise.identity.config.validated"},
	}, nil
}

func (c ProviderConfig) Validate() error {
	switch c.Kind {
	case ProviderOIDC:
		if c.OIDC == nil || c.SAML != nil {
			return fmt.Errorf("%w: oidc provider requires only oidc config", ErrInvalidConfig)
		}
		return c.OIDC.Validate()
	case ProviderSAML:
		if c.SAML == nil || c.OIDC != nil {
			return fmt.Errorf("%w: saml provider requires only saml config", ErrInvalidConfig)
		}
		return c.SAML.Validate()
	default:
		return fmt.Errorf("%w: unsupported provider kind %q", ErrInvalidConfig, c.Kind)
	}
}

func (c OIDCConfig) Validate() error {
	issuer, err := parseHTTPSURL(c.IssuerURL)
	if err != nil {
		return fmt.Errorf("%w: oidc issuer_url: %v", ErrInvalidConfig, err)
	}
	if issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("%w: oidc issuer_url must not include query or fragment", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("%w: oidc client_id is required", ErrInvalidConfig)
	}
	redirect, err := parseHTTPSURL(c.RedirectURL)
	if err != nil {
		return fmt.Errorf("%w: oidc redirect_url: %v", ErrInvalidConfig, err)
	}
	if redirect.Path != "/api/v1alpha/auth/oidc/callback" || redirect.RawQuery != "" || redirect.Fragment != "" {
		return fmt.Errorf("%w: oidc redirect_url must target /api/v1alpha/auth/oidc/callback without query or fragment", ErrInvalidConfig)
	}
	return nil
}

func (c SAMLConfig) Validate() error {
	if strings.TrimSpace(c.IDPEntityID) == "" || strings.TrimSpace(c.SPEntityID) == "" {
		return fmt.Errorf("%w: saml entity ids are required", ErrInvalidConfig)
	}
	sso, err := parseHTTPSURL(c.SSOURL)
	if err != nil {
		return fmt.Errorf("%w: saml sso_url: %v", ErrInvalidConfig, err)
	}
	if sso.RawQuery != "" || sso.Fragment != "" {
		return fmt.Errorf("%w: saml sso_url must not include query or fragment", ErrInvalidConfig)
	}
	acs, err := parseHTTPSURL(c.ACSURL)
	if err != nil {
		return fmt.Errorf("%w: saml acs_url: %v", ErrInvalidConfig, err)
	}
	if acs.Path != "/api/v1alpha/auth/saml/callback" || acs.RawQuery != "" || acs.Fragment != "" {
		return fmt.Errorf("%w: saml acs_url must target /api/v1alpha/auth/saml/callback without query or fragment", ErrInvalidConfig)
	}
	if len(c.SigningCertificatesPEM) == 0 {
		return fmt.Errorf("%w: saml signing certificate is required", ErrInvalidConfig)
	}
	for i, cert := range c.SigningCertificatesPEM {
		block, rest := pem.Decode([]byte(strings.TrimSpace(cert)))
		if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
			return fmt.Errorf("%w: saml signing_certificates_pem[%d] must be one CERTIFICATE PEM block", ErrInvalidConfig, i)
		}
	}
	return nil
}

func (c SCIMConfig) Validate() error {
	base, err := parseHTTPSURL(c.BaseURL)
	if err != nil {
		return fmt.Errorf("%w: scim base_url: %v", ErrInvalidConfig, err)
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("%w: scim base_url must not include query or fragment", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.BearerTokenSecretRef) == "" {
		return fmt.Errorf("%w: scim bearer_token_secret_ref is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.UserNameAttribute) == "" {
		return fmt.Errorf("%w: scim user_name_attribute is required", ErrInvalidConfig)
	}
	return nil
}

func parseHTTPSURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("must be an absolute https URL")
	}
	return parsed, nil
}

func validRole(role string) bool {
	switch role {
	case "owner", "admin", "developer", "viewer":
		return true
	default:
		return false
	}
}
