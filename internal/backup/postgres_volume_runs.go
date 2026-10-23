// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/volume"
)

var (
	ErrVolumeRunNotFound = errors.New("volume backup or restore run not found")
	ErrVolumeRunConflict = errors.New("volume backup or restore state conflict")
	ErrVolumeRunInvalid  = errors.New("volume backup or restore run is invalid")
)

type VolumeBackupRunState string

const (
	VolumeBackupPending   VolumeBackupRunState = "pending"
	VolumeBackupRunning   VolumeBackupRunState = "running"
	VolumeBackupSucceeded VolumeBackupRunState = "succeeded"
	VolumeBackupFailed    VolumeBackupRunState = "failed"
	VolumeBackupDeleted   VolumeBackupRunState = "deleted"
)

type VolumeRestoreRunState string

const (
	VolumeRestorePending  VolumeRestoreRunState = "pending"
	VolumeRestoreRunning  VolumeRestoreRunState = "running"
	VolumeRestoreVerified VolumeRestoreRunState = "verified"
	VolumeRestoreStaged   VolumeRestoreRunState = "staged"
	VolumeRestoreFailed   VolumeRestoreRunState = "failed"
	VolumeRestoreApplied  VolumeRestoreRunState = "applied"
)

type VolumeBackupRun struct {
	ID             string               `json:"id"`
	VolumeID       string               `json:"volume_id"`
	ProjectID      string               `json:"project_id"`
	EnvironmentID  string               `json:"environment_id"`
	ServiceID      string               `json:"service_id"`
	PolicyID       string               `json:"policy_id,omitempty"`
	TriggerKind    string               `json:"trigger_kind"`
	State          VolumeBackupRunState `json:"state"`
	ObjectKey      string               `json:"object_key"`
	ObjectSHA256   string               `json:"object_sha256,omitempty"`
	SourceSHA256   string               `json:"source_sha256,omitempty"`
	ObjectBytes    int64                `json:"object_bytes"`
	SourceBytes    int64                `json:"source_bytes"`
	ArchiveFormat  string               `json:"archive_format,omitempty"`
	Locked         bool                 `json:"locked"`
	RetentionUntil time.Time            `json:"retention_until,omitempty"`
	FailureCode    string               `json:"failure_code,omitempty"`
	FailureDetail  string               `json:"failure_detail,omitempty"`
	StartedAt      time.Time            `json:"started_at,omitempty"`
	CompletedAt    time.Time            `json:"completed_at,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

type VolumeRestoreRun struct {
	ID                    string                `json:"id"`
	BackupID              string                `json:"backup_id"`
	SourceVolumeID        string                `json:"source_volume_id"`
	ProjectID             string                `json:"project_id"`
	EnvironmentID         string                `json:"environment_id"`
	ServiceID             string                `json:"service_id"`
	State                 VolumeRestoreRunState `json:"state"`
	ReplacementVolumeID   string                `json:"replacement_volume_id,omitempty"`
	ReplacementVolumeName string                `json:"replacement_volume_name,omitempty"`
	SourceRetained        bool                  `json:"source_retained"`
	AttachmentSwapStaged  bool                  `json:"attachment_swap_staged"`
	RestoredSHA256        string                `json:"restored_sha256,omitempty"`
	RestoredBytes         int64                 `json:"restored_bytes"`
	FailureCode           string                `json:"failure_code,omitempty"`
	FailureDetail         string                `json:"failure_detail,omitempty"`
	StartedAt             time.Time             `json:"started_at,omitempty"`
	CompletedAt           time.Time             `json:"completed_at,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

type CreateVolumeBackupRun struct {
	ID             string
	VolumeID       string
	TriggerKind    string
	PolicyID       string
	ObjectKey      string
	RetentionUntil time.Time
}

type CreateVolumeRestoreRun struct {
	ID       string
	BackupID string
}

type VolumeBackupWork struct {
	Run VolumeBackupRun `json:"run"`
	Job queue.Job       `json:"job"`
}

type VolumeRestoreWork struct {
	Run VolumeRestoreRun `json:"run"`
	Job queue.Job        `json:"job"`
}

// PostgresVolumeRunStore persists backup/restore state and owns the atomic
// staged-volume swap. It never persists a passphrase or plaintext.
type PostgresVolumeRunStore struct {
	Pool      *dbpg.Pool
	Now       func() time.Time
	IDFactory func(prefix string) string
}

func NewPostgresVolumeRunStore(pool *dbpg.Pool) *PostgresVolumeRunStore {
	return &PostgresVolumeRunStore{
		Pool:      pool,
		Now:       func() time.Time { return time.Now().UTC() },
		IDFactory: randomVolumeRunID,
	}
}

func randomVolumeRunID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}

