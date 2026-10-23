// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/midnight/midnight/internal/cliconfig"
)

type cliGitHubRepositoryList struct {
	Items []cliGitHubRepository `json:"items"`
}

type cliGitHubRepository struct {
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryFullName string `json:"repository_full_name"`
	AccountLogin       string `json:"account_login,omitempty"`
	Private            bool   `json:"private"`
	DefaultBranch      string `json:"default_branch"`
	ResourceVersion    int64  `json:"resource_version,omitempty"`
}

type cliGitHubBranchList struct {
	Items []cliGitHubBranch `json:"items"`
}

type cliGitHubBranch struct {
	Name string `json:"name"`
}

type cliGitHubServiceLink struct {
	InstallationID             int64  `json:"installation_id"`
	RepositoryID               int64  `json:"repository_id"`
	RepositoryFullName         string `json:"repository_full_name"`
	Branch                     string `json:"branch"`
	BindingState               string `json:"binding_state"`
	RepositoryAccessVerifiedAt string `json:"repository_access_verified_at"`
	UpdatedAt                  string `json:"updated_at"`
}

type cliPutGitHubServiceLinkRequest struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Branch         string `json:"branch"`
}

type githubSourceFlags struct {
	api       *apiCommandConfig
	projectID string
}

func runServiceGitHub(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printServiceGitHubUsage(stderr)
		return 64
	}
	switch args[0] {
	case "repositories":
		return runServiceGitHubRepositories(args[1:], stdout, stderr)
	case "branches":
		return runServiceGitHubBranches(args[1:], stdout, stderr)
	case "get":
		return runServiceGitHubGet(args[1:], stdout, stderr)
	case "link":
		return runServiceGitHubLink(args[1:], stdout, stderr)
	case "unlink":
		return runServiceGitHubUnlink(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight service github: unknown subcommand %q\n", args[0])
		printServiceGitHubUsage(stderr)
		return 64
	}
}

func newGitHubSourceFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *githubSourceFlags) {
	fs, api := newAPIFlagSet(name, stderr)
	flags := &githubSourceFlags{api: api}
	cliStringVar(fs, &flags.projectID, flagProjectID, "", "project ID (defaults to the linked project)", "project")
	return fs, flags
}

// githubSourceCommandFlags builds the help/lookup copy of a GitHub source
// command's FlagSet through the very constructor the handler uses, so the two
// cannot drift.
func githubSourceCommandFlags(name string, extra func(*flag.FlagSet)) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs, _ := newGitHubSourceFlagSet(name, io.Discard)
		if extra != nil {
			extra(fs)
		}
		return fs
	}
}

// defineGitHubRepositoryIDFlag is the one definition of --repository-id.
func defineGitHubRepositoryIDFlag(fs *flag.FlagSet) *int64 {
	return fs.Int64("repository-id", 0, "numeric GitHub repository ID")
}

type serviceGitHubLinkFlags struct {
	installationID *int64
	repositoryID   *int64
	branch         *string
	key            *string
}

func defineServiceGitHubLinkFlags(fs *flag.FlagSet) serviceGitHubLinkFlags {
	return serviceGitHubLinkFlags{
		installationID: fs.Int64("installation-id", 0, "numeric GitHub App installation ID"),
		repositoryID:   defineGitHubRepositoryIDFlag(fs),
		branch:         fs.String("branch", "", "GitHub branch name, for example main"),
		key:            cliIdempotencyKeyFlag(fs, "stable key to reuse when retrying the same link request"),
	}
}

type serviceGitHubUnlinkFlags struct {
	key *string
}

func defineServiceGitHubUnlinkFlags(fs *flag.FlagSet) serviceGitHubUnlinkFlags {
	return serviceGitHubUnlinkFlags{
		key: cliIdempotencyKeyFlag(fs, "stable key to reuse when retrying the same unlink request"),
	}
}

func (f *githubSourceFlags) resolveProjectClient() (*cliAPIClient, string, error) {
	projectID := strings.TrimSpace(f.projectID)
	if projectID == "" {
		link, err := cliconfig.LoadProjectLink("")
		if err != nil {
			return nil, "", fmt.Errorf("--project-id is required when the current directory is not linked")
		}
		projectID = strings.TrimSpace(link.ProjectID)
	}
	if projectID == "" {
		return nil, "", fmt.Errorf("--project-id is required when the current directory is not linked")
	}
	client, err := resolveHumanGitHubClient(f.api)
	if err != nil {
		return nil, "", err
	}
	return client, projectID, nil
}

