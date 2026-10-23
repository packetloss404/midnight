// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors surfaced by every Store implementation. Callers translate
// these into the typed API error envelope.
var (
	// ErrVolumeNotFound is returned when a Volume lookup misses.
	ErrVolumeNotFound = errors.New("volume not found")
	// ErrVolumeConflict is returned when a unique constraint (e.g.
	// (service_id, environment_id, name)) is violated.
	ErrVolumeConflict = errors.New("volume conflict")
	// ErrVolumeInvalid is returned for validation failures at the store
	// boundary (missing service id, negative size, etc.).
	ErrVolumeInvalid = errors.New("volume invalid")
	// ErrVolumeAttached is returned when a delete is attempted on a
	// volume that still has a pending/active attachment or when a different
	// deployment already owns its single-writer lease.
	ErrVolumeAttached = errors.New("volume has active attachment")
	// ErrVolumeResizeDownUnsupported rejects resize requests that would
	// shrink an existing volume. Mirrors Railway's constraint.
	ErrVolumeResizeDownUnsupported = errors.New("volume resize down unsupported")
)

// VolumeCreateInput is the canonical create input shared by the in-memory
// and Postgres stores. ProjectID is denormalized off (service, environment)
// so the store does not need to join through services on every list.
type VolumeCreateInput struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Name          string
	SizeBytes     int64
	MountPath     string
}

// AttachmentInput captures one volume attachment intent. It is the input to
// Manager.EnsureForDeployment.
type AttachmentInput struct {
	VolumeID     string
	DeploymentID string
	InstanceID   string
	MountPath    string
	Sequence     int64
	ReadOnly     bool
}

// AttachedVolume is the materialized result of EnsureForDeployment. The
// scheduler reads (DockerVolumeName, MountPath) to populate the container's
// HostConfig.Mounts. Same VolumeID across redeploys yields the same
// DockerVolumeName so data persists.
type AttachedVolume struct {
	VolumeID         string
	DockerVolumeName string
	MountPath        string
	ReadOnly         bool
	ServiceID        string
	EnvironmentID    string
}

// ListFilter narrows a List query. An empty filter returns every volume.
type ListFilter struct {
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	IncludeSoft   bool
}

// Store is the persistence contract the API + Manager consume. The
// in-memory implementation backs unit tests and developer-mode boots; the
// Postgres implementation backs production.
//
// All methods MUST be safe for concurrent use.
type Store interface {
	// Create inserts a new volume row in the StatusProvisioning state and
	// returns it. Idempotency on (service_id, environment_id, name) is
	// surfaced as ErrVolumeConflict.
	Create(ctx context.Context, in VolumeCreateInput) (Volume, error)
	// Get returns the volume row for id or ErrVolumeNotFound.
	Get(ctx context.Context, id string) (Volume, error)
	// List returns matching volumes ordered by created_at, id.
	List(ctx context.Context, filter ListFilter) ([]Volume, error)
	// ListByService is a thin wrapper for the most common API filter.
	ListByService(ctx context.Context, serviceID string) ([]Volume, error)
	// ListByProject returns every volume bound to projectID.
	ListByProject(ctx context.Context, projectID string) ([]Volume, error)
	// Resize updates size_bytes. Returns ErrVolumeResizeDownUnsupported
	// when newSize is less than the current size, ErrVolumeNotFound when
	// the row is missing.
	Resize(ctx context.Context, id string, newSize int64) (Volume, error)
	// MarkReady transitions a provisioning volume into the ready state
	// after the Docker volume has been materialized.
	MarkReady(ctx context.Context, id string) error
	// Delete soft-deletes a volume. Returns ErrVolumeAttached when there
	// is an active attachment.
	Delete(ctx context.Context, id string) error
	// HasActiveAttachment reports whether any attachment row for the
	// volume is in the pending or active state.
	HasActiveAttachment(ctx context.Context, volumeID string) (bool, error)
	// RecordAttachment upserts an attachment row keyed by
	// (deployment_id, instance_id, volume_id). The unique index in
	// migrations/0008 keeps duplicate inserts deterministic.
	RecordAttachment(ctx context.Context, attach Attachment) (Attachment, error)
	// ActivateAttachment transitions a successfully scheduled pending
	// attachment to active. The store must reject activation if another writer
	// owns the volume.
	ActivateAttachment(ctx context.Context, attachmentID string) error
	// MarkAttachmentDetached transitions an attachment row to the
	// detached state with the supplied timestamp.
	MarkAttachmentDetached(ctx context.Context, attachmentID string, at time.Time) error
	// ListAttachments returns every attachment for the supplied volume,
	// ordered by sequence asc so the supersede arbitration is stable.
	ListAttachments(ctx context.Context, volumeID string) ([]Attachment, error)
}
