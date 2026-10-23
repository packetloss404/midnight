// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// LabelManagedBy marks every Docker volume Midnight materializes so the
// reconciler can distinguish managed volumes from operator-managed ones.
// Mirrors internal/runtime/docker.LabelManagedBy.
const (
	LabelManagedBy   = "midnight.managed_by"
	LabelManagedByV  = "midnight"
	LabelVolumeID    = "midnight.volume_id"
	LabelServiceID   = "midnight.service_id"
	LabelEnvID       = "midnight.environment_id"
	LabelProjectID   = "midnight.project_id"
	LabelVolumeName  = "midnight.volume_name"
	LabelVolumeSize  = "midnight.size_bytes"
	LabelVolumeKind  = "midnight.resource_kind"
	LabelVolumeKindV = "volume"
)

// DockerVolumeNamePrefix is the canonical prefix Midnight applies when
// translating a tracked volume id (`vol_*`) into the Docker volume name.
// The prefix is stable so a redeploy with the same vol_* id mounts the same
// host directory and data persists across container replaces.
const DockerVolumeNamePrefix = "midnight-"

// DockerVolumeName converts a Midnight volume id into the canonical Docker
// volume name. Reused across the driver and the scheduler so naming stays
// in lockstep.
func DockerVolumeName(volumeID string) string {
	return DockerVolumeNamePrefix + strings.TrimSpace(volumeID)
}

// DockerInfo describes a Docker volume the driver observed.
type DockerInfo struct {
	Name       string
	Driver     string
	Labels     map[string]string
	CreatedAt  string
	Mountpoint string
}

// DockerVolumeClient is the narrow surface the driver requires from the
// Docker SDK. The shape mirrors internal/runtime/docker.VolumeEngineClient
// but is intentionally redefined here so the volume package does not pull
// in the Docker adapter package and can be tested without a real Docker
// daemon.
type DockerVolumeClient interface {
	VolumeCreate(ctx context.Context, name string, driver string, labels map[string]string, opts map[string]string) (DockerInfo, error)
	VolumeInspect(ctx context.Context, name string) (DockerInfo, error)
	VolumeRemove(ctx context.Context, name string, force bool) error
}

// IsNotFoundError reports whether a Docker volume lookup miss should be
// treated as "ensure may proceed". Implementations of DockerVolumeClient
// should return errors that satisfy errors.Is(err, ErrVolumeMissing) or
// embed a "not found" substring; the helper handles both.
var ErrVolumeMissing = errors.New("docker volume missing")

// IsNotFound is a small helper the docker driver and tests use to detect
// "no such volume" responses without importing the docker SDK from a
// dependent layer.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrVolumeMissing) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such volume") || strings.Contains(msg, "not found")
}

// DockerDriver wraps a DockerVolumeClient with the Midnight idempotency
// rules and label conventions. The driver does NOT track ownership state
// itself — Postgres is the source of truth — but it does ensure every
// Docker-side write is labeled and named consistently so the reconciler
// can find orphans.
type DockerDriver struct {
	Client DockerVolumeClient
}

// NewDockerDriver returns a driver bound to client. Passing nil yields a
// driver whose Ensure/Remove methods return an error; tests construct one
// of these to validate the failure path.
func NewDockerDriver(client DockerVolumeClient) *DockerDriver {
	return &DockerDriver{Client: client}
}

// EnsureLabels returns the canonical label set every Midnight-managed
// Docker volume MUST carry. Callers may supply additional labels which
// are merged in last-write-wins order (canonical wins).
func EnsureLabels(volumeID, serviceID, environmentID, projectID, name string, extra map[string]string) map[string]string {
	labels := make(map[string]string, 6+len(extra))
	for k, v := range extra {
		labels[k] = v
	}
	labels[LabelManagedBy] = LabelManagedByV
	labels[LabelVolumeID] = volumeID
	labels[LabelServiceID] = serviceID
	labels[LabelEnvID] = environmentID
	labels[LabelProjectID] = projectID
	labels[LabelVolumeName] = name
	labels[LabelVolumeKind] = LabelVolumeKindV
	return labels
}

// EnsureDockerVolume materializes a Docker volume named DockerVolumeName(id)
// with the canonical Midnight label set. The call is idempotent — if a
// volume with the same name already exists, the existing volume is
// returned. The Docker local driver does NOT enforce size_bytes; size is
// recorded in a label so an operator can compare the declared quota with
// observed disk usage.
//
// Note: size is advisory. For real enforcement, operators should mount
// /var/lib/midnight on a quota-enabled filesystem (xfs prjquota or ext4
// quota) and either swap to a quota-aware Docker volume driver or rely on
// the host-side prjquota project id recorded on the volume row.
func (d *DockerDriver) EnsureDockerVolume(ctx context.Context, volumeID, serviceID, environmentID, projectID, volumeName string, sizeBytes int64) (DockerInfo, error) {
	if err := ctx.Err(); err != nil {
		return DockerInfo{}, err
	}
	if d.Client == nil {
		return DockerInfo{}, fmt.Errorf("docker driver: client is not configured")
	}
	if strings.TrimSpace(volumeID) == "" {
		return DockerInfo{}, fmt.Errorf("docker driver: volume id is required")
	}
	name := DockerVolumeName(volumeID)
	labels := EnsureLabels(volumeID, serviceID, environmentID, projectID, volumeName, map[string]string{
		LabelVolumeSize: fmt.Sprintf("%d", sizeBytes),
	})

	if existing, err := d.Client.VolumeInspect(ctx, name); err == nil {
		if err := validateDockerVolumeOwnership(existing, labels); err != nil {
			return DockerInfo{}, err
		}
		return existing, nil
	} else if !IsNotFound(err) {
		return DockerInfo{}, fmt.Errorf("docker volume inspect: %w", err)
	}
	info, err := d.Client.VolumeCreate(ctx, name, "local", labels, nil)
	if err != nil {
		// Race: another EnsureDockerVolume call won the create. The
		// inspect path returns the live volume, which is the correct
		// idempotent result.
		if existing, ierr := d.Client.VolumeInspect(ctx, name); ierr == nil {
			if err := validateDockerVolumeOwnership(existing, labels); err != nil {
				return DockerInfo{}, err
			}
			return existing, nil
		}
		return DockerInfo{}, fmt.Errorf("docker volume create: %w", err)
	}
	return info, nil
}

func validateDockerVolumeOwnership(existing DockerInfo, expected map[string]string) error {
	for _, key := range []string{
		LabelManagedBy, LabelVolumeID, LabelServiceID, LabelEnvID,
		LabelProjectID, LabelVolumeName, LabelVolumeKind,
	} {
		if strings.TrimSpace(existing.Labels[key]) != strings.TrimSpace(expected[key]) {
			return fmt.Errorf("docker driver: existing volume %s has conflicting %s ownership label", existing.Name, key)
		}
	}
	return nil
}

// Remove deletes the Docker volume backing volumeID. It is idempotent on
// "volume not found" — the soft-delete reaper calls Remove during the
// 7-day grace and a previously-reaped volume MUST NOT cause a second pass
// to fail.
func (d *DockerDriver) Remove(ctx context.Context, volumeID string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.Client == nil {
		return fmt.Errorf("docker driver: client is not configured")
	}
	if err := d.Client.VolumeRemove(ctx, DockerVolumeName(volumeID), force); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("docker volume remove: %w", err)
	}
	return nil
}
