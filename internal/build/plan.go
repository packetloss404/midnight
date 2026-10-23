package build

import (
	"fmt"
	"sort"
	"strings"
)

// SecretFingerprintRecord pairs a secret name with the redacted fingerprint
// computed by SecretFingerprint. The plain secret value is never included.
type SecretFingerprintRecord struct {
	Name        string
	Fingerprint string
}

// UntrustedBuildClassification labels a build with the trust tier used by the
// supply-chain isolation rules.
type UntrustedBuildClassification string

const (
	// UntrustedBuildTrusted marks builds that pass all trusted-source rules.
	UntrustedBuildTrusted UntrustedBuildClassification = "trusted"
	// UntrustedBuildUntrusted marks builds with one elevated risk signal.
	UntrustedBuildUntrusted UntrustedBuildClassification = "untrusted"
	// UntrustedBuildIsolated marks builds with multiple elevated risk
	// signals; quarantine and isolated cache are mandatory.
	UntrustedBuildIsolated UntrustedBuildClassification = "isolated"
)

// UntrustedBuildRules is the set of source-trust signals fed to the
// classifier. Each true bool elevates the resulting classification.
type UntrustedBuildRules struct {
	ExternalContributor bool
	MutableBaseImage    bool
	SecretAccess        bool
}

// ClassifyUntrustedBuild returns the strongest classification implied by the
// supplied set of trust signals.
func ClassifyUntrustedBuild(rules UntrustedBuildRules) UntrustedBuildClassification {
	if rules.ExternalContributor && rules.SecretAccess {
		return UntrustedBuildIsolated
	}
	if rules.ExternalContributor || rules.MutableBaseImage {
		return UntrustedBuildUntrusted
	}
	return UntrustedBuildTrusted
}

// FromTrust labels a single FROM reference as trusted or untrusted from the
// build planner's perspective. Trusted FROMs originate from the operator
// allowlist; untrusted FROMs are immediately rejected by the default profile
// even when pinned by digest.
type FromTrust string

const (
	// FromTrustTrusted marks FROMs that come from the trusted-source list.
	FromTrustTrusted FromTrust = "trusted"
	// FromTrustUntrusted marks FROMs that should be rejected by the default
	// profile.
	FromTrustUntrusted FromTrust = "untrusted"
)

// PlannedFrom enriches a parsed FROM line with planner-side trust metadata.
type PlannedFrom struct {
	From  DockerfileFromDigest
	Trust FromTrust
}

// BuildPlan represents the deterministic, offline plan produced by the build
// planner before any BuildKit invocation.
type BuildPlan struct {
	Profile            BuildProfile
	Limits             BuildPlanLimits
	Froms              []DockerfileFromDigest
	UntrustedFroms     []DockerfileFromDigest
	EstimatedLayers    int
	EstimatedSizeBytes int64
	NetworkMode        BuildNetworkMode
	BuildArgs          map[string]string
	Worker             WorkerConfig
	SecretFingerprints []SecretFingerprintRecord
	SnapshotDraft      DeploymentSnapshot
	UntrustedBuild     UntrustedBuildClassification
}

// BuildOperationState identifies a stage in the build operation state
// machine. Values flow strictly in the order returned by
// DefaultBuildOperationPlan.
type BuildOperationState string

const (
	// BuildOperationQueued is the initial wait-for-worker state.
	BuildOperationQueued BuildOperationState = "queued"
	// BuildOperationBuilding is the active BuildKit solve state.
	BuildOperationBuilding BuildOperationState = "building"
	// BuildOperationSigning is the post-build cosign-signing state.
	BuildOperationSigning BuildOperationState = "signing"
	// BuildOperationPushing is the registry push state.
	BuildOperationPushing BuildOperationState = "pushing"
	// BuildOperationSnapshot is the snapshot-record-write state.
	BuildOperationSnapshot BuildOperationState = "snapshot"
	// BuildOperationDeploy is the hand-off to the deploy worker.
	BuildOperationDeploy BuildOperationState = "deploy"
)

// BuildOperationStep names a single BuildOperationState transition.
type BuildOperationStep struct {
	State BuildOperationState
	Name  string
}

// BuildOperationPlan is the ordered list of build-operation steps the worker
// will emit. It is validated against defaultBuildOperationStates.
type BuildOperationPlan struct {
	Steps []BuildOperationStep
}

