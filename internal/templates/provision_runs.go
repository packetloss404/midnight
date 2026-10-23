// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
)

// ProvisionRun is the redaction-safe state of one official template saga.
// Secret values and database query output never cross this boundary.
type ProvisionRun struct {
	ID                    string            `json:"id"`
	TemplateSlug          string            `json:"template_slug"`
	ProjectID             string            `json:"project_id"`
	EnvironmentID         string            `json:"environment_id"`
	ServiceID             string            `json:"service_id"`
	VolumeID              string            `json:"volume_id"`
	OperationID           string            `json:"operation_id"`
	DeploymentID          string            `json:"deployment_id"`
	State                 string            `json:"state"`
	ConnectivityVerified  bool              `json:"connectivity_verified"`
	PersistenceVerified   bool              `json:"persistence_verified"`
	WriteProbeContainerID string            `json:"write_probe_container_id,omitempty"`
	ReadProbeContainerID  string            `json:"read_probe_container_id,omitempty"`
	MarkerSHA256          string            `json:"marker_sha256,omitempty"`
	CleanupCompleted      bool              `json:"cleanup_completed"`
	FailureCode           failurecodes.Code `json:"failure_code,omitempty"`
	FailureReason         string            `json:"failure_reason,omitempty"`
	StartedAt             *time.Time        `json:"started_at,omitempty"`
	FinishedAt            *time.Time        `json:"finished_at,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// VerificationEvidence is the metadata-only proof produced by the runtime
// verifier after its write, container replacement, and read sequence.
type VerificationEvidence struct {
	WriteProbeContainerID string
	ReadProbeContainerID  string
	MarkerSHA256          string
}

// RunRecorder is the worker-side durable state seam.
type RunRecorder interface {
	MarkRunning(context.Context, string) error
	MarkVerified(context.Context, string, VerificationEvidence) error
	MarkCleanupCompleted(context.Context, string) error
	MarkSucceeded(context.Context, string) error
	MarkFailed(context.Context, string, failurecodes.Code, string) error
}

// RunReader is the API-side status seam.
type RunReader interface {
	GetProvisionRun(context.Context, string, string) (ProvisionRun, error)
}

var ErrProvisionRunNotFound = errors.New("template provision run not found")

type PostgresRunStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewPostgresRunStore(pool *pg.Pool) *PostgresRunStore {
	return &PostgresRunStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresRunStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresRunStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("template provision run store: pool is required")
	}
	return nil
}

func (s *PostgresRunStore) MarkRunning(ctx context.Context, deploymentID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE template_provision_runs
		   SET state='running', started_at=COALESCE(started_at,$1), updated_at=$1
		 WHERE deployment_id=$2 AND state IN ('queued','running')`,
		s.now(), strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("template provision run start: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProvisionRunNotFound
	}
	return nil
}

