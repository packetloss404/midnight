// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package docker

import (
	"context"
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"

	"github.com/midnight/midnight/internal/runtime"
)

func TestScheduleAppliesCommandPortAndRestartPolicy(t *testing.T) {
	adapter, engine := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:      "dep_config",
		InstanceID:        "inst_config",
		ProjectID:         "prj_config",
		EnvironmentID:     "env_config",
		ServiceID:         "svc_config",
		Image:             "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey:    "config",
		StartCommand:      "bin/server --production",
		TargetPort:        9090,
		RestartPolicy:     "on-failure",
		RestartMaxRetries: 6,
		Replicas:          1,
	}
	if _, err := adapter.Schedule(context.Background(), req); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	var cfg *container.Config
	var hostConfig *container.HostConfig
	for _, item := range engine.containers {
		cfg, hostConfig = item.cfg, item.hc
	}
	if cfg == nil || hostConfig == nil {
		t.Fatal("container config was not recorded")
	}
	if !reflect.DeepEqual([]string(cfg.Entrypoint), []string{"/bin/sh", "-lc"}) ||
		!reflect.DeepEqual([]string(cfg.Cmd), []string{"bin/server --production"}) {
		t.Fatalf("entrypoint/cmd = %v / %v", cfg.Entrypoint, cfg.Cmd)
	}
	if !hasEnvValue(cfg.Env, "PORT=9090") {
		t.Fatalf("env = %v, want PORT=9090", cfg.Env)
	}
	if _, exists := cfg.ExposedPorts[dockernetwork.MustParsePort("9090/tcp")]; !exists {
		t.Fatalf("exposed ports = %v, want 9090/tcp", cfg.ExposedPorts)
	}
	if hostConfig.RestartPolicy.Name != container.RestartPolicyOnFailure ||
		hostConfig.RestartPolicy.MaximumRetryCount != 6 {
		t.Fatalf("restart policy = %+v", hostConfig.RestartPolicy)
	}
}

func hasEnvValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
