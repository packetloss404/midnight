// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package docker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/strslice"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/network"
	"github.com/midnight/midnight/internal/runtime"
)

const oneOffPollInterval = 100 * time.Millisecond

// RunOneOff executes a pre-deploy command in a deterministic, retained
// container. The retained terminal resource is the runtime half of the
// crash-safety protocol: if the worker dies after the command succeeds but
// before Postgres is updated, the retry inspects the same exited container
// and returns its exit code without starting it again.
func (a *Adapter) RunOneOff(ctx context.Context, request runtime.OneOffRequest) (runtime.OneOffResult, error) {
	if a == nil || a.client == nil {
		return runtime.OneOffResult{}, newError(failurecodes.AgentRuntimeUnavailable, "one-off Docker runtime is unavailable", "")
	}
	if err := validateOneOffRequest(request); err != nil {
		return runtime.OneOffResult{}, err
	}
	deadline := request.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(request.Timeout)
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	name := oneOffContainerName(request)
	result := runtime.OneOffResult{ContainerName: name, ExitCode: -1}
	// Initial observation uses the parent context, not the possibly-expired
	// execution deadline. A retry after timeout must still discover and stop a
	// running retained container; treating the inspect deadline as terminal
	// without observation could leave the migration running in the background.
	inspect, err := a.client.ContainerInspect(ctx, name)
	if err != nil && !isNotFound(err) {
		return result, newError(failurecodes.AgentRuntimeUnavailable, "inspect one-off container failed", err.Error())
	}
	if isNotFound(err) {
		if !time.Now().Before(deadline) {
			result.TimedOut = true
			return result, nil
		}
		// Verification and pull happen only for the first create. A retry
		// must be able to observe a retained successful container even if
		// the registry is temporarily unavailable.
		if _, err := a.PullVerify(runCtx, request.ScheduleRequest.Image); err != nil {
			if oneOffTimedOut(ctx, runCtx) {
				result.TimedOut = true
				return result, nil
			}
			return result, err
		}
		inspect, err = a.createOneOff(runCtx, request, name)
		if err != nil {
			if oneOffTimedOut(ctx, runCtx) {
				result.TimedOut = true
				return result, nil
			}
			return result, err
		}
	}
	if err := validateExistingOneOff(inspect, request); err != nil {
		return result, err
	}
	result.ContainerID = inspect.ID

	if inspect.State == nil {
		return result, newError(failurecodes.AgentRuntimeUnavailable, "one-off container has no state", name)
	}
	switch strings.ToLower(strings.TrimSpace(string(inspect.State.Status))) {
	case "exited", "dead":
		result.ExitCode = inspect.State.ExitCode
		if finishedAt, err := time.Parse(time.RFC3339Nano, inspect.State.FinishedAt); err == nil &&
			!finishedAt.Before(deadline) {
			result.TimedOut = true
			result.ExitCode = -1
		}
		return result, nil
	case "created":
		if !time.Now().Before(deadline) {
			result.TimedOut = true
			return result, nil
		}
		if err := a.client.ContainerStart(runCtx, inspect.ID); err != nil {
			if oneOffTimedOut(ctx, runCtx) {
				return a.stopTimedOutOneOff(ctx, result)
			}
			return result, newError(failurecodes.AgentRuntimeUnavailable, "start one-off container failed", err.Error())
		}
	case "running", "restarting":
		// Observe the existing attempt; never issue another start.
	default:
		return result, newError(
			failurecodes.AgentRuntimeUnavailable,
			"one-off container is not runnable",
			string(inspect.State.Status),
		)
	}

	ticker := time.NewTicker(oneOffPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return a.stopTimedOutOneOff(ctx, result)
		case <-ticker.C:
			current, err := a.client.ContainerInspect(runCtx, result.ContainerID)
			if err != nil {
				if oneOffTimedOut(ctx, runCtx) {
					return a.stopTimedOutOneOff(ctx, result)
				}
				return result, newError(failurecodes.AgentRuntimeUnavailable, "observe one-off container failed", err.Error())
			}
			if current.State == nil {
				return result, newError(failurecodes.AgentRuntimeUnavailable, "one-off container has no state", name)
			}
			if !current.State.Running && !current.State.Restarting {
				result.ExitCode = current.State.ExitCode
				return result, nil
			}
		}
	}
}

func (a *Adapter) createOneOff(ctx context.Context, request runtime.OneOffRequest, name string) (container.InspectResponse, error) {
	req := request.ScheduleRequest
	labels := mergeLabels(req, a.cfg)
	labels[LabelWorkloadKind] = oneOffKind(request)
	labels[oneOffSpecLabel(request)] = request.SpecSHA256
	cfg := HardenedContainerConfig(req.Image, labels)
	cfg.Env = applyEnvWithBuiltins(req, canonicalContainerName(req))
	cfg.Entrypoint = strslice.StrSlice{"/bin/sh", "-lc"}
	cfg.Cmd = strslice.StrSlice{request.Command}

	hc := HardenedHostConfig()
	hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
	// Do not call applyScheduleMounts: persistent data is intentionally
	// unavailable to migration commands.
	netCfg, err := a.buildOneOffNetworkingConfig(ctx, req)
	if err != nil {
		return container.InspectResponse{}, err
	}
	id, err := a.client.ContainerCreateWithNetworking(ctx, name, cfg, hc, netCfg)
	if err != nil {
		if !isNameInUse(err) {
			return container.InspectResponse{}, newError(failurecodes.AgentRuntimeUnavailable, "create one-off container failed", err.Error())
		}
		// A concurrent retry won the deterministic create race. Inspect and
		// converge on that same resource.
		return a.client.ContainerInspect(ctx, name)
	}
	return a.client.ContainerInspect(ctx, id)
}

