// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type RegionHealth string

const (
	RegionHealthHealthy   RegionHealth = "healthy"
	RegionHealthDegraded  RegionHealth = "degraded"
	RegionHealthUnhealthy RegionHealth = "unhealthy"
)

type RegionRouteCandidate struct {
	RegionID      string       `json:"region_id"`
	Endpoint      string       `json:"endpoint,omitempty"`
	Residency     string       `json:"residency"`
	Health        RegionHealth `json:"health"`
	LatencyMS     int          `json:"latency_ms"`
	Weight        int          `json:"weight"`
	FailoverOrder int          `json:"failover_order,omitempty"`
}

type RegionRoutePolicy struct {
	RequiredResidency string   `json:"required_residency,omitempty"`
	AllowedResidency  []string `json:"allowed_residency,omitempty"`
	MinimumStandbys   int      `json:"minimum_standbys,omitempty"`
}

type RegionRouteRejection struct {
	RegionID string `json:"region_id"`
	Reason   string `json:"reason"`
}

type RegionFailoverStep struct {
	FromRegionID string `json:"from_region_id"`
	ToRegionID   string `json:"to_region_id"`
	Reason       string `json:"reason"`
}

type RegionRoutePlan struct {
	Primary        RegionRouteCandidate   `json:"primary,omitempty"`
	HasPrimary     bool                   `json:"has_primary"`
	Standbys       []RegionRouteCandidate `json:"standbys,omitempty"`
	FailoverSteps  []RegionFailoverStep   `json:"failover_steps,omitempty"`
	Rejected       []RegionRouteRejection `json:"rejected,omitempty"`
	ResidencyScope []string               `json:"residency_scope,omitempty"`
	Degraded       bool                   `json:"degraded"`
	Reason         string                 `json:"reason,omitempty"`
}

func PlanRegionRoute(candidates []RegionRouteCandidate, policy RegionRoutePolicy) (RegionRoutePlan, error) {
	policy = normalizeRegionRoutePolicy(policy)
	if policy.MinimumStandbys < 0 {
		return RegionRoutePlan{}, errors.New("region route minimum_standbys must not be negative")
	}

	eligible := make([]RegionRouteCandidate, 0, len(candidates))
	rejected := make([]RegionRouteRejection, 0)
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate, err := normalizeRegionRouteCandidate(candidate)
		if err != nil {
			return RegionRoutePlan{}, err
		}
		if _, ok := seen[candidate.RegionID]; ok {
			return RegionRoutePlan{}, fmt.Errorf("region route candidate %q must be unique", candidate.RegionID)
		}
		seen[candidate.RegionID] = struct{}{}

		if reason := residencyIncompatibility(candidate.Residency, policy); reason != "" {
			rejected = append(rejected, RegionRouteRejection{RegionID: candidate.RegionID, Reason: reason})
			continue
		}
		if candidate.Health == RegionHealthUnhealthy {
			rejected = append(rejected, RegionRouteRejection{RegionID: candidate.RegionID, Reason: "region is unhealthy"})
			continue
		}
		eligible = append(eligible, candidate)
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		left, right := regionCandidateScore(eligible[i]), regionCandidateScore(eligible[j])
		if left != right {
			return left > right
		}
		if eligible[i].FailoverOrder != eligible[j].FailoverOrder {
			return eligible[i].FailoverOrder < eligible[j].FailoverOrder
		}
		if eligible[i].LatencyMS != eligible[j].LatencyMS {
			return eligible[i].LatencyMS < eligible[j].LatencyMS
		}
		return eligible[i].RegionID < eligible[j].RegionID
	})
	sort.SliceStable(rejected, func(i, j int) bool { return rejected[i].RegionID < rejected[j].RegionID })

	plan := RegionRoutePlan{Rejected: rejected, ResidencyScope: regionResidencyScope(policy)}
	if len(eligible) == 0 {
		plan.Degraded = true
		plan.Reason = "no healthy residency-compatible region candidates"
		return plan, nil
	}

	plan.Primary = eligible[0]
	plan.HasPrimary = true
	plan.Standbys = append([]RegionRouteCandidate(nil), eligible[1:]...)
	for _, standby := range plan.Standbys {
		plan.FailoverSteps = append(plan.FailoverSteps, RegionFailoverStep{
			FromRegionID: plan.Primary.RegionID,
			ToRegionID:   standby.RegionID,
			Reason:       "primary health failure or routing withdrawal",
		})
	}
	if len(plan.Standbys) < policy.MinimumStandbys {
		plan.Degraded = true
		plan.Reason = "insufficient residency-compatible standby regions"
	}
	return plan, nil
}

func normalizeRegionRouteCandidate(candidate RegionRouteCandidate) (RegionRouteCandidate, error) {
	candidate.RegionID = strings.TrimSpace(candidate.RegionID)
	candidate.Endpoint = strings.TrimSpace(candidate.Endpoint)
	candidate.Residency = strings.ToLower(strings.TrimSpace(candidate.Residency))
	candidate.Health = RegionHealth(strings.ToLower(strings.TrimSpace(string(candidate.Health))))
	if candidate.RegionID == "" {
		return RegionRouteCandidate{}, errors.New("region route candidate region_id is required")
	}
	if candidate.Residency == "" {
		return RegionRouteCandidate{}, fmt.Errorf("region route candidate %q residency is required", candidate.RegionID)
	}
	if candidate.Health == "" {
		candidate.Health = RegionHealthHealthy
	}
	if candidate.Health != RegionHealthHealthy && candidate.Health != RegionHealthDegraded && candidate.Health != RegionHealthUnhealthy {
		return RegionRouteCandidate{}, fmt.Errorf("region route candidate %q health is invalid", candidate.RegionID)
	}
	if candidate.LatencyMS < 0 {
		return RegionRouteCandidate{}, fmt.Errorf("region route candidate %q latency_ms must not be negative", candidate.RegionID)
	}
	if candidate.Weight <= 0 {
		candidate.Weight = 1
	}
	return candidate, nil
}

func normalizeRegionRoutePolicy(policy RegionRoutePolicy) RegionRoutePolicy {
	policy.RequiredResidency = strings.ToLower(strings.TrimSpace(policy.RequiredResidency))
	allowed := make([]string, 0, len(policy.AllowedResidency))
	seen := map[string]struct{}{}
	for _, residency := range policy.AllowedResidency {
		residency = strings.ToLower(strings.TrimSpace(residency))
		if residency == "" {
			continue
		}
		if _, ok := seen[residency]; ok {
			continue
		}
		seen[residency] = struct{}{}
		allowed = append(allowed, residency)
	}
	sort.Strings(allowed)
	policy.AllowedResidency = allowed
	return policy
}

func residencyIncompatibility(residency string, policy RegionRoutePolicy) string {
	if policy.RequiredResidency != "" && residency != policy.RequiredResidency {
		return "region residency does not match required residency"
	}
	if len(policy.AllowedResidency) == 0 {
		return ""
	}
	for _, allowed := range policy.AllowedResidency {
		if residency == allowed {
			return ""
		}
	}
	return "region residency is not allowed by policy"
}

func regionCandidateScore(candidate RegionRouteCandidate) int {
	healthBonus := 0
	if candidate.Health == RegionHealthHealthy {
		healthBonus = 1_000_000
	}
	return healthBonus + candidate.Weight*10_000 - candidate.LatencyMS
}

func regionResidencyScope(policy RegionRoutePolicy) []string {
	if policy.RequiredResidency != "" {
		return []string{policy.RequiredResidency}
	}
	return append([]string(nil), policy.AllowedResidency...)
}
