// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
)

type PostgresCommunityForkStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewPostgresCommunityForkStore(pool *pg.Pool) *PostgresCommunityForkStore {
	return &PostgresCommunityForkStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresCommunityForkStore) CreateFork(ctx context.Context, fork CommunityFork) (CommunityFork, error) {
	if err := s.ready(); err != nil {
		return CommunityFork{}, err
	}
	upstream, draft, provenance, findings, err := marshalForkDocuments(fork)
	if err != nil {
		return CommunityFork{}, err
	}
	now := s.now()
	fork.TrustClass = "local"
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO community_template_forks (
			id,project_id,trust_class,upstream_manifest,draft_manifest,provenance,
			readme,validation_findings,is_valid,resource_version,created_at,updated_at
		) VALUES ($1,$2,'local',$3,$4,$5,$6,$7,$8,1,$9,$9)
		RETURNING resource_version,created_at,updated_at`,
		fork.ID, fork.ProjectID, upstream, draft, provenance, fork.Readme, findings, fork.Valid, now,
	).Scan(&fork.ResourceVersion, &fork.CreatedAt, &fork.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CommunityFork{}, ErrCommunityForkConflict
		}
		return CommunityFork{}, fmt.Errorf("community template fork create: %w", err)
	}
	return fork, nil
}

func (s *PostgresCommunityForkStore) ListForks(ctx context.Context, projectID string) ([]CommunityFork, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, communityForkSelect+`
		WHERE project_id=$1
		ORDER BY updated_at DESC,id ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("community template fork list: %w", err)
	}
	defer rows.Close()
	forks := make([]CommunityFork, 0)
	for rows.Next() {
		fork, err := scanCommunityFork(rows)
		if err != nil {
			return nil, err
		}
		forks = append(forks, fork)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("community template fork list: %w", err)
	}
	return forks, nil
}

func (s *PostgresCommunityForkStore) GetFork(ctx context.Context, projectID, id string) (CommunityFork, error) {
	if err := s.ready(); err != nil {
		return CommunityFork{}, err
	}
	fork, err := scanCommunityFork(s.Pool.QueryRow(ctx, communityForkSelect+` WHERE project_id=$1 AND id=$2`, strings.TrimSpace(projectID), strings.TrimSpace(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return CommunityFork{}, ErrCommunityForkNotFound
	}
	if err != nil {
		return CommunityFork{}, err
	}
	return fork, nil
}

func (s *PostgresCommunityForkStore) UpdateFork(ctx context.Context, projectID, id string, expectedVersion int64, draft CommunityManifest, findings []ValidationFinding) (CommunityFork, error) {
	if err := s.ready(); err != nil {
		return CommunityFork{}, err
	}
	draftJSON, err := json.Marshal(draft)
	if err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork draft encode: %w", err)
	}
	findingsJSON, err := json.Marshal(normalizeFindings(findings))
	if err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork findings encode: %w", err)
	}
	now := s.now()
	fork, err := scanCommunityFork(s.Pool.QueryRow(ctx, `WITH updated AS (
		UPDATE community_template_forks
		   SET draft_manifest=$4,validation_findings=$5,is_valid=$6,
		       resource_version=resource_version+1,updated_at=$7
		 WHERE project_id=$1 AND id=$2 AND resource_version=$3
		 RETURNING *
	) SELECT id,project_id,trust_class,upstream_manifest,draft_manifest,provenance,
	         readme,validation_findings,is_valid,resource_version,created_at,updated_at
	    FROM updated`, strings.TrimSpace(projectID), strings.TrimSpace(id), expectedVersion, draftJSON, findingsJSON, !hasErrors(findings), now))
	if err == nil {
		return fork, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CommunityFork{}, err
	}
	var exists bool
	if lookupErr := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community_template_forks WHERE project_id=$1 AND id=$2)`, strings.TrimSpace(projectID), strings.TrimSpace(id)).Scan(&exists); lookupErr != nil {
		return CommunityFork{}, fmt.Errorf("community template fork conflict lookup: %w", lookupErr)
	}
	if !exists {
		return CommunityFork{}, ErrCommunityForkNotFound
	}
	return CommunityFork{}, ErrCommunityForkConflict
}

const communityForkSelect = `
	SELECT id,project_id,trust_class,upstream_manifest,draft_manifest,provenance,
	       readme,validation_findings,is_valid,resource_version,created_at,updated_at
	  FROM community_template_forks`

type communityForkScanner interface {
	Scan(...any) error
}

func scanCommunityFork(row communityForkScanner) (CommunityFork, error) {
	var fork CommunityFork
	var upstream, draft, provenance, findings []byte
	err := row.Scan(
		&fork.ID, &fork.ProjectID, &fork.TrustClass, &upstream, &draft, &provenance,
		&fork.Readme, &findings, &fork.Valid, &fork.ResourceVersion, &fork.CreatedAt, &fork.UpdatedAt,
	)
	if err != nil {
		return CommunityFork{}, err
	}
	if err := json.Unmarshal(upstream, &fork.UpstreamManifest); err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork upstream decode: %w", err)
	}
	if err := json.Unmarshal(draft, &fork.Draft); err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork draft decode: %w", err)
	}
	if err := json.Unmarshal(provenance, &fork.Provenance); err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork provenance decode: %w", err)
	}
	if err := json.Unmarshal(findings, &fork.Findings); err != nil {
		return CommunityFork{}, fmt.Errorf("community template fork findings decode: %w", err)
	}
	fork.Findings = normalizeFindings(fork.Findings)
	return fork, nil
}

func marshalForkDocuments(fork CommunityFork) ([]byte, []byte, []byte, []byte, error) {
	upstream, err := json.Marshal(fork.UpstreamManifest)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("community template fork upstream encode: %w", err)
	}
	draft, err := json.Marshal(fork.Draft)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("community template fork draft encode: %w", err)
	}
	provenance, err := json.Marshal(fork.Provenance)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("community template fork provenance encode: %w", err)
	}
	findings, err := json.Marshal(normalizeFindings(fork.Findings))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("community template fork findings encode: %w", err)
	}
	return upstream, draft, provenance, findings, nil
}

func (s *PostgresCommunityForkStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("community template fork store: pool is required")
	}
	return nil
}

func (s *PostgresCommunityForkStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
