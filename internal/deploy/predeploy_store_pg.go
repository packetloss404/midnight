// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

type PostgresPredeployStore struct {
	Pool *dbpg.Pool
	Now  func() time.Time
}

func NewPostgresPredeployStore(pool *dbpg.Pool) *PostgresPredeployStore {
	return &PostgresPredeployStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresPredeployStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("predeploy store: postgres pool is required")
	}
	return nil
}

func (s *PostgresPredeployStore) Ensure(ctx context.Context, seed PredeployRun) (PredeployRun, error) {
	if err := s.ready(); err != nil {
		return PredeployRun{}, err
	}
	now := s.Now()
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO deployment_predeploy_runs (
			deployment_id, project_id, environment_id, service_id,
			spec_sha256, state, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,'pending',$6,$6)
		ON CONFLICT (deployment_id) DO NOTHING`,
		seed.DeploymentID, seed.ProjectID, seed.EnvironmentID, seed.ServiceID,
		seed.SpecSHA256, now,
	)
	if err != nil {
		return PredeployRun{}, fmt.Errorf("ensure predeploy run: %w", err)
	}
	run, err := s.get(ctx, seed.DeploymentID)
	if err != nil {
		return PredeployRun{}, err
	}
	if run.ProjectID != seed.ProjectID || run.EnvironmentID != seed.EnvironmentID ||
		run.ServiceID != seed.ServiceID || run.SpecSHA256 != seed.SpecSHA256 {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	return run, nil
}

func (s *PostgresPredeployStore) MarkRunning(ctx context.Context, deploymentID, specSHA string) (PredeployRun, error) {
	if err := s.ready(); err != nil {
		return PredeployRun{}, err
	}
	now := s.Now()
	_, err := s.Pool.Exec(ctx, `
		UPDATE deployment_predeploy_runs
		   SET state='running',
		       started_at=COALESCE(started_at,$3),
		       updated_at=$3,
		       resource_version=resource_version+1
		 WHERE deployment_id=$1 AND spec_sha256=$2
		   AND state IN ('pending','running')`,
		deploymentID, specSHA, now,
	)
	if err != nil {
		return PredeployRun{}, fmt.Errorf("mark predeploy running: %w", err)
	}
	run, err := s.get(ctx, deploymentID)
	if err != nil {
		return PredeployRun{}, err
	}
	if run.SpecSHA256 != specSHA {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	return run, nil
}

func (s *PostgresPredeployStore) MarkTerminal(
	ctx context.Context,
	deploymentID, specSHA string,
	result runtime.OneOffResult,
	code failurecodes.Code,
	message string,
) (PredeployRun, error) {
	if err := s.ready(); err != nil {
		return PredeployRun{}, err
	}
	state := PredeployStateSucceeded
	if code != "" {
		state = PredeployStateFailed
		if !failurecodes.IsRegistered(code) {
			return PredeployRun{}, fmt.Errorf("mark predeploy terminal: unregistered failure code %q", code)
		}
	}
	now := s.Now()
	_, err := s.Pool.Exec(ctx, `
		UPDATE deployment_predeploy_runs
		   SET state=$3, container_name=$4, container_id=$5, exit_code=$6,
		       failure_code=$7, failure_message=$8, completed_at=$9,
		       updated_at=$9, resource_version=resource_version+1
		 WHERE deployment_id=$1 AND spec_sha256=$2
		   AND state IN ('pending','running')`,
		deploymentID, specSHA, state, result.ContainerName, result.ContainerID,
		result.ExitCode, code, message, now,
	)
	if err != nil {
		return PredeployRun{}, fmt.Errorf("mark predeploy terminal: %w", err)
	}
	run, err := s.get(ctx, deploymentID)
	if err != nil {
		return PredeployRun{}, err
	}
	if run.SpecSHA256 != specSHA {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	if run.State != state || run.ExitCode == nil || *run.ExitCode != result.ExitCode ||
		run.FailureCode != code {
		return PredeployRun{}, fmt.Errorf("%w: conflicting terminal result", ErrPredeploySpecConflict)
	}
	return run, nil
}

func (s *PostgresPredeployStore) get(ctx context.Context, deploymentID string) (PredeployRun, error) {
	var run PredeployRun
	var exitCode *int
	err := s.Pool.QueryRow(ctx, `
		SELECT deployment_id, project_id, environment_id, service_id,
		       spec_sha256, state, container_name, container_id, exit_code,
		       failure_code, failure_message, started_at, completed_at,
		       created_at, updated_at, resource_version
		  FROM deployment_predeploy_runs
		 WHERE deployment_id=$1`,
		deploymentID,
	).Scan(
		&run.DeploymentID, &run.ProjectID, &run.EnvironmentID, &run.ServiceID,
		&run.SpecSHA256, &run.State, &run.ContainerName, &run.ContainerID, &exitCode,
		&run.FailureCode, &run.FailureMessage, &run.StartedAt, &run.CompletedAt,
		&run.CreatedAt, &run.UpdatedAt, &run.ResourceVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PredeployRun{}, fmt.Errorf("predeploy run %s not found", deploymentID)
	}
	if err != nil {
		return PredeployRun{}, fmt.Errorf("get predeploy run: %w", err)
	}
	run.ExitCode = exitCode
	return run, nil
}
