package doctor

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/install"
	"github.com/midnight/midnight/internal/preflight"
)

// OperatorReadinessSchemaVersion is the stable JSON schema for local operator readiness.
const OperatorReadinessSchemaVersion = "midnight.dev/operator-readiness/v1alpha1"

// ReadinessStatus is the combined installer and doctor readiness state.
type ReadinessStatus string

const (
	ReadinessHealthy  ReadinessStatus = "healthy"
	ReadinessDegraded ReadinessStatus = "degraded"
	ReadinessBlocked  ReadinessStatus = "blocked"
)

// LocalReadinessStatus is the release/operator readiness state for one local gate.
type LocalReadinessStatus string

const (
	LocalReadinessReady       LocalReadinessStatus = "ready"
	LocalReadinessNeedsReview LocalReadinessStatus = "needs-review"
	LocalReadinessBlocked     LocalReadinessStatus = "blocked"
)

const (
	OperatorDomainInstallGate    = "install_gate"
	OperatorDomainDoctorSnapshot = "doctor_snapshot"
	OperatorDomainNotifications  = "notifications"
	OperatorDomainMarketplace    = "marketplace"
	OperatorDomainNetworking     = "networking"
	OperatorDomainRuntimeAdapter = "runtime_adapter"
	OperatorDomainRegistryCache  = "registry_cache"
	OperatorDomainVolumeAttach   = "volume_attach"
)

const (
	doctorActionProceed        = "proceed"
	doctorActionFixBlockers    = "fix-blockers"
	doctorActionReviewWarnings = "review-warnings"
	doctorActionInspectDoctor  = "inspect-doctor-results"
	doctorActionApplySafeFixes = "apply-safe-fixes"
	doctorActionPlanRestart    = "plan-restart"
)

// SourceCounts captures source-local status counts in a JSON-friendly shape.
type SourceCounts struct {
	Total        int            `json:"total"`
	StatusCounts map[string]int `json:"status_counts"`
}

// ReadinessSummary combines pure preflight, install, and doctor result data.
type ReadinessSummary struct {
	Status          ReadinessStatus         `json:"status"`
	CountsBySource  map[string]SourceCounts `json:"counts_by_source"`
	RestartRequired bool                    `json:"restart_required"`
	SafeFixCount    int                     `json:"safe_fix_count"`
	NextActions     []string                `json:"next_actions"`
}

// OperatorReadinessDomain identifies one required local product-readiness area.
type OperatorReadinessDomain struct {
	Phase  int    `json:"phase"`
	Domain string `json:"domain"`
}

// OperatorReadinessGate is caller-supplied local evidence from an existing planner or gate.
type OperatorReadinessGate struct {
	Phase      int                  `json:"phase"`
	Domain     string               `json:"domain"`
	Source     string               `json:"source"`
	Status     LocalReadinessStatus `json:"status"`
	Reason     string               `json:"reason,omitempty"`
	References []string             `json:"references,omitempty"`
}

// OperatorDomainReadiness rolls up all local gate evidence for one domain.
type OperatorDomainReadiness struct {
	Domain string                  `json:"domain"`
	Status LocalReadinessStatus    `json:"status"`
	Gates  []OperatorReadinessGate `json:"gates"`
}

// OperatorPhaseReadiness rolls up local readiness by product phase.
type OperatorPhaseReadiness struct {
	Phase   int                       `json:"phase"`
	Status  LocalReadinessStatus      `json:"status"`
	Domains []OperatorDomainReadiness `json:"domains"`
}

// OperatorReadiness is a pure local Phase 0-3 readiness model.
type OperatorReadiness struct {
	SchemaVersion  string                    `json:"schema_version"`
	Status         LocalReadinessStatus      `json:"status"`
	Phases         []OperatorPhaseReadiness  `json:"phases"`
	MissingDomains []OperatorReadinessDomain `json:"missing_domains,omitempty"`
}

