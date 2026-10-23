package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/registry"
	"github.com/midnight/midnight/pkg/midnightctl"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	envAPIBase                       = cliconfig.EnvAPIBase
	envBaseURL                       = cliconfig.EnvBaseURL
	envAPIToken                      = cliconfig.EnvAPIToken
	environmentUpdateBodyLimitBytes  = 16 << 10
	serviceConfigPutBodyLimitBytes   = 32 << 10
	serviceConfigExpectedVersionName = "expected_version"
	variableValueLimitBytes          = 64 << 10
	humanVariableNameLimitBytes      = 255
)

var environmentUpdateSlugPattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type apiCommandConfig struct {
	BaseURL string
	Token   string
	JSON    bool
}

type cliAPIClient struct {
	baseURL        string
	token          string
	credentialKind cliconfig.CredentialKind
	client         *http.Client
}

type cliProject struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Slug        string    `json:"slug"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type cliService struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Slug        string    `json:"slug"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type cliEnvironmentDuplicateOmission struct {
	Resource string `json:"resource"`
	Count    int    `json:"count"`
	Reason   string `json:"reason"`
}

type cliEnvironmentDuplicateResult struct {
	cliEnvironment
	CopyConfig             bool                              `json:"copy_config"`
	CopiedServiceConfigs   int                               `json:"copied_service_configs"`
	CopiedVariables        int                               `json:"copied_variables"`
	CopiedVariableVersions int                               `json:"copied_variable_versions"`
	OmittedResources       []cliEnvironmentDuplicateOmission `json:"omitted_resources"`
}

type cliEnvironmentUpdateRequest struct {
	ExpectedVersion int64   `json:"expected_version"`
	Slug            *string `json:"slug,omitempty"`
	Name            *string `json:"name,omitempty"`
	Tier            *string `json:"tier,omitempty"`
}

type cliServiceConfig struct {
	ProjectID             string    `json:"project_id"`
	ServiceID             string    `json:"service_id"`
	EnvironmentID         string    `json:"environment_id"`
	Version               int64     `json:"version"`
	Explicit              bool      `json:"explicit"`
	RootDirectory         string    `json:"root_directory"`
	Builder               string    `json:"builder"`
	DockerfilePath        string    `json:"dockerfile_path"`
	BuildCommand          string    `json:"build_command"`
	StartCommand          string    `json:"start_command"`
	PredeployCommand      string    `json:"predeploy_command"`
	TargetPort            int       `json:"target_port"`
	HealthKind            string    `json:"health_kind"`
	HealthPath            string    `json:"health_path"`
	HealthPort            int       `json:"health_port"`
	HealthTimeoutSeconds  int64     `json:"health_timeout_seconds"`
	HealthIntervalSeconds int64     `json:"health_interval_seconds"`
	RestartPolicy         string    `json:"restart_policy"`
	RestartRetries        int       `json:"restart_retries"`
	Replicas              int       `json:"replicas"`
	OverlapEnabled        bool      `json:"overlap_enabled"`
	DrainGraceSeconds     int64     `json:"drain_grace_seconds"`
	RuntimeKind           string    `json:"runtime_kind"`
	CronSchedule          string    `json:"cron_schedule"`
	CronTimeoutSeconds    int64     `json:"cron_timeout_seconds"`
	CronMaxAttempts       int       `json:"cron_max_attempts"`
	CronRetryDelaySeconds int64     `json:"cron_retry_delay_seconds"`
	CreatedAt             time.Time `json:"created_at,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
}

type cliPutServiceConfigRequest struct {
	ExpectedVersion       int64  `json:"expected_version"`
	RootDirectory         string `json:"root_directory"`
	Builder               string `json:"builder"`
	DockerfilePath        string `json:"dockerfile_path"`
	BuildCommand          string `json:"build_command"`
	StartCommand          string `json:"start_command"`
	PredeployCommand      string `json:"predeploy_command"`
	TargetPort            int    `json:"target_port"`
	HealthKind            string `json:"health_kind"`
	HealthPath            string `json:"health_path"`
	HealthPort            int    `json:"health_port"`
	HealthTimeoutSeconds  int64  `json:"health_timeout_seconds"`
	HealthIntervalSeconds int64  `json:"health_interval_seconds"`
	RestartPolicy         string `json:"restart_policy"`
	RestartRetries        int    `json:"restart_retries"`
	Replicas              int    `json:"replicas"`
	OverlapEnabled        bool   `json:"overlap_enabled"`
	DrainGraceSeconds     int64  `json:"drain_grace_seconds"`
	RuntimeKind           string `json:"runtime_kind"`
	CronSchedule          string `json:"cron_schedule"`
	CronTimeoutSeconds    int64  `json:"cron_timeout_seconds"`
	CronMaxAttempts       int    `json:"cron_max_attempts"`
	CronRetryDelaySeconds int64  `json:"cron_retry_delay_seconds"`
}

type cliVariable struct {
	ID             string                 `json:"id"`
	Scope          string                 `json:"scope"`
	WorkspaceID    string                 `json:"workspace_id"`
	ProjectID      string                 `json:"project_id"`
	EnvironmentID  string                 `json:"environment_id"`
	ServiceID      string                 `json:"service_id"`
	Name           string                 `json:"name"`
	Version        int                    `json:"version"`
	LastRotatedAt  time.Time              `json:"last_rotated_at"`
	LengthBucket   string                 `json:"length_bucket"`
	KEKFingerprint string                 `json:"kek_fingerprint"`
	ValueKind      string                 `json:"value_kind"`
	References     []cliVariableReference `json:"references"`
}

type cliVariableReference struct {
	Kind    string `json:"kind"`
	Service string `json:"service"`
	Name    string `json:"name"`
}

type variableSetRequest struct {
	Name            string `json:"name"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ServiceID       string `json:"service_id,omitempty"`
	Scope           string `json:"scope"`
	Value           string `json:"value"`
	ExpectedVersion *int   `json:"expected_version,omitempty"`
}

type variableDeleteRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	EnvironmentID   string `json:"environment_id"`
	ServiceID       string `json:"service_id"`
	ExpectedVersion int    `json:"expected_version"`
}

type humanVariableCreateRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	Value string `json:"value"`
}

type humanVariableUpdateRequest struct {
	ExpectedVersion int     `json:"expected_version"`
	Value           string  `json:"value"`
	Scope           *string `json:"scope,omitempty"`
}

type humanVariableDeleteRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type deployImageRequest struct {
	EnvironmentID string            `json:"environment_id,omitempty"`
	Image         string            `json:"image,omitempty"`
	DeploymentID  string            `json:"deployment_id,omitempty"`
	InstanceID    string            `json:"instance_id,omitempty"`
	Route         *deployRouteFlags `json:"route,omitempty"`
}

// tenantDeployImageRequest mirrors the deliberately narrow human-session
// admission contract. Keep this separate from deployImageRequest so a future
// field added for the operator surface cannot silently cross the tenant API.
type tenantDeployImageRequest struct {
	EnvironmentID string                       `json:"environment_id"`
	Image         string                       `json:"image"`
	Route         *tenantGeneratedRouteRequest `json:"route,omitempty"`
}

// tenantGeneratedRouteRequest deliberately has no host, path, group, name, or
// TLS fields. A human may opt into only the server-derived generated HTTP
// route; custom routing remains on the separately authorized operator surface.
type tenantGeneratedRouteRequest struct {
	Public bool `json:"public"`
}

type deployRouteFlags struct {
	Name   string `json:"name,omitempty"`
	Group  string `json:"group,omitempty"`
	Host   string `json:"host,omitempty"`
	Path   string `json:"path,omitempty"`
	Public bool   `json:"public"`
	TLS    bool   `json:"tls"`
}

type cliErrorResponse struct {
	Error orailtypes.ErrorEnvelope `json:"error"`
}

type cliAPIStatusFailure struct {
	Method  string
	Path    string
	Status  string
	Code    string
	Message string
	Text    string
}

func (e *cliAPIStatusFailure) Error() string {
	switch {
	case e.Code != "":
		return fmt.Sprintf("%s %s failed: %s: %s: %s", safeHTTPText(e.Method), safeHTTPText(e.Path), safeHTTPText(e.Status), safeHTTPText(e.Code), safeHTTPText(e.Message))
	case e.Text != "":
		return fmt.Sprintf("%s %s failed: %s: %s", safeHTTPText(e.Method), safeHTTPText(e.Path), safeHTTPText(e.Status), safeHTTPText(e.Text))
	default:
		return fmt.Sprintf("%s %s failed: %s", safeHTTPText(e.Method), safeHTTPText(e.Path), safeHTTPText(e.Status))
	}
}

type cliResponseDecodeFailure struct {
	Err error
}

func (e *cliResponseDecodeFailure) Error() string {
	return e.Err.Error()
}

func (e *cliResponseDecodeFailure) Unwrap() error {
	return e.Err
}

func runProject(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printProjectUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runProjectList(args[1:], stdout, stderr)
	case "create":
		return runProjectCreate(args[1:], stdout, stderr)
	case "delete":
		return runProjectDelete(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight project: unknown subcommand %q\n", args[0])
		printProjectUsage(stderr)
		return 64
	}
}

func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printServiceUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runServiceList(args[1:], stdout, stderr)
	case "create":
		return runServiceCreate(args[1:], stdout, stderr)
	case "delete":
		return runServiceDelete(args[1:], stdout, stderr)
	case "config":
		return runServiceConfig(args[1:], stdout, stderr)
	case "github":
		return runServiceGitHub(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight service: unknown subcommand %q\n", args[0])
		printServiceUsage(stderr)
		return 64
	}
}

func runEnvironment(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printEnvironmentUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runEnvironmentList(args[1:], stdout, stderr)
	case "create":
		return runEnvironmentCreate(args[1:], stdout, stderr)
	case "update":
		return runEnvironmentUpdate(args[1:], stdout, stderr)
	case "delete":
		return runEnvironmentDelete(args[1:], stdout, stderr)
	case "duplicate":
		return runEnvironmentDuplicate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight environment: unknown subcommand %q\n", args[0])
		printEnvironmentUsage(stderr)
		return 64
	}
}

func runEnvironmentList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight environment list", stderr)
	f := defineEnvironmentListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment list: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/environments"
	if strings.TrimSpace(*f.projectID) != "" {
		path += "?project_id=" + url.QueryEscape(strings.TrimSpace(*f.projectID))
	}
	var values []cliEnvironment
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &values)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, values,
		func(value cliEnvironment) string { return value.ID },
		writeEnvironmentTable,
	)
}

type environmentListFlags struct {
	projectID *string
}

func defineEnvironmentListFlags(fs *flag.FlagSet) environmentListFlags {
	return environmentListFlags{
		projectID: cliProjectIDFlag(fs, "filter environments by project id"),
	}
}

func runEnvironmentCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight environment create", stderr)
	f := defineEnvironmentCreateFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*f.projectID) == "" {
		fmt.Fprintln(stderr, "usage: midnight environment create --project-id ID [--name NAME] [--tier TIER] [--protected] [--idempotency-key KEY] <slug>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment create: %s\n", err)
		return 64
	}
	body := map[string]any{"project_id": *f.projectID, "slug": fs.Arg(0), "name": *f.name, "tier": *f.tier, "is_protected": *f.protected}
	resolvedKey, err := resolveMutationIdempotencyKey(*f.key, client.token, "environment-create", http.MethodPost, "/api/v1alpha/environments", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment create: prepare retry-safe request: %s\n", err)
		return 64
	}
	var value cliEnvironment
	raw, err := client.doJSON(http.MethodPost, "/api/v1alpha/environments", body, resolvedKey, &value)
	if err != nil {
		writeMutationFailure(stderr, "environment create", err, resolvedKey)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, value,
		func(value cliEnvironment) string { return value.ID },
		func(output io.Writer, value cliEnvironment) {
			fmt.Fprintf(output, "environment created: %s (%s)\n", value.ID, value.Slug)
		},
	)
}

