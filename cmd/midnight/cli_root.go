// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const cliUsageExitCode = 64

// CLIOptions are framework-wide options shared by every command. Legacy
// handlers do not receive these flags unless they already support the same
// option; new handlers can read them with CLIExecutionFrom.
type CLIOptions struct {
	Output  string
	Quiet   bool
	NoInput bool
	Yes     bool
	Timeout time.Duration
	Debug   bool
	NoColor bool
}

// CLIExecutionContext owns the process-independent inputs, outputs, and
// framework options for one CLI invocation.
type CLIExecutionContext struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Options CLIOptions
}

type cliExecutionContextKey struct{}

// NewCLIExecutionContext constructs an execution context suitable for tests,
// embedding, or the process entry point.
func NewCLIExecutionContext(stdin io.Reader, stdout, stderr io.Writer) *CLIExecutionContext {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &CLIExecutionContext{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Options: CLIOptions{Output: "table"},
	}
}

// CLIExecutionFrom returns the invocation context associated with cmd. It is
// the bridge for new IO-aware handlers introduced during the CLI migration.
func CLIExecutionFrom(cmd *cobra.Command) (*CLIExecutionContext, bool) {
	if cmd == nil {
		return nil, false
	}
	execution, ok := cmd.Context().Value(cliExecutionContextKey{}).(*CLIExecutionContext)
	return execution, ok
}

// CLIExitError carries a stable process exit code through Cobra's error path.
// Legacy handlers often print a precise diagnostic themselves, so Cause may
// intentionally be nil.
type CLIExitError struct {
	Code  int
	Cause error
}

func (e *CLIExitError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return fmt.Sprintf("command exited with status %d", e.Code)
}

func (e *CLIExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ExitCode returns a shell-compatible exit status.
func (e *CLIExitError) ExitCode() int {
	if e == nil || e.Code <= 0 {
		return 1
	}
	return e.Code
}

type legacyCLIHandler func(args []string, stdout, stderr io.Writer) int

type cliCommandSpec struct {
	Use      string
	Short    string
	Aliases  []string
	Children []cliCommandSpec
	Prefix   []string
	Handler  legacyCLIHandler
	// Flags optionally builds the stdlib FlagSet the handler parses, using the
	// same definition function the handler itself calls. It is used to list
	// the command's flags in --help and to keep framework flags such as
	// --output from consuming a legacy flag value.
	Flags func() *flag.FlagSet
}

// cliConfigExitCode maps local configuration errors (no server configured,
// directory not linked) to the usage exit code so every command reports the
// same status for the same class of problem. Other errors keep exit code 1.
func cliConfigExitCode(err error) int {
	if errors.Is(err, cliconfig.ErrNotLinked) || errors.Is(err, cliconfig.ErrNoAPIBase) {
		return cliUsageExitCode
	}
	return 1
}

// NewRootCommand builds the declarative command tree without executing it.
// Keeping construction separate makes help, completion, and handlers directly
// testable and lets main.go switch over in one small follow-up change.
func NewRootCommand(execution *CLIExecutionContext) *cobra.Command {
	if execution == nil {
		execution = NewCLIExecutionContext(nil, nil, nil)
	}

	root := &cobra.Command{
		Use:                        "midnight",
		Short:                      "Deploy and operate applications on Midnight",
		Version:                    version.Version,
		SilenceErrors:              true,
		SilenceUsage:               true,
		SuggestionsMinimumDistance: 3,
		Args:                       strictSubcommandArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetIn(execution.Stdin)
	root.SetOut(execution.Stdout)
	root.SetErr(execution.Stderr)
	root.SetContext(context.WithValue(context.Background(), cliExecutionContextKey{}, execution))
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &CLIExitError{Code: cliUsageExitCode, Cause: err}
	})

	flags := root.PersistentFlags()
	flags.StringVarP(&execution.Options.Output, "output", "o", "table", "output format: table, json, jsonl, or name")
	flags.BoolVarP(&execution.Options.Quiet, "quiet", "q", false, "print only the primary result")
	flags.BoolVar(&execution.Options.NoInput, "no-input", false, "never prompt for input")
	flags.BoolVarP(&execution.Options.Yes, "yes", "y", false, "approve confirmation prompts")
	flags.DurationVar(&execution.Options.Timeout, "timeout", 0, "maximum command duration (0 uses the command default)")
	flags.BoolVar(&execution.Options.Debug, "debug", false, "enable diagnostic output")
	flags.BoolVar(&execution.Options.NoColor, "no-color", false, "disable color output")

	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		return validateCLIOptions(&execution.Options)
	}

	for _, spec := range cliCommandSpecs() {
		root.AddCommand(buildCLICommand(spec, execution))
	}
	root.AddCommand(newVersionCommand())
	root.AddCommand(newCompletionCommand())
	return root
}

