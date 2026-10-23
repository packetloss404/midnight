// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/audit"
)

// ChainAuditSink records DNS actions in Midnight's integrity-protected audit
// chain. Provider credentials and plaintext record content are never included.
type ChainAuditSink struct {
	Chain     audit.AuditChain
	ActorID   string
	RequestID string
}

func (s ChainAuditSink) RecordDNS(ctx context.Context, event AuditEvent) error {
	if s.Chain == nil {
		return errors.New("DNS audit chain is required")
	}
	attrs := map[string]any{
		"provider":       event.Provider,
		"zone_id":        event.ZoneID,
		"record_type":    string(event.RecordType),
		"record_name":    event.RecordName,
		"content_sha256": event.ContentSHA256,
		"dry_run":        event.DryRun,
	}
	if event.ProviderRecordID != "" {
		attrs["provider_record_id"] = event.ProviderRecordID
	}
	if event.Reason != "" {
		attrs["reason"] = event.Reason
	}
	requestID := strings.TrimSpace(s.RequestID)
	if requestID == "" {
		requestID = "dns-reconciler"
	}
	actorID := strings.TrimSpace(s.ActorID)
	if actorID == "" {
		actorID = "dns-reconciler"
	}
	_, err := s.Chain.Append(ctx, audit.Row{
		RequestID:     requestID,
		OccurredAt:    event.At,
		RecordedAt:    event.At,
		ActorID:       actorID,
		ActorType:     "system",
		WorkspaceID:   event.Scope.WorkspaceID,
		ProjectID:     event.Scope.ProjectID,
		EnvironmentID: event.Scope.EnvironmentID,
		ResourceKind:  "dns_record_owner",
		ResourceID:    event.OwnerID,
		Action:        event.Action,
		Attrs:         attrs,
	})
	return err
}
