package supervisor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	AgentReleaseStatusReady       = "ready"
	AgentReleaseStatusNeedsReview = "needs-review"
	AgentReleaseStatusBlocked     = "blocked"

	AgentReleaseIssueDecisionBlocked       = "decision_blocked"
	AgentReleaseIssueDecisionPending       = "decision_pending"
	AgentReleaseIssueExecutionBlocked      = "execution_blocked"
	AgentReleaseIssueExecutionNeedsReview  = "execution_needs_review"
	AgentReleaseIssueHeartbeatLost         = "heartbeat_lost"
	AgentReleaseIssueHeartbeatStale        = "heartbeat_stale"
	AgentReleaseIssueReconcileStepsPending = "reconcile_steps_pending"
	AgentReleaseIssueRouteTargetsNotReady  = "route_targets_not_ready"
	AgentReleaseIssueDriftDetected         = "drift_detected"
	AgentReleaseIssueSelfHealCooldown      = "self_heal_cooldown"
	AgentReleaseIssueDiffStepsPending      = "diff_steps_pending"
)

// AgentReleaseEvidencePacket is a JSON-friendly release readiness rollup for one agent pass.
// It is the pass-level evidence packet: decision, execution, heartbeat, and
// runtime-route reviews are copied in and summarized into a deterministic issue rollup.
type AgentReleaseEvidencePacket struct {
	Status        string                         `json:"status"`
	Decision      AgentDecisionReview            `json:"decision"`
	Execution     LoopExecutionReview            `json:"execution"`
	Heartbeat     HeartbeatFreshnessReview       `json:"heartbeat"`
	Reconcile     AgentReleaseReconcileEvidence  `json:"reconcile"`
	RuntimeRoutes AgentReleaseRuntimeRouteStatus `json:"runtime_routes"`
	DiffPlan      AgentReleaseDiffPlan           `json:"diff_plan,omitempty"`
	Drift         []DriftEvent                   `json:"drift,omitempty"`
	SelfHeal      []SelfHealEvent                `json:"self_heal,omitempty"`
	IssueRollup   []AgentReleaseIssueRollup      `json:"issue_rollup"`
}

// AgentReleaseDiffPlan summarizes reconciler diff plan output for a pass.
// The plan is rolled up to per-action counts so reviewers see the shape of
// the upcoming reconciliation without scanning every step.
type AgentReleaseDiffPlan struct {
	StepCount int                  `json:"step_count"`
	Steps     []AgentDiffStepCount `json:"steps,omitempty"`
}

// AgentDiffStepCount is a deterministic per-(kind, action) count.
type AgentDiffStepCount struct {
	ResourceKind string `json:"resource_kind"`
	Action       string `json:"action"`
	Count        int    `json:"count"`
}

// AgentReleaseReconcileEvidence carries reconcile work counts into the release packet.
type AgentReleaseReconcileEvidence struct {
	StepCount      int                    `json:"step_count"`
	StepKindCounts []ReconcileStepKindSum `json:"step_kind_counts"`
}

// AgentReleaseRuntimeRouteStatus captures whether runtime route observations are release-ready.
type AgentReleaseRuntimeRouteStatus struct {
	Ready                     bool `json:"ready"`
	ObservedRouteTargets      int  `json:"observed_route_targets"`
	RouteTargetUpdatesPending int  `json:"route_target_updates_pending"`
}

// AgentReleaseIssueRollup summarizes deterministic release readiness issues across reviews.
type AgentReleaseIssueRollup struct {
	Code    string   `json:"code"`
	Count   int      `json:"count"`
	Sources []string `json:"sources"`
}

// AgentReleaseEvidenceExtras captures phase-1 evidence inputs that did
// not exist in the phase-0 packet. They are optional so the original
// build helper continues to work unchanged.
type AgentReleaseEvidenceExtras struct {
	DiffSteps []AgentDiffStepCount
	Drift     []DriftEvent
	SelfHeal  []SelfHealEvent
}