type environmentCreateFlags struct {
	projectID *string
	name      *string
	tier      *string
	protected *bool
	key       *string
}

func defineEnvironmentCreateFlags(fs *flag.FlagSet) environmentCreateFlags {
	return environmentCreateFlags{
		projectID: cliProjectIDFlag(fs, "owning project id"),
		name:      fs.String("name", "", "display name"),
		tier:      fs.String("tier", "development", "production|staging|preview|development"),
		protected: fs.Bool("protected", false, "protect from deletion"),
		key:       fs.String("idempotency-key", "", "idempotency key for the create request"),
	}
}

func runEnvironmentUpdate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight environment update", stderr)
	f := defineEnvironmentUpdateFlags(fs)
	expectedVersion, slug, name, tier, key := f.expectedVersion, f.slug, f.name, f.tier, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight environment update --expected-version N [--slug SLUG] [--name NAME] [--tier TIER] [--idempotency-key KEY] <environment-id>")
		return 64
	}
	environmentID := strings.TrimSpace(fs.Arg(0))
	if environmentID == "" {
		fmt.Fprintln(stderr, "midnight environment update: environment id is required")
		return 64
	}
	if *expectedVersion < 1 {
		fmt.Fprintln(stderr, "midnight environment update: --expected-version must be positive")
		return 64
	}

	present := make(map[string]bool, 3)
	fs.Visit(func(value *flag.Flag) {
		switch value.Name {
		case "slug", "name", "tier":
			present[value.Name] = true
		}
	})
	if len(present) == 0 {
		fmt.Fprintln(stderr, "midnight environment update: at least one of --slug, --name, or --tier is required")
		return 64
	}
	request := cliEnvironmentUpdateRequest{ExpectedVersion: *expectedVersion}
	if present["slug"] {
		value := strings.TrimSpace(*slug)
		if !environmentUpdateSlugPattern.MatchString(value) {
			fmt.Fprintln(stderr, "midnight environment update: --slug must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters")
			return 64
		}
		request.Slug = &value
	}
	if present["name"] {
		value := strings.TrimSpace(*name)
		if value == "" {
			fmt.Fprintln(stderr, "midnight environment update: --name cannot be blank")
			return 64
		}
		if utf8.RuneCountInString(value) > 200 {
			fmt.Fprintln(stderr, "midnight environment update: --name must be at most 200 characters")
			return 64
		}
		request.Name = &value
	}
	if present["tier"] {
		value := strings.TrimSpace(*tier)
		if !validEnvironmentUpdateTier(value) {
			fmt.Fprintln(stderr, "midnight environment update: --tier must be production, staging, preview, or development")
			return 64
		}
		request.Tier = &value
	}
	canonicalBody, err := json.Marshal(request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment update: encode request: %s\n", err)
		return 64
	}
	if len(canonicalBody) > environmentUpdateBodyLimitBytes {
		fmt.Fprintf(stderr, "midnight environment update: request body exceeds %d bytes\n", environmentUpdateBodyLimitBytes)
		return 64
	}
	if strings.TrimSpace(*key) == "" {
		*key = deterministicEnvironmentUpdateIdempotencyKey(environmentID, request)
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment update: %s\n", err)
		return 64
	}
	if client.credentialKind != cliconfig.CredentialKindHumanSession {
		fmt.Fprintln(stderr, "midnight environment update: environment update requires a human session; run `midnight login`")
		return 64
	}
	var value cliEnvironment
	raw, err := client.doJSON(
		http.MethodPatch,
		environmentUpdateAPIPath(environmentID),
		request,
		strings.TrimSpace(*key),
		&value,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment update: %s\n", err)
		return 1
	}
	if err := validateEnvironmentUpdateResponse(environmentID, request, value); err != nil {
		fmt.Fprintf(stderr, "midnight environment update: invalid update response: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "environment updated: %s (%s), version %d\n", value.ID, value.Slug, value.ResourceVersion)
	return 0
}

type environmentUpdateFlags struct {
	expectedVersion *int64
	slug            *string
	name            *string
	tier            *string
	key             *string
}

func defineEnvironmentUpdateFlags(fs *flag.FlagSet) environmentUpdateFlags {
	return environmentUpdateFlags{
		expectedVersion: fs.Int64("expected-version", 0, "required positive resource version returned by environment list/get"),
		slug:            fs.String("slug", "", "new environment slug"),
		name:            fs.String("name", "", "new display name"),
		tier:            fs.String("tier", "", "production|staging|preview|development"),
		key:             cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact update intent when omitted)"),
	}
}

func validateEnvironmentUpdateResponse(
	environmentID string,
	request cliEnvironmentUpdateRequest,
	value cliEnvironment,
) error {
	if value.ID != strings.TrimSpace(environmentID) {
		return fmt.Errorf("environment id %q does not match requested id %q", value.ID, strings.TrimSpace(environmentID))
	}
	if value.ResourceVersion != request.ExpectedVersion+1 {
		return fmt.Errorf(
			"resource_version %d does not follow expected_version %d",
			value.ResourceVersion,
			request.ExpectedVersion,
		)
	}
	if request.Slug != nil && value.Slug != *request.Slug {
		return fmt.Errorf("slug %q does not match requested slug %q", value.Slug, *request.Slug)
	}
	if request.Name != nil && value.Name != *request.Name {
		return fmt.Errorf("name %q does not match requested name %q", value.Name, *request.Name)
	}
	if request.Tier != nil && value.Tier != *request.Tier {
		return fmt.Errorf("tier %q does not match requested tier %q", value.Tier, *request.Tier)
	}
	return nil
}

func environmentUpdateAPIPath(environmentID string) string {
	return "/api/v1alpha/environments/" + url.PathEscape(strings.TrimSpace(environmentID))
}

func deterministicEnvironmentUpdateIdempotencyKey(
	environmentID string,
	request cliEnvironmentUpdateRequest,
) string {
	body, _ := json.Marshal(request)
	digest := sha256.New()
	for _, component := range [][]byte{
		[]byte("midnight-environment-update-v1"),
		[]byte(strings.TrimSpace(environmentID)),
		body,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(component))
		_, _ = digest.Write(component)
		_, _ = digest.Write([]byte{0})
	}
	return fmt.Sprintf("midnight-environment-update-v1-%x", digest.Sum(nil))
}

func validEnvironmentUpdateTier(tier string) bool {
	switch tier {
	case "production", "staging", "preview", "development":
		return true
	default:
		return false
	}
}

func runEnvironmentDelete(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight environment delete", stderr)
	f := defineEnvironmentDeleteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight environment delete [--yes] <environment-id>")
		return 64
	}
	environmentID := strings.TrimSpace(fs.Arg(0))
	if code := confirmCLIDestruction(stdout, stderr, "midnight environment delete", "environment", environmentID, *f.yes); code != 0 {
		return code
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment delete: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodDelete, "/api/v1alpha/environments/"+url.PathEscape(environmentID), nil, "", nil); err != nil {
		fmt.Fprintf(stderr, "midnight environment delete: %s\n", describeCLIDeleteError("environment", err))
		return 1
	}
	fmt.Fprintf(stdout, "environment deleted: %s\n", environmentID)
	return 0
}

type environmentDeleteFlags struct {
	yes *bool
}

func defineEnvironmentDeleteFlags(fs *flag.FlagSet) environmentDeleteFlags {
	return environmentDeleteFlags{
		yes: fs.Bool("yes", false, "skip the interactive confirmation prompt"),
	}
}

func runEnvironmentDuplicate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight environment duplicate", stderr)
	f := defineEnvironmentDuplicateFlags(fs)
	slug, name, copyConfig, idempotencyKey := f.slug, f.name, f.copyConfig, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*slug) == "" {
		fmt.Fprintln(stderr, "usage: midnight environment duplicate --slug NEW_SLUG [--name NAME] [--copy-config --idempotency-key KEY] <source-environment-id>")
		return 64
	}
	if *copyConfig && strings.TrimSpace(*idempotencyKey) == "" {
		fmt.Fprintln(stderr, "midnight environment duplicate: --idempotency-key is required with --copy-config")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment duplicate: %s\n", err)
		return 64
	}
	request := struct {
		Slug       string `json:"slug"`
		Name       string `json:"name,omitempty"`
		CopyConfig bool   `json:"copy_config,omitempty"`
	}{Slug: strings.TrimSpace(*slug), Name: strings.TrimSpace(*name), CopyConfig: *copyConfig}
	var value cliEnvironmentDuplicateResult
	raw, err := client.doJSON(http.MethodPost, "/api/v1alpha/environments/"+url.PathEscape(fs.Arg(0))+"/duplicate", request, strings.TrimSpace(*idempotencyKey), &value)
	if err != nil {
		fmt.Fprintf(stderr, "midnight environment duplicate: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "environment duplicated: %s (%s)\n", value.ID, value.Slug)
	if value.CopyConfig {
		fmt.Fprintf(
			stdout,
			"copied: %d service config(s), %d variable(s), %d variable version(s)\n",
			value.CopiedServiceConfigs,
			value.CopiedVariables,
			value.CopiedVariableVersions,
		)
		for _, omission := range value.OmittedResources {
			if omission.Count == 0 {
				continue
			}
			fmt.Fprintf(stdout, "not copied: %s (%d) — %s\n", omission.Resource, omission.Count, omission.Reason)
		}
	}
	return 0
}

type environmentDuplicateFlags struct {
	slug       *string
	name       *string
	copyConfig *bool
	key        *string
}

func defineEnvironmentDuplicateFlags(fs *flag.FlagSet) environmentDuplicateFlags {
	return environmentDuplicateFlags{
		slug:       fs.String("slug", "", "new environment slug"),
		name:       fs.String("name", "", "new display name"),
		copyConfig: fs.Bool("copy-config", false, "transactionally copy service config and re-encrypted variables"),
		key:        cliIdempotencyKeyFlag(fs, "required replay key when --copy-config is set"),
	}
}

func runVariable(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printVariableUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runVariableList(args[1:], stdout, stderr)
	case "set":
		return runVariableSet(args[1:], stdout, stderr)
	case "update":
		return runVariableUpdate(args[1:], stdout, stderr)
	case "delete", "unset":
		return runVariableDelete(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight variable: unknown subcommand %q\n", args[0])
		printVariableUsage(stderr)
		return 64
	}
}

func runDeploy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printDeployUsage(stderr)
		return 64
	}
	switch args[0] {
	case "image":
		return runDeployImage(args[1:], stdout, stderr)
	case "status":
		return runDeployStatus(args[1:], stdout, stderr)
	case "events":
		return runDeployEvents(args[1:], stdout, stderr)
	case "logs":
		return runDeployLogs(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight deploy: unknown subcommand %q\n", args[0])
		printDeployUsage(stderr)
		return 64
	}
}

func runProjectList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight project list", stderr)
	defineProjectListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight project list [--api-base URL] [--token TOKEN] [--output table|json]")
		return 64
	}
	client, err := newGeneratedCLIReadClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project list: %s\n", err)
		return 64
	}
	var projects []midnightctl.Project
	ctx, cancel := cliOutputContext(stdout)
	defer cancel()
	raw, err := executeGeneratedCLIRead(
		func() (*http.Response, error) { return client.client.ListProjects(ctx) },
		http.MethodGet,
		"/api/v1alpha/projects",
		&projects,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, projects,
		func(value midnightctl.Project) string { return value.Id },
		func(output io.Writer, values []midnightctl.Project) {
			writeProjectTable(output, generatedProjectsForTable(values))
		},
	)
}

func runProjectCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight project create", stderr)
	f := defineProjectCreateFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*f.workspaceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight project create --workspace-id ID [--name NAME] [--idempotency-key KEY] <slug>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project create: %s\n", err)
		return 64
	}
	body := map[string]string{"workspace_id": strings.TrimSpace(*f.workspaceID), "slug": fs.Arg(0), "display_name": *f.name}
	resolvedKey, err := resolveMutationIdempotencyKey(*f.key, client.token, "project-create", http.MethodPost, "/api/v1alpha/projects", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight project create: prepare retry-safe request: %s\n", err)
		return 64
	}
	var project cliProject
	raw, err := client.doJSON(http.MethodPost, "/api/v1alpha/projects", body, resolvedKey, &project)
	if err != nil {
		writeMutationFailure(stderr, "project create", err, resolvedKey)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, project,
		func(value cliProject) string { return value.ID },
		func(output io.Writer, value cliProject) {
			fmt.Fprintf(output, "project created: %s (%s)\n", value.ID, value.Slug)
		},
	)
}

// projectListFlags exists so `midnight project list --help` documents the
// same framework flags the handler accepts.
type projectListFlags struct{}

func defineProjectListFlags(_ *flag.FlagSet) projectListFlags {
	return projectListFlags{}
}

type projectCreateFlags struct {
	workspaceID *string
	name        *string
	key         *string
}

func defineProjectCreateFlags(fs *flag.FlagSet) projectCreateFlags {
	return projectCreateFlags{
		workspaceID: fs.String("workspace-id", "", "owning workspace id"),
		name:        fs.String("name", "", "project display name"),
		key:         fs.String("idempotency-key", "", "idempotency key for the create request"),
	}
}

func runServiceList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service list", stderr)
	f := defineServiceListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	client, err := newGeneratedCLIReadClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service list: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/services"
	var params *midnightctl.ListServicesParams
	if strings.TrimSpace(*f.projectID) != "" {
		value := midnightctl.ServiceProjectFilter(strings.TrimSpace(*f.projectID))
		params = &midnightctl.ListServicesParams{ProjectId: &value}
		path += "?project_id=" + url.QueryEscape(string(value))
	}
	var services []midnightctl.Service
	ctx, cancel := cliOutputContext(stdout)
	defer cancel()
	raw, err := executeGeneratedCLIRead(
		func() (*http.Response, error) { return client.client.ListServices(ctx, params) },
		http.MethodGet,
		path,
		&services,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, services,
		func(value midnightctl.Service) string { return value.Id },
		func(output io.Writer, values []midnightctl.Service) {
			writeServiceTable(output, generatedServicesForTable(values))
		},
	)
}

type serviceListFlags struct {
	projectID *string
}

func defineServiceListFlags(fs *flag.FlagSet) serviceListFlags {
	return serviceListFlags{
		projectID: cliProjectIDFlag(fs, "filter services by project id"),
	}
}

func runServiceCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service create", stderr)
	f := defineServiceCreateFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*f.projectID) == "" {
		fmt.Fprintln(stderr, "usage: midnight service create --project-id <id> [--name NAME] [--idempotency-key KEY] <slug>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service create: %s\n", err)
		return 64
	}
	body := map[string]string{"project_id": *f.projectID, "slug": fs.Arg(0), "display_name": *f.name}
	resolvedKey, err := resolveMutationIdempotencyKey(*f.key, client.token, "service-create", http.MethodPost, "/api/v1alpha/services", body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service create: prepare retry-safe request: %s\n", err)
		return 64
	}
	var service cliService
	raw, err := client.doJSON(http.MethodPost, "/api/v1alpha/services", body, resolvedKey, &service)
	if err != nil {
		writeMutationFailure(stderr, "service create", err, resolvedKey)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, service,
		func(value cliService) string { return value.ID },
		func(output io.Writer, value cliService) {
			fmt.Fprintf(output, "service created: %s (%s)\n", value.ID, value.Slug)
		},
	)
}

type serviceCreateFlags struct {
	projectID *string
	name      *string
	key       *string
}

func defineServiceCreateFlags(fs *flag.FlagSet) serviceCreateFlags {
	return serviceCreateFlags{
		projectID: cliProjectIDFlag(fs, "parent project id"),
		name:      fs.String("name", "", "service display name"),
		key:       fs.String("idempotency-key", "", "idempotency key for the create request"),
	}
}

func runServiceConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printServiceConfigUsage(stderr)
		return 64
	}
	switch args[0] {
	case "get":
		return runServiceConfigGet(args[1:], stdout, stderr)
	case "put":
		return runServiceConfigPut(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight service config: unknown subcommand %q\n", args[0])
		printServiceConfigUsage(stderr)
		return 64
	}
}

func runServiceConfigGet(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service config get", stderr)
	f := defineServiceConfigGetFlags(fs)
	environmentID := f.environmentID
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*environmentID) == "" {
		fmt.Fprintln(stderr, "usage: midnight service config get --environment-id ID [--json] <service-id>")
		return 64
	}
	serviceID := strings.TrimSpace(fs.Arg(0))
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service config get: %s\n", err)
		return 64
	}
	path := serviceConfigAPIPath(serviceID, *environmentID)
	var value cliServiceConfig
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &value)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service config get: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeServiceConfig(stdout, value)
	return 0
}

func runServiceConfigPut(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight service config put", stderr)
	f := defineServiceConfigPutFlags(fs)
	environmentID, file, key := f.environmentID, f.file, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*environmentID) == "" || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(stderr, "usage: midnight service config put --environment-id ID --file PATH [--idempotency-key STABLE_RETRY_KEY] [--json] <service-id>")
		return 64
	}
	requestBody, err := os.ReadFile(strings.TrimSpace(*file))
	if err != nil {
		fmt.Fprintf(stderr, "midnight service config put: read %s: %s\n", *file, err)
		return 1
	}
	trimmedBody := bytes.TrimSpace(requestBody)
	if len(trimmedBody) == 0 || bytes.Equal(trimmedBody, []byte("null")) {
		fmt.Fprintf(stderr, "midnight service config put: decode %s: expected one non-null JSON object\n", *file)
		return 64
	}
	var request cliPutServiceConfigRequest
	decoder := json.NewDecoder(bytes.NewReader(requestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		fmt.Fprintf(stderr, "midnight service config put: decode %s: %s\n", *file, err)
		return 64
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "midnight service config put: decode %s: expected one JSON object\n", *file)
		return 64
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmedBody, &fields); err != nil || fields == nil {
		fmt.Fprintf(stderr, "midnight service config put: decode %s: expected one non-null JSON object\n", *file)
		return 64
	}
	expectedVersion, present := fields[serviceConfigExpectedVersionName]
	if !present || bytes.Equal(bytes.TrimSpace(expectedVersion), []byte("null")) {
		fmt.Fprintln(stderr, "midnight service config put: expected_version is required")
		return 64
	}
	if request.ExpectedVersion < 0 {
		fmt.Fprintln(stderr, "midnight service config put: expected_version must be non-negative")
		return 64
	}
	canonicalBody, _ := json.Marshal(request)
	if len(canonicalBody) > serviceConfigPutBodyLimitBytes {
		fmt.Fprintf(stderr, "midnight service config put: request body exceeds %d bytes\n", serviceConfigPutBodyLimitBytes)
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight service config put: %s\n", err)
		return 64
	}
	serviceID := strings.TrimSpace(fs.Arg(0))
	environment := strings.TrimSpace(*environmentID)
	if strings.TrimSpace(*key) == "" {
		*key = deterministicServiceConfigPutIdempotencyKey(serviceID, environment, request)
	}
	var saved cliServiceConfig
	// The service-config resource requires a strong If-Match validator, which
	// the generic JSON helper does not carry: sending expected_version alone
	// is refused with 428, so this command could never succeed through doJSON.
	if err := client.putServiceConfigIfMatch(
		serviceConfigAPIPath(serviceID, environment),
		request,
		*key,
		request.ExpectedVersion,
		&saved,
	); err != nil {
		fmt.Fprintf(stderr, "midnight service config put: %s\n", err)
		return 1
	}
	if cfg.JSON {
		encoded, marshalErr := json.MarshalIndent(saved, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(stderr, "midnight service config put: format result: %s\n", marshalErr)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}
	fmt.Fprintf(stdout, "service config saved: %s/%s (version %d)\n", saved.ServiceID, saved.EnvironmentID, saved.Version)
	return 0
}

type serviceConfigGetFlags struct {
	environmentID *string
}

func defineServiceConfigGetFlags(fs *flag.FlagSet) serviceConfigGetFlags {
	return serviceConfigGetFlags{
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
	}
}

type serviceConfigPutFlags struct {
	environmentID *string
	file          *string
	key           *string
}

func defineServiceConfigPutFlags(fs *flag.FlagSet) serviceConfigPutFlags {
	return serviceConfigPutFlags{
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		file:          fs.String("file", "", "JSON service-config request file"),
		key:           cliIdempotencyKeyFlag(fs, "stable retry key (generated from the exact config intent when omitted)"),
	}
}

func serviceConfigAPIPath(serviceID, environmentID string) string {
	return "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(serviceID)) +
		"/environments/" + url.PathEscape(strings.TrimSpace(environmentID)) + "/config"
}

func deterministicServiceConfigPutIdempotencyKey(
	serviceID,
	environmentID string,
	request cliPutServiceConfigRequest,
) string {
	body, _ := json.Marshal(request)
	digest := sha256.New()
	for _, component := range [][]byte{
		[]byte("midnight-service-config-put-v1"),
		[]byte(strings.TrimSpace(serviceID)),
		[]byte(strings.TrimSpace(environmentID)),
		body,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(component))
		_, _ = digest.Write(component)
		_, _ = digest.Write([]byte{0})
	}
	return fmt.Sprintf("midnight-service-config-put-v1-%x", digest.Sum(nil))
}

func runVariableList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight variable list", stderr)
	f := defineVariableListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight variable list [--project-id ID] [--environment-id ID] [--service-id ID] [--api-base URL] [--token TOKEN] [--json]")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable list: %s\n", err)
		return 64
	}
	human, err := variableCredentialMode(client)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable list: %s\n", err)
		return 64
	}
	var variables []cliVariable
	path := "/api/v1alpha/variables"
	if human {
		environment := strings.TrimSpace(*f.environmentID)
		service := strings.TrimSpace(*f.serviceID)
		if environment == "" || service == "" {
			fmt.Fprintln(stderr, "midnight variable list: --environment-id and --service-id are required for a human session")
			return 64
		}
		if strings.TrimSpace(*f.projectID) != "" {
			fmt.Fprintln(stderr, "midnight variable list: --project-id is not accepted with a human session; ownership is derived from the service and environment")
			return 64
		}
		path = humanVariableCollectionAPIPath(service, environment)
	} else {
		query := url.Values{}
		query.Set("project_id", strings.TrimSpace(*f.projectID))
		query.Set("environment_id", strings.TrimSpace(*f.environmentID))
		query.Set("service_id", strings.TrimSpace(*f.serviceID))
		for key, values := range query {
			if len(values) == 0 || values[0] == "" {
				query.Del(key)
			}
		}
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
	}
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &variables)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable list: %s\n", safeVariableAPIError(err, ""))
		return 1
	}
	if err := validateVariableMetadataResponse(raw); err != nil {
		fmt.Fprintf(stderr, "midnight variable list: invalid metadata response: %s\n", err)
		return 1
	}
	if human {
		for i, variable := range variables {
			if err := validateHumanVariableMetadata(variable, "", "", strings.TrimSpace(*f.serviceID), strings.TrimSpace(*f.environmentID), 0); err != nil {
				fmt.Fprintf(stderr, "midnight variable list: invalid metadata response at index %d: %s\n", i, err)
				return 1
			}
		}
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeVariableTable(stdout, variables)
	return 0
}

type variableListFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
}

func defineVariableListFlags(fs *flag.FlagSet) variableListFlags {
	return variableListFlags{
		projectID:     cliProjectIDFlag(fs, "filter by project id"),
		environmentID: cliEnvironmentIDFlag(fs, "filter by environment id"),
		serviceID:     cliServiceIDFlag(fs, "filter by service id"),
	}
}

func runVariableSet(args []string, stdout, stderr io.Writer) int {
	return runVariableSetWithInput(args, os.Stdin, stdout, stderr)
}

func runVariableSetWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight variable set", stderr)
	f := defineVariableSetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	suppliedFlags := make(map[string]bool)
	fs.Visit(func(flag *flag.Flag) {
		suppliedFlags[flag.Name] = true
	})
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight variable set [--scope plain|secret|sealed] [--workspace-id ID] [--project-id ID] [--environment-id ID] [--service-id ID] (--value VALUE | --stdin) <name>")
		return 64
	}
	resolvedValue, err := readVariableValue(stdin, *f.value, suppliedFlags["value"], *f.useStdin)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable set: %s\n", err)
		return 64
	}
	if suppliedFlags["expected-version"] && *f.expectedVersion < 1 {
		fmt.Fprintln(stderr, "midnight variable set: --expected-version must be at least 1 when supplied")
		return 64
	}
	switch strings.TrimSpace(*f.scope) {
	case "plain", "secret", "sealed":
	default:
		fmt.Fprintln(stderr, "midnight variable set: --scope must be one of plain, secret, sealed")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable set: %s\n", err)
		return 64
	}
	human, err := variableCredentialMode(client)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable set: %s\n", err)
		return 64
	}
	name := fs.Arg(0)
	var (
		request any
		path    = "/api/v1alpha/variables"
	)
	if human {
		if !validHumanVariableName(name) {
			fmt.Fprintln(stderr, "midnight variable set: human variable name must match [A-Za-z_][A-Za-z0-9_]* and be at most 255 bytes")
			return 64
		}
		environment := strings.TrimSpace(*f.environmentID)
		service := strings.TrimSpace(*f.serviceID)
		if environment == "" || service == "" {
			fmt.Fprintln(stderr, "midnight variable set: --environment-id and --service-id are required for a human session")
			return 64
		}
		if strings.TrimSpace(*f.workspaceID) != "" || strings.TrimSpace(*f.projectID) != "" {
			fmt.Fprintln(stderr, "midnight variable set: --workspace-id and --project-id are not accepted with a human session; ownership is derived from the service and environment")
			return 64
		}
		if suppliedFlags["expected-version"] {
			fmt.Fprintln(stderr, "midnight variable set: --expected-version is not accepted with a human session; use `midnight variable update` with the exact variable ID")
			return 64
		}
		path = humanVariableCollectionAPIPath(service, environment)
		request = humanVariableCreateRequest{Name: name, Scope: strings.TrimSpace(*f.scope), Value: resolvedValue}
		if err := validateVariableRequestBodySize(request); err != nil {
			fmt.Fprintf(stderr, "midnight variable set: %s\n", err)
			return 64
		}
	} else {
		legacyRequest := variableSetRequest{
			Name:          name,
			WorkspaceID:   strings.TrimSpace(*f.workspaceID),
			ProjectID:     strings.TrimSpace(*f.projectID),
			EnvironmentID: strings.TrimSpace(*f.environmentID),
			ServiceID:     strings.TrimSpace(*f.serviceID),
			Scope:         strings.TrimSpace(*f.scope),
			Value:         resolvedValue,
		}
		if suppliedFlags["expected-version"] {
			legacyRequest.ExpectedVersion = f.expectedVersion
		}
		request = legacyRequest
	}
	resolvedKey, err := resolveVariableIdempotencyKey(*f.key, client.token, "create", http.MethodPost, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable set: prepare retry-safe request: %s\n", err)
		return 64
	}
	var variable cliVariable
	raw, err := client.doJSON(http.MethodPost, path, request, resolvedKey, &variable)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable set: %s\n", safeVariableAPIError(err, resolvedValue))
		return 1
	}
	if err := validateVariableMetadataResponse(raw); err != nil {
		fmt.Fprintf(stderr, "midnight variable set: invalid metadata response: %s\n", err)
		return 1
	}
	if human {
		if err := validateHumanVariableMetadata(variable, "", name, strings.TrimSpace(*f.serviceID), strings.TrimSpace(*f.environmentID), 1); err != nil {
			fmt.Fprintf(stderr, "midnight variable set: invalid metadata response: %s\n", err)
			return 1
		}
		if variable.Scope != strings.TrimSpace(*f.scope) {
			fmt.Fprintf(stderr, "midnight variable set: invalid metadata response: scope %q does not match requested scope %q\n", variable.Scope, strings.TrimSpace(*f.scope))
			return 1
		}
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "variable set: %s (%s, %s), version %d\n", variable.ID, variable.Name, variable.Scope, variable.Version)
	return 0
}

type variableSetFlags struct {
	workspaceID     *string
	projectID       *string
	environmentID   *string
	serviceID       *string
	scope           *string
	value           *string
	useStdin        *bool
	expectedVersion *int
	key             *string
}

func defineVariableSetFlags(fs *flag.FlagSet) variableSetFlags {
	return variableSetFlags{
		workspaceID:     fs.String("workspace-id", "", "workspace id"),
		projectID:       cliProjectIDFlag(fs, "project id"),
		environmentID:   cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:       cliServiceIDFlag(fs, "service id"),
		scope:           fs.String("scope", "secret", "variable scope: plain, secret, or sealed"),
		value:           fs.String("value", "", "variable value"),
		useStdin:        fs.Bool("stdin", false, "read the exact variable value from standard input"),
		expectedVersion: fs.Int("expected-version", 0, "current version required when replacing an existing variable"),
		key:             fs.String("idempotency-key", "", "idempotency key for the set request"),
	}
}

func runVariableUpdate(args []string, stdout, stderr io.Writer) int {
	return runVariableUpdateWithInput(args, os.Stdin, stdout, stderr)
}

func runVariableUpdateWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight variable update", stderr)
	f := defineVariableUpdateFlags(fs)
	workspaceID, projectID, environmentID, serviceID := f.workspaceID, f.projectID, f.environmentID, f.serviceID
	scope, value, useStdin, expectedVersion, key := f.scope, f.value, f.useStdin, f.expectedVersion, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	suppliedFlags := make(map[string]bool)
	fs.Visit(func(current *flag.Flag) {
		suppliedFlags[current.Name] = true
	})
	if fs.NArg() != 1 || !suppliedFlags["expected-version"] || *expectedVersion < 1 {
		fmt.Fprintln(stderr, "usage: midnight variable update --environment-id ID --service-id ID --expected-version N [--scope plain|secret|sealed] (--value VALUE | --stdin) <variable-id>")
		return 64
	}
	resolvedValue, err := readVariableValue(stdin, *value, suppliedFlags["value"], *useStdin)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable update: %s\n", err)
		return 64
	}
	if suppliedFlags["scope"] {
		switch strings.TrimSpace(*scope) {
		case "plain", "secret", "sealed":
		default:
			fmt.Fprintln(stderr, "midnight variable update: --scope must be one of plain, secret, sealed")
			return 64
		}
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable update: %s\n", err)
		return 64
	}
	human, err := variableCredentialMode(client)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable update: %s\n", err)
		return 64
	}
	variableID := strings.TrimSpace(fs.Arg(0))
	environment := strings.TrimSpace(*environmentID)
	service := strings.TrimSpace(*serviceID)
	if variableID == "" {
		fmt.Fprintln(stderr, "midnight variable update: variable ID must not be empty")
		return 64
	}
	var (
		request any
		path    string
	)
	if human {
		if environment == "" || service == "" {
			fmt.Fprintln(stderr, "midnight variable update: --environment-id and --service-id are required for a human session")
			return 64
		}
		if strings.TrimSpace(*workspaceID) != "" || strings.TrimSpace(*projectID) != "" {
			fmt.Fprintln(stderr, "midnight variable update: --workspace-id and --project-id are not accepted with a human session; ownership is derived from the service and environment")
			return 64
		}
		humanRequest := humanVariableUpdateRequest{
			ExpectedVersion: *expectedVersion,
			Value:           resolvedValue,
		}
		if suppliedFlags["scope"] {
			trimmedScope := strings.TrimSpace(*scope)
			humanRequest.Scope = &trimmedScope
		}
		request = humanRequest
		if err := validateVariableRequestBodySize(request); err != nil {
			fmt.Fprintf(stderr, "midnight variable update: %s\n", err)
			return 64
		}
		path = humanVariableItemAPIPath(service, environment, variableID)
	} else {
		if strings.TrimSpace(*workspaceID) == "" || strings.TrimSpace(*projectID) == "" ||
			environment == "" || service == "" || !suppliedFlags["scope"] {
			fmt.Fprintln(stderr, "midnight variable update: operator recovery compatibility requires --workspace-id, --project-id, --environment-id, --service-id, and --scope")
			return 64
		}
		request = struct {
			WorkspaceID     string `json:"workspace_id"`
			ProjectID       string `json:"project_id"`
			EnvironmentID   string `json:"environment_id"`
			ServiceID       string `json:"service_id"`
			Scope           string `json:"scope"`
			Value           string `json:"value"`
			ExpectedVersion int    `json:"expected_version"`
		}{
			WorkspaceID:     strings.TrimSpace(*workspaceID),
			ProjectID:       strings.TrimSpace(*projectID),
			EnvironmentID:   environment,
			ServiceID:       service,
			Scope:           strings.TrimSpace(*scope),
			Value:           resolvedValue,
			ExpectedVersion: *expectedVersion,
		}
		path = "/api/v1alpha/variables/" + url.PathEscape(variableID)
	}
	resolvedKey, err := resolveVariableIdempotencyKey(*key, client.token, "update", http.MethodPatch, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable update: prepare retry-safe request: %s\n", err)
		return 64
	}
	var variable cliVariable
	raw, err := client.doJSON(http.MethodPatch, path, request, resolvedKey, &variable)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable update: %s\n", safeVariableAPIError(err, resolvedValue))
		return 1
	}
	if err := validateVariableMetadataResponse(raw); err != nil {
		fmt.Fprintf(stderr, "midnight variable update: invalid metadata response: %s\n", err)
		return 1
	}
	if human {
		if err := validateHumanVariableMetadata(variable, variableID, "", service, environment, *expectedVersion+1); err != nil {
			fmt.Fprintf(stderr, "midnight variable update: invalid metadata response: %s\n", err)
			return 1
		}
		if suppliedFlags["scope"] && variable.Scope != strings.TrimSpace(*scope) {
			fmt.Fprintf(stderr, "midnight variable update: invalid metadata response: scope %q does not match requested scope %q\n", variable.Scope, strings.TrimSpace(*scope))
			return 1
		}
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "variable updated: %s (%s, %s), version %d\n", variable.ID, variable.Name, variable.Scope, variable.Version)
	return 0
}