// ExecuteCLI runs a single invocation and converts framework and legacy errors
// into stable integer exit codes. It never calls os.Exit.
func ExecuteCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	execution := NewCLIExecutionContext(stdin, stdout, stderr)
	root := NewRootCommand(execution)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var exitErr *CLIExitError
	if errors.As(err, &exitErr) {
		if exitErr.Cause != nil {
			fmt.Fprintf(execution.Stderr, "midnight: %s\n", exitErr.Cause)
		}
		return exitErr.ExitCode()
	}
	fmt.Fprintf(execution.Stderr, "midnight: %s\n", err)
	return cliUsageExitCode
}

func validateCLIOptions(options *CLIOptions) error {
	if options.Timeout < 0 {
		return &CLIExitError{
			Code:  cliUsageExitCode,
			Cause: fmt.Errorf("--timeout must not be negative"),
		}
	}
	switch options.Output {
	case "table", "json", "jsonl", "name":
		return nil
	default:
		return &CLIExitError{
			Code:  cliUsageExitCode,
			Cause: fmt.Errorf("invalid --output %q (want table, json, jsonl, or name)", options.Output),
		}
	}
}

func buildCLICommand(spec cliCommandSpec, execution *CLIExecutionContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Aliases: append([]string(nil), spec.Aliases...),
	}
	if len(spec.Children) > 0 {
		cmd.Args = strictSubcommandArgs
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		}
		for _, child := range spec.Children {
			cmd.AddCommand(buildCLICommand(child, execution))
		}
		return cmd
	}

	cmd.DisableFlagParsing = true
	cmd.Args = cobra.ArbitraryArgs
	var legacyFlags *flag.FlagSet
	if spec.Flags != nil {
		legacyFlags = spec.Flags()
		registerLegacyFlagsForHelp(cmd, legacyFlags)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		legacyArgs, changed, err := consumeFrameworkArgs(args, &execution.Options, legacyFlags)
		if err != nil {
			return &CLIExitError{Code: cliUsageExitCode, Cause: err}
		}
		if hasHelpArg(legacyArgs) {
			return cmd.Help()
		}
		if hasVersionArg(legacyArgs) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Version)
			return nil
		}
		if err := validateCLIOptions(&execution.Options); err != nil {
			return err
		}
		legacyArgs = forwardSupportedFrameworkArgs(cmd, legacyArgs, changed, execution.Options)
		output := &cliOutputWriter{Writer: execution.Stdout, execution: execution}
		code := spec.Handler(append(append([]string(nil), spec.Prefix...), legacyArgs...), output, execution.Stderr)
		if code != 0 {
			return &CLIExitError{Code: code}
		}
		return nil
	}
	return cmd
}

// consumeFrameworkArgs extracts framework flags (--output, --quiet, --timeout,
// ...) from any position in a leaf command's arguments and returns the
// remaining tokens for the legacy handler in their original order. A `--`
// terminator ends framework parsing. When the handler's FlagSet is known, a
// token following a value-taking legacy flag is preserved verbatim even if it
// looks like a framework flag (for example `--value --quiet`).
func consumeFrameworkArgs(args []string, options *CLIOptions, legacy *flag.FlagSet) ([]string, map[string]bool, error) {
	changed := make(map[string]bool)
	var remaining []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		name, inline, value, recognized := splitFrameworkArg(arg)
		if !recognized {
			remaining = append(remaining, arg)
			if legacyFlagTakesValue(legacy, arg) && i+1 < len(args) {
				i++
				remaining = append(remaining, args[i])
			}
			continue
		}
		changed[name] = true
		switch name {
		case "output":
			if !inline {
				i++
				if i >= len(args) {
					return nil, nil, fmt.Errorf("flag needs an argument: --output")
				}
				value = args[i]
			}
			options.Output = value
		case "timeout":
			if !inline {
				i++
				if i >= len(args) {
					return nil, nil, fmt.Errorf("flag needs an argument: --timeout")
				}
				value = args[i]
			}
			duration, err := time.ParseDuration(value)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid argument %q for --timeout: %w", value, err)
			}
			options.Timeout = duration
		default:
			boolValue := true
			if inline {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					return nil, nil, fmt.Errorf("invalid argument %q for --%s", value, name)
				}
				boolValue = parsed
			}
			setCLIFrameworkBool(options, name, boolValue)
		}
	}
	return remaining, changed, nil
}

