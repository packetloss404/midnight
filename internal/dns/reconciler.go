// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ActionKind string

const (
	ActionNoop     ActionKind = "noop"
	ActionCreate   ActionKind = "create"
	ActionRecreate ActionKind = "recreate"
	ActionUpdate   ActionKind = "update"
	ActionDelete   ActionKind = "delete"
	ActionForget   ActionKind = "forget"
	ActionConflict ActionKind = "conflict"
)

// Action never includes plaintext record content in its JSON representation.
// Desired is retained only for execution inside the process.
type Action struct {
	Kind             ActionKind    `json:"kind"`
	Type             RecordType    `json:"type"`
	Name             string        `json:"name"`
	ContentSHA256    string        `json:"content_sha256,omitempty"`
	ProviderRecordID string        `json:"provider_record_id,omitempty"`
	Reason           string        `json:"reason,omitempty"`
	Desired          DesiredRecord `json:"-"`
	Ownership        *Ownership    `json:"-"`
}

type Plan struct {
	Provider  string   `json:"provider"`
	ZoneID    string   `json:"zone_id"`
	OwnerKind string   `json:"owner_kind"`
	OwnerID   string   `json:"owner_id"`
	DryRun    bool     `json:"dry_run"`
	Actions   []Action `json:"actions"`
}

func (p Plan) HasConflicts() bool {
	for _, action := range p.Actions {
		if action.Kind == ActionConflict {
			return true
		}
	}
	return false
}

type Result struct {
	Plan    Plan `json:"plan"`
	Applied int  `json:"applied"`
}

type Reconciler struct {
	Provider Provider
	Store    OwnershipStore
	Audit    AuditSink
	Now      func() time.Time

	lockMu sync.Mutex
	locks  map[string]*ownerLock
}

type ownerLock struct {
	mu   sync.Mutex
	refs int
}

// Plan observes provider and durable ownership state without changing either.
func (r *Reconciler) Plan(ctx context.Context, request Request) (Plan, error) {
	if r == nil || r.Provider == nil || r.Store == nil {
		return Plan{}, errors.New("dns reconciler requires provider and ownership store")
	}
	request, err := normalizeRequest(request)
	if err != nil {
		return Plan{}, err
	}
	providerName := strings.ToLower(strings.TrimSpace(r.Provider.Name()))
	if providerName == "" {
		return Plan{}, errors.New("dns provider name is required")
	}
	observed, err := r.Provider.ListRecords(ctx, request.ZoneID)
	if err != nil {
		return Plan{}, fmt.Errorf("list %s DNS records: %w", providerName, err)
	}
	owned, err := r.Store.ListOwnerships(ctx, providerName, request.ZoneID, request.OwnerKind, request.OwnerID)
	if err != nil {
		return Plan{}, fmt.Errorf("list DNS ownership: %w", err)
	}
	return buildPlan(providerName, request, observed, owned)
}

