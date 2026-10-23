// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestCLIRootHelpGoldenContract(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := executeCLITest(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	for _, golden := range []string{
		"Deploy and operate applications on Midnight",
		"Usage:\n  midnight [flags]\n  midnight [command]",
		"Perform operator recovery and maintenance",
		"Generate shell completion scripts",
		"Query logs and metrics",
		"Print the Midnight CLI version",
		"--output string",
		"output format: table, json, jsonl, or name",
		"--no-input",
		"--no-color",
	} {
		if !strings.Contains(stdout, golden) {
			t.Errorf("root help does not contain golden fragment %q\nhelp:\n%s", golden, stdout)
		}
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCLICommandTreeCoversLegacyDispatch(t *testing.T) {
	t.Parallel()

	root := NewRootCommand(NewCLIExecutionContext(nil, io.Discard, io.Discard))
	got := make([]string, 0)
	for _, command := range root.Commands() {
		got = append(got, command.Name())
	}
	sort.Strings(got)
	want := []string{
		"admin", "audit", "cancel", "changeset", "completion", "config", "cron", "deploy",
		"domain", "environment", "github", "init", "invitation", "link", "login", "logout", "logs",
		"monitor", "observability", "open", "project", "redeploy", "remove",
		"restart", "rollback", "service", "status", "stop", "tcp-proxy", "template",
		"token", "unlink", "up", "usage", "variable", "version", "volume", "whoami",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("root commands = %#v, want %#v", got, want)
	}

	paths := []string{
		"audit verify", "admin migrate", "project list", "service config get",
		"service github repositories", "environment duplicate", "variable delete",
		"deploy events", "monitor channel create", "monitor incident list",
		"monitor delivery retry", "volume backup restore", "volume restore apply",
		"volume policy delete", "domain custom verify", "config sync", "changeset apply",
	}
	for _, path := range paths {
		if command, _, err := root.Find(strings.Fields(path)); err != nil || command.CommandPath() != "midnight "+path {
			t.Errorf("Find(%q) = command %q, err %v", path, command.CommandPath(), err)
		}
	}
}

func TestCLINestedHelpReturnsSuccessWithoutCallingHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"project", "--help"}, want: "Manage projects"},
		{args: []string{"service", "github", "--help"}, want: "Manage GitHub source links"},
		{args: []string{"volume", "backup", "create", "--help"}, want: "Create a volume backup"},
	}
	for _, test := range tests {
		test := test
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := executeCLITest(t, test.args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
			}
			if !strings.Contains(stdout, test.want) || !strings.Contains(stdout, "Usage:") {
				t.Fatalf("help missing %q or Usage:\n%s", test.want, stdout)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestCLIVersionCommandAndFlag(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, stdout, stderr := executeCLITest(t, args...)
		if code != 0 || stdout != version.Version+"\n" || stderr != "" {
			t.Errorf("ExecuteCLI(%q) = code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestCLICompletionShells(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shell string
		want  string
	}{
		{shell: "bash", want: "__start_midnight"},
		{shell: "zsh", want: "#compdef midnight"},
		{shell: "fish", want: "complete -c midnight"},
		{shell: "powershell", want: "Register-ArgumentCompleter"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.shell, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := executeCLITest(t, "completion", test.shell)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
			}
			if !strings.Contains(stdout, test.want) {
				t.Fatalf("completion does not contain %q", test.want)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestCLIUnknownCommandSuggestsAndUsesUsageExit(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := executeCLITest(t, "projec")
	if code != cliUsageExitCode {
		t.Fatalf("exit code = %d, want %d", code, cliUsageExitCode)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `unknown command "projec"`) || !strings.Contains(stderr, "project") {
		t.Fatalf("expected unknown-command suggestion, stderr=%q", stderr)
	}
}

func TestCLIStrictGroupAndVersionArguments(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"project", "unknown"}, {"version", "extra"}} {
		code, _, stderr := executeCLITest(t, args...)
		if code != cliUsageExitCode {
			t.Errorf("ExecuteCLI(%q) exit code = %d, want %d", args, code, cliUsageExitCode)
		}
		if stderr == "" {
			t.Errorf("ExecuteCLI(%q) produced no diagnostic", args)
		}
	}
}

func TestCLILegacyHandlerExitCodeIsPreserved(t *testing.T) {
	t.Parallel()

	execution := NewCLIExecutionContext(nil, io.Discard, io.Discard)
	command := buildCLICommand(cliCommandSpec{
		Use:   "fixture",
		Short: "fixture",
		Handler: func([]string, io.Writer, io.Writer) int {
			return 37
		},
	}, execution)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetContext(contextWithCLIExecution(context.Background(), execution))
	command.SetArgs(nil)
	err := command.Execute()
	var exitErr *CLIExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T %v, want *CLIExitError", err, err)
	}
	if exitErr.ExitCode() != 37 {
		t.Fatalf("exit code = %d, want 37", exitErr.ExitCode())
	}
}

func TestCLIFrameworkFlagsAfterLeafAreConsumedButNotLeaked(t *testing.T) {
	t.Parallel()

	var gotArgs []string
	execution := NewCLIExecutionContext(nil, io.Discard, io.Discard)
	command := buildCLICommand(cliCommandSpec{
		Use:   "fixture",
		Short: "fixture",
		Handler: func(args []string, _ io.Writer, _ io.Writer) int {
			gotArgs = append([]string(nil), args...)
			return 0
		},
	}, execution)
	command.SetContext(contextWithCLIExecution(context.Background(), execution))
	command.SetArgs([]string{"--output=jsonl", "--quiet", "--no-input", "--timeout", "5s", "--debug", "--no-color", "legacy-value"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"legacy-value"}) {
		t.Fatalf("legacy args = %#v, want only legacy-value", gotArgs)
	}
	want := CLIOptions{Output: "jsonl", Quiet: true, NoInput: true, Timeout: 5 * time.Second, Debug: true, NoColor: true}
	if !reflect.DeepEqual(execution.Options, want) {
		t.Fatalf("options = %#v, want %#v", execution.Options, want)
	}
}

func TestCLIFrameworkFlagsAreExtractedFromAnyPosition(t *testing.T) {
	t.Parallel()

	fixtureFlags := func() *flag.FlagSet {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.String("value", "", "legacy value")
		fs.String("project-id", "", "legacy id")
		fs.Bool("json", false, "legacy json")
		return fs
	}
	tests := []struct {
		name  string
		flags func() *flag.FlagSet
		args  []string
		want  []string
		opts  CLIOptions
	}{
		{
			name:  "framework flag after legacy flag with value",
			flags: fixtureFlags,
			args:  []string{"--project-id", "prj_1", "--output", "json"},
			want:  []string{"--project-id", "prj_1"},
			opts:  CLIOptions{Output: "json"},
		},
		{
			name:  "inline framework value between legacy flags",
			flags: fixtureFlags,
			args:  []string{"--project-id", "prj_1", "--output=jsonl", "--json", "--timeout", "5s"},
			want:  []string{"--project-id", "prj_1", "--json"},
			opts:  CLIOptions{Output: "jsonl", Timeout: 5 * time.Second},
		},
		{
			name:  "framework flag after positional",
			flags: fixtureFlags,
			args:  []string{"NAME", "--quiet"},
			want:  []string{"NAME"},
			opts:  CLIOptions{Output: "table", Quiet: true},
		},
		{
			name:  "known value-taking legacy flag keeps a framework-looking value",
			flags: fixtureFlags,
			args:  []string{"--value", "--quiet", "NAME"},
			want:  []string{"--value", "--quiet", "NAME"},
			opts:  CLIOptions{Output: "table"},
		},
		{
			name:  "known bool legacy flag does not swallow the next token",
			flags: fixtureFlags,
			args:  []string{"--json", "--output", "json"},
			want:  []string{"--json"},
			opts:  CLIOptions{Output: "json"},
		},
		{
			name:  "single-dash legacy flag with value",
			flags: fixtureFlags,
			args:  []string{"-project-id", "--quiet", "--no-color"},
			want:  []string{"-project-id", "--quiet"},
			opts:  CLIOptions{Output: "table", NoColor: true},
		},
		{
			name: "unknown flagset extracts framework flags anywhere",
			args: []string{"--project-id", "prj_1", "--output", "json", "NAME"},
			want: []string{"--project-id", "prj_1", "NAME"},
			opts: CLIOptions{Output: "json"},
		},
		{
			name:  "terminator protects framework-looking positional",
			flags: fixtureFlags,
			args:  []string{"--", "--quiet"},
			want:  []string{"--", "--quiet"},
			opts:  CLIOptions{Output: "table"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			execution := NewCLIExecutionContext(nil, io.Discard, io.Discard)
			var got []string
			command := buildCLICommand(cliCommandSpec{
				Use:   "fixture",
				Flags: test.flags,
				Handler: func(args []string, _, _ io.Writer) int {
					got = append([]string(nil), args...)
					return 0
				},
			}, execution)
			command.SetContext(contextWithCLIExecution(context.Background(), execution))
			command.SetArgs(test.args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("legacy args = %#v, want %#v", got, test.want)
			}
			if execution.Options != test.opts {
				t.Fatalf("framework options = %#v, want %#v", execution.Options, test.opts)
			}
		})
	}
}

func TestCLIGlobalOutputFlagAcceptedAfterCommandFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1alpha/services" || r.URL.Query().Get("project_id") != "prj_1" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"svc_1","project_id":"prj_1","slug":"web","display_name":"Web"}]`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	code, stdout, stderr := executeCLITest(t, "service", "list", "--project-id", "prj_1", "--output", "json")
	if code != 0 {
		t.Fatalf("exit code = %d stderr=%q", code, stderr)
	}
	var values []map[string]any
	if err := json.Unmarshal([]byte(stdout), &values); err != nil || len(values) != 1 || values[0]["id"] != "svc_1" {
		t.Fatalf("stdout = %q, want a JSON array with svc_1 (err=%v)", stdout, err)
	}
}

func TestCLILeafHelpListsCommandFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"deploy", "image", "--help"}, want: []string{"--service-id string", "--image string", "--route-public", "--idempotency-key string", "--api-base string"}},
		{args: []string{"up", "-h"}, want: []string{"--detach", "--service-id string", "--environment-id string", "--message string"}},
		{args: []string{"init", "--help"}, want: []string{"--name string", "--service-name string", "--environment-name string", "--workspace-id string"}},
		{args: []string{"status", "--help"}, want: []string{"--json"}},
		{args: []string{"service", "delete", "--help"}, want: []string{"--yes", "--idempotency-key string"}},
		{args: []string{"config", "apply", "--help"}, want: []string{"--file string", "--idempotency-key string", "--api-base string"}},
		{args: []string{"changeset", "apply", "--help"}, want: []string{"--environment-id string", "--revision int", "--idempotency-key string"}},
		{args: []string{"monitor", "create", "--help"}, want: []string{"--signal string", "--threshold float", "--window-seconds int"}},
		{args: []string{"volume", "policy", "create", "--help"}, want: []string{"--schedule string", "--retention-days int"}},
		{args: []string{"observability", "logs", "--help"}, want: []string{"--search string", "--surface string", "--limit int"}},
		{args: []string{"cron", "list", "--help"}, want: []string{"--service-id string", "--environment-id string", "--state string", "--limit int"}},
		{args: []string{"token", "create", "--help"}, want: []string{"--name string", "--expires-in duration", "--project-id string"}},
		{args: []string{"admin", "migrate", "--help"}, want: []string{"--dsn string", "--dry-run", "--no-prompt"}},
		{args: []string{"domain", "custom", "add", "--help"}, want: []string{"--tls", "--idempotency-key string"}},
		{args: []string{"project", "create", "--help"}, want: []string{"--workspace-id string", "--name string"}},
		{args: []string{"variable", "set", "--help"}, want: []string{"--value string", "--stdin", "--expected-version int", "--scope string"}},
		{args: []string{"login", "--help"}, want: []string{"--email string", "--api-base string"}},
		{args: []string{"link", "--help"}, want: []string{"--project-id string", "--environment-id string", "--service-id string"}},
		{args: []string{"logs", "--help"}, want: []string{"--follow", "--limit int", "--build", "--operation-id string"}},
		{args: []string{"deploy", "logs", "--help"}, want: []string{"--operation-id string", "--limit int", "--surface string"}},
	}
	for _, test := range tests {
		test := test
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := executeCLITest(t, test.args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
			}
			for _, want := range test.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("help does not list %q:\n%s", want, stdout)
				}
			}
			if !strings.Contains(stdout, "Global Flags:") || !strings.Contains(stdout, "--output string") {
				t.Errorf("help lost the global flag section:\n%s", stdout)
			}
		})
	}
}