func runVariableDelete(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight variable delete", stderr)
	f := defineVariableDeleteFlags(fs)
	workspaceID, projectID, environmentID, serviceID := f.workspaceID, f.projectID, f.environmentID, f.serviceID
	expectedVersion, key := f.expectedVersion, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || *expectedVersion < 1 {
		fmt.Fprintln(stderr, "usage: midnight variable delete --expected-version N [--workspace-id ID] [--project-id ID] [--environment-id ID] [--service-id ID] <variable-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable delete: %s\n", err)
		return 64
	}
	human, err := variableCredentialMode(client)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable delete: %s\n", err)
		return 64
	}
	variableID := strings.TrimSpace(fs.Arg(0))
	environment := strings.TrimSpace(*environmentID)
	service := strings.TrimSpace(*serviceID)
	if variableID == "" {
		fmt.Fprintln(stderr, "midnight variable delete: variable ID must not be empty")
		return 64
	}
	var (
		request any
		path    string
	)
	if human {
		if environment == "" || service == "" {
			fmt.Fprintln(stderr, "midnight variable delete: --environment-id and --service-id are required for a human session")
			return 64
		}
		if strings.TrimSpace(*workspaceID) != "" || strings.TrimSpace(*projectID) != "" {
			fmt.Fprintln(stderr, "midnight variable delete: --workspace-id and --project-id are not accepted with a human session; ownership is derived from the service and environment")
			return 64
		}
		request = humanVariableDeleteRequest{ExpectedVersion: *expectedVersion}
		if err := validateVariableRequestBodySize(request); err != nil {
			fmt.Fprintf(stderr, "midnight variable delete: %s\n", err)
			return 64
		}
		path = humanVariableItemAPIPath(service, environment, variableID)
	} else {
		request = variableDeleteRequest{
			WorkspaceID:     strings.TrimSpace(*workspaceID),
			ProjectID:       strings.TrimSpace(*projectID),
			EnvironmentID:   environment,
			ServiceID:       service,
			ExpectedVersion: *expectedVersion,
		}
		path = "/api/v1alpha/variables/" + url.PathEscape(variableID)
	}
	resolvedKey, err := resolveVariableIdempotencyKey(*key, client.token, "delete", http.MethodDelete, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight variable delete: prepare retry-safe request: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodDelete, path, request, resolvedKey, nil); err != nil {
		fmt.Fprintf(stderr, "midnight variable delete: %s\n", safeVariableAPIError(err, ""))
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, cliDeletedDocument("id", variableID)); done {
		return code
	}
	fmt.Fprintf(stdout, "variable deleted: %s\n", variableID)
	return 0
}

type variableUpdateFlags struct {
	workspaceID     *string
	projectID       *string
	environmentID   *string
	serviceID       *string
	scope           *string
	value           *string
	useStdin        *bool
	expectedVersion *int
	key             *string
}

func defineVariableUpdateFlags(fs *flag.FlagSet) variableUpdateFlags {
	return variableUpdateFlags{
		workspaceID:     fs.String("workspace-id", "", "workspace id (operator recovery compatibility)"),
		projectID:       cliProjectIDFlag(fs, "project id (operator recovery compatibility)"),
		environmentID:   cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:       cliServiceIDFlag(fs, "service id"),
		scope:           fs.String("scope", "", "new variable scope: plain, secret, or sealed; omitted retains the current scope for humans"),
		value:           fs.String("value", "", "new variable value"),
		useStdin:        fs.Bool("stdin", false, "read the exact variable value from standard input"),
		expectedVersion: fs.Int("expected-version", 0, "current variable version"),
		key:             cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact update intent for humans when omitted)"),
	}
}

type variableDeleteFlags struct {
	workspaceID     *string
	projectID       *string
	environmentID   *string
	serviceID       *string
	expectedVersion *int
	key             *string
}

func defineVariableDeleteFlags(fs *flag.FlagSet) variableDeleteFlags {
	return variableDeleteFlags{
		workspaceID:     fs.String("workspace-id", "", "workspace id"),
		projectID:       cliProjectIDFlag(fs, "project id"),
		environmentID:   cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:       cliServiceIDFlag(fs, "service id"),
		expectedVersion: fs.Int("expected-version", 0, "current variable version"),
		key:             cliIdempotencyKeyFlag(fs, "idempotency key for the delete request"),
	}
}

func runDeployImage(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight deploy image", stderr)
	f := defineDeployImageFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	suppliedFlags := make(map[string]bool)
	fs.Visit(func(current *flag.Flag) {
		suppliedFlags[current.Name] = true
	})
	if strings.TrimSpace(*f.serviceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight deploy image --service-id <id> --environment-id <id> --image <digest-ref> [--port N] [--health-kind none|tcp|http] [--health-path /path] [--route-public] [--idempotency-key KEY]")
		return 64
	}
	if strings.TrimSpace(*f.sourceRef) != "" {
		fmt.Fprintln(stderr, "midnight deploy image: --source-ref is reserved until source build integration can produce an immutable sha256 candidate image; pass a digest-pinned --image without --source-ref")
		return 64
	}
	if strings.TrimSpace(*f.image) == "" {
		fmt.Fprintln(stderr, "usage: midnight deploy image --service-id <id> --environment-id <id> --image <digest-ref> [--port N] [--health-kind none|tcp|http] [--health-path /path] [--route-public] [--idempotency-key KEY]")
		return 64
	}
	runtimeIntent := deployRuntimeIntent{
		port:       *f.port,
		healthKind: strings.ToLower(strings.TrimSpace(*f.healthKind)),
		healthPath: strings.TrimSpace(*f.healthPath),
	}
	if err := validateDeployRuntimeIntent(runtimeIntent); err != nil {
		fmt.Fprintf(stderr, "midnight deploy image: %s\n", err)
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy image: %s\n", err)
		return 64
	}
	environment := strings.TrimSpace(*f.environmentID)
	imageRef := strings.TrimSpace(*f.image)
	humanSession := client.credentialKind == cliconfig.CredentialKindHumanSession
	if humanSession {
		if environment == "" {
			fmt.Fprintln(stderr, "midnight deploy image: --environment-id is required for human-session deploys")
			return 64
		}
		parsedImage, parseErr := registry.ParseImageReference(imageRef)
		if parseErr != nil || parsedImage.Digest == "" {
			fmt.Fprintln(stderr, "midnight deploy image: --image must be an immutable reference ending in @sha256:<64 lowercase hex characters>")
			return 64
		}
		for _, forbidden := range []string{
			"deployment-id",
			"instance-id",
			"source-ref",
			"route-name",
			"route-group",
			"route-host",
			"route-path",
			"route-tls",
		} {
			if suppliedFlags[forbidden] {
				fmt.Fprintf(stderr, "midnight deploy image: --%s is not available when you are signed in as a user; you can pass only --service-id, --environment-id, a digest-pinned --image, and optionally --route-public\n", forbidden)
				return 64
			}
		}
		if suppliedFlags["route-public"] && !*f.routePublic {
			fmt.Fprintln(stderr, "midnight deploy image: --route-public=false is ambiguous for human-session deploys; omit the flag for a private deployment or pass --route-public to request a generated public route")
			return 64
		}
	}
	// The port and readiness intent must be durable before the rollout
	// starts: the worker reads the stored service configuration, so saving
	// it after enqueueing the deploy would race the deployment it describes.
	if !runtimeIntent.empty() {
		if environment == "" {
			fmt.Fprintln(stderr, "midnight deploy image: --environment-id is required with --port, --health-kind, or --health-path")
			return 64
		}
		saved, configErr := applyDeployRuntimeIntent(client, strings.TrimSpace(*f.serviceID), environment, runtimeIntent)
		if configErr != nil {
			fmt.Fprintf(stderr, "midnight deploy image: save runtime configuration: %s\n", configErr)
			return 1
		}
		if saved != nil && !cfg.JSON {
			fmt.Fprintf(stdout, "service config saved: port %d, readiness %s (version %d)\n",
				saved.TargetPort, saved.HealthKind, saved.Version)
		}
	}
	var request any = deployImageRequest{
		EnvironmentID: environment,
		Image:         imageRef,
		DeploymentID:  strings.TrimSpace(*f.deploymentID),
		InstanceID:    strings.TrimSpace(*f.instanceID),
	}
	if humanSession {
		tenantRequest := tenantDeployImageRequest{
			EnvironmentID: environment,
			Image:         imageRef,
		}
		if *f.routePublic {
			tenantRequest.Route = &tenantGeneratedRouteRequest{Public: true}
		}
		request = tenantRequest
	} else if *f.routePublic || hasRouteFlags(*f.routeName, *f.routeGroup, *f.routeHost, *f.routePath, *f.routeTLS) {
		operatorRequest := request.(deployImageRequest)
		operatorRequest.Route = &deployRouteFlags{
			Name:   strings.TrimSpace(*f.routeName),
			Group:  strings.TrimSpace(*f.routeGroup),
			Host:   strings.TrimSpace(*f.routeHost),
			Path:   strings.TrimSpace(*f.routePath),
			Public: *f.routePublic || strings.TrimSpace(*f.routeHost) != "",
			TLS:    *f.routeTLS,
		}
		request = operatorRequest
	}
	path := "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(*f.serviceID)) + "/deployments"
	attempt, err := beginPendingMutationAttempt(*f.key, client.token, "deploy-image", http.MethodPost, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy image: prepare retry-safe attempt: %s\n", err)
		return 64
	}
	var operation orailtypes.Operation
	raw, err := client.doJSON(http.MethodPost, path, request, attempt.Key, &operation)
	if finishErr := attempt.Finish(err); finishErr != nil {
		fmt.Fprintf(stderr, "midnight deploy image: %s\n", finishErr)
		return 1
	}
	if err != nil {
		writeMutationFailure(stderr, "deploy image", err, attempt.Key)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, operation,
		func(value orailtypes.Operation) string { return string(value.ID) },
		func(output io.Writer, value orailtypes.Operation) {
			fmt.Fprintf(output, "deploy queued: %s (state=%s)\n", value.ID, value.State)
		},
	)
}

type deployImageFlags struct {
	serviceID     *string
	image         *string
	environmentID *string
	sourceRef     *string
	deploymentID  *string
	instanceID    *string
	port          *int
	healthKind    *string
	healthPath    *string
	routeName     *string
	routeGroup    *string
	routeHost     *string
	routePublic   *bool
	routePath     *string
	routeTLS      *bool
	key           *string
}

