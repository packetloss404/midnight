// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/cliconfig"
)

const defaultProjectTokenLifetime = 30 * 24 * time.Hour

type cliAutomationTokenList struct {
	Items []automationtoken.Token `json:"items"`
}

type cliAutomationTokenCreateRequest struct {
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

func runProjectToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight token list|create|revoke [flags]")
		return 64
	}
	switch args[0] {
	case "list":
		return runProjectTokenList(args[1:], stdout, stderr)
	case "create":
		return runProjectTokenCreate(args[1:], stdout, stderr)
	case "revoke":
		return runProjectTokenRevoke(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight token: unknown subcommand %q\n", args[0])
		return 64
	}
}

type projectTokenFlags struct {
	api       *apiCommandConfig
	projectID string
}

func newProjectTokenFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *projectTokenFlags) {
	fs, api := newAPIFlagSet(name, stderr)
	flags := &projectTokenFlags{api: api}
	cliStringVar(fs, &flags.projectID, flagProjectID, "", "project ID (defaults to the linked project)", "project")
	return fs, flags
}

// projectTokenCommandFlags builds the help/lookup copy of a token command's
// FlagSet through the constructor the handler uses, so the two cannot drift.
func projectTokenCommandFlags(name string, extra func(*flag.FlagSet)) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs, _ := newProjectTokenFlagSet(name, io.Discard)
		if extra != nil {
			extra(fs)
		}
		return fs
	}
}

// defineProjectTokenCreateFlags and defineProjectTokenRevokeFlags own the
// non-shared flags of their commands.
func defineProjectTokenCreateFlags(fs *flag.FlagSet) (name *string, expiresIn *time.Duration, expiresAt *string, key *string) {
	name = fs.String("name", "", "display name for the credential")
	expiresIn = fs.Duration("expires-in", defaultProjectTokenLifetime, "credential lifetime (for example 720h)")
	expiresAt = fs.String("expires-at", "", "fixed RFC3339 expiry (required with a caller-supplied idempotency key)")
	key = cliIdempotencyKeyFlag(fs, "stable retry key (generated when omitted)")
	return name, expiresIn, expiresAt, key
}

func defineProjectTokenRevokeFlags(fs *flag.FlagSet) (tokenID *string, key *string) {
	tokenID = fs.String("token-id", "", "automation token ID")
	key = cliIdempotencyKeyFlag(fs, "stable retry key (generated when omitted)")
	return tokenID, key
}

func (f *projectTokenFlags) resolveClient() (*cliAPIClient, string, error) {
	projectID := strings.TrimSpace(f.projectID)
	var link cliconfig.ProjectLink
	if projectID == "" || strings.TrimSpace(f.api.BaseURL) == "" {
		loaded, err := cliconfig.LoadProjectLink("")
		if err == nil {
			link = loaded
		} else if projectID == "" {
			return nil, "", err
		}
	}
	if projectID == "" {
		projectID = strings.TrimSpace(link.ProjectID)
	}
	if projectID == "" {
		return nil, "", fmt.Errorf("--project-id is required when the current directory is not linked")
	}
	client, err := newCLIAPIClient(f.api)
	if err != nil {
		return nil, "", err
	}
	if client.credentialKind != cliconfig.CredentialKindHumanSession {
		return nil, "", fmt.Errorf("project automation token management requires a human session; run `midnight login`")
	}
	return client, projectID, nil
}

