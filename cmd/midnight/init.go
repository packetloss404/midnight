// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/cliconfig"
)

// initSlugPattern is the server's slug shape. `midnight init` derives a slug
// from the working directory, so it must sanitize before it proposes one.
var initSlugPattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

const initDefaultEnvironmentSlug = "production"

type initFlags struct {
	apiBase         *string
	token           *string
	name            *string
	workspaceID     *string
	projectSelect   *string
	environment     *string
	environmentName *string
	serviceSelect   *string
	serviceName     *string
	yes             *bool
}

func defineInitFlags(fs *flag.FlagSet) initFlags {
	return initFlags{
		apiBase:         cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		token:           fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
		name:            fs.String("name", "", "slug for the project to create when it does not exist"),
		workspaceID:     fs.String("workspace-id", "", "workspace that owns a newly created project"),
		projectSelect:   cliProjectIDFlag(fs, "existing project slug or id to use instead of creating one"),
		environment:     cliEnvironmentIDFlag(fs, "existing environment slug or id to link"),
		environmentName: fs.String("environment-name", initDefaultEnvironmentSlug, "slug for the environment to create when the project has none"),
		serviceSelect:   cliServiceIDFlag(fs, "existing service slug or id to link"),
		serviceName:     fs.String("service-name", "", "slug for the service to create (defaults to the directory name)"),
		yes:             fs.Bool("yes", false, "accept the proposed names without prompting"),
	}
}

// runInit is the one command that takes an unlinked directory to a deployable
// one: it selects or creates a project, guarantees an environment, selects or
// creates a service, and writes .midnight/project.json. It is interactive by
// default, fully flag-driven under --no-input, and safe to re-run: every step
// reuses what already exists.
func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineInitFlags(fs)
	if err := fs.Parse(args); err != nil {
		return cliUsageExitCode
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight init [--name SLUG] [--project-id SLUG_OR_ID] [--environment-id SLUG_OR_ID] [--service-name SLUG]")
		return cliUsageExitCode
	}
	options := cliOptionsFor(stdout)
	prompt := newCLIPrompter(stdout, cliStdinFor(stdout), options.NoInput || options.Yes || *f.yes)

	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		APIBase: *f.apiBase,
		Token:   *f.token,
	})
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: %s\n", err)
		if errors.Is(err, cliconfig.ErrNoAPIBase) {
			return cliUsageExitCode
		}
		return 1
	}
	client := &devAPIClient{base: credentials.APIBase, token: credentials.Token}
	api := &cliAPIClient{
		baseURL:        credentials.APIBase,
		token:          credentials.Token,
		credentialKind: credentials.Kind,
		client:         authHTTPClient,
	}

	project, code := initResolveProject(client, api, f, prompt, stdout, stderr)
	if code != 0 {
		return code
	}
	environment, code := initResolveEnvironment(client, api, project, f, stdout, stderr)
	if code != 0 {
		return code
	}
	service, code := initResolveService(client, api, project, f, prompt, stdout, stderr)
	if code != 0 {
		return code
	}

	link := cliconfig.ProjectLink{
		APIBase:         credentials.APIBase,
		ProjectID:       project.ID,
		ProjectSlug:     project.Slug,
		EnvironmentID:   environment.ID,
		EnvironmentName: firstNonEmptyString(environment.Slug, environment.Name),
		ServiceID:       service.ID,
		ServiceSlug:     service.Slug,
	}
	if err := cliconfig.SaveProjectLink("", link); err != nil {
		fmt.Fprintf(stderr, "midnight init: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight: linked %s/%s/%s -> .midnight/project.json\n",
		project.Slug, link.EnvironmentName, service.Slug)
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Next: midnight up")
	return 0
}