func (s *PostgresVolumeRunStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("volume run store pool is required")
	}
	return nil
}

func (s *PostgresVolumeRunStore) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *PostgresVolumeRunStore) newID(prefix string) string {
	if s.IDFactory == nil {
		return randomVolumeRunID(prefix)
	}
	return s.IDFactory(prefix)
}

func (s *PostgresVolumeRunStore) CreateBackup(ctx context.Context, request CreateVolumeBackupRun) (VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupRun{}, err
	}
	request.VolumeID = strings.TrimSpace(request.VolumeID)
	request.TriggerKind = strings.ToLower(strings.TrimSpace(request.TriggerKind))
	request.ObjectKey = strings.TrimSpace(request.ObjectKey)
	if request.TriggerKind == "" {
		request.TriggerKind = "manual"
	}
	switch request.TriggerKind {
	case "manual", "daily", "weekly", "monthly":
	default:
		return VolumeBackupRun{}, fmt.Errorf("%w: trigger_kind is invalid", ErrVolumeRunInvalid)
	}
	if request.VolumeID == "" || request.ObjectKey == "" {
		return VolumeBackupRun{}, fmt.Errorf("%w: volume_id and object_key are required", ErrVolumeRunInvalid)
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		id = s.newID("vbk")
	}
	now := s.now()
	var out VolumeBackupRun
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO volume_backup_runs (
			id, volume_id, project_id, environment_id, service_id, policy_id,
			trigger_kind, state, object_key, retention_until, created_at, updated_at
		)
		SELECT $1, v.id, v.project_id, v.environment_id, v.service_id,
		       NULLIF($2,''), $3, 'pending', $4, $5, $6, $6
		  FROM volumes v
		 WHERE v.id=$7
		   AND v.status IN ('ready','attached')
		RETURNING `+volumeBackupRunColumns,
		id, request.PolicyID, request.TriggerKind, request.ObjectKey,
		nullableTime(request.RetentionUntil), now, request.VolumeID,
	).Scan(volumeBackupRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRun{}, ErrVolumeRunNotFound
	}
	if err != nil {
		return VolumeBackupRun{}, fmt.Errorf("create volume backup run: %w", err)
	}
	return normalizeBackupRun(out), nil
}

// CreateBackupWork commits durable backup truth and executable queue intent in
// one transaction. A process crash can therefore leave both rows or neither,
// never an unqueued pending backup.
func (s *PostgresVolumeRunStore) CreateBackupWork(ctx context.Context, request CreateVolumeBackupRun) (VolumeBackupWork, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupWork{}, err
	}
	request.VolumeID = strings.TrimSpace(request.VolumeID)
	request.TriggerKind = strings.ToLower(strings.TrimSpace(request.TriggerKind))
	if request.TriggerKind == "" {
		request.TriggerKind = "manual"
	}
	switch request.TriggerKind {
	case "manual", "daily", "weekly", "monthly":
	default:
		return VolumeBackupWork{}, fmt.Errorf("%w: trigger_kind is invalid", ErrVolumeRunInvalid)
	}
	if request.VolumeID == "" {
		return VolumeBackupWork{}, fmt.Errorf("%w: volume_id is required", ErrVolumeRunInvalid)
	}
	runID := strings.TrimSpace(request.ID)
	if runID == "" {
		runID = s.newID("vbk")
	}
	if strings.TrimSpace(request.ObjectKey) == "" {
		request.ObjectKey = "volumes/" + request.VolumeID + "/" + runID + ".mnbk"
	}
	payload, err := EncodeVolumeJobPayload(VolumeJobPayload{Kind: VolumeJobBackup, RunID: runID})
	if err != nil {
		return VolumeBackupWork{}, err
	}
	now := s.now()
	jobID := s.newID("job")
	var out VolumeBackupWork
	err = s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		return createBackupWorkTx(ctx, tx, request, runID, jobID, payload, now, &out)
	})
	if err != nil {
		return VolumeBackupWork{}, fmt.Errorf("create volume backup work: %w", err)
	}
	out.Run = normalizeBackupRun(out.Run)
	out.Job = queue.Job{
		ID: jobID, Queue: queue.Cleanup, State: queue.JobQueued,
		PartitionKey: out.Run.ProjectID, AvailableAt: now, Payload: payload,
	}
	return out, nil
}

func createBackupWorkTx(
	ctx context.Context,
	tx *dbpg.Tx,
	request CreateVolumeBackupRun,
	runID, jobID string,
	payload []byte,
	now time.Time,
	out *VolumeBackupWork,
) error {
	err := tx.QueryRow(ctx, `
		INSERT INTO volume_backup_runs (
			id, volume_id, project_id, environment_id, service_id, policy_id,
			trigger_kind, state, object_key, retention_until, created_at, updated_at
		)
		SELECT $1, v.id, v.project_id, v.environment_id, v.service_id,
		       NULLIF($2,''), $3, 'pending', $4, $5, $6, $6
		  FROM volumes v
		 WHERE v.id=$7
		   AND v.status IN ('ready','attached')
		RETURNING `+volumeBackupRunColumns,
		runID, request.PolicyID, request.TriggerKind, request.ObjectKey,
		nullableTime(request.RetentionUntil), now, request.VolumeID,
	).Scan(volumeBackupRunScanTargets(&out.Run)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVolumeRunNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO jobs (
			id, queue, state, partition_key, priority, available_at,
			payload, created_at, updated_at
		) VALUES ($1,$2,'pending',$3,0,$4,$5,$4,$4)`,
		jobID, string(queue.Cleanup), out.Run.ProjectID, now, payload,
	)
	return err
}

