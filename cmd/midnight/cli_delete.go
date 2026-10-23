// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// cliOptionsFor recovers the framework options for a legacy handler. Legacy
// handlers receive only writers, but stdout is the framework's output writer,
// which carries the invocation context.
func cliOptionsFor(output io.Writer) CLIOptions {
	if wrapped, ok := output.(*cliOutputWriter); ok && wrapped.execution != nil {
		return wrapped.execution.Options
	}
	return CLIOptions{Output: "table"}
}

// cliStdinFor returns the invocation's stdin so prompts are testable and never
// read the process stdin behind a caller's back.
func cliStdinFor(output io.Writer) io.Reader {
	if wrapped, ok := output.(*cliOutputWriter); ok && wrapped.execution != nil && wrapped.execution.Stdin != nil {
		return wrapped.execution.Stdin
	}
	return strings.NewReader("")
}

// confirmCLIDestruction applies one confirmation contract to every delete
// verb: --yes (or the framework -y) approves, --no-input without --yes is a
// usage error, and an interactive terminal must retype the exact slug or id.
// It returns 0 when the caller may proceed.
func confirmCLIDestruction(stdout, stderr io.Writer, command, kind, name string, yes bool) int {
	options := cliOptionsFor(stdout)
	if yes || options.Yes {
		return 0
	}
	if options.NoInput {
		fmt.Fprintf(stderr, "%s: --yes is required with --no-input before deleting %s %s\n", command, kind, name)
		return cliUsageExitCode
	}
	fmt.Fprintf(stdout, "This permanently deletes %s %s.\n", kind, name)
	fmt.Fprintf(stdout, "Type %s to confirm: ", name)
	scanner := bufio.NewScanner(cliStdinFor(stdout))
	if !scanner.Scan() {
		fmt.Fprintf(stderr, "%s: aborted; no confirmation was read\n", command)
		return 1
	}
	if strings.TrimSpace(scanner.Text()) != strings.TrimSpace(name) {
		fmt.Fprintf(stderr, "%s: aborted; confirmation did not match %s\n", command, name)
		return 1
	}
	return 0
}

// describeCLIDeleteError turns the two server answers a not-yet-shipped
// delete route can give into one actionable sentence instead of a bare status
// line. Every other failure is reported verbatim.
func describeCLIDeleteError(kind string, err error) string {
	var failure *cliAPIStatusFailure
	if errors.As(err, &failure) {
		status := failure.Status
		switch {
		case strings.Contains(status, "405"), strings.Contains(status, "501"):
			return fmt.Sprintf(
				"this server does not implement %s deletion yet (%s); upgrade the server or delete from the console",
				kind,
				strings.TrimSpace(status),
			)
		case strings.Contains(status, "404"):
			return fmt.Sprintf("%s not found, or this server does not implement %s deletion yet (%s)", kind, kind, strings.TrimSpace(status))
		}
	}
	return err.Error()
}

func runServiceDelete(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service delete", stderr)
	f := defineServiceDeleteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return cliUsageExitCode
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fmt.Fprintln(stderr, "usage: midnight service delete [--yes] <service-id>")
		return cliUsageExitCode
	}
	serviceID := strings.TrimSpace(fs.Arg(0))
	if code := confirmCLIDestruction(stdout, stderr, "midnight service delete", "service", serviceID, *f.yes); code != 0 {
		return code
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service delete: %s\n", err)
		return cliUsageExitCode
	}
	path := "/api/v1alpha/services/" + url.PathEscape(serviceID)
	key, err := resolveMutationIdempotencyKey(*f.key, client.token, "service-delete", http.MethodDelete, path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service delete: prepare retry-safe request: %s\n", err)
		return cliUsageExitCode
	}
	if _, err := client.doJSON(http.MethodDelete, path, nil, key, nil); err != nil {
		fmt.Fprintf(stderr, "midnight service delete: %s\n", describeCLIDeleteError("service", err))
		return 1
	}
	fmt.Fprintf(stdout, "service deleted: %s\n", serviceID)
	return 0
}

type serviceDeleteFlags struct {
	yes *bool
	key *string
}

func defineServiceDeleteFlags(fs *flag.FlagSet) serviceDeleteFlags {
	return serviceDeleteFlags{
		yes: fs.Bool("yes", false, "skip the interactive confirmation prompt"),
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact delete intent when omitted)"),
	}
}

func runProjectDelete(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight project delete", stderr)
	f := defineProjectDeleteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return cliUsageExitCode
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fmt.Fprintln(stderr, "usage: midnight project delete [--yes] <project-id>")
		return cliUsageExitCode
	}
	projectID := strings.TrimSpace(fs.Arg(0))
	if code := confirmCLIDestruction(stdout, stderr, "midnight project delete", "project", projectID, *f.yes); code != 0 {
		return code
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project delete: %s\n", err)
		return cliUsageExitCode
	}
	path := "/api/v1alpha/projects/" + url.PathEscape(projectID)
	key, err := resolveMutationIdempotencyKey(*f.key, client.token, "project-delete", http.MethodDelete, path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project delete: prepare retry-safe request: %s\n", err)
		return cliUsageExitCode
	}
	if _, err := client.doJSON(http.MethodDelete, path, nil, key, nil); err != nil {
		fmt.Fprintf(stderr, "midnight project delete: %s\n", describeCLIDeleteError("project", err))
		return 1
	}
	fmt.Fprintf(stdout, "project deleted: %s\n", projectID)
	return 0
}

type projectDeleteFlags struct {
	yes *bool
	key *string
}

func defineProjectDeleteFlags(fs *flag.FlagSet) projectDeleteFlags {
	return projectDeleteFlags{
		yes: fs.Bool("yes", false, "skip the interactive confirmation prompt"),
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact delete intent when omitted)"),
	}
}
