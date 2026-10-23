// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"net/netip"
	"testing"
)

func TestControllerReconcilesDomainIntentAndCleansUpRetiredOwner(t *testing.T) {
	provider := newFakeProvider()
	ownerships := NewMemoryOwnershipStore()
	intentStore := &fakeDomainIntentStore{ownerships: ownerships, intents: []DomainIntent{{
		DomainID: "dom-1", Scope: Scope{WorkspaceID: "wrk-1", ProjectID: "prj-1", EnvironmentID: "env-1"},
		Kind: "custom", Hostname: "api.example.com",
		TXTRecordName: "_midnight-challenge.api.example.com", TXTRecordValue: "midnight-verification=value",
	}}}
	controller := &Controller{
		Reconciler: &Reconciler{Provider: provider, Store: ownerships, Audit: AuditSinkFunc(func(context.Context, AuditEvent) error { return nil })},
		Intents:    intentStore,
		Config:     ControllerConfig{ZoneID: "zone-1", ZoneName: "example.com", RouteTarget: "apps.example.com", TTL: 300},
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.recordCount() != 2 || provider.mutationCount() != 2 {
		t.Fatalf("records=%d mutations=%d, want TXT+CNAME", provider.recordCount(), provider.mutationCount())
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.mutationCount() != 2 {
		t.Fatalf("idempotent mutations=%d, want 2", provider.mutationCount())
	}

	intentStore.intents = nil
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.recordCount() != 0 || provider.mutationCount() != 4 {
		t.Fatalf("retired cleanup records=%d mutations=%d", provider.recordCount(), provider.mutationCount())
	}
}

func TestDesiredDomainRecordsUsesAddressRecordsAtApexAndRejectsOtherZones(t *testing.T) {
	config := ControllerConfig{
		ZoneID: "zone", ZoneName: "example.com", RouteTarget: "apps.example.com", TTL: 300,
		IPv4: netip.MustParseAddr("192.0.2.10"), IPv6: netip.MustParseAddr("2001:db8::10"),
	}
	config, err := normalizeControllerConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	apex, err := desiredDomainRecords(config, DomainIntent{
		DomainID: "dom-apex", Kind: "custom", Hostname: "example.com",
		TXTRecordName: "_midnight-challenge.example.com", TXTRecordValue: "proof",
	})
	if err != nil || len(apex) != 3 || apex[0].Type != TypeTXT || apex[1].Type != TypeA || apex[2].Type != TypeAAAA {
		t.Fatalf("apex=%#v err=%v", apex, err)
	}
	subdomain, err := desiredDomainRecords(config, DomainIntent{DomainID: "dom-sub", Kind: "generated", Hostname: "api.example.com"})
	if err != nil || len(subdomain) != 1 || subdomain[0].Type != TypeCNAME {
		t.Fatalf("subdomain=%#v err=%v", subdomain, err)
	}
	if _, err := desiredDomainRecords(config, DomainIntent{DomainID: "dom-other", Kind: "generated", Hostname: "api.other.test"}); err == nil {
		t.Fatal("expected out-of-zone hostname rejection")
	}
}

func TestControllerDryRunNeverCreatesProviderOrOwnershipState(t *testing.T) {
	provider := newFakeProvider()
	ownerships := NewMemoryOwnershipStore()
	controller := &Controller{
		Reconciler: &Reconciler{Provider: provider, Store: ownerships, Audit: AuditSinkFunc(func(context.Context, AuditEvent) error { return nil })},
		Intents: &fakeDomainIntentStore{ownerships: ownerships, intents: []DomainIntent{{
			DomainID: "dom-1", Scope: Scope{WorkspaceID: "wrk", ProjectID: "prj", EnvironmentID: "env"}, Kind: "generated", Hostname: "api.example.com",
		}}},
		Config: ControllerConfig{ZoneID: "zone", ZoneName: "example.com", RouteTarget: "apps.example.com", TTL: 300, DryRun: true},
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.mutationCount() != 0 || provider.recordCount() != 0 {
		t.Fatalf("dry-run mutations=%d records=%d", provider.mutationCount(), provider.recordCount())
	}
}

type fakeDomainIntentStore struct {
	intents    []DomainIntent
	ownerships *MemoryOwnershipStore
}

func (s *fakeDomainIntentStore) ListDomainIntents(context.Context) ([]DomainIntent, error) {
	return append([]DomainIntent(nil), s.intents...), nil
}

func (s *fakeDomainIntentStore) ListManagedOwners(_ context.Context, provider, zoneID string) ([]ManagedOwner, error) {
	s.ownerships.mu.RLock()
	defer s.ownerships.mu.RUnlock()
	byID := map[string]ManagedOwner{}
	for _, ownership := range s.ownerships.rows {
		if ownership.Provider == provider && ownership.ZoneID == zoneID {
			byID[ownership.OwnerID] = ManagedOwner{OwnerKind: ownership.OwnerKind, OwnerID: ownership.OwnerID, Scope: ownership.Scope}
		}
	}
	out := make([]ManagedOwner, 0, len(byID))
	for _, owner := range byID {
		out = append(out, owner)
	}
	return out, nil
}