func (s *PostgresVolumeRunStore) StartBackup(ctx context.Context, id string) (VolumeBackupRun, error) {
	return s.transitionBackup(ctx, id, VolumeBackupPending, VolumeBackupRunning)
}

func (s *PostgresVolumeRunStore) CompleteBackup(ctx context.Context, id string, evidence StreamBackupResult) (VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupRun{}, err
	}
	if evidence.Object.Bytes <= 0 || len(evidence.Object.SHA256) != 64 ||
		len(evidence.SourceSHA256) != 64 || strings.TrimSpace(evidence.Format) == "" {
		return VolumeBackupRun{}, fmt.Errorf("%w: complete backup evidence is invalid", ErrVolumeRunInvalid)
	}
	now := s.now()
	var out VolumeBackupRun
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_backup_runs
		   SET state='succeeded', object_sha256=$1, source_sha256=$2,
		       object_bytes=$3, source_bytes=$4, archive_format=$5,
		       completed_at=$6, updated_at=$6, resource_version=resource_version+1
		 WHERE id=$7 AND state='running'
		RETURNING `+volumeBackupRunColumns,
		strings.ToLower(evidence.Object.SHA256), strings.ToLower(evidence.SourceSHA256),
		evidence.Object.Bytes, evidence.SourceBytes, evidence.Format, now, id,
	).Scan(volumeBackupRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeBackupRun{}, fmt.Errorf("complete volume backup run: %w", err)
	}
	return normalizeBackupRun(out), nil
}

func (s *PostgresVolumeRunStore) FailBackup(ctx context.Context, id, code, detail string) (VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupRun{}, err
	}
	now := s.now()
	var out VolumeBackupRun
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_backup_runs
		   SET state='failed', failure_code=$1, failure_detail=$2,
		       completed_at=$3, updated_at=$3, resource_version=resource_version+1
		 WHERE id=$4 AND state IN ('pending','running')
		RETURNING `+volumeBackupRunColumns,
		boundedRunText(code, 128), boundedRunText(detail, 4096), now, id,
	).Scan(volumeBackupRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeBackupRun{}, fmt.Errorf("fail volume backup run: %w", err)
	}
	return normalizeBackupRun(out), nil
}

func (s *PostgresVolumeRunStore) GetBackup(ctx context.Context, id string) (VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupRun{}, err
	}
	var out VolumeBackupRun
	err := s.Pool.QueryRow(ctx, `SELECT `+volumeBackupRunColumns+` FROM volume_backup_runs WHERE id=$1`, id).
		Scan(volumeBackupRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRun{}, ErrVolumeRunNotFound
	}
	if err != nil {
		return VolumeBackupRun{}, fmt.Errorf("get volume backup run: %w", err)
	}
	return normalizeBackupRun(out), nil
}