// initResolveProject returns the project to link, creating one when the
// caller named a project that does not exist yet.
func initResolveProject(
	client *devAPIClient,
	api *cliAPIClient,
	f initFlags,
	prompt *cliPrompter,
	stdout, stderr io.Writer,
) (cliProject, int) {
	projects, err := client.listProjects()
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: list projects: %s\n", err)
		return cliProject{}, 1
	}
	if selector := strings.TrimSpace(*f.projectSelect); selector != "" {
		for _, project := range projects {
			if project.ID == selector || project.Slug == selector {
				fmt.Fprintf(stdout, "midnight init: using project %s\n", project.Slug)
				return project, 0
			}
		}
		fmt.Fprintf(stderr, "midnight init: project %q not found\n", selector)
		return cliProject{}, 1
	}
	slug := strings.TrimSpace(*f.name)
	if slug == "" && len(projects) == 1 {
		fmt.Fprintf(stdout, "midnight init: using project %s (only one available)\n", projects[0].Slug)
		return projects[0], 0
	}
	if slug == "" && len(projects) > 1 {
		sort.SliceStable(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
		if !prompt.interactive() {
			fmt.Fprintln(stderr, "midnight init: --project-id or --name is required when several projects exist and input is disabled")
			return cliProject{}, cliUsageExitCode
		}
		fmt.Fprintln(stdout, "Select a project:")
		for index, project := range projects {
			fmt.Fprintf(stdout, "  [%d] %s  (%s)\n", index+1, project.Slug, project.ID)
		}
		index, err := prompt.choice(len(projects))
		if err != nil {
			fmt.Fprintf(stderr, "midnight init: %s\n", err)
			return cliProject{}, 1
		}
		return projects[index], 0
	}
	if slug == "" {
		proposed := initDefaultSlug()
		slug, err = prompt.text("Project slug", proposed)
		if err != nil {
			fmt.Fprintf(stderr, "midnight init: %s\n", err)
			return cliProject{}, cliUsageExitCode
		}
	}
	for _, project := range projects {
		if project.Slug == slug || project.ID == slug {
			fmt.Fprintf(stdout, "midnight init: project %s already exists\n", project.Slug)
			return project, 0
		}
	}
	if err := validateInitSlug("project", slug); err != nil {
		fmt.Fprintf(stderr, "midnight init: %s\n", err)
		return cliProject{}, cliUsageExitCode
	}
	workspaceID, code := initResolveWorkspace(client, f, stderr)
	if code != 0 {
		return cliProject{}, code
	}
	body := map[string]string{"workspace_id": workspaceID, "slug": slug, "display_name": slug}
	key, err := resolveMutationIdempotencyKey("", api.token, "init-project-create", http.MethodPost, "/api/v1alpha/projects", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: prepare retry-safe request: %s\n", err)
		return cliProject{}, cliUsageExitCode
	}
	var created cliProject
	if _, err := api.doJSON(http.MethodPost, "/api/v1alpha/projects", body, key, &created); err != nil {
		writeMutationFailure(stderr, "init project create", err, key)
		return cliProject{}, 1
	}
	fmt.Fprintf(stdout, "midnight init: created project %s (%s)\n", created.Slug, created.ID)
	return created, 0
}

func initResolveWorkspace(client *devAPIClient, f initFlags, stderr io.Writer) (string, int) {
	if workspaceID := strings.TrimSpace(*f.workspaceID); workspaceID != "" {
		return workspaceID, 0
	}
	var workspaces []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := client.getJSON("/api/v1alpha/workspaces", &workspaces); err != nil {
		fmt.Fprintf(stderr, "midnight init: list workspaces: %s; pass --workspace-id\n", err)
		return "", 1
	}
	if len(workspaces) == 0 {
		fmt.Fprintln(stderr, "midnight init: no workspace is available for this credential; pass --workspace-id")
		return "", 1
	}
	if len(workspaces) > 1 {
		fmt.Fprintln(stderr, "midnight init: several workspaces are available; pass --workspace-id to choose one")
		return "", cliUsageExitCode
	}
	return workspaces[0].ID, 0
}

// initResolveEnvironment reuses an existing environment when the project has
// one and creates the named environment only when the project has none.
func initResolveEnvironment(
	client *devAPIClient,
	api *cliAPIClient,
	project cliProject,
	f initFlags,
	stdout, stderr io.Writer,
) (cliEnvironment, int) {
	environments, err := client.listEnvironments(project.ID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: list environments: %s\n", err)
		return cliEnvironment{}, 1
	}
	if selector := strings.TrimSpace(*f.environment); selector != "" {
		for _, environment := range environments {
			if environment.ID == selector || environment.Slug == selector || environment.Name == selector {
				return environment, 0
			}
		}
		fmt.Fprintf(stderr, "midnight init: environment %q not found in project %s\n", selector, project.Slug)
		return cliEnvironment{}, 1
	}
	wanted := strings.TrimSpace(*f.environmentName)
	if wanted == "" {
		wanted = initDefaultEnvironmentSlug
	}
	for _, environment := range environments {
		if environment.Slug == wanted || environment.Name == wanted {
			return environment, 0
		}
	}
	if len(environments) == 1 {
		return environments[0], 0
	}
	if len(environments) > 1 {
		fmt.Fprintf(stderr, "midnight init: project %s has several environments; pass --environment-id\n", project.Slug)
		return cliEnvironment{}, cliUsageExitCode
	}
	if err := validateInitSlug("environment", wanted); err != nil {
		fmt.Fprintf(stderr, "midnight init: %s\n", err)
		return cliEnvironment{}, cliUsageExitCode
	}
	body := map[string]any{"project_id": project.ID, "slug": wanted, "name": wanted, "tier": "production"}
	key, err := resolveMutationIdempotencyKey("", api.token, "init-environment-create", http.MethodPost, "/api/v1alpha/environments", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: prepare retry-safe request: %s\n", err)
		return cliEnvironment{}, cliUsageExitCode
	}
	var created cliEnvironment
	if _, err := api.doJSON(http.MethodPost, "/api/v1alpha/environments", body, key, &created); err != nil {
		writeMutationFailure(stderr, "init environment create", err, key)
		return cliEnvironment{}, 1
	}
	fmt.Fprintf(stdout, "midnight init: created environment %s (%s)\n", created.Slug, created.ID)
	return created, 0
}