func defineDeployImageFlags(fs *flag.FlagSet) deployImageFlags {
	return deployImageFlags{
		serviceID:     cliServiceIDFlag(fs, "service id to deploy"),
		image:         fs.String("image", "", "digest-pinned image reference"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		sourceRef:     fs.String("source-ref", "", "source revision or ref"),
		deploymentID:  fs.String("deployment-id", "", "deployment id hint"),
		instanceID:    fs.String("instance-id", "", "instance id hint"),
		port:          fs.Int("port", 0, "port the image listens on; saved to the service configuration before deploying"),
		healthKind:    fs.String("health-kind", "", "how readiness is proven: none, tcp, or http"),
		healthPath:    fs.String("health-path", "", "HTTP readiness path, used with --health-kind http"),
		routeName:     fs.String("route-name", "", "route name"),
		routeGroup:    fs.String("route-group", "", "route group"),
		routeHost:     fs.String("route-host", "", "public route host"),
		routePublic:   fs.Bool("route-public", false, "explicitly enable public routing; an empty --route-host requests a generated domain"),
		routePath:     fs.String("route-path", "/", "public route path"),
		routeTLS:      fs.Bool("route-tls", false, "request TLS routing"),
		key:           fs.String("idempotency-key", "", "idempotency key for the deploy request"),
	}
}

// deployRuntimeIntent is the runtime configuration a deploy may carry
// inline. It is deliberately the small subset a rollout cannot proceed
// without knowing — which port the image listens on, and how readiness is
// proven — rather than a second copy of the whole config surface.
type deployRuntimeIntent struct {
	port       int
	healthKind string
	healthPath string
}

func (intent deployRuntimeIntent) empty() bool {
	return intent.port == 0 && intent.healthKind == "" && intent.healthPath == ""
}

// validateDeployRuntimeIntent rejects nonsense before any server round trip
// so the operator learns about a bad port from the flag, not from a rollout.
func validateDeployRuntimeIntent(intent deployRuntimeIntent) error {
	if intent.port != 0 && (intent.port < 1 || intent.port > 65535) {
		return errors.New("--port must be between 1 and 65535")
	}
	switch intent.healthKind {
	case "", "none", "tcp", "http":
	default:
		return errors.New("--health-kind must be none, tcp, or http")
	}
	if intent.healthPath != "" {
		if !strings.HasPrefix(intent.healthPath, "/") || strings.ContainsAny(intent.healthPath, "?#") {
			return errors.New("--health-path must be an absolute path without a query or fragment")
		}
		if intent.healthKind != "" && intent.healthKind != "http" {
			return errors.New("--health-path applies only to --health-kind http")
		}
	}
	return nil
}

// applyDeployRuntimeIntent saves the runtime intent to the service
// configuration the deployment will read. It reads the current config first
// so unrelated fields survive the replace, and sends the version it read as
// both expected_version and If-Match so a concurrent edit loses rather than
// being silently overwritten. It returns the saved config, or nil when the
// stored config already matches and no write was needed.
func applyDeployRuntimeIntent(
	client *cliAPIClient,
	serviceID, environmentID string,
	intent deployRuntimeIntent,
) (*cliServiceConfig, error) {
	path := serviceConfigAPIPath(serviceID, environmentID)
	var current cliServiceConfig
	if _, err := client.doJSON(http.MethodGet, path, nil, "", &current); err != nil {
		return nil, err
	}
	request := cliPutServiceConfigRequest{
		ExpectedVersion:       current.Version,
		RootDirectory:         current.RootDirectory,
		Builder:               current.Builder,
		DockerfilePath:        current.DockerfilePath,
		BuildCommand:          current.BuildCommand,
		StartCommand:          current.StartCommand,
		PredeployCommand:      current.PredeployCommand,
		TargetPort:            current.TargetPort,
		HealthKind:            current.HealthKind,
		HealthPath:            current.HealthPath,
		HealthPort:            current.HealthPort,
		HealthTimeoutSeconds:  current.HealthTimeoutSeconds,
		HealthIntervalSeconds: current.HealthIntervalSeconds,
		RestartPolicy:         current.RestartPolicy,
		RestartRetries:        current.RestartRetries,
		Replicas:              current.Replicas,
		OverlapEnabled:        current.OverlapEnabled,
		DrainGraceSeconds:     current.DrainGraceSeconds,
		RuntimeKind:           current.RuntimeKind,
		CronSchedule:          current.CronSchedule,
		CronTimeoutSeconds:    current.CronTimeoutSeconds,
		CronMaxAttempts:       current.CronMaxAttempts,
		CronRetryDelaySeconds: current.CronRetryDelaySeconds,
	}
	if intent.port != 0 {
		request.TargetPort = intent.port
	}
	if intent.healthKind != "" {
		request.HealthKind = intent.healthKind
	}
	if intent.healthPath != "" {
		request.HealthPath = intent.healthPath
	}
	if current.Explicit &&
		request.TargetPort == current.TargetPort &&
		request.HealthKind == current.HealthKind &&
		request.HealthPath == current.HealthPath {
		return nil, nil
	}
	var saved cliServiceConfig
	key := deterministicServiceConfigPutIdempotencyKey(serviceID, environmentID, request)
	if err := client.putServiceConfigIfMatch(path, request, key, current.Version, &saved); err != nil {
		return nil, err
	}
	return &saved, nil
}

// putServiceConfigIfMatch is a conditional PUT. It exists alongside doJSON
// because the service-config resource requires a strong If-Match validator,
// which the generic helper does not carry.
func (c *cliAPIClient) putServiceConfigIfMatch(
	path string,
	body any,
	idempotencyKey string,
	version int64,
	out any,
) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", fmt.Sprintf("%q", fmt.Sprintf("%d", version)))
	if strings.TrimSpace(idempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, responseBody, err := executeCLIRequest(c.client, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodPut, path, res.Status, responseBody)
	}
	if out == nil {
		return nil
	}
	if len(responseBody) == 0 {
		return errors.New("empty response body")
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return &cliResponseDecodeFailure{Err: err}
	}
	return nil
}

func runDeployStatus(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight deploy status", stderr)
	f := defineDeployStatusFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*f.operationID) == "" {
		fmt.Fprintln(stderr, "usage: midnight deploy status --operation-id <id>")
		return 64
	}
	client, err := newGeneratedCLIReadClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy status: %s\n", err)
		return 64
	}
	resolvedOperationID := strings.TrimSpace(*f.operationID)
	path := "/api/v1alpha/operations/" + url.PathEscape(resolvedOperationID)
	var operation midnightctl.Operation
	ctx, cancel := cliOutputContext(stdout)
	defer cancel()
	raw, err := executeGeneratedCLIRead(
		func() (*http.Response, error) {
			return client.client.GetOperation(ctx, midnightctl.OperationID(resolvedOperationID))
		},
		http.MethodGet,
		path,
		&operation,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy status: %s\n", err)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, operation,
		func(value midnightctl.Operation) string { return value.Id },
		func(output io.Writer, value midnightctl.Operation) {
			kind := generatedString(value.Kind)
			code := generatedString(value.Code)
			reason := generatedString(value.Reason)
			fmt.Fprintf(output, "%s\t%s\t%s\n", value.Id, kind, value.State)
			if code != "" || reason != "" {
				fmt.Fprintf(output, "reason\t%s\t%s\n", code, reason)
			}
		},
	)
}

type deployStatusFlags struct {
	operationID *string
}

func defineDeployStatusFlags(fs *flag.FlagSet) deployStatusFlags {
	return deployStatusFlags{
		operationID: fs.String("operation-id", "", "deploy operation id"),
	}
}

func runDeployEvents(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight deploy events", stderr)
	f := defineDeployEventsFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*f.operationID) == "" {
		fmt.Fprintln(stderr, "usage: midnight deploy events --operation-id <id> [--after N]")
		return 64
	}
	if *f.after < 0 {
		fmt.Fprintln(stderr, "midnight deploy events: --after must be >= 0")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy events: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/operations/" + url.PathEscape(strings.TrimSpace(*f.operationID)) + "/events"
	if *f.after > 0 {
		path += "?after=" + url.QueryEscape(fmt.Sprintf("%d", *f.after))
	}
	raw, err := client.doJSON(http.MethodGet, path, nil, "", nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy events: %s\n", err)
		return 1
	}
	if cfg.JSON {
		return writeOperationEventsAsJSON(stdout, stderr, raw)
	}
	_, _ = stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}

type cliOperationLogsResponse struct {
	OperationID     string                `json:"operation_id"`
	OperationEvents cliOperationEventsRef `json:"operation_events"`
	DeployLogs      cliOperationEventsRef `json:"deploy_logs"`
	AppLogs         []cliLogSurface       `json:"app_logs"`
	PlatformLogs    []cliLogSurface       `json:"platform_logs"`
	Message         string                `json:"message"`
}

type cliOperationEventsRef struct {
	Available bool   `json:"available"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Count     int    `json:"count"`
}

type cliLogSurface struct {
	Surface      string        `json:"surface"`
	Available    bool          `json:"available"`
	Reason       string        `json:"reason"`
	ReasonCode   string        `json:"reason_code"`
	RecoveryHint string        `json:"recovery_hint"`
	Count        int           `json:"count"`
	Entries      []cliLogEntry `json:"entries"`
	NextCursor   *struct {
		Token string `json:"token"`
	} `json:"next_cursor"`
	Truncated bool `json:"truncated"`
}

type cliLogEntry struct {
	At        time.Time      `json:"at"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields"`
}

type deployEventsFlags struct {
	operationID *string
	after       *int
}

func defineDeployEventsFlags(fs *flag.FlagSet) deployEventsFlags {
	return deployEventsFlags{
		operationID: fs.String("operation-id", "", "deploy operation id"),
		after:       fs.Int("after", 0, "return events after this sequence"),
	}
}

func runDeployLogs(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight deploy logs", stderr)
	f := defineDeployLogsFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*f.operationID) == "" {
		fmt.Fprintln(stderr, "usage: midnight deploy logs --operation-id <id> [--limit N] [--search TEXT] [--surface NAME --cursor TOKEN] [--api-base URL] [--token TOKEN] [--output json]")
		return 64
	}
	if *f.limit < 0 {
		fmt.Fprintln(stderr, "midnight deploy logs: --limit must be >= 0")
		return 64
	}
	if strings.TrimSpace(*f.cursor) != "" && strings.TrimSpace(*f.surface) == "" {
		fmt.Fprintln(stderr, "midnight deploy logs: --surface is required with --cursor")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy logs: %s\n", err)
		return 64
	}
	var logs cliOperationLogsResponse
	path := "/api/v1alpha/operations/" + url.PathEscape(strings.TrimSpace(*f.operationID)) + "/logs"
	values := url.Values{}
	if *f.limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", *f.limit))
	}
	if strings.TrimSpace(*f.freeText) != "" {
		values.Set("q", strings.TrimSpace(*f.freeText))
	}
	if strings.TrimSpace(*f.cursor) != "" {
		values.Set("surface", strings.TrimSpace(*f.surface))
		values.Set("cursor", strings.TrimSpace(*f.cursor))
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &logs)
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy logs: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeDeployLogsSummary(stdout, logs)
	return 0
}

type deployLogsFlags struct {
	operationID *string
	limit       *int
	freeText    *string
	surface     *string
	cursor      *string
}

func defineDeployLogsFlags(fs *flag.FlagSet) deployLogsFlags {
	return deployLogsFlags{
		operationID: fs.String("operation-id", "", "deploy operation id"),
		limit:       cliLimitFlag(fs, 0, "maximum log entries per surface"),
		freeText:    cliSearchFlag(fs, "filter log entries by message or field text"),
		surface:     fs.String("surface", "", "log surface for cursor resume: build, runtime, http, or platform"),
		cursor:      fs.String("cursor", "", "resume from an opaque log cursor returned by the API"),
	}
}

func newAPIFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *apiCommandConfig) {
	cfg := &apiCommandConfig{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cliStringVar(fs, &cfg.BaseURL, flagAPIBase, "", "Midnight API base URL (overrides environment, project link, and stored server)", "endpoint")
	fs.StringVar(&cfg.Token, "token", "", "Midnight API bearer token (overrides environment and stored credential)")
	fs.BoolVar(&cfg.JSON, "json", false, "alias for --output json")
	return fs, cfg
}

func newCLIAPIClient(cfg *apiCommandConfig) (*cliAPIClient, error) {
	projectAPIBase := ""
	if strings.TrimSpace(cfg.BaseURL) == "" && firstEnv(envAPIBase, envBaseURL) == "" {
		link, err := cliconfig.LoadProjectLink("")
		switch {
		case err == nil:
			projectAPIBase = link.APIBase
		case errors.Is(err, cliconfig.ErrNotLinked):
			// A unique stored server remains a valid lower-precedence source.
		default:
			return nil, err
		}
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		APIBase:        cfg.BaseURL,
		Token:          cfg.Token,
		ProjectAPIBase: projectAPIBase,
	})
	if err != nil {
		return nil, err
	}
	if _, err := url.ParseRequestURI(credentials.APIBase); err != nil {
		return nil, fmt.Errorf("invalid API base URL %q: %w", credentials.APIBase, err)
	}
	return &cliAPIClient{
		baseURL:        credentials.APIBase,
		token:          credentials.Token,
		credentialKind: credentials.Kind,
		// Product credentials are never replayed through redirects. This is
		// particularly important for one-time token management and keeps the
		// same-origin authentication boundary used by human login/logout.
		client: authHTTPClient,
	}, nil
}

