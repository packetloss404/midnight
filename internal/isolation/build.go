package isolation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/midnight/midnight/internal/tenancy"
)

var safeBuildID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,127}$`)

type RiskTier string

const (
	RiskTrusted   RiskTier = "trusted"
	RiskUntrusted RiskTier = "untrusted"
	RiskIsolated  RiskTier = "isolated"
)

type Backend string

const (
	BackendContainer Backend = "container"
	BackendGVisor    Backend = "gvisor"
	BackendKata      Backend = "kata"
	BackendMicroVM   Backend = "microvm"
)

type CacheMode string

const (
	CachePrivate CacheMode = "private"
)

type BuildRequest struct {
	Tenant           tenancy.Scope `json:"tenant"`
	BuildID          string        `json:"build_id"`
	Risk             RiskTier      `json:"risk"`
	RequestedBackend Backend       `json:"requested_backend,omitempty"`
}

type BuildPlan struct {
	Tenant            tenancy.Scope     `json:"tenant"`
	BuildID           string            `json:"build_id"`
	Risk              RiskTier          `json:"risk"`
	Backend           Backend           `json:"backend"`
	CacheMode         CacheMode         `json:"cache_mode"`
	CacheNamespace    string            `json:"cache_namespace"`
	NetworkEgress     bool              `json:"network_egress"`
	HostSocketAccess  bool              `json:"host_socket_access"`
	Privileged        bool              `json:"privileged"`
	PlanOnly          bool              `json:"plan_only"`
	RequiredLabels    map[string]string `json:"required_labels"`
	IsolationWarnings []string          `json:"isolation_warnings,omitempty"`
}

// PlanBuild returns the fail-closed isolation intent for a future build worker.
// It deliberately stays side-effect free: no VM, microVM, gVisor, Kata, or
// BuildKit process is started here.
func PlanBuild(req BuildRequest) (BuildPlan, error) {
	if err := req.Tenant.Validate(); err != nil {
		return BuildPlan{}, err
	}
	buildID := strings.TrimSpace(req.BuildID)
	if err := validateBuildID(buildID, req.BuildID); err != nil {
		return BuildPlan{}, err
	}
	risk := req.Risk
	if risk == "" {
		risk = RiskTrusted
	}
	if !validRisk(risk) {
		return BuildPlan{}, fmt.Errorf("risk tier %q is not supported", risk)
	}
	backend := req.RequestedBackend
	if backend == "" {
		backend = defaultBackend(risk)
	}
	if !validBackend(backend) {
		return BuildPlan{}, fmt.Errorf("isolation backend %q is not supported", backend)
	}
	if risk == RiskIsolated && backend == BackendContainer {
		return BuildPlan{}, fmt.Errorf("isolated builds require gvisor, kata, or microvm backend")
	}

	labels := req.Tenant.Labels()
	labels["midnight.build"] = buildID
	labels["midnight.isolation.backend"] = string(backend)

	plan := BuildPlan{
		Tenant:           req.Tenant,
		BuildID:          buildID,
		Risk:             risk,
		Backend:          backend,
		CacheMode:        CachePrivate,
		CacheNamespace:   cacheNamespace(req.Tenant, buildID),
		NetworkEgress:    risk == RiskTrusted,
		HostSocketAccess: false,
		Privileged:       false,
		PlanOnly:         true,
		RequiredLabels:   labels,
	}
	if backend == BackendKata || backend == BackendMicroVM {
		plan.IsolationWarnings = append(plan.IsolationWarnings, "backend planned only; no VM runtime launcher is wired")
	}
	return plan, nil
}

func (p BuildPlan) Validate() error {
	if err := p.Tenant.Validate(); err != nil {
		return err
	}
	if p.BuildID == "" {
		return fmt.Errorf("build_id is required")
	}
	if err := validateBuildID(p.BuildID, p.BuildID); err != nil {
		return err
	}
	if !validRisk(p.Risk) {
		return fmt.Errorf("risk tier %q is not supported", p.Risk)
	}
	if !validBackend(p.Backend) {
		return fmt.Errorf("isolation backend %q is not supported", p.Backend)
	}
	if p.CacheMode != CachePrivate {
		return fmt.Errorf("build cache mode must be private")
	}
	if p.CacheNamespace != cacheNamespace(p.Tenant, p.BuildID) {
		return fmt.Errorf("build cache namespace does not match tenant scope")
	}
	if p.HostSocketAccess || p.Privileged {
		return fmt.Errorf("build isolation plan must not request host socket access or privileged execution")
	}
	if p.Risk != RiskTrusted && p.NetworkEgress {
		return fmt.Errorf("untrusted build isolation plan must disable network egress")
	}
	return p.Tenant.ValidateLabels(p.RequiredLabels)
}

func defaultBackend(risk RiskTier) Backend {
	switch risk {
	case RiskIsolated:
		return BackendMicroVM
	case RiskUntrusted:
		return BackendGVisor
	default:
		return BackendContainer
	}
}

func validRisk(risk RiskTier) bool {
	switch risk {
	case RiskTrusted, RiskUntrusted, RiskIsolated:
		return true
	default:
		return false
	}
}

func validBackend(backend Backend) bool {
	switch backend {
	case BackendContainer, BackendGVisor, BackendKata, BackendMicroVM:
		return true
	default:
		return false
	}
}

func cacheNamespace(scope tenancy.Scope, buildID string) string {
	return "workspace:" + scope.WorkspaceID + "|project:" + scope.ProjectID + "|environment:" + scope.EnvironmentID + "|build:" + buildID
}

func validateBuildID(trimmed, original string) error {
	if trimmed == "" || trimmed != original {
		return fmt.Errorf("build_id is required and must not contain surrounding whitespace")
	}
	if !safeBuildID.MatchString(trimmed) {
		return fmt.Errorf("build_id %q contains unsupported characters", trimmed)
	}
	return nil
}
