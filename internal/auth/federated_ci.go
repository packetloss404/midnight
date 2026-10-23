// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"errors"
	"fmt"
	"strings"
)

type CIProvider string

const (
	CIProviderJenkins       CIProvider = "jenkins"
	CIProviderAzureDevOps   CIProvider = "azure_devops"
	CIProviderBitbucket     CIProvider = "bitbucket"
	CIProviderGitHubActions CIProvider = "github_actions"
	CIProviderGitLabCI      CIProvider = "gitlab_ci"
)

type FederatedCredentialConfig struct {
	Name             string     `json:"name"`
	Provider         CIProvider `json:"provider"`
	IssuerURL        string     `json:"issuer_url"`
	Subject          string     `json:"subject"`
	Audience         string     `json:"audience"`
	WorkspaceID      string     `json:"workspace_id"`
	ServiceAccountID string     `json:"service_account_id"`
}

type FederatedClaims struct {
	Issuer   string
	Subject  string
	Audience string
}

func (c FederatedCredentialConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validCIProvider(c.Provider) {
		return fmt.Errorf("provider %q is not supported", c.Provider)
	}
	issuer, err := parseHTTPSURL(c.IssuerURL)
	if err != nil {
		return fmt.Errorf("issuer_url: %w", err)
	}
	if issuer.RawQuery != "" || issuer.Fragment != "" {
		return errors.New("issuer_url must not include query or fragment")
	}
	if strings.TrimSpace(c.Subject) == "" {
		return errors.New("subject is required")
	}
	if strings.TrimSpace(c.Audience) == "" {
		return errors.New("audience is required")
	}
	if strings.TrimSpace(c.WorkspaceID) == "" {
		return errors.New("workspace_id is required")
	}
	if strings.TrimSpace(c.ServiceAccountID) == "" {
		return errors.New("service_account_id is required")
	}
	return nil
}

func (c FederatedCredentialConfig) Matches(claims FederatedClaims) bool {
	if c.Validate() != nil {
		return false
	}
	return strings.TrimSpace(c.IssuerURL) == strings.TrimSpace(claims.Issuer) &&
		c.Subject == claims.Subject &&
		c.Audience == claims.Audience
}

func validCIProvider(provider CIProvider) bool {
	switch provider {
	case CIProviderJenkins, CIProviderAzureDevOps, CIProviderBitbucket, CIProviderGitHubActions, CIProviderGitLabCI:
		return true
	default:
		return false
	}
}