func (c *cliAPIClient) doJSON(method, path string, body any, idempotencyKey string, out any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, responseBody, err := executeCLIRequest(c.client, req, maxCLIResponseBytes)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return responseBody, apiStatusError(method, path, res.Status, responseBody)
	}
	if out != nil {
		if len(responseBody) == 0 {
			return responseBody, errors.New("empty response body")
		}
		if err := json.Unmarshal(responseBody, out); err != nil {
			return responseBody, &cliResponseDecodeFailure{Err: err}
		}
	}
	return responseBody, nil
}

func apiStatusError(method, path, status string, body []byte) error {
	var envelope cliErrorResponse
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		return &cliAPIStatusFailure{
			Method:  safeHTTPText(method),
			Path:    safeHTTPText(path),
			Status:  safeHTTPText(status),
			Code:    safeHTTPText(string(envelope.Error.Code)),
			Message: safeHTTPText(envelope.Error.Message),
		}
	}
	text := safeHTTPText(strings.TrimSpace(string(body)))
	return &cliAPIStatusFailure{
		Method: safeHTTPText(method),
		Path:   safeHTTPText(path),
		Status: safeHTTPText(status),
		Text:   text,
	}
}

func writeRawJSON(stdout, stderr io.Writer, raw []byte) int {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		fmt.Fprintf(stderr, "midnight: format JSON response: %s\n", err)
		return 1
	}
	pretty.WriteByte('\n')
	_, _ = stdout.Write(pretty.Bytes())
	return 0
}

func writeOperationEventsAsJSON(stdout, stderr io.Writer, raw []byte) int {
	events := make([]json.RawMessage, 0)
	for _, block := range strings.Split(string(raw), "\n\n") {
		var eventName string
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if eventName == "operation" && data != "" {
			events = append(events, json.RawMessage(data))
		}
	}
	out, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "midnight deploy events: format JSON response: %s\n", err)
		return 1
	}
	_, _ = stdout.Write(out)
	fmt.Fprintln(stdout)
	return 0
}

func writeDeployLogsSummary(w io.Writer, logs cliOperationLogsResponse) {
	fmt.Fprintf(w, "operation logs: %s\n", logs.OperationID)
	deployLogs := logs.DeployLogs
	if deployLogs.Kind == "" && logs.OperationEvents.Kind != "" {
		deployLogs = logs.OperationEvents
	}
	if deployLogs.Available {
		fmt.Fprintf(w, "deploy logs: available (%d events) %s\n", deployLogs.Count, deployLogs.URL)
	} else {
		fmt.Fprintln(w, "deploy logs: unavailable")
	}
	writeLogSurfaceSummaries(w, "app", logs.AppLogs)
	writeLogSurfaceSummaries(w, "platform", logs.PlatformLogs)
	if strings.TrimSpace(logs.Message) != "" {
		fmt.Fprintln(w, logs.Message)
	}
}

func writeLogSurfaceSummaries(w io.Writer, group string, surfaces []cliLogSurface) {
	for _, surface := range surfaces {
		status := "unavailable"
		if surface.Available {
			status = "available"
		}
		name := surface.Surface
		if group != "" && group != "app" && name != group {
			name = group + "/" + name
		}
		detail := logSurfaceDetail(surface)
		if detail != "" {
			fmt.Fprintf(w, "%s logs: %s (%s)\n", name, status, detail)
		} else {
			fmt.Fprintf(w, "%s logs: %s\n", name, status)
		}
		for _, entry := range surface.Entries {
			writeLogEntry(w, entry)
		}
		if surface.Truncated && surface.NextCursor != nil {
			fmt.Fprintf(w, "%s logs next cursor: --surface %s --cursor %s\n", name, surface.Surface, surface.NextCursor.Token)
		}
	}
}

func logSurfaceDetail(surface cliLogSurface) string {
	parts := make([]string, 0, 3)
	if surface.Available {
		parts = append(parts, fmt.Sprintf("%d entries", surface.Count))
	}
	if strings.TrimSpace(surface.ReasonCode) != "" {
		parts = append(parts, "code="+strings.TrimSpace(surface.ReasonCode))
	}
	if strings.TrimSpace(surface.Reason) != "" {
		parts = append(parts, strings.TrimSpace(surface.Reason))
	}
	return strings.Join(parts, "; ")
}

func writeLogEntry(w io.Writer, entry cliLogEntry) {
	at := ""
	if !entry.At.IsZero() {
		at = entry.At.UTC().Format(time.RFC3339)
	}
	level := strings.TrimSpace(entry.Level)
	component := strings.TrimSpace(entry.Component)
	prefix := strings.TrimSpace(strings.Join([]string{at, level, component}, " "))
	if prefix == "" {
		fmt.Fprintf(w, "  %s\n", entry.Message)
		return
	}
	fmt.Fprintf(w, "  %s  %s\n", prefix, entry.Message)
}

func writeProjectTable(w io.Writer, projects []cliProject) {
	if len(projects) == 0 {
		fmt.Fprintln(w, "No projects.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSLUG\tNAME")
	for _, project := range projects {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", project.ID, project.Slug, project.DisplayName)
	}
	_ = tw.Flush()
}

func writeServiceTable(w io.Writer, services []cliService) {
	if len(services) == 0 {
		fmt.Fprintln(w, "No services.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROJECT\tSLUG\tNAME")
	for _, service := range services {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", service.ID, service.ProjectID, service.Slug, service.DisplayName)
	}
	_ = tw.Flush()
}

func writeServiceConfig(w io.Writer, value cliServiceConfig) {
	mode := "default"
	if value.Explicit {
		mode = "explicit"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "SERVICE\t%s\n", value.ServiceID)
	fmt.Fprintf(tw, "ENVIRONMENT\t%s\n", value.EnvironmentID)
	fmt.Fprintf(tw, "VERSION\t%d (%s)\n", value.Version, mode)
	fmt.Fprintf(tw, "BUILDER\t%s\n", value.Builder)
	fmt.Fprintf(tw, "ROOT DIRECTORY\t%s\n", value.RootDirectory)
	fmt.Fprintf(tw, "DOCKERFILE\t%s\n", value.DockerfilePath)
	fmt.Fprintf(tw, "BUILD COMMAND\t%s\n", value.BuildCommand)
	fmt.Fprintf(tw, "START COMMAND\t%s\n", value.StartCommand)
	fmt.Fprintf(tw, "PREDEPLOY COMMAND\t%s\n", value.PredeployCommand)
	fmt.Fprintf(tw, "TARGET PORT\t%d\n", value.TargetPort)
	fmt.Fprintf(tw, "HEALTH\t%s %s (port %d, timeout %ds, interval %ds)\n",
		value.HealthKind, value.HealthPath, value.HealthPort,
		value.HealthTimeoutSeconds, value.HealthIntervalSeconds)
	fmt.Fprintf(tw, "RESTART\t%s (retries %d)\n", value.RestartPolicy, value.RestartRetries)
	fmt.Fprintf(tw, "REPLICAS\t%d\n", value.Replicas)
	fmt.Fprintf(tw, "OVERLAP\t%t\n", value.OverlapEnabled)
	fmt.Fprintf(tw, "DRAIN GRACE\t%ds\n", value.DrainGraceSeconds)
	fmt.Fprintf(tw, "RUNTIME\t%s\n", value.RuntimeKind)
	if value.RuntimeKind == "cron" {
		fmt.Fprintf(tw, "CRON\t%s (timeout %ds, attempts %d, retry delay %ds)\n",
			value.CronSchedule,
			value.CronTimeoutSeconds,
			value.CronMaxAttempts,
			value.CronRetryDelaySeconds,
		)
	}
	_ = tw.Flush()
}

func writeEnvironmentTable(w io.Writer, values []cliEnvironment) {
	if len(values) == 0 {
		fmt.Fprintln(w, "No environments.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROJECT\tSLUG\tNAME\tTIER\tVERSION\tPROTECTED")
	for _, value := range values {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%t\n", value.ID, value.ProjectID, value.Slug, value.Name, value.Tier, value.ResourceVersion, value.Protected)
	}
	_ = tw.Flush()
}

func writeVariableTable(w io.Writer, variables []cliVariable) {
	if len(variables) == 0 {
		fmt.Fprintln(w, "No variables.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tPROJECT\tENVIRONMENT\tSERVICE\tVERSION\tBUCKET")
	for _, variable := range variables {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			variable.ID,
			variable.Name,
			variable.Scope,
			variable.ProjectID,
			variable.EnvironmentID,
			variable.ServiceID,
			variable.Version,
			variable.LengthBucket,
		)
	}
	_ = tw.Flush()
}

func variableCredentialMode(client *cliAPIClient) (bool, error) {
	switch client.credentialKind {
	case cliconfig.CredentialKindHumanSession:
		return true, nil
	case cliconfig.CredentialKindOperatorRecovery:
		return false, nil
	default:
		return false, errors.New("variable commands require a human session or a stored operator recovery credential; run `midnight login` for tenant access")
	}
}

func humanVariableCollectionAPIPath(serviceID, environmentID string) string {
	return "/api/v1alpha/services/" + url.PathEscape(strings.TrimSpace(serviceID)) +
		"/environments/" + url.PathEscape(strings.TrimSpace(environmentID)) + "/variables"
}

func humanVariableItemAPIPath(serviceID, environmentID, variableID string) string {
	return humanVariableCollectionAPIPath(serviceID, environmentID) +
		"/" + url.PathEscape(strings.TrimSpace(variableID))
}

func readVariableValue(stdin io.Reader, flagValue string, valueSupplied, useStdin bool) (string, error) {
	if valueSupplied == useStdin {
		return "", errors.New("exactly one of --value or --stdin is required")
	}
	if !useStdin {
		if len(flagValue) > variableValueLimitBytes {
			return "", fmt.Errorf("variable value exceeds %d bytes", variableValueLimitBytes)
		}
		return flagValue, nil
	}
	value, err := io.ReadAll(io.LimitReader(stdin, variableValueLimitBytes+1))
	if err != nil {
		return "", fmt.Errorf("read --stdin value: %w", err)
	}
	if len(value) > variableValueLimitBytes {
		return "", fmt.Errorf("variable value exceeds %d bytes", variableValueLimitBytes)
	}
	return string(value), nil
}

func validHumanVariableName(name string) bool {
	if len(name) == 0 || len(name) > humanVariableNameLimitBytes {
		return false
	}
	for index := 0; index < len(name); index++ {
		current := name[index]
		letter := current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z'
		if index == 0 {
			if !letter && current != '_' {
				return false
			}
			continue
		}
		digit := current >= '0' && current <= '9'
		if !letter && !digit && current != '_' {
			return false
		}
	}
	return true
}

func deterministicVariableIdempotencyKey(token, operation, method, path string, request any) string {
	body, _ := json.Marshal(request)
	digest := hmac.New(sha256.New, []byte(strings.TrimSpace(token)))
	for _, component := range [][]byte{
		[]byte("midnight-variable-intent-v1"),
		[]byte(strings.TrimSpace(operation)),
		[]byte(strings.TrimSpace(method)),
		[]byte(path),
		body,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(component))
		_, _ = digest.Write(component)
		_, _ = digest.Write([]byte{0})
	}
	return fmt.Sprintf("midnight-variable-%s-v1-%x", strings.TrimSpace(operation), digest.Sum(nil))
}

// resolveVariableIdempotencyKey keeps the established human-variable wire key
// format while giving explicit keys the shared mutation-key validation rules.
func resolveVariableIdempotencyKey(explicit, token, operation, method, path string, request any) (string, error) {
	if explicit != "" {
		return resolveMutationIdempotencyKey(explicit, token, operation, method, path, request)
	}
	return deterministicVariableIdempotencyKey(token, operation, method, path, request), nil
}

func validateVariableRequestBodySize(request any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return errors.New("variable request could not be encoded")
	}
	if len(body) > variableValueLimitBytes {
		return fmt.Errorf("variable request body exceeds %d bytes", variableValueLimitBytes)
	}
	return nil
}

func safeVariableAPIError(err error, submittedValue string) string {
	var statusFailure *cliAPIStatusFailure
	if errors.As(err, &statusFailure) {
		if safeCode := safeVariableFailureCode(statusFailure.Code); safeCode != "" {
			return fmt.Sprintf(
				"%s %s failed: %s: %s",
				statusFailure.Method,
				statusFailure.Path,
				statusFailure.Status,
				safeCode,
			)
		}
		return fmt.Sprintf(
			"%s %s failed: %s",
			statusFailure.Method,
			statusFailure.Path,
			statusFailure.Status,
		)
	}
	var decodeFailure *cliResponseDecodeFailure
	if errors.As(err, &decodeFailure) {
		return "server returned an invalid metadata response"
	}
	message := err.Error()
	if submittedValue != "" {
		message = strings.ReplaceAll(message, submittedValue, "[REDACTED]")
	}
	return message
}

func safeVariableFailureCode(code string) string {
	switch failurecodes.Code(code) {
	case failurecodes.AuthRequiredMissing,
		failurecodes.AuthTokenHeaderInvalid,
		failurecodes.AuthSessionInvalid,
		failurecodes.AuthorizationDenied,
		failurecodes.IdempotencyKeyInvalid,
		failurecodes.IdempotencyKeyRequired,
		failurecodes.IdempotencyKeyReused,
		failurecodes.IdempotencyKeyReusedDifferentBody,
		failurecodes.IdempotencyRequestInProgress,
		failurecodes.ResourceNotFound,
		failurecodes.ResourceValidationFailed,
		failurecodes.ResourceConflict,
		failurecodes.InternalError:
		return code
	default:
		return ""
	}
}

func validateVariableMetadataResponse(raw []byte) error {
	if err := rejectDuplicateVariableMetadataFields(raw); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	switch typed := value.(type) {
	case map[string]any:
		return validateVariableMetadataObject(typed)
	case []any:
		for index, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("metadata at index %d is not an object", index)
			}
			if err := validateVariableMetadataObject(object); err != nil {
				return fmt.Errorf("metadata at index %d: %w", index, err)
			}
		}
		return nil
	default:
		return errors.New("metadata response is not an object or array")
	}
}