// Reconcile serializes one owner in-process, plans from fresh provider state,
// refuses the entire apply when any conflict exists, and records redaction-safe
// intent before each external mutation. Dry-run performs no provider or
// ownership mutation.
func (r *Reconciler) Reconcile(ctx context.Context, request Request) (Result, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	unlock := r.lockOwner(request.ZoneID + "\x00" + request.OwnerKind + "\x00" + request.OwnerID)
	defer unlock()

	plan, err := r.Plan(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if request.DryRun {
		for _, action := range plan.Actions {
			event := auditEventForAction(request, action)
			event.Action = "dns.record." + string(action.Kind) + ".planned"
			event.DryRun = true
			if err := r.audit(ctx, request, event); err != nil {
				return Result{Plan: plan}, fmt.Errorf("record DNS dry-run action: %w", err)
			}
		}
		if err := r.audit(ctx, request, AuditEvent{Action: "dns.reconcile.dry_run", DryRun: true, Reason: planSummary(plan)}); err != nil {
			return Result{Plan: plan}, err
		}
		return Result{Plan: plan}, nil
	}
	if r.Audit == nil {
		return Result{Plan: plan}, errors.New("dns reconcile apply requires an audit sink")
	}
	if plan.HasConflicts() {
		if auditErr := r.audit(ctx, request, AuditEvent{Action: "dns.reconcile.conflict", Reason: planSummary(plan)}); auditErr != nil {
			return Result{Plan: plan}, errors.Join(ErrConflict, fmt.Errorf("record DNS conflict: %w", auditErr))
		}
		return Result{Plan: plan}, ErrConflict
	}

	result := Result{Plan: plan}
	for _, action := range plan.Actions {
		if action.Kind == ActionNoop {
			continue
		}
		event := auditEventForAction(request, action)
		event.Action = "dns.record." + string(action.Kind) + ".requested"
		if err := r.audit(ctx, request, event); err != nil {
			return result, fmt.Errorf("record DNS mutation intent: %w", err)
		}
		if err := r.applyAction(ctx, request, action, &event); err != nil {
			failed := event
			failed.Action = "dns.record." + string(action.Kind) + ".failed"
			failed.Reason = "provider or ownership operation failed"
			_ = r.audit(ctx, request, failed)
			return result, err
		}
		event.Action = "dns.record." + string(action.Kind) + ".succeeded"
		if err := r.audit(ctx, request, event); err != nil {
			return result, fmt.Errorf("record DNS mutation result: %w", err)
		}
		result.Applied++
	}
	return result, nil
}

func (r *Reconciler) applyAction(ctx context.Context, request Request, action Action, event *AuditEvent) error {
	provider := strings.ToLower(strings.TrimSpace(r.Provider.Name()))
	now := r.now()
	switch action.Kind {
	case ActionCreate, ActionRecreate:
		created, err := r.Provider.CreateRecord(ctx, request.ZoneID, action.Desired)
		if err != nil {
			return fmt.Errorf("create %s DNS record: %w", action.Type, err)
		}
		created, err = validateMutationResult(created, request.ZoneID, action.Desired)
		if err != nil {
			return err
		}
		ownership := newOwnership(provider, request, created, now)
		if err := r.Store.PutOwnership(ctx, ownership); err != nil {
			return fmt.Errorf("persist created DNS ownership: %w", err)
		}
		event.ProviderRecordID = created.ID
	case ActionUpdate:
		updated, err := r.Provider.UpdateRecord(ctx, request.ZoneID, action.ProviderRecordID, action.Desired)
		if err != nil {
			return fmt.Errorf("update %s DNS record: %w", action.Type, err)
		}
		updated, err = validateMutationResult(updated, request.ZoneID, action.Desired)
		if err != nil {
			return err
		}
		ownership := newOwnership(provider, request, updated, now)
		if action.Ownership != nil {
			ownership.CreatedAt = action.Ownership.CreatedAt
		}
		if err := r.Store.PutOwnership(ctx, ownership); err != nil {
			return fmt.Errorf("persist updated DNS ownership: %w", err)
		}
		event.ProviderRecordID = updated.ID
	case ActionDelete:
		if err := r.Provider.DeleteRecord(ctx, request.ZoneID, action.ProviderRecordID); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("delete %s DNS record: %w", action.Type, err)
		}
		if err := r.Store.DeleteOwnership(ctx, provider, request.ZoneID, request.OwnerKind, request.OwnerID, action.Type, action.Name); err != nil {
			return fmt.Errorf("delete DNS ownership: %w", err)
		}
	case ActionForget:
		if err := r.Store.DeleteOwnership(ctx, provider, request.ZoneID, request.OwnerKind, request.OwnerID, action.Type, action.Name); err != nil {
			return fmt.Errorf("forget absent DNS ownership: %w", err)
		}
	default:
		return fmt.Errorf("unsupported DNS plan action %q", action.Kind)
	}
	return nil
}

