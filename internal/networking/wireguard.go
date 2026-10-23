// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const DefaultWireGuardMTU = 1380

const MinimumWireGuardPMTUMTU = 1280

var defaultPMTUProbeSizes = []int{1500, 1420, DefaultWireGuardMTU, MinimumWireGuardPMTUMTU}

type PeerState string

const (
	PeerStateAbsent        PeerState = "absent"
	PeerStatePendingAdd    PeerState = "pending-add"
	PeerStateActive        PeerState = "active"
	PeerStatePendingRemove PeerState = "pending-remove"
	PeerStateRemoved       PeerState = "removed"
)

type PeerEvent string

const (
	PeerEventRequestAdd    PeerEvent = "request-add"
	PeerEventAddApplied    PeerEvent = "add-applied"
	PeerEventRequestRemove PeerEvent = "request-remove"
	PeerEventRemoveApplied PeerEvent = "remove-applied"
)

type PMTUProbePlan struct {
	PeerID      string `json:"peer_id"`
	ProbeSizes  []int  `json:"probe_sizes"`
	DefaultMTU  int    `json:"default_mtu"`
	MinimumMTU  int    `json:"minimum_mtu"`
	SelectedMTU int    `json:"selected_mtu"`
	Override    bool   `json:"override"`
	Degraded    bool   `json:"degraded"`
	Reason      string `json:"reason,omitempty"`
}

type WireGuardInterfacePlan struct {
	Name        string     `json:"name"`
	AddressCIDR string     `json:"address_cidr"`
	ListenPort  int        `json:"listen_port"`
	MTU         int        `json:"mtu"`
	Peers       []PeerSpec `json:"peers"`
}

type PeerSpec struct {
	ID         string   `json:"id"`
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowed_ips"`
	DNSName    string   `json:"dns_name,omitempty"`
}

type KeyRotationPolicy struct {
	Interval time.Duration `json:"interval"`
	Overlap  time.Duration `json:"overlap"`
}

type KeyRotationPlan struct {
	Due                  bool          `json:"due"`
	OverlapActive        bool          `json:"overlap_active"`
	RotateAt             time.Time     `json:"rotate_at"`
	OverlapUntil         time.Time     `json:"overlap_until"`
	NextRotationInterval time.Duration `json:"next_rotation_interval"`
}

type PeerKeyRotationPlan struct {
	PeerID            string          `json:"peer_id"`
	ActivePublicKeys  []string        `json:"active_public_keys"`
	RetirePublicKeyAt *time.Time      `json:"retire_public_key_at,omitempty"`
	Actions           []string        `json:"actions"`
	KeyRotation       KeyRotationPlan `json:"key_rotation"`
}

type ConntrackFlushPlan struct {
	Required   bool                    `json:"required"`
	PeerID     string                  `json:"peer_id,omitempty"`
	AllowedIPs []string                `json:"allowed_ips,omitempty"`
	Reason     string                  `json:"reason,omitempty"`
	Commands   []ConntrackFlushCommand `json:"commands,omitempty"`
}

type ConntrackFlushCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

type PeerTransitionPlan struct {
	From           PeerState          `json:"from"`
	To             PeerState          `json:"to"`
	Event          PeerEvent          `json:"event"`
	Actions        []string           `json:"actions"`
	ConntrackFlush ConntrackFlushPlan `json:"conntrack_flush,omitempty"`
}

func DefaultWireGuardPMTUProbeSizes() []int {
	return append([]int(nil), defaultPMTUProbeSizes...)
}

func PlanPMTUProbe(peer PeerSpec, observedWorkingMTU int) (PMTUProbePlan, error) {
	peer, err := normalizePeerSpec(peer)
	if err != nil {
		return PMTUProbePlan{}, err
	}
	plan := PMTUProbePlan{
		PeerID:      peer.ID,
		ProbeSizes:  DefaultWireGuardPMTUProbeSizes(),
		DefaultMTU:  DefaultWireGuardMTU,
		MinimumMTU:  MinimumWireGuardPMTUMTU,
		SelectedMTU: DefaultWireGuardMTU,
	}
	if observedWorkingMTU == 0 {
		plan.Reason = "no pmtu observation supplied; using default mtu"
		return plan, nil
	}
	if observedWorkingMTU < MinimumWireGuardPMTUMTU {
		plan.SelectedMTU = observedWorkingMTU
		plan.Degraded = true
		plan.Reason = "observed pmtu is below wireguard minimum"
		return plan, nil
	}
	if observedWorkingMTU < DefaultWireGuardMTU {
		plan.SelectedMTU = observedWorkingMTU
		plan.Override = true
		plan.Degraded = true
		plan.Reason = "observed pmtu is below default mtu; per-link override required"
	}
	return plan, nil
}

