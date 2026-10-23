// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresEnvironmentResourceEvidenceStore batches the durable source,
// replica, and verified public-route evidence needed by the environment
// workspace. It deliberately does not read live Docker state.
type PostgresEnvironmentResourceEvidenceStore struct {
	Pool *pg.Pool
}

func NewPostgresEnvironmentResourceEvidenceStore(pool *pg.Pool) *PostgresEnvironmentResourceEvidenceStore {
	return &PostgresEnvironmentResourceEvidenceStore{Pool: pool}
}

func (s *PostgresEnvironmentResourceEvidenceStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("environment resource evidence store: pool required")
	}
	return nil
}

func (s *PostgresEnvironmentResourceEvidenceStore) ListPersistedReplicasByEnvironment(
	ctx context.Context,
	projectID, environmentID string,
) ([]PersistedReplicaEvidence, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.project_id, d.environment_id, d.service_id, i.deployment_id,
		       COUNT(*)::integer,
		       COUNT(*) FILTER (WHERE i.ready)::integer,
		       COUNT(*) FILTER (WHERE i.observed_state='running')::integer,
		       MAX(i.updated_at)
		  FROM deployment_instances i
		  JOIN deployments d ON d.id=i.deployment_id
		 WHERE d.project_id=$1 AND d.environment_id=$2
		 GROUP BY d.project_id, d.environment_id, d.service_id, i.deployment_id
		 ORDER BY d.service_id, i.deployment_id`,
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
	)
	if err != nil {
		return nil, fmt.Errorf("environment replica evidence: %w", err)
	}
	defer rows.Close()
	out := make([]PersistedReplicaEvidence, 0)
	for rows.Next() {
		var item PersistedReplicaEvidence
		if err := rows.Scan(
			&item.ProjectID,
			&item.EnvironmentID,
			&item.ServiceID,
			&item.DeploymentID,
			&item.DesiredReplicas,
			&item.ReadyReplicas,
			&item.RunningReplicas,
			&item.EvidenceAt,
		); err != nil {
			return nil, fmt.Errorf("environment replica evidence scan: %w", err)
		}
		item.EvidenceAt = item.EvidenceAt.UTC()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("environment replica evidence rows: %w", err)
	}
	return out, nil
}

func (s *PostgresEnvironmentResourceEvidenceStore) ListPersistedEndpointsByEnvironment(
	ctx context.Context,
	projectID, environmentID string,
) ([]PersistedEndpointEvidence, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (d.service_id, d.normalized_hostname, b.deployment_id)
		       d.project_id, d.environment_id, d.service_id, b.deployment_id,
		       d.normalized_hostname, r.path_prefix,
		       (d.tls_state='ready') AS tls, o.observed_at
		  FROM route_backends b
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		  JOIN domains d
		    ON d.id=r.domain_id
		   AND d.project_id=r.project_id
		   AND d.environment_id=r.environment_id
		   AND d.service_id=r.service_id
		  JOIN route_controller_groups g
		    ON g.project_id=d.project_id AND g.environment_id=d.environment_id
		  JOIN route_publication_observations o
		    ON o.project_id=g.project_id
		   AND o.environment_id=g.environment_id
		   AND o.desired_revision=g.desired_revision
		   AND o.artifact_hash=g.artifact_hash
		 WHERE d.project_id=$1
		   AND d.environment_id=$2
		   AND r.public_enabled=TRUE
		   AND b.ready=TRUE
		   AND g.apply_state='ready'
		   AND g.desired_revision=g.observed_revision
		   AND g.desired_revision=g.applied_revision
		   AND o.state='ready'
		   AND o.traefik_acknowledged=TRUE
		   AND o.ingress_probe_succeeded=TRUE
		 ORDER BY d.service_id, d.normalized_hostname, b.deployment_id, o.observed_at DESC`,
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
	)
	if err != nil {
		return nil, fmt.Errorf("environment endpoint evidence: %w", err)
	}
	defer rows.Close()
	out := make([]PersistedEndpointEvidence, 0)
	for rows.Next() {
		var item PersistedEndpointEvidence
		var path string
		if err := rows.Scan(
			&item.ProjectID,
			&item.EnvironmentID,
			&item.ServiceID,
			&item.DeploymentID,
			&item.Host,
			&path,
			&item.TLS,
			&item.EvidenceAt,
		); err != nil {
			return nil, fmt.Errorf("environment endpoint evidence scan: %w", err)
		}
		scheme := "http://"
		if item.TLS {
			scheme = "https://"
		}
		item.URL = scheme + item.Host
		if path = strings.TrimSpace(path); path != "" && path != "/" {
			item.URL += path
		}
		item.EvidenceAt = item.EvidenceAt.UTC()
		item.Persisted = true
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("environment endpoint evidence rows: %w", err)
	}
	return out, nil
}

func (s *PostgresEnvironmentResourceEvidenceStore) ListPersistedSourcesByProject(
	ctx context.Context,
	workspaceID, projectID string,
) ([]PersistedSourceEvidence, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT link.project_id, link.service_id, link.repository_full_name,
		       link.branch, link.watch_paths, link.root_directory,
		       link.config_path, link.wait_for_ci, link.required_checks,
		       link.auto_deploy,
		       repository.repository_access_verified_at, link.updated_at
		  FROM service_github_links link
		  JOIN github_workspace_repository_grants repository
		    ON repository.workspace_id=link.workspace_id
		   AND repository.installation_id=link.installation_id
		   AND repository.repository_id=link.repository_id
		   AND repository.repository_full_name=link.repository_full_name
		  JOIN github_workspace_installation_grants installation
		    ON installation.workspace_id=repository.workspace_id
		   AND installation.installation_id=repository.installation_id
		 WHERE link.workspace_id=$1 AND link.project_id=$2
		 ORDER BY link.service_id`,
		strings.TrimSpace(workspaceID),
		strings.TrimSpace(projectID),
	)
	if err != nil {
		return nil, fmt.Errorf("environment source evidence: %w", err)
	}
	defer rows.Close()
	out := make([]PersistedSourceEvidence, 0)
	for rows.Next() {
		item := PersistedSourceEvidence{
			Type:        "github",
			TruthSource: "canonical-github-link",
		}
		if err := rows.Scan(
			&item.ProjectID,
			&item.ServiceID,
			&item.Repository,
			&item.Ref,
			&item.WatchPaths,
			&item.RootDir,
			&item.ConfigPath,
			&item.WaitForCI,
			&item.RequiredChecks,
			&item.AutoDeploy,
			&item.VerifiedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("environment source evidence scan: %w", err)
		}
		item.VerifiedAt = item.VerifiedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("environment source evidence rows: %w", err)
	}
	return out, nil
}

var _ EnvironmentEndpointEvidenceStore = (*PostgresEnvironmentResourceEvidenceStore)(nil)
var _ EnvironmentReplicaEvidenceStore = (*PostgresEnvironmentResourceEvidenceStore)(nil)
var _ EnvironmentSourceEvidenceStore = (*PostgresEnvironmentResourceEvidenceStore)(nil)