// TestEveryLeafCommandWithFlagsDocumentsThem walks the whole command tree and
// fails if any leaf parses flags it does not list in --help. The allowlist is
// the set of leaves that genuinely take no flags of their own.
func TestEveryLeafCommandWithFlagsDocumentsThem(t *testing.T) {
	t.Parallel()

	flagless := map[string]bool{
		"midnight unlink":                true,
		"midnight domain list":           true,
		"midnight domain get":            true,
		"midnight domain custom list":    true,
		"midnight help":                  true,
		"midnight version":               true,
		"midnight completion bash":       true,
		"midnight completion zsh":        true,
		"midnight completion fish":       true,
		"midnight completion powershell": true,
	}

	root := NewRootCommand(NewCLIExecutionContext(nil, io.Discard, io.Discard))
	var undocumented []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.HasSubCommands() {
			for _, child := range cmd.Commands() {
				walk(child)
			}
			return
		}
		path := cmd.CommandPath()
		if flagless[path] {
			return
		}
		documented := false
		cmd.LocalFlags().VisitAll(func(defined *pflag.Flag) {
			if defined.Name != "help" && !defined.Hidden {
				documented = true
			}
		})
		if !documented {
			undocumented = append(undocumented, path)
		}
	}
	walk(root)
	if len(undocumented) > 0 {
		t.Fatalf("leaf commands whose --help lists no flags of their own: %v", undocumented)
	}
}

