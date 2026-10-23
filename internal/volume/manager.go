// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// ReplicasUnsupportedReason aliases the registered failurecodes constant
// so callers in this package never type the literal twice. The wire format
// is enforced by the test in internal/failurecodes.
const (
	ReplicasUnsupportedReason   = failurecodes.VolumeReplicasUnsupported
	ResizeDownUnsupportedReason = failurecodes.VolumeResizeDownUnsupported
	AttachedReason              = failurecodes.VolumeAttached
)

// ReplicasUnsupportedError is the typed error the scheduler returns when
// the single-writer guard fires.
type ReplicasUnsupportedError struct {
	ServiceID string
	Replicas  int
}

// Error implements error.
func (e *ReplicasUnsupportedError) Error() string {
	return fmt.Sprintf("volume attachment incompatible with replicas=%d on service %s", e.Replicas, e.ServiceID)
}

// Code reports the typed failure code.
func (e *ReplicasUnsupportedError) Code() failurecodes.Code { return ReplicasUnsupportedReason }
func (e *ReplicasUnsupportedError) Reason() (failurecodes.Code, string) {
	return e.Code(), e.Error()
}

// LifecycleError carries a registered failure code across the deploy/runtime
// boundary without exposing storage or Docker implementation details.
type LifecycleError struct {
	FailureCode failurecodes.Code
	Message     string
}

func (e *LifecycleError) Error() string { return e.Message }
func (e *LifecycleError) Reason() (failurecodes.Code, string) {
	return e.FailureCode, e.Message
}

// Manager owns the deploy-time orchestration of volumes: materialize the
// Docker volume on the host, record the attachment row, and return the
// (docker_volume_name, mount_path) pairs the scheduler folds into the
// container's HostConfig.Mounts.
//
// Manager is intentionally thin — every persistence concern is delegated
// to Store and every host-side write to DockerDriver. Manager exists so
// the scheduler has a single seam to call.
type Manager struct {
	Store  Store
	Driver *DockerDriver
	Now    func() time.Time
}

// NewManager returns a Manager bound to store and driver. Either may be
// nil for tests; in production the API constructor wires both.
func NewManager(store Store, driver *DockerDriver) *Manager {
	return &Manager{
		Store:  store,
		Driver: driver,
		Now:    func() time.Time { return time.Now().UTC() },
	}
}

// DeploymentAttachments captures the volume attachments associated with a
// single deployment instance. The scheduler builds one of these from the
// deploy job spec and passes it to EnsureForDeployment.
type DeploymentAttachments struct {
	DeploymentID  string
	InstanceID    string
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	Replicas      int
	Sequence      int64
	Attachments   []AttachmentInput
}

// PreparedAttachments is a durable single-writer reservation plus the
// runtime mounts derived from it. Attachment rows remain pending until the
// scheduler proves a container was created.
type PreparedAttachments struct {
	Deployment DeploymentAttachments
	Claims     []Attachment
	Mounts     []AttachedVolume
}

// ObservedAttachment is runtime evidence for one running container mount.
type ObservedAttachment struct {
	VolumeID      string
	DeploymentID  string
	InstanceID    string
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	Source        string
	MountPath     string
	ReadOnly      bool
}