// legacyFlagTakesValue reports whether arg names a non-boolean flag in the
// handler's FlagSet written without an inline `=value`, meaning the next
// token is that flag's value.
func legacyFlagTakesValue(legacy *flag.FlagSet, arg string) bool {
	if legacy == nil || !strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
		return false
	}
	name := strings.TrimLeft(arg, "-")
	if name == "" {
		return false
	}
	defined := legacy.Lookup(name)
	if defined == nil {
		return false
	}
	return !isBoolFlag(defined)
}

func isBoolFlag(defined *flag.Flag) bool {
	boolean, ok := defined.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

// legacyFlagValue adapts a stdlib flag value so cobra can list it in help
// and shell completion. Leaf commands disable cobra flag parsing, so these
// definitions are never used to parse arguments.
type legacyFlagValue struct {
	inner    flag.Value
	typeName string
}

func (v *legacyFlagValue) String() string {
	if v == nil || v.inner == nil {
		return ""
	}
	return v.inner.String()
}

func (v *legacyFlagValue) Set(value string) error { return v.inner.Set(value) }

func (v *legacyFlagValue) Type() string { return v.typeName }

func registerLegacyFlagsForHelp(cmd *cobra.Command, legacy *flag.FlagSet) {
	if cmd == nil || legacy == nil {
		return
	}
	legacy.VisitAll(func(defined *flag.Flag) {
		if cmd.Flags().Lookup(defined.Name) != nil {
			return
		}
		_, isAlias := defined.Value.(cliAliasFlag)
		typeName, _ := flag.UnquoteUsage(defined)
		boolean := isBoolFlag(defined)
		if boolean {
			typeName = "bool"
		}
		value := &legacyFlagValue{inner: defined.Value, typeName: typeName}
		// Never register a shorthand: single-letter legacy names such as
		// deploy logs --q would collide with root shorthands like -q.
		registered := cmd.Flags().VarPF(value, defined.Name, "", defined.Usage)
		registered.DefValue = defined.DefValue
		if boolean {
			registered.NoOptDefVal = "true"
		}
		// Compatibility spellings stay parseable but never appear in help,
		// so each concept reads exactly one way.
		registered.Hidden = isAlias
	})
	cmd.Flags().SortFlags = true
}

var _ pflag.Value = (*legacyFlagValue)(nil)

func splitFrameworkArg(arg string) (name string, inline bool, value string, recognized bool) {
	if arg == "-q" {
		return "quiet", false, "", true
	}
	if arg == "-y" {
		return "yes", false, "", true
	}
	if strings.HasPrefix(arg, "-o=") {
		return "output", true, strings.TrimPrefix(arg, "-o="), true
	}
	if arg == "-o" {
		return "output", false, "", true
	}
	if !strings.HasPrefix(arg, "--") {
		return "", false, "", false
	}
	value = ""
	name = strings.TrimPrefix(arg, "--")
	if before, after, found := strings.Cut(name, "="); found {
		name, value, inline = before, after, true
	}
	switch name {
	case "output", "quiet", "no-input", "yes", "timeout", "debug", "no-color":
		return name, inline, value, true
	default:
		return "", false, "", false
	}
}

func setCLIFrameworkBool(options *CLIOptions, name string, value bool) {
	switch name {
	case "quiet":
		options.Quiet = value
	case "no-input":
		options.NoInput = value
	case "yes":
		options.Yes = value
	case "debug":
		options.Debug = value
	case "no-color":
		options.NoColor = value
	}
}

func forwardSupportedFrameworkArgs(cmd *cobra.Command, args []string, changed map[string]bool, options CLIOptions) []string {
	path := cmd.CommandPath()
	if path == "midnight admin migrate" {
		if changed["yes"] || cmd.Flags().Changed("yes") {
			args = append(args, "--yes="+strconv.FormatBool(options.Yes))
		}
		if changed["timeout"] || cmd.Flags().Changed("timeout") {
			args = append(args, "--timeout="+options.Timeout.String())
		}
	}
	if path == "midnight login" && (changed["timeout"] || cmd.Flags().Changed("timeout")) {
		args = append(args, "--timeout="+options.Timeout.String())
	}
	return args
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func hasVersionArg(args []string) bool {
	return len(args) == 1 && args[0] == "--version"
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Midnight CLI version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	}
}

func newCompletionCommand() *cobra.Command {
	completion := &cobra.Command{
		Use:   "completion",
		Short: "Generate shell completion scripts",
		Args:  strictSubcommandArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	generators := []struct {
		name     string
		generate func(*cobra.Command, io.Writer) error
	}{
		{name: "bash", generate: func(root *cobra.Command, out io.Writer) error { return root.GenBashCompletion(out) }},
		{name: "zsh", generate: func(root *cobra.Command, out io.Writer) error { return root.GenZshCompletion(out) }},
		{name: "fish", generate: func(root *cobra.Command, out io.Writer) error { return root.GenFishCompletion(out, true) }},
		{name: "powershell", generate: func(root *cobra.Command, out io.Writer) error { return root.GenPowerShellCompletion(out) }},
	}
	for _, generator := range generators {
		generator := generator
		completion.AddCommand(&cobra.Command{
			Use:   generator.name,
			Short: "Generate completion for " + generator.name,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return generator.generate(cmd.Root(), cmd.OutOrStdout())
			},
		})
	}
	return completion
}

func strictSubcommandArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("unknown command %q for %q%s", args[0], cmd.CommandPath(), cliSuggestions(cmd, args[0]))
}