// TestCLILeafHelpHidesCompatibilityAliases proves each concept reads one way
// in help while the historic spelling still parses.
func TestCLILeafHelpHidesCompatibilityAliases(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := executeCLITest(t, "up", "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	for _, hidden := range []string{"--env ", "--service ", "--endpoint "} {
		if strings.Contains(stdout, hidden) {
			t.Errorf("help still advertises the alias %q:\n%s", hidden, stdout)
		}
	}
}

// TestCLICompatibilityAliasesStillParse keeps existing scripts working.
func TestCLICompatibilityAliasesStillParse(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := defineUpFlags(fs)
	if err := fs.Parse([]string{"--env", "env_1", "--service", "web", "--endpoint", "https://example.test"}); err != nil {
		t.Fatalf("parse aliases: %v", err)
	}
	if *f.envOverride != "env_1" || *f.serviceOverride != "web" || *f.apiBase != "https://example.test" {
		t.Fatalf("aliases did not write through: env=%q service=%q api-base=%q",
			*f.envOverride, *f.serviceOverride, *f.apiBase)
	}
	if !flagSupplied(fs, flagEnvironmentID) || !flagSupplied(fs, flagServiceID) {
		t.Fatal("flagSupplied did not see the alias spellings")
	}
}

func TestCLINotConfiguredErrorsAreGuidingAndUseUsageExit(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, "")
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	t.Chdir(t.TempDir())

	code, _, stderr := executeCLITest(t, "status")
	if code != cliUsageExitCode {
		t.Fatalf("status exit code = %d, want %d; stderr=%q", code, cliUsageExitCode, stderr)
	}
	if strings.Contains(stderr, "cliconfig:") || !strings.Contains(stderr, "isn't linked to a service yet") || !strings.Contains(stderr, "`midnight link`") {
		t.Fatalf("status stderr = %q, want guiding not-linked message", stderr)
	}

	code, _, stderr = executeCLITest(t, "link")
	if code != cliUsageExitCode {
		t.Fatalf("link exit code = %d, want %d; stderr=%q", code, cliUsageExitCode, stderr)
	}
	if strings.Contains(stderr, "cliconfig:") || !strings.Contains(stderr, "No Midnight server configured") ||
		!strings.Contains(stderr, "midnight login --api-base") || !strings.Contains(stderr, cliconfig.EnvAPIBase) {
		t.Fatalf("link stderr = %q, want guiding no-server message", stderr)
	}
}

func TestCLIInvalidOutputReturnsUsage(t *testing.T) {
	t.Parallel()

	code, _, stderr := executeCLITest(t, "status", "--output", "xml")
	if code != cliUsageExitCode || !strings.Contains(stderr, `invalid --output "xml"`) {
		t.Fatalf("exit code = %d stderr = %q", code, stderr)
	}
}

func executeCLITest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := ExecuteCLI(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func contextWithCLIExecution(parent context.Context, execution *CLIExecutionContext) context.Context {
	return context.WithValue(parent, cliExecutionContextKey{}, execution)
}
