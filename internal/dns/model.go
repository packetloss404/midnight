// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package dns provides provider-neutral desired-state DNS reconciliation.
// Provider credentials and public reachability evidence deliberately remain
// outside its persistence model.
package dns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

var (
	ErrInvalidRequest = errors.New("dns reconciliation request is invalid")
	ErrConflict       = errors.New("dns reconciliation conflicts with provider state")
	ErrNotFound       = errors.New("dns provider record not found")
)

// RecordType is the deliberately small record set Midnight can own.
type RecordType string

const (
	TypeA     RecordType = "A"
	TypeAAAA  RecordType = "AAAA"
	TypeCNAME RecordType = "CNAME"
	TypeTXT   RecordType = "TXT"
	// TypeNS is observation-only. Midnight never creates or owns NS records,
	// but an unmanaged delegation must block conflicting writes.
	TypeNS RecordType = "NS"
)

func (t RecordType) valid() bool {
	switch t {
	case TypeA, TypeAAAA, TypeCNAME, TypeTXT:
		return true
	default:
		return false
	}
}

// Record is the provider-observed representation used by the reconciler.
type Record struct {
	ID      string
	ZoneID  string
	Type    RecordType
	Name    string
	Content string
	TTL     int
}

// DesiredRecord is one record Midnight is asked to own for a resource.
type DesiredRecord struct {
	Type    RecordType `json:"type"`
	Name    string     `json:"name"`
	Content string     `json:"content"`
	TTL     int        `json:"ttl"`
}

// Scope carries tenant lineage into ownership and audit evidence. It is never
// sent to a DNS provider.
type Scope struct {
	WorkspaceID   string
	ProjectID     string
	EnvironmentID string
}

// Request defines the complete desired set for one resource owner. Records
// omitted from Desired are eligible for deletion only when durable ownership
// and unchanged provider identity both prove that Midnight created them.
type Request struct {
	ZoneID    string
	OwnerKind string
	OwnerID   string
	Scope     Scope
	Desired   []DesiredRecord
	DryRun    bool
}

