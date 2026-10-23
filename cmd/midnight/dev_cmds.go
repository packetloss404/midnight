// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
)

// decodeJSON is the package-private json.Unmarshal shim used by the dev
// commands so a single import path covers both the operator and developer
// command files.
func decodeJSON(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

// cliStatusRelease is one rollout as `midnight status` reports it.
type cliStatusRelease struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	State        string `json:"state,omitempty"`
	Status       string `json:"status,omitempty"`
	Code         string `json:"code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	At           string `json:"at,omitempty"`
}

// cliStatusReport is the whole answer to "what is running here right now".
type cliStatusReport struct {
	APIBase         string            `json:"api_base"`
	ProjectID       string            `json:"project_id"`
	ProjectSlug     string            `json:"project_slug"`
	EnvironmentID   string            `json:"environment_id"`
	EnvironmentName string            `json:"environment_name"`
	ServiceID       string            `json:"service_id"`
	ServiceSlug     string            `json:"service_slug"`
	Serving         *cliStatusRelease `json:"serving,omitempty"`
	InFlight        *cliStatusRelease `json:"in_flight,omitempty"`
	LastFailure     *cliStatusRelease `json:"last_failure,omitempty"`
	// Note explains why the rollout fields are empty when the server could
	// not be read; the link context above is still exact.
	Note string `json:"note,omitempty"`
}

type cliOperationDetail struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineStatusFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight status [--output table|json]")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight status: %s\n", err)
		return cliConfigExitCode(err)
	}
	report := cliStatusReport{
		APIBase:         link.APIBase,
		ProjectID:       link.ProjectID,
		ProjectSlug:     link.ProjectSlug,
		EnvironmentID:   link.EnvironmentID,
		EnvironmentName: link.EnvironmentName,
		ServiceID:       link.ServiceID,
		ServiceSlug:     link.ServiceSlug,
	}
	readErr := populateCLIStatusRollouts(&report, link)
	if readErr != nil {
		report.Note = readErr.Error()
	}
	raw, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		fmt.Fprintf(stderr, "midnight status: %s\n", marshalErr)
		return 1
	}
	code := renderCLISingle(stdout, stderr, *f.asJSON, raw, report,
		func(value cliStatusReport) string { return value.ServiceID },
		writeCLIStatusReport,
	)
	if code != 0 {
		return code
	}
	if readErr != nil {
		fmt.Fprintf(stderr, "midnight status: %s\n", readErr)
		return 1
	}
	return 0
}

type statusFlags struct {
	asJSON *bool
}

func defineStatusFlags(fs *flag.FlagSet) statusFlags {
	return statusFlags{
		asJSON: fs.Bool("json", false, "alias for --output json"),
	}
}

// populateCLIStatusRollouts fills in the serving release, the in-flight
// rollout, and the most recent failure. The deployment list is ordered newest
// first, so the first match in each category is the current one.
func populateCLIStatusRollouts(report *cliStatusReport, link cliconfig.ProjectLink) error {
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		return err
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{ProjectAPIBase: link.APIBase})
	if err != nil {
		return err
	}
	report.APIBase = credentials.APIBase
	deployments, err := listServiceDeployments(credentials.APIBase, credentials.Token, link.ServiceID, environmentID)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		release := statusReleaseFrom(deployment)
		state := strings.ToUpper(strings.TrimSpace(deployment.OperationState))
		switch {
		case report.InFlight == nil && (state == "QUEUED" || state == "RUNNING"):
			report.InFlight = release
		case report.Serving == nil && deployment.isActive():
			report.Serving = release
		case report.LastFailure == nil && (state == "FAILED" || state == "CANCELED"):
			report.LastFailure = release
		}
	}
	if report.LastFailure != nil && strings.TrimSpace(report.LastFailure.OperationID) != "" {
		annotateStatusFailure(credentials.APIBase, credentials.Token, report.LastFailure)
	}
	return nil
}

func statusReleaseFrom(deployment cliDeployment) *cliStatusRelease {
	release := &cliStatusRelease{
		DeploymentID: deployment.canonicalID(),
		OperationID:  strings.TrimSpace(deployment.OperationID),
		State:        strings.ToUpper(strings.TrimSpace(deployment.OperationState)),
		Status:       strings.TrimSpace(deployment.Status),
	}
	if !deployment.LastEventAt.IsZero() {
		release.At = deployment.LastEventAt.UTC().Format(time.RFC3339)
	}
	return release
}

// annotateStatusFailure adds the operation's failure code and reason. A
// failure to read the operation is not itself an error: the rollout state is
// already known and worth printing.
func annotateStatusFailure(apiBase, token string, release *cliStatusRelease) {
	client := &devAPIClient{base: apiBase, token: token}
	var operation cliOperationDetail
	if err := client.getJSON("/api/v1alpha/operations/"+url.PathEscape(release.OperationID), &operation); err != nil {
		return
	}
	release.Code = strings.TrimSpace(operation.Code)
	release.Reason = strings.TrimSpace(operation.Reason)
}

func writeCLIStatusReport(stdout io.Writer, report cliStatusReport) {
	fmt.Fprintf(stdout, "api base     : %s\n", report.APIBase)
	fmt.Fprintf(stdout, "project      : %s (%s)\n", report.ProjectSlug, report.ProjectID)
	fmt.Fprintf(stdout, "environment  : %s (%s)\n", report.EnvironmentName, report.EnvironmentID)
	fmt.Fprintf(stdout, "service      : %s (%s)\n", report.ServiceSlug, report.ServiceID)
	fmt.Fprintf(stdout, "serving      : %s\n", describeStatusRelease(report.Serving, "no active deployment"))
	fmt.Fprintf(stdout, "in flight    : %s\n", describeStatusRelease(report.InFlight, "none"))
	if report.LastFailure != nil {
		fmt.Fprintf(stdout, "last failure : %s\n", describeStatusRelease(report.LastFailure, ""))
	}
	if strings.TrimSpace(report.Note) != "" {
		fmt.Fprintf(stdout, "note         : %s\n", report.Note)
	}
}

func describeStatusRelease(release *cliStatusRelease, empty string) string {
	if release == nil {
		return empty
	}
	parts := make([]string, 0, 4)
	if release.DeploymentID != "" {
		parts = append(parts, release.DeploymentID)
	}
	if release.OperationID != "" {
		parts = append(parts, "operation "+release.OperationID)
	}
	if release.State != "" {
		parts = append(parts, release.State)
	}
	if release.At != "" {
		parts = append(parts, release.At)
	}
	summary := strings.Join(parts, "  ")
	if release.Code != "" || release.Reason != "" {
		summary += "\n               " + strings.TrimSpace(release.Code+": "+release.Reason)
	}
	if summary == "" {
		return empty
	}
	return summary
}

func runOpen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineOpenFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight open [--print]")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight open: %s\n", err)
		return cliConfigExitCode(err)
	}
	target := strings.TrimRight(link.APIBase, "/") + "/dashboard/#/projects/" + link.ProjectID
	if link.ServiceID != "" {
		target += "/services/" + link.ServiceID
	}
	if *f.printOnly {
		fmt.Fprintln(stdout, target)
		return 0
	}
	fmt.Fprintf(stdout, "midnight: opening %s\n", target)
	if err := openExternal(target); err != nil {
		fmt.Fprintf(stderr, "midnight open: %s\n", err)
		return 1
	}
	return 0
}

type openFlags struct {
	printOnly *bool
}

func defineOpenFlags(fs *flag.FlagSet) openFlags {
	return openFlags{
		printOnly: fs.Bool("print", false, "print the console URL instead of opening a browser"),
	}
}

func runLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineLogsFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight logs: %s\n", err)
		return cliConfigExitCode(err)
	}
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight logs: %s\n", err)
		return 1
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		ProjectAPIBase: link.APIBase,
	})
	if err != nil {
		fmt.Fprintf(stderr, "midnight logs: %s\n", err)
		return cliConfigExitCode(err)
	}
	opID := strings.TrimSpace(*f.operationID)
	if opID == "" {
		var err error
		opID, err = latestDeployOperationID(credentials.APIBase, credentials.Token, link.ServiceID, environmentID)
		if err != nil {
			fmt.Fprintf(stderr, "midnight logs: %s\n", err)
			return 1
		}
	}
	if *f.follow {
		if *f.build {
			return streamOperationEventsForCommand(
				credentials.APIBase,
				credentials.Token,
				opID,
				"midnight logs",
				"operation",
				stdout,
				stderr,
			)
		}
		if credential := classifyTenantScopedCLICredential(credentials.Token); credential != "" {
			fmt.Fprintf(
				stderr,
				"midnight logs: %s credentials cannot stream logs from the running container yet; use --build to follow build and deploy events instead\n",
				credential,
			)
			return 1
		}
		deploymentID, err := deploymentIDForOperation(credentials.APIBase, credentials.Token, link.ServiceID, environmentID, opID)
		if err != nil {
			fmt.Fprintf(stderr, "midnight logs: %s\n", err)
			return 1
		}
		values := scopedObservabilityValues(
			link.ProjectID, environmentID, link.ServiceID, deploymentID, "",
		)
		if *f.tail > 0 {
			values.Set("limit", fmt.Sprintf("%d", *f.tail))
		}
		client := &cliAPIClient{baseURL: credentials.APIBase, token: credentials.Token, client: authHTTPClient}
		return streamObservabilityLogs(
			context.Background(),
			client,
			"/api/v1alpha/observability/logs/stream?"+values.Encode(),
			false,
			stdout,
			stderr,
		)
	}
	path := "/api/v1alpha/operations/" + opID + "/logs"
	if *f.tail > 0 {
		path += "?limit=" + fmt.Sprintf("%d", *f.tail)
	}
	if *f.build {
		// no-op for now; the operations/{id}/logs endpoint always includes
		// the build/operation events surface alongside runtime surfaces.
		_ = f.build
	}
	client := &devAPIClient{base: credentials.APIBase, token: credentials.Token}
	var raw json.RawMessage
	if err := client.getJSON(path, &raw); err != nil {
		fmt.Fprintf(stderr, "midnight logs: %s\n", err)
		return 1
	}
	var logs cliOperationLogsResponse
	if err := json.Unmarshal(raw, &logs); err != nil {
		fmt.Fprintf(stderr, "midnight logs: decode operation logs: %s\n", err)
		return 1
	}
	return renderCLISingle(stdout, stderr, false, raw, logs,
		func(value cliOperationLogsResponse) string { return value.OperationID },
		writeDeployLogsSummary,
	)
}

type tenantScopedCLICredential string

type logsFlags struct {
	follow      *bool
	tail        *int
	build       *bool
	operationID *string
}

func defineLogsFlags(fs *flag.FlagSet) logsFlags {
	return logsFlags{
		follow:      cliFollowFlag(fs, "stream logs as they arrive"),
		tail:        cliLimitFlag(fs, 0, "show at most N log entries per surface"),
		build:       fs.Bool("build", false, "show build and deploy events even when logs from the running container are available"),
		operationID: fs.String("operation-id", "", "operation id to fetch logs for (defaults to the latest deploy)"),
	}
}

func classifyTenantScopedCLICredential(token string) tenantScopedCLICredential {
	switch {
	case strings.HasPrefix(strings.TrimSpace(token), "midn_user_"):
		return "human-session"
	case strings.HasPrefix(strings.TrimSpace(token), "midn_sa_"):
		return "project-automation"
	default:
		return ""
	}
}

func deploymentActionCredentials(projectAPIBase string) (cliconfig.Credentials, error) {
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		ProjectAPIBase: projectAPIBase,
	})
	if err != nil {
		return cliconfig.Credentials{}, err
	}
	credential := classifyTenantScopedCLICredential(credentials.Token)
	if credentials.Kind == cliconfig.CredentialKindHumanSession {
		credential = "human-session"
	}
	if credential == "project-automation" {
		return cliconfig.Credentials{}, fmt.Errorf(
			"%s credentials cannot use human/operator deployment actions",
			credential,
		)
	}
	return credentials, nil
}

// deploymentActionToken is retained for package-level compatibility tests and
// helpers. Command paths use deploymentActionCredentials so the resolved
// endpoint and credential always travel together.
func deploymentActionToken(projectAPIBase string) (string, error) {
	credentials, err := deploymentActionCredentials(projectAPIBase)
	if err != nil {
		return "", err
	}
	return credentials.Token, nil
}

func runRedeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight redeploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineRedeployFlags(fs)
	deploymentID, explicitKey := f.deploymentID, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight redeploy: %s\n", err)
		return cliConfigExitCode(err)
	}
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight redeploy: %s\n", err)
		return 1
	}
	credentials, err := deploymentActionCredentials(link.APIBase)
	if err != nil {
		fmt.Fprintf(stderr, "midnight redeploy: %s\n", err)
		return 1
	}
	target := strings.TrimSpace(*deploymentID)
	if target == "" {
		latest, err := latestActiveDeployment(credentials.APIBase, credentials.Token, link.ServiceID, environmentID)
		if err != nil {
			fmt.Fprintf(stderr, "midnight redeploy: %s\n", err)
			return 1
		}
		target = latest
	} else if err := validateDeploymentTarget(
		credentials.APIBase,
		credentials.Token,
		link.ServiceID,
		environmentID,
		target,
	); err != nil {
		fmt.Fprintf(stderr, "midnight redeploy: %s\n", err)
		return 1
	}
	if target == "" {
		fmt.Fprintln(stderr, "midnight redeploy: no deployment to redeploy")
		return 1
	}
	path := deploymentActionPath(link.ServiceID, target, "redeploy")
	attempt, err := beginPendingMutationAttempt(
		*explicitKey, credentials.Token, "redeploy", http.MethodPost, path, nil,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight redeploy: prepare retry-safe attempt: %s\n", err)
		return 64
	}
	requestErr := postDeploymentAction(credentials.APIBase, credentials.Token, link.ServiceID, target, "redeploy", attempt.Key, stdout)
	if finishErr := attempt.Finish(requestErr); finishErr != nil {
		fmt.Fprintf(stderr, "midnight redeploy: %s\n", finishErr)
		return 1
	}
	if requestErr != nil {
		writeMutationFailure(stderr, "redeploy", requestErr, attempt.Key)
		return 1
	}
	return 0
}

type redeployFlags struct {
	deploymentID *string
	key          *string
}

func defineRedeployFlags(fs *flag.FlagSet) redeployFlags {
	return redeployFlags{
		deploymentID: cliString(fs, "deployment-id", "", "deployment id to redeploy (defaults to the latest active deployment)", "deployment"),
		key:          cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

func runRollback(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineRollbackFlags(fs)
	to, previous, explicitKey := f.to, f.previous, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if !*previous && strings.TrimSpace(*to) == "" {
		fmt.Fprintln(stderr, "midnight rollback: pass --previous or --to <deployment-id>")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight rollback: %s\n", err)
		return cliConfigExitCode(err)
	}
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight rollback: %s\n", err)
		return 1
	}
	credentials, err := deploymentActionCredentials(link.APIBase)
	if err != nil {
		fmt.Fprintf(stderr, "midnight rollback: %s\n", err)
		return 1
	}
	target := strings.TrimSpace(*to)
	if *previous {
		prev, err := previousDeployment(credentials.APIBase, credentials.Token, link.ServiceID, environmentID)
		if err != nil {
			fmt.Fprintf(stderr, "midnight rollback: %s\n", err)
			return 1
		}
		target = prev
	} else if err := validateDeploymentTarget(
		credentials.APIBase,
		credentials.Token,
		link.ServiceID,
		environmentID,
		target,
	); err != nil {
		fmt.Fprintf(stderr, "midnight rollback: %s\n", err)
		return 1
	}
	if target == "" {
		fmt.Fprintln(stderr, "midnight rollback: could not resolve a target deployment")
		return 1
	}
	path := deploymentActionPath(link.ServiceID, target, "rollback")
	attempt, err := beginPendingMutationAttempt(
		*explicitKey, credentials.Token, "rollback", http.MethodPost, path, nil,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight rollback: prepare retry-safe attempt: %s\n", err)
		return 64
	}
	requestErr := postDeploymentAction(credentials.APIBase, credentials.Token, link.ServiceID, target, "rollback", attempt.Key, stdout)
	if finishErr := attempt.Finish(requestErr); finishErr != nil {
		fmt.Fprintf(stderr, "midnight rollback: %s\n", finishErr)
		return 1
	}
	if requestErr != nil {
		writeMutationFailure(stderr, "rollback", requestErr, attempt.Key)
		return 1
	}
	return 0
}

type rollbackFlags struct {
	to       *string
	previous *bool
	key      *string
}

func defineRollbackFlags(fs *flag.FlagSet) rollbackFlags {
	return rollbackFlags{
		to:       fs.String("to", "", "deployment id to roll back to"),
		previous: fs.Bool("previous", false, "roll back to the deployment just before the active one"),
		key:      cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

func runRuntimeAction(action string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineRuntimeActionFlags(fs)
	deploymentID, explicitKey := f.deploymentID, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight %s: %s\n", action, err)
		return cliConfigExitCode(err)
	}
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight %s: %s\n", action, err)
		return 1
	}
	credentials, err := deploymentActionCredentials(link.APIBase)
	if err != nil {
		fmt.Fprintf(stderr, "midnight %s: %s\n", action, err)
		return 1
	}
	target := strings.TrimSpace(*deploymentID)
	if target == "" {
		target, err = latestActiveDeployment(credentials.APIBase, credentials.Token, link.ServiceID, environmentID)
		if err != nil {
			fmt.Fprintf(stderr, "midnight %s: %s\n", action, err)
			return 1
		}
	} else if target != "" {
		if err := validateDeploymentTarget(
			credentials.APIBase,
			credentials.Token,
			link.ServiceID,
			environmentID,
			target,
		); err != nil {
			fmt.Fprintf(stderr, "midnight %s: %s\n", action, err)
			return 1
		}
	}
	if target == "" {
		fmt.Fprintf(stderr, "midnight %s: no deployment to act on; pass --deployment with an id from `midnight status`\n", action)
		return 64
	}
	path := deploymentActionPath(link.ServiceID, target, action)
	var attempt mutationAttempt
	if action == "restart" {
		attempt, err = beginPendingMutationAttempt(
			*explicitKey, credentials.Token, "runtime-restart", http.MethodPost, path, nil,
		)
	} else {
		attempt.Key, err = resolveMutationIdempotencyKey(
			*explicitKey, credentials.Token, "runtime-"+action, http.MethodPost, path, nil,
		)
	}
	if err != nil {
		fmt.Fprintf(stderr, "midnight %s: prepare retry-safe request: %s\n", action, err)
		return 64
	}
	requestErr := postDeploymentAction(credentials.APIBase, credentials.Token, link.ServiceID, target, action, attempt.Key, stdout)
	if finishErr := attempt.Finish(requestErr); finishErr != nil {
		fmt.Fprintf(stderr, "midnight %s: %s\n", action, finishErr)
		return 1
	}
	if requestErr != nil {
		writeMutationFailure(stderr, action, requestErr, attempt.Key)
		return 1
	}
	return 0
}

type runtimeActionFlags struct {
	deploymentID *string
	key          *string
}

func defineRuntimeActionFlags(fs *flag.FlagSet) runtimeActionFlags {
	return runtimeActionFlags{
		deploymentID: cliString(fs, "deployment-id", "", "deployment id (defaults to the latest active deployment)", "deployment"),
		key:          cliIdempotencyKeyFlag(fs, "stable retry key (restart attempts are journaled; desired-state actions derive exact intent when omitted)"),
	}
}

func runCancel(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineCancelFlags(fs)
	operationID, explicitKey := f.operationID, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight cancel: %s\n", err)
		return cliConfigExitCode(err)
	}
	environmentID, err := requireDeploymentEnvironment(link.EnvironmentID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight cancel: %s\n", err)
		return 1
	}
	credentials, err := deploymentActionCredentials(link.APIBase)
	if err != nil {
		fmt.Fprintf(stderr, "midnight cancel: %s\n", err)
		return 1
	}
	target := strings.TrimSpace(*operationID)
	if target == "" {
		target, err = latestCancelableOperationID(
			credentials.APIBase,
			credentials.Token,
			link.ServiceID,
			environmentID,
		)
	} else {
		err = validateCancelableOperationTarget(
			credentials.APIBase,
			credentials.Token,
			link.ServiceID,
			environmentID,
			target,
		)
	}
	if err != nil {
		fmt.Fprintf(stderr, "midnight cancel: %s\n", err)
		return 1
	}
	path := operationCancelPath(target)
	key, err := resolveMutationIdempotencyKey(
		*explicitKey, credentials.Token, "cancel", http.MethodPost, path, nil,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight cancel: invalid idempotency key: %s\n", err)
		return 64
	}
	if err := postOperationCancel(
		credentials.APIBase,
		credentials.Token,
		target,
		key,
		stdout,
	); err != nil {
		writeMutationFailure(stderr, "cancel", err, key)
		return 1
	}
	return 0
}

type cancelFlags struct {
	operationID *string
	key         *string
}

func defineCancelFlags(fs *flag.FlagSet) cancelFlags {
	return cancelFlags{
		operationID: cliString(fs, "operation-id", "", "operation id to cancel (defaults to the latest in-flight deploy)", "operation"),
		key:         cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact cancel intent when omitted)"),
	}
}

func runDomain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight domain list | add | get DOMAIN_ID | delete --expected-version N DOMAIN_ID | custom list|add HOST|verify DOMAIN_ID|delete DOMAIN_ID")
		return 64
	}
	switch args[0] {
	case "list":
		return runDomainList(args[1:], stdout, stderr)
	case "add":
		return runDomainAdd(args[1:], stdout, stderr)
	case "get":
		return runDomainGet(args[1:], stdout, stderr)
	case "delete", "remove":
		return runDomainDelete(args[1:], stdout, stderr)
	case "custom":
		return runCustomDomain(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight domain: unknown subcommand %q\n", args[0])
		return 64
	}
}

func runCustomDomain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight domain custom list | add [--tls] HOST | verify --expected-version N DOMAIN_ID | delete --expected-version N DOMAIN_ID")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain custom: %s\n", err)
		return cliConfigExitCode(err)
	}
	client, mode, err := linkedDomainClient(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain custom: %s\n", err)
		return 1
	}
	if mode != domainCredentialHuman {
		fmt.Fprintln(stderr, "midnight domain custom: requires a human session")
		return 64
	}
	collection, err := humanCustomDomainCollectionPath(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain custom: %s\n", err)
		return 1
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: midnight domain custom list")
			return 64
		}
		var values []cliCustomDomain
		raw, err := client.doJSON(http.MethodGet, collection, nil, "", &values)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain custom list: %s\n", err)
			return 1
		}
		if err := validateCLIResponseStrict(raw, &values); err != nil {
			fmt.Fprintf(stderr, "midnight domain custom list: invalid response: %s\n", err)
			return 1
		}
		return renderCLIList(stdout, stderr, false, raw, values,
			func(value cliCustomDomain) string { return value.ID },
			writeCustomDomainTable,
		)
	case "add":
		fs := flag.NewFlagSet("midnight domain custom add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		f := defineDomainCustomAddFlags(fs)
		tls, explicitKey := f.tls, f.key
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
			fmt.Fprintln(stderr, "usage: midnight domain custom add [--tls] HOST")
			return 64
		}
		request := map[string]any{"hostname": fs.Arg(0), "tls": *tls}
		key, err := resolveMutationIdempotencyKey(
			*explicitKey, client.token, "custom-domain-add", http.MethodPost, collection, request,
		)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain custom add: invalid idempotency key: %s\n", err)
			return 64
		}
		var value cliCustomDomain
		raw, err := client.doJSON(http.MethodPost, collection, request, key, &value)
		if err != nil {
			writeMutationFailure(stderr, "domain custom add", err, key)
			return 1
		}
		if err := validateCLIResponseStrict(raw, &value); err != nil {
			fmt.Fprintf(stderr, "midnight domain custom add: invalid response: %s\n", err)
			return 1
		}
		return renderCLISingle(stdout, stderr, false, raw, value,
			func(value cliCustomDomain) string { return value.ID },
			writeCustomDomain,
		)
	case "verify":
		return runCustomDomainVerifyDelete(args[1:], true, collection, client, stdout, stderr)
	case "delete":
		return runCustomDomainVerifyDelete(args[1:], false, collection, client, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight domain custom: unknown subcommand %q\n", args[0])
		return 64
	}
}

func runCustomDomainVerifyDelete(args []string, verify bool, collection string, client *cliAPIClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight domain custom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineDomainVersionedFlags(fs)
	version, explicitKey := f.expectedVersion, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || *version < 1 {
		fmt.Fprintln(stderr, "usage: midnight domain custom", map[bool]string{true: "verify", false: "delete"}[verify], "--expected-version N DOMAIN_ID")
		return 64
	}
	id := url.PathEscape(strings.TrimSpace(fs.Arg(0)))
	action := map[bool]string{true: "verify", false: "delete"}[verify]
	method := map[bool]string{true: http.MethodPost, false: http.MethodDelete}[verify]
	path := collection + "/" + id
	if verify {
		path += "/verify"
	}
	request := map[string]int64{"expected_version": *version}
	key, keyErr := resolveMutationIdempotencyKey(
		*explicitKey, client.token, "custom-domain-"+action, method, path, request,
	)
	if keyErr != nil {
		fmt.Fprintf(stderr, "midnight domain custom %s: invalid idempotency key: %s\n", action, keyErr)
		return 64
	}
	var value cliCustomDomain
	if verify {
		raw, requestErr := client.doJSON(http.MethodPost, path, request, key, &value)
		if requestErr != nil {
			writeMutationFailure(stderr, "domain custom "+action, requestErr, key)
			return 1
		}
		if err := validateCLIResponseStrict(raw, &value); err != nil {
			fmt.Fprintf(stderr, "midnight domain custom verify: invalid response: %s\n", err)
			return 1
		}
		return renderCLISingle(stdout, stderr, false, raw, value,
			func(value cliCustomDomain) string { return value.ID },
			writeCustomDomain,
		)
	}
	if _, requestErr := client.doJSON(http.MethodDelete, path, request, key, nil); requestErr != nil {
		writeMutationFailure(stderr, "domain custom "+action, requestErr, key)
		return 1
	}
	fmt.Fprintf(stdout, "Deleted custom domain %s at version %d\n", id, *version)
	return 0
}

func runDomainList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight domain list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight domain list")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain list: %s\n", err)
		return cliConfigExitCode(err)
	}
	client, mode, err := linkedDomainClient(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain list: %s\n", err)
		return 1
	}
	if mode == domainCredentialHuman {
		path, err := humanGeneratedDomainCollectionPath(link)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain list: %s\n", err)
			return 1
		}
		var domains []cliGeneratedDomain
		raw, err := client.doJSON(http.MethodGet, path, nil, "", &domains)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain list: %s\n", err)
			return 1
		}
		if err := validateCLIResponseStrict(raw, &domains); err != nil {
			fmt.Fprintf(stderr, "midnight domain list: invalid generated-domain response: %s\n", err)
			return 1
		}
		for index := range domains {
			if err := validateCLIGeneratedDomain(domains[index], link, ""); err != nil {
				fmt.Fprintf(stderr, "midnight domain list: invalid generated-domain response at index %d: %s\n", index, err)
				return 1
			}
		}
		sort.Slice(domains, func(i, j int) bool {
			if domains[i].Hostname == domains[j].Hostname {
				return domains[i].ID < domains[j].ID
			}
			return domains[i].Hostname < domains[j].Hostname
		})
		return renderCLIList(stdout, stderr, false, raw, domains,
			func(value cliGeneratedDomain) string { return value.ID },
			writeGeneratedDomainTable,
		)
	}
	path := "/api/v1alpha/domains?project_id=" + url.QueryEscape(link.ProjectID) +
		"&environment_id=" + url.QueryEscape(link.EnvironmentID) +
		"&service_id=" + url.QueryEscape(link.ServiceID)
	var domains []map[string]any
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &domains)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain list: %s\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "midnight domain list: operator recovery inventory (not the tenant product API)")
	return renderCLIList(stdout, stderr, false, raw, domains,
		func(value map[string]any) string { return recoveryDomainField(value, "id") },
		writeRecoveryDomainTable,
	)
}

func runDomainAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight domain add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineDomainAddFlags(fs)
	generated, private, tlsDesired := f.generated, f.private, f.tls
	targetPort, pathPrefix, key := f.port, f.path, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain add: %s\n", err)
		return cliConfigExitCode(err)
	}
	client, mode, err := linkedDomainClient(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain add: %s\n", err)
		return 1
	}
	if mode == domainCredentialHuman {
		var forbidden []string
		fs.Visit(func(value *flag.Flag) {
			switch value.Name {
			case "generated", "private", "tls", "port", "path":
				forbidden = append(forbidden, "--"+value.Name)
			}
		})
		if fs.NArg() != 0 || len(forbidden) > 0 {
			fmt.Fprintln(stderr, "midnight domain add: a human session accepts no hostname, TLS, path, port, public/private, or project override; the server generates one HTTP domain")
			return 64
		}
		path, err := humanGeneratedDomainCollectionPath(link)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain add: %s\n", err)
			return 1
		}
		request := struct{}{}
		attempt, err := beginPendingMutationAttempt(
			*key, client.token, "generated-domain-add", http.MethodPost, path, request,
		)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain add: invalid idempotency key: %s\n", err)
			return 64
		}
		var created cliGeneratedDomain
		raw, requestErr := client.doJSON(http.MethodPost, path, request, attempt.Key, &created)
		finishErr := attempt.Finish(requestErr)
		if requestErr != nil {
			writeMutationFailure(stderr, "domain add", requestErr, attempt.Key)
			return 1
		}
		if finishErr != nil {
			fmt.Fprintf(stderr, "midnight domain add: %s\n", finishErr)
			return 1
		}
		if err := validateCLIResponseStrict(raw, &created); err != nil {
			fmt.Fprintf(stderr, "midnight domain add: invalid generated-domain response: %s\n", err)
			return 1
		}
		if err := validateCLIGeneratedDomain(created, link, ""); err != nil {
			fmt.Fprintf(stderr, "midnight domain add: invalid generated-domain response: %s\n", err)
			return 1
		}
		return renderCLISingle(stdout, stderr, false, raw, created,
			func(value cliGeneratedDomain) string { return value.ID },
			writeGeneratedDomain,
		)
	}
	if (*generated && fs.NArg() != 0) || (!*generated && fs.NArg() != 1) {
		fmt.Fprintln(stderr, "usage: midnight domain add [--tls] [--port 8080] HOST | midnight domain add --generated [--tls]")
		return 64
	}
	host := ""
	kind := "generated"
	if !*generated {
		host = fs.Arg(0)
		kind = "custom"
	}
	request := map[string]any{
		"project_id": link.ProjectID, "environment_id": link.EnvironmentID,
		"service_id": link.ServiceID, "kind": kind, "hostname": host,
		"path_prefix": *pathPrefix, "target_port": *targetPort,
		"public": !*private, "tls": *tlsDesired,
	}
	var created map[string]any
	path := "/api/v1alpha/domains"
	resolvedKey, err := resolveMutationIdempotencyKey(
		*key, client.token, "domain-recovery-add", http.MethodPost, path, request,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain add: invalid idempotency key: %s\n", err)
		return 64
	}
	raw, err := client.doJSON(http.MethodPost, path, request, resolvedKey, &created)
	if err != nil {
		writeMutationFailure(stderr, "domain add", err, resolvedKey)
		return 1
	}
	fmt.Fprintln(stderr, "midnight domain add: operator recovery mutation (not the tenant product API)")
	return renderCLISingle(stdout, stderr, false, raw, created,
		func(value map[string]any) string { return recoveryDomainField(value, "id") },
		func(output io.Writer, value map[string]any) {
			writeRecoveryDomainTable(output, []map[string]any{value})
		},
	)
}

func runDomainGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight domain get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight domain get DOMAIN_ID")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain get: %s\n", err)
		return cliConfigExitCode(err)
	}
	client, mode, err := linkedDomainClient(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain get: %s\n", err)
		return 1
	}
	if mode != domainCredentialHuman {
		fmt.Fprintln(stderr, "midnight domain get: the typed item view requires a human session; flat operator recovery has no equivalent")
		return 1
	}
	collection, err := humanGeneratedDomainCollectionPath(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain get: %s\n", err)
		return 1
	}
	domainID := strings.TrimSpace(fs.Arg(0))
	path := collection + "/" + url.PathEscape(domainID)
	var value cliGeneratedDomain
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &value)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain get: %s\n", err)
		return 1
	}
	if err := validateCLIResponseStrict(raw, &value); err != nil {
		fmt.Fprintf(stderr, "midnight domain get: invalid generated-domain response: %s\n", err)
		return 1
	}
	if err := validateCLIGeneratedDomain(value, link, domainID); err != nil {
		fmt.Fprintf(stderr, "midnight domain get: invalid generated-domain response: %s\n", err)
		return 1
	}
	return renderCLISingle(stdout, stderr, false, raw, value,
		func(value cliGeneratedDomain) string { return value.ID },
		writeGeneratedDomain,
	)
}

func runDomainDelete(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight domain delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineDomainVersionedFlags(fs)
	expectedVersion, key := f.expectedVersion, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight domain delete --expected-version N DOMAIN_ID")
		return 64
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain delete: %s\n", err)
		return cliConfigExitCode(err)
	}
	client, mode, err := linkedDomainClient(link)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain delete: %s\n", err)
		return 1
	}
	domainID := strings.TrimSpace(fs.Arg(0))
	if mode == domainCredentialHuman {
		if *expectedVersion < 1 {
			fmt.Fprintln(stderr, "midnight domain delete: --expected-version must be at least 1 for a human session")
			return 64
		}
		collection, err := humanGeneratedDomainCollectionPath(link)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain delete: %s\n", err)
			return 1
		}
		path := collection + "/" + url.PathEscape(domainID)
		request := cliGeneratedDomainDeleteRequest{ExpectedVersion: *expectedVersion}
		attempt, err := beginPendingMutationAttempt(
			*key, client.token, "generated-domain-delete", http.MethodDelete, path, request,
		)
		if err != nil {
			fmt.Fprintf(stderr, "midnight domain delete: invalid idempotency key: %s\n", err)
			return 64
		}
		_, requestErr := client.doJSON(http.MethodDelete, path, request, attempt.Key, nil)
		finishErr := attempt.Finish(requestErr)
		if requestErr != nil {
			writeMutationFailure(stderr, "domain delete", requestErr, attempt.Key)
			return 1
		}
		if finishErr != nil {
			fmt.Fprintf(stderr, "midnight domain delete: %s\n", finishErr)
			return 1
		}
		fmt.Fprintf(stdout, "Deleted generated domain %s at version %d\n", domainID, *expectedVersion)
		return 0
	}
	if *expectedVersion != 0 {
		fmt.Fprintln(stderr, "midnight domain delete: --expected-version applies only to the human generated-domain API")
		return 64
	}
	path := "/api/v1alpha/domains/" + url.PathEscape(domainID)
	resolvedKey, err := resolveMutationIdempotencyKey(
		*key, client.token, "domain-recovery-delete", http.MethodDelete, path, nil,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight domain delete: invalid idempotency key: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodDelete, path, nil, resolvedKey, nil); err != nil {
		writeMutationFailure(stderr, "domain delete", err, resolvedKey)
		return 1
	}
	fmt.Fprintln(stderr, "midnight domain delete: operator recovery mutation (not the tenant product API)")
	fmt.Fprintf(stdout, "Deleted recovery domain %s\n", domainID)
	return 0
}

type domainAddFlags struct {
	generated *bool
	private   *bool
	tls       *bool
	port      *int
	path      *string
	key       *string
}

func defineDomainAddFlags(fs *flag.FlagSet) domainAddFlags {
	return domainAddFlags{
		generated: fs.Bool("generated", false, "allocate a collision-resistant hostname below the configured apps domain"),
		private:   fs.Bool("private", false, "record the domain without enabling public routing"),
		tls:       fs.Bool("tls", false, "request HTTPS (remains unavailable until DNS ownership and certificate issuance complete)"),
		port:      fs.Int("port", 8080, "service container port"),
		path:      fs.String("path", "/", "public route path prefix"),
		key:       cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact domain intent when omitted)"),
	}
}

type domainCustomAddFlags struct {
	tls *bool
	key *string
}

func defineDomainCustomAddFlags(fs *flag.FlagSet) domainCustomAddFlags {
	return domainCustomAddFlags{
		tls: fs.Bool("tls", false, "request HTTPS certificate lifecycle"),
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact domain intent when omitted)"),
	}
}

// domainVersionedFlags is shared by every domain mutation that must name the
// version it expects to replace.
type domainVersionedFlags struct {
	expectedVersion *int64
	key             *string
}

func defineDomainVersionedFlags(fs *flag.FlagSet) domainVersionedFlags {
	return domainVersionedFlags{
		expectedVersion: fs.Int64("expected-version", 0, "positive domain version shown by list/get"),
		key:             cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact domain intent when omitted)"),
	}
}

func writeGeneratedDomainTable(w io.Writer, domains []cliGeneratedDomain) {
	if len(domains) == 0 {
		fmt.Fprintln(w, "No generated domains.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOSTNAME\tPORT\tROUTE\tREACHABLE\tVERSION")
	for _, domain := range domains {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%t\t%d\n",
			domain.ID, domain.Hostname, domain.TargetPort, domain.RouteApplyState, domain.RouteReachable, domain.Version)
	}
	_ = tw.Flush()
}

func writeGeneratedDomain(w io.Writer, domain cliGeneratedDomain) {
	fmt.Fprintf(w, "id         : %s\n", domain.ID)
	fmt.Fprintf(w, "hostname   : %s\n", domain.Hostname)
	fmt.Fprintf(w, "path       : %s\n", domain.Path)
	fmt.Fprintf(w, "port       : %d\n", domain.TargetPort)
	fmt.Fprintf(w, "route      : %s (reachable %t)\n", domain.RouteApplyState, domain.RouteReachable)
	fmt.Fprintf(w, "version    : %d\n", domain.Version)
}

func writeCustomDomainTable(w io.Writer, domains []cliCustomDomain) {
	if len(domains) == 0 {
		fmt.Fprintln(w, "No custom domains.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOSTNAME\tOWNERSHIP\tROUTE\tTLS\tVERSION")
	for _, domain := range domains {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%d\n",
			domain.ID, domain.Hostname, domain.OwnershipState, domain.RouteApplyState, domain.TLS, domain.Version)
	}
	_ = tw.Flush()
}

func writeCustomDomain(w io.Writer, domain cliCustomDomain) {
	fmt.Fprintf(w, "id         : %s\n", domain.ID)
	fmt.Fprintf(w, "hostname   : %s\n", domain.Hostname)
	fmt.Fprintf(w, "ownership  : %s\n", domain.OwnershipState)
	fmt.Fprintf(w, "route      : %s (reachable %t)\n", domain.RouteApplyState, domain.RouteReachable)
	fmt.Fprintf(w, "tls        : %t\n", domain.TLS)
	fmt.Fprintf(w, "version    : %d\n", domain.Version)
	if domain.DNSChallenge.RecordName != "" {
		fmt.Fprintf(w, "dns record : %s -> %s (%s)\n",
			domain.DNSChallenge.RecordName, domain.DNSChallenge.RecordValue, domain.DNSChallenge.State)
	}
	if domain.RoutingDNS.RecordName != "" {
		fmt.Fprintf(w, "routing    : %s %s -> %s (%s)\n",
			domain.RoutingDNS.RecordType, domain.RoutingDNS.RecordName, domain.RoutingDNS.RecordValue, domain.RoutingDNS.State)
	}
}

// recoveryDomainField reads one string field from the untyped operator
// recovery inventory without assuming a schema the product API does not own.
func recoveryDomainField(value map[string]any, key string) string {
	if text, ok := value[key].(string); ok {
		return text
	}
	return ""
}

func writeRecoveryDomainTable(w io.Writer, domains []map[string]any) {
	if len(domains) == 0 {
		fmt.Fprintln(w, "No domains.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOSTNAME\tKIND\tROUTE")
	for _, domain := range domains {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			recoveryDomainField(domain, "id"),
			recoveryDomainField(domain, "hostname"),
			recoveryDomainField(domain, "kind"),
			recoveryDomainField(domain, "route_apply_state"))
	}
	_ = tw.Flush()
}

type domainCredentialMode string

const (
	domainCredentialHuman    domainCredentialMode = "human"
	domainCredentialRecovery domainCredentialMode = "operator-recovery"
)

type cliGeneratedDomain struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	ProjectID       string    `json:"project_id"`
	ServiceID       string    `json:"service_id"`
	EnvironmentID   string    `json:"environment_id"`
	Kind            string    `json:"kind"`
	Hostname        string    `json:"hostname"`
	Path            string    `json:"path"`
	TargetPort      int       `json:"target_port"`
	Public          bool      `json:"public"`
	TLS             bool      `json:"tls"`
	Version         int64     `json:"version"`
	RouteApplyState string    `json:"route_apply_state"`
	RouteReachable  bool      `json:"route_reachable"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type cliCustomDomain struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	ServiceID       string `json:"service_id"`
	EnvironmentID   string `json:"environment_id"`
	Kind            string `json:"kind"`
	Hostname        string `json:"hostname"`
	Path            string `json:"path"`
	TargetPort      int    `json:"target_port"`
	Public          bool   `json:"public"`
	TLS             bool   `json:"tls"`
	Version         int64  `json:"version"`
	OwnershipState  string `json:"ownership_state"`
	RouteApplyState string `json:"route_apply_state"`
	RouteReachable  bool   `json:"route_reachable"`
	RoutingDNS      struct {
		RecordType  string `json:"record_type"`
		RecordName  string `json:"record_name"`
		RecordValue string `json:"record_value"`
		State       string `json:"state"`
		Note        string `json:"note"`
	} `json:"routing_dns"`
	DNSChallenge struct {
		RecordName  string `json:"record_name"`
		RecordValue string `json:"record_value"`
		State       string `json:"state"`
		Attempts    int64  `json:"attempts"`
	} `json:"dns_challenge"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cliGeneratedDomainDeleteRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

func linkedDomainClient(
	link cliconfig.ProjectLink,
) (*cliAPIClient, domainCredentialMode, error) {
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		ProjectAPIBase: link.APIBase,
	})
	if err != nil {
		return nil, "", err
	}
	kind := credentials.Kind
	if classifyTenantScopedCLICredential(credentials.Token) == "human-session" {
		kind = cliconfig.CredentialKindHumanSession
	}
	if classifyTenantScopedCLICredential(credentials.Token) == "project-automation" {
		return nil, "", errors.New("project automation credentials cannot manage domains; run `midnight login`")
	}
	mode := domainCredentialRecovery
	if kind == cliconfig.CredentialKindHumanSession {
		mode = domainCredentialHuman
	}
	return &cliAPIClient{
		baseURL:        credentials.APIBase,
		token:          credentials.Token,
		credentialKind: credentials.Kind,
		client:         authHTTPClient,
	}, mode, nil
}

func humanGeneratedDomainCollectionPath(link cliconfig.ProjectLink) (string, error) {
	serviceID := strings.TrimSpace(link.ServiceID)
	environmentID := strings.TrimSpace(link.EnvironmentID)
	if serviceID == "" || environmentID == "" {
		return "", errors.New("a linked service and environment are required; run `midnight link`")
	}
	return "/api/v1alpha/services/" + url.PathEscape(serviceID) +
		"/environments/" + url.PathEscape(environmentID) +
		"/domains", nil
}

func humanCustomDomainCollectionPath(link cliconfig.ProjectLink) (string, error) {
	serviceID := strings.TrimSpace(link.ServiceID)
	environmentID := strings.TrimSpace(link.EnvironmentID)
	if serviceID == "" || environmentID == "" {
		return "", errors.New("a linked service and environment are required; run `midnight link`")
	}
	return "/api/v1alpha/services/" + url.PathEscape(serviceID) + "/environments/" + url.PathEscape(environmentID) + "/custom-domains", nil
}

func newDomainIdempotencyKey(operation string, entropy io.Reader) (string, error) {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return "", errors.New("domain operation is required")
	}
	if entropy == nil {
		return "", errors.New("secure random source is required")
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(entropy, nonce); err != nil {
		return "", fmt.Errorf("read secure randomness: %w", err)
	}
	return fmt.Sprintf(
		"midnight-generated-domain-%s-v1-%x",
		operation,
		nonce,
	), nil
}

func validateCLIResponseStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("response contains more than one JSON value")
		}
		return err
	}
	return nil
}

func validateCLIGeneratedDomain(
	value cliGeneratedDomain,
	link cliconfig.ProjectLink,
	expectedID string,
) error {
	if value.ID == "" ||
		value.WorkspaceID == "" ||
		value.ProjectID == "" ||
		value.ServiceID == "" ||
		value.EnvironmentID == "" ||
		value.Hostname == "" {
		return errors.New("required identity or hostname is missing")
	}
	if expectedID != "" && value.ID != expectedID {
		return fmt.Errorf("domain id %q does not match requested %q", value.ID, expectedID)
	}
	if value.ProjectID != strings.TrimSpace(link.ProjectID) ||
		value.ServiceID != strings.TrimSpace(link.ServiceID) ||
		value.EnvironmentID != strings.TrimSpace(link.EnvironmentID) {
		return errors.New("response lineage does not match the linked project, service, and environment")
	}
	if value.Kind != "generated" ||
		value.Path != "/" ||
		!value.Public ||
		value.TLS ||
		value.TargetPort < 1 ||
		value.TargetPort > 65535 ||
		value.Version < 1 ||
		value.CreatedAt.IsZero() ||
		value.UpdatedAt.IsZero() {
		return errors.New("response is not a valid server-derived generated HTTP domain")
	}
	switch value.RouteApplyState {
	case "pending",
		"private",
		"blocked_tls",
		"blocked_backend",
		"file_applied",
		"ready",
		"failed":
	default:
		return fmt.Errorf("unknown route_apply_state %q", value.RouteApplyState)
	}
	if value.RouteReachable && value.RouteApplyState != "ready" {
		return errors.New("route_reachable requires route_apply_state=ready")
	}
	return nil
}

const deploymentListPageLimit = 100

type cliDeployment struct {
	ID             string    `json:"id"`
	DeploymentID   string    `json:"deployment_id,omitempty"`
	ServiceID      string    `json:"service_id"`
	ProjectID      string    `json:"project_id,omitempty"`
	EnvironmentID  string    `json:"environment_id"`
	OperationID    string    `json:"operation_id"`
	OperationState string    `json:"operation_state"`
	Status         string    `json:"status"`
	Active         bool      `json:"active"`
	LastEventAt    time.Time `json:"last_event_at,omitempty"`
}

type cliDeploymentPage struct {
	Items      []cliDeployment `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func requireDeploymentEnvironment(environmentID string) (string, error) {
	environmentID = strings.TrimSpace(environmentID)
	if environmentID == "" {
		return "", fmt.Errorf(".midnight/project.json has no environment_id; re-run `midnight link` or select an environment")
	}
	return environmentID, nil
}

func serviceDeploymentsPath(serviceID, environmentID, cursor string) string {
	values := url.Values{}
	values.Set("environment_id", strings.TrimSpace(environmentID))
	values.Set("limit", fmt.Sprintf("%d", deploymentListPageLimit))
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		values.Set("cursor", cursor)
	}
	return "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(serviceID)) + "/deployments?" + values.Encode()
}

