// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/queue"
)

type VolumeBackupPolicy struct {
	ID               string     `json:"id"`
	VolumeID         string     `json:"volume_id"`
	ProjectID        string     `json:"project_id"`
	EnvironmentID    string     `json:"environment_id"`
	ServiceID        string     `json:"service_id"`
	ScheduleKind     string     `json:"schedule_kind"`
	Enabled          bool       `json:"enabled"`
	RetentionSeconds int64      `json:"retention_seconds"`
	NextRunAt        time.Time  `json:"next_run_at"`
	LastRunAt        *time.Time `json:"last_run_at,omitempty"`
	ResourceVersion  int64      `json:"resource_version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type CreateVolumeBackupPolicy struct {
	VolumeID         string
	ScheduleKind     string
	RetentionSeconds int64
}

func (s *PostgresVolumeRunStore) CreatePolicy(ctx context.Context, request CreateVolumeBackupPolicy) (VolumeBackupPolicy, error) {
	if err := s.ready(); err != nil {
		return VolumeBackupPolicy{}, err
	}
	request.VolumeID = strings.TrimSpace(request.VolumeID)
	request.ScheduleKind = strings.ToLower(strings.TrimSpace(request.ScheduleKind))
	if request.VolumeID == "" || !validPolicySchedule(request.ScheduleKind) ||
		request.RetentionSeconds <= 0 {
		return VolumeBackupPolicy{}, fmt.Errorf("%w: invalid backup policy", ErrVolumeRunInvalid)
	}
	now := s.now()
	next := nextPolicyTime(request.ScheduleKind, now)
	var out VolumeBackupPolicy
	err := s.Pool.QueryRow(ctx, `
INSERT INTO volume_backup_policies(
    id,volume_id,project_id,environment_id,service_id,schedule_kind,
    retention_seconds,next_run_at,created_at,updated_at
)
SELECT $1,volume.id,volume.project_id,volume.environment_id,volume.service_id,
       $2,$3,$4,$5,$5
  FROM volumes AS volume
 WHERE volume.id=$6 AND volume.status IN ('ready','attached')
RETURNING `+volumeBackupPolicyColumns,
		s.newID("vbp"), request.ScheduleKind, request.RetentionSeconds,
		next, now, request.VolumeID,
	).Scan(volumeBackupPolicyTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupPolicy{}, ErrVolumeRunNotFound
	}
	if err != nil {
		return VolumeBackupPolicy{}, fmt.Errorf("create volume backup policy: %w", err)
	}
	return normalizeVolumePolicy(out), nil
}

func (s *PostgresVolumeRunStore) ListPolicies(ctx context.Context, volumeID string) ([]VolumeBackupPolicy, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
SELECT `+volumeBackupPolicyColumns+`
  FROM volume_backup_policies
 WHERE volume_id=$1
 ORDER BY schedule_kind,id`, strings.TrimSpace(volumeID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := make([]VolumeBackupPolicy, 0)
	for rows.Next() {
		var policy VolumeBackupPolicy
		if err := rows.Scan(volumeBackupPolicyTargets(&policy)...); err != nil {
			return nil, err
		}
		policies = append(policies, normalizeVolumePolicy(policy))
	}
	return policies, rows.Err()
}

func (s *PostgresVolumeRunStore) DeletePolicy(ctx context.Context, volumeID, policyID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM volume_backup_policies WHERE id=$1 AND volume_id=$2`,
		strings.TrimSpace(policyID), strings.TrimSpace(volumeID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrVolumeRunNotFound
	}
	return nil
}

// EnqueueDuePolicies advances each policy and creates the backup run plus
// cleanup-queue job in the same transaction. A worker/API crash cannot leave a
// policy advanced without its work or enqueue the same scheduled occurrence
// twice.
func (s *PostgresVolumeRunStore) EnqueueDuePolicies(ctx context.Context, now time.Time, limit int) ([]VolumeBackupWork, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("backup policy batch size must be between 1 and 100")
	}
	now = now.UTC()
	works := make([]VolumeBackupWork, 0, limit)
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		rows, err := tx.Query(ctx, `
SELECT `+volumeBackupPolicyColumns+`
  FROM volume_backup_policies
 WHERE enabled=TRUE AND next_run_at <= $1
 ORDER BY next_run_at,id
 FOR UPDATE SKIP LOCKED
 LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		policies := make([]VolumeBackupPolicy, 0, limit)
		for rows.Next() {
			var policy VolumeBackupPolicy
			if err := rows.Scan(volumeBackupPolicyTargets(&policy)...); err != nil {
				rows.Close()
				return err
			}
			policies = append(policies, normalizeVolumePolicy(policy))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		for _, policy := range policies {
			runID := s.newID("vbk")
			jobID := s.newID("job")
			payload, err := EncodeVolumeJobPayload(VolumeJobPayload{
				Kind: VolumeJobBackup, RunID: runID,
			})
			if err != nil {
				return err
			}
			request := CreateVolumeBackupRun{
				ID: runID, VolumeID: policy.VolumeID, PolicyID: policy.ID,
				TriggerKind:    policy.ScheduleKind,
				ObjectKey:      "volumes/" + policy.VolumeID + "/" + runID + ".mnbk",
				RetentionUntil: now.Add(time.Duration(policy.RetentionSeconds) * time.Second),
			}
			var work VolumeBackupWork
			if err := createBackupWorkTx(ctx, tx, request, runID, jobID, payload, now, &work); err != nil {
				return err
			}
			next := policy.NextRunAt
			for !next.After(now) {
				next = nextPolicyTime(policy.ScheduleKind, next)
			}
			tag, err := tx.Exec(ctx, `
UPDATE volume_backup_policies
   SET last_run_at=$1,next_run_at=$2,updated_at=$1,
       resource_version=resource_version+1
 WHERE id=$3 AND next_run_at=$4`,
				now, next, policy.ID, policy.NextRunAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrVolumeRunConflict
			}
			work.Run = normalizeBackupRun(work.Run)
			work.Job = queue.Job{
				ID: jobID, Queue: queue.Cleanup, State: queue.JobQueued,
				PartitionKey: work.Run.ProjectID, AvailableAt: now, Payload: payload,
			}
			works = append(works, work)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue due backup policies: %w", err)
	}
	return works, nil
}

func validPolicySchedule(schedule string) bool {
	switch schedule {
	case "daily", "weekly", "monthly":
		return true
	default:
		return false
	}
}

// nextPolicyTime returns the next UTC calendar boundary strictly after at.
func nextPolicyTime(schedule string, at time.Time) time.Time {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	switch schedule {
	case "weekly":
		days := (8 - int(day.Weekday())) % 7
		if days == 0 {
			days = 7
		}
		return day.AddDate(0, 0, days)
	case "monthly":
		return time.Date(at.Year(), at.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	default:
		return day.AddDate(0, 0, 1)
	}
}

const volumeBackupPolicyColumns = `
id,volume_id,project_id,environment_id,service_id,schedule_kind,enabled,
retention_seconds,next_run_at,last_run_at,created_at,updated_at,resource_version`

func volumeBackupPolicyTargets(policy *VolumeBackupPolicy) []any {
	return []any{
		&policy.ID, &policy.VolumeID, &policy.ProjectID, &policy.EnvironmentID,
		&policy.ServiceID, &policy.ScheduleKind, &policy.Enabled,
		&policy.RetentionSeconds, &policy.NextRunAt, &policy.LastRunAt,
		&policy.CreatedAt, &policy.UpdatedAt, &policy.ResourceVersion,
	}
}

func normalizeVolumePolicy(policy VolumeBackupPolicy) VolumeBackupPolicy {
	policy.NextRunAt = policy.NextRunAt.UTC()
	policy.CreatedAt = policy.CreatedAt.UTC()
	policy.UpdatedAt = policy.UpdatedAt.UTC()
	if policy.LastRunAt != nil {
		value := policy.LastRunAt.UTC()
		policy.LastRunAt = &value
	}
	return policy
}
