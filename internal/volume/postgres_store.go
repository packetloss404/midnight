// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresStore is the production Store backed by the volumes /
// volume_attachments tables (migrations/0008_volumes.up.sql).
type PostgresStore struct {
	Pool      *pg.Pool
	Now       func() time.Time
	IDFactory func() string
}

// NewPostgresStore wires a PostgresStore against pool.
func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{
		Pool:      pool,
		Now:       func() time.Time { return time.Now().UTC() },
		IDFactory: defaultPostgresVolumeID,
	}
}

func defaultPostgresVolumeID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("vol_%d", time.Now().UnixNano())
	}
	return "vol_" + hex.EncodeToString(b[:])
}

func defaultPostgresAttachmentID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("vat_%d", time.Now().UnixNano())
	}
	return "vat_" + hex.EncodeToString(b[:])
}

func (s *PostgresStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// Create implements Store.
func (s *PostgresStore) Create(ctx context.Context, in VolumeCreateInput) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	if err := validateVolumeCreateInput(&in); err != nil {
		return Volume{}, err
	}
	now := s.now()
	v := Volume{
		ID:              s.IDFactory(),
		ProjectID:       in.ProjectID,
		EnvironmentID:   in.EnvironmentID,
		ServiceID:       in.ServiceID,
		Name:            in.Name,
		SizeBytes:       in.SizeBytes,
		MountPath:       in.MountPath,
		Status:          StatusProvisioning,
		CreatedAt:       now,
		UpdatedAt:       now,
		ResourceVersion: 1,
	}
	if err := insertVolume(ctx, s.Pool, v); err != nil {
		return Volume{}, err
	}
	return v, nil
}

func validateVolumeCreateInput(in *VolumeCreateInput) error {
	if strings.TrimSpace(in.ServiceID) == "" {
		return fmt.Errorf("%w: service_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.EnvironmentID) == "" {
		return fmt.Errorf("%w: environment_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return fmt.Errorf("%w: project_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrVolumeInvalid)
	}
	if in.SizeBytes < 0 {
		return fmt.Errorf("%w: size_bytes must be non-negative", ErrVolumeInvalid)
	}
	in.MountPath = strings.TrimSpace(in.MountPath)
	if in.MountPath != "" && !validMountPath(in.MountPath) {
		return fmt.Errorf("%w: mount_path must be an absolute clean non-root path", ErrVolumeInvalid)
	}
	return nil
}

func insertVolume(ctx context.Context, conn pg.Conn, v Volume) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO volumes (id, project_id, environment_id, service_id, name, size_bytes, mount_path, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		v.ID, v.ProjectID, v.EnvironmentID, v.ServiceID, v.Name, v.SizeBytes, v.MountPath, string(v.Status), v.CreatedAt, v.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: volume %q already exists on (%s,%s)", ErrVolumeConflict, v.Name, v.ServiceID, v.EnvironmentID)
		}
		return fmt.Errorf("volume insert: %w", err)
	}
	return nil
}

// CreateWithIDInTx creates canonical volume intent inside a caller-owned
// serializable transaction. The caller supplies a previously reviewed stable
// ID so transaction retries cannot mint a different resource.
func (s *PostgresStore) CreateWithIDInTx(ctx context.Context, tx *pg.Tx, id string, in VolumeCreateInput) (Volume, error) {
	if tx == nil || strings.TrimSpace(id) == "" {
		return Volume{}, fmt.Errorf("%w: transaction and id are required", ErrVolumeInvalid)
	}
	if err := validateVolumeCreateInput(&in); err != nil {
		return Volume{}, err
	}
	now := s.now()
	v := Volume{ID: id, ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID, ServiceID: in.ServiceID, Name: in.Name, SizeBytes: in.SizeBytes, MountPath: in.MountPath, Status: StatusProvisioning, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}
	if err := insertVolume(ctx, tx, v); err != nil {
		return Volume{}, err
	}
	return v, nil
}