func listServiceDeployments(apiBase, token, serviceID, environmentID string) ([]cliDeployment, error) {
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		return nil, fmt.Errorf(".midnight/project.json has no service_id; re-run `midnight link`")
	}
	var err error
	environmentID, err = requireDeploymentEnvironment(environmentID)
	if err != nil {
		return nil, err
	}

	client := &devAPIClient{base: apiBase, token: token}
	deployments := make([]cliDeployment, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		var raw json.RawMessage
		path := serviceDeploymentsPath(serviceID, environmentID, cursor)
		if err := client.getJSON(path, &raw); err != nil {
			return nil, fmt.Errorf("list deployments: %w", err)
		}
		items, nextCursor, err := decodeDeploymentPage(raw)
		if err != nil {
			return nil, fmt.Errorf("list deployments: %w", err)
		}
		for _, deployment := range items {
			if strings.TrimSpace(deployment.EnvironmentID) != environmentID {
				continue
			}
			if candidateServiceID := strings.TrimSpace(deployment.ServiceID); candidateServiceID != "" && candidateServiceID != serviceID {
				continue
			}
			deployments = append(deployments, deployment)
		}
		nextCursor = strings.TrimSpace(nextCursor)
		if nextCursor == "" {
			return deployments, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, fmt.Errorf("list deployments: server repeated pagination cursor")
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
}

func decodeDeploymentPage(raw json.RawMessage) ([]cliDeployment, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var deployments []cliDeployment
		if err := json.Unmarshal(raw, &deployments); err != nil {
			return nil, "", err
		}
		return deployments, "", nil
	}
	var page cliDeploymentPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, "", err
	}
	if page.Items == nil {
		return nil, "", fmt.Errorf("deployment response must be an array or an object with items")
	}
	return page.Items, page.NextCursor, nil
}

