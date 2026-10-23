// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// ErrEnvironmentDuplicateIdempotencyConflict means an Idempotency-Key was
// already committed for a different environment-duplicate request.
var ErrEnvironmentDuplicateIdempotencyConflict = errors.New("environment duplicate idempotency conflict")

// EnvironmentDuplicateRequest is the normalized store boundary for the
// config-bearing duplicate operation. The Postgres implementation owns the
// entire environment/config/variable copy in one transaction.
type EnvironmentDuplicateRequest struct {
	SourceID       orailtypes.ID
	ProjectID      orailtypes.ID
	Slug           string
	Name           string
	Tier           string
	IsProtected    *bool
	IdempotencyKey idempotency.Key
}

// EnvironmentDuplicateOmission makes unsupported resource classes explicit.
// The copier never silently implies that runtime state or public endpoints
// were cloned.
type EnvironmentDuplicateOmission struct {
	Resource string `json:"resource"`
	Count    int    `json:"count"`
	Reason   string `json:"reason"`
}

// EnvironmentDuplicateResult embeds Environment so older clients that decode
// the metadata-only response keep working. Copy counters and omissions are
// redaction-safe; no variable plaintext or envelope bytes are exposed.
type EnvironmentDuplicateResult struct {
	Environment
	CopyConfig             bool                           `json:"copy_config"`
	CopiedServiceConfigs   int                            `json:"copied_service_configs"`
	CopiedVariables        int                            `json:"copied_variables"`
	CopiedVariableVersions int                            `json:"copied_variable_versions"`
	OmittedResources       []EnvironmentDuplicateOmission `json:"omitted_resources"`
	Replayed               bool                           `json:"-"`
}

// EnvironmentDuplicator is intentionally separate from EnvironmentStore:
// memory mode can support metadata lifecycle without pretending it can provide
// the transactional, encrypted copy guarantees of the Postgres adapter.
type EnvironmentDuplicator interface {
	DuplicateEnvironment(context.Context, EnvironmentDuplicateRequest) (EnvironmentDuplicateResult, error)
}