// GetInTx reads and optionally locks a volume through the caller's transaction.
func (s *PostgresStore) GetInTx(ctx context.Context, tx *pg.Tx, id string, lock bool) (Volume, error) {
	if tx == nil {
		return Volume{}, errors.New("volume get in tx: transaction is required")
	}
	query := volumeSelectSQL + ` WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanVolume(tx.QueryRow(ctx, query, id))
}

// ResizeCASInTx grows a volume only when its exact resource version matches.
func (s *PostgresStore) ResizeCASInTx(ctx context.Context, tx *pg.Tx, id string, expectedVersion, newSize int64) (Volume, error) {
	current, err := s.GetInTx(ctx, tx, id, true)
	if err != nil {
		return Volume{}, err
	}
	if current.ResourceVersion != expectedVersion {
		return Volume{}, ErrVolumeConflict
	}
	if newSize < current.SizeBytes {
		return Volume{}, ErrVolumeResizeDownUnsupported
	}
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE volumes SET size_bytes=$1,updated_at=$2,resource_version=resource_version+1 WHERE id=$3 AND resource_version=$4`, newSize, now, id, expectedVersion)
	if err != nil {
		return Volume{}, fmt.Errorf("volume resize: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return Volume{}, ErrVolumeConflict
	}
	current.SizeBytes, current.UpdatedAt, current.ResourceVersion = newSize, now, expectedVersion+1
	return current, nil
}

// DeleteCASInTx soft-deletes unattached volume intent under exact version CAS.
func (s *PostgresStore) DeleteCASInTx(ctx context.Context, tx *pg.Tx, id string, expectedVersion int64) error {
	current, err := s.GetInTx(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if current.ResourceVersion != expectedVersion {
		return ErrVolumeConflict
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM volume_attachments WHERE volume_id=$1 AND status IN ('pending','active')`, id).Scan(&count); err != nil {
		return fmt.Errorf("attachment count: %w", err)
	}
	if count > 0 {
		return ErrVolumeAttached
	}
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE volumes SET status='soft_deleted',soft_deleted_at=$1,hard_delete_at=$2,updated_at=$1,resource_version=resource_version+1 WHERE id=$3 AND resource_version=$4`, now, now.Add(SoftDeleteGrace), id, expectedVersion)
	if err != nil {
		return fmt.Errorf("volume soft-delete: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrVolumeConflict
	}
	return nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, id string) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	row := s.Pool.QueryRow(ctx, volumeSelectSQL+` WHERE id = $1`, id)
	return scanVolume(row)
}

// List implements Store.
func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]Volume, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := make([]any, 0, 4)
	clauses := []string{}
	if filter.ProjectID != "" {
		args = append(args, filter.ProjectID)
		clauses = append(clauses, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if filter.ServiceID != "" {
		args = append(args, filter.ServiceID)
		clauses = append(clauses, fmt.Sprintf("service_id = $%d", len(args)))
	}
	if filter.EnvironmentID != "" {
		args = append(args, filter.EnvironmentID)
		clauses = append(clauses, fmt.Sprintf("environment_id = $%d", len(args)))
	}
	if !filter.IncludeSoft {
		clauses = append(clauses, "status NOT IN ('restore_staged','restore_retained','soft_deleted','deleted')")
	}
	query := volumeSelectSQL
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at ASC, id ASC"

	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("volume list: %w", err)
	}
	defer rows.Close()
	out := make([]Volume, 0)
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListByService implements Store.
func (s *PostgresStore) ListByService(ctx context.Context, serviceID string) ([]Volume, error) {
	return s.List(ctx, ListFilter{ServiceID: serviceID})
}

// ListByProject implements Store.
func (s *PostgresStore) ListByProject(ctx context.Context, projectID string) ([]Volume, error) {
	return s.List(ctx, ListFilter{ProjectID: projectID})
}

// Resize implements Store.
func (s *PostgresStore) Resize(ctx context.Context, id string, newSize int64) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	if newSize < 0 {
		return Volume{}, fmt.Errorf("%w: size_bytes must be non-negative", ErrVolumeInvalid)
	}
	var out Volume
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		current, err := scanVolume(tx.QueryRow(ctx, volumeSelectSQL+` WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if newSize < current.SizeBytes {
			return ErrVolumeResizeDownUnsupported
		}
		now := s.now()
		_, err = tx.Exec(ctx,
			`UPDATE volumes SET size_bytes = $1, updated_at = $2, resource_version = resource_version + 1 WHERE id = $3`,
			newSize, now, id,
		)
		if err != nil {
			return fmt.Errorf("volume resize: %w", err)
		}
		current.SizeBytes = newSize
		current.UpdatedAt = now
		current.ResourceVersion++
		out = current
		return nil
	})
	if err != nil {
		return Volume{}, err
	}
	return out, nil
}

// MarkReady implements Store.
func (s *PostgresStore) MarkReady(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE volumes SET status = CASE WHEN status IN ('soft_deleted','deleted','attached') THEN status ELSE 'ready' END,
			updated_at = $1, resource_version = resource_version + 1 WHERE id = $2`,
		s.now(), id,
	)
	if err != nil {
		return fmt.Errorf("volume mark ready: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVolumeNotFound
	}
	return nil
}

// Delete implements Store.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var count int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM volume_attachments WHERE volume_id = $1 AND status IN ('pending','active')`,
			id,
		).Scan(&count); err != nil {
			return fmt.Errorf("attachment count: %w", err)
		}
		if count > 0 {
			return ErrVolumeAttached
		}
		now := s.now()
		hard := now.Add(SoftDeleteGrace)
		tag, err := tx.Exec(ctx,
			`UPDATE volumes
			   SET status = 'soft_deleted',
			       soft_deleted_at = $1,
			       hard_delete_at = $2,
			       updated_at = $1,
			       resource_version = resource_version + 1
			 WHERE id = $3`,
			now, hard, id,
		)
		if err != nil {
			return fmt.Errorf("volume soft-delete: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrVolumeNotFound
		}
		return nil
	})
}

