// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
)

func TestReconcilerOwnsSupportedRecordsIdempotentlyAndDeletesOnlyOwnedState(t *testing.T) {
	provider := newFakeProvider()
	store := NewMemoryOwnershipStore()
	var events []AuditEvent
	reconciler := &Reconciler{
		Provider: provider,
		Store:    store,
		Audit: AuditSinkFunc(func(_ context.Context, event AuditEvent) error {
			events = append(events, event)
			return nil
		}),
		Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	}
	request := Request{
		ZoneID: "zone-1", OwnerKind: "domain", OwnerID: "dom-1",
		Scope: Scope{WorkspaceID: "wrk-1", ProjectID: "prj-1", EnvironmentID: "env-1"},
		Desired: []DesiredRecord{
			{Type: TypeTXT, Name: "_midnight.api.example.com", Content: "midnight-verification=secret-value", TTL: 300},
			{Type: TypeCNAME, Name: "api.example.com", Content: "apps.example.com", TTL: 300},
			{Type: TypeA, Name: "v4.example.com", Content: "192.0.2.8", TTL: 60},
			{Type: TypeAAAA, Name: "v6.example.com", Content: "2001:db8::8", TTL: 60},
		},
	}

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if result.Applied != 4 || provider.mutationCount() != 4 {
		t.Fatalf("applied=%d provider mutations=%d, want 4/4", result.Applied, provider.mutationCount())
	}
	ownerships, err := store.ListOwnerships(context.Background(), "cloudflare", "zone-1", "domain", "dom-1")
	if err != nil || len(ownerships) != 4 {
		t.Fatalf("ownerships=%d err=%v, want 4", len(ownerships), err)
	}
	for _, ownership := range ownerships {
		if ownership.ContentSHA256 == "" || ownership.ContentSHA256 == "midnight-verification=secret-value" {
			t.Fatalf("ownership retained plaintext or empty content digest: %#v", ownership)
		}
	}
	for _, event := range events {
		if event.ContentSHA256 == "midnight-verification=secret-value" {
			t.Fatal("audit event retained plaintext TXT content")
		}
	}

	second, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if second.Applied != 0 || provider.mutationCount() != 4 {
		t.Fatalf("idempotent applied=%d provider mutations=%d", second.Applied, provider.mutationCount())
	}

	dryRun := request
	dryRun.DryRun = true
	dryRun.Desired = nil
	preview, err := reconciler.Reconcile(context.Background(), dryRun)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if preview.Applied != 0 || provider.mutationCount() != 4 {
		t.Fatalf("dry-run applied=%d provider mutations=%d", preview.Applied, provider.mutationCount())
	}
	for _, action := range preview.Plan.Actions {
		if action.Kind != ActionDelete {
			t.Fatalf("dry-run action=%q, want delete", action.Kind)
		}
	}

	request.Desired = nil
	deleted, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if deleted.Applied != 4 || provider.recordCount() != 0 {
		t.Fatalf("deleted=%d remaining=%d, want 4/0", deleted.Applied, provider.recordCount())
	}
}