// RequiresSerialTakeover validates durable service volume intent without
// writing it and reports whether a previous instance must be stopped before a
// candidate can acquire the local driver's single-writer lease.
func (m *Manager) RequiresSerialTakeover(ctx context.Context, dep DeploymentAttachments) (bool, error) {
	if m == nil || m.Store == nil {
		return false, errors.New("volume manager: store is required")
	}
	volumes, err := m.Store.List(ctx, ListFilter{
		ProjectID: dep.ProjectID, ServiceID: dep.ServiceID, EnvironmentID: dep.EnvironmentID,
	})
	if err != nil {
		return false, fmt.Errorf("volume manager: list service volumes: %w", err)
	}
	if len(volumes) == 0 {
		return false, nil
	}
	if dep.Replicas > 1 {
		return false, &ReplicasUnsupportedError{ServiceID: dep.ServiceID, Replicas: dep.Replicas}
	}
	if m.Driver == nil {
		return false, &LifecycleError{
			FailureCode: failurecodes.AgentRuntimeUnavailable,
			Message:     "volume manager: Docker volume driver is required",
		}
	}
	for _, vol := range volumes {
		if vol.ProjectID != dep.ProjectID || vol.ServiceID != dep.ServiceID || vol.EnvironmentID != dep.EnvironmentID ||
			!validMountPath(vol.MountPath) ||
			vol.Status == StatusRestoreStaged || vol.Status == StatusRestoreRetained ||
			vol.Status == StatusSoftDeleted || vol.Status == StatusDeleted {
			return false, &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: durable intent is invalid for %s", vol.ID),
			}
		}
	}
	return true, nil
}

// DetachInstance releases durable writer leases only after the executor has
// successfully drained/stopped the corresponding runtime instance.
func (m *Manager) DetachInstance(ctx context.Context, dep DeploymentAttachments, deploymentID, instanceID string) error {
	if m == nil || m.Store == nil {
		return errors.New("volume manager: store is required")
	}
	volumes, err := m.Store.List(ctx, ListFilter{
		ProjectID: dep.ProjectID, ServiceID: dep.ServiceID, EnvironmentID: dep.EnvironmentID,
	})
	if err != nil {
		return fmt.Errorf("volume manager: list service volumes for detach: %w", err)
	}
	for _, vol := range volumes {
		attachments, err := m.Store.ListAttachments(ctx, vol.ID)
		if err != nil {
			return fmt.Errorf("volume manager: list attachments for detach: %w", err)
		}
		for _, attachment := range attachments {
			if attachment.DeploymentID != deploymentID || attachment.InstanceID != instanceID ||
				(attachment.Status != AttachmentPending && attachment.Status != AttachmentActive) {
				continue
			}
			if attachment.ProjectID != "" && attachment.ProjectID != dep.ProjectID {
				return &LifecycleError{
					FailureCode: failurecodes.ResourceValidationFailed,
					Message:     fmt.Sprintf("volume manager: attachment %s is outside deployment scope", attachment.ID),
				}
			}
			if err := m.Store.MarkAttachmentDetached(ctx, attachment.ID, m.now()); err != nil {
				return fmt.Errorf("volume manager: detach attachment %s: %w", attachment.ID, err)
			}
		}
	}
	return nil
}

