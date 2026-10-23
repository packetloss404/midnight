// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package serviceconfig owns durable per-service/environment build and runtime
// intent plus the immutable, redaction-safe copy bound to each deployment.
package serviceconfig

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/scheduler"
)

const (
	BuilderAuto       = "auto"
	BuilderDockerfile = "dockerfile"
	BuilderRailpack   = "railpack"

	HealthNone = "none"
	HealthHTTP = "http"
	HealthTCP  = "tcp"

	RestartNo            = "no"
	RestartAlways        = "always"
	RestartUnlessStopped = "unless-stopped"
	RestartOnFailure     = "on-failure"

	RuntimeKindService    = "service"
	RuntimeKindBackground = "background"
	RuntimeKindCron       = "cron"

	DefaultTargetPort    = 8080
	DefaultHealthTimeout = 300 * time.Second
	// DefaultImageHealthTimeout bounds readiness for a deploy of an already
	// built, immutable image. Nothing is compiled or installed at rollout
	// time, so a container that has not answered in two minutes is far more
	// likely broken than slow. Source builds keep DefaultHealthTimeout,
	// where a cold cache legitimately costs minutes. Either default is
	// overridden the moment an operator sets Config.HealthTimeout.
	DefaultImageHealthTimeout = 120 * time.Second
	DefaultHealthInterval     = time.Second
	DefaultDrainGrace         = 30 * time.Second
	DefaultReplicas           = 1
	MaxCommandBytes           = 32 << 10
	MaxRootDirectoryBytes     = 1024
	MaxDockerfilePathBytes    = 1024
	MaxHealthPathBytes        = 2048
	MaxRestartRetries         = 1000
	// MaxReplicas is an operator cap for the single-host Docker scheduler;
	// it is not a promise of regional placement or multi-region capacity.
	MaxReplicas            = 50
	MaxHealthTimeout       = 24 * time.Hour
	MaxDrainGrace          = time.Hour
	DefaultDockerfilePath  = "Dockerfile"
	DefaultCronTimeout     = 10 * time.Minute
	DefaultCronRetryDelay  = time.Minute
	DefaultCronMaxAttempts = 1
	MaxCronTimeout         = 24 * time.Hour
	MaxCronRetryDelay      = 24 * time.Hour
	MaxCronAttempts        = 10
	SnapshotSchemaVersion  = "midnight.dev/deployment-config-snapshot/v1"
	// VariableRefsSchemaV1 is retained for snapshots created before service
	// aliases were pinned. V2 adds the slug/display-name bindings required to
	// resolve cross-service references without consulting mutable live rows.
	VariableRefsSchemaV1 = "midnight.dev/variable-version-refs/v1"
	VariableRefsSchema   = "midnight.dev/variable-version-refs/v2"
)

var (
	ErrInvalid  = errors.New("service config is invalid")
	ErrNotFound = errors.New("service config not found")
	ErrConflict = errors.New("service config version conflict")
	ErrScope    = errors.New("service config scope mismatch")
	// ErrUnsupported is returned when durable intent names a capability that
	// is modeled and snapshotted but has no safe production execution seam.
	ErrUnsupported = errors.New("service config capability is not implemented")
)

// Config is effective non-secret deployment intent for one service in one
// environment. Version is zero only for the synthetic default configuration;
// persisted rows start at one.
type Config struct {
	ProjectID        string        `json:"project_id"`
	ServiceID        string        `json:"service_id"`
	EnvironmentID    string        `json:"environment_id"`
	Version          int64         `json:"version"`
	RootDirectory    string        `json:"root_directory"`
	Builder          string        `json:"builder"`
	DockerfilePath   string        `json:"dockerfile_path"`
	BuildCommand     string        `json:"build_command"`
	StartCommand     string        `json:"start_command"`
	PredeployCommand string        `json:"predeploy_command"`
	TargetPort       int           `json:"target_port"`
	HealthKind       string        `json:"health_kind"`
	HealthPath       string        `json:"health_path"`
	HealthPort       int           `json:"health_port"`
	HealthTimeout    time.Duration `json:"health_timeout"`
	HealthInterval   time.Duration `json:"health_interval"`
	RestartPolicy    string        `json:"restart_policy"`
	RestartRetries   int           `json:"restart_retries"`
	Replicas         int           `json:"replicas"`
	OverlapEnabled   bool          `json:"overlap_enabled"`
	DrainGrace       time.Duration `json:"drain_grace"`
	// RuntimeKind selects a long-running service, a private background
	// service, or a durable UTC cron definition. Cron definitions never
	// schedule a service container; each due occurrence is a one-off.
	RuntimeKind     string        `json:"runtime_kind"`
	CronSchedule    string        `json:"cron_schedule"`
	CronTimeout     time.Duration `json:"cron_timeout"`
	CronMaxAttempts int           `json:"cron_max_attempts"`
	CronRetryDelay  time.Duration `json:"cron_retry_delay"`
	CreatedAt       time.Time     `json:"created_at,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at,omitempty"`
}