func TestReconcilerConflictFailsWholePlanWithoutMutatingUnmanagedRecords(t *testing.T) {
	provider := newFakeProvider()
	provider.records["unmanaged"] = Record{ID: "unmanaged", ZoneID: "zone-1", Type: TypeCNAME, Name: "api.example.com", Content: "other.example.com", TTL: 300}
	var events []AuditEvent
	reconciler := &Reconciler{Provider: provider, Store: NewMemoryOwnershipStore(), Audit: AuditSinkFunc(func(_ context.Context, event AuditEvent) error {
		events = append(events, event)
		return nil
	})}
	request := Request{ZoneID: "zone-1", OwnerKind: "domain", OwnerID: "dom-1", Desired: []DesiredRecord{
		{Type: TypeA, Name: "new.example.com", Content: "192.0.2.3", TTL: 60},
		{Type: TypeCNAME, Name: "api.example.com", Content: "apps.example.com", TTL: 300},
	}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if !result.Plan.HasConflicts() || result.Applied != 0 || provider.mutationCount() != 0 || provider.recordCount() != 1 {
		t.Fatalf("conflict plan mutated state: %#v mutations=%d records=%d", result, provider.mutationCount(), provider.recordCount())
	}
	if len(events) != 1 || events[0].Action != "dns.reconcile.conflict" {
		t.Fatalf("audit events=%#v", events)
	}
}

func TestReconcilerTreatsUnmanagedNSDelegationAsConflict(t *testing.T) {
	provider := newFakeProvider()
	provider.records["delegation"] = Record{ID: "delegation", ZoneID: "zone-1", Type: TypeNS, Name: "api.example.com", Content: "ns.example.net", TTL: 300}
	reconciler := &Reconciler{Provider: provider, Store: NewMemoryOwnershipStore(), Audit: AuditSinkFunc(func(context.Context, AuditEvent) error { return nil })}
	request := Request{ZoneID: "zone-1", OwnerKind: "domain", OwnerID: "dom-1", Desired: []DesiredRecord{{Type: TypeTXT, Name: "api.example.com", Content: "proof", TTL: 300}}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if !errors.Is(err, ErrConflict) || !result.Plan.HasConflicts() || provider.mutationCount() != 0 {
		t.Fatalf("result=%#v err=%v mutations=%d", result, err, provider.mutationCount())
	}
}

func TestReconcilerRefusesExternallyChangedOwnedRecordAndRecreatesMissingRecord(t *testing.T) {
	provider := newFakeProvider()
	store := NewMemoryOwnershipStore()
	reconciler := &Reconciler{Provider: provider, Store: store, Audit: AuditSinkFunc(func(context.Context, AuditEvent) error { return nil })}
	request := Request{ZoneID: "zone-1", OwnerKind: "domain", OwnerID: "dom-1", Desired: []DesiredRecord{{Type: TypeA, Name: "api.example.com", Content: "192.0.2.4", TTL: 60}}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	record := provider.onlyRecord(t)
	record.Content = "192.0.2.99"
	provider.records[record.ID] = record
	request.Desired = nil
	result, err := reconciler.Reconcile(context.Background(), request)
	if !errors.Is(err, ErrConflict) || result.Applied != 0 || provider.recordCount() != 1 {
		t.Fatalf("external drift result=%#v err=%v", result, err)
	}

	delete(provider.records, record.ID)
	request.Desired = []DesiredRecord{{Type: TypeA, Name: "api.example.com", Content: "192.0.2.4", TTL: 60}}
	result, err = reconciler.Reconcile(context.Background(), request)
	if err != nil || result.Applied != 1 || result.Plan.Actions[0].Kind != ActionRecreate {
		t.Fatalf("recreate result=%#v err=%v", result, err)
	}
}

func TestReconcilerRequiresAuditBeforeApply(t *testing.T) {
	provider := newFakeProvider()
	request := Request{ZoneID: "zone", OwnerKind: "domain", OwnerID: "dom", Desired: []DesiredRecord{{Type: TypeA, Name: "api.example.com", Content: "192.0.2.1", TTL: 60}}}
	reconciler := &Reconciler{Provider: provider, Store: NewMemoryOwnershipStore()}
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil || provider.mutationCount() != 0 {
		t.Fatalf("missing audit err=%v mutations=%d", err, provider.mutationCount())
	}
	reconciler.Audit = AuditSinkFunc(func(context.Context, AuditEvent) error { return errors.New("audit unavailable") })
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil || provider.mutationCount() != 0 {
		t.Fatalf("failed audit err=%v mutations=%d", err, provider.mutationCount())
	}
}

func TestReconcilerSerializesConcurrentOwnerApplies(t *testing.T) {
	provider := newFakeProvider()
	reconciler := &Reconciler{
		Provider: provider, Store: NewMemoryOwnershipStore(),
		Audit: AuditSinkFunc(func(context.Context, AuditEvent) error { return nil }),
	}
	request := Request{ZoneID: "zone", OwnerKind: "domain", OwnerID: "dom", Desired: []DesiredRecord{{Type: TypeA, Name: "api.example.com", Content: "192.0.2.1", TTL: 60}}}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := reconciler.Reconcile(context.Background(), request)
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.mutationCount() != 1 || provider.recordCount() != 1 {
		t.Fatalf("mutations=%d records=%d, want one converged create", provider.mutationCount(), provider.recordCount())
	}
}

func TestChainAuditSinkNeverStoresPlaintextDNSContent(t *testing.T) {
	chain := audit.NewMemoryChain()
	sink := ChainAuditSink{Chain: chain, ActorID: "worker-1"}
	secret := "midnight-verification=secret-value"
	if err := sink.RecordDNS(context.Background(), AuditEvent{
		Action: "dns.record.create.succeeded", Provider: "cloudflare", ZoneID: "zone-1",
		OwnerKind: "domain", OwnerID: "dom-1", Scope: Scope{ProjectID: "prj-1"},
		RecordType: TypeTXT, RecordName: "_midnight.example.com", ContentSHA256: contentDigest(secret),
		ProviderRecordID: "record-1", At: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	encoded := fmt.Sprintf("%#v", rows[0])
	if strings.Contains(encoded, secret) || rows[0].Action != "dns.record.create.succeeded" || rows[0].ProjectID != "prj-1" {
		t.Fatalf("unsafe or malformed audit row=%s", encoded)
	}
}

func TestDesiredRecordValidation(t *testing.T) {
	tests := []DesiredRecord{
		{Type: "MX", Name: "example.com", Content: "mail.example.com", TTL: 60},
		{Type: TypeA, Name: "example.com", Content: "2001:db8::1", TTL: 60},
		{Type: TypeAAAA, Name: "example.com", Content: "192.0.2.1", TTL: 60},
		{Type: TypeCNAME, Name: "example.com", Content: "not a host", TTL: 60},
		{Type: TypeTXT, Name: "_midnight.example.com", Content: "", TTL: 60},
		{Type: TypeTXT, Name: "_midnight.example.com", Content: "value", TTL: 59},
	}
	for _, record := range tests {
		if _, err := normalizeDesired(record); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("record=%#v err=%v, want ErrInvalidRequest", record, err)
		}
	}
	valid, err := normalizeDesired(DesiredRecord{Type: TypeTXT, Name: "_MIDNIGHT.Example.COM.", Content: "value", TTL: 0})
	if err != nil || valid.Name != "_midnight.example.com" || valid.TTL != 1 {
		t.Fatalf("valid normalized=%#v err=%v", valid, err)
	}
}

type fakeProvider struct {
	mu        sync.Mutex
	records   map[string]Record
	mutations []string
	nextID    int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{records: make(map[string]Record), nextID: 1}
}

func (*fakeProvider) Name() string { return "cloudflare" }

func (p *fakeProvider) ListRecords(_ context.Context, zoneID string) ([]Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Record, 0, len(p.records))
	for _, record := range p.records {
		if record.ZoneID == zoneID {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *fakeProvider) CreateRecord(_ context.Context, zoneID string, desired DesiredRecord) (Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := fmt.Sprintf("record-%d", p.nextID)
	p.nextID++
	record := Record{ID: id, ZoneID: zoneID, Type: desired.Type, Name: desired.Name, Content: desired.Content, TTL: desired.TTL}
	p.records[id] = record
	p.mutations = append(p.mutations, "create:"+id)
	return record, nil
}

func (p *fakeProvider) UpdateRecord(_ context.Context, zoneID, recordID string, desired DesiredRecord) (Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.records[recordID]; !exists {
		return Record{}, ErrNotFound
	}
	record := Record{ID: recordID, ZoneID: zoneID, Type: desired.Type, Name: desired.Name, Content: desired.Content, TTL: desired.TTL}
	p.records[recordID] = record
	p.mutations = append(p.mutations, "update:"+recordID)
	return record, nil
}

func (p *fakeProvider) DeleteRecord(_ context.Context, _ string, recordID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.records[recordID]; !exists {
		return ErrNotFound
	}
	delete(p.records, recordID)
	p.mutations = append(p.mutations, "delete:"+recordID)
	return nil
}

func (p *fakeProvider) mutationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.mutations)
}

func (p *fakeProvider) recordCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.records)
}

func (p *fakeProvider) onlyRecord(t *testing.T) Record {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.records) != 1 {
		t.Fatalf("records=%d, want 1", len(p.records))
	}
	for _, record := range p.records {
		return record
	}
	return Record{}
}
