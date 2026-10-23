// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the deterministic in-memory Store used by unit tests and
// developer-mode boots. The persistence shape mirrors the Postgres tables
// but never blocks on a real connection.
type MemoryStore struct {
	mu          sync.Mutex
	volumes     map[string]Volume
	order       []string
	attachments map[string]Attachment
	attachOrder []string
	now         func() time.Time
	idFactory   func() string
}

// NewMemoryStore returns an empty MemoryStore wall-clocked to time.Now.
func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithClock(func() time.Time { return time.Now().UTC() })
}

// NewMemoryStoreWithClock supports deterministic tests. The clock is the
// source of created_at, updated_at, attached_at, soft_deleted_at, etc.
func NewMemoryStoreWithClock(now func() time.Time) *MemoryStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryStore{
		volumes:     make(map[string]Volume),
		attachments: make(map[string]Attachment),
		now:         now,
		idFactory:   defaultMemoryVolumeID,
	}
}

func defaultMemoryVolumeID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("vol_%d", time.Now().UnixNano())
	}
	return "vol_" + hex.EncodeToString(b[:])
}

func defaultMemoryAttachmentID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("vat_%d", time.Now().UnixNano())
	}
	return "vat_" + hex.EncodeToString(b[:])
}

// Create implements Store.
func (s *MemoryStore) Create(ctx context.Context, in VolumeCreateInput) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	if strings.TrimSpace(in.ServiceID) == "" {
		return Volume{}, fmt.Errorf("%w: service_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.EnvironmentID) == "" {
		return Volume{}, fmt.Errorf("%w: environment_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return Volume{}, fmt.Errorf("%w: project_id is required", ErrVolumeInvalid)
	}
	if strings.TrimSpace(in.Name) == "" {
		return Volume{}, fmt.Errorf("%w: name is required", ErrVolumeInvalid)
	}
	if in.SizeBytes < 0 {
		return Volume{}, fmt.Errorf("%w: size_bytes must be non-negative", ErrVolumeInvalid)
	}
	in.MountPath = strings.TrimSpace(in.MountPath)
	if in.MountPath != "" && !validMountPath(in.MountPath) {
		return Volume{}, fmt.Errorf("%w: mount_path must be an absolute clean non-root path", ErrVolumeInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.volumes {
		if v.Status == StatusDeleted {
			continue
		}
		if v.ServiceID == in.ServiceID && v.EnvironmentID == in.EnvironmentID && v.Name == in.Name {
			return Volume{}, fmt.Errorf("%w: volume %q already exists on (%s,%s)", ErrVolumeConflict, in.Name, in.ServiceID, in.EnvironmentID)
		}
	}
	now := s.now()
	v := Volume{
		ID:              s.idFactory(),
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
	s.volumes[v.ID] = v
	s.order = append(s.order, v.ID)
	return v, nil
}

// Get implements Store.
func (s *MemoryStore) Get(ctx context.Context, id string) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[id]
	if !ok {
		return Volume{}, ErrVolumeNotFound
	}
	return v, nil
}

// List implements Store.
func (s *MemoryStore) List(ctx context.Context, filter ListFilter) ([]Volume, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Volume, 0, len(s.order))
	for _, id := range s.order {
		v := s.volumes[id]
		if !filter.IncludeSoft && (v.Status == StatusSoftDeleted || v.Status == StatusDeleted) {
			continue
		}
		if filter.ProjectID != "" && v.ProjectID != filter.ProjectID {
			continue
		}
		if filter.ServiceID != "" && v.ServiceID != filter.ServiceID {
			continue
		}
		if filter.EnvironmentID != "" && v.EnvironmentID != filter.EnvironmentID {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ListByService implements Store.
func (s *MemoryStore) ListByService(ctx context.Context, serviceID string) ([]Volume, error) {
	return s.List(ctx, ListFilter{ServiceID: serviceID})
}

// ListByProject implements Store.
func (s *MemoryStore) ListByProject(ctx context.Context, projectID string) ([]Volume, error) {
	return s.List(ctx, ListFilter{ProjectID: projectID})
}

// Resize implements Store. Resize UP is allowed; resize DOWN is rejected.
func (s *MemoryStore) Resize(ctx context.Context, id string, newSize int64) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	if newSize < 0 {
		return Volume{}, fmt.Errorf("%w: size_bytes must be non-negative", ErrVolumeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[id]
	if !ok {
		return Volume{}, ErrVolumeNotFound
	}
	if newSize < v.SizeBytes {
		return Volume{}, ErrVolumeResizeDownUnsupported
	}
	v.SizeBytes = newSize
	v.UpdatedAt = s.now()
	v.ResourceVersion++
	s.volumes[id] = v
	return v, nil
}

// MarkReady implements Store.
func (s *MemoryStore) MarkReady(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[id]
	if !ok {
		return ErrVolumeNotFound
	}
	if v.Status == StatusSoftDeleted || v.Status == StatusDeleted {
		return nil
	}
	if v.Status != StatusReady && v.Status != StatusAttached {
		v.Status = StatusReady
	}
	v.UpdatedAt = s.now()
	v.ResourceVersion++
	s.volumes[id] = v
	return nil
}

// Delete implements Store. Soft-delete is non-destructive; HasActiveAttachment
// gates the call.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[id]
	if !ok {
		return ErrVolumeNotFound
	}
	for _, a := range s.attachments {
		if a.VolumeID == id && (a.Status == AttachmentPending || a.Status == AttachmentActive) {
			return ErrVolumeAttached
		}
	}
	now := s.now()
	v.MarkSoftDeleted(now)
	v.ResourceVersion++
	s.volumes[id] = v
	return nil
}

// HasActiveAttachment implements Store.
func (s *MemoryStore) HasActiveAttachment(ctx context.Context, volumeID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attachments {
		if a.VolumeID == volumeID && (a.Status == AttachmentPending || a.Status == AttachmentActive) {
			return true, nil
		}
	}
	return false, nil
}

// RecordAttachment implements Store.
func (s *MemoryStore) RecordAttachment(ctx context.Context, attach Attachment) (Attachment, error) {
	if err := ctx.Err(); err != nil {
		return Attachment{}, err
	}
	if strings.TrimSpace(attach.VolumeID) == "" || strings.TrimSpace(attach.DeploymentID) == "" {
		return Attachment{}, fmt.Errorf("%w: deployment_id and volume_id are required", ErrVolumeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[attach.VolumeID]
	if !ok {
		return Attachment{}, ErrVolumeNotFound
	}
	if attach.ServiceID != v.ServiceID || attach.EnvironmentID != v.EnvironmentID {
		return Attachment{}, fmt.Errorf("%w: attachment scope does not match volume", ErrVolumeInvalid)
	}
	if !validMountPath(attach.MountPath) || (v.MountPath != "" && attach.MountPath != v.MountPath) {
		return Attachment{}, fmt.Errorf("%w: attachment mount_path does not match volume", ErrVolumeInvalid)
	}
	if attach.ID == "" {
		attach.ID = defaultMemoryAttachmentID()
	}
	if attach.AttachedAt.IsZero() {
		attach.AttachedAt = s.now()
	}
	if attach.Status == "" {
		attach.Status = AttachmentPending
	}
	// Dedupe on (deployment_id, instance_id, volume_id) per migrations/0008.
	for id, existing := range s.attachments {
		if existing.DeploymentID == attach.DeploymentID && existing.InstanceID == attach.InstanceID && existing.VolumeID == attach.VolumeID {
			attach.ID = id
			s.attachments[id] = attach
			return attach, nil
		}
		if existing.VolumeID == attach.VolumeID && (existing.Status == AttachmentPending || existing.Status == AttachmentActive) {
			return Attachment{}, ErrVolumeAttached
		}
	}
	s.attachments[attach.ID] = attach
	s.attachOrder = append(s.attachOrder, attach.ID)
	// Pending is a durable lease, not evidence that Docker attached the
	// volume. Only an explicitly active row promotes the volume status.
	if attach.Status == AttachmentActive && (v.Status == StatusReady || v.Status == StatusProvisioning) {
		v.Status = StatusAttached
		v.UpdatedAt = s.now()
		s.volumes[attach.VolumeID] = v
	}
	return attach, nil
}

// ActivateAttachment implements Store.
func (s *MemoryStore) ActivateAttachment(ctx context.Context, attachmentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[attachmentID]
	if !ok {
		return ErrVolumeNotFound
	}
	if a.Status != AttachmentPending && a.Status != AttachmentActive {
		return fmt.Errorf("%w: detached attachment cannot be activated", ErrVolumeInvalid)
	}
	for id, other := range s.attachments {
		if id != attachmentID && other.VolumeID == a.VolumeID &&
			(other.Status == AttachmentPending || other.Status == AttachmentActive) {
			return ErrVolumeAttached
		}
	}
	a.Status = AttachmentActive
	a.DetachedAt = time.Time{}
	s.attachments[attachmentID] = a
	v := s.volumes[a.VolumeID]
	if v.Status != StatusSoftDeleted && v.Status != StatusDeleted {
		v.Status = StatusAttached
		v.UpdatedAt = s.now()
		s.volumes[a.VolumeID] = v
	}
	return nil
}

// MarkAttachmentDetached implements Store.
func (s *MemoryStore) MarkAttachmentDetached(ctx context.Context, attachmentID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[attachmentID]
	if !ok {
		return nil
	}
	a.Status = AttachmentDetached
	a.DetachedAt = at
	s.attachments[attachmentID] = a
	v := s.volumes[a.VolumeID]
	hasWriter := false
	for id, other := range s.attachments {
		if id != attachmentID && other.VolumeID == a.VolumeID &&
			(other.Status == AttachmentPending || other.Status == AttachmentActive) {
			hasWriter = true
			break
		}
	}
	if !hasWriter && v.Status == StatusAttached {
		v.Status = StatusReady
		v.UpdatedAt = s.now()
		s.volumes[a.VolumeID] = v
	}
	return nil
}

// ListAttachments implements Store.
func (s *MemoryStore) ListAttachments(ctx context.Context, volumeID string) ([]Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Attachment, 0, len(s.attachOrder))
	for _, id := range s.attachOrder {
		a := s.attachments[id]
		if a.VolumeID != volumeID {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}
