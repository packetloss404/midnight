// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"errors"
	"fmt"
	"strings"
)

// PromptArg is one structured argument on a server-side prompt template.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt is a pre-built template the MCP server offers under
// prompts/list. The template body uses simple `${arg}` substitution
// resolved by [renderPrompt].
type Prompt struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Arguments   []PromptArg `json:"arguments"`
	Template    string      `json:"template"`
}

// ErrUnknownPrompt is returned by prompts/get for an unregistered name.
var ErrUnknownPrompt = errors.New("mcp: unknown prompt")

// ErrMissingRequiredArg is returned by prompts/get when a required
// argument isn't supplied. The error message names the field.
type ErrMissingRequiredArg struct{ Name string }

func (e ErrMissingRequiredArg) Error() string {
	return "mcp: missing required prompt argument: " + e.Name
}

// defaultPrompts returns the v1 prompt catalog. All four cover common
// agent workflows operators ask for in chat — pre-canned so MCP
// clients can offer them as one-click "slash commands".
func defaultPrompts() []Prompt {
	return []Prompt{
		{
			Name:        "diagnose-deploy",
			Description: "Diagnose a failed Midnight deployment.",
			Arguments: []PromptArg{
				{Name: "deployment_id", Description: "Deployment to investigate.", Required: true},
			},
			Template: `Investigate Midnight deployment ${deployment_id}.

Steps:
1. Call get_deployment with deployment_id=${deployment_id} to load the deployment metadata.
2. Call get_logs with deployment_id=${deployment_id} (lines=100) and surface any errors or panics in the tail.
3. If the build failed, look at the commit SHA and the failed stage; suggest the most likely fix.
4. Stop and report findings — DO NOT mutate anything.`,
		},
		{
			Name:        "rollback-suggestion",
			Description: "Pick the best deployment to roll back to for a service.",
			Arguments: []PromptArg{
				{Name: "service_id", Description: "Service that is in a bad state.", Required: true},
			},
			Template: `The Midnight service ${service_id} is in a bad state. Find a safe rollback target.

Steps:
1. Call get_service with service_id=${service_id} to load deployment history.
2. Identify the most recent deployment with Status="succeeded" and Route set.
3. Recommend that deployment id as the rollback target — explain why.
4. Stop and ask the operator to confirm before issuing rollback.`,
		},
		{
			Name:        "migrate-from-railway",
			Description: "Guide an operator through importing a Railway project into Midnight.",
			Arguments: []PromptArg{
				{Name: "railway_project_id", Description: "Optional Railway project id to import.", Required: false},
			},
			Template: `Help the operator migrate their Railway project ${railway_project_id} into Midnight.

Walkthrough:
1. Confirm the operator has exported a Railway environment dump (variables + service list).
2. For each Railway service, create the matching Midnight service via deploy_from_source
   (when AllowMutations is enabled in this session). If mutations are disabled, output the
   exact tool call the operator should make after toggling the flag.
3. Map Railway variable groups onto Midnight set_variable calls one-to-one.
4. Surface any Railway-specific features Midnight does not yet support (eg. private network DNS
   names, plugin types) so the operator can plan around them.`,
		},
		{
			Name:        "setup-database",
			Description: "Interactive template walkthrough for adding a managed database to a Midnight project.",
			Arguments: []PromptArg{
				{Name: "database", Description: `One of "postgres" or "redis".`, Required: true},
				{Name: "project_id", Description: "Project to attach the database to.", Required: true},
				{Name: "service_id_to_consume_url", Description: "Optional service to receive DATABASE_URL.", Required: false},
			},
			Template: `Provision a ${database} database in Midnight project ${project_id}.

Steps:
1. Call list_services with project_id=${project_id} to confirm the project exists and what is already deployed.
2. Pick the matching Midnight template (postgres-15 / redis-7) and deploy it via deploy_from_source.
3. Once the database service reports Status="running", capture its connection URL.
4. If service_id_to_consume_url is set (${service_id_to_consume_url}), call set_variable on that service
   with the database URL bound to the canonical env var (DATABASE_URL for postgres, REDIS_URL for redis)
   and then redeploy the consuming service.
5. Confirm with the operator before any mutation lands.`,
		},
	}
}

// renderPrompt resolves a prompt's template against args. Required
// args MUST be present; optional args default to the empty string.
// Substitution is a single `strings.NewReplacer` pass; no nesting,
// no escapes — the templates are static strings shipped by us.
func renderPrompt(p Prompt, args map[string]string) (string, error) {
	pairs := make([]string, 0, 2*len(p.Arguments))
	for _, a := range p.Arguments {
		v, ok := args[a.Name]
		if !ok || v == "" {
			if a.Required {
				return "", ErrMissingRequiredArg{Name: a.Name}
			}
			v = ""
		}
		pairs = append(pairs, "${"+a.Name+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(p.Template), nil
}

// promptCatalog wraps the slice with name-keyed lookup.
type promptCatalog struct {
	prompts []Prompt
	index   map[string]Prompt
}

func newPromptCatalog() *promptCatalog {
	ps := defaultPrompts()
	idx := make(map[string]Prompt, len(ps))
	for _, p := range ps {
		idx[p.Name] = p
	}
	return &promptCatalog{prompts: ps, index: idx}
}

func (c *promptCatalog) list() []Prompt { return c.prompts }

func (c *promptCatalog) get(name string, args map[string]string) (string, Prompt, error) {
	p, ok := c.index[name]
	if !ok {
		return "", Prompt{}, fmt.Errorf("%w: %s", ErrUnknownPrompt, name)
	}
	out, err := renderPrompt(p, args)
	return out, p, err
}