func (s *PostgresVolumeRunStore) ListBackups(ctx context.Context, projectID, environmentID, serviceID, volumeID string) ([]VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+volumeBackupRunColumns+`
		  FROM volume_backup_runs
		 WHERE ($1='' OR project_id=$1)
		   AND ($2='' OR environment_id=$2)
		   AND ($3='' OR service_id=$3)
		   AND ($4='' OR volume_id=$4)
		 ORDER BY created_at DESC, id DESC`,
		projectID, environmentID, serviceID, volumeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list volume backup runs: %w", err)
	}
	defer rows.Close()
	out := make([]VolumeBackupRun, 0)
	for rows.Next() {
		var value VolumeBackupRun
		if err := rows.Scan(volumeBackupRunScanTargets(&value)...); err != nil {
			return nil, err
		}
		out = append(out, normalizeBackupRun(value))
	}
	return out, rows.Err()
}

func (s *PostgresVolumeRunStore) CreateRestore(ctx context.Context, request CreateVolumeRestoreRun) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		id = s.newID("vrs")
	}
	now := s.now()
	var out VolumeRestoreRun
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO volume_restore_runs (
			id, backup_id, source_volume_id, project_id, environment_id,
			service_id, state, source_retained, created_at, updated_at
		)
		SELECT $1, b.id, b.volume_id, b.project_id, b.environment_id,
		       b.service_id, 'pending', TRUE, $2, $2
		  FROM volume_backup_runs b
		 WHERE b.id=$3 AND b.state='succeeded'
		RETURNING `+volumeRestoreRunColumns,
		id, now, request.BackupID,
	).Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunNotFound
	}
	if err != nil {
		return VolumeRestoreRun{}, fmt.Errorf("create volume restore run: %w", err)
	}
	return normalizeRestoreRun(out), nil
}

// CreateRestoreWork atomically creates a pending restore and its worker job.
func (s *PostgresVolumeRunStore) CreateRestoreWork(ctx context.Context, request CreateVolumeRestoreRun) (VolumeRestoreWork, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreWork{}, err
	}
	request.BackupID = strings.TrimSpace(request.BackupID)
	if request.BackupID == "" {
		return VolumeRestoreWork{}, fmt.Errorf("%w: backup_id is required", ErrVolumeRunInvalid)
	}
	runID := strings.TrimSpace(request.ID)
	if runID == "" {
		runID = s.newID("vrs")
	}
	payload, err := EncodeVolumeJobPayload(VolumeJobPayload{Kind: VolumeJobRestore, RunID: runID})
	if err != nil {
		return VolumeRestoreWork{}, err
	}
	now := s.now()
	jobID := s.newID("job")
	var out VolumeRestoreWork
	err = s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO volume_restore_runs (
				id, backup_id, source_volume_id, project_id, environment_id,
				service_id, state, source_retained, created_at, updated_at
			)
			SELECT $1, b.id, b.volume_id, b.project_id, b.environment_id,
			       b.service_id, 'pending', TRUE, $2, $2
			  FROM volume_backup_runs b
			 WHERE b.id=$3 AND b.state='succeeded'
			RETURNING `+volumeRestoreRunColumns,
			runID, now, request.BackupID,
		).Scan(volumeRestoreRunScanTargets(&out.Run)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVolumeRunNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO jobs (
				id, queue, state, partition_key, priority, available_at,
				payload, created_at, updated_at
			) VALUES ($1,$2,'pending',$3,10,$4,$5,$4,$4)`,
			jobID, string(queue.Cleanup), out.Run.ProjectID, now, payload,
		)
		return err
	})
	if err != nil {
		return VolumeRestoreWork{}, fmt.Errorf("create volume restore work: %w", err)
	}
	out.Run = normalizeRestoreRun(out.Run)
	out.Job = queue.Job{
		ID: jobID, Queue: queue.Cleanup, State: queue.JobQueued, Priority: 10,
		PartitionKey: out.Run.ProjectID, AvailableAt: now, Payload: payload,
	}
	return out, nil
}

func (s *PostgresVolumeRunStore) StartRestore(ctx context.Context, id string) (VolumeRestoreRun, error) {
	return s.transitionRestore(ctx, id, VolumeRestorePending, VolumeRestoreRunning)
}

// StageReplacement implements VolumeAttachmentStager. The store is scoped to
// one running restore so a helper container cannot stage arbitrary volumes.
func (s *PostgresVolumeRunStore) StageReplacement(ctx context.Context, restoreID, sourceVolumeName string, replacement ReplacementVolume) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := validateReplacementVolume(replacement); err != nil {
		return err
	}
	return s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var sourceID, projectID, environmentID, serviceID, displayName, mountPath, status string
		var sizeBytes int64
		err := tx.QueryRow(ctx, `
			SELECT r.source_volume_id, r.project_id, r.environment_id, r.service_id,
			       v.name, v.mount_path, v.size_bytes, v.status
			  FROM volume_restore_runs r
			  JOIN volumes v ON v.id=r.source_volume_id
			 WHERE r.id=$1 AND r.state='running'
			 FOR UPDATE OF r, v`,
			restoreID,
		).Scan(&sourceID, &projectID, &environmentID, &serviceID, &displayName, &mountPath, &sizeBytes, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVolumeRunConflict
		}
		if err != nil {
			return err
		}
		if sourceVolumeName != volume.DockerVolumeName(sourceID) ||
			replacement.ProjectID != projectID ||
			replacement.EnvironmentID != environmentID ||
			replacement.ServiceID != serviceID ||
			replacement.MountPath != mountPath ||
			replacement.SizeBytes != sizeBytes ||
			replacement.RuntimeName != volume.DockerVolumeName(replacement.ID) {
			return fmt.Errorf("%w: replacement scope or source does not match restore", ErrVolumeRunInvalid)
		}
		if replacement.DisplayName == "" {
			replacement.DisplayName = displayName + " restored"
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `
			INSERT INTO volumes (
				id, project_id, environment_id, service_id, name, size_bytes,
				mount_path, status, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,'restore_staged',$8,$8)`,
			replacement.ID, projectID, environmentID, serviceID,
			replacement.DisplayName, sizeBytes, mountPath, now,
		); err != nil {
			return fmt.Errorf("insert staged restore volume: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE volume_restore_runs
			   SET replacement_volume_id=$1, replacement_volume_name=$2, updated_at=$3,
			       resource_version=resource_version+1
			 WHERE id=$4 AND state='running'`,
			replacement.ID, replacement.RuntimeName, now, restoreID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrVolumeRunConflict
		}
		return nil
	})
}

