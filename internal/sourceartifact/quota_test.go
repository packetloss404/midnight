// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSourceArtifactAdvisoryLockKeysAreDistinct(t *testing.T) {
	keys := []int64{
		LifecycleAdvisoryLockKey,
		legacyJanitorAdvisoryLockKey,
		QuotaAdvisoryLockKey,
	}
	seen := map[int64]struct{}{}
	for _, key := range keys {
		if _, exists := seen[key]; exists {
			t.Fatalf("source artifact advisory lock key %d is reused", key)
		}
		seen[key] = struct{}{}
	}
}

func TestQuotaAdmissionArithmeticCannotOverflow(t *testing.T) {
	config := DefaultQuotaConfig
	config.WorkspaceBytes = math.MaxInt64
	config.WorkspaceInflightBytes = math.MaxInt64
	config.GlobalInflightBytes = math.MaxInt64
	store := NewQuotaStore(nil, config)
	err := store.enforceAdmission(quotaUsage{
		LogicalBytes:           math.MaxInt64 - 1,
		WorkspaceInflightBytes: math.MaxInt64 - 1,
		GlobalInflightBytes:    math.MaxInt64 - 1,
	}, 2)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("error=%v, want quota exceeded without overflow", err)
	}
}

func TestLoadQuotaConfigStrictlyParsesAndChecksRetention(t *testing.T) {
	values := map[string]string{
		EnvWorkspaceQuotaBytes:   "6442450944",
		EnvUploadLease:           "11m",
		EnvOrphanChargeRetention: "27h",
	}
	config, err := LoadQuotaConfig(func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}, DefaultLimits.MaxCompressedBytes, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if config.WorkspaceBytes != 6<<30 ||
		config.UploadLease != 11*time.Minute ||
		config.OrphanChargeRetention != 27*time.Hour {
		t.Fatalf("config=%#v", config)
	}

	for name, value := range map[string]string{
		"malformed bytes":       "not-a-number",
		"nonpositive bytes":     "0",
		"malformed duration":    "forever",
		"short charge duration": "24h",
	} {
		t.Run(name, func(t *testing.T) {
			lookup := func(variable string) (string, bool) {
				switch {
				case strings.Contains(name, "duration") && variable == EnvUploadLease:
					return value, true
				case strings.Contains(name, "charge") && variable == EnvOrphanChargeRetention:
					return value, true
				case !strings.Contains(name, "duration") &&
					!strings.Contains(name, "charge") &&
					variable == EnvWorkspaceQuotaBytes:
					return value, true
				default:
					return "", false
				}
			}
			if _, err := LoadQuotaConfig(
				lookup,
				DefaultLimits.MaxCompressedBytes,
				5*time.Minute,
			); err == nil {
				t.Fatal("unsafe environment config was accepted")
			}
		})
	}
}

func TestDefaultQuotaConfigValidatesAgainstUploadContract(t *testing.T) {
	if err := DefaultQuotaConfig.Validate(DefaultLimits.MaxCompressedBytes, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaRetentionCoverageRejectsDurationOverflow(t *testing.T) {
	config := DefaultQuotaConfig
	if err := config.ValidateRetentionCoverage(
		time.Duration(math.MaxInt64),
		time.Second,
		time.Second,
		time.Second,
	); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("error=%v, want overflow rejection", err)
	}
	if err := config.ValidateRetentionCoverage(
		time.Second,
		time.Second,
		time.Duration(math.MaxInt64),
		time.Second,
	); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("error=%v, want doubled interval overflow rejection", err)
	}
}

func TestQuotaConfigRejectsUnsafeBoundaries(t *testing.T) {
	tests := map[string]func(*QuotaConfig){
		"workspace bytes": func(config *QuotaConfig) {
			config.WorkspaceBytes = DefaultLimits.MaxCompressedBytes - 1
		},
		"workspace inflight bytes": func(config *QuotaConfig) {
			config.WorkspaceInflightBytes = DefaultLimits.MaxCompressedBytes - 1
		},
		"workspace inflight count": func(config *QuotaConfig) {
			config.WorkspaceInflightCount = 0
		},
		"global inflight bytes": func(config *QuotaConfig) {
			config.GlobalInflightBytes = config.WorkspaceInflightBytes - 1
		},
		"global inflight count": func(config *QuotaConfig) {
			config.GlobalInflightCount = config.WorkspaceInflightCount - 1
		},
		"upload lease": func(config *QuotaConfig) {
			config.UploadLease = time.Minute
		},
		"charge retention": func(config *QuotaConfig) {
			config.OrphanChargeRetention = config.UploadLease - time.Second
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := DefaultQuotaConfig
			mutate(&config)
			if err := config.Validate(DefaultLimits.MaxCompressedBytes, 5*time.Minute); err == nil {
				t.Fatal("unsafe quota config was accepted")
			}
		})
	}
}