// HasActiveAttachment implements Store.
func (s *PostgresStore) HasActiveAttachment(ctx context.Context, volumeID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var count int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM volume_attachments WHERE volume_id = $1 AND status IN ('pending','active')`,
		volumeID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("attachment count: %w", err)
	}
	return count > 0, nil
}

// RecordAttachment implements Store.
func (s *PostgresStore) RecordAttachment(ctx context.Context, attach Attachment) (Attachment, error) {
	if err := ctx.Err(); err != nil {
		return Attachment{}, err
	}
	if strings.TrimSpace(attach.VolumeID) == "" || strings.TrimSpace(attach.DeploymentID) == "" {
		return Attachment{}, fmt.Errorf("%w: deployment_id and volume_id are required", ErrVolumeInvalid)
	}
	if attach.ID == "" {
		attach.ID = defaultPostgresAttachmentID()
	}
	if attach.AttachedAt.IsZero() {
		attach.AttachedAt = s.now()
	}
	if attach.Status == "" {
		attach.Status = AttachmentPending
	}
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		// Serialize every claim for the durable volume before checking the
		// partial-unique invariant. This makes the error deterministic instead
		// of depending on which concurrent INSERT reaches PostgreSQL first.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "midnight_volume:"+attach.VolumeID); err != nil {
			return fmt.Errorf("attachment lock: %w", err)
		}
		vol, err := scanVolume(tx.QueryRow(ctx, volumeSelectSQL+` WHERE id = $1 FOR UPDATE`, attach.VolumeID))
		if err != nil {
			return err
		}
		if attach.ProjectID != vol.ProjectID || attach.ServiceID != vol.ServiceID || attach.EnvironmentID != vol.EnvironmentID {
			return fmt.Errorf("%w: attachment scope does not match volume", ErrVolumeInvalid)
		}
		if !validMountPath(attach.MountPath) || (vol.MountPath != "" && attach.MountPath != vol.MountPath) {
			return fmt.Errorf("%w: attachment mount_path does not match volume", ErrVolumeInvalid)
		}
		var writerID, deploymentID, instanceID string
		writerErr := tx.QueryRow(ctx, `
			SELECT id, deployment_id, instance_id
			  FROM volume_attachments
			 WHERE volume_id = $1
			   AND status IN ('pending','active')
			 FOR UPDATE`,
			attach.VolumeID,
		).Scan(&writerID, &deploymentID, &instanceID)
		if writerErr != nil && !errors.Is(writerErr, pgx.ErrNoRows) {
			return fmt.Errorf("attachment writer lookup: %w", writerErr)
		}
		if writerErr == nil && (deploymentID != attach.DeploymentID || instanceID != attach.InstanceID) {
			return ErrVolumeAttached
		}
		if writerErr == nil {
			attach.ID = writerID
		} else {
			var existingID string
			err := tx.QueryRow(ctx, `
				SELECT id FROM volume_attachments
				 WHERE deployment_id = $1
				   AND instance_id = $2
				   AND volume_id = $3
				 FOR UPDATE`,
				attach.DeploymentID, attach.InstanceID, attach.VolumeID,
			).Scan(&existingID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("attachment idempotency lookup: %w", err)
			}
			if err == nil {
				attach.ID = existingID
			}
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO volume_attachments (
				id, volume_id, deployment_id, instance_id, project_id, service_id,
				environment_id, mount_path, status, sequence, attached_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (deployment_id, instance_id, volume_id) DO UPDATE
			SET mount_path = EXCLUDED.mount_path,
			    status = EXCLUDED.status,
			    sequence = EXCLUDED.sequence,
			    attached_at = EXCLUDED.attached_at,
			    detached_at = NULL`,
			attach.ID, attach.VolumeID, attach.DeploymentID, attach.InstanceID,
			attach.ProjectID, attach.ServiceID, attach.EnvironmentID,
			attach.MountPath, string(attach.Status), attach.Sequence, attach.AttachedAt,
		)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Attachment{}, ErrVolumeAttached
		}
		return Attachment{}, fmt.Errorf("attachment upsert: %w", err)
	}
	return attach, nil
}