// NewReadinessSummary derives a pure readiness summary from already-collected data.
func NewReadinessSummary(preflightSummary preflight.AssessmentSummary, installSummary install.ReadinessSummary, doctorResults []Result) ReadinessSummary {
	doctorCounts := countDoctorResults(doctorResults)
	summary := ReadinessSummary{
		Status: ReadinessHealthy,
		CountsBySource: map[string]SourceCounts{
			"preflight": sourceCountsFromPreflight(preflightSummary.StatusCounts),
			"install":   sourceCountsFromInstall(installSummary.Readiness),
			"doctor":    doctorCounts,
		},
		RestartRequired: installSummary.RestartRequiredCount > 0,
		SafeFixCount:    preflightSummary.SafeFixAvailableCount + installSummary.SafeFixCount,
	}

	doctorBlocked, doctorDegraded := classifyDoctorResults(doctorResults)
	preflightWarns := preflightSummary.StatusCounts[preflight.StatusWarn] > 0
	blocked := preflightSummary.HasBlockers || installSummary.Readiness == install.ReadinessBlocked || doctorBlocked
	degraded := preflightWarns || installSummary.Readiness == install.ReadinessReadyWithWarnings || doctorDegraded

	if blocked {
		summary.Status = ReadinessBlocked
	} else if degraded {
		summary.Status = ReadinessDegraded
	}
	summary.NextActions = readinessNextActions(summary, preflightSummary, installSummary, doctorBlocked, doctorDegraded)

	return summary
}

// MarshalReadinessSummaryJSON emits the stable combined readiness summary JSON representation.
func MarshalReadinessSummaryJSON(summary ReadinessSummary) ([]byte, error) {
	return json.Marshal(summary)
}

// RequiredOperatorReadinessDomains returns the Phase 0-3 local readiness coverage set.
func RequiredOperatorReadinessDomains() []OperatorReadinessDomain {
	return []OperatorReadinessDomain{
		{Phase: 0, Domain: OperatorDomainInstallGate},
		{Phase: 1, Domain: OperatorDomainDoctorSnapshot},
		{Phase: 2, Domain: OperatorDomainNotifications},
		{Phase: 2, Domain: OperatorDomainMarketplace},
		{Phase: 3, Domain: OperatorDomainNetworking},
		{Phase: 3, Domain: OperatorDomainRuntimeAdapter},
		{Phase: 3, Domain: OperatorDomainRegistryCache},
		{Phase: 3, Domain: OperatorDomainVolumeAttach},
	}
}

// NewOperatorReadiness derives Phase 0-3 operator readiness from local evidence only.
func NewOperatorReadiness(summary ReadinessSummary, gates []OperatorReadinessGate) OperatorReadiness {
	allGates := append([]OperatorReadinessGate{
		readinessSummaryGate(0, OperatorDomainInstallGate, "doctor.readiness_summary", summary),
		readinessSummaryGate(1, OperatorDomainDoctorSnapshot, "doctor.snapshot_summary", summary),
	}, normalizeOperatorReadinessGates(gates)...)

	covered := make(map[OperatorReadinessDomain]struct{}, len(allGates))
	for _, gate := range allGates {
		covered[OperatorReadinessDomain{Phase: gate.Phase, Domain: gate.Domain}] = struct{}{}
	}
	missing := make([]OperatorReadinessDomain, 0)
	for _, required := range RequiredOperatorReadinessDomains() {
		if _, ok := covered[required]; ok {
			continue
		}
		missing = append(missing, required)
		allGates = append(allGates, OperatorReadinessGate{
			Phase:  required.Phase,
			Domain: required.Domain,
			Source: "operator_readiness.coverage",
			Status: LocalReadinessNeedsReview,
			Reason: "no local readiness evidence supplied",
		})
	}
	sortOperatorReadinessDomains(missing)

	phases := operatorPhaseReadiness(allGates)
	readiness := OperatorReadiness{SchemaVersion: OperatorReadinessSchemaVersion, Status: LocalReadinessReady, Phases: phases, MissingDomains: missing}
	for _, phase := range phases {
		readiness.Status = aggregateLocalReadinessStatus(readiness.Status, phase.Status)
	}
	return readiness
}

