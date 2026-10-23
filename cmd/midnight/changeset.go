// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/midnight/midnight/internal/cliconfig"
)

// changesetFlags are the flags every `midnight changeset` subcommand accepts.
type changesetFlags struct {
	environmentID *string
	apiBase       *string
	token         *string
	key           *string
	revision      *int64
}

func defineChangesetFlags(fs *flag.FlagSet) changesetFlags {
	return changesetFlags{
		environmentID: cliEnvironmentIDFlag(fs, "environment whose changeset is being reviewed"),
		apiBase:       cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		token:         fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
		key:           cliIdempotencyKeyFlag(fs, "idempotency key; required for changeset apply and discard"),
		revision:      fs.Int64("revision", 0, "positive changeset revision the transition expects"),
	}
}

func runChangeset(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: midnight changeset get|validate|apply|discard --environment-id <id> [--revision <n>] [--idempotency-key <key>]")
	}
	command := args[0]
	switch command {
	case "get", "validate", "apply", "discard":
	default:
		return fmt.Errorf("unknown changeset command %q", command)
	}
	fs := flag.NewFlagSet("midnight changeset "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := defineChangesetFlags(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("midnight changeset %s takes no positional arguments", command)
	}
	environmentID := strings.TrimSpace(*f.environmentID)
	endpoint := strings.TrimRight(strings.TrimSpace(*f.apiBase), "/")
	token := strings.TrimSpace(*f.token)
	key := strings.TrimSpace(*f.key)
	revision := *f.revision
	revisionSet := flagSupplied(fs, "revision")
	if revisionSet && revision < 1 {
		return fmt.Errorf("--revision must be a positive integer")
	}
	if environmentID == "" {
		return fmt.Errorf("--environment-id is required")
	}
	if command != "get" && !revisionSet {
		return fmt.Errorf("--revision is required for changeset %s", command)
	}
	if (command == "apply" || command == "discard") && key == "" {
		return fmt.Errorf("--idempotency-key is required for changeset %s", command)
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{APIBase: endpoint, Token: token})
	if err != nil {
		return err
	}
	path := "/api/v1alpha/environments/" + url.PathEscape(environmentID) + "/changeset"
	method := http.MethodGet
	var body []byte
	switch command {
	case "get":
	case "validate", "apply", "discard":
		method = http.MethodPost
		path += "/" + command
		body, err = json.Marshal(map[string]int64{"expected_revision": revision})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown changeset command %q", command)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, credentials.APIBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if credentials.Token != "" {
		req.Header.Set("Authorization", "Bearer "+credentials.Token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if command == "apply" || command == "discard" {
		req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	response, responseBody, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiStatusError(method, req.URL.RequestURI(), response.Status, responseBody)
	}
	_, _ = stdout.Write(responseBody)
	if len(responseBody) == 0 || responseBody[len(responseBody)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return nil
}