func (a *Adapter) buildOneOffNetworkingConfig(ctx context.Context, req runtime.ScheduleRequest) (*dockernetwork.NetworkingConfig, error) {
	if a.provisioner == nil {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"private network provisioner is unavailable",
			"predeploy project/environment isolation cannot be guaranteed",
		)
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.EnvironmentID) == "" {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"project_id and environment_id are required for private networking",
			req.DeploymentID,
		)
	}
	if _, err := a.provisioner.EnsureNetwork(ctx, req.ProjectID, req.EnvironmentID); err != nil {
		return nil, newError(failurecodes.PrivateNetworkUnavailable, "private network convergence failed", err.Error())
	}
	networkName := (network.ProjectNetwork{ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID}).Name()
	return &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			networkName: {},
		},
	}, nil
}

func (a *Adapter) stopTimedOutOneOff(parent context.Context, result runtime.OneOffResult) (runtime.OneOffResult, error) {
	if parent.Err() != nil {
		return result, parent.Err()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if result.ContainerID != "" {
		if err := a.client.ContainerStop(stopCtx, result.ContainerID, 10*time.Second); err != nil && !isNotFound(err) {
			return result, newError(failurecodes.AgentRuntimeUnavailable, "stop timed-out one-off container failed", err.Error())
		}
	}
	result.TimedOut = true
	result.ExitCode = -1
	return result, nil
}

func validateOneOffRequest(request runtime.OneOffRequest) error {
	req := request.ScheduleRequest
	if strings.TrimSpace(req.DeploymentID) == "" || strings.TrimSpace(req.InstanceID) == "" ||
		strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.EnvironmentID) == "" ||
		strings.TrimSpace(req.ServiceID) == "" || strings.TrimSpace(req.Image) == "" ||
		strings.TrimSpace(request.Command) == "" || request.Timeout <= 0 {
		return newError(failurecodes.ContainerSpecInvalid, "one-off specification is incomplete", req.DeploymentID)
	}
	if len(req.Mounts) != 0 {
		return newError(failurecodes.ContainerSpecInvalid, "persistent mounts are forbidden for one-off containers", req.DeploymentID)
	}
	if len(request.SpecSHA256) != 64 || !isLowerHex(request.SpecSHA256) {
		return newError(failurecodes.ContainerSpecInvalid, "one-off spec digest is invalid", req.DeploymentID)
	}
	switch oneOffKind(request) {
	case WorkloadKindPredeploy, WorkloadKindCron, WorkloadKindTemplateSmoke:
	default:
		return newError(failurecodes.ContainerSpecInvalid, "one-off workload kind is invalid", oneOffKind(request))
	}
	return nil
}

func validateExistingOneOff(inspect container.InspectResponse, request runtime.OneOffRequest) error {
	if inspect.Config == nil {
		return newError(failurecodes.ContainerNameConflict, "one-off container has no configuration", inspect.ID)
	}
	labels := inspect.Config.Labels
	if !ownedByMidnight(labels) ||
		labels[LabelWorkloadKind] != oneOffKind(request) ||
		labels[LabelDeployment] != request.ScheduleRequest.DeploymentID ||
		labels[oneOffSpecLabel(request)] != request.SpecSHA256 {
		return newError(failurecodes.ContainerNameConflict, "one-off container identity does not match immutable specification", inspect.ID)
	}
	if inspect.Config.Image != request.ScheduleRequest.Image {
		return newError(failurecodes.ContainerNameConflict, "one-off container image drift detected", inspect.ID)
	}
	return nil
}

func oneOffKind(request runtime.OneOffRequest) string {
	if strings.TrimSpace(request.WorkloadKind) == "" {
		return WorkloadKindPredeploy
	}
	return strings.TrimSpace(request.WorkloadKind)
}

func oneOffSpecLabel(request runtime.OneOffRequest) string {
	if oneOffKind(request) == WorkloadKindCron {
		return LabelCronSpec
	}
	if oneOffKind(request) == WorkloadKindTemplateSmoke {
		return LabelTemplateSmokeSpec
	}
	return LabelPredeploySpec
}

func oneOffContainerName(request runtime.OneOffRequest) string {
	return "or-" + oneOffKind(request) + "-" + request.SpecSHA256[:24]
}

func oneOffTimedOut(parent, run context.Context) bool {
	return parent.Err() == nil && errors.Is(run.Err(), context.DeadlineExceeded)
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