// ReconcileObserved repairs attachment truth from a complete, successful
// Docker observation. It fails before writes if Docker reports two live
// writers or any cross-scope/mount mismatch.
func (m *Manager) ReconcileObserved(ctx context.Context, observed []ObservedAttachment) error {
	if m == nil || m.Store == nil {
		return errors.New("volume manager: store is required")
	}
	byVolume := make(map[string]ObservedAttachment, len(observed))
	for _, item := range observed {
		if prior, exists := byVolume[item.VolumeID]; exists &&
			(prior.DeploymentID != item.DeploymentID || prior.InstanceID != item.InstanceID) {
			return &LifecycleError{
				FailureCode: failurecodes.VolumeAttached,
				Message:     fmt.Sprintf("volume manager: Docker reports multiple live writers for %s", item.VolumeID),
			}
		}
		vol, err := m.Store.Get(ctx, item.VolumeID)
		if err != nil {
			return fmt.Errorf("volume manager: observed volume lookup %s: %w", item.VolumeID, err)
		}
		if vol.ProjectID != item.ProjectID || vol.ServiceID != item.ServiceID || vol.EnvironmentID != item.EnvironmentID {
			return &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: observed volume %s has mismatched runtime scope", item.VolumeID),
			}
		}
		if item.Source != DockerVolumeName(item.VolumeID) || item.MountPath != vol.MountPath || !validMountPath(item.MountPath) {
			return &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: observed mount for %s differs from durable intent", item.VolumeID),
			}
		}
		byVolume[item.VolumeID] = item
	}

	volumes, err := m.Store.List(ctx, ListFilter{IncludeSoft: true})
	if err != nil {
		return fmt.Errorf("volume manager: list volumes for reconcile: %w", err)
	}
	// Only after validating the complete observation do we release stale
	// leases. A successful Docker list+inspect pass is the evidence boundary.
	for _, vol := range volumes {
		attachments, err := m.Store.ListAttachments(ctx, vol.ID)
		if err != nil {
			return fmt.Errorf("volume manager: list attachments for %s: %w", vol.ID, err)
		}
		live, hasLive := byVolume[vol.ID]
		for _, attachment := range attachments {
			if attachment.Status != AttachmentPending && attachment.Status != AttachmentActive {
				continue
			}
			if hasLive && attachment.DeploymentID == live.DeploymentID && attachment.InstanceID == live.InstanceID {
				continue
			}
			if err := m.Store.MarkAttachmentDetached(ctx, attachment.ID, m.now()); err != nil {
				return fmt.Errorf("volume manager: detach stale attachment %s: %w", attachment.ID, err)
			}
		}
	}
	for _, item := range observed {
		claim, err := m.Store.RecordAttachment(ctx, Attachment{
			VolumeID:      item.VolumeID,
			DeploymentID:  item.DeploymentID,
			InstanceID:    item.InstanceID,
			ProjectID:     item.ProjectID,
			ServiceID:     item.ServiceID,
			EnvironmentID: item.EnvironmentID,
			MountPath:     item.MountPath,
			Status:        AttachmentPending,
			AttachedAt:    m.now(),
		})
		if err != nil {
			return fmt.Errorf("volume manager: recover attachment %s: %w", item.VolumeID, err)
		}
		if err := m.Store.ActivateAttachment(ctx, claim.ID); err != nil {
			return fmt.Errorf("volume manager: activate recovered attachment %s: %w", claim.ID, err)
		}
	}
	return nil
}

// PrepareServiceDeployment loads volume ids and mount paths from PostgreSQL
// truth. Queue payloads intentionally do not duplicate either value.
func (m *Manager) PrepareServiceDeployment(ctx context.Context, dep DeploymentAttachments) (PreparedAttachments, error) {
	if m == nil || m.Store == nil {
		return PreparedAttachments{}, errors.New("volume manager: store is required")
	}
	volumes, err := m.Store.List(ctx, ListFilter{
		ProjectID:     dep.ProjectID,
		ServiceID:     dep.ServiceID,
		EnvironmentID: dep.EnvironmentID,
	})
	if err != nil {
		return PreparedAttachments{}, fmt.Errorf("volume manager: list service volumes: %w", err)
	}
	dep.Attachments = make([]AttachmentInput, 0, len(volumes))
	for _, vol := range volumes {
		dep.Attachments = append(dep.Attachments, AttachmentInput{
			VolumeID:     vol.ID,
			DeploymentID: dep.DeploymentID,
			InstanceID:   dep.InstanceID,
			MountPath:    vol.MountPath,
			Sequence:     dep.Sequence,
		})
	}
	return m.PrepareForDeployment(ctx, dep)
}