func (deployment cliDeployment) canonicalID() string {
	if id := strings.TrimSpace(deployment.DeploymentID); id != "" {
		return id
	}
	id := strings.TrimSpace(deployment.ID)
	if id != "" && id != strings.TrimSpace(deployment.OperationID) {
		return id
	}
	return ""
}

func (deployment cliDeployment) isActive() bool {
	return deployment.Active || strings.EqualFold(strings.TrimSpace(deployment.Status), "active")
}

func (deployment cliDeployment) isSuccessfulPreviousCandidate() bool {
	if deployment.isActive() || deployment.canonicalID() == "" {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(deployment.Status))
	operationState := strings.ToUpper(strings.TrimSpace(deployment.OperationState))
	if operationState != "" && operationState != "SUCCEEDED" {
		return false
	}
	return status == "SUPERSEDED" || status == "SUCCEEDED" ||
		(status == "" && operationState == "SUCCEEDED")
}

func validateDeploymentTarget(
	apiBase,
	token,
	serviceID,
	environmentID,
	deploymentID string,
) error {
	deployments, err := listServiceDeployments(
		apiBase,
		token,
		serviceID,
		environmentID,
	)
	if err != nil {
		return err
	}
	deploymentID = strings.TrimSpace(deploymentID)
	for _, deployment := range deployments {
		if deployment.canonicalID() == deploymentID {
			return nil
		}
	}
	return fmt.Errorf(
		"deployment %s was not found in linked environment %s",
		deploymentID,
		environmentID,
	)
}

