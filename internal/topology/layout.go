// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package topology stores presentation-only project map layouts.
package topology

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
)

const (
	MaxNodes      = 500
	MaxCoordinate = 100_000
)

var (
	ErrNotFound        = errors.New("topology layout not found")
	ErrVersionConflict = errors.New("topology layout version conflict")
	ErrInvalid         = errors.New("invalid topology layout")
)

// Node is one service's position in logical canvas pixels.
type Node struct {
	ServiceID string `json:"service_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

// Layout is the durable project/environment presentation preference.
type Layout struct {
	WorkspaceID   string    `json:"workspace_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	Nodes         []Node    `json:"nodes"`
	Version       int64     `json:"resource_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store implements read and atomic compare-and-swap replacement.
type Store interface {
	Get(context.Context, string, string) (Layout, error)
	Replace(context.Context, Layout, int64) (Layout, error)
}

func Validate(nodes []Node) error {
	if len(nodes) > MaxNodes {
		return fmt.Errorf("%w: nodes exceeds %d", ErrInvalid, MaxNodes)
	}
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ServiceID)
		if id == "" || len(id) > 128 {
			return fmt.Errorf("%w: service_id is required and must be at most 128 characters", ErrInvalid)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate service_id", ErrInvalid)
		}
		seen[id] = struct{}{}
		if math.Abs(float64(node.X)) > MaxCoordinate || math.Abs(float64(node.Y)) > MaxCoordinate {
			return fmt.Errorf("%w: coordinates exceed %d", ErrInvalid, MaxCoordinate)
		}
	}
	return nil
}

// MemoryStore provides the same CAS contract as PostgreSQL for tests/dev.
type MemoryStore struct {
	mu      sync.RWMutex
	clock   func() time.Time
	layouts map[string]Layout
}

func NewMemoryStore(clock func() time.Time) *MemoryStore {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryStore{clock: clock, layouts: make(map[string]Layout)}
}

func layoutKey(projectID, environmentID string) string { return projectID + "\x00" + environmentID }

func clone(layout Layout) Layout {
	layout.Nodes = append([]Node(nil), layout.Nodes...)
	return layout
}

func (s *MemoryStore) Get(ctx context.Context, projectID, environmentID string) (Layout, error) {
	if err := ctx.Err(); err != nil {
		return Layout{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	layout, ok := s.layouts[layoutKey(projectID, environmentID)]
	if !ok {
		return Layout{}, ErrNotFound
	}
	return clone(layout), nil
}

func (s *MemoryStore) Replace(ctx context.Context, layout Layout, expected int64) (Layout, error) {
	if err := ctx.Err(); err != nil {
		return Layout{}, err
	}
	if expected < 0 || layout.WorkspaceID == "" || layout.ProjectID == "" || layout.EnvironmentID == "" {
		return Layout{}, ErrInvalid
	}
	if err := Validate(layout.Nodes); err != nil {
		return Layout{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := layoutKey(layout.ProjectID, layout.EnvironmentID)
	current, exists := s.layouts[key]
	if (!exists && expected != 0) || (exists && current.Version != expected) {
		return Layout{}, ErrVersionConflict
	}
	now := s.clock().UTC()
	if exists {
		layout.CreatedAt = current.CreatedAt
	} else {
		layout.CreatedAt = now
	}
	layout.Version = expected + 1
	layout.UpdatedAt = now
	s.layouts[key] = clone(layout)
	return clone(layout), nil
}

// PostgresStore persists topology layouts with atomic compare-and-swap.
type PostgresStore struct{ Pool *pg.Pool }

func NewPostgresStore(pool *pg.Pool) *PostgresStore { return &PostgresStore{Pool: pool} }

func (s *PostgresStore) Get(ctx context.Context, projectID, environmentID string) (Layout, error) {
	var layout Layout
	var nodes []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT workspace_id, project_id, environment_id, nodes, resource_version, created_at, updated_at
		FROM topology_layouts WHERE project_id=$1 AND environment_id=$2`, projectID, environmentID).
		Scan(&layout.WorkspaceID, &layout.ProjectID, &layout.EnvironmentID, &nodes, &layout.Version, &layout.CreatedAt, &layout.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Layout{}, ErrNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("get topology layout: %w", err)
	}
	if err := unmarshalNodes(nodes, &layout.Nodes); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

func (s *PostgresStore) Replace(ctx context.Context, layout Layout, expected int64) (Layout, error) {
	if s == nil || s.Pool == nil || expected < 0 || layout.WorkspaceID == "" || layout.ProjectID == "" || layout.EnvironmentID == "" {
		return Layout{}, ErrInvalid
	}
	if err := Validate(layout.Nodes); err != nil {
		return Layout{}, err
	}
	nodes, err := marshalNodes(layout.Nodes)
	if err != nil {
		return Layout{}, err
	}
	var out Layout
	var outNodes []byte
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		// The handler performs the same validation for a useful client error, but
		// only the store can keep active-service validation and layout CAS inside
		// one transaction. Row locks prevent a concurrent soft/hard deletion from
		// turning a just-accepted node into stale durable state.
		if len(layout.Nodes) > 0 {
			ids := make([]string, 0, len(layout.Nodes))
			for _, node := range layout.Nodes {
				ids = append(ids, node.ServiceID)
			}
			rows, queryErr := tx.Query(ctx, `SELECT id FROM services
 WHERE project_id=$1 AND id=ANY($2::text[]) AND deleted_at IS NULL FOR SHARE`, layout.ProjectID, ids)
			if queryErr != nil {
				return queryErr
			}
			count := 0
			for rows.Next() {
				count++
			}
			queryErr = rows.Err()
			rows.Close()
			if queryErr != nil {
				return queryErr
			}
			if count != len(ids) {
				return ErrInvalid
			}
		}
		if expected == 0 {
			return tx.QueryRow(ctx, `
			INSERT INTO topology_layouts (workspace_id, project_id, environment_id, nodes, resource_version)
			VALUES ($1, $2, $3, $4::jsonb, 1)
			ON CONFLICT (project_id, environment_id) DO NOTHING
			RETURNING workspace_id, project_id, environment_id, nodes, resource_version, created_at, updated_at`,
				layout.WorkspaceID, layout.ProjectID, layout.EnvironmentID, nodes).
				Scan(&out.WorkspaceID, &out.ProjectID, &out.EnvironmentID, &outNodes, &out.Version, &out.CreatedAt, &out.UpdatedAt)
		}
		return tx.QueryRow(ctx, `
			UPDATE topology_layouts
			SET nodes=$4::jsonb, updated_at=CURRENT_TIMESTAMP, resource_version=resource_version+1
			WHERE workspace_id=$1 AND project_id=$2 AND environment_id=$3 AND resource_version=$5
			RETURNING workspace_id, project_id, environment_id, nodes, resource_version, created_at, updated_at`,
			layout.WorkspaceID, layout.ProjectID, layout.EnvironmentID, nodes, expected).
			Scan(&out.WorkspaceID, &out.ProjectID, &out.EnvironmentID, &outNodes, &out.Version, &out.CreatedAt, &out.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Layout{}, ErrVersionConflict
	}
	if errors.Is(err, ErrInvalid) {
		return Layout{}, ErrInvalid
	}
	if err != nil {
		return Layout{}, fmt.Errorf("replace topology layout: %w", err)
	}
	if err := unmarshalNodes(outNodes, &out.Nodes); err != nil {
		return Layout{}, err
	}
	return out, nil
}