func (s *PostgresVolumeRunStore) CompleteRestore(ctx context.Context, id string, evidence StreamRestoreResult) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	if len(evidence.RestoredSHA256) != 64 || evidence.RestoredBytes < 0 {
		return VolumeRestoreRun{}, fmt.Errorf("%w: restore evidence is invalid", ErrVolumeRunInvalid)
	}
	now := s.now()
	var out VolumeRestoreRun
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_restore_runs
		   SET state='staged', source_retained=TRUE, attachment_swap_staged=TRUE,
		       restored_sha256=$1, restored_bytes=$2, completed_at=$3,
		       updated_at=$3, resource_version=resource_version+1
		 WHERE id=$4 AND state='running'
		   AND replacement_volume_id IS NOT NULL
		   AND replacement_volume_name <> ''
		RETURNING `+volumeRestoreRunColumns,
		strings.ToLower(evidence.RestoredSHA256), evidence.RestoredBytes, now, id,
	).Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeRestoreRun{}, fmt.Errorf("complete volume restore run: %w", err)
	}
	return normalizeRestoreRun(out), nil
}

// ApplyRestore atomically switches durable attachment intent to the verified
// replacement. The source volume remains in restore_retained state and is not
// deleted; the caller then triggers a normal redeploy to enact the change.
func (s *PostgresVolumeRunStore) ApplyRestore(ctx context.Context, id string) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	var out VolumeRestoreRun
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var err error
		out, err = s.ApplyRestoreTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	return normalizeRestoreRun(out), nil
}

// ApplyRestoreTx performs the durable attachment-intent swap in the caller's
// transaction. It exists so the API can commit the swap and the immutable
// redeploy/outbox handoff as one database decision: either both become visible
// after a crash, or neither does.
func (s *PostgresVolumeRunStore) ApplyRestoreTx(ctx context.Context, tx *dbpg.Tx, id string) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	if tx == nil {
		return VolumeRestoreRun{}, errors.New("volume restore transaction is required")
	}
	current, err := getRestoreRunTx(ctx, tx, strings.TrimSpace(id), true)
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	if current.State != VolumeRestoreStaged || current.ReplacementVolumeID == "" {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	now := s.now()
	sourceTag, err := tx.Exec(ctx, `
		UPDATE volumes
		   SET status='restore_retained', updated_at=$1,
		       resource_version=resource_version+1
		 WHERE id=$2 AND status IN ('ready','attached')`,
		now, current.SourceVolumeID,
	)
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	if sourceTag.RowsAffected() != 1 {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	replacementTag, err := tx.Exec(ctx, `
		UPDATE volumes
		   SET status='ready', updated_at=$1, resource_version=resource_version+1
		 WHERE id=$2 AND status='restore_staged'`,
		now, current.ReplacementVolumeID,
	)
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	if replacementTag.RowsAffected() != 1 {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	var out VolumeRestoreRun
	if err := tx.QueryRow(ctx, `
		UPDATE volume_restore_runs
		   SET state='applied', updated_at=$1, resource_version=resource_version+1
		 WHERE id=$2 AND state='staged'
		RETURNING `+volumeRestoreRunColumns,
		now, current.ID,
	).Scan(volumeRestoreRunScanTargets(&out)...); err != nil {
		return VolumeRestoreRun{}, err
	}
	return normalizeRestoreRun(out), nil
}

// GetRestoreTx reads a restore inside a caller-owned transaction. lock=true is
// intended for coordinators that will make a state transition.
func (s *PostgresVolumeRunStore) GetRestoreTx(ctx context.Context, tx *dbpg.Tx, id string, lock bool) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	if tx == nil {
		return VolumeRestoreRun{}, errors.New("volume restore transaction is required")
	}
	return getRestoreRunTx(ctx, tx, strings.TrimSpace(id), lock)
}

func (s *PostgresVolumeRunStore) FailRestore(ctx context.Context, id, code, detail string) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	now := s.now()
	var out VolumeRestoreRun
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_restore_runs
		   SET state='failed', failure_code=$1, failure_detail=$2,
		       completed_at=$3, updated_at=$3, resource_version=resource_version+1
		 WHERE id=$4 AND state IN ('pending','running')
		RETURNING `+volumeRestoreRunColumns,
		boundedRunText(code, 128), boundedRunText(detail, 4096), now, id,
	).Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeRestoreRun{}, fmt.Errorf("fail volume restore run: %w", err)
	}
	return normalizeRestoreRun(out), nil
}