func NormalizeWireGuardInterfacePlan(plan WireGuardInterfacePlan) (WireGuardInterfacePlan, error) {
	plan.Name = strings.TrimSpace(plan.Name)
	plan.AddressCIDR = strings.TrimSpace(plan.AddressCIDR)
	if plan.MTU == 0 {
		plan.MTU = DefaultWireGuardMTU
	}
	if plan.Name == "" {
		return WireGuardInterfacePlan{}, errors.New("wireguard interface name is required")
	}
	if _, err := netip.ParsePrefix(plan.AddressCIDR); err != nil {
		return WireGuardInterfacePlan{}, fmt.Errorf("wireguard interface address_cidr is invalid: %w", err)
	}
	if plan.ListenPort <= 0 || plan.ListenPort > 65535 {
		return WireGuardInterfacePlan{}, errors.New("wireguard listen_port must be between 1 and 65535")
	}
	if plan.MTU != DefaultWireGuardMTU {
		return WireGuardInterfacePlan{}, fmt.Errorf("wireguard mtu must use PMTU-safe default %d", DefaultWireGuardMTU)
	}
	plan.Peers = append([]PeerSpec(nil), plan.Peers...)
	seenPeers := map[string]struct{}{}
	for i := range plan.Peers {
		peer, err := normalizePeerSpec(plan.Peers[i])
		if err != nil {
			return WireGuardInterfacePlan{}, err
		}
		if _, ok := seenPeers[peer.ID]; ok {
			return WireGuardInterfacePlan{}, fmt.Errorf("wireguard peer %q must be unique", peer.ID)
		}
		seenPeers[peer.ID] = struct{}{}
		plan.Peers[i] = peer
	}
	sort.SliceStable(plan.Peers, func(i, j int) bool { return plan.Peers[i].ID < plan.Peers[j].ID })
	return plan, nil
}

func PlanKeyRotation(now, lastRotated time.Time, policy KeyRotationPolicy) (KeyRotationPlan, error) {
	if policy.Interval <= 0 {
		return KeyRotationPlan{}, errors.New("key rotation interval must be positive")
	}
	if policy.Overlap <= 0 {
		return KeyRotationPlan{}, errors.New("key rotation overlap must be positive")
	}
	if policy.Overlap >= policy.Interval {
		return KeyRotationPlan{}, errors.New("key rotation overlap must be shorter than interval")
	}
	if lastRotated.IsZero() {
		return KeyRotationPlan{}, errors.New("last rotated time is required")
	}
	rotateAt := lastRotated.Add(policy.Interval)
	overlapUntil := rotateAt.Add(policy.Overlap)
	return KeyRotationPlan{
		Due:                  !now.Before(rotateAt),
		OverlapActive:        !now.Before(rotateAt) && now.Before(overlapUntil),
		RotateAt:             rotateAt,
		OverlapUntil:         overlapUntil,
		NextRotationInterval: policy.Interval,
	}, nil
}

func PlanPeerKeyRotation(now, lastRotated time.Time, policy KeyRotationPolicy, peer PeerSpec, nextPublicKey string) (PeerKeyRotationPlan, error) {
	peer, err := normalizePeerSpec(peer)
	if err != nil {
		return PeerKeyRotationPlan{}, err
	}
	nextPublicKey = strings.TrimSpace(nextPublicKey)
	if nextPublicKey == "" {
		return PeerKeyRotationPlan{}, errors.New("next peer public_key is required")
	}
	if nextPublicKey == peer.PublicKey {
		return PeerKeyRotationPlan{}, errors.New("next peer public_key must differ from current public_key")
	}
	rotation, err := PlanKeyRotation(now, lastRotated, policy)
	if err != nil {
		return PeerKeyRotationPlan{}, err
	}
	plan := PeerKeyRotationPlan{PeerID: peer.ID, ActivePublicKeys: []string{peer.PublicKey}, KeyRotation: rotation}
	if !rotation.Due {
		plan.Actions = []string{"keep-current-peer-key"}
		return plan, nil
	}
	if rotation.OverlapActive {
		retireAt := rotation.OverlapUntil
		plan.ActivePublicKeys = []string{peer.PublicKey, nextPublicKey}
		plan.RetirePublicKeyAt = &retireAt
		plan.Actions = []string{"write-overlap-peer-key", "accept-current-and-next-peer-keys"}
		return plan, nil
	}
	plan.ActivePublicKeys = []string{nextPublicKey}
	plan.Actions = []string{"write-next-peer-key", "retire-current-peer-key"}
	return plan, nil
}

