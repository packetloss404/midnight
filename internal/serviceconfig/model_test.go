// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package serviceconfig

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultIsValidAndProductionSafe(t *testing.T) {
	cfg := Default("prj_1", "svc_1", "env_1")
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate(Default): %v", err)
	}
	if cfg.TargetPort != 8080 || cfg.HealthTimeout != 300*time.Second ||
		cfg.HealthKind != HealthTCP || cfg.RestartPolicy != RestartUnlessStopped || !cfg.OverlapEnabled ||
		cfg.DrainGrace != 30*time.Second || cfg.Replicas != 1 {
		t.Fatalf("Default() = %+v", cfg)
	}
}

func TestValidateRejectsUnsafeOrUnrepresentableConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "root escape", mutate: func(c *Config) { c.RootDirectory = "../secret" }},
		{name: "absolute dockerfile", mutate: func(c *Config) { c.DockerfilePath = "/Dockerfile" }},
		{name: "health redirect-shaped query", mutate: func(c *Config) {
			c.HealthKind, c.HealthPath = HealthHTTP, "/ready?token=x"
		}},
		{name: "retry without on failure", mutate: func(c *Config) { c.RestartRetries = 3 }},
		{name: "too many replicas", mutate: func(c *Config) { c.Replicas = MaxReplicas + 1 }},
		{name: "interval not below timeout", mutate: func(c *Config) {
			c.HealthInterval, c.HealthTimeout = time.Minute, time.Minute
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default("prj_1", "svc_1", "env_1")
			test.mutate(&cfg)
			if err := Validate(cfg); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateCronRequiresHonestOneOffShape(t *testing.T) {
	cfg := Default("prj-1", "svc-1", "env-1")
	cfg.RuntimeKind = RuntimeKindCron
	cfg.CronSchedule = "*/5 * * * *"
	cfg.StartCommand = "bin/job"
	cfg.RestartPolicy = RestartNo
	cfg.HealthKind = HealthNone
	cfg.Replicas = 1
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate(cron): %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"six fields":         func(c *Config) { c.CronSchedule = "0 */5 * * * *" },
		"one minute cadence": func(c *Config) { c.CronSchedule = "* * * * *" },
		"persistent restart": func(c *Config) { c.RestartPolicy = RestartAlways },
		"replicas":           func(c *Config) { c.Replicas = 2 },
		"probe":              func(c *Config) { c.HealthKind = HealthTCP },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cfg
			mutate(&bad)
			if err := Validate(bad); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestEffectiveHealthKindFallsBackToNoneOnlyForPortlessImages(t *testing.T) {
	defaults := Default("p", "s", "e")
	if got := EffectiveHealthKind(defaults, false); got != HealthNone {
		t.Fatalf("portless image with default config resolved to %q, want %q", got, HealthNone)
	}
	if got := EffectiveHealthKind(defaults, true); got != HealthTCP {
		t.Fatalf("image declaring a port resolved to %q, want %q", got, HealthTCP)
	}
}

func TestEffectiveHealthKindKeepsOperatorChoices(t *testing.T) {
	chosenPort := Default("p", "s", "e")
	chosenPort.TargetPort = 5432
	if got := EffectiveHealthKind(chosenPort, false); got != HealthTCP {
		t.Fatalf("operator-chosen port resolved to %q, want the configured %q", got, HealthTCP)
	}

	chosenHealthPort := Default("p", "s", "e")
	chosenHealthPort.HealthPort = 9000
	if got := EffectiveHealthKind(chosenHealthPort, false); got != HealthTCP {
		t.Fatalf("operator-chosen health port resolved to %q, want the configured %q", got, HealthTCP)
	}

	chosenHTTP := Default("p", "s", "e")
	chosenHTTP.HealthKind = HealthHTTP
	chosenHTTP.HealthPath = "/healthz"
	if got := EffectiveHealthKind(chosenHTTP, false); got != HealthHTTP {
		t.Fatalf("operator-chosen HTTP probe resolved to %q, want %q", got, HealthHTTP)
	}

	chosenNone := Default("p", "s", "e")
	chosenNone.HealthKind = HealthNone
	if got := EffectiveHealthKind(chosenNone, true); got != HealthNone {
		t.Fatalf("operator-chosen none resolved to %q, want %q", got, HealthNone)
	}
}

func TestEffectiveHealthTimeoutShortensOnlyImageDeployDefaults(t *testing.T) {
	defaults := Default("p", "s", "e")
	if got := EffectiveHealthTimeout(defaults, true); got != DefaultImageHealthTimeout {
		t.Fatalf("image deploy default timeout = %s, want %s", got, DefaultImageHealthTimeout)
	}
	if got := EffectiveHealthTimeout(defaults, false); got != DefaultHealthTimeout {
		t.Fatalf("source build default timeout = %s, want %s", got, DefaultHealthTimeout)
	}

	chosen := Default("p", "s", "e")
	chosen.HealthTimeout = 9 * time.Minute
	if got := EffectiveHealthTimeout(chosen, true); got != 9*time.Minute {
		t.Fatalf("operator timeout = %s, want it to win over both defaults", got)
	}
}
