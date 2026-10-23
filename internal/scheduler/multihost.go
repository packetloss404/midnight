package scheduler

import (
	"errors"
	"sort"
	"strings"
)

// HostStatus is the scheduler-visible lifecycle state for a host agent.
type HostStatus string

const (
	HostStatusReady    HostStatus = "ready"
	HostStatusDraining HostStatus = "draining"
	HostStatusOffline  HostStatus = "offline"
)

// Host describes the placement inputs the control plane already knows. It is
// intentionally runtime-neutral; agents or adapters provide these facts.
type Host struct {
	ID           string            `json:"id"`
	AgentVersion string            `json:"agent_version,omitempty"`
	Status       HostStatus        `json:"status"`
	Capacity     int               `json:"capacity"`
	Used         int               `json:"used"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// StatelessServiceRequest asks the scheduler to place replicas for a service
// that has no host-local state. Stateful services are rejected by this planner.
type StatelessServiceRequest struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id"`
	ServiceID      string            `json:"service_id"`
	Replicas       int               `json:"replicas"`
	Stateless      bool              `json:"stateless"`
	RequiredLabels map[string]string `json:"required_labels,omitempty"`
}

// PlacementPlan is the deterministic multihost placement result.
type PlacementPlan struct {
	RequestID  string          `json:"request_id"`
	ProjectID  string          `json:"project_id"`
	ServiceID  string          `json:"service_id"`
	Placements []Placement     `json:"placements"`
	Counts     []DispatchCount `json:"counts"`
	Notes      []DispatchNote  `json:"notes,omitempty"`
}

// Placement records one replica placement decision.
type Placement struct {
	Replica int             `json:"replica"`
	HostID  string          `json:"host_id,omitempty"`
	Outcome DispatchOutcome `json:"outcome"`
	Reason  string          `json:"reason,omitempty"`
}

// CanaryPolicy bounds an agent rollout planning batch.
type CanaryPolicy struct {
	TargetVersion  string `json:"target_version"`
	MaxBatch       int    `json:"max_batch"`
	MaxUnavailable int    `json:"max_unavailable"`
}

// AgentCanaryPlan is a side-effect-free rollout batch for host agents.
type AgentCanaryPlan struct {
	TargetVersion string         `json:"target_version"`
	Updates       []AgentUpdate  `json:"updates"`
	Holdouts      []AgentHoldout `json:"holdouts,omitempty"`
	Notes         []DispatchNote `json:"notes,omitempty"`
}

type AgentUpdate struct {
	HostID      string `json:"host_id"`
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version"`
}

type AgentHoldout struct {
	HostID string `json:"host_id"`
	Reason string `json:"reason"`
}

var (
	ErrStatefulServiceUnsupported = errors.New("scheduler: multihost planner only supports stateless services")
	ErrEmptyServiceRequestID      = errors.New("scheduler: service request id is required")
	ErrEmptyServiceID             = errors.New("scheduler: service id is required")
	ErrEmptyTargetVersion         = errors.New("scheduler: target agent version is required")
)

// PlanStatelessService spreads stateless replicas across ready hosts with spare
// capacity. It never schedules to draining/offline hosts and returns denied
// replica decisions when capacity or label constraints cannot be satisfied.
func PlanStatelessService(req StatelessServiceRequest, hosts []Host) (PlacementPlan, error) {
	if strings.TrimSpace(req.ID) == "" {
		return PlacementPlan{}, ErrEmptyServiceRequestID
	}
	if strings.TrimSpace(req.ServiceID) == "" {
		return PlacementPlan{}, ErrEmptyServiceID
	}
	if !req.Stateless {
		return PlacementPlan{}, ErrStatefulServiceUnsupported
	}
	if req.Replicas < 0 {
		req.Replicas = 0
	}

	candidates := eligibleHosts(hosts, req.RequiredLabels)
	planned := make(map[string]int, len(candidates))
	plan := PlacementPlan{
		RequestID:  req.ID,
		ProjectID:  req.ProjectID,
		ServiceID:  req.ServiceID,
		Placements: make([]Placement, 0, req.Replicas),
	}
	for replica := 0; replica < req.Replicas; replica++ {
		host, ok := nextHost(candidates, planned)
		if !ok {
			plan.Placements = append(plan.Placements, Placement{Replica: replica, Outcome: OutcomeQuotaDenied, Reason: "no ready host capacity for stateless replica"})
			continue
		}
		planned[host.ID]++
		plan.Placements = append(plan.Placements, Placement{Replica: replica, HostID: host.ID, Outcome: OutcomeAccepted})
	}
	plan.Counts = countPlacementOutcomes(plan.Placements)
	if hasPlacementOutcome(plan.Placements, OutcomeQuotaDenied) {
		plan.Notes = []DispatchNote{{Code: "scheduler.multihost_capacity", Message: "one or more stateless replicas could not be placed on ready hosts"}}
	}
	return plan, nil
}