// Ownership is the durable proof that permits later updates and deletion.
// Content is represented only by a digest so ownership state and audit rows do
// not retain TXT challenges or other provider values.
type Ownership struct {
	Provider         string
	ZoneID           string
	OwnerKind        string
	OwnerID          string
	Scope            Scope
	ProviderRecordID string
	Type             RecordType
	Name             string
	ContentSHA256    string
	TTL              int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (o Ownership) key() string { return recordKey(o.Type, o.Name) }

// Provider is the external DNS boundary. Implementations must return complete
// zone listings and must not expose credentials in errors.
type Provider interface {
	Name() string
	ListRecords(context.Context, string) ([]Record, error)
	CreateRecord(context.Context, string, DesiredRecord) (Record, error)
	UpdateRecord(context.Context, string, string, DesiredRecord) (Record, error)
	DeleteRecord(context.Context, string, string) error
}

// OwnershipStore persists only Midnight's authority evidence, never provider
// credentials or plaintext record content.
type OwnershipStore interface {
	ListOwnerships(context.Context, string, string, string, string) ([]Ownership, error)
	PutOwnership(context.Context, Ownership) error
	DeleteOwnership(context.Context, string, string, string, string, RecordType, string) error
}

// AuditEvent is a redaction-safe record of planning or mutation intent.
type AuditEvent struct {
	Action           string
	Provider         string
	ZoneID           string
	OwnerKind        string
	OwnerID          string
	Scope            Scope
	RecordType       RecordType
	RecordName       string
	ContentSHA256    string
	ProviderRecordID string
	DryRun           bool
	Reason           string
	At               time.Time
}

type AuditSink interface {
	RecordDNS(context.Context, AuditEvent) error
}

type AuditSinkFunc func(context.Context, AuditEvent) error

func (f AuditSinkFunc) RecordDNS(ctx context.Context, event AuditEvent) error {
	return f(ctx, event)
}

func normalizeRequest(request Request) (Request, error) {
	request.Desired = append([]DesiredRecord(nil), request.Desired...)
	request.ZoneID = strings.TrimSpace(request.ZoneID)
	request.OwnerKind = strings.ToLower(strings.TrimSpace(request.OwnerKind))
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	request.Scope.WorkspaceID = strings.TrimSpace(request.Scope.WorkspaceID)
	request.Scope.ProjectID = strings.TrimSpace(request.Scope.ProjectID)
	request.Scope.EnvironmentID = strings.TrimSpace(request.Scope.EnvironmentID)
	if request.ZoneID == "" || request.OwnerKind == "" || request.OwnerID == "" {
		return Request{}, fmt.Errorf("%w: zone, owner kind, and owner id are required", ErrInvalidRequest)
	}
	if len(request.ZoneID) > 255 || len(request.OwnerKind) > 64 || len(request.OwnerID) > 255 {
		return Request{}, fmt.Errorf("%w: ownership identity exceeds its limit", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(request.Desired))
	for index := range request.Desired {
		record, err := normalizeDesired(request.Desired[index])
		if err != nil {
			return Request{}, fmt.Errorf("desired record %d: %w", index, err)
		}
		key := recordKey(record.Type, record.Name)
		if _, exists := seen[key]; exists {
			return Request{}, fmt.Errorf("%w: duplicate desired %s record for %s", ErrInvalidRequest, record.Type, record.Name)
		}
		seen[key] = struct{}{}
		request.Desired[index] = record
	}
	sort.Slice(request.Desired, func(i, j int) bool {
		return recordKey(request.Desired[i].Type, request.Desired[i].Name) < recordKey(request.Desired[j].Type, request.Desired[j].Name)
	})
	return request, nil
}

func normalizeDesired(record DesiredRecord) (DesiredRecord, error) {
	record.Type = RecordType(strings.ToUpper(strings.TrimSpace(string(record.Type))))
	if !record.Type.valid() {
		return DesiredRecord{}, fmt.Errorf("%w: record type must be A, AAAA, CNAME, or TXT", ErrInvalidRequest)
	}
	name, err := normalizeRecordName(record.Name, record.Type == TypeTXT)
	if err != nil {
		return DesiredRecord{}, err
	}
	record.Name = name
	record.Content = strings.TrimSpace(record.Content)
	if record.Content == "" || len(record.Content) > 4096 {
		return DesiredRecord{}, fmt.Errorf("%w: record content is empty or too large", ErrInvalidRequest)
	}
	switch record.Type {
	case TypeA:
		address, parseErr := netip.ParseAddr(record.Content)
		if parseErr != nil || !address.Is4() {
			return DesiredRecord{}, fmt.Errorf("%w: A content must be an IPv4 address", ErrInvalidRequest)
		}
		record.Content = address.String()
	case TypeAAAA:
		address, parseErr := netip.ParseAddr(record.Content)
		if parseErr != nil || !address.Is6() || address.Is4In6() {
			return DesiredRecord{}, fmt.Errorf("%w: AAAA content must be an IPv6 address", ErrInvalidRequest)
		}
		record.Content = address.String()
	case TypeCNAME:
		target, targetErr := normalizeRecordName(record.Content, false)
		if targetErr != nil {
			return DesiredRecord{}, fmt.Errorf("%w: invalid CNAME target", ErrInvalidRequest)
		}
		record.Content = target
	}
	if record.TTL == 0 {
		record.TTL = 1
	}
	if record.TTL != 1 && (record.TTL < 60 || record.TTL > 86400) {
		return DesiredRecord{}, fmt.Errorf("%w: ttl must be provider-auto (1) or 60..86400 seconds", ErrInvalidRequest)
	}
	return record, nil
}

func normalizeRecordName(value string, allowUnderscore bool) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " /:@") {
		return "", fmt.Errorf("%w: invalid DNS record name", ErrInvalidRequest)
	}
	labels := strings.Split(value, ".")
	for index, original := range labels {
		label := original
		underscoreCount := 0
		if allowUnderscore {
			label = strings.TrimLeft(original, "_")
			underscoreCount = len(original) - len(label)
		}
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("%w: invalid DNS record name", ErrInvalidRequest)
		}
		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil || ascii == "" || len(ascii) > 63 || ascii[0] == '-' || ascii[len(ascii)-1] == '-' {
			return "", fmt.Errorf("%w: invalid DNS record name", ErrInvalidRequest)
		}
		for _, char := range ascii {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", fmt.Errorf("%w: invalid DNS record name", ErrInvalidRequest)
			}
		}
		labels[index] = strings.Repeat("_", underscoreCount) + ascii
	}
	return strings.Join(labels, "."), nil
}

func recordKey(recordType RecordType, name string) string {
	return string(recordType) + "\x00" + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func contentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