func (s *PostgresRunStore) MarkVerified(ctx context.Context, deploymentID string, evidence VerificationEvidence) error {
	if err := s.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(evidence.WriteProbeContainerID) == "" ||
		strings.TrimSpace(evidence.ReadProbeContainerID) == "" ||
		len(strings.TrimSpace(evidence.MarkerSHA256)) != 64 {
		return errors.New("template provision success requires complete smoke evidence")
	}
	now := s.now()
	tag, err := s.Pool.Exec(ctx, `
		UPDATE template_provision_runs
		   SET connectivity_verified=TRUE,
		       persistence_verified=TRUE,
		       write_probe_container_id=$1,
		       read_probe_container_id=$2,
		       marker_sha256=$3,
		       failure_code='',
		       failure_reason='',
		       started_at=COALESCE(started_at,$4),
		       updated_at=$4
		 WHERE deployment_id=$5 AND state IN ('queued','running')`,
		evidence.WriteProbeContainerID, evidence.ReadProbeContainerID,
		evidence.MarkerSHA256, now, strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("template provision run verification evidence: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProvisionRunNotFound
	}
	return nil
}

func (s *PostgresRunStore) MarkSucceeded(ctx context.Context, deploymentID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now()
	tag, err := s.Pool.Exec(ctx, `
		UPDATE template_provision_runs
		   SET state='succeeded', finished_at=COALESCE(finished_at,$1), updated_at=$1
		 WHERE deployment_id=$2
		   AND state IN ('queued','running','succeeded')
		   AND cleanup_completed
		   AND connectivity_verified
		   AND persistence_verified
		   AND write_probe_container_id <> ''
		   AND read_probe_container_id <> ''
		   AND marker_sha256 <> ''`,
		now, strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("template provision run success: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProvisionRunNotFound
	}
	return nil
}

func (s *PostgresRunStore) MarkCleanupCompleted(ctx context.Context, deploymentID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE template_provision_runs
		   SET cleanup_completed=TRUE, updated_at=$1
		 WHERE deployment_id=$2 AND state IN ('queued','running','failed')`,
		s.now(), strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("template provision run cleanup completion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProvisionRunNotFound
	}
	return nil
}

func (s *PostgresRunStore) MarkFailed(ctx context.Context, deploymentID string, code failurecodes.Code, reason string) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now()
	reason = safeTemplateFailureReason(reason)
	tag, err := s.Pool.Exec(ctx, `
		UPDATE template_provision_runs
		   SET state='failed', failure_code=$1, failure_reason=$2,
		       started_at=COALESCE(started_at,$3), finished_at=$3, updated_at=$3
		 WHERE deployment_id=$4 AND state IN ('queued','running')`,
		string(code), strings.TrimSpace(reason), now, strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("template provision run failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProvisionRunNotFound
	}
	return nil
}

func (s *PostgresRunStore) GetProvisionRun(ctx context.Context, slug, id string) (ProvisionRun, error) {
	if err := s.ready(); err != nil {
		return ProvisionRun{}, err
	}
	var run ProvisionRun
	err := s.Pool.QueryRow(ctx, `
		SELECT id,template_slug,project_id,environment_id,service_id,volume_id,
		       operation_id,deployment_id,state,connectivity_verified,persistence_verified,
		       write_probe_container_id,read_probe_container_id,marker_sha256,
		       failure_code,failure_reason,cleanup_completed,started_at,finished_at,created_at,updated_at
		  FROM template_provision_runs
		 WHERE id=$1 AND template_slug=$2`,
		strings.TrimSpace(id), strings.TrimSpace(slug),
	).Scan(
		&run.ID, &run.TemplateSlug, &run.ProjectID, &run.EnvironmentID,
		&run.ServiceID, &run.VolumeID, &run.OperationID, &run.DeploymentID,
		&run.State, &run.ConnectivityVerified, &run.PersistenceVerified,
		&run.WriteProbeContainerID, &run.ReadProbeContainerID, &run.MarkerSHA256,
		&run.FailureCode, &run.FailureReason, &run.CleanupCompleted, &run.StartedAt, &run.FinishedAt,
		&run.CreatedAt, &run.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionRun{}, ErrProvisionRunNotFound
	}
	if err != nil {
		return ProvisionRun{}, fmt.Errorf("template provision run get: %w", err)
	}
	return run, nil
}

func safeTemplateFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	// Runtime errors for this path should contain only resource ids and exit
	// codes. Still fail closed on common credential-bearing shapes before the
	// value reaches a durable/API-visible column.
	lower := strings.ToLower(reason)
	for _, marker := range []string{
		"password=", "postgres://", "redis://", "mysql://", "mongodb://",
		"authorization:", "bearer ",
	} {
		if strings.Contains(lower, marker) {
			reason = "official template runtime verification failed; inspect redacted worker diagnostics"
			break
		}
	}
	const maxFailureReasonBytes = 512
	if len(reason) > maxFailureReasonBytes {
		reason = reason[:maxFailureReasonBytes]
	}
	return reason
}
