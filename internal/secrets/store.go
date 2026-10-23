// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"errors"
)

// ErrVariableScopeIntegrity is returned when a durable variable attempts to
// cross a project/environment/service boundary. The error deliberately omits
// the variable plaintext.
var ErrVariableScopeIntegrity = errors.New("midnight: variable scope integrity violation")

// ErrVariableVersionConflict is returned when a lifecycle mutation was based
// on stale metadata. Callers may inspect VariableVersionConflict for
// redaction-safe expected/actual versions.
var ErrVariableVersionConflict = errors.New("midnight: variable version conflict")

// VariableInfrastructureError marks a storage, key-custody, or audit failure
// at the decrypt boundary. Resolvers can distinguish it from an actually
// missing reference and report an honest internal failure code.
type VariableInfrastructureError struct {
	Err error
}

func (e *VariableInfrastructureError) Error() string {
	// This text crosses durable deployment/build and product API boundaries.
	// Keep the wrapped cause available through errors.Unwrap for trusted
	// diagnostics, but never render storage, key-custody, audit, or host-path
	// detail into the public reason.
	return "midnight: variable infrastructure failure"
}

func (e *VariableInfrastructureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsVariableInfrastructureFailure is a cross-package marker used by the
// deploy failure-code mapper.
func (*VariableInfrastructureError) IsVariableInfrastructureFailure() {}

func variableInfrastructureError(err error) error {
	if err == nil {
		return nil
	}
	var existing *VariableInfrastructureError
	if errors.As(err, &existing) {
		return err
	}
	return &VariableInfrastructureError{Err: err}
}

// VariableVersionConflict contains only metadata. It must never include an
// envelope or plaintext value.
type VariableVersionConflict struct {
	Expected int
	Actual   int
}

func (e *VariableVersionConflict) Error() string {
	return ErrVariableVersionConflict.Error()
}

func (e *VariableVersionConflict) Unwrap() error {
	return ErrVariableVersionConflict
}

// PersistedVariable is the encrypted-at-rest record owned by an EnvelopeStore.
// Meta is safe for read APIs. Envelope is opaque encrypted material; plaintext
// must never be added to this shape.
type PersistedVariable struct {
	Meta     VariableMeta
	Envelope Envelope
}

// VariableOwnershipScope is the canonical tenant lineage of one variable
// collection. Service is empty only for the historical shared-variable
// sentinel; every other field is required. Privacy classification is not part
// of ownership and deliberately does not appear here.
type VariableOwnershipScope struct {
	Workspace   string
	Project     string
	Environment string
	Service     string
}

// EnvelopeStore persists encrypted variables independently of a Vault process.
// Implementations must enforce the project/environment/service relationship on
// writes and must never persist decrypted values.
type EnvelopeStore interface {
	Put(ctx context.Context, variable PersistedVariable) error
	Get(ctx context.Context, id VariableID) (PersistedVariable, error)
	Find(ctx context.Context, projectID, environmentID, serviceID, name string) (PersistedVariable, error)
	List(ctx context.Context) ([]PersistedVariable, error)
}

// LifecycleEnvelopeStore adds atomic compare-and-swap writes and safe delete
// tombstones. PostgreSQL implements this interface; legacy/test stores may
// continue to implement EnvelopeStore and use the Vault's process-local CAS.
type LifecycleEnvelopeStore interface {
	EnvelopeStore
	PutCAS(ctx context.Context, variable PersistedVariable, expectedVersion int) error
	DeleteCAS(ctx context.Context, id VariableID, expectedVersion int) error
	GetVersion(ctx context.Context, id VariableID, version int) (PersistedVariable, error)
}