func latestDeployOperationID(apiBase, token, serviceID, environmentID string) (string, error) {
	deployments, err := listServiceDeployments(apiBase, token, serviceID, environmentID)
	if err != nil {
		return "", err
	}
	for _, deployment := range deployments {
		if operationID := strings.TrimSpace(deployment.OperationID); operationID != "" {
			return operationID, nil
		}
	}
	return "", fmt.Errorf("service %s has no deployments with an operation in environment %s", serviceID, environmentID)
}

func latestCancelableOperationID(
	apiBase,
	token,
	serviceID,
	environmentID string,
) (string, error) {
	deployments, err := listServiceDeployments(
		apiBase,
		token,
		serviceID,
		environmentID,
	)
	if err != nil {
		return "", err
	}
	for _, deployment := range deployments {
		state := strings.ToUpper(strings.TrimSpace(deployment.OperationState))
		if state != "QUEUED" && state != "RUNNING" {
			continue
		}
		if operationID := strings.TrimSpace(deployment.OperationID); operationID != "" {
			return operationID, nil
		}
	}
	return "", fmt.Errorf(
		"service %s has no queued or running deployment in environment %s",
		serviceID,
		environmentID,
	)
}

func validateCancelableOperationTarget(
	apiBase,
	token,
	serviceID,
	environmentID,
	operationID string,
) error {
	deployments, err := listServiceDeployments(
		apiBase,
		token,
		serviceID,
		environmentID,
	)
	if err != nil {
		return err
	}
	operationID = strings.TrimSpace(operationID)
	for _, deployment := range deployments {
		if strings.TrimSpace(deployment.OperationID) != operationID {
			continue
		}
		state := strings.ToUpper(strings.TrimSpace(deployment.OperationState))
		if state == "QUEUED" || state == "RUNNING" {
			return nil
		}
		return fmt.Errorf(
			"operation %s is %s, not queued or running",
			operationID,
			strings.ToLower(state),
		)
	}
	return fmt.Errorf(
		"operation %s was not found in linked environment %s",
		operationID,
		environmentID,
	)
}