// DefaultBuildOperationPlan returns the canonical Phase 0 build-operation
// step order.
func DefaultBuildOperationPlan() BuildOperationPlan {
	return BuildOperationPlan{Steps: []BuildOperationStep{
		{State: BuildOperationQueued, Name: "queued"},
		{State: BuildOperationBuilding, Name: "building"},
		{State: BuildOperationSigning, Name: "signing"},
		{State: BuildOperationPushing, Name: "pushing"},
		{State: BuildOperationSnapshot, Name: "snapshot"},
		{State: BuildOperationDeploy, Name: "deploy"},
	}}
}

// Validate rejects build-operation plans whose step order or shape diverges
// from the canonical order.
func (p BuildOperationPlan) Validate() error {
	if len(p.Steps) != len(defaultBuildOperationStates) {
		return fmt.Errorf("build operation plan has %d steps, want %d", len(p.Steps), len(defaultBuildOperationStates))
	}

	for i, step := range p.Steps {
		if step.State != defaultBuildOperationStates[i] {
			return fmt.Errorf("build operation step %d = %q, want %q", i, step.State, defaultBuildOperationStates[i])
		}
	}

	return nil
}

var defaultBuildOperationStates = []BuildOperationState{
	BuildOperationQueued,
	BuildOperationBuilding,
	BuildOperationSigning,
	BuildOperationPushing,
	BuildOperationSnapshot,
	BuildOperationDeploy,
}

// Validate enforces all Phase 1 build-plan invariants. The default profile
// applies the panel-locked supply-chain hard rules; alternative profiles may
// relax these via the BuildPlanLimits struct.
func (p BuildPlan) Validate() error {
	if err := p.Worker.Validate(); err != nil {
		return err
	}
	for _, from := range p.Froms {
		if !strings.HasPrefix(from.Digest, "sha256:") {
			return fmt.Errorf("dockerfile FROM line %d must be pinned with sha256 digest", from.Line)
		}
	}
	for _, secret := range p.SecretFingerprints {
		if secret.Name == "" {
			return fmt.Errorf("secret fingerprint name must not be empty")
		}
		if !strings.HasPrefix(secret.Fingerprint, "sha256:") {
			return fmt.Errorf("secret fingerprint %q must be a sha256 digest", secret.Name)
		}
	}
	if err := p.SnapshotDraft.ValidateDraft(); err != nil {
		return err
	}
	switch p.UntrustedBuild {
	case "", UntrustedBuildTrusted, UntrustedBuildUntrusted, UntrustedBuildIsolated:
	default:
		return fmt.Errorf("untrusted build classification %q is not supported", p.UntrustedBuild)
	}

	profile := p.Profile
	if profile == "" {
		profile = BuildProfileDefault
	}
	limits := p.Limits
	if limits.MaxLayers == 0 && limits.MaxFinalImageSizeBytes == 0 && len(limits.AllowedBuildArgs) == 0 && limits.RequiredNetworkMode == "" {
		limits = DefaultBuildPlanLimits()
	}
	if profile == BuildProfileDefault {
		if err := validateDefaultProfile(p, limits); err != nil {
			return err
		}
	}
	if limits.MaxLayers > 0 && p.EstimatedLayers > limits.MaxLayers {
		return fmt.Errorf("build plan estimated layers %d exceed maximum %d", p.EstimatedLayers, limits.MaxLayers)
	}
	if limits.MaxFinalImageSizeBytes > 0 && p.EstimatedSizeBytes > limits.MaxFinalImageSizeBytes {
		return fmt.Errorf("build plan estimated image size %d bytes exceeds maximum %d bytes", p.EstimatedSizeBytes, limits.MaxFinalImageSizeBytes)
	}
	if p.EstimatedLayers < 0 {
		return fmt.Errorf("build plan estimated layers must not be negative")
	}
	if p.EstimatedSizeBytes < 0 {
		return fmt.Errorf("build plan estimated image size must not be negative")
	}
	return nil
}

func validateDefaultProfile(plan BuildPlan, limits BuildPlanLimits) error {
	if len(plan.UntrustedFroms) > 0 {
		var lines []string
		for _, from := range plan.UntrustedFroms {
			lines = append(lines, fmt.Sprintf("line %d %q", from.Line, from.Image))
		}
		return fmt.Errorf("default profile rejects untrusted FROM(s): %s", strings.Join(lines, ", "))
	}
	required := limits.requiredNetworkMode()
	mode := plan.NetworkMode
	if mode == "" {
		mode = required
	}
	if mode != required {
		return fmt.Errorf("default profile requires network mode %q, got %q", required, mode)
	}
	allowed := limits.allowedBuildArgs()
	keys := make([]string, 0, len(plan.BuildArgs))
	for key := range plan.BuildArgs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("default profile rejects build arg %q (not in whitelist)", key)
		}
	}
	return nil
}
