// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/cliconfig"
)

type cliEnvironment struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	Tier            string `json:"tier,omitempty"`
	Protected       bool   `json:"is_protected,omitempty"`
	ResourceVersion int64  `json:"resource_version"`
}

func runLink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineLinkFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		APIBase: *f.apiBase,
		Token:   *f.explicitToken,
	})
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: %s\n", err)
		if errors.Is(err, cliconfig.ErrNoAPIBase) {
			return 64
		}
		return 1
	}
	base := credentials.APIBase
	token := credentials.Token
	client := &devAPIClient{base: base, token: token}
	projects, err := client.listProjects()
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: list projects: %s\n", err)
		return 1
	}
	project, err := pickProject(projects, *f.projectSelect, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: %s\n", err)
		return 1
	}
	environments, err := client.listEnvironments(project.ID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: list environments: %s\n", err)
		return 1
	}
	environment, err := pickEnvironment(environments, *f.envSelect, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: %s\n", err)
		return 1
	}
	services, err := client.listServices(project.ID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: list services: %s\n", err)
		return 1
	}
	service, err := pickService(services, *f.serviceSelect, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "midnight link: %s\n", err)
		return 1
	}

	link := cliconfig.ProjectLink{
		APIBase:         base,
		ProjectID:       project.ID,
		ProjectSlug:     project.Slug,
		EnvironmentID:   environment.ID,
		EnvironmentName: firstNonEmptyString(environment.Slug, environment.Name),
		ServiceID:       service.ID,
		ServiceSlug:     service.Slug,
	}
	if err := cliconfig.SaveProjectLink("", link); err != nil {
		fmt.Fprintf(stderr, "midnight link: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight: linked %s/%s/%s -> .midnight/project.json\n", project.Slug, link.EnvironmentName, service.Slug)
	return 0
}

type linkFlags struct {
	apiBase       *string
	explicitToken *string
	projectSelect *string
	envSelect     *string
	serviceSelect *string
}

func defineLinkFlags(fs *flag.FlagSet) linkFlags {
	return linkFlags{
		apiBase:       cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		explicitToken: fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
		projectSelect: cliProjectIDFlag(fs, "project slug or id"),
		envSelect:     cliEnvironmentIDFlag(fs, "environment slug or id"),
		serviceSelect: cliServiceIDFlag(fs, "service slug or id"),
	}
}

func runUnlink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight unlink", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight unlink")
		return 64
	}
	if err := cliconfig.DeleteProjectLink(""); err != nil {
		fmt.Fprintf(stderr, "midnight unlink: %s\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "midnight: removed .midnight/project.json")
	return 0
}

// devAPIClient is a thin REST helper used by the developer commands. It is
// a sibling of cliAPIClient (in product_cli.go) but uses cliconfig-resolved
// tokens and treats every response as JSON.
type devAPIClient struct {
	base   string
	token  string
	client *http.Client
}

func (c *devAPIClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return authHTTPClient
}

func (c *devAPIClient) listProjects() ([]cliProject, error) {
	var projects []cliProject
	if err := c.getJSON("/api/v1alpha/projects", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *devAPIClient) listServices(projectID string) ([]cliService, error) {
	path := "/api/v1alpha/services"
	if strings.TrimSpace(projectID) != "" {
		path += "?project_id=" + url.QueryEscape(projectID)
	}
	var services []cliService
	if err := c.getJSON(path, &services); err != nil {
		return nil, err
	}
	return services, nil
}

func (c *devAPIClient) listEnvironments(projectID string) ([]cliEnvironment, error) {
	path := "/api/v1alpha/environments"
	if strings.TrimSpace(projectID) != "" {
		path += "?project_id=" + url.QueryEscape(projectID)
	}
	var envs []cliEnvironment
	if err := c.getJSON(path, &envs); err != nil {
		return nil, err
	}
	return envs, nil
}

func (c *devAPIClient) getJSON(path string, out any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.token))
	}
	req.Header.Set("Accept", "application/json")
	res, body, err := executeCLIRequest(c.httpClient(), req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodGet, path, res.Status, body)
	}
	return jsonDecode(body, out)
}