// BuildAgentReleaseEvidencePacketWithExtras is the phase-1 builder that
// folds drift, self-heal, and diff-plan evidence into the release packet.
// It calls BuildAgentReleaseEvidencePacket internally and adds the extras.
func BuildAgentReleaseEvidencePacketWithExtras(decisionArtifact AgentDecisionArtifact, decisionReview AgentDecisionReview, executionReview LoopExecutionReview, heartbeatReview HeartbeatFreshnessReview, extras AgentReleaseEvidenceExtras) AgentReleaseEvidencePacket {
	packet := BuildAgentReleaseEvidencePacket(decisionArtifact, decisionReview, executionReview, heartbeatReview)
	stepCount := 0
	for _, count := range extras.DiffSteps {
		stepCount += count.Count
	}
	if len(extras.DiffSteps) > 0 {
		packet.DiffPlan = AgentReleaseDiffPlan{StepCount: stepCount, Steps: append([]AgentDiffStepCount(nil), extras.DiffSteps...)}
	}
	if len(extras.Drift) > 0 {
		packet.Drift = append([]DriftEvent(nil), extras.Drift...)
	}
	if len(extras.SelfHeal) > 0 {
		packet.SelfHeal = append([]SelfHealEvent(nil), extras.SelfHeal...)
	}

	collector := newAgentReleaseIssueCollector()
	for _, existing := range packet.IssueRollup {
		for _, source := range existing.Sources {
			collector.add(existing.Code, source, existing.Count/len(existing.Sources)+1)
		}
		if len(existing.Sources) == 0 {
			collector.add(existing.Code, "decision", existing.Count)
		}
	}
	for _, event := range extras.Drift {
		if event.Classification == DriftDetected {
			collector.add(AgentReleaseIssueDriftDetected, "drift", 1)
			packet.Status = maxAgentReleaseStatus(packet.Status, AgentReleaseStatusNeedsReview)
		}
	}
	for _, event := range extras.SelfHeal {
		if event.Decision == SelfHealCooldownOn {
			collector.add(AgentReleaseIssueSelfHealCooldown, "self_heal", 1)
		}
	}
	if stepCount > 0 {
		collector.add(AgentReleaseIssueDiffStepsPending, "diff_plan", stepCount)
		packet.Status = maxAgentReleaseStatus(packet.Status, AgentReleaseStatusNeedsReview)
	}
	packet.IssueRollup = collector.rollup()
	return packet
}