func cliSuggestions(cmd *cobra.Command, typed string) string {
	suggestions := cmd.SuggestionsFor(typed)
	if len(suggestions) == 0 {
		return ""
	}
	return "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
}

func configHandler(args []string, stdout, stderr io.Writer) int {
	if err := runConfig(args, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func changesetHandler(args []string, stdout, stderr io.Writer) int {
	if err := runChangeset(args, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// commandFlags builds a help/lookup copy of a handler's stdlib FlagSet from
// the definition function the handler itself uses, so the two never drift.
func commandFlags[T any](name string, define func(*flag.FlagSet) T) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		define(fs)
		return fs
	}
}

// apiCommandFlags is commandFlags for handlers built on newAPIFlagSet, which
// adds the shared --api-base, --token, and --json flags.
func apiCommandFlags[T any](name string, define func(*flag.FlagSet) T) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs, _ := newAPIFlagSet(name, io.Discard)
		define(fs)
		return fs
	}
}

// apiOnlyFlags describes a command whose only flags are the shared API flags.
func apiOnlyFlags(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs, _ := newAPIFlagSet(name, io.Discard)
		return fs
	}
}

func cliCommandSpecs() []cliCommandSpec {
	leaf := func(use, short string, prefix []string, handler legacyCLIHandler) cliCommandSpec {
		return cliCommandSpec{Use: use, Short: short, Prefix: prefix, Handler: handler}
	}
	leafFlags := func(use, short string, prefix []string, handler legacyCLIHandler, flags func() *flag.FlagSet) cliCommandSpec {
		return cliCommandSpec{Use: use, Short: short, Prefix: prefix, Handler: handler, Flags: flags}
	}
	group := func(use, short string, children ...cliCommandSpec) cliCommandSpec {
		return cliCommandSpec{Use: use, Short: short, Children: children}
	}

	return []cliCommandSpec{
		leafFlags("login", "Log in with a human session", nil, runLogin, commandFlags("midnight login", defineLoginFlags)),
		leafFlags("logout", "Log out of the current human session", nil, runLogout, commandFlags("midnight logout", defineLogoutFlags)),
		leafFlags("whoami", "Show the authenticated identity", nil, runWhoami, commandFlags("midnight whoami", defineWhoamiFlags)),
		leafFlags("init", "Create or select a project, service, and environment, then link this directory", nil, runInit, commandFlags("midnight init", defineInitFlags)),
		leafFlags("link", "Link the current directory to a project", nil, runLink, commandFlags("midnight link", defineLinkFlags)),
		leaf("unlink", "Unlink the current directory", nil, runUnlink),
		leafFlags("up", "Build and deploy the current directory", nil, runUp, commandFlags("midnight up", defineUpFlags)),
		leafFlags("logs", "Read or follow deployment logs", nil, runLogs, commandFlags("midnight logs", defineLogsFlags)),
		leafFlags("redeploy", "Redeploy an existing deployment", nil, runRedeploy, commandFlags("midnight redeploy", defineRedeployFlags)),
		leafFlags("rollback", "Roll back to an earlier deployment", nil, runRollback, commandFlags("midnight rollback", defineRollbackFlags)),
		leafFlags("restart", "Restart a deployment", nil, func(args []string, stdout, stderr io.Writer) int {
			return runRuntimeAction("restart", args, stdout, stderr)
		}, commandFlags("midnight restart", defineRuntimeActionFlags)),
		leafFlags("stop", "Stop a deployment", nil, func(args []string, stdout, stderr io.Writer) int {
			return runRuntimeAction("stop", args, stdout, stderr)
		}, commandFlags("midnight stop", defineRuntimeActionFlags)),
		leafFlags("remove", "Remove a deployment", nil, func(args []string, stdout, stderr io.Writer) int {
			return runRuntimeAction("remove", args, stdout, stderr)
		}, commandFlags("midnight remove", defineRuntimeActionFlags)),
		leafFlags("cancel", "Cancel an in-progress operation", nil, runCancel, commandFlags("midnight cancel", defineCancelFlags)),
		leafFlags("status", "Show the linked context, the serving release, and any in-flight rollout", nil, runStatus, commandFlags("midnight status", defineStatusFlags)),
		leafFlags("open", "Open the linked project in a browser", nil, runOpen, commandFlags("midnight open", defineOpenFlags)),
		leafFlags("usage", "Show workspace usage and policy coverage", nil, runUsage, apiCommandFlags("midnight usage", defineUsageFlags)),

		group("audit", "Verify the audit trail",
			leafFlags("verify", "Verify audit records and signatures", []string{"verify"}, runAudit, commandFlags("midnight audit verify", defineAuditVerifyFlags))),
		group("admin", "Perform operator recovery and maintenance",
			leafFlags("create-user", "Create the first human administrator", []string{"create-user"}, runAdmin, apiCommandFlags("midnight admin create-user", defineAdminCreateUserFlags)),
			leafFlags("reset-password", "Reset an administrator password", []string{"reset-password"}, runAdmin, apiCommandFlags("midnight admin reset-password", defineAdminResetPasswordFlags)),
			leafFlags("migrate", "Apply database migrations", []string{"migrate"}, runAdmin, commandFlags("midnight admin migrate", defineAdminMigrateFlags)),
			leafFlags("authorize-cli", "Authorize an operator recovery credential", []string{"authorize-cli"}, runAdmin, commandFlags("midnight admin authorize-cli", defineAdminAuthorizeCLIFlags))),
		group("project", "Manage projects",
			leafFlags("list", "List projects", []string{"list"}, runProject, apiCommandFlags("midnight project list", defineProjectListFlags)),
			leafFlags("create", "Create a project", []string{"create"}, runProject, apiCommandFlags("midnight project create", defineProjectCreateFlags)),
			leafFlags("delete", "Delete a project", []string{"delete"}, runProject, apiCommandFlags("midnight project delete", defineProjectDeleteFlags))),
		group("token", "Manage project automation tokens",
			leafFlags("list", "List project automation tokens", []string{"list"}, runProjectToken, projectTokenCommandFlags("midnight token list", nil)),
			leafFlags("create", "Create a project automation token", []string{"create"}, runProjectToken, projectTokenCommandFlags("midnight token create", func(fs *flag.FlagSet) {
				defineProjectTokenCreateFlags(fs)
			})),
			leafFlags("revoke", "Revoke a project automation token", []string{"revoke"}, runProjectToken, projectTokenCommandFlags("midnight token revoke", func(fs *flag.FlagSet) {
				defineProjectTokenRevokeFlags(fs)
			}))),
		group("invitation", "Manage workspace invitations",
			leafFlags("list", "List invitations", []string{"list"}, runInvitation, apiCommandFlags("midnight invitation list", defineInvitationListFlags)),
			leafFlags("create", "Create an invitation", []string{"create"}, runInvitation, apiCommandFlags("midnight invitation create", defineInvitationCreateFlags)),
			leafFlags("revoke", "Revoke an invitation", []string{"revoke"}, runInvitation, apiCommandFlags("midnight invitation revoke", defineInvitationRevokeFlags)),
			leafFlags("reissue", "Reissue an invitation", []string{"reissue"}, runInvitation, apiCommandFlags("midnight invitation reissue", defineInvitationReissueFlags)),
			leafFlags("accept", "Accept an invitation", []string{"accept"}, runInvitation, apiCommandFlags("midnight invitation accept", defineInvitationAcceptFlags))),
		group("github", "Install the GitHub App",
			leafFlags("setup", "Start a verified workspace installation", []string{"setup"}, runGitHub, apiCommandFlags("midnight github setup", defineGitHubSetupFlags))),
		group("service", "Manage services",
			leafFlags("list", "List services", []string{"list"}, runService, apiCommandFlags("midnight service list", defineServiceListFlags)),
			leafFlags("create", "Create a service", []string{"create"}, runService, apiCommandFlags("midnight service create", defineServiceCreateFlags)),
			leafFlags("delete", "Delete a service", []string{"delete"}, runService, apiCommandFlags("midnight service delete", defineServiceDeleteFlags)),
			group("config", "Manage service configuration",
				leafFlags("get", "Get service configuration", []string{"config", "get"}, runService, apiCommandFlags("midnight service config get", defineServiceConfigGetFlags)),
				leafFlags("put", "Replace service configuration", []string{"config", "put"}, runService, apiCommandFlags("midnight service config put", defineServiceConfigPutFlags))),
			group("github", "Manage GitHub source links",
				leafFlags("repositories", "List accessible GitHub repositories", []string{"github", "repositories"}, runService, githubSourceCommandFlags("midnight service github repositories", nil)),
				leafFlags("branches", "List GitHub repository branches", []string{"github", "branches"}, runService, githubSourceCommandFlags("midnight service github branches", func(fs *flag.FlagSet) {
					defineGitHubRepositoryIDFlag(fs)
				})),
				leafFlags("get", "Show a service GitHub source link", []string{"github", "get"}, runService, apiOnlyFlags("midnight service github get")),
				leafFlags("link", "Link a service to a GitHub repository", []string{"github", "link"}, runService, apiCommandFlags("midnight service github link", defineServiceGitHubLinkFlags)),
				leafFlags("unlink", "Remove a service GitHub source link", []string{"github", "unlink"}, runService, apiCommandFlags("midnight service github unlink", defineServiceGitHubUnlinkFlags)))),
		group("environment", "Manage environments",
			leafFlags("list", "List environments", []string{"list"}, runEnvironment, apiCommandFlags("midnight environment list", defineEnvironmentListFlags)),
			leafFlags("create", "Create an environment", []string{"create"}, runEnvironment, apiCommandFlags("midnight environment create", defineEnvironmentCreateFlags)),
			leafFlags("update", "Update an environment", []string{"update"}, runEnvironment, apiCommandFlags("midnight environment update", defineEnvironmentUpdateFlags)),
			leafFlags("delete", "Delete an environment", []string{"delete"}, runEnvironment, apiCommandFlags("midnight environment delete", defineEnvironmentDeleteFlags)),
			leafFlags("duplicate", "Duplicate an environment", []string{"duplicate"}, runEnvironment, apiCommandFlags("midnight environment duplicate", defineEnvironmentDuplicateFlags))),
		group("variable", "Manage variables",
			leafFlags("list", "List variables", []string{"list"}, runVariable, apiCommandFlags("midnight variable list", defineVariableListFlags)),
			leafFlags("set", "Set a variable", []string{"set"}, runVariable, apiCommandFlags("midnight variable set", defineVariableSetFlags)),
			leafFlags("update", "Update a variable", []string{"update"}, runVariable, apiCommandFlags("midnight variable update", defineVariableUpdateFlags)),
			cliCommandSpec{
				Use:     "delete",
				Short:   "Delete a variable",
				Aliases: []string{"unset"},
				Prefix:  []string{"delete"},
				Handler: runVariable,
				Flags:   apiCommandFlags("midnight variable delete", defineVariableDeleteFlags),
			}),
		group("deploy", "Deploy images and inspect operations",
			leafFlags("image", "Deploy an image", []string{"image"}, runDeploy, apiCommandFlags("midnight deploy image", defineDeployImageFlags)),
			leafFlags("status", "Show deployment operation status", []string{"status"}, runDeploy, apiCommandFlags("midnight deploy status", defineDeployStatusFlags)),
			leafFlags("events", "Read deployment operation events", []string{"events"}, runDeploy, apiCommandFlags("midnight deploy events", defineDeployEventsFlags)),
			leafFlags("logs", "Read deployment operation logs", []string{"logs"}, runDeploy, apiCommandFlags("midnight deploy logs", defineDeployLogsFlags))),
		group("observability", "Query logs and metrics",
			leafFlags("logs", "Query structured logs", []string{"logs"}, runObservability, apiCommandFlags("midnight observability logs", defineObservabilityLogsFlags)),
			leafFlags("metrics", "Query service metrics", []string{"metrics"}, runObservability, apiCommandFlags("midnight observability metrics", defineObservabilityMetricsFlags))),
		group("monitor", "Manage monitors and notifications",
			leafFlags("list", "List monitors", []string{"list"}, runMonitor, apiCommandFlags("midnight monitor list", defineMonitorListFlags)),
			leafFlags("create", "Create a monitor", []string{"create"}, runMonitor, apiCommandFlags("midnight monitor create", defineMonitorCreateFlags)),
			leafFlags("delete", "Delete a monitor", []string{"delete"}, runMonitor, apiCommandFlags("midnight monitor delete", defineMonitorDeleteFlags)),
			leafFlags("subscribe", "Subscribe a channel to a monitor", []string{"subscribe"}, runMonitor, apiCommandFlags("midnight monitor subscribe", defineMonitorSubscribeFlags)),
			group("channel", "Manage notification channels",
				leafFlags("list", "List notification channels", []string{"channel", "list"}, runMonitor, apiCommandFlags("midnight monitor channel list", defineMonitorChannelListFlags)),
				leafFlags("create", "Create a notification channel", []string{"channel", "create"}, runMonitor, apiCommandFlags("midnight monitor channel create", defineMonitorChannelCreateFlags))),
			group("incident", "Inspect monitor incidents",
				leafFlags("list", "List monitor incidents", []string{"incident", "list"}, runMonitor, apiCommandFlags("midnight monitor incident list", defineMonitorIncidentListFlags))),
			group("delivery", "Inspect and retry notification deliveries",
				leafFlags("list", "List notification deliveries", []string{"delivery", "list"}, runMonitor, apiCommandFlags("midnight monitor delivery list", defineMonitorDeliveryListFlags)),
				leafFlags("retry", "Retry a notification delivery", []string{"delivery", "retry"}, runMonitor, apiCommandFlags("midnight monitor delivery retry", defineMonitorDeliveryRetryFlags)))),
		group("tcp-proxy", "Manage TCP proxy endpoints",
			leafFlags("list", "List TCP proxy endpoints", []string{"list"}, runTCPProxy, apiCommandFlags("midnight tcp-proxy list", defineTCPProxyListFlags)),
			leafFlags("create", "Create a TCP proxy endpoint", []string{"create"}, runTCPProxy, apiCommandFlags("midnight tcp-proxy create", defineTCPProxyCreateFlags)),
			leafFlags("status", "Show TCP proxy endpoint status", []string{"status"}, runTCPProxy, apiOnlyFlags("midnight tcp-proxy status")),
			leafFlags("withdraw", "Withdraw a TCP proxy endpoint", []string{"withdraw"}, runTCPProxy, apiCommandFlags("midnight tcp-proxy withdraw", defineTCPProxyWithdrawFlags))),
		group("cron", "Inspect cron occurrences",
			leafFlags("list", "List cron occurrences", []string{"list"}, runCron, apiCommandFlags("midnight cron list", defineCronFlags)),
			leafFlags("get", "Get a cron occurrence", []string{"get"}, runCron, apiCommandFlags("midnight cron get", defineCronFlags))),
		group("volume", "Manage persistent volumes and recovery",
			leafFlags("list", "List volumes", []string{"list"}, runVolume, apiCommandFlags("midnight volume list", defineVolumeListFlags)),
			leafFlags("create", "Create a volume", []string{"create"}, runVolume, apiCommandFlags("midnight volume create", defineVolumeCreateFlags)),
			group("backup", "Manage volume backups",
				leafFlags("list", "List volume backups", []string{"backup", "list"}, runVolume, apiOnlyFlags("midnight volume backup list")),
				leafFlags("create", "Create a volume backup", []string{"backup", "create"}, runVolume, apiCommandFlags("midnight volume backup create", defineVolumeBackupCreateFlags)),
				leafFlags("restore", "Stage a backup restore", []string{"backup", "restore"}, runVolume, apiCommandFlags("midnight volume backup restore", defineVolumeBackupRestoreFlags))),
			group("restore", "Inspect and apply volume restores",
				leafFlags("list", "List volume restores", []string{"restore", "list"}, runVolume, apiOnlyFlags("midnight volume restore list")),
				leafFlags("apply", "Apply a staged volume restore", []string{"restore", "apply"}, runVolume, apiCommandFlags("midnight volume restore apply", defineVolumeRestoreApplyFlags))),
			group("policy", "Manage volume backup policies",
				leafFlags("list", "List volume backup policies", []string{"policy", "list"}, runVolume, apiOnlyFlags("midnight volume policy list")),
				leafFlags("create", "Create a volume backup policy", []string{"policy", "create"}, runVolume, apiCommandFlags("midnight volume policy create", defineVolumePolicyCreateFlags)),
				leafFlags("delete", "Delete a volume backup policy", []string{"policy", "delete"}, runVolume, apiCommandFlags("midnight volume policy delete", defineVolumePolicyDeleteFlags)))),
		group("template", "Deploy and inspect service templates",
			leafFlags("list", "List templates", []string{"list"}, runTemplate, apiOnlyFlags("midnight template list")),
			leafFlags("deploy", "Deploy a template", []string{"deploy"}, runTemplate, apiCommandFlags("midnight template deploy", defineTemplateDeployFlags)),
			leafFlags("status", "Show template deployment status", []string{"status"}, runTemplate, apiCommandFlags("midnight template status", defineTemplateStatusFlags))),
		group("config", "Validate, compare, and apply config-as-code",
			leafFlags("validate", "Validate a configuration document", []string{"validate"}, configHandler, commandFlags("midnight config validate", defineConfigFlags)),
			leafFlags("diff", "Compare desired and current configuration", []string{"diff"}, configHandler, commandFlags("midnight config diff", defineConfigFlags)),
			leafFlags("apply", "Apply a configuration document", []string{"apply"}, configHandler, commandFlags("midnight config apply", defineConfigFlags)),
			leafFlags("sync", "Synchronize a configuration document", []string{"sync"}, configHandler, commandFlags("midnight config sync", defineConfigFlags))),
		group("changeset", "Review, apply, or discard saved environment changes",
			leafFlags("get", "Show the environment's saved changes", []string{"get"}, changesetHandler, commandFlags("midnight changeset get", defineChangesetFlags)),
			leafFlags("validate", "Recheck the environment's saved changes", []string{"validate"}, changesetHandler, commandFlags("midnight changeset validate", defineChangesetFlags)),
			leafFlags("apply", "Apply the environment's saved changes", []string{"apply"}, changesetHandler, commandFlags("midnight changeset apply", defineChangesetFlags)),
			leafFlags("discard", "Discard the environment's saved changes", []string{"discard"}, changesetHandler, commandFlags("midnight changeset discard", defineChangesetFlags))),
		group("domain", "Manage generated and custom domains",
			leaf("list", "List generated domains", []string{"list"}, runDomain),
			leafFlags("add", "Add a generated domain", []string{"add"}, runDomain, commandFlags("midnight domain add", defineDomainAddFlags)),
			leaf("get", "Get a generated domain", []string{"get"}, runDomain),
			cliCommandSpec{
				Use:     "delete",
				Short:   "Delete a generated domain",
				Aliases: []string{"remove"},
				Prefix:  []string{"delete"},
				Handler: runDomain,
				Flags:   commandFlags("midnight domain delete", defineDomainVersionedFlags),
			},
			group("custom", "Manage custom domains",
				leaf("list", "List custom domains", []string{"custom", "list"}, runDomain),
				leafFlags("add", "Add a custom domain", []string{"custom", "add"}, runDomain, commandFlags("midnight domain custom add", defineDomainCustomAddFlags)),
				leafFlags("verify", "Verify a custom domain", []string{"custom", "verify"}, runDomain, commandFlags("midnight domain custom verify", defineDomainVersionedFlags)),
				leafFlags("delete", "Delete a custom domain", []string{"custom", "delete"}, runDomain, commandFlags("midnight domain custom delete", defineDomainVersionedFlags)))),
	}
}