func PlanPeerTransition(current PeerState, event PeerEvent, peer PeerSpec) (PeerTransitionPlan, error) {
	peer, err := normalizePeerSpec(peer)
	if err != nil {
		return PeerTransitionPlan{}, err
	}
	plan := PeerTransitionPlan{From: current, Event: event}
	switch current {
	case PeerStateAbsent, PeerStateRemoved:
		if event == PeerEventRequestAdd {
			plan.To = PeerStatePendingAdd
			plan.Actions = []string{"write-peer-config", "await-peer-handshake"}
			return plan, nil
		}
	case PeerStatePendingAdd:
		if event == PeerEventAddApplied {
			plan.To = PeerStateActive
			plan.Actions = []string{"mark-peer-active"}
			return plan, nil
		}
		if event == PeerEventRequestRemove {
			return removalPlan(plan, peer), nil
		}
	case PeerStateActive:
		if event == PeerEventRequestRemove {
			return removalPlan(plan, peer), nil
		}
	case PeerStatePendingRemove:
		if event == PeerEventRemoveApplied {
			plan.To = PeerStateRemoved
			plan.Actions = []string{"mark-peer-removed"}
			return plan, nil
		}
	}
	return PeerTransitionPlan{}, fmt.Errorf("invalid peer transition from %q on %q", current, event)
}

func removalPlan(plan PeerTransitionPlan, peer PeerSpec) PeerTransitionPlan {
	plan.To = PeerStatePendingRemove
	plan.Actions = []string{"remove-peer-config", "flush-peer-conntrack"}
	plan.ConntrackFlush = planConntrackFlush(peer)
	return plan
}

func PlanConntrackFlush(peer PeerSpec) (ConntrackFlushPlan, error) {
	peer, err := normalizePeerSpec(peer)
	if err != nil {
		return ConntrackFlushPlan{}, err
	}
	return planConntrackFlush(peer), nil
}

func planConntrackFlush(peer PeerSpec) ConntrackFlushPlan {
	flush := ConntrackFlushPlan{
		Required:   true,
		PeerID:     peer.ID,
		AllowedIPs: append([]string(nil), peer.AllowedIPs...),
		Reason:     "peer removal can leave stale NAT/session state for allowed IPs",
	}
	for _, allowedIP := range peer.AllowedIPs {
		flush.Commands = append(flush.Commands,
			ConntrackFlushCommand{Program: "conntrack", Args: []string{"-D", "-d", allowedIP}},
			ConntrackFlushCommand{Program: "conntrack", Args: []string{"-D", "-s", allowedIP}},
		)
	}
	return flush
}

func normalizePeerSpec(peer PeerSpec) (PeerSpec, error) {
	peer.ID = strings.TrimSpace(peer.ID)
	peer.PublicKey = strings.TrimSpace(peer.PublicKey)
	peer.Endpoint = strings.TrimSpace(peer.Endpoint)
	peer.DNSName = normalizeDNSName(peer.DNSName)
	if peer.ID == "" {
		return PeerSpec{}, errors.New("wireguard peer id is required")
	}
	if peer.PublicKey == "" {
		return PeerSpec{}, errors.New("wireguard peer public_key is required")
	}
	if len(peer.AllowedIPs) == 0 {
		return PeerSpec{}, errors.New("wireguard peer allowed_ips is required")
	}
	peer.AllowedIPs = append([]string(nil), peer.AllowedIPs...)
	seenAllowed := map[string]struct{}{}
	for i, allowed := range peer.AllowedIPs {
		allowed = strings.TrimSpace(allowed)
		if _, err := netip.ParsePrefix(allowed); err != nil {
			return PeerSpec{}, fmt.Errorf("wireguard peer allowed_ip %q is invalid: %w", allowed, err)
		}
		if _, ok := seenAllowed[allowed]; ok {
			return PeerSpec{}, fmt.Errorf("wireguard peer allowed_ip %q must be unique", allowed)
		}
		seenAllowed[allowed] = struct{}{}
		peer.AllowedIPs[i] = allowed
	}
	sort.Strings(peer.AllowedIPs)
	return peer, nil
}