// BuildAgentReleaseEvidencePacket combines pure reviews into a release readiness packet.
// It performs no HTTP, Docker, or filesystem I/O.
func BuildAgentReleaseEvidencePacket(decisionArtifact AgentDecisionArtifact, decisionReview AgentDecisionReview, executionReview LoopExecutionReview, heartbeatReview HeartbeatFreshnessReview) AgentReleaseEvidencePacket {
	reconcile := AgentReleaseReconcileEvidence{
		StepCount:      decisionArtifact.Reconcile.StepCount,
		StepKindCounts: append(make([]ReconcileStepKindSum, 0, len(decisionArtifact.Reconcile.StepKindCounts)), decisionArtifact.Reconcile.StepKindCounts...),
	}
	runtimeRoutes := AgentReleaseRuntimeRouteStatus{
		Ready:                     decisionArtifact.Reconcile.RouteTargetsReady,
		ObservedRouteTargets:      decisionArtifact.Observations.RouteTargets,
		RouteTargetUpdatesPending: decisionArtifact.Reconcile.RouteTargetUpdatesPending,
	}

	collector := newAgentReleaseIssueCollector()
	status := AgentReleaseStatusReady

	if decisionReview.Status == AgentDecisionStatusBlocked {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusBlocked)
		collector.add(AgentReleaseIssueDecisionBlocked, "decision", 1)
	} else if decisionReview.Status == AgentDecisionStatusPending {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusNeedsReview)
		collector.add(AgentReleaseIssueDecisionPending, "decision", 1)
	}
	for _, issue := range decisionReview.Issues {
		collector.add(issue.Code, "decision", 1)
	}

	if executionReview.ReleaseGate == LoopExecutionReleaseGateBlocked {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusBlocked)
		collector.add(AgentReleaseIssueExecutionBlocked, "execution", 1)
	} else if executionReview.ReleaseGate == LoopExecutionReleaseGateNeedsReview {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusNeedsReview)
		collector.add(AgentReleaseIssueExecutionNeedsReview, "execution", 1)
	}
	for _, issue := range executionReview.IssueRollup {
		collector.add(issue.IssueCode, "execution", issue.Count)
	}

	if heartbeatReview.Status == HeartbeatFreshnessStatusLost {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusBlocked)
		collector.add(AgentReleaseIssueHeartbeatLost, "heartbeat", 1)
	} else if heartbeatReview.Status == HeartbeatFreshnessStatusStale {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusNeedsReview)
		collector.add(AgentReleaseIssueHeartbeatStale, "heartbeat", 1)
	}
	for _, issue := range heartbeatReview.Issues {
		collector.add(issue.Code, "heartbeat", 1)
	}

	if reconcile.StepCount > 0 {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusNeedsReview)
		collector.add(AgentReleaseIssueReconcileStepsPending, "reconcile", reconcile.StepCount)
	}
	if !runtimeRoutes.Ready {
		status = maxAgentReleaseStatus(status, AgentReleaseStatusBlocked)
		collector.add(AgentReleaseIssueRouteTargetsNotReady, "runtime_routes", 1)
	}

	return AgentReleaseEvidencePacket{
		Status:        status,
		Decision:      decisionReview,
		Execution:     executionReview,
		Heartbeat:     heartbeatReview,
		Reconcile:     reconcile,
		RuntimeRoutes: runtimeRoutes,
		IssueRollup:   collector.rollup(),
	}
}

// ValidateAgentReleaseEvidencePacket validates release packet status and deterministic issue shape.
func ValidateAgentReleaseEvidencePacket(packet AgentReleaseEvidencePacket) error {
	if !isAgentReleaseStatus(packet.Status) {
		return fmt.Errorf("agent release status %q is not allowed", packet.Status)
	}
	previousCode := ""
	for i, issue := range packet.IssueRollup {
		if strings.TrimSpace(issue.Code) == "" {
			return fmt.Errorf("agent release issue at index %d must have a code", i)
		}
		if issue.Count <= 0 {
			return fmt.Errorf("agent release issue %q count must be positive", issue.Code)
		}
		if i > 0 && issue.Code < previousCode {
			return fmt.Errorf("agent release issue rollup must be sorted by code")
		}
		previousCode = issue.Code

		previousSource := ""
		for j, source := range issue.Sources {
			if strings.TrimSpace(source) == "" {
				return fmt.Errorf("agent release issue %q source at index %d must not be empty", issue.Code, j)
			}
			if j > 0 && source <= previousSource {
				return fmt.Errorf("agent release issue %q sources must be sorted and unique", issue.Code)
			}
			previousSource = source
		}
	}
	for i, event := range packet.Drift {
		if !isDriftClassification(event.Classification) {
			return fmt.Errorf("agent release drift event at index %d has invalid classification %q", i, event.Classification)
		}
		if event.At.IsZero() {
			return fmt.Errorf("agent release drift event at index %d must have a timestamp", i)
		}
	}
	for i, event := range packet.SelfHeal {
		if !isSelfHealDecision(event.Decision) {
			return fmt.Errorf("agent release self-heal event at index %d has invalid decision %q", i, event.Decision)
		}
		if event.At.IsZero() {
			return fmt.Errorf("agent release self-heal event at index %d must have a timestamp", i)
		}
		if event.Decision == SelfHealCooldownOn && strings.TrimSpace(event.InstanceID) == "" && strings.TrimSpace(event.Reason) == "" {
			return fmt.Errorf("agent release self-heal cooldown event at index %d must explain why it was denied", i)
		}
	}
	return nil
}

func isDriftClassification(c DriftClassification) bool {
	switch c {
	case DriftConverged, DriftDetected, DriftUnknown:
		return true
	default:
		return false
	}
}

