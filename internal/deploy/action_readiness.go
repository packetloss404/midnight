// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/deploy/readiness"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/serviceconfig"
)

func (e ActionExecutor) waitRestartReady(ctx context.Context, action RuntimeActionRecord) error {
	resolver, ok := e.Runtime.(InstanceRuntimeResolver)
	if !ok {
		return errors.New("restart requires a routing target resolver")
	}
	observer, canObserve := e.Instances.(interface {
		Observe(context.Context, runtime.RoutingTarget) error
	})
	if e.Instances != nil && !canObserve {
		return errors.New("restart requires durable instance observation")
	}
	cfg := serviceconfig.Default(action.ProjectID, action.ServiceID, action.EnvironmentID)
	if e.Configuration != nil {
		snapshot, err := e.Configuration.ResolveDeployment(ctx, action.DeploymentID, action.ProjectID, action.EnvironmentID, action.ServiceID)
		if err != nil {
			return fmt.Errorf("restart configuration: %w", err)
		}
		cfg = snapshot.Config
		if reporter, ok := e.Runtime.(runtime.ImagePortReporter); ok && action.Image != "" {
			ports, err := reporter.ImageDeclaredPorts(ctx, action.Image)
			if err == nil {
				cfg.HealthKind = serviceconfig.EffectiveHealthKind(cfg, len(ports) > 0)
			}
		}
	}
	timeout := e.Readiness.Timeout
	if timeout <= 0 {
		timeout = cfg.HealthTimeout
	}
	return readiness.Wait(ctx, readiness.ProbeFunc(func(ctx context.Context) error {
		return e.forEachInstance(ctx, action, func(instanceID string) error {
			target, err := resolver.ResolveRoutingTarget(ctx, action.DeploymentID, instanceID)
			if err != nil {
				return err
			}
			if target.DeploymentID != action.DeploymentID || target.InstanceID != instanceID ||
				(action.ProjectID != "" && target.ProjectID != action.ProjectID) ||
				(action.EnvironmentID != "" && target.EnvironmentID != action.EnvironmentID) {
				return readiness.Fatal(errors.New("restart routing target scope mismatch"))
			}
			if !target.Ready || strings.TrimSpace(target.ContainerIP) == "" {
				return errors.New("restarted instance is not ready")
			}
			if e.Configuration != nil {
				probe, err := applicationReadinessProbe(cfg, target)
				if err != nil {
					return err
				}
				if probe != nil {
					if err := probe.CheckReady(ctx); err != nil {
						return err
					}
				}
			}
			if canObserve {
				return observer.Observe(ctx, target)
			}
			return nil
		})
	}), readiness.Options{Timeout: timeout, Interval: e.Readiness.Interval, Now: e.Readiness.Now, Sleep: e.Readiness.Sleep})
}
