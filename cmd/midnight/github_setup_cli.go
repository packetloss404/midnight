// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type cliGitHubSetupInitiation struct {
	InstallURL string    `json:"install_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type gitHubSetupFlags struct {
	workspaceID *string
}

func defineGitHubSetupFlags(fs *flag.FlagSet) gitHubSetupFlags {
	return gitHubSetupFlags{
		workspaceID: fs.String("workspace-id", "", "workspace to receive the verified GitHub App installation"),
	}
}

func runGitHub(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "setup" {
		fmt.Fprintln(stderr, "usage: midnight github setup --workspace-id ID [--api-base URL] [--token TOKEN] [--json]")
		return 64
	}
	fs, cfg := newAPIFlagSet("midnight github setup", stderr)
	f := defineGitHubSetupFlags(fs)
	workspaceID := f.workspaceID
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	canonicalWorkspaceID := strings.TrimSpace(*workspaceID)
	if fs.NArg() != 0 || canonicalWorkspaceID == "" || canonicalWorkspaceID != *workspaceID {
		fmt.Fprintln(stderr, "usage: midnight github setup --workspace-id ID [--api-base URL] [--token TOKEN] [--json]")
		return 64
	}
	client, err := resolveHumanGitHubClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight github setup: %s\n", err)
		return 1
	}
	path := "/api/v1alpha/workspaces/" + url.PathEscape(canonicalWorkspaceID) + "/github/setup"
	var result cliGitHubSetupInitiation
	raw, err := client.doJSON(http.MethodPost, path, nil, "", &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight github setup: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	if strings.TrimSpace(result.InstallURL) == "" || result.ExpiresAt.IsZero() {
		fmt.Fprintln(stderr, "midnight github setup: server returned an incomplete installation handoff")
		return 1
	}
	fmt.Fprintln(stdout, terminalSafeText(result.InstallURL))
	fmt.Fprintf(stdout, "Expires: %s\n", result.ExpiresAt.UTC().Format(time.RFC3339))
	return 0
}
