// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
)

type PostgresOwnershipStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewPostgresOwnershipStore(pool *pg.Pool) *PostgresOwnershipStore {
	return &PostgresOwnershipStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresOwnershipStore) ListOwnerships(ctx context.Context, provider, zoneID, ownerKind, ownerID string) ([]Ownership, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("DNS ownership store is not initialized")
	}
	rows, err := s.Pool.Query(ctx, `
SELECT r.provider,r.zone_id,r.owner_kind,r.owner_id,
       o.workspace_id,o.project_id,o.environment_id,
       r.provider_record_id,r.record_type,r.record_name,r.content_sha256,r.ttl,
       r.created_at,r.updated_at
  FROM dns_record_ownerships r
  JOIN dns_record_owners o
    ON o.provider=r.provider AND o.zone_id=r.zone_id
   AND o.owner_kind=r.owner_kind AND o.owner_id=r.owner_id
 WHERE r.provider=$1 AND r.zone_id=$2 AND r.owner_kind=$3 AND r.owner_id=$4
 ORDER BY r.record_type,r.record_name`,
		strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(zoneID),
		strings.ToLower(strings.TrimSpace(ownerKind)), strings.TrimSpace(ownerID),
	)
	if err != nil {
		return nil, fmt.Errorf("list DNS record ownerships: %w", err)
	}
	defer rows.Close()
	out := []Ownership{}
	for rows.Next() {
		var ownership Ownership
		if err := rows.Scan(
			&ownership.Provider, &ownership.ZoneID, &ownership.OwnerKind, &ownership.OwnerID,
			&ownership.Scope.WorkspaceID, &ownership.Scope.ProjectID, &ownership.Scope.EnvironmentID,
			&ownership.ProviderRecordID, &ownership.Type, &ownership.Name, &ownership.ContentSHA256, &ownership.TTL,
			&ownership.CreatedAt, &ownership.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan DNS record ownership: %w", err)
		}
		ownership.CreatedAt = ownership.CreatedAt.UTC()
		ownership.UpdatedAt = ownership.UpdatedAt.UTC()
		out = append(out, ownership)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list DNS record ownerships: %w", err)
	}
	return out, nil
}

func (s *PostgresOwnershipStore) PutOwnership(ctx context.Context, ownership Ownership) error {
	if s == nil || s.Pool == nil {
		return errors.New("DNS ownership store is not initialized")
	}
	ownership, err := s.normalizeOwnership(ownership)
	if err != nil {
		return err
	}
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		tag, execErr := tx.Exec(ctx, `
INSERT INTO dns_record_owners(
    provider,zone_id,owner_kind,owner_id,
    workspace_id,project_id,environment_id,created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(provider,zone_id,owner_kind,owner_id)
DO UPDATE SET updated_at=GREATEST(dns_record_owners.updated_at,EXCLUDED.updated_at)
WHERE dns_record_owners.workspace_id=EXCLUDED.workspace_id
  AND dns_record_owners.project_id=EXCLUDED.project_id
  AND dns_record_owners.environment_id=EXCLUDED.environment_id`,
			ownership.Provider, ownership.ZoneID, ownership.OwnerKind, ownership.OwnerID,
			ownership.Scope.WorkspaceID, ownership.Scope.ProjectID, ownership.Scope.EnvironmentID,
			ownership.CreatedAt, ownership.UpdatedAt,
		)
		if execErr != nil {
			return fmt.Errorf("put DNS record owner: %w", execErr)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: DNS owner scope is immutable", ErrInvalidRequest)
		}
		_, execErr = tx.Exec(ctx, `
INSERT INTO dns_record_ownerships(
    provider,zone_id,owner_kind,owner_id,
    provider_record_id,record_type,record_name,content_sha256,ttl,
    created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT(provider,zone_id,owner_kind,owner_id,record_type,record_name)
DO UPDATE SET
    provider_record_id=EXCLUDED.provider_record_id,
    content_sha256=EXCLUDED.content_sha256,
    ttl=EXCLUDED.ttl,
    updated_at=EXCLUDED.updated_at`,
			ownership.Provider, ownership.ZoneID, ownership.OwnerKind, ownership.OwnerID,
			ownership.ProviderRecordID, ownership.Type, ownership.Name, ownership.ContentSHA256, ownership.TTL,
			ownership.CreatedAt, ownership.UpdatedAt,
		)
		if execErr != nil {
			return fmt.Errorf("put DNS record ownership: %w", execErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *PostgresOwnershipStore) DeleteOwnership(ctx context.Context, provider, zoneID, ownerKind, ownerID string, recordType RecordType, name string) error {
	if s == nil || s.Pool == nil {
		return errors.New("DNS ownership store is not initialized")
	}
	name, err := normalizeRecordName(name, recordType == TypeTXT)
	if err != nil || !recordType.valid() {
		return fmt.Errorf("%w: invalid ownership deletion identity", ErrInvalidRequest)
	}
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, execErr := tx.Exec(ctx, `
DELETE FROM dns_record_ownerships
 WHERE provider=$1 AND zone_id=$2 AND owner_kind=$3 AND owner_id=$4
   AND record_type=$5 AND record_name=$6`,
			strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(zoneID),
			strings.ToLower(strings.TrimSpace(ownerKind)), strings.TrimSpace(ownerID), recordType, name,
		); execErr != nil {
			return execErr
		}
		_, execErr := tx.Exec(ctx, `
DELETE FROM dns_record_owners o
 WHERE provider=$1 AND zone_id=$2 AND owner_kind=$3 AND owner_id=$4
   AND NOT EXISTS (
       SELECT 1 FROM dns_record_ownerships r
        WHERE r.provider=o.provider AND r.zone_id=o.zone_id
          AND r.owner_kind=o.owner_kind AND r.owner_id=o.owner_id
   )`, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(zoneID), strings.ToLower(strings.TrimSpace(ownerKind)), strings.TrimSpace(ownerID))
		return execErr
	})
	if err != nil {
		return fmt.Errorf("delete DNS record ownership: %w", err)
	}
	return nil
}