// ActivateAttachment implements Store.
func (s *PostgresStore) ActivateAttachment(ctx context.Context, attachmentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var volumeID, status string
		if err := tx.QueryRow(ctx,
			`SELECT volume_id, status FROM volume_attachments WHERE id = $1 FOR UPDATE`,
			attachmentID,
		).Scan(&volumeID, &status); errors.Is(err, pgx.ErrNoRows) {
			return ErrVolumeNotFound
		} else if err != nil {
			return fmt.Errorf("attachment activate lookup: %w", err)
		}
		if status != string(AttachmentPending) && status != string(AttachmentActive) {
			return fmt.Errorf("%w: detached attachment cannot be activated", ErrVolumeInvalid)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "midnight_volume:"+volumeID); err != nil {
			return fmt.Errorf("attachment lock: %w", err)
		}
		var conflict bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM volume_attachments
				 WHERE volume_id = $1
				   AND id <> $2
				   AND status IN ('pending','active')
			)`,
			volumeID, attachmentID,
		).Scan(&conflict); err != nil {
			return fmt.Errorf("attachment conflict lookup: %w", err)
		}
		if conflict {
			return ErrVolumeAttached
		}
		if _, err := tx.Exec(ctx,
			`UPDATE volume_attachments
			    SET status = 'active', detached_at = NULL
			  WHERE id = $1 AND status IN ('pending','active')`,
			attachmentID,
		); err != nil {
			return fmt.Errorf("attachment activate: %w", err)
		}
		_, err := tx.Exec(ctx,
			`UPDATE volumes
			    SET status = CASE WHEN status IN ('soft_deleted','deleted') THEN status ELSE 'attached' END,
			        updated_at = $1,
			        resource_version = resource_version + 1
			  WHERE id = $2`,
			s.now(), volumeID,
		)
		return err
	})
}

// MarkAttachmentDetached implements Store.
func (s *PostgresStore) MarkAttachmentDetached(ctx context.Context, attachmentID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if at.IsZero() {
		at = s.now()
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var volumeID string
		err := tx.QueryRow(ctx,
			`UPDATE volume_attachments
			    SET status = 'detached', detached_at = $1
			  WHERE id = $2
			  RETURNING volume_id`,
			at, attachmentID,
		).Scan(&volumeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("attachment detach: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE volumes
			   SET status = CASE
			         WHEN status = 'attached' AND NOT EXISTS (
			             SELECT 1 FROM volume_attachments
			              WHERE volume_id = $1
			                AND status IN ('pending','active')
			         ) THEN 'ready'
			         ELSE status
			       END,
			       updated_at = $2,
			       resource_version = resource_version + 1
			 WHERE id = $1`,
			volumeID, s.now(),
		)
		return err
	})
}

