// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package serviceconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// Store persists configuration and resolves immutable deployment snapshots.
type Store struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewStore(pool *pg.Pool) *Store {
	return &Store{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("service config store: pool is required")
	}
	return nil
}

// Get returns an explicit row. Use ResolveDeployment to get defaults when no
// row exists and to bind those defaults immutably to a deployment.
func (s *Store) Get(ctx context.Context, projectID, environmentID, serviceID string) (Config, error) {
	if err := s.ready(); err != nil {
		return Config{}, err
	}
	return getConfig(ctx, s.Pool, projectID, environmentID, serviceID)
}

// GetInTx returns an explicit row through a caller-owned transaction. The
// caller owns commit/rollback, allowing authorization and the exact-scope read
// to share one transaction snapshot.
func (s *Store) GetInTx(
	ctx context.Context,
	tx *pg.Tx,
	projectID,
	environmentID,
	serviceID string,
) (Config, error) {
	if err := s.ready(); err != nil {
		return Config{}, err
	}
	if tx == nil {
		return Config{}, errors.New("service config GetInTx: transaction is required")
	}
	return getConfig(ctx, tx, projectID, environmentID, serviceID)
}

func getConfig(
	ctx context.Context,
	conn pg.Conn,
	projectID,
	environmentID,
	serviceID string,
) (Config, error) {
	return scanConfig(conn.QueryRow(ctx, configSelect+`
		WHERE project_id = $1 AND environment_id = $2 AND service_id = $3`,
		projectID, environmentID, serviceID))
}

// Put creates or compare-and-swap updates a config. expectedVersion=0 creates
// the first row; updates require the exact current version.
func (s *Store) Put(ctx context.Context, in Config, expectedVersion int64) (Config, error) {
	if err := s.ready(); err != nil {
		return Config{}, err
	}
	return s.put(ctx, s.Pool, in, expectedVersion)
}

// PutInTx creates or compare-and-swap updates a config through a caller-owned
// transaction. It never commits or rolls back the transaction.
func (s *Store) PutInTx(
	ctx context.Context,
	tx *pg.Tx,
	in Config,
	expectedVersion int64,
) (Config, error) {
	if err := s.ready(); err != nil {
		return Config{}, err
	}
	if tx == nil {
		return Config{}, errors.New("service config PutInTx: transaction is required")
	}
	return s.put(ctx, tx, in, expectedVersion)
}

