// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeWireGuardInterfacePlanDefaultsMTU(t *testing.T) {
	plan, err := NormalizeWireGuardInterfacePlan(WireGuardInterfacePlan{
		Name:        " wg-midnight0 ",
		AddressCIDR: "100.64.0.1/24",
		ListenPort:  51820,
		Peers: []PeerSpec{{
			ID:         " peer-b ",
			PublicKey:  "pubkey-b",
			AllowedIPs: []string{"100.64.0.3/32"},
		}, {
			ID:         " peer-a ",
			PublicKey:  "pubkey-a",
			AllowedIPs: []string{"100.64.0.2/32"},
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeWireGuardInterfacePlan() error = %v", err)
	}
	if plan.MTU != DefaultWireGuardMTU {
		t.Fatalf("MTU = %d, want %d", plan.MTU, DefaultWireGuardMTU)
	}
	if plan.Name != "wg-midnight0" || len(plan.Peers) != 2 || plan.Peers[0].ID != "peer-a" || plan.Peers[1].ID != "peer-b" {
		t.Fatalf("plan was not normalized: %+v", plan)
	}
}

func TestNormalizeWireGuardInterfacePlanRejectsNonDefaultMTU(t *testing.T) {
	_, err := NormalizeWireGuardInterfacePlan(WireGuardInterfacePlan{Name: "wg0", AddressCIDR: "100.64.0.1/24", ListenPort: 51820, MTU: 1420})
	if err == nil || !strings.Contains(err.Error(), "1380") {
		t.Fatalf("NormalizeWireGuardInterfacePlan() error = %v, want PMTU default error", err)
	}
}

func TestPlanPMTUProbeUsesDefaultWithoutObservation(t *testing.T) {
	plan, err := PlanPMTUProbe(PeerSpec{ID: "peer-a", PublicKey: "pubkey-a", AllowedIPs: []string{"100.64.0.2/32"}}, 0)
	if err != nil {
		t.Fatalf("PlanPMTUProbe() error = %v", err)
	}
	if plan.SelectedMTU != DefaultWireGuardMTU || plan.Override || plan.Degraded || len(plan.ProbeSizes) != 4 {
		t.Fatalf("pmtu plan = %+v", plan)
	}
}

func TestPlanPMTUProbePlansOverrideForLowObservation(t *testing.T) {
	plan, err := PlanPMTUProbe(PeerSpec{ID: "peer-a", PublicKey: "pubkey-a", AllowedIPs: []string{"100.64.0.2/32"}}, 1300)
	if err != nil {
		t.Fatalf("PlanPMTUProbe() error = %v", err)
	}
	if plan.SelectedMTU != 1300 || !plan.Override || !plan.Degraded {
		t.Fatalf("pmtu plan = %+v", plan)
	}
}

func TestPlanKeyRotationSchedulesOverlap(t *testing.T) {
	last := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	plan, err := PlanKeyRotation(last.Add(30*24*time.Hour), last, KeyRotationPolicy{Interval: 30 * 24 * time.Hour, Overlap: 24 * time.Hour})
	if err != nil {
		t.Fatalf("PlanKeyRotation() error = %v", err)
	}
	if !plan.Due || !plan.RotateAt.Equal(last.Add(30*24*time.Hour)) || !plan.OverlapUntil.Equal(last.Add(31*24*time.Hour)) {
		t.Fatalf("rotation plan = %+v", plan)
	}
}

func TestPlanKeyRotationRejectsOverlapLongerThanInterval(t *testing.T) {
	_, err := PlanKeyRotation(time.Now(), time.Now(), KeyRotationPolicy{Interval: time.Hour, Overlap: time.Hour})
	if err == nil {
		t.Fatal("PlanKeyRotation() error = nil, want overlap validation error")
	}
}

func TestPlanPeerKeyRotationAcceptsBothKeysDuringOverlap(t *testing.T) {
	last := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	plan, err := PlanPeerKeyRotation(last.Add(30*24*time.Hour+time.Hour), last, KeyRotationPolicy{Interval: 30 * 24 * time.Hour, Overlap: 24 * time.Hour}, PeerSpec{ID: "peer-a", PublicKey: "pubkey-old", AllowedIPs: []string{"100.64.0.2/32"}}, "pubkey-new")
	if err != nil {
		t.Fatalf("PlanPeerKeyRotation() error = %v", err)
	}
	if !plan.KeyRotation.OverlapActive || len(plan.ActivePublicKeys) != 2 || plan.RetirePublicKeyAt == nil {
		t.Fatalf("peer key rotation plan = %+v", plan)
	}
}

func TestPlanPeerKeyRotationRetiresOldKeyAfterOverlap(t *testing.T) {
	last := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	plan, err := PlanPeerKeyRotation(last.Add(31*24*time.Hour+time.Hour), last, KeyRotationPolicy{Interval: 30 * 24 * time.Hour, Overlap: 24 * time.Hour}, PeerSpec{ID: "peer-a", PublicKey: "pubkey-old", AllowedIPs: []string{"100.64.0.2/32"}}, "pubkey-new")
	if err != nil {
		t.Fatalf("PlanPeerKeyRotation() error = %v", err)
	}
	if plan.KeyRotation.OverlapActive || len(plan.ActivePublicKeys) != 1 || plan.ActivePublicKeys[0] != "pubkey-new" {
		t.Fatalf("peer key rotation plan = %+v", plan)
	}
}

func TestPlanPeerTransitionAddAndRemove(t *testing.T) {
	peer := PeerSpec{ID: "peer-a", PublicKey: "pubkey-a", AllowedIPs: []string{"100.64.0.2/32"}}
	addPlan, err := PlanPeerTransition(PeerStateAbsent, PeerEventRequestAdd, peer)
	if err != nil {
		t.Fatalf("PlanPeerTransition(add) error = %v", err)
	}
	if addPlan.To != PeerStatePendingAdd || strings.Join(addPlan.Actions, ",") != "write-peer-config,await-peer-handshake" {
		t.Fatalf("add plan = %+v", addPlan)
	}

	removePlan, err := PlanPeerTransition(PeerStateActive, PeerEventRequestRemove, peer)
	if err != nil {
		t.Fatalf("PlanPeerTransition(remove) error = %v", err)
	}
	if removePlan.To != PeerStatePendingRemove || !removePlan.ConntrackFlush.Required || removePlan.ConntrackFlush.AllowedIPs[0] != "100.64.0.2/32" {
		t.Fatalf("remove plan = %+v", removePlan)
	}
}

func TestPlanConntrackFlushBuildsSourceAndDestinationCommands(t *testing.T) {
	plan, err := PlanConntrackFlush(PeerSpec{ID: "peer-a", PublicKey: "pubkey-a", AllowedIPs: []string{"100.64.0.2/32"}})
	if err != nil {
		t.Fatalf("PlanConntrackFlush() error = %v", err)
	}
	if !plan.Required || len(plan.Commands) != 2 {
		t.Fatalf("flush plan = %+v", plan)
	}
	if strings.Join(plan.Commands[0].Args, " ") != "-D -d 100.64.0.2/32" || strings.Join(plan.Commands[1].Args, " ") != "-D -s 100.64.0.2/32" {
		t.Fatalf("flush commands = %+v", plan.Commands)
	}
}

func TestPlanPeerTransitionRejectsInvalidTransition(t *testing.T) {
	peer := PeerSpec{ID: "peer-a", PublicKey: "pubkey-a", AllowedIPs: []string{"100.64.0.2/32"}}
	_, err := PlanPeerTransition(PeerStateAbsent, PeerEventRemoveApplied, peer)
	if err == nil {
		t.Fatal("PlanPeerTransition() error = nil, want invalid transition")
	}
}
