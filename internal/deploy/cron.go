// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/varrefs"
)

const (
	CronRunPending   = "pending"
	CronRunRunning   = "running"
	CronRunRetryWait = "retry_wait"
	CronRunSucceeded = "succeeded"
	CronRunFailed    = "failed"
)

// CronActivator records a cron deployment's immutable definition. It does not
// start an application container; due occurrences are executed later by
// CronExecutor through the retained one-off runtime.
type CronActivator interface {
	Activate(context.Context, runtime.ScheduleRequest, serviceconfig.Snapshot) error
}

// CronActivation is the immutable input committed together with the canonical
// deployment pointer by production workers. Keeping it value-only lets the API
// persistence layer own one PostgreSQL transaction without duplicating cron
// validation or cursor SQL.
type CronActivation struct {
	Candidate runtime.ScheduleRequest
	Snapshot  serviceconfig.Snapshot
}

type CronOccurrence struct {
	ID               string
	DeploymentID     string
	ProjectID        string
	ServiceID        string
	EnvironmentID    string
	DueAt            time.Time
	SnapshotSHA256   string
	Image            string
	SpecSHA256       string
	State            string
	Attempt          int
	MaxAttempts      int
	Timeout          time.Duration
	RetryDelay       time.Duration
	RetryAt          *time.Time
	AttemptStartedAt *time.Time
	ContainerName    string
	ContainerID      string
	ExitCode         *int
	FailureCode      failurecodes.Code
	FailureMessage   string
}

type cronCursor struct {
	DeploymentID   string
	ProjectID      string
	ServiceID      string
	EnvironmentID  string
	SnapshotSHA256 string
	Image          string
	Schedule       string
	Timeout        time.Duration
	MaxAttempts    int
	RetryDelay     time.Duration
	LastEvaluated  time.Time
}

// PostgresCronStore owns the durable cursor and occurrence state machines.
// It stores no command text or variable plaintext; both are fetched from the
// deployment snapshot at execution time and bound into the occurrence spec.
type PostgresCronStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

// ListOccurrences returns redaction-safe execution evidence for a single
// service/environment scope. It intentionally never joins command text or
// resolved variable values from the immutable deployment snapshot.
func (s *PostgresCronStore) ListOccurrences(ctx context.Context, projectID, environmentID, serviceID, state string, limit int) ([]CronOccurrence, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		return nil, errors.New("cron occurrence limit must be between 1 and 200")
	}
	query := cronRunSelect + ` WHERE project_id=$1 AND environment_id=$2 AND service_id=$3`
	args := []any{projectID, environmentID, serviceID}
	if state != "" {
		query += ` AND state=$4`
		args = append(args, state)
	}
	query += ` ORDER BY due_at DESC, id DESC LIMIT ` + fmt.Sprintf("%d", limit)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list cron occurrences: %w", err)
	}
	defer rows.Close()
	out := make([]CronOccurrence, 0)
	for rows.Next() {
		item, err := scanCronOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetOccurrence reads a single execution record only when the full durable
// scope matches, preventing a guessed occurrence ID from crossing tenants.
func (s *PostgresCronStore) GetOccurrence(ctx context.Context, projectID, environmentID, serviceID, id string) (CronOccurrence, error) {
	if err := s.ready(); err != nil {
		return CronOccurrence{}, err
	}
	item, err := scanCronOccurrence(s.Pool.QueryRow(ctx, cronRunSelect+` WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4`, id, projectID, environmentID, serviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CronOccurrence{}, pgx.ErrNoRows
	}
	if err != nil {
		return CronOccurrence{}, fmt.Errorf("get cron occurrence: %w", err)
	}
	return item, nil
}

func NewPostgresCronStore(pool *pg.Pool) *PostgresCronStore {
	return &PostgresCronStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresCronStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("cron store: postgres pool is required")
	}
	return nil
}

func (s *PostgresCronStore) Activate(ctx context.Context, candidate runtime.ScheduleRequest, snapshot serviceconfig.Snapshot) error {
	if err := s.ready(); err != nil {
		return err
	}
	activation := CronActivation{Candidate: candidate, Snapshot: snapshot}
	if err := ValidateCronActivation(activation); err != nil {
		return err
	}
	now := s.now()
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		return ActivateCronTx(ctx, tx, activation, now)
	})
}

