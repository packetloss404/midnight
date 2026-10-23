// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

const DefaultPredeployTimeout = 10 * time.Minute

const (
	PredeployStatePending   = "pending"
	PredeployStateRunning   = "running"
	PredeployStateSucceeded = "succeeded"
	PredeployStateFailed    = "failed"
)

var (
	ErrPredeployUnavailable  = errors.New("predeploy executor is not configured")
	ErrPredeployInvalid      = errors.New("predeploy request is invalid")
	ErrPredeploySpecConflict = errors.New("predeploy immutable specification conflict")
)

// PredeployRun is the redaction-safe durable record for one deployment's
// one-shot command. Command text and environment values never enter this
// record; SpecSHA256 binds the command and variable-version identities to the
// immutable deployment snapshot.
type PredeployRun struct {
	DeploymentID    string
	ProjectID       string
	EnvironmentID   string
	ServiceID       string
	SpecSHA256      string
	State           string
	ContainerName   string
	ContainerID     string
	ExitCode        *int
	FailureCode     failurecodes.Code
	FailureMessage  string
	StartedAt       *time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion int64
}

// PredeployStore persists the one-shot state machine. Ensure is idempotent on
// deployment id + spec digest; implementations reject any immutable drift.
type PredeployStore interface {
	Ensure(context.Context, PredeployRun) (PredeployRun, error)
	MarkRunning(context.Context, string, string) (PredeployRun, error)
	MarkTerminal(context.Context, string, string, runtime.OneOffResult, failurecodes.Code, string) (PredeployRun, error)
}

// PredeployRunner is the runtime-specific one-off execution boundary. It must
// observe deterministic existing resources on retry and never restart a
// terminal container.
type PredeployRunner interface {
	RunOneOff(context.Context, runtime.OneOffRequest) (runtime.OneOffResult, error)
}

type PredeployRequest struct {
	Candidate      runtime.ScheduleRequest
	Command        string
	Timeout        time.Duration
	SnapshotSHA256 string
}

// PredeployExecutor combines durable state with the runtime boundary. A
// worker crash after the command exits but before MarkTerminal is harmless:
// retry reaches the same deterministic runtime resource and observes its exit
// status instead of running the command again.
type PredeployExecutor struct {
	Store  PredeployStore
	Runner PredeployRunner
	Now    func() time.Time
}

func (e *PredeployExecutor) Execute(ctx context.Context, request PredeployRequest) (PredeployRun, error) {
	if e == nil || e.Store == nil || e.Runner == nil {
		return PredeployRun{}, &PredeployFailure{
			Code: failurecodes.NotImplemented, Message: ErrPredeployUnavailable.Error(),
		}
	}
	request.Command = strings.TrimSpace(request.Command)
	if request.Timeout <= 0 {
		request.Timeout = DefaultPredeployTimeout
	}
	if err := validatePredeployRequest(request); err != nil {
		return PredeployRun{}, err
	}
	specSHA, err := predeploySpecSHA256(request)
	if err != nil {
		return PredeployRun{}, err
	}
	run, err := e.Store.Ensure(ctx, PredeployRun{
		DeploymentID:  request.Candidate.DeploymentID,
		ProjectID:     request.Candidate.ProjectID,
		EnvironmentID: request.Candidate.EnvironmentID,
		ServiceID:     request.Candidate.ServiceID,
		SpecSHA256:    specSHA,
		State:         PredeployStatePending,
	})
	if err != nil {
		return PredeployRun{}, err
	}
	switch run.State {
	case PredeployStateSucceeded:
		return run, nil
	case PredeployStateFailed:
		return run, failureFromRun(run)
	}
	running, err := e.Store.MarkRunning(ctx, run.DeploymentID, specSHA)
	if err != nil {
		return PredeployRun{}, err
	}
	switch running.State {
	case PredeployStateSucceeded:
		return running, nil
	case PredeployStateFailed:
		return running, failureFromRun(running)
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	remaining := request.Timeout
	deadline := now.Add(request.Timeout)
	if running.StartedAt != nil {
		deadline = running.StartedAt.UTC().Add(request.Timeout)
		if elapsed := now.Sub(running.StartedAt.UTC()); elapsed > 0 {
			remaining -= elapsed
			if remaining <= 0 {
				remaining = time.Nanosecond
			}
		}
	}

	candidate := request.Candidate
	// Persistent service volumes are intentionally unavailable to pre-deploy
	// commands. The one-off runtime independently enforces this boundary too.
	candidate.Mounts = nil
	candidate.RestartPolicy = "no"
	candidate.RestartMaxRetries = 0
	result, err := e.Runner.RunOneOff(ctx, runtime.OneOffRequest{
		ScheduleRequest: candidate,
		Command:         request.Command,
		SpecSHA256:      specSHA,
		Timeout:         remaining,
		Deadline:        deadline,
	})
	if err != nil {
		// Transport loss or worker cancellation is not a terminal command
		// result. Leave the row running so a queue retry observes the same
		// deterministic container.
		return PredeployRun{}, err
	}

	code := failurecodes.Code("")
	message := ""
	if result.TimedOut {
		code = failurecodes.PredeployTimeout
		message = fmt.Sprintf("predeploy command exceeded timeout %s", request.Timeout)
	} else if result.ExitCode != 0 {
		code = failurecodes.PredeployFailed
		message = fmt.Sprintf("predeploy command exited with status %d", result.ExitCode)
	}
	terminal, err := e.Store.MarkTerminal(ctx, run.DeploymentID, specSHA, result, code, message)
	if err != nil {
		// The terminal Docker container remains available. A later retry
		// observes it and repairs this durable state without rerunning it.
		return PredeployRun{}, err
	}
	if terminal.State == PredeployStateFailed {
		return terminal, failureFromRun(terminal)
	}
	return terminal, nil
}

// PredeployFailure carries a registered operation failure code without
// exposing command or environment contents in the error string.
type PredeployFailure struct {
	Code    failurecodes.Code
	Message string
}

func (e *PredeployFailure) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *PredeployFailure) Reason() (failurecodes.Code, string) {
	if e == nil {
		return failurecodes.InternalError, ""
	}
	return e.Code, failurecodes.RecoveryHint(e.Code)
}

