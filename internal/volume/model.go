// Package volume implements the Phase 1 §1.9 volume model: tenant-
// scoped persistent volumes with single-writer enforcement, prjquota
// reservation, and 7-day soft-delete grace.
//
// The package is the storage-graph counterpart to internal/runtime/
// docker's VolumeCreate / VolumeAttach / VolumeDetach methods (the
// runtime adapter). Lane 1 owns the Postgres schema; Lane 2 owns the
// host-side Docker volume; this package is the deployment-orchestrator
// glue that ties them together with the panel-locked lifecycle.
package volume

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Status is the lifecycle classification for a volume row.
type Status string

const (
	// StatusProvisioning marks a volume that has been created in
	// Postgres but not yet realized on the host.
	StatusProvisioning Status = "provisioning"
	// StatusReady is the steady-state value; the volume exists on
	// the host and is attachable.
	StatusReady Status = "ready"
	// StatusAttached records that the volume is currently bound to
	// a deployment instance via volume_attachments.
	StatusAttached Status = "attached"
	// StatusRestoreStaged is a verified replacement created by a restore.
	// It is excluded from ordinary deployment attachment resolution until the
	// operator applies the staged swap.
	StatusRestoreStaged Status = "restore_staged"
	// StatusRestoreRetained is the previous volume after an applied restore.
	// Its bytes remain available for rollback, but new deployments do not
	// attach it implicitly.
	StatusRestoreRetained Status = "restore_retained"
	// StatusSoftDeleted is the 7-day grace state: the row remains
	// in Postgres and the on-host directory is preserved, but new
	// attachments are refused.
	StatusSoftDeleted Status = "soft_deleted"
	// StatusDeleted is the terminal state recorded after the soft-
	// delete grace expires. Does NOT remove the row; only flips it
	// to a terminal classification so the cleanup audit row exists.
	StatusDeleted Status = "deleted"
)

// AttachmentStatus is the lifecycle classification for one row in
// the volume_attachments table.
type AttachmentStatus string

const (
	AttachmentPending  AttachmentStatus = "pending"
	AttachmentActive   AttachmentStatus = "active"
	AttachmentDetached AttachmentStatus = "detached"
)

// Volume is the in-memory shape of a row in the `volumes` table. The
// Postgres schema lives in migrations/0008_volumes.up.sql.
type Volume struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServiceID     string `json:"service_id"`
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	// MountPath is the authoritative container path for this volume. It lives
	// on the volume row so deploy workers can recover intent after an API or
	// worker restart without consulting an in-memory sidecar.
	MountPath       string    `json:"mount_path"`
	Status          Status    `json:"status"`
	HostFSType      string    `json:"host_fs_type,omitempty"`
	QuotaProjectID  *uint32   `json:"quota_project_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	SoftDeletedAt   time.Time `json:"soft_deleted_at,omitempty"`
	HardDeleteAt    time.Time `json:"hard_delete_at,omitempty"`
	DeletedAt       time.Time `json:"deleted_at,omitempty"`
	ResourceVersion int64     `json:"resource_version"`
}

// Attachment is the in-memory shape of a row in volume_attachments.
type Attachment struct {
	ID            string           `json:"id"`
	VolumeID      string           `json:"volume_id"`
	DeploymentID  string           `json:"deployment_id"`
	InstanceID    string           `json:"instance_id"`
	ProjectID     string           `json:"project_id"`
	ServiceID     string           `json:"service_id"`
	EnvironmentID string           `json:"environment_id"`
	MountPath     string           `json:"mount_path"`
	Status        AttachmentStatus `json:"status"`
	Sequence      int64            `json:"sequence"`
	AttachedAt    time.Time        `json:"attached_at"`
	DetachedAt    time.Time        `json:"detached_at,omitempty"`
}

// Validate rejects malformed Volume rows.
func (v Volume) Validate() error {
	if strings.TrimSpace(v.ID) == "" {
		return errors.New("volume id is required")
	}
	if strings.TrimSpace(v.ServiceID) == "" {
		return errors.New("volume service_id is required")
	}
	if strings.TrimSpace(v.EnvironmentID) == "" {
		return errors.New("volume environment_id is required")
	}
	if strings.TrimSpace(v.ProjectID) == "" {
		return errors.New("volume project_id is required")
	}
	if v.SizeBytes < 0 {
		return errors.New("volume size_bytes must be non-negative")
	}
	if v.MountPath != "" && !validMountPath(v.MountPath) {
		return errors.New("volume mount_path must be an absolute clean path")
	}
	switch v.Status {
	case StatusProvisioning, StatusReady, StatusAttached, StatusRestoreStaged, StatusRestoreRetained, StatusSoftDeleted, StatusDeleted:
	default:
		return fmt.Errorf("volume status %q is invalid", v.Status)
	}
	return nil
}

func validMountPath(mountPath string) bool {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" || mountPath == "/" || !strings.HasPrefix(mountPath, "/") || path.Clean(mountPath) != mountPath {
		return false
	}
	for _, r := range mountPath {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// SoftDeleteGrace is the panel-locked 7-day grace before a soft-
// deleted volume is reaped (§1.9). The grace is configurable down to
// 24h, never below.
const SoftDeleteGrace = 7 * 24 * time.Hour

// MarkSoftDeleted flips a Volume into the soft-delete state with the
// hard-delete timestamp scheduled SoftDeleteGrace later.
func (v *Volume) MarkSoftDeleted(now time.Time) {
	v.Status = StatusSoftDeleted
	v.SoftDeletedAt = now
	v.HardDeleteAt = now.Add(SoftDeleteGrace)
	v.UpdatedAt = now
}

// CanReap reports whether the volume is past its soft-delete grace.
func (v Volume) CanReap(now time.Time) bool {
	if v.Status != StatusSoftDeleted {
		return false
	}
	return !v.HardDeleteAt.IsZero() && !now.Before(v.HardDeleteAt)
}

// SingleWriterAdvisoryKey returns the canonical key passed to
// pg_advisory_xact_lock(hashtext(...)) so deploys that mount a volume
// serialize per (service_id, environment_id). Matches the §1.9 brief.
func SingleWriterAdvisoryKey(serviceID, environmentID string) string {
	return fmt.Sprintf("midnight_volume:%s:%s", serviceID, environmentID)
}