func pickProject(projects []cliProject, want string, stdout, stderr io.Writer) (cliProject, error) {
	if len(projects) == 0 {
		return cliProject{}, fmt.Errorf("no projects exist; create one with `midnight project create`")
	}
	want = strings.TrimSpace(want)
	if want != "" {
		for _, project := range projects {
			if project.ID == want || project.Slug == want {
				return project, nil
			}
		}
		return cliProject{}, fmt.Errorf("project %q not found", want)
	}
	if len(projects) == 1 {
		fmt.Fprintf(stdout, "midnight link: project %s (only one available)\n", projects[0].Slug)
		return projects[0], nil
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
	fmt.Fprintln(stdout, "Select a project:")
	for i, project := range projects {
		fmt.Fprintf(stdout, "  [%d] %s  (%s)\n", i+1, project.Slug, project.ID)
	}
	idx, err := readChoice(stdout, len(projects))
	if err != nil {
		return cliProject{}, err
	}
	return projects[idx], nil
}

func pickService(services []cliService, want string, stdout, stderr io.Writer) (cliService, error) {
	if len(services) == 0 {
		return cliService{}, fmt.Errorf("no services exist in this project; create one with `midnight service create`")
	}
	want = strings.TrimSpace(want)
	if want != "" {
		for _, service := range services {
			if service.ID == want || service.Slug == want {
				return service, nil
			}
		}
		return cliService{}, fmt.Errorf("service %q not found", want)
	}
	if len(services) == 1 {
		fmt.Fprintf(stdout, "midnight link: service %s (only one available)\n", services[0].Slug)
		return services[0], nil
	}
	sort.SliceStable(services, func(i, j int) bool { return services[i].Slug < services[j].Slug })
	fmt.Fprintln(stdout, "Select a service:")
	for i, service := range services {
		fmt.Fprintf(stdout, "  [%d] %s  (%s)\n", i+1, service.Slug, service.ID)
	}
	idx, err := readChoice(stdout, len(services))
	if err != nil {
		return cliService{}, err
	}
	return services[idx], nil
}

func pickEnvironment(envs []cliEnvironment, want string, stdout, stderr io.Writer) (cliEnvironment, error) {
	if len(envs) == 0 {
		return cliEnvironment{}, fmt.Errorf("no environments exist")
	}
	want = strings.TrimSpace(want)
	if want != "" {
		for _, env := range envs {
			if env.ID == want || env.Slug == want || env.Name == want {
				return env, nil
			}
		}
		return cliEnvironment{}, fmt.Errorf("environment %q not found", want)
	}
	if len(envs) == 1 {
		return envs[0], nil
	}
	sort.SliceStable(envs, func(i, j int) bool { return envs[i].Slug < envs[j].Slug })
	fmt.Fprintln(stdout, "Select an environment:")
	for i, env := range envs {
		fmt.Fprintf(stdout, "  [%d] %s\n", i+1, firstNonEmptyString(env.Slug, env.Name, env.ID))
	}
	idx, err := readChoice(stdout, len(envs))
	if err != nil {
		return cliEnvironment{}, err
	}
	return envs[idx], nil
}

func readChoice(stdout io.Writer, max int) (int, error) {
	fmt.Fprintf(stdout, "Enter a number [1..%d]: ", max)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return 0, fmt.Errorf("no selection made")
	}
	line := strings.TrimSpace(scanner.Text())
	var idx int
	if _, err := fmt.Sscanf(line, "%d", &idx); err != nil {
		return 0, fmt.Errorf("invalid selection %q", line)
	}
	if idx < 1 || idx > max {
		return 0, fmt.Errorf("selection %d out of range", idx)
	}
	return idx - 1, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// jsonDecode wraps encoding/json so the helper can sit in one file without
// pulling in encoding/json everywhere a dev command lives.
func jsonDecode(data []byte, out any) error {
	if out == nil {
		return nil
	}
	return decodeJSON(data, out)
}
