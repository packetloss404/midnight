// Command midnight is the operator-facing CLI surface.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/pkg/configcode"
)

func main() {
	os.Exit(ExecuteCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// configFlags are the flags every `midnight config` subcommand accepts. They
// live in a FlagSet like every other command so --help, shell completion, and
// the framework's flag handling all see the same definitions.
type configFlags struct {
	file    *string
	apiBase *string
	token   *string
	key     *string
}

func defineConfigFlags(fs *flag.FlagSet) configFlags {
	return configFlags{
		file:    fs.String("file", "", "path to the configuration document"),
		apiBase: cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		token:   fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
		key:     cliIdempotencyKeyFlag(fs, "idempotency key; required for config apply and config sync"),
	}
}

func runConfig(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: midnight config validate|diff|apply|sync --file <path> [--api-base <url>] [--idempotency-key <key>] [--token <bearer>]")
	}
	command := args[0]
	switch command {
	case "validate", "diff", "apply", "sync":
	default:
		return fmt.Errorf("unknown config command %q", command)
	}
	fs := flag.NewFlagSet("midnight config "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := defineConfigFlags(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("midnight config %s takes no positional arguments", command)
	}
	file := strings.TrimSpace(*f.file)
	endpoint := strings.TrimRight(strings.TrimSpace(*f.apiBase), "/")
	key := strings.TrimSpace(*f.key)
	token := strings.TrimSpace(*f.token)
	if file == "" {
		return fmt.Errorf("--file is required")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if command == "sync" {
		var document configcode.SyncDocument
		if err := json.Unmarshal(body, &document); err != nil {
			return err
		}
		if err := configcode.ValidateSync(document); err != nil {
			return err
		}
		if key == "" {
			return fmt.Errorf("--idempotency-key is required for config sync")
		}
		credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{APIBase: endpoint, Token: token})
		if err != nil {
			return err
		}
		return postConfigWithIfMatch(stdout, credentials.APIBase+"/api/v1alpha/config/sync", body, key, credentials.Token, strconv.FormatInt(*document.ExpectedRevision, 10))
	}
	var doc configcode.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	if err := configcode.Validate(doc); err != nil {
		return err
	}
	switch command {
	case "validate":
		fmt.Fprintln(stdout, "valid")
		return nil
	case "diff":
		if endpoint != "" {
			credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
				APIBase: endpoint,
				Token:   token,
			})
			if err != nil {
				return err
			}
			return postConfig(stdout, credentials.APIBase+"/api/v1alpha/config/diff", body, "", credentials.Token)
		}
		plan, err := configcode.Diff(doc, configcode.State{})
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(plan)
	default:
		if key == "" {
			return fmt.Errorf("--idempotency-key is required for config apply")
		}
		credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
			APIBase: endpoint,
			Token:   token,
		})
		if err != nil {
			return err
		}
		return postConfig(stdout, credentials.APIBase+"/api/v1alpha/config/apply", body, key, credentials.Token)
	}
}

func postConfig(stdout io.Writer, url string, body []byte, idempotencyKey string, token string) error {
	return postConfigWithIfMatch(stdout, url, body, idempotencyKey, token, "")
}

func postConfigWithIfMatch(stdout io.Writer, url string, body []byte, idempotencyKey string, token string, ifMatch string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	res, responseBody, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodPost, req.URL.RequestURI(), res.Status, responseBody)
	}
	_, _ = stdout.Write(responseBody)
	if len(responseBody) == 0 || responseBody[len(responseBody)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return nil
}