// ListAttachments implements Store.
func (s *PostgresStore) ListAttachments(ctx context.Context, volumeID string) ([]Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, volume_id, deployment_id, instance_id, project_id, service_id, environment_id,
		       mount_path, status, sequence, attached_at, COALESCE(detached_at, '0001-01-01'::timestamptz)
		  FROM volume_attachments WHERE volume_id = $1 ORDER BY sequence ASC, id ASC`,
		volumeID,
	)
	if err != nil {
		return nil, fmt.Errorf("attachment list: %w", err)
	}
	defer rows.Close()
	out := make([]Attachment, 0)
	for rows.Next() {
		var a Attachment
		var detachedAt time.Time
		var status string
		if err := rows.Scan(&a.ID, &a.VolumeID, &a.DeploymentID, &a.InstanceID,
			&a.ProjectID, &a.ServiceID, &a.EnvironmentID, &a.MountPath, &status, &a.Sequence,
			&a.AttachedAt, &detachedAt); err != nil {
			return nil, fmt.Errorf("attachment scan: %w", err)
		}
		a.Status = AttachmentStatus(status)
		if !detachedAt.IsZero() && detachedAt.Year() > 1 {
			a.DetachedAt = detachedAt
		}
		a.AttachedAt = a.AttachedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// volumeSelectSQL is the canonical projection used by Get/List/Resize.
const volumeSelectSQL = `SELECT id, project_id, environment_id, service_id, name,
	size_bytes, mount_path, status, host_fs_type, quota_project_id,
	COALESCE(soft_deleted_at, '0001-01-01'::timestamptz),
	COALESCE(hard_delete_at, '0001-01-01'::timestamptz),
	COALESCE(deleted_at, '0001-01-01'::timestamptz),
	created_at, updated_at, resource_version
	FROM volumes`

// rowScanner is the minimal interface QueryRow + Rows.Next share.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanVolume(row rowScanner) (Volume, error) {
	var v Volume
	var statusText string
	var hostFS string
	var quotaProject *int32
	var softDeleted, hardDelete, deletedAt time.Time
	err := row.Scan(&v.ID, &v.ProjectID, &v.EnvironmentID, &v.ServiceID, &v.Name,
		&v.SizeBytes, &v.MountPath, &statusText, &hostFS, &quotaProject,
		&softDeleted, &hardDelete, &deletedAt,
		&v.CreatedAt, &v.UpdatedAt, &v.ResourceVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Volume{}, ErrVolumeNotFound
	}
	if err != nil {
		return Volume{}, fmt.Errorf("volume scan: %w", err)
	}
	v.Status = Status(statusText)
	v.HostFSType = hostFS
	if quotaProject != nil {
		q := uint32(*quotaProject)
		v.QuotaProjectID = &q
	}
	if softDeleted.Year() > 1 {
		v.SoftDeletedAt = softDeleted.UTC()
	}
	if hardDelete.Year() > 1 {
		v.HardDeleteAt = hardDelete.UTC()
	}
	if deletedAt.Year() > 1 {
		v.DeletedAt = deletedAt.UTC()
	}
	v.CreatedAt = v.CreatedAt.UTC()
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
