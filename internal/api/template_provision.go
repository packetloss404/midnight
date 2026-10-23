// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"

	"github.com/midnight/midnight/internal/idempotency"
	templatepkg "github.com/midnight/midnight/internal/templates"
)

// TemplateProvisionRequest is the server-validated input to the atomic
// official-template intake transaction.
type TemplateProvisionRequest struct {
	TemplateSlug   string
	ProjectID      string
	EnvironmentID  string
	Name           string
	IdempotencyKey idempotency.Key
	IdempotencyRef idempotency.Reference
}

type TemplateProvisionResult struct {
	Run      templatepkg.ProvisionRun
	Service  TemplateCreatedService
	VolumeID string
}

// TemplateProvisioner atomically owns the control-plane half of the saga.
// A nil seam means one-click runtime provisioning is honestly unavailable.
type TemplateProvisioner interface {
	ProvisionTemplate(context.Context, TemplateProvisionRequest) (TemplateProvisionResult, error)
}