// MarshalOperatorReadinessJSON emits the stable local operator readiness JSON representation.
func MarshalOperatorReadinessJSON(readiness OperatorReadiness) ([]byte, error) {
	if readiness.SchemaVersion == "" {
		readiness.SchemaVersion = OperatorReadinessSchemaVersion
	}
	return json.Marshal(readiness)
}

func sourceCountsFromPreflight(statusCounts map[preflight.Status]int) SourceCounts {
	counts := make(map[string]int, len(statusCounts))
	var total int
	for status, count := range statusCounts {
		counts[string(status)] = count
		total += count
	}
	return SourceCounts{Total: total, StatusCounts: counts}
}

func sourceCountsFromInstall(readiness install.Readiness) SourceCounts {
	counts := map[string]int{}
	if readiness != "" {
		counts[string(readiness)] = 1
	}
	return SourceCounts{Total: len(counts), StatusCounts: counts}
}

func countDoctorResults(results []Result) SourceCounts {
	counts := make(map[string]int)
	for _, result := range results {
		counts[string(result.Status)]++
	}
	return SourceCounts{Total: len(results), StatusCounts: counts}
}

func classifyDoctorResults(results []Result) (blocked bool, degraded bool) {
	for _, result := range results {
		switch result.Status {
		case StatusConfigError:
			blocked = true
		case StatusFail:
			if result.Severity == "fatal" {
				blocked = true
			} else {
				degraded = true
			}
		case StatusWarn, StatusSkip:
			degraded = true
		}
	}
	return blocked, degraded
}

func readinessNextActions(summary ReadinessSummary, preflightSummary preflight.AssessmentSummary, installSummary install.ReadinessSummary, doctorBlocked bool, doctorDegraded bool) []string {
	actions := make([]string, 0, 5)
	if preflightSummary.HasBlockers || installSummary.BlockerCount > 0 {
		actions = append(actions, doctorActionFixBlockers)
	}
	if preflightSummary.StatusCounts[preflight.StatusWarn] > 0 || installSummary.WarningCount > 0 {
		actions = append(actions, doctorActionReviewWarnings)
	}
	if doctorBlocked || doctorDegraded {
		actions = append(actions, doctorActionInspectDoctor)
	}
	if summary.SafeFixCount > 0 {
		actions = append(actions, doctorActionApplySafeFixes)
	}
	if summary.RestartRequired {
		actions = append(actions, doctorActionPlanRestart)
	}
	if len(actions) == 0 {
		return []string{doctorActionProceed}
	}
	return actions
}

func readinessSummaryGate(phase int, domain, source string, summary ReadinessSummary) OperatorReadinessGate {
	status := LocalReadinessReady
	switch summary.Status {
	case ReadinessBlocked:
		status = LocalReadinessBlocked
	case ReadinessDegraded:
		status = LocalReadinessNeedsReview
	}
	return OperatorReadinessGate{Phase: phase, Domain: domain, Source: source, Status: status, Reason: "derived from combined doctor readiness summary"}
}

func normalizeOperatorReadinessGates(gates []OperatorReadinessGate) []OperatorReadinessGate {
	normalized := make([]OperatorReadinessGate, 0, len(gates))
	for _, gate := range gates {
		gate.Domain = strings.TrimSpace(gate.Domain)
		gate.Source = strings.TrimSpace(gate.Source)
		gate.Reason = strings.TrimSpace(gate.Reason)
		gate.References = normalizeReadinessReferences(gate.References)
		if gate.Domain == "" {
			gate.Domain = "unknown"
		}
		if gate.Source == "" {
			gate.Source = "local_gate"
		}
		if gate.Status == "" {
			gate.Status = LocalReadinessNeedsReview
			gate.Reason = strings.TrimSpace(gate.Reason + " missing local gate status")
		} else if !validLocalReadinessStatus(gate.Status) {
			gate.Reason = strings.TrimSpace(gate.Reason + " unsupported local gate status")
			gate.Status = LocalReadinessNeedsReview
		}
		normalized = append(normalized, gate)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return operatorGateKey(normalized[i]) < operatorGateKey(normalized[j]) })
	return normalized
}