// PrepareForDeployment validates the complete durable attachment set, acquires
// every single-writer reservation, and materializes the named Docker volumes.
// It never marks an attachment active.
func (m *Manager) PrepareForDeployment(ctx context.Context, dep DeploymentAttachments) (PreparedAttachments, error) {
	if m == nil {
		return PreparedAttachments{}, errors.New("volume manager: not configured")
	}
	prepared := PreparedAttachments{Deployment: dep}
	if len(dep.Attachments) == 0 {
		return prepared, nil
	}
	if dep.Replicas <= 0 {
		dep.Replicas = 1
		prepared.Deployment.Replicas = 1
	}
	if dep.Replicas > 1 {
		return PreparedAttachments{}, &ReplicasUnsupportedError{ServiceID: dep.ServiceID, Replicas: dep.Replicas}
	}
	if m.Store == nil {
		return PreparedAttachments{}, errors.New("volume manager: store is required")
	}
	if m.Driver == nil {
		return PreparedAttachments{}, &LifecycleError{
			FailureCode: failurecodes.AgentRuntimeUnavailable,
			Message:     "volume manager: Docker volume driver is required",
		}
	}
	if strings.TrimSpace(dep.DeploymentID) == "" || strings.TrimSpace(dep.InstanceID) == "" ||
		strings.TrimSpace(dep.ProjectID) == "" || strings.TrimSpace(dep.ServiceID) == "" ||
		strings.TrimSpace(dep.EnvironmentID) == "" {
		return PreparedAttachments{}, &LifecycleError{
			FailureCode: failurecodes.ResourceValidationFailed,
			Message:     "volume manager: deployment, instance, project, service, and environment are required",
		}
	}

	// Validate the entire set before the first database/Docker side effect.
	volumes := make([]Volume, 0, len(dep.Attachments))
	for _, in := range dep.Attachments {
		vol, err := m.Store.Get(ctx, in.VolumeID)
		if err != nil {
			return PreparedAttachments{}, fmt.Errorf("volume manager: lookup %s: %w", in.VolumeID, err)
		}
		if vol.ProjectID != dep.ProjectID || vol.ServiceID != dep.ServiceID || vol.EnvironmentID != dep.EnvironmentID {
			return PreparedAttachments{}, &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: volume %s is outside deployment scope", vol.ID),
			}
		}
		if vol.Status == StatusRestoreStaged || vol.Status == StatusRestoreRetained ||
			vol.Status == StatusSoftDeleted || vol.Status == StatusDeleted {
			return PreparedAttachments{}, &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: volume %s is %s", in.VolumeID, vol.Status),
			}
		}
		mountPath := strings.TrimSpace(in.MountPath)
		if mountPath == "" {
			mountPath = strings.TrimSpace(vol.MountPath)
		}
		if !validMountPath(mountPath) {
			return PreparedAttachments{}, &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: durable mount_path is invalid for %s", in.VolumeID),
			}
		}
		if vol.MountPath != "" && mountPath != vol.MountPath {
			return PreparedAttachments{}, &LifecycleError{
				FailureCode: failurecodes.ResourceValidationFailed,
				Message:     fmt.Sprintf("volume manager: mount_path for %s differs from durable intent", in.VolumeID),
			}
		}
		dep.Attachments[len(volumes)].MountPath = mountPath
		volumes = append(volumes, vol)
	}
	prepared.Deployment = dep

	// Reserve first. If any reservation fails, release only reservations from
	// this attempt. No Docker call has occurred yet.
	for i, in := range dep.Attachments {
		claim, err := m.Store.RecordAttachment(ctx, Attachment{
			VolumeID:      volumes[i].ID,
			DeploymentID:  dep.DeploymentID,
			InstanceID:    dep.InstanceID,
			ProjectID:     dep.ProjectID,
			ServiceID:     dep.ServiceID,
			EnvironmentID: dep.EnvironmentID,
			MountPath:     in.MountPath,
			Status:        AttachmentPending,
			Sequence:      maxInt64(in.Sequence, dep.Sequence),
			AttachedAt:    m.now(),
		})
		if err != nil {
			m.detachPrepared(ctx, prepared)
			if errors.Is(err, ErrVolumeAttached) {
				return PreparedAttachments{}, &LifecycleError{
					FailureCode: failurecodes.VolumeAttached,
					Message:     fmt.Sprintf("volume manager: volume %s already has a writer", volumes[i].ID),
				}
			}
			return PreparedAttachments{}, fmt.Errorf("volume manager: reserve attachment %s: %w", volumes[i].ID, err)
		}
		prepared.Claims = append(prepared.Claims, claim)
	}

	for i, vol := range volumes {
		if _, err := m.Driver.EnsureDockerVolume(ctx, vol.ID, vol.ServiceID, vol.EnvironmentID, vol.ProjectID, vol.Name, vol.SizeBytes); err != nil {
			m.detachPrepared(ctx, prepared)
			return PreparedAttachments{}, &LifecycleError{
				FailureCode: failurecodes.AgentRuntimeUnavailable,
				Message:     fmt.Sprintf("volume manager: ensure Docker volume %s failed: %v", vol.ID, err),
			}
		}
		if err := m.Store.MarkReady(ctx, vol.ID); err != nil {
			m.detachPrepared(ctx, prepared)
			return PreparedAttachments{}, fmt.Errorf("volume manager: mark ready %s: %w", vol.ID, err)
		}
		prepared.Mounts = append(prepared.Mounts, AttachedVolume{
			VolumeID:         vol.ID,
			DockerVolumeName: DockerVolumeName(vol.ID),
			MountPath:        dep.Attachments[i].MountPath,
			ReadOnly:         dep.Attachments[i].ReadOnly,
			ServiceID:        vol.ServiceID,
			EnvironmentID:    vol.EnvironmentID,
		})
	}
	return prepared, nil
}

