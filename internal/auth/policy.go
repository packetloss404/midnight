package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalidPolicy = errors.New("invalid auth policy")

type SubjectKind string

const (
	SubjectUser           SubjectKind = "user"
	SubjectTeam           SubjectKind = "team"
	SubjectServiceAccount SubjectKind = "service_account"
)

type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
)

type Action string

const (
	ActionWorkspaceRead   Action = "workspace.read"
	ActionWorkspaceManage Action = "workspace.manage"
	ActionPolicyWrite     Action = "policy.write"
	ActionServiceRead     Action = "service.read"
	ActionDeploymentWrite Action = "deployment.write"
)

type ScopeKind string

const (
	ScopeWorkspace ScopeKind = "workspace"
)

type SubjectRef struct {
	Kind SubjectKind `json:"kind"`
	ID   string      `json:"id"`
}

type ScopeRef struct {
	Kind ScopeKind `json:"kind"`
	ID   string    `json:"id"`
}

type Workspace struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type Team struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type TeamMembership struct {
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
}

type PolicyBinding struct {
	Subject SubjectRef `json:"subject"`
	Role    Role       `json:"role"`
	Scope   ScopeRef   `json:"scope"`
}

type PolicyDocument struct {
	Version  int             `json:"version"`
	Bindings []PolicyBinding `json:"bindings"`
}

type AccessRequest struct {
	Subject SubjectRef `json:"subject"`
	TeamIDs []string   `json:"team_ids,omitempty"`
	Action  Action     `json:"action"`
	Scope   ScopeRef   `json:"scope"`
}

type DecisionEffect string

const (
	DecisionAllow DecisionEffect = "allow"
	DecisionDeny  DecisionEffect = "deny"
)

type Decision struct {
	Effect        DecisionEffect `json:"effect"`
	Allowed       bool           `json:"allowed"`
	Reason        string         `json:"reason"`
	RuleID        string         `json:"rule_id,omitempty"`
	PolicyVersion int            `json:"policy_version"`
	Subject       SubjectRef     `json:"subject"`
	Action        Action         `json:"action"`
	Scope         ScopeRef       `json:"scope"`
}

func ParsePolicyDocument(data []byte) (PolicyDocument, error) {
	var policy PolicyDocument
	if err := json.Unmarshal(data, &policy); err != nil {
		return PolicyDocument{}, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := policy.Validate(); err != nil {
		return PolicyDocument{}, err
	}
	return policy, nil
}

func (p PolicyDocument) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("%w: version must be 1", ErrInvalidPolicy)
	}
	for i, binding := range p.Bindings {
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("%w: binding %d: %v", ErrInvalidPolicy, i, err)
		}
	}
	return nil
}

func (b PolicyBinding) Validate() error {
	if err := b.Subject.Validate(); err != nil {
		return err
	}
	if !validRole(b.Role) {
		return fmt.Errorf("role %q is not supported", b.Role)
	}
	return b.Scope.Validate()
}

func (s SubjectRef) Validate() error {
	switch s.Kind {
	case SubjectUser, SubjectTeam, SubjectServiceAccount:
	default:
		return fmt.Errorf("subject kind %q is not supported", s.Kind)
	}
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("subject id is required")
	}
	return nil
}

func (s ScopeRef) Validate() error {
	if s.Kind != ScopeWorkspace {
		return fmt.Errorf("scope kind %q is not supported", s.Kind)
	}
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("scope id is required")
	}
	return nil
}

func (p PolicyDocument) Allows(request AccessRequest) bool {
	return p.Evaluate(request).Allowed
}

func (p PolicyDocument) Evaluate(request AccessRequest) Decision {
	decision := Decision{
		Effect:        DecisionDeny,
		Allowed:       false,
		Reason:        "no-matching-binding",
		PolicyVersion: p.Version,
		Subject:       request.Subject,
		Action:        request.Action,
		Scope:         request.Scope,
	}
	if request.Subject.Validate() != nil || request.Scope.Validate() != nil || !validAction(request.Action) {
		decision.Reason = "invalid-request"
		return decision
	}

	matchedSubject := false
	for i, binding := range p.Bindings {
		if binding.Validate() != nil {
			continue
		}
		if binding.Scope != request.Scope || !subjectMatches(binding.Subject, request.Subject, request.TeamIDs) {
			continue
		}
		matchedSubject = true
		if roleAllows(binding.Role, request.Action) {
			decision.Effect = DecisionAllow
			decision.Allowed = true
			decision.Reason = "binding-allowed"
			decision.RuleID = fmt.Sprintf("authz.binding.%d.%s", i, binding.Role)
			return decision
		}
	}
	if matchedSubject {
		decision.Reason = "role-denied"
	}
	return decision
}

func subjectMatches(binding SubjectRef, subject SubjectRef, teamIDs []string) bool {
	if binding == subject {
		return true
	}
	if binding.Kind != SubjectTeam || subject.Kind != SubjectUser {
		return false
	}
	for _, teamID := range teamIDs {
		if binding.ID == teamID {
			return true
		}
	}
	return false
}

func validRole(role Role) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer:
		return true
	default:
		return false
	}
}

func validAction(action Action) bool {
	switch action {
	case ActionWorkspaceRead, ActionWorkspaceManage, ActionPolicyWrite, ActionServiceRead, ActionDeploymentWrite:
		return true
	default:
		return false
	}
}

func roleAllows(role Role, action Action) bool {
	switch role {
	case RoleOwner:
		return true
	case RoleAdmin:
		return action == ActionWorkspaceRead || action == ActionWorkspaceManage || action == ActionServiceRead || action == ActionDeploymentWrite
	case RoleDeveloper:
		return action == ActionWorkspaceRead || action == ActionServiceRead || action == ActionDeploymentWrite
	case RoleViewer:
		return action == ActionWorkspaceRead || action == ActionServiceRead
	default:
		return false
	}
}

type OIDCProviderConfig struct {
	IssuerURL    string   `json:"issuer_url"`
	ClientID     string   `json:"client_id"`
	RedirectURL  string   `json:"redirect_url"`
	AllowedTeams []string `json:"allowed_teams,omitempty"`
}

func (c OIDCProviderConfig) Validate() error {
	issuer, err := parseHTTPSURL(c.IssuerURL)
	if err != nil {
		return fmt.Errorf("issuer_url: %w", err)
	}
	if issuer.RawQuery != "" || issuer.Fragment != "" {
		return errors.New("issuer_url must not include query or fragment")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("client_id is required")
	}
	redirect, err := parseHTTPSURL(c.RedirectURL)
	if err != nil {
		return fmt.Errorf("redirect_url: %w", err)
	}
	if redirect.RawQuery != "" || redirect.Fragment != "" {
		return errors.New("redirect_url must not include query or fragment")
	}
	if redirect.Path != "/api/v1alpha/auth/oidc/callback" {
		return errors.New("redirect_url path must target /api/v1alpha/auth/oidc/callback")
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