func normalizeReadinessReferences(references []string) []string {
	out := make([]string, 0, len(references))
	seen := map[string]struct{}{}
	for _, reference := range references {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			continue
		}
		if _, ok := seen[reference]; ok {
			continue
		}
		seen[reference] = struct{}{}
		out = append(out, reference)
	}
	sort.Strings(out)
	return out
}

func operatorPhaseReadiness(gates []OperatorReadinessGate) []OperatorPhaseReadiness {
	byPhaseDomain := map[int]map[string][]OperatorReadinessGate{}
	for _, gate := range gates {
		if _, ok := byPhaseDomain[gate.Phase]; !ok {
			byPhaseDomain[gate.Phase] = map[string][]OperatorReadinessGate{}
		}
		byPhaseDomain[gate.Phase][gate.Domain] = append(byPhaseDomain[gate.Phase][gate.Domain], gate)
	}

	phases := make([]OperatorPhaseReadiness, 0, len(byPhaseDomain))
	phaseIDs := make([]int, 0, len(byPhaseDomain))
	for phase := range byPhaseDomain {
		phaseIDs = append(phaseIDs, phase)
	}
	sort.Ints(phaseIDs)
	for _, phaseID := range phaseIDs {
		domains := make([]OperatorDomainReadiness, 0, len(byPhaseDomain[phaseID]))
		for domain, domainGates := range byPhaseDomain[phaseID] {
			sort.SliceStable(domainGates, func(i, j int) bool { return operatorGateKey(domainGates[i]) < operatorGateKey(domainGates[j]) })
			domainReadiness := OperatorDomainReadiness{Domain: domain, Status: LocalReadinessReady, Gates: domainGates}
			for _, gate := range domainGates {
				domainReadiness.Status = aggregateLocalReadinessStatus(domainReadiness.Status, gate.Status)
			}
			domains = append(domains, domainReadiness)
		}
		sort.SliceStable(domains, func(i, j int) bool { return domains[i].Domain < domains[j].Domain })
		phase := OperatorPhaseReadiness{Phase: phaseID, Status: LocalReadinessReady, Domains: domains}
		for _, domain := range domains {
			phase.Status = aggregateLocalReadinessStatus(phase.Status, domain.Status)
		}
		phases = append(phases, phase)
	}
	return phases
}

func aggregateLocalReadinessStatus(current, next LocalReadinessStatus) LocalReadinessStatus {
	if current == LocalReadinessBlocked || next == LocalReadinessBlocked {
		return LocalReadinessBlocked
	}
	if current == LocalReadinessNeedsReview || next == LocalReadinessNeedsReview {
		return LocalReadinessNeedsReview
	}
	return LocalReadinessReady
}

func validLocalReadinessStatus(status LocalReadinessStatus) bool {
	switch status {
	case LocalReadinessReady, LocalReadinessNeedsReview, LocalReadinessBlocked:
		return true
	default:
		return false
	}
}

func sortOperatorReadinessDomains(domains []OperatorReadinessDomain) {
	sort.SliceStable(domains, func(i, j int) bool {
		if domains[i].Phase != domains[j].Phase {
			return domains[i].Phase < domains[j].Phase
		}
		return domains[i].Domain < domains[j].Domain
	})
}

func operatorGateKey(gate OperatorReadinessGate) string {
	return strings.Join([]string{strconv.Itoa(gate.Phase), gate.Domain, gate.Source, string(gate.Status), gate.Reason}, "\x00")
}