// Default returns the effective configuration for a service/environment that
// does not yet have an explicit row. Version zero makes that fact observable
// in the immutable deployment snapshot.
func Default(projectID, serviceID, environmentID string) Config {
	return Config{
		ProjectID:      strings.TrimSpace(projectID),
		ServiceID:      strings.TrimSpace(serviceID),
		EnvironmentID:  strings.TrimSpace(environmentID),
		Builder:        BuilderAuto,
		DockerfilePath: DefaultDockerfilePath,
		TargetPort:     DefaultTargetPort,
		// Persistent services must prove a listening application endpoint
		// before promotion even when no HTTP path is configured. Operators
		// explicitly select "none" only for background workers/no-port jobs.
		HealthKind:      HealthTCP,
		HealthPath:      "/",
		HealthTimeout:   DefaultHealthTimeout,
		HealthInterval:  DefaultHealthInterval,
		RestartPolicy:   RestartUnlessStopped,
		Replicas:        DefaultReplicas,
		OverlapEnabled:  true,
		DrainGrace:      DefaultDrainGrace,
		RuntimeKind:     RuntimeKindService,
		CronTimeout:     DefaultCronTimeout,
		CronMaxAttempts: DefaultCronMaxAttempts,
		CronRetryDelay:  DefaultCronRetryDelay,
	}
}

// Normalize fills defaults and trims operator-controlled strings.
func Normalize(in Config) Config {
	out := in
	out.ProjectID = strings.TrimSpace(out.ProjectID)
	out.ServiceID = strings.TrimSpace(out.ServiceID)
	out.EnvironmentID = strings.TrimSpace(out.EnvironmentID)
	out.RootDirectory = strings.TrimSpace(out.RootDirectory)
	out.Builder = strings.ToLower(strings.TrimSpace(out.Builder))
	out.DockerfilePath = strings.TrimSpace(out.DockerfilePath)
	out.BuildCommand = strings.TrimSpace(out.BuildCommand)
	out.StartCommand = strings.TrimSpace(out.StartCommand)
	out.PredeployCommand = strings.TrimSpace(out.PredeployCommand)
	out.HealthKind = strings.ToLower(strings.TrimSpace(out.HealthKind))
	out.HealthPath = strings.TrimSpace(out.HealthPath)
	out.RestartPolicy = strings.ToLower(strings.TrimSpace(out.RestartPolicy))
	out.RuntimeKind = strings.ToLower(strings.TrimSpace(out.RuntimeKind))
	out.CronSchedule = strings.TrimSpace(out.CronSchedule)
	if out.Builder == "" {
		out.Builder = BuilderAuto
	}
	if out.DockerfilePath == "" {
		out.DockerfilePath = DefaultDockerfilePath
	}
	if out.TargetPort == 0 {
		out.TargetPort = DefaultTargetPort
	}
	if out.HealthKind == "" {
		out.HealthKind = HealthTCP
	}
	if out.HealthPath == "" {
		out.HealthPath = "/"
	}
	if out.HealthTimeout <= 0 {
		out.HealthTimeout = DefaultHealthTimeout
	}
	if out.HealthInterval <= 0 {
		out.HealthInterval = DefaultHealthInterval
	}
	if out.RestartPolicy == "" {
		out.RestartPolicy = RestartUnlessStopped
	}
	if out.Replicas == 0 {
		out.Replicas = DefaultReplicas
	}
	if out.DrainGrace <= 0 {
		out.DrainGrace = DefaultDrainGrace
	}
	if out.RuntimeKind == "" {
		out.RuntimeKind = RuntimeKindService
	}
	if out.CronTimeout <= 0 {
		out.CronTimeout = DefaultCronTimeout
	}
	if out.CronMaxAttempts == 0 {
		out.CronMaxAttempts = DefaultCronMaxAttempts
	}
	if out.CronRetryDelay <= 0 {
		out.CronRetryDelay = DefaultCronRetryDelay
	}
	return out
}