func failureFromRun(run PredeployRun) error {
	code := run.FailureCode
	if !failurecodes.IsRegistered(code) {
		code = failurecodes.PredeployFailed
	}
	message := strings.TrimSpace(run.FailureMessage)
	if message == "" {
		message = "predeploy command failed"
	}
	return &PredeployFailure{Code: code, Message: message}
}

func validatePredeployRequest(request PredeployRequest) error {
	candidate := request.Candidate
	if candidate.DeploymentID == "" || candidate.ProjectID == "" ||
		candidate.EnvironmentID == "" || candidate.ServiceID == "" ||
		candidate.Image == "" || request.Command == "" ||
		len(request.SnapshotSHA256) != 64 || !isLowerHexDigest(request.SnapshotSHA256) {
		return fmt.Errorf("%w: deployment, project, environment, service, image, command, and immutable snapshot digest are required", ErrPredeployInvalid)
	}
	if request.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrPredeployInvalid)
	}
	if len(candidate.Mounts) > 0 {
		return fmt.Errorf("%w: persistent mounts are not allowed", ErrPredeployInvalid)
	}
	return nil
}

func predeploySpecSHA256(request PredeployRequest) (string, error) {
	// encoding/json sorts map keys, giving Labels deterministic ordering.
	canonical := struct {
		DeploymentID    string            `json:"deployment_id"`
		InstanceID      string            `json:"instance_id"`
		ProjectID       string            `json:"project_id"`
		EnvironmentID   string            `json:"environment_id"`
		ServiceID       string            `json:"service_id"`
		ServiceSlug     string            `json:"service_slug"`
		ProjectSlug     string            `json:"project_slug"`
		EnvironmentName string            `json:"environment_name"`
		Image           string            `json:"image"`
		Labels          map[string]string `json:"labels"`
		EnvKeys         []string          `json:"env_keys"`
		PublicDomain    string            `json:"public_domain"`
		Command         string            `json:"command"`
		TimeoutNanos    int64             `json:"timeout_nanos"`
		SnapshotSHA256  string            `json:"snapshot_sha256"`
	}{
		DeploymentID: request.Candidate.DeploymentID, InstanceID: request.Candidate.InstanceID,
		ProjectID: request.Candidate.ProjectID, EnvironmentID: request.Candidate.EnvironmentID,
		ServiceID: request.Candidate.ServiceID, ServiceSlug: request.Candidate.ServiceSlug,
		ProjectSlug: request.Candidate.ProjectSlug, EnvironmentName: request.Candidate.EnvironmentName,
		Image: request.Candidate.Image, Labels: request.Candidate.Labels,
		EnvKeys: predeployEnvKeys(request.Candidate.Env), PublicDomain: request.Candidate.PublicDomain,
		Command: request.Command, TimeoutNanos: int64(request.Timeout),
		SnapshotSHA256: request.SnapshotSHA256,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal predeploy specification: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func predeployEnvKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func isLowerHexDigest(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