// PlanAgentCanaryRollout picks a deterministic batch of ready agents to update
// while respecting an unavailable-host budget.
func PlanAgentCanaryRollout(policy CanaryPolicy, hosts []Host) (AgentCanaryPlan, error) {
	policy.TargetVersion = strings.TrimSpace(policy.TargetVersion)
	if policy.TargetVersion == "" {
		return AgentCanaryPlan{}, ErrEmptyTargetVersion
	}
	if policy.MaxBatch <= 0 {
		policy.MaxBatch = 1
	}
	if policy.MaxUnavailable <= 0 {
		policy.MaxUnavailable = 1
	}

	ordered := append([]Host(nil), hosts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	unavailable := 0
	eligible := make([]Host, 0, len(ordered))
	plan := AgentCanaryPlan{TargetVersion: policy.TargetVersion}
	for _, host := range ordered {
		if strings.TrimSpace(host.ID) == "" {
			unavailable++
			plan.Holdouts = append(plan.Holdouts, AgentHoldout{Reason: "host id is required for canary"})
			continue
		}
		switch host.Status {
		case HostStatusReady:
			if host.AgentVersion == policy.TargetVersion {
				continue
			}
			eligible = append(eligible, host)
		case HostStatusDraining, HostStatusOffline:
			unavailable++
			plan.Holdouts = append(plan.Holdouts, AgentHoldout{HostID: host.ID, Reason: "host unavailable for canary"})
		default:
			unavailable++
			plan.Holdouts = append(plan.Holdouts, AgentHoldout{HostID: host.ID, Reason: "host status is not ready"})
		}
	}

	budget := policy.MaxUnavailable - unavailable
	if budget <= 0 {
		for _, host := range eligible {
			plan.Holdouts = append(plan.Holdouts, AgentHoldout{HostID: host.ID, Reason: "unavailable budget exhausted"})
		}
		plan.Notes = []DispatchNote{{Code: "scheduler.canary_budget_exhausted", Message: "agent canary rollout is held until unavailable hosts recover"}}
		return plan, nil
	}
	limit := min(policy.MaxBatch, budget, len(eligible))
	for i, host := range eligible {
		if i < limit {
			plan.Updates = append(plan.Updates, AgentUpdate{HostID: host.ID, FromVersion: host.AgentVersion, ToVersion: policy.TargetVersion})
			continue
		}
		plan.Holdouts = append(plan.Holdouts, AgentHoldout{HostID: host.ID, Reason: "outside canary batch"})
	}
	return plan, nil
}

func eligibleHosts(hosts []Host, labels map[string]string) []Host {
	out := make([]Host, 0, len(hosts))
	for _, host := range hosts {
		if strings.TrimSpace(host.ID) == "" || host.Status != HostStatusReady || available(host) <= 0 || !matchesLabels(host.Labels, labels) {
			continue
		}
		out = append(out, host)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func nextHost(hosts []Host, planned map[string]int) (Host, bool) {
	best := -1
	for i, host := range hosts {
		if planned[host.ID] >= available(host) {
			continue
		}
		if best == -1 || preferredHost(host, hosts[best], planned) {
			best = i
		}
	}
	if best == -1 {
		return Host{}, false
	}
	return hosts[best], true
}

func preferredHost(candidate, current Host, planned map[string]int) bool {
	candidateLoad := candidate.Used + planned[candidate.ID]
	currentLoad := current.Used + planned[current.ID]
	if candidateLoad != currentLoad {
		return candidateLoad < currentLoad
	}
	candidateRemaining := available(candidate) - planned[candidate.ID]
	currentRemaining := available(current) - planned[current.ID]
	if candidateRemaining != currentRemaining {
		return candidateRemaining > currentRemaining
	}
	return candidate.ID < current.ID
}

func available(host Host) int {
	return host.Capacity - host.Used
}

func matchesLabels(hostLabels, required map[string]string) bool {
	for key, value := range required {
		if hostLabels[key] != value {
			return false
		}
	}
	return true
}

func countPlacementOutcomes(placements []Placement) []DispatchCount {
	decisions := make([]Decision, 0, len(placements))
	for _, placement := range placements {
		decisions = append(decisions, Decision{Outcome: placement.Outcome})
	}
	return countOutcomes(decisions)
}

func hasPlacementOutcome(placements []Placement, outcome DispatchOutcome) bool {
	for _, placement := range placements {
		if placement.Outcome == outcome {
			return true
		}
	}
	return false
}