// Validate rejects unsafe paths, malformed probes, and settings that the local
// Docker runtime cannot represent faithfully.
func Validate(in Config) error {
	cfg := Normalize(in)
	if cfg.ProjectID == "" || cfg.ServiceID == "" || cfg.EnvironmentID == "" {
		return fmt.Errorf("%w: project_id, service_id, and environment_id are required", ErrInvalid)
	}
	if err := validateRelativePath(cfg.RootDirectory, MaxRootDirectoryBytes, true); err != nil {
		return fmt.Errorf("%w: root_directory: %v", ErrInvalid, err)
	}
	if err := validateRelativePath(cfg.DockerfilePath, MaxDockerfilePathBytes, false); err != nil {
		return fmt.Errorf("%w: dockerfile_path: %v", ErrInvalid, err)
	}
	switch cfg.Builder {
	case BuilderAuto, BuilderDockerfile, BuilderRailpack:
	default:
		return fmt.Errorf("%w: builder must be auto, dockerfile, or railpack", ErrInvalid)
	}
	for name, command := range map[string]string{
		"build_command":     cfg.BuildCommand,
		"start_command":     cfg.StartCommand,
		"predeploy_command": cfg.PredeployCommand,
	} {
		if len(command) > MaxCommandBytes || containsControlExceptWhitespace(command) {
			return fmt.Errorf("%w: %s contains invalid control characters or is too large", ErrInvalid, name)
		}
	}
	if !validPort(cfg.TargetPort) {
		return fmt.Errorf("%w: target_port must be between 1 and 65535", ErrInvalid)
	}
	if cfg.HealthPort != 0 && !validPort(cfg.HealthPort) {
		return fmt.Errorf("%w: health_port must be zero or between 1 and 65535", ErrInvalid)
	}
	switch cfg.HealthKind {
	case HealthNone, HealthHTTP, HealthTCP:
	default:
		return fmt.Errorf("%w: health_kind must be none, http, or tcp", ErrInvalid)
	}
	if cfg.HealthKind == HealthHTTP {
		if len(cfg.HealthPath) > MaxHealthPathBytes || !strings.HasPrefix(cfg.HealthPath, "/") ||
			strings.ContainsAny(cfg.HealthPath, "?#") || containsControlExceptWhitespace(cfg.HealthPath) {
			return fmt.Errorf("%w: HTTP health_path must be an absolute path without query or fragment", ErrInvalid)
		}
	}
	if cfg.HealthTimeout > MaxHealthTimeout || cfg.HealthInterval >= cfg.HealthTimeout {
		return fmt.Errorf("%w: health timeout must be at most 24h and greater than interval", ErrInvalid)
	}
	switch cfg.RestartPolicy {
	case RestartNo, RestartAlways, RestartUnlessStopped, RestartOnFailure:
	default:
		return fmt.Errorf("%w: restart_policy is unsupported", ErrInvalid)
	}
	if cfg.RestartRetries < 0 || cfg.RestartRetries > MaxRestartRetries {
		return fmt.Errorf("%w: restart_retries must be between 0 and %d", ErrInvalid, MaxRestartRetries)
	}
	if cfg.RestartPolicy != RestartOnFailure && cfg.RestartRetries != 0 {
		return fmt.Errorf("%w: restart_retries requires restart_policy on-failure", ErrInvalid)
	}
	if cfg.Replicas < 1 || cfg.Replicas > MaxReplicas {
		return fmt.Errorf("%w: replicas must be between 1 and %d", ErrInvalid, MaxReplicas)
	}
	if cfg.DrainGrace > MaxDrainGrace {
		return fmt.Errorf("%w: drain_grace must be at most 1h", ErrInvalid)
	}
	switch cfg.RuntimeKind {
	case RuntimeKindService, RuntimeKindBackground:
		if cfg.CronSchedule != "" {
			return fmt.Errorf("%w: cron_schedule requires runtime_kind cron", ErrInvalid)
		}
	case RuntimeKindCron:
		if cfg.CronSchedule == "" {
			return fmt.Errorf("%w: cron_schedule is required for runtime_kind cron", ErrInvalid)
		}
		// Keep the scheduler contract in the model layer: exactly five fields,
		// UTC semantics, minute-or-slower cadence, and no parser extensions.
		if err := validateCronSchedule(cfg.CronSchedule); err != nil {
			return fmt.Errorf("%w: cron_schedule: %v", ErrInvalid, err)
		}
		if cfg.StartCommand == "" || cfg.Replicas != 1 || cfg.RestartPolicy != RestartNo || cfg.RestartRetries != 0 ||
			cfg.HealthKind != HealthNone || cfg.PredeployCommand != "" {
			return fmt.Errorf("%w: cron requires start_command, replicas=1, restart_policy=no, health_kind=none, and no predeploy_command", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: runtime_kind must be service, background, or cron", ErrInvalid)
	}
	if cfg.CronTimeout > MaxCronTimeout || cfg.CronRetryDelay > MaxCronRetryDelay ||
		cfg.CronMaxAttempts < 1 || cfg.CronMaxAttempts > MaxCronAttempts {
		return fmt.Errorf("%w: cron timeout, retry delay, or max attempts is out of range", ErrInvalid)
	}
	return nil
}

func validateCronSchedule(value string) error {
	_, _, err := scheduler.NormalizeCronConfig(scheduler.CronConfig{
		Schedule: value,
		Timezone: scheduler.DefaultCronTimezone,
	})
	return err
}

func validateRelativePath(value string, maxBytes int, emptyOK bool) error {
	if value == "" && emptyOK {
		return nil
	}
	if value == "" {
		return errors.New("value is required")
	}
	if len(value) > maxBytes || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") ||
		containsControlExceptWhitespace(value) || path.Clean(value) != value || value == "." ||
		value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("must be a clean relative path inside the source checkout")
	}
	return nil
}

func containsControlExceptWhitespace(value string) bool {
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

// VariableVersionRef is deliberately metadata-only. IDs and names are useful
// for reproducibility; encrypted values and ciphertext are not duplicated.
type VariableVersionRef struct {
	ID                 string `json:"id"`
	ServiceID          string `json:"service_id"`
	ServiceSlug        string `json:"service_slug,omitempty"`
	ServiceDisplayName string `json:"service_display_name,omitempty"`
	Name               string `json:"name"`
	Version            int    `json:"version"`
}

// Snapshot is the immutable effective config bound to a canonical deployment.
type Snapshot struct {
	SchemaVersion string `json:"schema_version"`
	DeploymentID  string `json:"deployment_id"`
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	// ConfigurationRevision is the environment-scoped applied revision whose
	// desired config and variable versions this immutable snapshot captured.
	// Zero denotes deployments/snapshots created before revision tracking.
	ConfigurationRevision int64                `json:"configuration_revision"`
	ConfigVersion         int64                `json:"config_version"`
	Config                Config               `json:"config"`
	VariableSchema        string               `json:"variable_schema"`
	VariableVersions      []VariableVersionRef `json:"variable_versions"`
	SHA256                string               `json:"sha256"`
	CreatedAt             time.Time            `json:"created_at"`
}

// Resolver is the worker boundary. ResolveDeployment returns an existing
// immutable snapshot on retry, or atomically creates the first snapshot after
// proving the deployment and config scopes in PostgreSQL.
type Resolver interface {
	ResolveDeployment(ctx context.Context, deploymentID, projectID, environmentID, serviceID string) (Snapshot, error)
}

// PortConfigured reports that a human chose the application port rather
// than inheriting the platform default. It is the same "is this a guess?"
// question runtime.ScheduleRequest.TargetPortInferred asks, kept here so the
// deploy executor and the runtime adapter answer it identically.
func PortConfigured(cfg Config) bool {
	return cfg.HealthPort != 0 || (cfg.TargetPort != 0 && cfg.TargetPort != DefaultTargetPort)
}

// ProbeConfigured reports that a human chose how this service proves it is
// ready. A stored config always carries a health kind (Normalize fills in
// "tcp"), so an explicit choice is only observable as one that differs from
// the default, or as a health port/HTTP path the default would never set.
func ProbeConfigured(cfg Config) bool {
	kind := strings.ToLower(strings.TrimSpace(cfg.HealthKind))
	return (kind != "" && kind != HealthTCP) || cfg.HealthPort != 0 || PortConfigured(cfg)
}

// EffectiveHealthKind resolves how readiness is actually proven for one
// rollout, given what the image itself declares.
//
// An image that declares no port, deployed by an operator who chose no port
// either, cannot answer a TCP probe: a worker, a cron-shaped image, or a
// queue consumer has nothing listening, so the default "tcp" probe would
// keep such a service permanently un-ready. In that one case readiness falls
// back to "none" — running and not exited becomes the signal.
//
// Every other case keeps the configured probe. A service with a port, an
// image that declares one, or an operator who picked a health kind, port, or
// HTTP path must still prove it is listening before traffic moves to it.
func EffectiveHealthKind(cfg Config, imageDeclaresPort bool) string {
	kind := strings.ToLower(strings.TrimSpace(cfg.HealthKind))
	if kind == "" {
		kind = HealthTCP
	}
	if kind == HealthNone || imageDeclaresPort || ProbeConfigured(cfg) {
		return kind
	}
	return HealthNone
}

// EffectiveHealthTimeout resolves the readiness budget for one rollout.
// An operator-set timeout always wins; otherwise an immutable-image deploy
// gets the shorter DefaultImageHealthTimeout window.
func EffectiveHealthTimeout(cfg Config, immutableImage bool) time.Duration {
	if cfg.HealthTimeout > 0 && cfg.HealthTimeout != DefaultHealthTimeout {
		return cfg.HealthTimeout
	}
	if immutableImage {
		return DefaultImageHealthTimeout
	}
	return DefaultHealthTimeout
}