func validateVariableMetadataObject(object map[string]any) error {
	allowed := map[string]struct{}{
		"id": {}, "scope": {}, "workspace_id": {}, "project_id": {},
		"environment_id": {}, "service_id": {}, "name": {}, "version": {},
		"last_rotated_at": {}, "length_bucket": {}, "kek_fingerprint": {},
		"value_kind": {}, "references": {},
	}
	for key, value := range object {
		switch key {
		case "value", "plaintext", "ciphertext", "value_ciphertext", "wrapped_dek", "dek_ciphertext":
			return fmt.Errorf("response contained forbidden secret field %q", key)
		}
		if _, ok := allowed[key]; !ok {
			return errors.New("response contained an unexpected non-metadata field")
		}
		switch key {
		case "id", "scope", "workspace_id", "project_id", "environment_id",
			"service_id", "name", "last_rotated_at", "length_bucket",
			"kek_fingerprint", "value_kind":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("metadata field %s is not a string", key)
			}
		case "version":
			if _, ok := value.(float64); !ok {
				return errors.New("metadata field version is not a number")
			}
		}
		if key != "references" {
			continue
		}
		references, ok := value.([]any)
		if !ok {
			return errors.New("references metadata is not an array")
		}
		for index, rawReference := range references {
			reference, ok := rawReference.(map[string]any)
			if !ok {
				return fmt.Errorf("reference metadata at index %d is not an object", index)
			}
			allowedReference := map[string]struct{}{"kind": {}, "service": {}, "name": {}}
			for referenceKey := range reference {
				if _, ok := allowedReference[referenceKey]; !ok {
					return fmt.Errorf("reference metadata at index %d contains an unexpected field", index)
				}
			}
			kind, kindOK := reference["kind"].(string)
			name, nameOK := reference["name"].(string)
			if !kindOK || !nameOK || strings.TrimSpace(name) == "" {
				return fmt.Errorf("reference metadata at index %d requires string kind and non-empty name", index)
			}
			switch kind {
			case "service_var", "shared", "local", "builtin":
			default:
				return fmt.Errorf("reference metadata at index %d has an invalid kind", index)
			}
			if service, present := reference["service"]; present {
				if _, ok := service.(string); !ok {
					return fmt.Errorf("reference metadata at index %d has non-string service", index)
				}
			}
		}
	}
	return nil
}

func rejectDuplicateVariableMetadataFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := validateUniqueVariableJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("metadata response contains trailing JSON")
		}
		return err
	}
	return nil
}

func validateUniqueVariableJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("metadata response contains a non-string object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("metadata response contains a duplicate field")
			}
			seen[key] = struct{}{}
			if err := validateUniqueVariableJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim('}') {
			return errors.New("metadata response object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueVariableJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim(']') {
			return errors.New("metadata response array is not closed")
		}
	default:
		return errors.New("metadata response contains an invalid delimiter")
	}
	return nil
}

func validateHumanVariableMetadata(
	variable cliVariable,
	expectedID,
	expectedName,
	expectedServiceID,
	expectedEnvironmentID string,
	expectedVersion int,
) error {
	if strings.TrimSpace(variable.ID) == "" {
		return errors.New("variable id is empty")
	}
	if expectedID != "" && variable.ID != expectedID {
		return errors.New("variable id does not match the requested id")
	}
	if strings.TrimSpace(variable.Name) == "" {
		return errors.New("variable name is empty")
	}
	if expectedName != "" && variable.Name != expectedName {
		return errors.New("variable name does not match the requested name")
	}
	switch variable.Scope {
	case "plain", "secret", "sealed":
	default:
		return errors.New("variable scope is invalid")
	}
	if variable.ServiceID != expectedServiceID {
		return errors.New("service id does not match the requested service")
	}
	if variable.EnvironmentID != expectedEnvironmentID {
		return errors.New("environment id does not match the requested environment")
	}
	if variable.Version < 1 {
		return fmt.Errorf("variable version %d must be positive", variable.Version)
	}
	if expectedVersion > 0 && variable.Version != expectedVersion {
		return fmt.Errorf("variable version %d does not match expected version %d", variable.Version, expectedVersion)
	}
	switch variable.ValueKind {
	case "literal", "reference":
	default:
		return errors.New("variable value kind is invalid")
	}
	if variable.ValueKind == "reference" && len(variable.References) == 0 {
		return errors.New("reference variable metadata has no references")
	}
	if variable.ValueKind != "reference" && len(variable.References) != 0 {
		return errors.New("non-reference variable metadata contains references")
	}
	for index, reference := range variable.References {
		switch reference.Kind {
		case "service_var", "shared", "local", "builtin":
		default:
			return fmt.Errorf("variable reference at index %d has an invalid kind", index)
		}
		if strings.TrimSpace(reference.Name) == "" {
			return fmt.Errorf("variable reference at index %d has an empty name", index)
		}
		if reference.Kind == "service_var" && strings.TrimSpace(reference.Service) == "" {
			return fmt.Errorf("service variable reference at index %d has no service", index)
		}
		if reference.Kind != "service_var" && strings.TrimSpace(reference.Service) != "" {
			return fmt.Errorf("non-service variable reference at index %d contains a service", index)
		}
	}
	return nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func hasRouteFlags(name, group, host, path string, tls bool) bool {
	return strings.TrimSpace(name) != "" ||
		strings.TrimSpace(group) != "" ||
		strings.TrimSpace(host) != "" ||
		strings.TrimSpace(path) != "" && strings.TrimSpace(path) != "/" ||
		tls
}

func printProjectUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight project list [--api-base URL] [--token TOKEN] [--output table|json]")
	fmt.Fprintln(w, "  midnight project create --workspace-id ID [--name NAME] [--idempotency-key KEY] <slug>")
	fmt.Fprintln(w, "  midnight project delete [--yes] <project-id>")
}

func printServiceUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight service list [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight service create --project-id <id> [--name NAME] [--idempotency-key KEY] <slug>")
	fmt.Fprintln(w, "  midnight service delete [--yes] <service-id>")
	fmt.Fprintln(w, "  midnight service config get|put ...")
	fmt.Fprintln(w, "  midnight service github repositories|branches|get|link|unlink ...")
}

func printServiceConfigUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight service config get --environment-id ID [--api-base URL] [--token TOKEN] [--json] <service-id>")
	fmt.Fprintln(w, "  midnight service config put --environment-id ID --file PATH [--idempotency-key STABLE_RETRY_KEY] [--api-base URL] [--token TOKEN] [--json] <service-id>")
}

func printEnvironmentUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight environment list [--project-id ID] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight environment create --project-id ID [--name NAME] [--tier TIER] [--protected] [--idempotency-key KEY] <slug>")
	fmt.Fprintln(w, "  midnight environment update --expected-version N [--slug SLUG] [--name NAME] [--tier TIER] [--idempotency-key KEY] <environment-id>")
	fmt.Fprintln(w, "  midnight environment delete [--yes] <environment-id>")
	fmt.Fprintln(w, "  midnight environment duplicate --slug NEW_SLUG [--name NAME] [--copy-config --idempotency-key KEY] <source-environment-id>")
}

func printVariableUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight variable list [--project-id ID] [--environment-id ID] [--service-id ID] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight variable set [--expected-version N] [--scope plain|secret|sealed] [--workspace-id ID] [--project-id ID] [--environment-id ID] [--service-id ID] (--value VALUE | --stdin) <name>")
	fmt.Fprintln(w, "  midnight variable update --environment-id ID --service-id ID --expected-version N [--scope plain|secret|sealed] (--value VALUE | --stdin) <variable-id>")
	fmt.Fprintln(w, "  midnight variable delete --expected-version N [--workspace-id ID] [--project-id ID] [--environment-id ID] [--service-id ID] <variable-id>")
}

func printDeployUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight deploy image --service-id <id> --environment-id <id> --image <digest-ref> [--idempotency-key KEY]")
	fmt.Fprintln(w, "  midnight deploy status --operation-id <id> [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight deploy events --operation-id <id> [--after N] [--api-base URL] [--token TOKEN] [--json]")
	fmt.Fprintln(w, "  midnight deploy logs --operation-id <id> [--api-base URL] [--token TOKEN] [--json]")
}