func deploymentIDForOperation(apiBase, token, serviceID, environmentID, operationID string) (string, error) {
	deployments, err := listServiceDeployments(apiBase, token, serviceID, environmentID)
	if err != nil {
		return "", err
	}
	operationID = strings.TrimSpace(operationID)
	for _, deployment := range deployments {
		if strings.TrimSpace(deployment.OperationID) != operationID {
			continue
		}
		if deploymentID := deployment.canonicalID(); deploymentID != "" {
			return deploymentID, nil
		}
		return "", fmt.Errorf("the deployment for operation %s has no deployment id", operationID)
	}
	return "", fmt.Errorf("operation %s has no deployment for service %s in environment %s", operationID, serviceID, environmentID)
}

func latestActiveDeployment(apiBase, token, serviceID, environmentID string) (string, error) {
	deployments, err := listServiceDeployments(apiBase, token, serviceID, environmentID)
	if err != nil {
		return "", err
	}
	for _, deployment := range deployments {
		if !deployment.isActive() {
			continue
		}
		if deploymentID := deployment.canonicalID(); deploymentID != "" {
			return deploymentID, nil
		}
	}
	return "", fmt.Errorf("service %s has no active deployment in environment %s", serviceID, environmentID)
}

func previousDeployment(apiBase, token, serviceID, environmentID string) (string, error) {
	deployments, err := listServiceDeployments(apiBase, token, serviceID, environmentID)
	if err != nil {
		return "", err
	}
	foundActive := false
	for _, deployment := range deployments {
		if !foundActive {
			if deployment.isActive() && deployment.canonicalID() != "" {
				foundActive = true
			}
			continue
		}
		if deployment.isSuccessfulPreviousCandidate() {
			return deployment.canonicalID(), nil
		}
	}
	if !foundActive {
		return "", fmt.Errorf("service %s has no active deployment in environment %s", serviceID, environmentID)
	}
	return "", fmt.Errorf("service %s has no previous successful deployment in environment %s", serviceID, environmentID)
}