// ConfirmForSchedule re-acquires the same attachment rows immediately before
// container create. This closes the gap between initial reservation and attach.
func (m *Manager) ConfirmForSchedule(ctx context.Context, prepared PreparedAttachments) error {
	for _, claim := range prepared.Claims {
		claim.Status = AttachmentPending
		current, err := m.Store.RecordAttachment(ctx, claim)
		if err != nil {
			if errors.Is(err, ErrVolumeAttached) {
				return &LifecycleError{FailureCode: failurecodes.VolumeAttached, Message: "volume manager: single-writer reservation was lost before schedule"}
			}
			return fmt.Errorf("volume manager: confirm attachment %s: %w", claim.VolumeID, err)
		}
		if current.ID != claim.ID {
			return &LifecycleError{FailureCode: failurecodes.VolumeAttached, Message: "volume manager: attachment owner changed before schedule"}
		}
	}
	return nil
}

// ActivatePrepared records only scheduler-proven mounts as active.
func (m *Manager) ActivatePrepared(ctx context.Context, prepared PreparedAttachments) error {
	for _, claim := range prepared.Claims {
		if err := m.Store.ActivateAttachment(ctx, claim.ID); err != nil {
			return fmt.Errorf("volume manager: activate attachment %s: %w", claim.ID, err)
		}
	}
	return nil
}

// DetachPrepared releases reservations known not to have reached container
// create. Callers must not use it after an ambiguous/confirmed runtime attach.
func (m *Manager) DetachPrepared(ctx context.Context, prepared PreparedAttachments) {
	m.detachPrepared(ctx, prepared)
}

func (m *Manager) detachPrepared(ctx context.Context, prepared PreparedAttachments) {
	for _, claim := range prepared.Claims {
		_ = m.Store.MarkAttachmentDetached(ctx, claim.ID, m.now())
	}
}

// EnsureForDeployment materializes a Docker volume for every requested
// attachment, records the volume_attachments rows, and returns the
// AttachedVolume slice the scheduler folds into the container create.
//
// Pre-conditions:
//   - Every AttachmentInput.VolumeID MUST refer to a Volume row already
//     created via Store.Create.
//   - The supplied Replicas value gates the single-writer constraint —
//     Replicas > 1 with any attachment returns a ReplicasUnsupportedError.
func (m *Manager) EnsureForDeployment(ctx context.Context, dep DeploymentAttachments) ([]AttachedVolume, error) {
	prepared, err := m.PrepareForDeployment(ctx, dep)
	if err != nil {
		return nil, err
	}
	if err := m.ConfirmForSchedule(ctx, prepared); err != nil {
		m.DetachPrepared(ctx, prepared)
		return nil, err
	}
	if err := m.ActivatePrepared(ctx, prepared); err != nil {
		return nil, err
	}
	return prepared.Mounts, nil
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
