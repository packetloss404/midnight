package docker

import (
	"fmt"
	"regexp"
	"strings"
)

// Container/network labels and name format are pinned by Sprint 4 (S4-04 +
// S4-07). Adapters MUST keep these in sync with docs/runtime/labels.md and
// the conformance suite's required label set.
const (
	// LabelManagedBy marks every Midnight-managed container so reconcile
	// can filter foreign workloads. The canonical value is
	// `midnight-agent`.
	LabelManagedBy  = "midnight.managed_by"
	LabelManagedByV = "midnight-agent"

	LabelProject     = "midnight.project_id"
	LabelEnvironment = "midnight.environment_id"
	LabelService     = "midnight.service_id"
	LabelDeployment  = "midnight.deployment_id"
	LabelInstance    = "midnight.instance_id"
	LabelAgentID     = "midnight.agent_id"
	LabelImageDigest = "midnight.image_digest"
	LabelHostID      = "midnight.host_id"
	LabelIdempotency = "midnight.idempotency_key"
	// LabelWorkloadKind separates long-running services from terminal
	// one-off resources so adapter boot reconciliation never mistakes a
	// retained predeploy container for the application instance.
	LabelWorkloadKind         = "midnight.workload_kind"
	WorkloadKindService       = "service"
	WorkloadKindPredeploy     = "predeploy"
	WorkloadKindCron          = "cron"
	WorkloadKindTemplateSmoke = "template-smoke"
	LabelPredeploySpec        = "midnight.predeploy_spec_sha256"
	LabelCronSpec             = "midnight.cron_spec_sha256"
	LabelTemplateSmokeSpec    = "midnight.template_smoke_spec_sha256"

	// ContainerNamePrefix is the deterministic prefix Sprint 4 (S4-04)
	// pinned: `or-{svc_id_short}-{dep_seq}-{inst_seq}`. The prefix is "or"
	// (Open-Rails legacy) followed by `-`.
	ContainerNamePrefix = "or"
	// NetworkNamePrefix is the per-(project, environment) bridge name
	// prefix from Sprint 4 (S4-07).
	NetworkNamePrefix = "or-net"

	// MaxContainerNameLength matches Docker's RFC 1123 cap (63).
	MaxContainerNameLength = 63
)

var (
	nameSanitizer = regexp.MustCompile(`[^a-z0-9]+`)
)

// ContainerName returns the deterministic Docker container name for the
// given service, deployment seq, and instance seq. The format is locked by
// Sprint 4 (S4-04): `or-{svc_id_short}-{dep_seq}-{inst_seq}`. The result is
// always RFC 1123 valid and ≤63 characters.
//
// `svcID` may be a full ULID-style id (`svc_01HAA...`) or a pre-shortened
// token; either way the function strips the prefix and shortens.
func ContainerName(svcID string, depSeq, instSeq int64) string {
	short := shortServiceID(svcID)
	name := fmt.Sprintf("%s-%s-%d-%d", ContainerNamePrefix, short, depSeq, instSeq)
	if len(name) <= MaxContainerNameLength {
		return name
	}
	// Trim the service token rather than the deployment sequence to
	// preserve uniqueness.
	excess := len(name) - MaxContainerNameLength
	if excess >= len(short) {
		// Pathological: clamp short to a stable hash-prefix.
		short = short[:1]
	} else {
		short = short[:len(short)-excess]
	}
	return fmt.Sprintf("%s-%s-%d-%d", ContainerNamePrefix, short, depSeq, instSeq)
}

// NetworkName returns the deterministic Docker bridge name for the given
// (project, environment). Format `or-net-{prj}-{env}` per Sprint 4 (S4-07).
func NetworkName(projectID, envID string) string {
	prj := shortToken(projectID)
	env := shortToken(envID)
	name := fmt.Sprintf("%s-%s-%s", NetworkNamePrefix, prj, env)
	if len(name) > MaxContainerNameLength {
		// Truncate from the env side; project_id is more semantically
		// meaningful for operator debugging.
		excess := len(name) - MaxContainerNameLength
		if excess < len(env) {
			env = env[:len(env)-excess]
		} else {
			env = env[:1]
		}
		name = fmt.Sprintf("%s-%s-%s", NetworkNamePrefix, prj, env)
	}
	return name
}

// LabelSet builds the canonical Midnight label set for a managed container.
// The set is the contract every adapter implementation must apply (Sprint 4
// S4-04 + ADR-015 conformance check `label_hygiene`).
type LabelSet struct {
	ProjectID      string
	EnvironmentID  string
	ServiceID      string
	DeploymentID   string
	InstanceID     string
	AgentID        string
	HostID         string
	ImageDigest    string
	IdempotencyKey string
	Extra          map[string]string
}

// AsMap returns the deterministic label map. Extra labels are merged after
// the canonical set so they cannot shadow the required keys.
func (l LabelSet) AsMap() map[string]string {
	out := map[string]string{
		LabelManagedBy:   LabelManagedByV,
		LabelProject:     l.ProjectID,
		LabelEnvironment: l.EnvironmentID,
		LabelService:     l.ServiceID,
		LabelDeployment:  l.DeploymentID,
		LabelInstance:    l.InstanceID,
	}
	if l.AgentID != "" {
		out[LabelAgentID] = l.AgentID
	}
	if l.HostID != "" {
		out[LabelHostID] = l.HostID
	}
	if l.ImageDigest != "" {
		out[LabelImageDigest] = l.ImageDigest
	}
	if l.IdempotencyKey != "" {
		out[LabelIdempotency] = l.IdempotencyKey
	}
	for k, v := range l.Extra {
		if _, reserved := out[k]; reserved {
			continue
		}
		out[k] = v
	}
	return out
}

// RequiredLabelKeys lists the labels every Midnight-managed container must
// carry. The conformance suite uses this list directly.
func RequiredLabelKeys() []string {
	return []string{
		LabelManagedBy,
		LabelProject,
		LabelEnvironment,
		LabelService,
		LabelDeployment,
		LabelInstance,
	}
}

// shortServiceID strips a known prefix and returns up to 8 lowercase chars.
// Service ids land at `svc_<26-char ULID>` (panel decision); the short token
// is the first 8 chars of the ULID. For ad-hoc tests we accept any string
// and sanitize it.
func shortServiceID(svcID string) string {
	id := strings.TrimSpace(strings.ToLower(svcID))
	if idx := strings.Index(id, "_"); idx >= 0 && idx < len(id)-1 {
		id = id[idx+1:]
	}
	id = nameSanitizer.ReplaceAllString(id, "")
	if id == "" {
		id = "svc"
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

func shortToken(id string) string {
	v := strings.TrimSpace(strings.ToLower(id))
	if idx := strings.Index(v, "_"); idx >= 0 && idx < len(v)-1 {
		v = v[idx+1:]
	}
	v = nameSanitizer.ReplaceAllString(v, "")
	if v == "" {
		v = "x"
	}
	if len(v) > 12 {
		v = v[:12]
	}
	return v
}

// SequenceFromInstanceID extracts a stable numeric `inst_seq` token from a
// Midnight instance id. The instance id schema is opaque outside this
// package; we simply hash the id deterministically into a small integer so
// the container name remains stable across reconciles.
func SequenceFromInstanceID(instanceID string) int64 {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		return 0
	}
	var hash uint64 = 1469598103934665603 // FNV-1a 64-bit offset basis
	for _, b := range []byte(id) {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return int64(hash & 0x7fffffff)
}