func postIdempotentJSON(target, token, idempotencyKey string, body any, stdout io.Writer) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	res, resp, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodPost, req.URL.RequestURI(), res.Status, resp)
	}
	renderCLIOperationResponse(stdout, resp)
	return nil
}

func deploymentActionPath(serviceID, deploymentID, action string) string {
	return "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(serviceID)) +
		"/deployments/" + url.PathEscape(strings.TrimSpace(deploymentID)) +
		"/actions/" + url.PathEscape(strings.TrimSpace(action))
}

func postDeploymentAction(apiBase, token, serviceID, deploymentID, action, idempotencyKey string, stdout io.Writer) error {
	path := deploymentActionPath(serviceID, deploymentID, action)
	target := strings.TrimRight(apiBase, "/") + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	res, resp, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodPost, path, res.Status, resp)
	}
	renderCLIOperationResponse(stdout, resp)
	return nil
}

func operationCancelPath(operationID string) string {
	return "/api/v1alpha/operations/" +
		url.PathEscape(strings.TrimSpace(operationID)) +
		"/cancel"
}

func postOperationCancel(
	apiBase,
	token,
	operationID,
	idempotencyKey string,
	stdout io.Writer,
) error {
	path := operationCancelPath(operationID)
	target := strings.TrimRight(apiBase, "/") + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if strings.TrimSpace(token) != "" {
		req.Header.Set(
			"Authorization",
			"Bearer "+strings.TrimSpace(token),
		)
	}
	res, resp, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodPost, path, res.Status, resp)
	}
	renderCLIOperationResponse(stdout, resp)
	return nil
}

