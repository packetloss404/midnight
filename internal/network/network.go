// Package network provides per-(project, environment) private networking
// primitives for the single-host scheduler.
//
// The runtime wires services on the same project + environment into a
// dedicated user-defined Docker bridge so they can address each other by
// service slug over Docker's embedded DNS. This package owns:
//
//   - The deterministic Docker network name derived from
//     (project_id, environment_id).
//   - The {slug}.midnight.internal alias every container advertises.
//   - The Provisioner boundary the runtime adapter calls before container
//     create. The DockerProvisioner implementation is in
//     docker_provisioner.go.
//
// The package is intentionally small and runtime-agnostic; the multi-host
// WireGuard mesh (future work; see ADR-0020) will provide a different
// Provisioner implementation behind the same interface so callers do not
// have to special-case the topology.
package network

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// PrivateDomainSuffix is the DNS suffix used for service-to-service private
// addressing. Mirrors Railway's `*.railway.internal` shape so the developer
// experience is portable.
const PrivateDomainSuffix = ".midnight.internal"

// MaxNetworkNameLength matches Docker's RFC 1123 cap for network names. The
// embedded DNS resolver rejects names beyond this length.
const MaxNetworkNameLength = 63

// NetworkNamePrefix is the deterministic prefix every per-project network
// uses. Aligns with the existing or-net-* style in
// internal/runtime/docker/names.go but lives in its own namespace because
// the project/environment full ids encode here, not the short tokens.
const NetworkNamePrefix = "midnight-proj"

// LabelProjectID is the Docker network label recording the owning project.
// The control-plane reconciler filters networks by these labels so the boot
// pass can re-discover the per-(project, environment) bridge without
// re-creating it.
const (
	LabelProjectID     = "midnight.project_id"
	LabelEnvironmentID = "midnight.environment_id"
	LabelManagedBy     = "midnight.managed_by"
	LabelManagedByV    = "midnight-network-provisioner"
)

// FeatureFlagEnv is retained as a compatibility constant for older
// configuration files. The Docker runtime no longer honors an opt-out:
// workload scheduling fails closed when a private network cannot converge.
const FeatureFlagEnv = "MIDNIGHT_PRIVATE_NETWORK"

// NetworkID is the Docker engine's network id returned by NetworkCreate.
// The value is opaque outside this package; callers attach it to the
// container's NetworkingConfig.
type NetworkID string

// ProjectNetwork is the value object describing one per-(project,
// environment) Docker network. It is deterministic in (project_id,
// environment_id) so the same pair always produces the same Name() across
// process restarts.
type ProjectNetwork struct {
	ProjectID     string
	EnvironmentID string
}

// Name returns the deterministic Docker network name for the
// (project_id, environment_id) pair. The format is:
//
//	midnight-proj-{project_id_safe}-env-{environment_id_safe}
//
// The result is RFC 1123-safe (lowercase alphanumerics + hyphen) and at
// most MaxNetworkNameLength bytes. When the full form exceeds the cap the
// environment token is trimmed before the project token because the
// project_id is more semantically meaningful for operator debugging.
func (p ProjectNetwork) Name() string {
	prj := sanitizeNameToken(p.ProjectID)
	env := sanitizeNameToken(p.EnvironmentID)
	name := fmt.Sprintf("%s-%s-env-%s", NetworkNamePrefix, prj, env)
	if len(name) <= MaxNetworkNameLength {
		return name
	}
	// Trim env first.
	excess := len(name) - MaxNetworkNameLength
	if excess < len(env) {
		env = env[:len(env)-excess]
	} else {
		env = "x"
	}
	name = fmt.Sprintf("%s-%s-env-%s", NetworkNamePrefix, prj, env)
	if len(name) <= MaxNetworkNameLength {
		return name
	}
	// Pathological case: trim project too.
	excess = len(name) - MaxNetworkNameLength
	if excess < len(prj) {
		prj = prj[:len(prj)-excess]
	} else {
		prj = "x"
	}
	return fmt.Sprintf("%s-%s-env-%s", NetworkNamePrefix, prj, env)
}

// Labels returns the canonical label map every Midnight-managed network
// carries. The reconciler reads these labels back via NetworkList(filter)
// to rebuild state after a process restart.
func (p ProjectNetwork) Labels() map[string]string {
	return map[string]string{
		LabelManagedBy:     LabelManagedByV,
		LabelProjectID:     p.ProjectID,
		LabelEnvironmentID: p.EnvironmentID,
	}
}

// ServiceAlias returns the deterministic {slug}.midnight.internal hostname
// the runtime advertises for one service. Returns empty string when slug
// is blank; callers MUST guard against this when injecting env vars.
func ServiceAlias(slug string) string {
	slug = strings.TrimSpace(strings.ToLower(slug))
	if slug == "" {
		return ""
	}
	return slug + PrivateDomainSuffix
}

// ServiceAliases returns the two DNS aliases the runtime registers for one
// service on the project network: the bare slug and the
// {slug}.midnight.internal FQDN. Docker's embedded DNS honors both so
// callers can address services by either form.
func ServiceAliases(slug string) []string {
	slug = strings.TrimSpace(strings.ToLower(slug))
	if slug == "" {
		return nil
	}
	return []string{slug, slug + PrivateDomainSuffix}
}

// Provisioner is the runtime-facing boundary for per-(project, environment)
// networks. Implementations MUST be idempotent on EnsureNetwork: a second
// call with the same pair returns the same NetworkID without creating a
// duplicate.
//
// EnsureNetwork is called before every container schedule. RemoveNetwork
// is a teardown hook surfaced for project/environment deletion; today
// callers never invoke it (networks are low-cardinality and cheap to keep
// around) — the hook exists so the future cleanup pass has somewhere to
// land.
type Provisioner interface {
	EnsureNetwork(ctx context.Context, projectID, environmentID string) (NetworkID, error)
	RemoveNetwork(ctx context.Context, projectID, environmentID string) error
	// ListManagedNetworks returns the (project_id, environment_id) pairs
	// the Docker daemon currently has Midnight-managed networks for. The
	// worker boot pass uses this to reconcile state without round-tripping
	// the Postgres deployment table.
	ListManagedNetworks(ctx context.Context) ([]ProjectNetwork, error)
}

// ErrProvisionerUnavailable is returned by EnsureNetwork when the
// underlying engine is offline or otherwise refuses to converge the
// network. Callers MUST treat this as transient.
var ErrProvisionerUnavailable = errors.New("network provisioner: engine unavailable")

// ErrInvalidScope is returned when project_id or environment_id is empty.
// The runtime adapter validates these before reaching this package, but
// the boundary defends in depth.
var ErrInvalidScope = errors.New("network provisioner: project_id and environment_id are required")

// validateScope returns ErrInvalidScope for empty inputs.
func validateScope(projectID, environmentID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(environmentID) == "" {
		return ErrInvalidScope
	}
	return nil
}

var nameSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeNameToken returns a deterministic, RFC 1123-safe representation
// of an id for use inside a Docker network name. It lowercases, strips any
// `kind_` prefix common to Midnight ULIDs (e.g., `prj_01HAA...`), and
// drops any non-alphanumeric character.
func sanitizeNameToken(id string) string {
	v := strings.TrimSpace(strings.ToLower(id))
	if idx := strings.Index(v, "_"); idx >= 0 && idx < len(v)-1 {
		v = v[idx+1:]
	}
	v = nameSanitizer.ReplaceAllString(v, "")
	if v == "" {
		v = "x"
	}
	if len(v) > 24 {
		v = v[:24]
	}
	return v
}
