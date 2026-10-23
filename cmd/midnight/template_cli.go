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
)

type cliTemplateSummary struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

type cliTemplateDeployResponse struct {
	OperationID     string `json:"operation_id"`
	ProvisionRunID  string `json:"provision_run_id"`
	TemplateSlug    string `json:"template_slug"`
	State           string `json:"state"`
	EnvironmentID   string `json:"environment_id"`
	DeploymentID    string `json:"deployment_id"`
	StatusURL       string `json:"status_url"`
	CreatedServices []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"created_services"`
}

type cliTemplateProvisionRun struct {
	ID                   string `json:"id"`
	TemplateSlug         string `json:"template_slug"`
	ProjectID            string `json:"project_id"`
	EnvironmentID        string `json:"environment_id"`
	ServiceID            string `json:"service_id"`
	VolumeID             string `json:"volume_id"`
	OperationID          string `json:"operation_id"`
	DeploymentID         string `json:"deployment_id"`
	State                string `json:"state"`
	ConnectivityVerified bool   `json:"connectivity_verified"`
	PersistenceVerified  bool   `json:"persistence_verified"`
	CleanupCompleted     bool   `json:"cleanup_completed"`
	FailureCode          string `json:"failure_code"`
	FailureReason        string `json:"failure_reason"`
}

func runTemplate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTemplateUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runTemplateList(args[1:], stdout, stderr)
	case "deploy":
		return runTemplateDeploy(args[1:], stdout, stderr)
	case "status":
		return runTemplateStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight template: unknown subcommand %q\n", args[0])
		printTemplateUsage(stderr)
		return 64
	}
}

func runTemplateList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight template list", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template list: %s\n", err)
		return 64
	}
	var response struct {
		Templates []cliTemplateSummary `json:"templates"`
	}
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/templates", nil, "", &response)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template list: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tNAME\tVERSION\tTAGS")
	for _, item := range response.Templates {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.Slug, item.Name, item.Version, strings.Join(item.Tags, ","))
	}
	_ = tw.Flush()
	return 0
}

func runTemplateDeploy(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight template deploy", stderr)
	f := defineTemplateDeployFlags(fs)
	projectID, environmentID, name, key := f.projectID, f.environmentID, f.name, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*projectID) == "" {
		printTemplateUsage(stderr)
		return 64
	}
	slug := strings.TrimSpace(fs.Arg(0))
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template deploy: %s\n", err)
		return 64
	}
	body := map[string]string{"project_id": strings.TrimSpace(*projectID)}
	if value := strings.TrimSpace(*environmentID); value != "" {
		body["environment_id"] = value
	}
	if value := strings.TrimSpace(*name); value != "" {
		body["name"] = value
	}
	path := "/api/v1alpha/templates/" + url.PathEscape(slug) + "/instances"
	attempt, err := beginPendingMutationAttempt(*key, client.token, "template-deploy", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template deploy: prepare retry-safe attempt: %s\n", err)
		return 64
	}
	var response cliTemplateDeployResponse
	raw, err := client.doJSON(http.MethodPost, path, body, attempt.Key, &response)
	if finishErr := attempt.Finish(err); finishErr != nil {
		fmt.Fprintf(stderr, "midnight template deploy: %s\n", finishErr)
		return 1
	}
	if err != nil {
		writeMutationFailure(stderr, "template deploy", err, attempt.Key)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "Template %s queued\n", response.TemplateSlug)
	fmt.Fprintf(stdout, "Run: %s\nOperation: %s\nDeployment: %s\nEnvironment: %s\n",
		response.ProvisionRunID, response.OperationID, response.DeploymentID, response.EnvironmentID)
	fmt.Fprintf(stdout, "Status: midnight template status --slug %s %s\n",
		response.TemplateSlug, response.ProvisionRunID)
	fmt.Fprintln(stdout, "Success remains pending until connectivity and container-replacement persistence checks pass.")
	return 0
}

func runTemplateStatus(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight template status", stderr)
	f := defineTemplateStatusFlags(fs)
	slug := f.slug
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		printTemplateUsage(stderr)
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template status: %s\n", err)
		return 64
	}
	var run cliTemplateProvisionRun
	raw, err := client.doJSON(http.MethodGet,
		"/api/v1alpha/templates/"+url.PathEscape(strings.TrimSpace(*slug))+
			"/instances/"+url.PathEscape(strings.TrimSpace(fs.Arg(0))),
		nil, "", &run)
	if err != nil {
		fmt.Fprintf(stderr, "midnight template status: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "Template: %s\nState: %s\nRun: %s\nOperation: %s\nDeployment: %s\n",
		run.TemplateSlug, run.State, run.ID, run.OperationID, run.DeploymentID)
	fmt.Fprintf(stdout, "Connectivity verified: %t\nPersistence verified: %t\nProbe cleanup completed: %t\n",
		run.ConnectivityVerified, run.PersistenceVerified, run.CleanupCompleted)
	if run.FailureCode != "" {
		fmt.Fprintf(stdout, "Failure: %s %s\n", run.FailureCode, run.FailureReason)
	}
	return 0
}

func printTemplateUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight template list [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight template deploy --project-id ID [--environment-id ID] [--name NAME] [--idempotency-key KEY] <postgres|redis|mysql|mongodb>")
	fmt.Fprintln(w, "  midnight template status [--slug SLUG] [--json] <provision-run-id>  (default slug: postgres)")
}

type templateDeployFlags struct {
	projectID     *string
	environmentID *string
	name          *string
	key           *string
}

func defineTemplateDeployFlags(fs *flag.FlagSet) templateDeployFlags {
	return templateDeployFlags{
		projectID:     cliProjectIDFlag(fs, "destination project id"),
		environmentID: cliEnvironmentIDFlag(fs, "destination environment id (defaults to production)"),
		name:          fs.String("name", "", "database service display name"),
		key:           cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

type templateStatusFlags struct {
	slug *string
}

func defineTemplateStatusFlags(fs *flag.FlagSet) templateStatusFlags {
	return templateStatusFlags{
		slug: fs.String("slug", "postgres", "template slug"),
	}
}