func buildPlan(provider string, request Request, observed []Record, owned []Ownership) (Plan, error) {
	plan := Plan{Provider: provider, ZoneID: request.ZoneID, OwnerKind: request.OwnerKind, OwnerID: request.OwnerID, DryRun: request.DryRun}
	byID := make(map[string]Record, len(observed))
	byName := make(map[string][]Record)
	for _, raw := range observed {
		if raw.Type == TypeNS {
			name, nameErr := normalizeRecordName(raw.Name, false)
			if nameErr != nil || strings.TrimSpace(raw.ID) == "" {
				return Plan{}, errors.New("DNS provider returned invalid NS record metadata")
			}
			raw.ID = strings.TrimSpace(raw.ID)
			raw.Name = name
			raw.ZoneID = request.ZoneID
			byID[raw.ID] = raw
			byName[raw.Name] = append(byName[raw.Name], raw)
			continue
		}
		record, err := normalizeObserved(raw, request.ZoneID)
		if err != nil {
			return Plan{}, err
		}
		if record.ID == "" {
			return Plan{}, errors.New("DNS provider returned a record without an id")
		}
		byID[record.ID] = record
		byName[record.Name] = append(byName[record.Name], record)
	}
	ownedByKey := make(map[string]Ownership, len(owned))
	for _, ownership := range owned {
		if ownership.Provider != provider || ownership.ZoneID != request.ZoneID || ownership.OwnerKind != request.OwnerKind || ownership.OwnerID != request.OwnerID {
			return Plan{}, errors.New("DNS ownership store returned a row outside the requested owner")
		}
		ownedByKey[ownership.key()] = ownership
	}
	desiredByKey := make(map[string]DesiredRecord, len(request.Desired))
	for _, desired := range request.Desired {
		key := recordKey(desired.Type, desired.Name)
		desiredByKey[key] = desired
		ownership, isOwned := ownedByKey[key]
		if !isOwned {
			if conflict := unmanagedConflict(desired, byName[desired.Name]); conflict != "" {
				plan.Actions = append(plan.Actions, actionFor(ActionConflict, desired, nil, "", conflict))
				continue
			}
			plan.Actions = append(plan.Actions, actionFor(ActionCreate, desired, nil, "", ""))
			continue
		}
		providerRecord, exists := byID[ownership.ProviderRecordID]
		if !exists {
			if conflict := unmanagedConflict(desired, byName[desired.Name]); conflict != "" {
				plan.Actions = append(plan.Actions, actionFor(ActionConflict, desired, &ownership, ownership.ProviderRecordID, "owned provider record is absent; "+conflict))
				continue
			}
			plan.Actions = append(plan.Actions, actionFor(ActionRecreate, desired, &ownership, ownership.ProviderRecordID, "owned provider record is absent"))
			continue
		}
		if providerRecord.Type != ownership.Type || providerRecord.Name != ownership.Name {
			plan.Actions = append(plan.Actions, actionFor(ActionConflict, desired, &ownership, providerRecord.ID, "owned provider id now identifies a different record"))
			continue
		}
		if contentDigest(providerRecord.Content) != ownership.ContentSHA256 {
			plan.Actions = append(plan.Actions, actionFor(ActionConflict, desired, &ownership, providerRecord.ID, "owned record changed outside Midnight"))
			continue
		}
		if providerRecord.Content == desired.Content && providerRecord.TTL == desired.TTL {
			plan.Actions = append(plan.Actions, actionFor(ActionNoop, desired, &ownership, providerRecord.ID, ""))
			continue
		}
		plan.Actions = append(plan.Actions, actionFor(ActionUpdate, desired, &ownership, providerRecord.ID, ""))
	}
	for key, ownership := range ownedByKey {
		if _, exists := desiredByKey[key]; exists {
			continue
		}
		desired := DesiredRecord{Type: ownership.Type, Name: ownership.Name, TTL: ownership.TTL}
		providerRecord, exists := byID[ownership.ProviderRecordID]
		if !exists {
			plan.Actions = append(plan.Actions, actionFor(ActionForget, desired, &ownership, ownership.ProviderRecordID, "provider record is already absent"))
			continue
		}
		if providerRecord.Type != ownership.Type || providerRecord.Name != ownership.Name || contentDigest(providerRecord.Content) != ownership.ContentSHA256 {
			plan.Actions = append(plan.Actions, actionFor(ActionConflict, desired, &ownership, providerRecord.ID, "owned provider record changed outside Midnight; deletion refused"))
			continue
		}
		plan.Actions = append(plan.Actions, actionFor(ActionDelete, desired, &ownership, providerRecord.ID, ""))
	}
	sort.SliceStable(plan.Actions, func(i, j int) bool {
		left := recordKey(plan.Actions[i].Type, plan.Actions[i].Name)
		right := recordKey(plan.Actions[j].Type, plan.Actions[j].Name)
		if left == right {
			return plan.Actions[i].Kind < plan.Actions[j].Kind
		}
		return left < right
	})
	return plan, nil
}