func ValidateCronActivation(activation CronActivation) error {
	candidate, snapshot := activation.Candidate, activation.Snapshot
	cfg := snapshot.Config
	if cfg.RuntimeKind != serviceconfig.RuntimeKindCron {
		return errors.New("cron activation requires a cron config snapshot")
	}
	if err := serviceconfig.Validate(cfg); err != nil {
		return err
	}
	if candidate.DeploymentID != snapshot.DeploymentID || candidate.Image == "" {
		return errors.New("cron activation immutable deployment or image is missing")
	}
	if candidate.ProjectID != snapshot.ProjectID ||
		candidate.ServiceID != snapshot.ServiceID ||
		candidate.EnvironmentID != snapshot.EnvironmentID ||
		cfg.ProjectID != snapshot.ProjectID ||
		cfg.ServiceID != snapshot.ServiceID ||
		cfg.EnvironmentID != snapshot.EnvironmentID {
		return errors.New("cron activation immutable scope does not match snapshot")
	}
	return nil
}

// ActivateCronTx applies cursor/history changes inside a caller-owned
// transaction. The canonical deployment store uses this to make activation
// and active-pointer promotion indivisible.
func ActivateCronTx(ctx context.Context, tx *pg.Tx, activation CronActivation, now time.Time) error {
	if tx == nil {
		return errors.New("cron activation transaction is required")
	}
	if err := ValidateCronActivation(activation); err != nil {
		return err
	}
	candidate, snapshot := activation.Candidate, activation.Snapshot
	cfg := snapshot.Config
	// A replacement definition cannot run an old queued occurrence. A
	// currently running occurrence remains observable and blocks the new
	// scope until it reaches its retained terminal state.
	if _, err := tx.Exec(ctx, `UPDATE cron_schedule_cursors SET enabled=false, updated_at=$4, resource_version=resource_version+1
             WHERE project_id=$1 AND service_id=$2 AND environment_id=$3 AND deployment_id<>$5 AND enabled`, candidate.ProjectID, candidate.ServiceID, candidate.EnvironmentID, now, candidate.DeploymentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cron_occurrence_runs SET state='failed', failure_code=$4, failure_message='superseded by a newer cron deployment', completed_at=$5, updated_at=$5, resource_version=resource_version+1
             WHERE project_id=$1 AND service_id=$2 AND environment_id=$3 AND state IN ('pending','retry_wait')`, candidate.ProjectID, candidate.ServiceID, candidate.EnvironmentID, string(failurecodes.DeploySuperseded), now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO cron_schedule_cursors (
             deployment_id,project_id,service_id,environment_id,snapshot_sha256,image_ref,schedule,
             timeout_seconds,max_attempts,retry_delay_seconds,last_evaluated_at,enabled,created_at,updated_at
           ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true,$11,$11)
           ON CONFLICT (deployment_id) DO UPDATE SET snapshot_sha256=EXCLUDED.snapshot_sha256,image_ref=EXCLUDED.image_ref,
             schedule=EXCLUDED.schedule,timeout_seconds=EXCLUDED.timeout_seconds,max_attempts=EXCLUDED.max_attempts,
             retry_delay_seconds=EXCLUDED.retry_delay_seconds,last_evaluated_at=EXCLUDED.last_evaluated_at,enabled=true,
             updated_at=EXCLUDED.updated_at,resource_version=cron_schedule_cursors.resource_version+1`,
		candidate.DeploymentID, candidate.ProjectID, candidate.ServiceID, candidate.EnvironmentID, snapshot.SHA256,
		candidate.Image, cfg.CronSchedule, seconds(cfg.CronTimeout), cfg.CronMaxAttempts, seconds(cfg.CronRetryDelay), now)
	return err
}