func isSelfHealDecision(d SelfHealDecision) bool {
	switch d {
	case SelfHealAllow, SelfHealCooldownOn:
		return true
	default:
		return false
	}
}

// MarshalAgentReleaseEvidencePacket validates and marshals the packet with stable field names.
func MarshalAgentReleaseEvidencePacket(packet AgentReleaseEvidencePacket) ([]byte, error) {
	if err := ValidateAgentReleaseEvidencePacket(packet); err != nil {
		return nil, err
	}
	return json.Marshal(packet)
}

// WriteAgentReleaseEvidencePacketJSON writes a validated packet to an explicit .json path.
func WriteAgentReleaseEvidencePacketJSON(path string, packet AgentReleaseEvidencePacket) error {
	if err := validateAgentReleaseEvidencePacketJSONPath(path); err != nil {
		return err
	}
	if err := validateAgentReleaseEvidencePacketJSONParent(path); err != nil {
		return err
	}
	encoded, err := MarshalAgentReleaseEvidencePacket(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// ReadAgentReleaseEvidencePacketJSON strictly decodes and validates a packet from an explicit .json path.
func ReadAgentReleaseEvidencePacketJSON(path string) (AgentReleaseEvidencePacket, error) {
	var packet AgentReleaseEvidencePacket
	if err := validateAgentReleaseEvidencePacketJSONPath(path); err != nil {
		return packet, err
	}
	if err := validateAgentReleaseEvidencePacketJSONParent(path); err != nil {
		return packet, err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return packet, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&packet); err != nil {
		return packet, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("agent release evidence packet JSON must contain exactly one value")
		}
		return packet, err
	}
	if err := ValidateAgentReleaseEvidencePacket(packet); err != nil {
		return packet, err
	}
	return packet, nil
}

func validateAgentReleaseEvidencePacketJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("agent release evidence packet path must not be empty")
	}
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("agent release evidence packet path %q must end with .json", path)
	}
	return nil
}

func validateAgentReleaseEvidencePacketJSONParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("agent release evidence packet parent %q is not available: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agent release evidence packet parent %q is not a directory", parent)
	}
	return nil
}

type agentReleaseIssueCollector struct {
	counts  map[string]int
	sources map[string]map[string]struct{}
}

func newAgentReleaseIssueCollector() agentReleaseIssueCollector {
	return agentReleaseIssueCollector{
		counts:  make(map[string]int),
		sources: make(map[string]map[string]struct{}),
	}
}

func (c agentReleaseIssueCollector) add(code, source string, count int) {
	code = strings.TrimSpace(code)
	source = strings.TrimSpace(source)
	if code == "" || source == "" {
		return
	}
	if count <= 0 {
		count = 1
	}
	c.counts[code] += count
	if c.sources[code] == nil {
		c.sources[code] = make(map[string]struct{})
	}
	c.sources[code][source] = struct{}{}
}

func (c agentReleaseIssueCollector) rollup() []AgentReleaseIssueRollup {
	codes := make([]string, 0, len(c.counts))
	for code := range c.counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	rollup := make([]AgentReleaseIssueRollup, 0, len(codes))
	for _, code := range codes {
		sources := make([]string, 0, len(c.sources[code]))
		for source := range c.sources[code] {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		rollup = append(rollup, AgentReleaseIssueRollup{Code: code, Count: c.counts[code], Sources: sources})
	}
	return rollup
}

func maxAgentReleaseStatus(a, b string) string {
	if agentReleaseStatusRank(b) > agentReleaseStatusRank(a) {
		return b
	}
	return a
}

func agentReleaseStatusRank(status string) int {
	switch status {
	case AgentReleaseStatusBlocked:
		return 2
	case AgentReleaseStatusNeedsReview:
		return 1
	default:
		return 0
	}
}

func isAgentReleaseStatus(status string) bool {
	switch status {
	case AgentReleaseStatusReady, AgentReleaseStatusNeedsReview, AgentReleaseStatusBlocked:
		return true
	default:
		return false
	}
}