func unmanagedConflict(desired DesiredRecord, records []Record) string {
	for _, record := range records {
		if record.Type == TypeNS {
			return "unmanaged NS delegation exists at the requested name"
		}
		if desired.Type == TypeTXT {
			if record.Type == TypeTXT && record.Content == desired.Content {
				return "matching TXT record exists without Midnight ownership"
			}
			continue
		}
		if desired.Type == TypeCNAME && (record.Type == TypeA || record.Type == TypeAAAA || record.Type == TypeCNAME) {
			return "unmanaged address or CNAME record exists at the requested name"
		}
		if (desired.Type == TypeA || desired.Type == TypeAAAA) && (record.Type == TypeCNAME || record.Type == desired.Type) {
			return "unmanaged same-type or CNAME record exists at the requested name"
		}
	}
	return ""
}

func normalizeObserved(record Record, zoneID string) (Record, error) {
	desired, err := normalizeDesired(DesiredRecord{Type: record.Type, Name: record.Name, Content: record.Content, TTL: record.TTL})
	if err != nil {
		return Record{}, fmt.Errorf("DNS provider returned invalid record metadata: %w", err)
	}
	record.ID = strings.TrimSpace(record.ID)
	record.ZoneID = zoneID
	record.Type = desired.Type
	record.Name = desired.Name
	record.Content = desired.Content
	record.TTL = desired.TTL
	return record, nil
}

func actionFor(kind ActionKind, desired DesiredRecord, ownership *Ownership, providerRecordID, reason string) Action {
	digest := contentDigest(desired.Content)
	if desired.Content == "" && ownership != nil {
		digest = ownership.ContentSHA256
	}
	return Action{Kind: kind, Type: desired.Type, Name: desired.Name, ContentSHA256: digest, ProviderRecordID: providerRecordID, Reason: reason, Desired: desired, Ownership: ownership}
}

func validateMutationResult(record Record, zoneID string, desired DesiredRecord) (Record, error) {
	record, err := normalizeObserved(record, zoneID)
	if err != nil {
		return Record{}, errors.New("DNS provider returned invalid mutation evidence")
	}
	if record.ID == "" || record.Type != desired.Type || record.Name != desired.Name || record.Content != desired.Content || record.TTL != desired.TTL {
		return Record{}, errors.New("DNS provider returned mutation evidence that does not match desired state")
	}
	return record, nil
}

func newOwnership(provider string, request Request, record Record, now time.Time) Ownership {
	return Ownership{Provider: provider, ZoneID: request.ZoneID, OwnerKind: request.OwnerKind, OwnerID: request.OwnerID, Scope: request.Scope, ProviderRecordID: record.ID, Type: record.Type, Name: record.Name, ContentSHA256: contentDigest(record.Content), TTL: record.TTL, CreatedAt: now, UpdatedAt: now}
}

func auditEventForAction(request Request, action Action) AuditEvent {
	return AuditEvent{ZoneID: request.ZoneID, OwnerKind: request.OwnerKind, OwnerID: request.OwnerID, Scope: request.Scope, RecordType: action.Type, RecordName: action.Name, ContentSHA256: action.ContentSHA256, ProviderRecordID: action.ProviderRecordID, DryRun: request.DryRun, Reason: action.Reason}
}

func (r *Reconciler) audit(ctx context.Context, request Request, event AuditEvent) error {
	if r.Audit == nil {
		if request.DryRun {
			return nil
		}
		return errors.New("DNS audit sink is required")
	}
	event.Provider = strings.ToLower(strings.TrimSpace(r.Provider.Name()))
	event.ZoneID = request.ZoneID
	event.OwnerKind = request.OwnerKind
	event.OwnerID = request.OwnerID
	event.Scope = request.Scope
	event.At = r.now()
	return r.Audit.RecordDNS(ctx, event)
}

func (r *Reconciler) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) lockOwner(key string) func() {
	r.lockMu.Lock()
	if r.locks == nil {
		r.locks = make(map[string]*ownerLock)
	}
	lock := r.locks[key]
	if lock == nil {
		lock = &ownerLock{}
		r.locks[key] = lock
	}
	lock.refs++
	r.lockMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		r.lockMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.locks, key)
		}
		r.lockMu.Unlock()
	}
}

func planSummary(plan Plan) string {
	counts := make(map[ActionKind]int)
	for _, action := range plan.Actions {
		counts[action.Kind]++
	}
	parts := make([]string, 0, len(counts))
	for kind, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