func resolveHumanGitHubClient(cfg *apiCommandConfig) (*cliAPIClient, error) {
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	if client.credentialKind != cliconfig.CredentialKindHumanSession {
		return nil, fmt.Errorf("GitHub source linking requires a human session; run `midnight login`")
	}
	return client, nil
}

func runServiceGitHubRepositories(args []string, stdout, stderr io.Writer) int {
	fs, flags := newGitHubSourceFlagSet("midnight service github repositories", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight service github repositories [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
		return 64
	}
	client, projectID, err := flags.resolveProjectClient()
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github repositories: %s\n", err)
		return 1
	}
	var result cliGitHubRepositoryList
	path := projectGitHubRepositoriesAPIPath(projectID)
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github repositories: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, flags.api.JSON, raw); done {
		return code
	}
	writeGitHubRepositoryTable(stdout, result.Items)
	return 0
}

func runServiceGitHubBranches(args []string, stdout, stderr io.Writer) int {
	fs, flags := newGitHubSourceFlagSet("midnight service github branches", stderr)
	repositoryID := defineGitHubRepositoryIDFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 || *repositoryID <= 0 {
		fmt.Fprintln(stderr, "usage: midnight service github branches --repository-id ID [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
		return 64
	}
	client, projectID, err := flags.resolveProjectClient()
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github branches: %s\n", err)
		return 1
	}
	var result cliGitHubBranchList
	path := projectGitHubRepositoriesAPIPath(projectID) + "/" + url.PathEscape(fmt.Sprintf("%d", *repositoryID)) + "/branches"
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github branches: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, flags.api.JSON, raw); done {
		return code
	}
	writeGitHubBranchTable(stdout, result.Items)
	return 0
}

func runServiceGitHubGet(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service github get", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	serviceID, err := resolveGitHubServiceID(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, "usage: midnight service github get [--api-base URL] [--token TOKEN] [--json] [<service-id>]")
		return 64
	}
	client, err := resolveHumanGitHubClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github get: %s\n", err)
		return 1
	}
	var link cliGitHubServiceLink
	raw, err := client.doJSON(http.MethodGet, serviceGitHubAPIPath(serviceID), nil, "", &link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github get: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeGitHubServiceLink(stdout, serviceID, link)
	return 0
}

func runServiceGitHubLink(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service github link", stderr)
	f := defineServiceGitHubLinkFlags(fs)
	installationID, repositoryID, branch, key := f.installationID, f.repositoryID, f.branch, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	serviceID, err := resolveGitHubServiceID(fs.Args())
	canonicalBranch := strings.TrimSpace(*branch)
	if err != nil ||
		*installationID <= 0 ||
		*repositoryID <= 0 ||
		*branch != canonicalBranch ||
		!validGitHubBranch(canonicalBranch) ||
		(*key != "" && !validGitHubIdempotencyKey(*key)) {
		fmt.Fprintln(stderr, "usage: midnight service github link --installation-id ID --repository-id ID --branch BRANCH [--idempotency-key KEY] [--api-base URL] [--token TOKEN] [--json] [<service-id>]")
		return 64
	}
	client, err := resolveHumanGitHubClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github link: %s\n", err)
		return 1
	}
	request := cliPutGitHubServiceLinkRequest{
		InstallationID: *installationID,
		RepositoryID:   *repositoryID,
		Branch:         canonicalBranch,
	}
	path := serviceGitHubAPIPath(serviceID)
	resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "service-github-link", http.MethodPut, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github link: resolve idempotency key: %s\n", err)
		return 64
	}
	var link cliGitHubServiceLink
	raw, err := client.doJSON(http.MethodPut, path, request, resolvedKey, &link)
	if err != nil {
		writeMutationFailure(stderr, "service github link", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "GitHub source linked: %s -> %s@%s\n",
		terminalSafeText(serviceID),
		terminalSafeText(link.RepositoryFullName),
		terminalSafeText(link.Branch),
	)
	return 0
}