func (s *PostgresVolumeRunStore) GetRestore(ctx context.Context, id string) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	return getRestoreRun(s.Pool, ctx, id)
}

// ListRestores returns durable restore history for one source volume, newest
// first. It exposes staged/applied evidence after API or browser restarts.
func (s *PostgresVolumeRunStore) ListRestores(ctx context.Context, volumeID string) ([]VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" {
		return nil, fmt.Errorf("%w: volume_id is required", ErrVolumeRunInvalid)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+volumeRestoreRunColumns+`
		  FROM volume_restore_runs
		 WHERE source_volume_id=$1
		 ORDER BY created_at DESC,id DESC`,
		volumeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list volume restore runs: %w", err)
	}
	defer rows.Close()
	out := make([]VolumeRestoreRun, 0)
	for rows.Next() {
		var value VolumeRestoreRun
		if err := rows.Scan(volumeRestoreRunScanTargets(&value)...); err != nil {
			return nil, err
		}
		out = append(out, normalizeRestoreRun(value))
	}
	return out, rows.Err()
}

func (s *PostgresVolumeRunStore) transitionBackup(ctx context.Context, id string, from, to VolumeBackupRunState) (VolumeBackupRun, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupRun{}, err
	}
	var out VolumeBackupRun
	now := s.now()
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_backup_runs
		   SET state=$1, started_at=CASE WHEN $1='running' THEN $2 ELSE started_at END,
		       updated_at=$2, resource_version=resource_version+1
		 WHERE id=$3 AND state=$4
		RETURNING `+volumeBackupRunColumns,
		string(to), now, id, string(from),
	).Scan(volumeBackupRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeBackupRun{}, err
	}
	return normalizeBackupRun(out), nil
}

func (s *PostgresVolumeRunStore) transitionRestore(ctx context.Context, id string, from, to VolumeRestoreRunState) (VolumeRestoreRun, error) {
	if err := s.ready(); err != nil {
		return VolumeRestoreRun{}, err
	}
	var out VolumeRestoreRun
	now := s.now()
	err := s.Pool.QueryRow(ctx, `
		UPDATE volume_restore_runs
		   SET state=$1, started_at=CASE WHEN $1='running' THEN $2 ELSE started_at END,
		       updated_at=$2, resource_version=resource_version+1
		 WHERE id=$3 AND state=$4
		RETURNING `+volumeRestoreRunColumns,
		string(to), now, id, string(from),
	).Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunConflict
	}
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	return normalizeRestoreRun(out), nil
}

func getRestoreRun(conn dbpg.Conn, ctx context.Context, id string) (VolumeRestoreRun, error) {
	var out VolumeRestoreRun
	err := conn.QueryRow(ctx, `SELECT `+volumeRestoreRunColumns+` FROM volume_restore_runs WHERE id=$1`, id).
		Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunNotFound
	}
	if err != nil {
		return VolumeRestoreRun{}, err
	}
	return normalizeRestoreRun(out), nil
}

func getRestoreRunTx(ctx context.Context, tx *dbpg.Tx, id string, lock bool) (VolumeRestoreRun, error) {
	query := `SELECT ` + volumeRestoreRunColumns + ` FROM volume_restore_runs WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var out VolumeRestoreRun
	err := tx.QueryRow(ctx, query, id).Scan(volumeRestoreRunScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreRun{}, ErrVolumeRunNotFound
	}
	return normalizeRestoreRun(out), err
}

