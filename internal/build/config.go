package build

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// WorkerConfig contains local build-worker safety limits.
type WorkerConfig struct {
	Timeout             time.Duration
	MaxConcurrency      int
	MinFreeDiskBytes    int64
	MaxDiskUsagePercent int
}

// Validate rejects unsafe or nonsensical worker limits before a build starts.
func (c WorkerConfig) Validate() error {
	if c.Timeout <= 0 {
		return fmt.Errorf("build worker timeout must be greater than zero")
	}
	if c.MaxConcurrency <= 0 {
		return fmt.Errorf("build worker concurrency must be greater than zero")
	}
	if c.MinFreeDiskBytes < 0 {
		return fmt.Errorf("build worker minimum free disk bytes must not be negative")
	}
	if c.MaxDiskUsagePercent <= 0 || c.MaxDiskUsagePercent > 100 {
		return fmt.Errorf("build worker max disk usage percent must be between 1 and 100")
	}

	return nil
}

// BuildProfile selects the strictness of build-plan invariant checks.
//
// "default" enforces the panel-locked supply-chain hard rules: untrusted
// FROMs hard-fail, build args must come from the explicit whitelist, and the
// network mode must be "none". Other profiles relax these rules for explicit
// opt-in scenarios such as quarantine builds.
type BuildProfile string

const (
	// BuildProfileDefault is the strict, locked-down build profile used by the
	// default Phase 0 worker. It rejects untrusted FROMs, unknown build args,
	// and any network mode other than "none".
	BuildProfileDefault BuildProfile = "default"
	// BuildProfileQuarantine is used for untrusted-classification builds. It
	// still enforces layer/image-size invariants but permits a relaxed
	// build-arg whitelist controlled by the caller.
	BuildProfileQuarantine BuildProfile = "quarantine"
)

// BuildNetworkMode identifies the network-isolation level requested for the
// BuildKit invocation. Only NetworkModeNone is allowed in the default profile.
type BuildNetworkMode string

const (
	// NetworkModeNone disables network access during the build.
	NetworkModeNone BuildNetworkMode = "none"
	// NetworkModeHost shares the host network namespace; never allowed in the
	// default profile.
	NetworkModeHost BuildNetworkMode = "host"
	// NetworkModeDefault uses BuildKit's default network sandbox.
	NetworkModeDefault BuildNetworkMode = "default"
)

// BuildPlanLimits encodes the additional invariants the plan validator
// enforces in the default profile. Zero values disable the corresponding
// check.
type BuildPlanLimits struct {
	// MaxLayers is the maximum number of resulting image layers permitted by
	// the build. The validator rejects plans that exceed this count.
	MaxLayers int
	// MaxFinalImageSizeBytes is an estimated upper bound on the produced
	// image size; values <= 0 disable the check.
	MaxFinalImageSizeBytes int64
	// AllowedBuildArgs is the explicit whitelist of build-arg keys that may
	// be passed to BuildKit in the default profile. The validator rejects
	// any key outside this set.
	AllowedBuildArgs []string
	// RequiredNetworkMode is the only network mode permitted by the
	// validator. Empty falls back to NetworkModeNone.
	RequiredNetworkMode BuildNetworkMode
}

// DefaultBuildPlanLimits returns the panel-locked Phase 0 default invariants.
// Callers can override them for non-default profiles or for tests that need
// looser limits.
func DefaultBuildPlanLimits() BuildPlanLimits {
	return BuildPlanLimits{
		MaxLayers:              200,
		MaxFinalImageSizeBytes: 5 * 1024 * 1024 * 1024,
		AllowedBuildArgs:       []string{"BUILDKIT_INLINE_CACHE", "TARGETARCH", "TARGETOS", "TARGETPLATFORM"},
		RequiredNetworkMode:    NetworkModeNone,
	}
}

// allowedBuildArgs returns the validator's effective whitelist set.
func (l BuildPlanLimits) allowedBuildArgs() map[string]struct{} {
	out := make(map[string]struct{}, len(l.AllowedBuildArgs))
	for _, key := range l.AllowedBuildArgs {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

// requiredNetworkMode returns the configured required mode falling back to
// NetworkModeNone when unset.
func (l BuildPlanLimits) requiredNetworkMode() BuildNetworkMode {
	if l.RequiredNetworkMode == "" {
		return NetworkModeNone
	}
	return l.RequiredNetworkMode
}

// SortedAllowedBuildArgs returns the whitelist sorted lexically. It is used by
// the planner output to keep CLI evidence deterministic.
func (l BuildPlanLimits) SortedAllowedBuildArgs() []string {
	out := make([]string, 0, len(l.AllowedBuildArgs))
	for key := range l.allowedBuildArgs() {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