// initResolveService reuses a service with the wanted slug when one exists, so
// re-running init in the same directory is a no-op rather than a duplicate.
func initResolveService(
	client *devAPIClient,
	api *cliAPIClient,
	project cliProject,
	f initFlags,
	prompt *cliPrompter,
	stdout, stderr io.Writer,
) (cliService, int) {
	services, err := client.listServices(project.ID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: list services: %s\n", err)
		return cliService{}, 1
	}
	if selector := strings.TrimSpace(*f.serviceSelect); selector != "" {
		for _, service := range services {
			if service.ID == selector || service.Slug == selector {
				return service, 0
			}
		}
		fmt.Fprintf(stderr, "midnight init: service %q not found in project %s\n", selector, project.Slug)
		return cliService{}, 1
	}
	slug := strings.TrimSpace(*f.serviceName)
	if slug == "" {
		proposed := initDefaultSlug()
		slug, err = prompt.text("Service slug", proposed)
		if err != nil {
			fmt.Fprintf(stderr, "midnight init: %s\n", err)
			return cliService{}, cliUsageExitCode
		}
	}
	for _, service := range services {
		if service.Slug == slug || service.ID == slug {
			fmt.Fprintf(stdout, "midnight init: service %s already exists\n", service.Slug)
			return service, 0
		}
	}
	if err := validateInitSlug("service", slug); err != nil {
		fmt.Fprintf(stderr, "midnight init: %s\n", err)
		return cliService{}, cliUsageExitCode
	}
	body := map[string]string{"project_id": project.ID, "slug": slug, "display_name": slug}
	key, err := resolveMutationIdempotencyKey("", api.token, "init-service-create", http.MethodPost, "/api/v1alpha/services", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight init: prepare retry-safe request: %s\n", err)
		return cliService{}, cliUsageExitCode
	}
	var created cliService
	if _, err := api.doJSON(http.MethodPost, "/api/v1alpha/services", body, key, &created); err != nil {
		writeMutationFailure(stderr, "init service create", err, key)
		return cliService{}, 1
	}
	fmt.Fprintf(stdout, "midnight init: created service %s (%s)\n", created.Slug, created.ID)
	return created, 0
}

// initDefaultSlug proposes the working directory's name, sanitized to the
// server's slug shape.
func initDefaultSlug() string {
	working, err := os.Getwd()
	if err != nil {
		return "app"
	}
	return sanitizeInitSlug(filepath.Base(working))
}

func sanitizeInitSlug(value string) string {
	lowered := strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, current := range lowered {
		switch {
		case current >= 'a' && current <= 'z', current >= '0' && current <= '9':
			builder.WriteRune(current)
		case current == '-' || current == '_' || current == ' ' || current == '.':
			builder.WriteRune('-')
		}
	}
	slug := strings.Trim(builder.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if slug == "" || !(slug[0] >= 'a' && slug[0] <= 'z') {
		slug = "app-" + slug
		slug = strings.TrimRight(slug, "-")
	}
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

func validateInitSlug(kind, slug string) error {
	if !initSlugPattern.MatchString(slug) {
		return fmt.Errorf(
			"%s slug %q must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters",
			kind, slug,
		)
	}
	return nil
}

// cliPrompter reads confirmations and free text from the invocation's stdin.
// It refuses to prompt when input is disabled, so --no-input never blocks.
type cliPrompter struct {
	stdout   io.Writer
	scanner  *bufio.Scanner
	disabled bool
}

func newCLIPrompter(stdout io.Writer, stdin io.Reader, disabled bool) *cliPrompter {
	return &cliPrompter{stdout: stdout, scanner: bufio.NewScanner(stdin), disabled: disabled}
}

func (p *cliPrompter) interactive() bool { return !p.disabled }

// text returns the caller's answer, or fallback when input is disabled or the
// caller just pressed return.
func (p *cliPrompter) text(label, fallback string) (string, error) {
	if p.disabled {
		if fallback == "" {
			return "", fmt.Errorf("%s is required when input is disabled", strings.ToLower(label))
		}
		return fallback, nil
	}
	if fallback != "" {
		fmt.Fprintf(p.stdout, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(p.stdout, "%s: ", label)
	}
	if !p.scanner.Scan() {
		if fallback == "" {
			return "", fmt.Errorf("no value was read for %s", strings.ToLower(label))
		}
		return fallback, nil
	}
	answer := strings.TrimSpace(p.scanner.Text())
	if answer == "" {
		if fallback == "" {
			return "", fmt.Errorf("%s is required", strings.ToLower(label))
		}
		return fallback, nil
	}
	return answer, nil
}

func (p *cliPrompter) choice(max int) (int, error) {
	if p.disabled {
		return 0, fmt.Errorf("a selection is required, but input is disabled")
	}
	fmt.Fprintf(p.stdout, "Enter a number [1..%d]: ", max)
	if !p.scanner.Scan() {
		return 0, fmt.Errorf("no selection made")
	}
	line := strings.TrimSpace(p.scanner.Text())
	var index int
	if _, err := fmt.Sscanf(line, "%d", &index); err != nil {
		return 0, fmt.Errorf("invalid selection %q", line)
	}
	if index < 1 || index > max {
		return 0, fmt.Errorf("selection %d out of range", index)
	}
	return index - 1, nil
}