// RestoreStager binds the generic Docker archive driver to one durable restore.
type RestoreStager struct {
	Store     *PostgresVolumeRunStore
	RestoreID string
}

func (s RestoreStager) StageReplacement(ctx context.Context, sourceVolumeName string, replacement ReplacementVolume) error {
	if s.Store == nil || strings.TrimSpace(s.RestoreID) == "" {
		return errors.New("restore stager store and restore id are required")
	}
	return s.Store.StageReplacement(ctx, s.RestoreID, sourceVolumeName, replacement)
}

const volumeBackupRunColumns = `id, volume_id, project_id, environment_id, service_id,
	COALESCE(policy_id,''), trigger_kind, state, object_key, object_sha256,
	source_sha256, object_bytes, source_bytes, archive_format, locked,
	COALESCE(retention_until,'epoch'::timestamptz), failure_code, failure_detail,
	COALESCE(started_at,'epoch'::timestamptz),
	COALESCE(completed_at,'epoch'::timestamptz),
	created_at, updated_at`

func volumeBackupRunScanTargets(out *VolumeBackupRun) []any {
	return []any{
		&out.ID, &out.VolumeID, &out.ProjectID, &out.EnvironmentID, &out.ServiceID,
		&out.PolicyID, &out.TriggerKind, &out.State, &out.ObjectKey, &out.ObjectSHA256,
		&out.SourceSHA256, &out.ObjectBytes, &out.SourceBytes, &out.ArchiveFormat, &out.Locked,
		&out.RetentionUntil, &out.FailureCode, &out.FailureDetail, &out.StartedAt, &out.CompletedAt,
		&out.CreatedAt, &out.UpdatedAt,
	}
}

const volumeRestoreRunColumns = `id, backup_id, source_volume_id, project_id,
	environment_id, service_id, state, COALESCE(replacement_volume_id,''),
	replacement_volume_name, source_retained, attachment_swap_staged,
	restored_sha256, restored_bytes, failure_code, failure_detail,
	COALESCE(started_at,'epoch'::timestamptz),
	COALESCE(completed_at,'epoch'::timestamptz), created_at, updated_at`

func volumeRestoreRunScanTargets(out *VolumeRestoreRun) []any {
	return []any{
		&out.ID, &out.BackupID, &out.SourceVolumeID, &out.ProjectID,
		&out.EnvironmentID, &out.ServiceID, &out.State, &out.ReplacementVolumeID,
		&out.ReplacementVolumeName, &out.SourceRetained, &out.AttachmentSwapStaged,
		&out.RestoredSHA256, &out.RestoredBytes, &out.FailureCode, &out.FailureDetail,
		&out.StartedAt, &out.CompletedAt, &out.CreatedAt, &out.UpdatedAt,
	}
}

func normalizeBackupRun(value VolumeBackupRun) VolumeBackupRun {
	value.RetentionUntil = value.RetentionUntil.UTC()
	value.StartedAt = value.StartedAt.UTC()
	value.CompletedAt = value.CompletedAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value
}

func normalizeRestoreRun(value VolumeRestoreRun) VolumeRestoreRun {
	value.StartedAt = value.StartedAt.UTC()
	value.CompletedAt = value.CompletedAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func boundedRunText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
