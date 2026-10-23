// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// MemoryDeploymentStore gives development routers and unit tests the same
// canonical deployment semantics as PostgreSQL. It is process-local and is
// never presented as persistence evidence.
type MemoryDeploymentStore struct {
	mu      sync.RWMutex
	rows    map[string]DeploymentRecord
	events  map[string][]DeploymentEventRecord
	now     func() time.Time
	mintID  func() string
	trigger map[string]string
}

func NewMemoryDeploymentStore(clock func() time.Time) *MemoryDeploymentStore {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryDeploymentStore{
		rows:    make(map[string]DeploymentRecord),
		events:  make(map[string][]DeploymentEventRecord),
		now:     clock,
		mintID:  func() string { return mintID(orailtypes.IDPrefixDeployment) },
		trigger: make(map[string]string),
	}
}

var _ DeploymentStore = (*MemoryDeploymentStore)(nil)
var _ DeploymentReadStore = (*MemoryDeploymentStore)(nil)

func (s *MemoryDeploymentStore) CreateDeployment(ctx context.Context, rec DeploymentRecord) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	if !deploy.ValidAction(rec.Action) {
		return DeploymentRecord{}, errors.New("deployment action is invalid")
	}
	if strings.TrimSpace(rec.ProjectID) == "" ||
		strings.TrimSpace(rec.ServiceID) == "" ||
		strings.TrimSpace(rec.EnvironmentID) == "" {
		return DeploymentRecord{}, errors.New("deployment project, service, and environment are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.TriggerKey != "" {
		if _, exists := s.trigger[rec.TriggerKey]; exists {
			return DeploymentRecord{}, errors.New("deployment trigger key already exists")
		}
	}
	if rec.ID == "" {
		rec.ID = s.mintID()
	}
	if _, exists := s.rows[rec.ID]; exists {
		return DeploymentRecord{}, errors.New("deployment already exists")
	}
	now := s.now().UTC()
	rec.State = deploy.DeploymentQueued
	rec.Active = false
	rec.CreatedAt = now
	rec.UpdatedAt = now
	s.rows[rec.ID] = rec
	if rec.TriggerKey != "" {
		s.trigger[rec.TriggerKey] = rec.ID
	}
	return rec, nil
}

func (s *MemoryDeploymentStore) GetDeploymentByOperation(ctx context.Context, operationID string) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found DeploymentRecord
	for _, rec := range s.rows {
		if rec.OperationID == operationID &&
			(found.ID == "" || rec.CreatedAt.After(found.CreatedAt)) {
			found = rec
		}
	}
	if found.ID == "" {
		return DeploymentRecord{}, ErrDeploymentNotFound
	}
	return found, nil
}

