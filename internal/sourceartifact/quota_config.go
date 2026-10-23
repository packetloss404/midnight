// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	EnvWorkspaceQuotaBytes    = "MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_QUOTA_BYTES"
	EnvWorkspaceInflightBytes = "MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_BYTES"
	EnvWorkspaceInflightCount = "MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_COUNT"
	EnvGlobalInflightBytes    = "MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_BYTES"
	EnvGlobalInflightCount    = "MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_COUNT"
	EnvUploadLease            = "MIDNIGHT_SOURCE_ARTIFACT_UPLOAD_LEASE"
	EnvOrphanChargeRetention  = "MIDNIGHT_SOURCE_ARTIFACT_ORPHAN_CHARGE_RETENTION"
	EnvBlobRetention          = "MIDNIGHT_SOURCE_ARTIFACT_RETENTION"
	EnvQuarantineRetention    = "MIDNIGHT_SOURCE_ARTIFACT_QUARANTINE_RETENTION"
	EnvGCInterval             = "MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL"
	EnvGCTimeout              = "MIDNIGHT_SOURCE_ARTIFACT_GC_TIMEOUT"

	DefaultGCInterval        = 15 * time.Minute
	DefaultGCTimeout         = 30 * time.Second
	orphanChargeTimingMargin = 30 * time.Minute
)

// LoadQuotaConfig parses the production quota environment strictly. An
// explicitly malformed or unsafe value fails boot rather than silently
// widening or disabling the boundary.
func LoadQuotaConfig(
	lookup func(string) (string, bool),
	maxUploadBytes int64,
	minimumLease time.Duration,
) (QuotaConfig, error) {
	if lookup == nil {
		return QuotaConfig{}, fmt.Errorf("source artifact quota: environment lookup is required")
	}
	config := DefaultQuotaConfig
	var err error
	if config.WorkspaceBytes, err = quotaEnvInt64(
		lookup,
		EnvWorkspaceQuotaBytes,
		config.WorkspaceBytes,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.WorkspaceInflightBytes, err = quotaEnvInt64(
		lookup,
		EnvWorkspaceInflightBytes,
		config.WorkspaceInflightBytes,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.WorkspaceInflightCount, err = quotaEnvInt(
		lookup,
		EnvWorkspaceInflightCount,
		config.WorkspaceInflightCount,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.GlobalInflightBytes, err = quotaEnvInt64(
		lookup,
		EnvGlobalInflightBytes,
		config.GlobalInflightBytes,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.GlobalInflightCount, err = quotaEnvInt(
		lookup,
		EnvGlobalInflightCount,
		config.GlobalInflightCount,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.UploadLease, err = quotaEnvDuration(
		lookup,
		EnvUploadLease,
		config.UploadLease,
	); err != nil {
		return QuotaConfig{}, err
	}
	if config.OrphanChargeRetention, err = quotaEnvDuration(
		lookup,
		EnvOrphanChargeRetention,
		config.OrphanChargeRetention,
	); err != nil {
		return QuotaConfig{}, err
	}
	blobRetention, err := quotaEnvDuration(
		lookup,
		EnvBlobRetention,
		DefaultJanitorConfig.BlobRetention,
	)
	if err != nil {
		return QuotaConfig{}, err
	}
	quarantineRetention, err := quotaEnvDuration(
		lookup,
		EnvQuarantineRetention,
		DefaultJanitorConfig.QuarantineRetention,
	)
	if err != nil {
		return QuotaConfig{}, err
	}
	gcInterval, err := quotaEnvDuration(
		lookup,
		EnvGCInterval,
		DefaultGCInterval,
	)
	if err != nil {
		return QuotaConfig{}, err
	}
	gcTimeout, err := quotaEnvDuration(
		lookup,
		EnvGCTimeout,
		DefaultGCTimeout,
	)
	if err != nil {
		return QuotaConfig{}, err
	}
	if err := config.Validate(maxUploadBytes, minimumLease); err != nil {
		return QuotaConfig{}, err
	}
	if err := config.ValidateRetentionCoverage(
		blobRetention,
		quarantineRetention,
		gcInterval,
		gcTimeout,
	); err != nil {
		return QuotaConfig{}, err
	}
	return config, nil
}

// ValidateRetentionCoverage ensures a conservative published-orphan charge
// cannot expire before two automatic janitor passes have had time to
// quarantine and finalize the blob.
func (config QuotaConfig) ValidateRetentionCoverage(
	blobRetention time.Duration,
	quarantineRetention time.Duration,
	gcInterval time.Duration,
	gcTimeout time.Duration,
) error {
	if blobRetention <= 0 ||
		quarantineRetention <= 0 ||
		gcInterval <= 0 ||
		gcTimeout <= 0 {
		return fmt.Errorf("source artifact quota: janitor retention and timing values must be positive")
	}
	timingMargin := orphanChargeTimingMargin
	if gcTimeout > timingMargin {
		timingMargin = gcTimeout
	}
	if gcInterval > time.Duration(math.MaxInt64/2) {
		return fmt.Errorf("source artifact quota: janitor retention coverage overflows")
	}
	minimumCharge, ok := checkedDurationSum(
		blobRetention,
		quarantineRetention,
		2*gcInterval,
		timingMargin,
	)
	if !ok {
		return fmt.Errorf("source artifact quota: janitor retention coverage overflows")
	}
	if config.OrphanChargeRetention < minimumCharge {
		return fmt.Errorf(
			"source artifact quota: %s must be at least blob retention + quarantine retention + two GC intervals + timing margin (%s)",
			EnvOrphanChargeRetention,
			minimumCharge,
		)
	}
	return nil
}

func checkedDurationSum(values ...time.Duration) (time.Duration, bool) {
	var total time.Duration
	for _, value := range values {
		if value < 0 || total > time.Duration(math.MaxInt64)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func quotaEnvInt64(
	lookup func(string) (string, bool),
	name string,
	fallback int64,
) (int64, error) {
	raw, present := lookup(name)
	if !present || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("source artifact quota: %s must be a positive integer", name)
	}
	return value, nil
}

func quotaEnvInt(
	lookup func(string) (string, bool),
	name string,
	fallback int,
) (int, error) {
	value, err := quotaEnvInt64(lookup, name, int64(fallback))
	if err != nil {
		return 0, err
	}
	converted := int(value)
	if int64(converted) != value {
		return 0, fmt.Errorf("source artifact quota: %s exceeds platform integer range", name)
	}
	return converted, nil
}

func quotaEnvDuration(
	lookup func(string) (string, bool),
	name string,
	fallback time.Duration,
) (time.Duration, error) {
	raw, present := lookup(name)
	if !present || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("source artifact quota: %s must be a positive duration", name)
	}
	return value, nil
}
