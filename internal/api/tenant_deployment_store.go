// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"sort"

	"github.com/midnight/midnight/internal/identity"
)

// tenantDeploymentStore overrides only the collection read. All mutation
// methods remain the embedded canonical store and are still protected by the
// route's separate deny-by-default mutation admission.
type tenantDeploymentStore struct {
	DeploymentStore
	access identity.AccessStore
}

var _ DeploymentReadStore = tenantDeploymentStore{}
var _ EnvironmentDeploymentBatchStore = tenantDeploymentStore{}

func (s tenantDeploymentStore) ListDeploymentsByEnvironment(
	ctx context.Context,
	projectID, environmentID string,
) ([]DeploymentRecord, error) {
	batch, ok := s.DeploymentStore.(EnvironmentDeploymentBatchStore)
	if !ok {
		return nil, nil
	}
	userID, human := humanProductUserID(ctx)
	if !human {
		return batch.ListDeploymentsByEnvironment(ctx, projectID, environmentID)
	}
	_, _, err := identity.Authorize(
		ctx,
		s.access,
		userID,
		identity.AccessTarget{
			ProjectID:     projectID,
			EnvironmentID: environmentID,
		},
		identity.CapabilityServiceRead,
		false,
	)
	if err != nil {
		if tenantReadDenied(err) {
			return []DeploymentRecord{}, nil
		}
		return nil, err
	}
	return batch.ListDeploymentsByEnvironment(ctx, projectID, environmentID)
}

func (s tenantDeploymentStore) ListDeploymentsByService(
	ctx context.Context,
	serviceID string,
) ([]DeploymentRecord, error) {
	userID, human := humanProductUserID(ctx)
	if !human {
		return s.DeploymentStore.ListDeploymentsByService(ctx, serviceID)
	}
	if durable, ok := tenantIndexedDeploymentStore(s.DeploymentStore, s.access); ok {
		return durable.tenantListDeploymentsByService(ctx, userID, serviceID)
	}
	records, err := s.DeploymentStore.ListDeploymentsByService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make([]DeploymentRecord, 0, len(records))
	for _, record := range records {
		_, _, authorizeErr := identity.Authorize(
			ctx,
			s.access,
			userID,
			identity.AccessTarget{
				ProjectID:     record.ProjectID,
				EnvironmentID: record.EnvironmentID,
			},
			identity.CapabilityServiceRead,
			false,
		)
		if authorizeErr == nil {
			out = append(out, record)
			continue
		}
		if !tenantReadDenied(authorizeErr) {
			return nil, authorizeErr
		}
	}
	return out, nil
}

func (s tenantDeploymentStore) ReadDeploymentPage(
	ctx context.Context,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	userID, human := humanProductUserID(ctx)
	if !human {
		if reader, ok := s.DeploymentStore.(DeploymentReadStore); ok {
			return reader.ReadDeploymentPage(ctx, filter)
		}
		return readDeploymentPageFromList(ctx, s.DeploymentStore, filter)
	}
	if durable, ok := tenantIndexedDeploymentStore(s.DeploymentStore, s.access); ok {
		return durable.tenantReadDeploymentPage(ctx, userID, filter)
	}
	records, err := s.DeploymentStore.ListDeploymentsByService(ctx, filter.ServiceID)
	if err != nil {
		return DeploymentRecordPage{}, err
	}
	authorized := make([]DeploymentRecord, 0, len(records))
	for _, record := range records {
		_, _, authorizeErr := identity.Authorize(
			ctx,
			s.access,
			userID,
			identity.AccessTarget{
				ProjectID:     record.ProjectID,
				EnvironmentID: record.EnvironmentID,
			},
			identity.CapabilityServiceRead,
			false,
		)
		if authorizeErr == nil {
			authorized = append(authorized, record)
			continue
		}
		if !tenantReadDenied(authorizeErr) {
			return DeploymentRecordPage{}, authorizeErr
		}
	}
	return deploymentPageFromRecords(authorized, filter)
}

func readDeploymentPageFromList(
	ctx context.Context,
	store DeploymentStore,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	records, err := store.ListDeploymentsByService(ctx, filter.ServiceID)
	if err != nil {
		return DeploymentRecordPage{}, err
	}
	return deploymentPageFromRecords(records, filter)
}

func deploymentPageFromRecords(
	records []DeploymentRecord,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	if err := validateDeploymentReadFilter(filter); err != nil {
		return DeploymentRecordPage{}, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
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

func tenantIndexedDeploymentStore(
	base DeploymentStore,
	access identity.AccessStore,
) (*PostgresDeploymentStore, bool) {
	deployments, deploymentsOK := base.(*PostgresDeploymentStore)
	authority, authorityOK := access.(*identity.PostgresStore)
	if !deploymentsOK || !authorityOK ||
		deployments.Pool == nil ||
		authority.Pool == nil ||
		deployments.Pool != authority.Pool {
		return nil, false
	}
	return deployments, true
}