func (s *MemoryDeploymentStore) ListDeploymentsByService(ctx context.Context, serviceID string) ([]DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeploymentRecord, 0)
	for _, rec := range s.rows {
		if rec.ServiceID == serviceID {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryDeploymentStore) ReadDeploymentPage(
	ctx context.Context,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	if err := validateDeploymentReadFilter(filter); err != nil {
		return DeploymentRecordPage{}, err
	}
	records, err := s.ListDeploymentsByService(ctx, filter.ServiceID)
	if err != nil {
		return DeploymentRecordPage{}, err
	}
	filtered := make([]DeploymentRecord, 0, min(len(records), filter.Limit+1))
	for _, record := range records {
		if filter.EnvironmentID != "" && record.EnvironmentID != filter.EnvironmentID {
			continue
		}
		if filter.DeploymentID != "" && record.ID != filter.DeploymentID {
			continue
		}
		if filter.ActiveOnly && !record.Active {
			continue
		}
		if !filter.BeforeCreatedAt.IsZero() &&
			(record.CreatedAt.After(filter.BeforeCreatedAt) ||
				(record.CreatedAt.Equal(filter.BeforeCreatedAt) && record.ID >= filter.BeforeID)) {
			continue
		}
		filtered = append(filtered, record)
		if len(filtered) == filter.Limit+1 {
			break
		}
	}
	return boundDeploymentRecordPage(filtered, filter.Limit), nil
}

func (s *MemoryDeploymentStore) MarkDeploymentBuilding(ctx context.Context, id string) error {
	return s.transition(ctx, id, deploy.DeploymentQueued, deploy.DeploymentBuilding, func(rec *DeploymentRecord, now time.Time) {
		if rec.StartedAt == nil {
			rec.StartedAt = timePointer(now)
		}
	})
}

func (s *MemoryDeploymentStore) RecordDeploymentBuild(ctx context.Context, id, sourceSHA, builder, imageRef, imageDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if imageRef == "" || imageDigest == "" {
		return errors.New("deployment build result requires image_ref and image_digest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[id]
	if !ok {
		return ErrDeploymentNotFound
	}
	if rec.State != deploy.DeploymentBuilding || rec.ImageRef != "" || rec.ImageDigest != "" {
		return ErrDeploymentTransitionConflict
	}
	rec.SHA = sourceSHA
	rec.Builder = builder
	rec.ImageRef = imageRef
	rec.ImageDigest = imageDigest
	rec.UpdatedAt = s.now().UTC()
	s.rows[id] = rec
	return nil
}

func (s *MemoryDeploymentStore) MarkDeploymentDeploying(ctx context.Context, id string, from deploy.DeploymentState) error {
	return s.transition(ctx, id, from, deploy.DeploymentDeploying, func(rec *DeploymentRecord, now time.Time) {
		if rec.StartedAt == nil {
			rec.StartedAt = timePointer(now)
		}
	})
}

func (s *MemoryDeploymentStore) FailDeployment(ctx context.Context, id string, from deploy.DeploymentState, code, message string) error {
	if code == "" {
		return errors.New("deployment failure requires a failure_code")
	}
	return s.transition(ctx, id, from, deploy.DeploymentFailed, func(rec *DeploymentRecord, now time.Time) {
		rec.FailureCode = code
		rec.FailureMessage = message
		rec.CompletedAt = timePointer(now)
	})
}

func (s *MemoryDeploymentStore) CancelDeployment(ctx context.Context, id string, from deploy.DeploymentState) error {
	return s.transition(ctx, id, from, deploy.DeploymentCanceled, func(rec *DeploymentRecord, now time.Time) {
		rec.CompletedAt = timePointer(now)
	})
}

func (s *MemoryDeploymentStore) PromoteDeployment(ctx context.Context, id, expectedActiveID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, ok := s.rows[id]
	if !ok {
		return ErrDeploymentNotFound
	}
	if !deploy.CanPromote(candidate.State) {
		return ErrDeploymentTransitionConflict
	}
	currentID := ""
	for otherID, rec := range s.rows {
		if otherID != id &&
			rec.ServiceID == candidate.ServiceID &&
			rec.EnvironmentID == candidate.EnvironmentID &&
			rec.Active {
			currentID = otherID
			break
		}
	}
	if expectedActiveID != "" && currentID != expectedActiveID {
		return ErrPromotionConflict
	}
	now := s.now().UTC()
	if currentID != "" {
		current := s.rows[currentID]
		if current.State != deploy.DeploymentActive {
			return ErrPromotionConflict
		}
		current.State = deploy.DeploymentSuperseded
		current.Active = false
		current.RuntimeDesiredState = "stopped"
		current.RuntimeObservedState = "stopped"
		current.RuntimeGeneration++
		current.UpdatedAt = now
		s.rows[currentID] = current
	}
	candidate.State = deploy.DeploymentActive
	candidate.Active = true
	candidate.RuntimeDesiredState = "running"
	candidate.RuntimeObservedState = "running"
	if candidate.RuntimeGeneration == 0 {
		candidate.RuntimeGeneration = 1
	}
	candidate.UpdatedAt = now
	candidate.CompletedAt = timePointer(now)
	s.rows[id] = candidate
	return nil
}

func (s *MemoryDeploymentStore) CreateRollbackDeployment(ctx context.Context, sourceID string) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	s.mu.RLock()
	source, ok := s.rows[sourceID]
	currentID := ""
	if ok {
		for id, rec := range s.rows {
			if rec.ServiceID == source.ServiceID &&
				rec.EnvironmentID == source.EnvironmentID &&
				rec.Active {
				currentID = id
				break
			}
		}
	}
	s.mu.RUnlock()
	if !ok {
		return DeploymentRecord{}, ErrDeploymentNotFound
	}
	if source.ImageDigest == "" {
		return DeploymentRecord{}, errors.New("rollback source has no recorded image digest")
	}
	return s.CreateDeployment(ctx, DeploymentRecord{
		ProjectID:             source.ProjectID,
		ServiceID:             source.ServiceID,
		EnvironmentID:         source.EnvironmentID,
		Action:                deploy.ActionRollbackDigest,
		ImageRef:              source.ImageRef,
		ImageDigest:           source.ImageDigest,
		ConfigurationRevision: source.ConfigurationRevision,
		RollbackSourceID:      sourceID,
		ExpectedActiveID:      currentID,
	})
}

func (s *MemoryDeploymentStore) AppendDeploymentEvent(ctx context.Context, evt DeploymentEventRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if evt.EventType == "" {
		return errors.New("deployment event type is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[evt.DeploymentID]; !ok {
		return ErrDeploymentNotFound
	}
	s.events[evt.DeploymentID] = append(s.events[evt.DeploymentID], evt)
	return nil
}

func (s *MemoryDeploymentStore) transition(
	ctx context.Context,
	id string,
	from, to deploy.DeploymentState,
	mutate func(*DeploymentRecord, time.Time),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := deploy.ValidateTransition(from, to); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[id]
	if !ok {
		return ErrDeploymentNotFound
	}
	if rec.State != from {
		return ErrDeploymentTransitionConflict
	}
	now := s.now().UTC()
	rec.State = to
	rec.Active = to == deploy.DeploymentActive
	rec.UpdatedAt = now
	if mutate != nil {
		mutate(&rec, now)
	}
	s.rows[id] = rec
	return nil
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