func (s *PostgresOwnershipStore) ListDomainIntents(ctx context.Context) ([]DomainIntent, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("DNS ownership store is not initialized")
	}
	rows, err := s.Pool.Query(ctx, `
SELECT d.id,p.workspace_id,d.project_id,d.environment_id,
       d.kind,d.normalized_hostname,d.apps_domain,
       COALESCE(dc.record_name,''),COALESCE(dc.record_value,'')
  FROM service_domain_bindings b
  JOIN domains d
    ON d.id=b.domain_id
   AND d.project_id=b.project_id
   AND d.environment_id=b.environment_id
   AND d.service_id=b.service_id
  JOIN projects p ON p.id=d.project_id
  JOIN environments e ON e.id=d.environment_id AND e.project_id=d.project_id
  JOIN services s ON s.id=d.service_id AND s.project_id=d.project_id
  LEFT JOIN domain_dns_challenges dc ON dc.domain_id=d.id
 WHERE p.deleted_at IS NULL AND e.deleted_at IS NULL AND s.deleted_at IS NULL
 ORDER BY d.id`)
	if err != nil {
		return nil, fmt.Errorf("list DNS domain intents: %w", err)
	}
	defer rows.Close()
	out := []DomainIntent{}
	for rows.Next() {
		var intent DomainIntent
		if err := rows.Scan(
			&intent.DomainID, &intent.Scope.WorkspaceID, &intent.Scope.ProjectID, &intent.Scope.EnvironmentID,
			&intent.Kind, &intent.Hostname, &intent.AppsDomain, &intent.TXTRecordName, &intent.TXTRecordValue,
		); err != nil {
			return nil, fmt.Errorf("scan DNS domain intent: %w", err)
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

func (s *PostgresOwnershipStore) ListManagedOwners(ctx context.Context, provider, zoneID string) ([]ManagedOwner, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("DNS ownership store is not initialized")
	}
	rows, err := s.Pool.Query(ctx, `
SELECT owner_kind,owner_id,workspace_id,project_id,environment_id
  FROM dns_record_owners
 WHERE provider=$1 AND zone_id=$2
 ORDER BY owner_kind,owner_id`, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(zoneID))
	if err != nil {
		return nil, fmt.Errorf("list DNS managed owners: %w", err)
	}
	defer rows.Close()
	out := []ManagedOwner{}
	for rows.Next() {
		var owner ManagedOwner
		if err := rows.Scan(&owner.OwnerKind, &owner.OwnerID, &owner.Scope.WorkspaceID, &owner.Scope.ProjectID, &owner.Scope.EnvironmentID); err != nil {
			return nil, fmt.Errorf("scan DNS managed owner: %w", err)
		}
		out = append(out, owner)
	}
	return out, rows.Err()
}

func (s *PostgresOwnershipStore) normalizeOwnership(ownership Ownership) (Ownership, error) {
	ownership.Provider = strings.ToLower(strings.TrimSpace(ownership.Provider))
	ownership.ZoneID = strings.TrimSpace(ownership.ZoneID)
	ownership.OwnerKind = strings.ToLower(strings.TrimSpace(ownership.OwnerKind))
	ownership.OwnerID = strings.TrimSpace(ownership.OwnerID)
	ownership.ProviderRecordID = strings.TrimSpace(ownership.ProviderRecordID)
	ownership.Scope.WorkspaceID = strings.TrimSpace(ownership.Scope.WorkspaceID)
	ownership.Scope.ProjectID = strings.TrimSpace(ownership.Scope.ProjectID)
	ownership.Scope.EnvironmentID = strings.TrimSpace(ownership.Scope.EnvironmentID)
	name, err := normalizeRecordName(ownership.Name, ownership.Type == TypeTXT)
	if err != nil {
		return Ownership{}, err
	}
	ownership.Name = name
	if ownership.Provider == "" || ownership.ZoneID == "" || ownership.OwnerKind == "" || ownership.OwnerID == "" ||
		ownership.ProviderRecordID == "" || ownership.Scope.WorkspaceID == "" || ownership.Scope.ProjectID == "" || ownership.Scope.EnvironmentID == "" ||
		!ownership.Type.valid() || (ownership.TTL != 1 && (ownership.TTL < 60 || ownership.TTL > 86400)) {
		return Ownership{}, fmt.Errorf("%w: incomplete DNS ownership", ErrInvalidRequest)
	}
	digest, err := hex.DecodeString(ownership.ContentSHA256)
	if err != nil || len(digest) != 32 || ownership.ContentSHA256 != strings.ToLower(ownership.ContentSHA256) {
		return Ownership{}, fmt.Errorf("%w: invalid DNS ownership content digest", ErrInvalidRequest)
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if ownership.CreatedAt.IsZero() {
		ownership.CreatedAt = now
	} else {
		ownership.CreatedAt = ownership.CreatedAt.UTC()
	}
	if ownership.UpdatedAt.IsZero() {
		ownership.UpdatedAt = now
	} else {
		ownership.UpdatedAt = ownership.UpdatedAt.UTC()
	}
	return ownership, nil
}