func (s *Store) put(
	ctx context.Context,
	conn pg.Conn,
	in Config,
	expectedVersion int64,
) (Config, error) {
	cfg := Normalize(in)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	now := s.Now()
	if expectedVersion == 0 {
		cfg.Version = 1
		cfg.CreatedAt, cfg.UpdatedAt = now, now
		tag, err := conn.Exec(ctx, `
			INSERT INTO service_environment_configs (
				project_id, service_id, environment_id, version,
				root_directory, builder, dockerfile_path, build_command,
				start_command, predeploy_command, target_port,
				health_kind, health_path, health_port,
				health_timeout_seconds, health_interval_seconds,
				restart_policy, restart_max_retries, replicas,
				overlap_enabled, drain_grace_seconds,
				runtime_kind, cron_schedule, cron_timeout_seconds,
				cron_max_attempts, cron_retry_delay_seconds, created_at, updated_at
			) VALUES (
				$1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,
				$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$26
			)
			ON CONFLICT (service_id, environment_id) DO NOTHING`,
			cfg.ProjectID, cfg.ServiceID, cfg.EnvironmentID,
			cfg.RootDirectory, cfg.Builder, cfg.DockerfilePath, cfg.BuildCommand,
			cfg.StartCommand, cfg.PredeployCommand, cfg.TargetPort,
			cfg.HealthKind, cfg.HealthPath, cfg.HealthPort,
			durationSeconds(cfg.HealthTimeout), durationSeconds(cfg.HealthInterval),
			cfg.RestartPolicy, cfg.RestartRetries, cfg.Replicas,
			cfg.OverlapEnabled, durationSeconds(cfg.DrainGrace), cfg.RuntimeKind,
			cfg.CronSchedule, durationSeconds(cfg.CronTimeout), cfg.CronMaxAttempts,
			durationSeconds(cfg.CronRetryDelay), now,
		)
		if err != nil {
			return Config{}, fmt.Errorf("service config insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return Config{}, ErrConflict
		}
		return cfg, nil
	}

	tag, err := conn.Exec(ctx, `
		UPDATE service_environment_configs SET
			root_directory=$1, builder=$2, dockerfile_path=$3, build_command=$4,
			start_command=$5, predeploy_command=$6, target_port=$7,
			health_kind=$8, health_path=$9, health_port=$10,
			health_timeout_seconds=$11, health_interval_seconds=$12,
			restart_policy=$13, restart_max_retries=$14, replicas=$15,
			overlap_enabled=$16, drain_grace_seconds=$17, runtime_kind=$18,
			cron_schedule=$19, cron_timeout_seconds=$20, cron_max_attempts=$21,
			cron_retry_delay_seconds=$22, version=version+1, updated_at=$23
		WHERE project_id=$24 AND environment_id=$25 AND service_id=$26 AND version=$27`,
		cfg.RootDirectory, cfg.Builder, cfg.DockerfilePath, cfg.BuildCommand,
		cfg.StartCommand, cfg.PredeployCommand, cfg.TargetPort,
		cfg.HealthKind, cfg.HealthPath, cfg.HealthPort,
		durationSeconds(cfg.HealthTimeout), durationSeconds(cfg.HealthInterval),
		cfg.RestartPolicy, cfg.RestartRetries, cfg.Replicas,
		cfg.OverlapEnabled, durationSeconds(cfg.DrainGrace), cfg.RuntimeKind,
		cfg.CronSchedule, durationSeconds(cfg.CronTimeout), cfg.CronMaxAttempts,
		durationSeconds(cfg.CronRetryDelay), now,
		cfg.ProjectID, cfg.EnvironmentID, cfg.ServiceID, expectedVersion,
	)
	if err != nil {
		return Config{}, fmt.Errorf("service config update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Config{}, ErrConflict
	}
	cfg.Version = expectedVersion + 1
	cfg.UpdatedAt = now
	return cfg, nil
}

// ResolveDeployment loads or creates the immutable config/variable metadata
// snapshot for one canonical deployment. Existing snapshots always win: a
// lease retry cannot drift onto config or variable versions edited later.
func (s *Store) ResolveDeployment(ctx context.Context, deploymentID, projectID, environmentID, serviceID string) (Snapshot, error) {
	if err := s.ready(); err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pg.Tx) error {
		existing, err := getSnapshot(ctx, tx, deploymentID)
		if err == nil {
			if existing.ProjectID != projectID || existing.EnvironmentID != environmentID || existing.ServiceID != serviceID {
				return ErrScope
			}
			if err := bindDeploymentConfigurationRevisionInTx(ctx, tx, deploymentID, projectID, environmentID, serviceID, existing.ConfigurationRevision); err != nil {
				return err
			}
			out = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		var legacyRoot, legacyBuilder, legacyDockerfile string
		if err := tx.QueryRow(ctx, `
			SELECT root_directory, builder, dockerfile_path
			  FROM deployments
			 WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4
			 FOR UPDATE`,
			deploymentID, projectID, environmentID, serviceID,
		).Scan(&legacyRoot, &legacyBuilder, &legacyDockerfile); errors.Is(err, pgx.ErrNoRows) {
			return ErrScope
		} else if err != nil {
			return fmt.Errorf("service config snapshot deployment scope: %w", err)
		}

		cfg, err := scanConfig(tx.QueryRow(ctx, configSelect+`
			WHERE project_id=$1 AND environment_id=$2 AND service_id=$3`,
			projectID, environmentID, serviceID))
		if errors.Is(err, ErrNotFound) {
			cfg = Default(projectID, serviceID, environmentID)
			// Legacy intake already persists these non-secret source fields on
			// the canonical deployment. Use that durable row, never the queue
			// payload, while explicit service config is rolled out.
			cfg.RootDirectory = legacyRoot
			if legacyBuilder != "" {
				cfg.Builder = legacyBuilder
			}
			if legacyDockerfile != "" {
				cfg.DockerfilePath = legacyDockerfile
			}
			cfg = Normalize(cfg)
			if err := Validate(cfg); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		refs, err := listVariableVersions(ctx, tx, projectID, environmentID)
		if err != nil {
			return err
		}
		configJSON, refsJSON, digest, err := snapshotPayload(cfg, refs)
		if err != nil {
			return err
		}
		now := s.Now()
		configurationRevision, err := currentServiceAppliedRevisionInTx(ctx, tx, projectID, environmentID, serviceID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO deployment_config_snapshots (
				deployment_id, project_id, service_id, environment_id,
				configuration_revision, config_version, effective_config, variable_versions,
				snapshot_sha256, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10)`,
			deploymentID, projectID, serviceID, environmentID, configurationRevision, cfg.Version,
			string(configJSON), string(refsJSON), digest, now,
		)
		if err != nil {
			return fmt.Errorf("service config snapshot insert: %w", err)
		}
		if err := bindDeploymentConfigurationRevisionInTx(ctx, tx, deploymentID, projectID, environmentID, serviceID, configurationRevision); err != nil {
			return err
		}
		out = Snapshot{
			SchemaVersion:         SnapshotSchemaVersion,
			DeploymentID:          deploymentID,
			ProjectID:             projectID,
			ServiceID:             serviceID,
			EnvironmentID:         environmentID,
			ConfigurationRevision: configurationRevision,
			ConfigVersion:         cfg.Version,
			Config:                cfg,
			VariableSchema:        variableRefsSchema(refs),
			VariableVersions:      refs,
			SHA256:                digest,
			CreatedAt:             now,
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

// ReplaceDeploymentSnapshotInTx binds the current desired configuration and
// variable versions to a newly-created deployment inside the caller's apply
// transaction. It is intentionally limited to a deployment that is still
// queued: an observed or running deployment snapshot is immutable.
func (s *Store) ReplaceDeploymentSnapshotInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID, projectID, environmentID, serviceID string,
) (Snapshot, error) {
	if err := s.ready(); err != nil {
		return Snapshot{}, err
	}
	if tx == nil {
		return Snapshot{}, errors.New("service config ReplaceDeploymentSnapshotInTx: transaction is required")
	}
	var state string
	if err := tx.QueryRow(ctx, `
		SELECT state FROM deployments
		 WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4
		 FOR UPDATE`, deploymentID, projectID, environmentID, serviceID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrScope
	} else if err != nil {
		return Snapshot{}, fmt.Errorf("service config replacement snapshot deployment scope: %w", err)
	}
	if state != "queued" {
		return Snapshot{}, errors.New("service config replacement snapshot requires a queued deployment")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM deployment_config_snapshots WHERE deployment_id=$1`, deploymentID); err != nil {
		return Snapshot{}, fmt.Errorf("service config replacement snapshot delete: %w", err)
	}
	cfg, err := scanConfig(tx.QueryRow(ctx, configSelect+`
		WHERE project_id=$1 AND environment_id=$2 AND service_id=$3`,
		projectID, environmentID, serviceID))
	if errors.Is(err, ErrNotFound) {
		cfg = Default(projectID, serviceID, environmentID)
		var root, builder, dockerfile string
		if err := tx.QueryRow(ctx, `SELECT root_directory,builder,dockerfile_path FROM deployments WHERE id=$1`, deploymentID).Scan(&root, &builder, &dockerfile); err != nil {
			return Snapshot{}, fmt.Errorf("service config replacement snapshot legacy config: %w", err)
		}
		cfg.RootDirectory = root
		if builder != "" {
			cfg.Builder = builder
		}
		if dockerfile != "" {
			cfg.DockerfilePath = dockerfile
		}
		cfg = Normalize(cfg)
		if err := Validate(cfg); err != nil {
			return Snapshot{}, err
		}
	} else if err != nil {
		return Snapshot{}, err
	}
	refs, err := listVariableVersions(ctx, tx, projectID, environmentID)
	if err != nil {
		return Snapshot{}, err
	}
	configJSON, refsJSON, digest, err := snapshotPayload(cfg, refs)
	if err != nil {
		return Snapshot{}, err
	}
	now := s.Now().UTC()
	configurationRevision, err := currentServiceAppliedRevisionInTx(ctx, tx, projectID, environmentID, serviceID)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,configuration_revision,config_version,
			effective_config,variable_versions,snapshot_sha256,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10)`,
		deploymentID, projectID, serviceID, environmentID, configurationRevision, cfg.Version,
		string(configJSON), string(refsJSON), digest, now); err != nil {
		return Snapshot{}, fmt.Errorf("service config replacement snapshot insert: %w", err)
	}
	if err := bindDeploymentConfigurationRevisionInTx(ctx, tx, deploymentID, projectID, environmentID, serviceID, configurationRevision); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion, DeploymentID: deploymentID,
		ProjectID: projectID, ServiceID: serviceID, EnvironmentID: environmentID,
		ConfigurationRevision: configurationRevision,
		ConfigVersion:         cfg.Version, Config: cfg, VariableSchema: variableRefsSchema(refs),
		VariableVersions: refs, SHA256: digest, CreatedAt: now,
	}, nil
}

func currentServiceAppliedRevisionInTx(
	ctx context.Context,
	tx *pg.Tx,
	projectID, environmentID, serviceID string,
) (int64, error) {
	var revision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT applied_revision
			  FROM service_configuration_revisions
			 WHERE project_id=$1 AND environment_id=$2 AND service_id=$3
		),0)`, projectID, environmentID, serviceID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("service config applied revision: %w", err)
	}
	return revision, nil
}

// bindDeploymentConfigurationRevisionInTx makes the deployment row and its
// immutable snapshot one atomic revision fact. A zero revision is retained for
// deployments created before revision tracking or before any applied change.
func bindDeploymentConfigurationRevisionInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID, projectID, environmentID, serviceID string,
	revision int64,
) error {
	if revision < 0 {
		return errors.New("service config snapshot revision cannot be negative")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE deployments
		   SET configuration_revision=$5
		 WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4`,
		deploymentID, projectID, environmentID, serviceID, revision)
	if err != nil {
		return fmt.Errorf("service config snapshot revision bind: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrScope
	}
	return nil
}

func getSnapshot(ctx context.Context, conn pg.Conn, deploymentID string) (Snapshot, error) {
	var out Snapshot
	var configJSON, refsJSON []byte
	err := conn.QueryRow(ctx, `
		SELECT deployment_id, project_id, service_id, environment_id,
		       configuration_revision, config_version, effective_config, variable_versions,
		       snapshot_sha256, created_at
		  FROM deployment_config_snapshots WHERE deployment_id=$1`,
		deploymentID,
	).Scan(
		&out.DeploymentID, &out.ProjectID, &out.ServiceID, &out.EnvironmentID, &out.ConfigurationRevision,
		&out.ConfigVersion, &configJSON, &refsJSON, &out.SHA256, &out.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("service config snapshot get: %w", err)
	}
	if err := json.Unmarshal(configJSON, &out.Config); err != nil {
		return Snapshot{}, fmt.Errorf("service config snapshot decode config: %w", err)
	}
	if err := json.Unmarshal(refsJSON, &out.VariableVersions); err != nil {
		return Snapshot{}, fmt.Errorf("service config snapshot decode variables: %w", err)
	}
	_, _, digest, err := snapshotPayload(out.Config, out.VariableVersions)
	if err != nil {
		return Snapshot{}, err
	}
	if digest != out.SHA256 {
		return Snapshot{}, errors.New("service config snapshot checksum mismatch")
	}
	out.SchemaVersion = SnapshotSchemaVersion
	out.VariableSchema = variableRefsSchema(out.VariableVersions)
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

func listVariableVersions(ctx context.Context, conn pg.Conn, projectID, environmentID string) ([]VariableVersionRef, error) {
	rows, err := conn.Query(ctx, `
		SELECT v.id,
		       v.service_id,
		       COALESCE(s.slug, ''),
		       COALESCE(s.display_name, ''),
		       v.name,
		       v.version
		  FROM service_variables v
		  LEFT JOIN services s
		    ON s.id = v.service_scope_id
		   AND s.project_id = v.project_id
		   AND s.deleted_at IS NULL
		 WHERE v.project_id=$1
		   AND v.environment_id=$2
		   AND v.deleted_at IS NULL
		   AND (v.service_id = '' OR s.id IS NOT NULL)
		 ORDER BY v.service_id ASC, v.name ASC, v.id ASC`,
		projectID, environmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("service config snapshot list variable versions: %w", err)
	}
	defer rows.Close()
	out := make([]VariableVersionRef, 0)
	for rows.Next() {
		var item VariableVersionRef
		if err := rows.Scan(
			&item.ID,
			&item.ServiceID,
			&item.ServiceSlug,
			&item.ServiceDisplayName,
			&item.Name,
			&item.Version,
		); err != nil {
			return nil, fmt.Errorf("service config snapshot scan variable version: %w", err)
		}
		if item.ServiceID != "" && (item.ServiceSlug == "" || item.ServiceDisplayName == "") {
			return nil, fmt.Errorf(
				"service config snapshot variable %q has no live service alias binding",
				item.ID,
			)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service config snapshot variable rows: %w", err)
	}
	return out, nil
}

func snapshotPayload(cfg Config, refs []VariableVersionRef) ([]byte, []byte, string, error) {
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("service config snapshot encode config: %w", err)
	}
	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return nil, nil, "", fmt.Errorf("service config snapshot encode variable versions: %w", err)
	}
	sum := sha256.Sum256(append(append(configJSON, '\n'), refsJSON...))
	return configJSON, refsJSON, hex.EncodeToString(sum[:]), nil
}

func variableRefsSchema(refs []VariableVersionRef) string {
	for _, ref := range refs {
		if ref.ServiceID != "" && (ref.ServiceSlug == "" || ref.ServiceDisplayName == "") {
			return VariableRefsSchemaV1
		}
	}
	return VariableRefsSchema
}

const configSelect = `SELECT
	project_id, service_id, environment_id, version,
	root_directory, builder, dockerfile_path, build_command,
	start_command, predeploy_command, target_port,
	health_kind, health_path, health_port,
	health_timeout_seconds, health_interval_seconds,
	restart_policy, restart_max_retries, replicas,
	overlap_enabled, drain_grace_seconds,
	runtime_kind, cron_schedule, cron_timeout_seconds,
	cron_max_attempts, cron_retry_delay_seconds, created_at, updated_at
FROM service_environment_configs`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfig(row rowScanner) (Config, error) {
	var out Config
	var healthTimeout, healthInterval, drainGrace, cronTimeout, cronRetryDelay int64
	err := row.Scan(
		&out.ProjectID, &out.ServiceID, &out.EnvironmentID, &out.Version,
		&out.RootDirectory, &out.Builder, &out.DockerfilePath, &out.BuildCommand,
		&out.StartCommand, &out.PredeployCommand, &out.TargetPort,
		&out.HealthKind, &out.HealthPath, &out.HealthPort,
		&healthTimeout, &healthInterval,
		&out.RestartPolicy, &out.RestartRetries, &out.Replicas,
		&out.OverlapEnabled, &drainGrace, &out.RuntimeKind, &out.CronSchedule,
		&cronTimeout, &out.CronMaxAttempts, &cronRetryDelay, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("service config scan: %w", err)
	}
	out.HealthTimeout = time.Duration(healthTimeout) * time.Second
	out.HealthInterval = time.Duration(healthInterval) * time.Second
	out.DrainGrace = time.Duration(drainGrace) * time.Second
	out.CronTimeout = time.Duration(cronTimeout) * time.Second
	out.CronRetryDelay = time.Duration(cronRetryDelay) * time.Second
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func durationSeconds(value time.Duration) int64 {
	return int64(value / time.Second)
}