func runProjectTokenList(args []string, stdout, stderr io.Writer) int {
	fs, flags := newProjectTokenFlagSet("midnight token list", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	client, projectID, err := flags.resolveClient()
	if err != nil {
		fmt.Fprintf(stderr, "midnight token list: %s\n", err)
		return 1
	}
	var result cliAutomationTokenList
	path := projectAutomationTokenPath(projectID)
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight token list: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, flags.api.JSON, raw); done {
		return code
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tPREFIX\tPROFILE\tEXPIRES\tSTATE")
	for _, token := range result.Items {
		state := "active"
		if token.RevokedAt != nil {
			state = "revoked"
		} else if !time.Now().UTC().Before(token.ExpiresAt) {
			state = "expired"
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			terminalSafeText(token.ID),
			terminalSafeText(token.Name),
			terminalSafeText(token.TokenPrefix),
			terminalSafeText(string(token.Profile)),
			token.ExpiresAt.UTC().Format(time.RFC3339),
			state,
		)
	}
	_ = writer.Flush()
	return 0
}

func runProjectTokenCreate(args []string, stdout, stderr io.Writer) int {
	fs, flags := newProjectTokenFlagSet("midnight token create", stderr)
	name, expiresIn, expiresAt, idempotencyKey := defineProjectTokenCreateFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "midnight token create: --name is required")
		return 64
	}
	expiresInSet := false
	fs.Visit(func(value *flag.Flag) {
		if value.Name == "expires-in" {
			expiresInSet = true
		}
	})
	if *expiresIn <= 0 {
		fmt.Fprintln(stderr, "midnight token create: --expires-in must be positive")
		return 64
	}
	if strings.TrimSpace(*expiresAt) != "" && expiresInSet {
		fmt.Fprintln(stderr, "midnight token create: --expires-at and --expires-in are mutually exclusive")
		return 64
	}
	client, projectID, err := flags.resolveClient()
	if err != nil {
		fmt.Fprintf(stderr, "midnight token create: %s\n", err)
		return 1
	}
	key := strings.TrimSpace(*idempotencyKey)
	if key != "" && strings.TrimSpace(*expiresAt) == "" {
		fmt.Fprintln(stderr, "midnight token create: --expires-at is required with --idempotency-key so an exact retry uses the same request")
		return 64
	}
	if key == "" {
		key, err = newProjectTokenIdempotencyKey()
		if err != nil {
			fmt.Fprintf(stderr, "midnight token create: generate idempotency key: %s\n", err)
			return 1
		}
	}
	expiry := time.Now().UTC().Add(*expiresIn)
	if value := strings.TrimSpace(*expiresAt); value != "" {
		expiry, err = time.Parse(time.RFC3339, value)
		if err != nil {
			fmt.Fprintln(stderr, "midnight token create: --expires-at must be RFC3339")
			return 64
		}
		expiry = expiry.UTC()
	}
	request := cliAutomationTokenCreateRequest{
		Name:      strings.TrimSpace(*name),
		ExpiresAt: expiry,
	}
	var result automationtoken.MintResult
	raw, err := client.doJSON(http.MethodPost, projectAutomationTokenPath(projectID), request, key, &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight token create: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, flags.api.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "Created project automation token %s (%s).\n", terminalSafeText(result.Token.Name), terminalSafeText(result.Token.ID))
	fmt.Fprintln(stdout, "Copy this credential now. Midnight will not show it again:")
	fmt.Fprintln(stdout, terminalSafeText(result.AccessToken))
	return 0
}

func runProjectTokenRevoke(args []string, stdout, stderr io.Writer) int {
	fs, flags := newProjectTokenFlagSet("midnight token revoke", stderr)
	tokenID, idempotencyKey := defineProjectTokenRevokeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*tokenID) == "" {
		fmt.Fprintln(stderr, "midnight token revoke: --token-id is required")
		return 64
	}
	client, projectID, err := flags.resolveClient()
	if err != nil {
		fmt.Fprintf(stderr, "midnight token revoke: %s\n", err)
		return 1
	}
	key := strings.TrimSpace(*idempotencyKey)
	if key == "" {
		key, err = newProjectTokenIdempotencyKey()
		if err != nil {
			fmt.Fprintf(stderr, "midnight token revoke: generate idempotency key: %s\n", err)
			return 1
		}
	}
	path := projectAutomationTokenPath(projectID) + "/" + url.PathEscape(strings.TrimSpace(*tokenID))
	if _, err := client.doJSON(http.MethodDelete, path, nil, key, nil); err != nil {
		fmt.Fprintf(stderr, "midnight token revoke: %s\n", err)
		return 1
	}
	document := cliRevokedDocument("id", strings.TrimSpace(*tokenID))
	if code, done := renderCLIRawDocument(stdout, stderr, flags.api.JSON, document); done {
		return code
	}
	fmt.Fprintf(stdout, "Revoked project automation token %s.\n", terminalSafeText(*tokenID))
	return 0
}

func projectAutomationTokenPath(projectID string) string {
	return "/api/v1alpha/projects/" + url.PathEscape(strings.TrimSpace(projectID)) + "/automation-tokens"
}

func newProjectTokenIdempotencyKey() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "cli-token-" + base64.RawURLEncoding.EncodeToString(value[:]), nil
}