func runServiceGitHubUnlink(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service github unlink", stderr)
	key := defineServiceGitHubUnlinkFlags(fs).key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	serviceID, err := resolveGitHubServiceID(fs.Args())
	if err != nil || (*key != "" && !validGitHubIdempotencyKey(*key)) {
		fmt.Fprintln(stderr, "usage: midnight service github unlink [--idempotency-key KEY] [--api-base URL] [--token TOKEN] [--json] [<service-id>]")
		return 64
	}
	client, err := resolveHumanGitHubClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github unlink: %s\n", err)
		return 1
	}
	path := serviceGitHubAPIPath(serviceID)
	resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "service-github-unlink", http.MethodDelete, path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service github unlink: resolve idempotency key: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodDelete, path, nil, resolvedKey, nil); err != nil {
		writeMutationFailure(stderr, "service github unlink", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, cliDeletedDocument("service_id", serviceID)); done {
		return code
	}
	fmt.Fprintf(stdout, "GitHub source unlinked: %s\n", terminalSafeText(serviceID))
	return 0
}

func resolveGitHubServiceID(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("at most one service id is accepted")
	}
	if len(args) == 1 {
		serviceID := strings.TrimSpace(args[0])
		if serviceID == "" {
			return "", fmt.Errorf("service id is required")
		}
		return serviceID, nil
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		return "", fmt.Errorf("service id is required when the current directory is not linked")
	}
	serviceID := strings.TrimSpace(link.ServiceID)
	if serviceID == "" {
		return "", fmt.Errorf("service id is required when the current directory is not linked")
	}
	return serviceID, nil
}

func validGitHubBranch(branch string) bool {
	return branch != "" &&
		len(branch) <= 255 &&
		!strings.HasPrefix(branch, "refs/") &&
		strings.IndexFunc(branch, func(r rune) bool {
			return r <= 0x20 || r == 0x7f
		}) < 0
}

func validGitHubIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 255 {
		return false
	}
	return strings.IndexFunc(key, func(r rune) bool {
		return r <= ' ' || r > '~' || r == ','
	}) < 0
}

func projectGitHubRepositoriesAPIPath(projectID string) string {
	return "/api/v1alpha/projects/" + url.PathEscape(strings.TrimSpace(projectID)) + "/github/repositories"
}

func serviceGitHubAPIPath(serviceID string) string {
	return "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(serviceID)) + "/github"
}

func writeGitHubRepositoryTable(w io.Writer, repositories []cliGitHubRepository) {
	if len(repositories) == 0 {
		fmt.Fprintln(w, "No GitHub repositories are granted to this project.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "INSTALLATION\tREPOSITORY\tNAME\tDEFAULT BRANCH\tVISIBILITY")
	for _, repository := range repositories {
		visibility := "public"
		if repository.Private {
			visibility = "private"
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\n",
			repository.InstallationID,
			repository.RepositoryID,
			terminalSafeText(repository.RepositoryFullName),
			terminalSafeText(repository.DefaultBranch),
			visibility,
		)
	}
	_ = tw.Flush()
}

func writeGitHubBranchTable(w io.Writer, branches []cliGitHubBranch) {
	if len(branches) == 0 {
		fmt.Fprintln(w, "No GitHub branches are visible for this repository.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BRANCH")
	for _, branch := range branches {
		fmt.Fprintln(tw, terminalSafeText(branch.Name))
	}
	_ = tw.Flush()
}

func writeGitHubServiceLink(w io.Writer, serviceID string, link cliGitHubServiceLink) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "SERVICE\t%s\n", terminalSafeText(serviceID))
	fmt.Fprintf(tw, "INSTALLATION\t%d\n", link.InstallationID)
	fmt.Fprintf(tw, "REPOSITORY\t%d (%s)\n", link.RepositoryID, terminalSafeText(link.RepositoryFullName))
	fmt.Fprintf(tw, "BRANCH\t%s\n", terminalSafeText(link.Branch))
	fmt.Fprintf(tw, "STATE\t%s\n", terminalSafeText(link.BindingState))
	fmt.Fprintf(tw, "ACCESS VERIFIED\t%s\n", terminalSafeText(link.RepositoryAccessVerifiedAt))
	fmt.Fprintf(tw, "UPDATED\t%s\n", terminalSafeText(link.UpdatedAt))
	_ = tw.Flush()
}

func printServiceGitHubUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight service github repositories [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight service github branches --repository-id ID [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight service github get [--api-base URL] [--token TOKEN] [--json] [<service-id>]")
	fmt.Fprintln(w, "  midnight service github link --installation-id ID --repository-id ID --branch BRANCH [--idempotency-key KEY] [<service-id>]")
	fmt.Fprintln(w, "  midnight service github unlink [--idempotency-key KEY] [--json] [<service-id>]")
}