// RunDue advances bounded UTC cursors and executes at most one no-overlap
// occurrence per scope. It is safe to call from multiple worker processes.
func (e *CronExecutor) RunDue(ctx context.Context) error {
	if e == nil || e.Store == nil || e.Runner == nil || e.Variables == nil {
		return errors.New("cron executor is not configured")
	}
	now := e.now()
	cursors, err := e.Store.enabled(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, cursor := range cursors {
		run, err := e.Store.advance(ctx, cursor, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if run == nil {
			continue
		}
		if err := e.execute(ctx, *run); err != nil {
			errs = append(errs, err)
		}
	}
	retries, err := e.Store.readyRetries(ctx, now)
	if err != nil {
		errs = append(errs, err)
	}
	for _, run := range retries {
		if err := e.execute(ctx, run); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CronExecutor materializes exact variable-version refs only for an active
// one-off process. The resolved environment is deliberately never persisted.
type CronExecutor struct {
	Store     *PostgresCronStore
	Runner    PredeployRunner
	Variables VariableResolver
	Now       func() time.Time
}

func (e *CronExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *PostgresCronStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresCronStore) enabled(ctx context.Context) ([]cronCursor, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT deployment_id,project_id,service_id,environment_id,snapshot_sha256,image_ref,schedule,timeout_seconds,max_attempts,retry_delay_seconds,last_evaluated_at FROM cron_schedule_cursors WHERE enabled ORDER BY updated_at`)
	if err != nil {
		return nil, fmt.Errorf("list cron cursors: %w", err)
	}
	defer rows.Close()
	var out []cronCursor
	for rows.Next() {
		var c cronCursor
		var timeout, delay int64
		if err := rows.Scan(&c.DeploymentID, &c.ProjectID, &c.ServiceID, &c.EnvironmentID, &c.SnapshotSHA256, &c.Image, &c.Schedule, &timeout, &c.MaxAttempts, &delay, &c.LastEvaluated); err != nil {
			return nil, err
		}
		c.Timeout = time.Duration(timeout) * time.Second
		c.RetryDelay = time.Duration(delay) * time.Second
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresCronStore) advance(ctx context.Context, cursor cronCursor, now time.Time) (*CronOccurrence, error) {
	var out *CronOccurrence
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var c cronCursor
		var timeout, delay int64
		err := tx.QueryRow(ctx, `SELECT deployment_id,project_id,service_id,environment_id,snapshot_sha256,image_ref,schedule,timeout_seconds,max_attempts,retry_delay_seconds,last_evaluated_at FROM cron_schedule_cursors WHERE deployment_id=$1 AND enabled FOR UPDATE`, cursor.DeploymentID).Scan(&c.DeploymentID, &c.ProjectID, &c.ServiceID, &c.EnvironmentID, &c.SnapshotSHA256, &c.Image, &c.Schedule, &timeout, &c.MaxAttempts, &delay, &c.LastEvaluated)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		c.Timeout = time.Duration(timeout) * time.Second
		c.RetryDelay = time.Duration(delay) * time.Second
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cron_occurrence_runs WHERE project_id=$1 AND service_id=$2 AND environment_id=$3 AND state IN ('pending','running','retry_wait'))`, c.ProjectID, c.ServiceID, c.EnvironmentID).Scan(&active); err != nil {
			return err
		}
		if active {
			// No-overlap means missed ticks are skipped, not replayed after a
			// long-running execution completes. Advancing while the row lock is
			// held makes that decision durable across workers and restarts.
			_, err := tx.Exec(ctx, `UPDATE cron_schedule_cursors SET last_evaluated_at=$2,updated_at=$2,resource_version=resource_version+1 WHERE deployment_id=$1`, c.DeploymentID, now)
			return err
		}
		due, skipped, err := cronDueWindowsWithRecovery(c, now)
		if err != nil {
			return fmt.Errorf("cron cursor %s: %w", c.DeploymentID, err)
		}
		if skipped {
			// An outage beyond the bounded due window is a deliberate skip, not
			// a permanent wedge or catch-up request. Advance under the cursor lock
			// so the next worker tick resumes from this durable boundary.
			_, err = tx.Exec(ctx, `UPDATE cron_schedule_cursors SET last_evaluated_at=$2,updated_at=$2,resource_version=resource_version+1 WHERE deployment_id=$1`, c.DeploymentID, now)
			return err
		}
		if len(due) == 0 {
			_, err = tx.Exec(ctx, `UPDATE cron_schedule_cursors SET last_evaluated_at=$2,updated_at=$2,resource_version=resource_version+1 WHERE deployment_id=$1`, c.DeploymentID, now)
			return err
		}
		item := CronOccurrence{ID: cronOccurrenceID(c.DeploymentID, due[0]), DeploymentID: c.DeploymentID, ProjectID: c.ProjectID, ServiceID: c.ServiceID, EnvironmentID: c.EnvironmentID, DueAt: due[0], SnapshotSHA256: c.SnapshotSHA256, Image: c.Image, State: CronRunPending, MaxAttempts: c.MaxAttempts, Timeout: c.Timeout, RetryDelay: c.RetryDelay}
		item.SpecSHA256 = cronOccurrenceSpec(item, 1)
		_, err = tx.Exec(ctx, `INSERT INTO cron_occurrence_runs (id,deployment_id,project_id,service_id,environment_id,due_at,snapshot_sha256,image_ref,spec_sha256,state,max_attempts,timeout_seconds,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11,$12,$12) ON CONFLICT (deployment_id,due_at) DO NOTHING`, item.ID, item.DeploymentID, item.ProjectID, item.ServiceID, item.EnvironmentID, item.DueAt, item.SnapshotSHA256, item.Image, item.SpecSHA256, item.MaxAttempts, seconds(item.Timeout), now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE cron_schedule_cursors SET last_evaluated_at=$2,updated_at=$3,resource_version=resource_version+1 WHERE deployment_id=$1`, c.DeploymentID, due[0], now); err != nil {
			return err
		}
		loaded, err := scanCronOccurrence(tx.QueryRow(ctx, cronRunSelect+` WHERE id=$1`, item.ID))
		if err != nil {
			return err
		}
		out = &loaded
		return nil
	})
	return out, err
}

func cronDueWindowsWithRecovery(c cronCursor, now time.Time) ([]time.Time, bool, error) {
	due, err := scheduler.CronDueWindows(scheduler.CronConfig{Schedule: c.Schedule, Timezone: scheduler.DefaultCronTimezone, Timeout: c.Timeout, RetryPolicy: scheduler.CronRetryPolicy{MaxAttempts: c.MaxAttempts}}, c.LastEvaluated, now)
	var windowErr *scheduler.CronDueWindowError
	if errors.As(err, &windowErr) {
		return nil, true, nil
	}
	return due, false, err
}

func (s *PostgresCronStore) readyRetries(ctx context.Context, now time.Time) ([]CronOccurrence, error) {
	rows, err := s.Pool.Query(ctx, cronRunSelect+` WHERE state='running' OR (state='retry_wait' AND retry_at <= $1) ORDER BY retry_at NULLS FIRST, due_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronOccurrence
	for rows.Next() {
		item, err := scanCronOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (e *CronExecutor) execute(ctx context.Context, seed CronOccurrence) error {
	run, err := e.Store.claim(ctx, seed.ID, e.now())
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	cfg, refs, err := e.Store.snapshot(ctx, run.DeploymentID, run.SnapshotSHA256)
	if err != nil {
		return e.Store.finish(ctx, *run, runtime.OneOffResult{}, failurecodes.ReconcileStepFailed, "cron snapshot lookup failed", e.now())
	}
	resolver, ok := e.Variables.(versionedVariableResolver)
	if !ok {
		return e.Store.finish(ctx, *run, runtime.OneOffResult{}, failurecodes.NotImplemented, "cron requires immutable variable-version resolution", e.now())
	}
	var pinned []varrefs.VariableVersionRef
	for _, ref := range refs {
		pinned = append(pinned, varrefs.VariableVersionRef{
			ID:                 ref.ID,
			ServiceID:          ref.ServiceID,
			ServiceSlug:        ref.ServiceSlug,
			ServiceDisplayName: ref.ServiceDisplayName,
			Name:               ref.Name,
			Version:            ref.Version,
		})
	}
	variableCtx := secrets.WithDecryptAuditExecution(ctx, secrets.DecryptAuditExecution{
		Kind:         secrets.DecryptExecutionCron,
		ExecutionID:  run.ID,
		DeploymentID: run.DeploymentID,
		Attempt:      run.Attempt,
	})
	env, err := resolver.ResolveAtVersions(variableCtx, run.ProjectID, run.EnvironmentID, run.ServiceID, pinned)
	if err != nil {
		return e.Store.finish(ctx, *run, runtime.OneOffResult{}, variableFailureCode(err), "cron variable resolution failed", e.now())
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, k := range keys {
		values = append(values, k+"="+env[k])
	}
	spec := cronOccurrenceSpec(*run, run.Attempt)
	deadline := run.AttemptStartedAt.Add(run.Timeout)
	remaining := deadline.Sub(e.now())
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	result, err := e.Runner.RunOneOff(ctx, runtime.OneOffRequest{WorkloadKind: "cron", ScheduleRequest: runtime.ScheduleRequest{DeploymentID: run.DeploymentID, InstanceID: run.ID, ProjectID: run.ProjectID, ServiceID: run.ServiceID, EnvironmentID: run.EnvironmentID, Image: run.Image, StartCommand: cfg.StartCommand, Env: values, RestartPolicy: "no", RestartMaxRetries: 0}, Command: cfg.StartCommand, SpecSHA256: spec, Timeout: remaining, Deadline: deadline})
	if err != nil {
		return err
	}
	code, msg := failurecodes.Code(""), ""
	if result.TimedOut {
		code = failurecodes.PredeployTimeout
		msg = "cron occurrence exceeded its timeout"
	} else if result.ExitCode != 0 {
		code = failurecodes.PredeployFailed
		msg = fmt.Sprintf("cron occurrence exited with status %d", result.ExitCode)
	}
	return e.Store.finish(ctx, *run, result, code, msg, e.now())
}

func (s *PostgresCronStore) claim(ctx context.Context, id string, now time.Time) (*CronOccurrence, error) {
	var out *CronOccurrence
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		run, err := scanCronOccurrence(tx.QueryRow(ctx, cronRunSelect+` WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if run.State == CronRunRetryWait && (run.RetryAt == nil || run.RetryAt.After(now)) {
			return nil
		}
		if run.State != CronRunPending && run.State != CronRunRetryWait && run.State != CronRunRunning {
			return nil
		}
		if run.State != CronRunRunning {
			if _, err = tx.Exec(ctx, `UPDATE cron_occurrence_runs SET state='running',attempt=attempt+1,attempt_started_at=$2,retry_at=NULL,updated_at=$2,resource_version=resource_version+1 WHERE id=$1`, id, now); err != nil {
				return err
			}
		}
		run, err = scanCronOccurrence(tx.QueryRow(ctx, cronRunSelect+` WHERE id=$1`, id))
		if err != nil {
			return err
		}
		out = &run
		return nil
	})
	return out, err
}

func (s *PostgresCronStore) finish(ctx context.Context, run CronOccurrence, result runtime.OneOffResult, code failurecodes.Code, message string, now time.Time) error {
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		current, err := scanCronOccurrence(tx.QueryRow(ctx, cronRunSelect+` WHERE id=$1 FOR UPDATE`, run.ID))
		if err != nil {
			return err
		}
		if current.State == CronRunSucceeded || current.State == CronRunFailed {
			return nil
		}
		state := CronRunSucceeded
		var retryAt any = nil
		if code != "" {
			if current.Attempt < current.MaxAttempts {
				state = CronRunRetryWait
				retryAt = now.Add(current.RetryDelay)
			} else {
				state = CronRunFailed
			}
		}
		completed := any(nil)
		if state == CronRunSucceeded || state == CronRunFailed {
			completed = now
		}
		_, err = tx.Exec(ctx, `UPDATE cron_occurrence_runs SET state=$2,container_name=$3,container_id=$4,exit_code=$5,failure_code=$6,failure_message=$7,retry_at=$8,completed_at=$9,updated_at=$10,resource_version=resource_version+1 WHERE id=$1`, current.ID, state, result.ContainerName, result.ContainerID, result.ExitCode, string(code), message, retryAt, completed, now)
		return err
	})
}

func (s *PostgresCronStore) snapshot(ctx context.Context, deploymentID, sha string) (serviceconfig.Config, []serviceconfig.VariableVersionRef, error) {
	var cfg serviceconfig.Config
	var refs []serviceconfig.VariableVersionRef
	var c, r []byte
	var observedSHA string
	err := s.Pool.QueryRow(ctx, `SELECT effective_config,variable_versions,snapshot_sha256 FROM deployment_config_snapshots WHERE deployment_id=$1`, deploymentID).Scan(&c, &r, &observedSHA)
	if err != nil {
		return cfg, nil, err
	}
	if observedSHA != sha {
		return cfg, nil, errors.New("cron snapshot checksum drift")
	}
	if err = json.Unmarshal(c, &cfg); err != nil {
		return cfg, nil, err
	}
	if err = json.Unmarshal(r, &refs); err != nil {
		return cfg, nil, err
	}
	if cfg.RuntimeKind != serviceconfig.RuntimeKindCron {
		return cfg, nil, errors.New("snapshot is not cron")
	}
	return cfg, refs, nil
}

const cronRunSelect = `SELECT id,deployment_id,project_id,service_id,environment_id,due_at,snapshot_sha256,image_ref,spec_sha256,state,attempt,max_attempts,timeout_seconds,retry_at,attempt_started_at,container_name,container_id,exit_code,failure_code,failure_message FROM cron_occurrence_runs`

func scanCronOccurrence(row interface{ Scan(...any) error }) (CronOccurrence, error) {
	var r CronOccurrence
	var timeout int64
	err := row.Scan(&r.ID, &r.DeploymentID, &r.ProjectID, &r.ServiceID, &r.EnvironmentID, &r.DueAt, &r.SnapshotSHA256, &r.Image, &r.SpecSHA256, &r.State, &r.Attempt, &r.MaxAttempts, &timeout, &r.RetryAt, &r.AttemptStartedAt, &r.ContainerName, &r.ContainerID, &r.ExitCode, &r.FailureCode, &r.FailureMessage)
	r.Timeout = time.Duration(timeout) * time.Second
	return r, err
}
func cronOccurrenceID(deploymentID string, due time.Time) string {
	sum := sha256.Sum256([]byte(deploymentID + "\n" + due.UTC().Format(time.RFC3339Nano)))
	return "cron_" + hex.EncodeToString(sum[:16])
}
func cronOccurrenceSpec(run CronOccurrence, attempt int) string {
	raw := fmt.Sprintf("%s\n%s\n%s\n%s\n%d", run.DeploymentID, run.DueAt.UTC().Format(time.RFC3339Nano), run.SnapshotSHA256, run.Image, attempt)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func seconds(v time.Duration) int64 { return int64(v / time.Second) }