// renderCLIOperationResponse prints an accepted-operation body under the one
// output contract: a summary line by default, the raw document under
// --output json/jsonl, and the operation id alone under --output name.
func renderCLIOperationResponse(stdout io.Writer, raw []byte) {
	var operation cliOperationDetail
	if err := json.Unmarshal(raw, &operation); err != nil || strings.TrimSpace(operation.ID) == "" {
		// Not an operation document; the caller still deserves the body.
		selection := cliOutputSelectionFor(stdout, false)
		if selection.format == "name" {
			return
		}
		fmt.Fprintln(stdout, strings.TrimRight(string(raw), "\n"))
		return
	}
	renderCLISingle(stdout, io.Discard, false, raw, operation,
		func(value cliOperationDetail) string { return value.ID },
		func(output io.Writer, value cliOperationDetail) {
			fmt.Fprintf(output, "operation %s: %s\n", value.ID, value.State)
			if strings.TrimSpace(value.Code) != "" || strings.TrimSpace(value.Reason) != "" {
				fmt.Fprintf(output, "reason: %s %s\n", value.Code, value.Reason)
			}
		},
	)
}

func openExternal(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// scannerOf wraps a reader in a Scanner with a generous buffer so SSE
// streams with long event lines do not error out. It is unused by the
// initial implementations but kept here for future log-streaming
// commands.
func scannerOf(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1<<20)
	return s
}
