// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

const (
	// QuotaAdvisoryLockKey serializes installation-wide inflight accounting.
	// Every quota path takes this lock before the workspace state row and
	// before BindVerifiedInTx takes LifecycleAdvisoryLockKey.
	QuotaAdvisoryLockKey int64 = 60718768232145224

	DefaultWorkspaceQuotaBytes         int64 = 5 << 30
	DefaultWorkspaceInflightBytes      int64 = 200 << 20
	DefaultWorkspaceInflightCount            = 4
	DefaultGlobalInflightBytes         int64 = 1 << 30
	DefaultGlobalInflightCount               = 32
	DefaultUploadLease                       = 10 * time.Minute
	DefaultOrphanChargeRetention             = 26 * time.Hour
	defaultQuotaReconcileRetryInterval       = 15 * time.Minute
)

var (
	ErrQuotaExceeded         = errors.New("source artifact: workspace quota exceeded")
	ErrUploadBackpressure    = errors.New("source artifact: upload backpressure")
	ErrReservationConflict   = errors.New("source artifact: upload reservation conflicts with existing intent")
	ErrReservationInProgress = errors.New("source artifact: upload reservation is in progress")
	ErrReservationInvalid    = errors.New("source artifact: upload reservation is invalid or expired")
	reservationIDRE          = regexp.MustCompile(`^suar_[0-9a-f]{32}$`)
	reservationIntentHashRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// QuotaConfig is the installation policy applied independently to every
// workspace. Plan-specific overrides are deliberately outside this gate.
type QuotaConfig struct {
	WorkspaceBytes         int64
	WorkspaceInflightBytes int64
	WorkspaceInflightCount int
	GlobalInflightBytes    int64
	GlobalInflightCount    int
	UploadLease            time.Duration
	OrphanChargeRetention  time.Duration
}

var DefaultQuotaConfig = QuotaConfig{
	WorkspaceBytes:         DefaultWorkspaceQuotaBytes,
	WorkspaceInflightBytes: DefaultWorkspaceInflightBytes,
	WorkspaceInflightCount: DefaultWorkspaceInflightCount,
	GlobalInflightBytes:    DefaultGlobalInflightBytes,
	GlobalInflightCount:    DefaultGlobalInflightCount,
	UploadLease:            DefaultUploadLease,
	OrphanChargeRetention:  DefaultOrphanChargeRetention,
}

func (config QuotaConfig) Validate(maxUploadBytes int64, minimumLease time.Duration) error {
	if maxUploadBytes <= 0 {
		return errors.New("source artifact quota: maximum upload bytes must be positive")
	}
	if minimumLease <= 0 {
		return errors.New("source artifact quota: minimum upload lease must be positive")
	}
	if config.WorkspaceBytes < maxUploadBytes {
		return fmt.Errorf("source artifact quota: workspace bytes must be at least %d", maxUploadBytes)
	}
	if config.WorkspaceInflightBytes < maxUploadBytes {
		return fmt.Errorf("source artifact quota: workspace inflight bytes must be at least %d", maxUploadBytes)
	}
	if config.WorkspaceInflightCount <= 0 {
		return errors.New("source artifact quota: workspace inflight count must be positive")
	}
	if config.GlobalInflightBytes < config.WorkspaceInflightBytes {
		return errors.New("source artifact quota: global inflight bytes must be at least workspace inflight bytes")
	}
	if config.GlobalInflightCount < config.WorkspaceInflightCount {
		return errors.New("source artifact quota: global inflight count must be at least workspace inflight count")
	}
	if config.UploadLease < minimumLease {
		return fmt.Errorf("source artifact quota: upload lease must be at least %s", minimumLease)
	}
	if config.OrphanChargeRetention < config.UploadLease {
		return errors.New("source artifact quota: orphan charge retention must be at least the upload lease")
	}
	return nil
}

// ReservationRequest is server-derived after current tenant authorization.
// IntentHash binds the complete preflight-visible intent without persisting
// the optional human message.
type ReservationRequest struct {
	WorkspaceID     string
	ProjectID       string
	ServiceID       string
	EnvironmentID   string
	ActorUserID     string
	SessionID       string
	IdempotencyKey  string
	IntentHash      string
	ExpectedDigest  string
	CompressedBytes int64
}

type Reservation struct {
	ID              string
	WorkspaceID     string
	ProjectID       string
	ServiceID       string
	EnvironmentID   string
	ActorUserID     string
	SessionID       string
	IdempotencyKey  string
	IntentHash      string
	ExpectedDigest  string
	CompressedBytes int64
	LeaseExpiresAt  time.Time
	ChargeExpiresAt time.Time
	PublishedAt     *time.Time
	CreatedAt       time.Time
}

// QuotaSnapshot is safe to return to the authenticated tenant in structured
// quota errors. It never includes another workspace's usage.
type QuotaSnapshot struct {
	QuotaBytes     int64
	UsedBytes      int64
	ReservedBytes  int64
	RequestedBytes int64
}

// LimitError preserves a stable sentinel while carrying tenant-safe details.
type LimitError struct {
	Kind     error
	Snapshot QuotaSnapshot
}

func (e *LimitError) Error() string {
	if e == nil || e.Kind == nil {
		return "source artifact: quota limit exceeded"
	}
	return e.Kind.Error()
}

func (e *LimitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

type quotaUsage struct {
	LogicalBytes           int64
	WorkspaceInflightBytes int64
	WorkspaceInflightCount int
	GlobalInflightBytes    int64
	GlobalInflightCount    int
	DigestAlreadyCharged   bool
}

// QuotaStore owns durable, multi-replica upload admission reservations.
type QuotaStore struct {
	Pool      *pg.Pool
	Config    QuotaConfig
	Now       func() time.Time
	IDFactory func() (string, error)
}

func NewQuotaStore(pool *pg.Pool, config QuotaConfig) *QuotaStore {
	return &QuotaStore{
		Pool:   pool,
		Config: config,
		Now:    func() time.Time { return time.Now().UTC() },
		IDFactory: func() (string, error) {
			var value [16]byte
			if _, err := rand.Read(value[:]); err != nil {
				return "", err
			}
			return "suar_" + hex.EncodeToString(value[:]), nil
		},
	}
}

// ReserveInTx admits one body before it is read. Storage accounting de-dupes
// by digest within a workspace; inflight accounting deliberately does not.
func (s *QuotaStore) ReserveInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ReservationRequest,
) (Reservation, error) {
	if err := s.validateAuthority(tx); err != nil {
		return Reservation{}, err
	}
	if err := validateReservationRequest(request); err != nil {
		return Reservation{}, err
	}
	now := s.now()
	if err := acquireQuotaLocks(ctx, tx, request.WorkspaceID, now); err != nil {
		return Reservation{}, err
	}

	existing, found, err := reservationForScopeInTx(ctx, tx, request)
	if err != nil {
		return Reservation{}, err
	}
	if found && existing.IntentHash != request.IntentHash {
		return Reservation{}, ErrReservationConflict
	}
	if found && existing.LeaseExpiresAt.After(now) {
		return Reservation{}, ErrReservationInProgress
	}

	usage, err := quotaUsageInTx(ctx, tx, request.WorkspaceID, request.ExpectedDigest, now)
	if err != nil {
		return Reservation{}, err
	}
	if err := s.enforceAdmission(usage, request.CompressedBytes); err != nil {
		return Reservation{}, err
	}
	leaseExpiresAt := now.Add(s.Config.UploadLease)
	chargeExpiresAt := now.Add(s.Config.OrphanChargeRetention)
	if found {
		renewedID, err := s.newID()
		if err != nil {
			return Reservation{}, fmt.Errorf("source artifact quota: renewed reservation id: %w", err)
		}
		if existing.ChargeExpiresAt.After(chargeExpiresAt) {
			chargeExpiresAt = existing.ChargeExpiresAt
		}
		err = tx.QueryRow(
			ctx,
			`UPDATE source_artifact_upload_reservations
			    SET id=$2,
			        session_id=$3,
			        lease_expires_at=$4,
			        charge_expires_at=$5,
			        published_at=COALESCE(published_at,$1),
			        updated_at=$1
			  WHERE id=$6
			  RETURNING id,workspace_id,project_id,service_id,environment_id,
			            actor_user_id,session_id,idempotency_key,intent_hash,
			            expected_digest,compressed_bytes,lease_expires_at,
			            charge_expires_at,published_at,created_at`,
			now,
			renewedID,
			request.SessionID,
			leaseExpiresAt,
			chargeExpiresAt,
			existing.ID,
		).Scan(reservationScanTargets(&existing)...)
		if err != nil {
			return Reservation{}, fmt.Errorf("source artifact quota: renew reservation: %w", err)
		}
		return existing, nil
	}

	id, err := s.newID()
	if err != nil {
		return Reservation{}, fmt.Errorf("source artifact quota: reservation id: %w", err)
	}
	var created Reservation
	err = tx.QueryRow(
		ctx,
		`INSERT INTO source_artifact_upload_reservations (
		    id,workspace_id,project_id,service_id,environment_id,
		    actor_user_id,session_id,idempotency_key,intent_hash,
		    expected_digest,compressed_bytes,lease_expires_at,
		    charge_expires_at,created_at,updated_at
		 ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		 RETURNING id,workspace_id,project_id,service_id,environment_id,
		           actor_user_id,session_id,idempotency_key,intent_hash,
		           expected_digest,compressed_bytes,lease_expires_at,
		           charge_expires_at,published_at,created_at`,
		id,
		request.WorkspaceID,
		request.ProjectID,
		request.ServiceID,
		request.EnvironmentID,
		request.ActorUserID,
		request.SessionID,
		request.IdempotencyKey,
		request.IntentHash,
		request.ExpectedDigest,
		request.CompressedBytes,
		leaseExpiresAt,
		chargeExpiresAt,
		now,
	).Scan(reservationScanTargets(&created)...)
	if err != nil {
		return Reservation{}, fmt.Errorf("source artifact quota: insert reservation: %w", err)
	}
	return created, nil
}

// ValidateInTx reacquires the quota locks in the required order and fences
// final admission to the exact live reservation and current configuration.
func (s *QuotaStore) ValidateInTx(
	ctx context.Context,
	tx *pg.Tx,
	reservationID string,
	request ReservationRequest,
) (Reservation, error) {
	if err := s.validateAuthority(tx); err != nil {
		return Reservation{}, err
	}
	if !reservationIDRE.MatchString(reservationID) {
		return Reservation{}, ErrReservationInvalid
	}
	if err := validateReservationRequest(request); err != nil {
		return Reservation{}, err
	}
	now := s.now()
	if err := acquireQuotaLocks(ctx, tx, request.WorkspaceID, now); err != nil {
		return Reservation{}, err
	}
	reservation, found, err := reservationByIDInTx(ctx, tx, reservationID, true)
	if err != nil {
		return Reservation{}, err
	}
	if !found ||
		!reservation.LeaseExpiresAt.After(now) ||
		!reservationMatchesRequest(reservation, request) {
		return Reservation{}, ErrReservationInvalid
	}
	usage, err := quotaUsageInTx(ctx, tx, request.WorkspaceID, request.ExpectedDigest, now)
	if err != nil {
		return Reservation{}, err
	}
	if usage.LogicalBytes > s.Config.WorkspaceBytes {
		return Reservation{}, &LimitError{
			Kind: ErrQuotaExceeded,
			Snapshot: QuotaSnapshot{
				QuotaBytes:    s.Config.WorkspaceBytes,
				UsedBytes:     usage.LogicalBytes,
				ReservedBytes: usage.LogicalBytes,
			},
		}
	}
	if usage.WorkspaceInflightBytes > s.Config.WorkspaceInflightBytes ||
		usage.WorkspaceInflightCount > s.Config.WorkspaceInflightCount ||
		usage.GlobalInflightBytes > s.Config.GlobalInflightBytes ||
		usage.GlobalInflightCount > s.Config.GlobalInflightCount {
		return Reservation{}, &LimitError{Kind: ErrUploadBackpressure}
	}
	return reservation, nil
}

// PublishInTx closes the process-pause gap between reservation validation and
// filesystem publication. The transaction holds quota global -> workspace ->
// lifecycle locks while Commit makes the canonical CAS path visible. A crash
// after publication may roll back the marker, but the pre-existing long charge
// and exact filesystem proof still prevent reconciliation from undercounting.
func (s *QuotaStore) PublishInTx(
	ctx context.Context,
	tx *pg.Tx,
	filesystem *FSStore,
	staged Staged,
	reservationID string,
	request ReservationRequest,
) (Artifact, error) {
	if filesystem == nil {
		return Artifact{}, errors.New("source artifact quota: filesystem store is required")
	}
	if staged.Digest != request.ExpectedDigest ||
		staged.CompressedBytes != request.CompressedBytes {
		return Artifact{}, ErrReservationInvalid
	}
	if _, err := s.ValidateInTx(ctx, tx, reservationID, request); err != nil {
		return Artifact{}, err
	}
	if err := acquireLifecycleLock(ctx, tx); err != nil {
		return Artifact{}, err
	}
	artifact, err := filesystem.Commit(ctx, staged)
	if err != nil {
		return Artifact{}, err
	}
	now := s.now()
	tag, err := tx.Exec(
		ctx,
		`UPDATE source_artifact_upload_reservations
		    SET lease_expires_at=$2,
		        charge_expires_at=GREATEST(charge_expires_at,$3),
		        published_at=COALESCE(published_at,$1),
		        updated_at=$1
		  WHERE id=$4`,
		now,
		now.Add(s.Config.UploadLease),
		now.Add(s.Config.OrphanChargeRetention),
		reservationID,
	)
	if err != nil {
		return Artifact{}, fmt.Errorf(
			"source artifact quota: mark published reservation: %w",
			err,
		)
	}
	if tag.RowsAffected() != 1 {
		return Artifact{}, ErrReservationInvalid
	}
	return artifact, nil
}

func (s *QuotaStore) ConsumeInTx(ctx context.Context, tx *pg.Tx, reservationID string) error {
	if err := s.validateAuthority(tx); err != nil {
		return err
	}
	tag, err := tx.Exec(
		ctx,
		`DELETE FROM source_artifact_upload_reservations WHERE id=$1`,
		reservationID,
	)
	if err != nil {
		return fmt.Errorf("source artifact quota: consume reservation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrReservationInvalid
	}
	return nil
}

// ChargeWorkspaceDigestInTx transfers a verified upload from its short-lived
// reservation into durable workspace accounting. The ledger deliberately has
// no project, service, environment, session, operation, or deployment foreign
// key, so product-row cascades cannot silently restore quota while CAS bytes
// remain.
func (s *QuotaStore) ChargeWorkspaceDigestInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	digest string,
	compressedBytes int64,
) error {
	return s.chargeWorkspaceDigestInTx(
		ctx,
		tx,
		workspaceID,
		digest,
		compressedBytes,
		s.now().Add(s.Config.OrphanChargeRetention),
	)
}

func (s *QuotaStore) chargeWorkspaceDigestInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	digest string,
	compressedBytes int64,
	reconcileAfter time.Time,
) error {
	if err := s.validateAuthority(tx); err != nil {
		return err
	}
	if strings.TrimSpace(workspaceID) == "" ||
		!digestRE.MatchString(digest) ||
		compressedBytes <= 0 ||
		reconcileAfter.IsZero() {
		return ErrReservationInvalid
	}
	now := s.now()
	if reconcileAfter.Before(now) {
		reconcileAfter = now
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO source_artifact_workspace_charges (
		    workspace_id,artifact_digest,compressed_bytes,reconcile_after,
		    created_at,updated_at
		 ) VALUES ($1,$2,$3,$4,$5,$5)
		 ON CONFLICT (workspace_id,artifact_digest) DO NOTHING`,
		workspaceID,
		digest,
		compressedBytes,
		reconcileAfter.UTC(),
		now,
	); err != nil {
		return fmt.Errorf("source artifact quota: charge workspace digest: %w", err)
	}
	var persistedBytes int64
	if err := tx.QueryRow(
		ctx,
		`SELECT compressed_bytes
		   FROM source_artifact_workspace_charges
		  WHERE workspace_id=$1
		    AND artifact_digest=$2
		  FOR UPDATE`,
		workspaceID,
		digest,
	).Scan(&persistedBytes); err != nil {
		return fmt.Errorf("source artifact quota: verify workspace charge: %w", err)
	}
	if persistedBytes != compressedBytes {
		return fmt.Errorf(
			"%w: workspace digest charge size differs",
			ErrReservationConflict,
		)
	}
	return nil
}

// WorkspaceChargesDigestInTx reports whether durable ledger accounting has
// already assumed physical responsibility for this workspace and digest.
func (s *QuotaStore) WorkspaceChargesDigestInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	digest string,
) (bool, error) {
	if err := s.validateAuthority(tx); err != nil {
		return false, err
	}
	var charged bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
		    SELECT 1
		      FROM source_artifact_workspace_charges
		     WHERE workspace_id=$1
		       AND artifact_digest=$2
		 )`,
		workspaceID,
		digest,
	).Scan(&charged); err != nil {
		return false, fmt.Errorf("source artifact quota: inspect workspace charge: %w", err)
	}
	return charged, nil
}

// WorkspaceRetainsDigestInTx reports whether normal retained-reference
// accounting already charges this workspace for the digest.
func (s *QuotaStore) WorkspaceRetainsDigestInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	digest string,
) (bool, error) {
	if err := s.validateAuthority(tx); err != nil {
		return false, err
	}
	var retained bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
		    SELECT 1
		      FROM source_artifact_refs AS artifact_ref
		      JOIN projects AS project
		        ON project.id=artifact_ref.project_id
		     WHERE project.workspace_id=$1
		       AND artifact_ref.artifact_digest=$2
		 )`,
		workspaceID,
		digest,
	).Scan(&retained); err != nil {
		return false, fmt.Errorf("source artifact quota: inspect retained digest: %w", err)
	}
	return retained, nil
}

// PreservePublishedInTx ends inflight accounting but retains the conservative
// logical charge when a fully verified body has already been published.
func (s *QuotaStore) PreservePublishedInTx(
	ctx context.Context,
	tx *pg.Tx,
	reservationID string,
) error {
	if err := s.validateAuthority(tx); err != nil {
		return err
	}
	now := s.now()
	tag, err := tx.Exec(
		ctx,
		`UPDATE source_artifact_upload_reservations
		    SET lease_expires_at=GREATEST(created_at + INTERVAL '1 microsecond',$2),
		        published_at=COALESCE(published_at,$2),
		        updated_at=$2
		  WHERE id=$1`,
		reservationID,
		now,
	)
	if err != nil {
		return fmt.Errorf("source artifact quota: preserve published reservation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrReservationInvalid
	}
	return nil
}

// Finish releases a reservation after the handler exits without committing
// the accepting transaction. Pre-publication failures delete the row. Once
// the filesystem blob is published, only its short inflight lease is ended;
// the long logical charge remains for convergence.
func (s *QuotaStore) Finish(ctx context.Context, reservationID string, published bool) error {
	if s == nil || s.Pool == nil {
		return errors.New("source artifact quota: Postgres pool is required")
	}
	if !reservationIDRE.MatchString(reservationID) {
		return ErrReservationInvalid
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, QuotaAdvisoryLockKey); err != nil {
			return fmt.Errorf("source artifact quota: acquire global lock: %w", err)
		}
		var workspaceID string
		err := tx.QueryRow(
			ctx,
			`SELECT workspace_id
			   FROM source_artifact_upload_reservations
			  WHERE id=$1`,
			reservationID,
		).Scan(&workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("source artifact quota: locate reservation: %w", err)
		}
		if err := lockWorkspaceQuotaState(ctx, tx, workspaceID, s.now()); err != nil {
			return err
		}
		if published {
			return s.PreservePublishedInTx(ctx, tx, reservationID)
		}
		tag, err := tx.Exec(
			ctx,
			`DELETE FROM source_artifact_upload_reservations
			  WHERE id=$1
			    AND published_at IS NULL`,
			reservationID,
		)
		if err != nil {
			return fmt.Errorf("source artifact quota: cancel reservation: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// A renewed, previously published, or crash-ambiguous reservation
			// must keep its long charge. End only the inflight lease; the
			// proof-based reconciler is the sole authority that may remove it.
			if err := s.PreservePublishedInTx(ctx, tx, reservationID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReconcileExpired removes bounded historical charges only after exact
// database/filesystem evidence proves a retained reference already accounts
// for the globally de-duplicated digest or no committed CAS/quarantine bytes
// remain. Expiry is eligibility, never authorization to undercount.
func (s *QuotaStore) ReconcileExpired(
	ctx context.Context,
	filesystem *FSStore,
	now time.Time,
	maxItems int,
) (int64, error) {
	if s == nil || s.Pool == nil {
		return 0, errors.New("source artifact quota: Postgres pool is required")
	}
	if filesystem == nil {
		return 0, errors.New("source artifact quota: filesystem store is required")
	}
	if now.IsZero() {
		now = s.now()
	}
	if maxItems <= 0 {
		return 0, errors.New("source artifact quota: cleanup max items must be positive")
	}
	var removed int64
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, QuotaAdvisoryLockKey); err != nil {
			return fmt.Errorf("source artifact quota: acquire global lock: %w", err)
		}
		rows, err := tx.Query(
			ctx,
			`SELECT candidate_kind,candidate_id,workspace_id,digest,
			        compressed_bytes
			   FROM (
			       SELECT 0 AS candidate_kind,
			              id AS candidate_id,
			              workspace_id,
			              expected_digest AS digest,
			              compressed_bytes,
			              charge_expires_at AS eligible_at
			         FROM source_artifact_upload_reservations
			        WHERE charge_expires_at <= $1
			        UNION ALL
			       SELECT 1 AS candidate_kind,
			              workspace_id || ':' || artifact_digest AS candidate_id,
			              workspace_id,
			              artifact_digest AS digest,
			              compressed_bytes,
			              reconcile_after AS eligible_at
			         FROM source_artifact_workspace_charges
			        WHERE reconcile_after <= $1
			   ) AS candidates
			  ORDER BY eligible_at,candidate_kind,candidate_id
			  LIMIT $2`,
			now.UTC(),
			maxItems,
		)
		if err != nil {
			return fmt.Errorf("source artifact quota: list reconciliation candidates: %w", err)
		}
		type candidate struct {
			kind                    int
			id, workspaceID, digest string
			compressedBytes         int64
		}
		var candidates []candidate
		workspaces := map[string]struct{}{}
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.kind,
				&value.id,
				&value.workspaceID,
				&value.digest,
				&value.compressedBytes,
			); err != nil {
				rows.Close()
				return fmt.Errorf("source artifact quota: scan reconciliation candidate: %w", err)
			}
			candidates = append(candidates, value)
			workspaces[value.workspaceID] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("source artifact quota: list reconciliation candidates: %w", err)
		}
		rows.Close()
		workspaceIDs := make([]string, 0, len(workspaces))
		for workspaceID := range workspaces {
			workspaceIDs = append(workspaceIDs, workspaceID)
		}
		sort.Strings(workspaceIDs)
		for _, workspaceID := range workspaceIDs {
			if err := lockWorkspaceQuotaState(ctx, tx, workspaceID, now.UTC()); err != nil {
				return err
			}
		}
		if err := acquireLifecycleLock(ctx, tx); err != nil {
			return err
		}
		for _, value := range candidates {
			switch value.kind {
			case 0:
				reconciled, err := s.reconcileReservationCandidateInTx(
					ctx,
					tx,
					filesystem,
					value.id,
					value.workspaceID,
					value.digest,
					value.compressedBytes,
					now.UTC(),
				)
				if err != nil {
					return err
				}
				removed += reconciled
			case 1:
				reconciled, err := s.reconcileWorkspaceChargeCandidateInTx(
					ctx,
					tx,
					filesystem,
					value.workspaceID,
					value.digest,
					now.UTC(),
				)
				if err != nil {
					return err
				}
				removed += reconciled
			default:
				return errors.New("source artifact quota: invalid reconciliation candidate")
			}
		}
		return nil
	})
	return removed, err
}

func (s *QuotaStore) reconcileReservationCandidateInTx(
	ctx context.Context,
	tx *pg.Tx,
	filesystem *FSStore,
	reservationID string,
	workspaceID string,
	digest string,
	compressedBytes int64,
	now time.Time,
) (int64, error) {
	charged, err := s.WorkspaceChargesDigestInTx(ctx, tx, workspaceID, digest)
	if err != nil {
		return 0, err
	}
	if !charged {
		retained, err := s.WorkspaceRetainsDigestInTx(ctx, tx, workspaceID, digest)
		if err != nil {
			return 0, err
		}
		otherCharge, err := s.otherWorkspaceChargesDigestInTx(
			ctx,
			tx,
			workspaceID,
			digest,
		)
		if err != nil {
			return 0, err
		}
		switch {
		case retained:
			err = s.chargeWorkspaceDigestInTx(
				ctx,
				tx,
				workspaceID,
				digest,
				compressedBytes,
				now.Add(defaultQuotaReconcileRetryInterval),
			)
		case otherCharge:
			// The installation-wide CAS bytes already have one durable owner.
		case !otherCharge:
			var absent bool
			absent, err = filesystem.committedArtifactBytesAbsent(digest)
			if err == nil && !absent {
				// An expired, crash-ambiguous reservation is the only durable
				// evidence for still-present bytes. Transfer it to the
				// workspace ledger before releasing scoped foreign keys.
				err = s.chargeWorkspaceDigestInTx(
					ctx,
					tx,
					workspaceID,
					digest,
					compressedBytes,
					now.Add(defaultQuotaReconcileRetryInterval),
				)
			}
		}
		if err != nil {
			return 0, err
		}
	}
	tag, err := tx.Exec(
		ctx,
		`DELETE FROM source_artifact_upload_reservations
		  WHERE id=$1
		    AND charge_expires_at <= $2`,
		reservationID,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"source artifact quota: reconcile reservation: %w",
			err,
		)
	}
	return tag.RowsAffected(), nil
}

func (s *QuotaStore) reconcileWorkspaceChargeCandidateInTx(
	ctx context.Context,
	tx *pg.Tx,
	filesystem *FSStore,
	workspaceID string,
	digest string,
	now time.Time,
) (int64, error) {
	retained, err := s.WorkspaceRetainsDigestInTx(ctx, tx, workspaceID, digest)
	if err != nil {
		return 0, err
	}
	if !retained {
		otherCharge, err := s.otherWorkspaceChargesDigestInTx(
			ctx,
			tx,
			workspaceID,
			digest,
		)
		if err != nil {
			return 0, err
		}
		absent := false
		if !otherCharge {
			absent, err = filesystem.committedArtifactBytesAbsent(digest)
			if err != nil {
				return 0, err
			}
		}
		if otherCharge || absent {
			tag, err := tx.Exec(
				ctx,
				`DELETE FROM source_artifact_workspace_charges
				  WHERE workspace_id=$1
				    AND artifact_digest=$2
				    AND reconcile_after <= $3`,
				workspaceID,
				digest,
				now,
			)
			if err != nil {
				return 0, fmt.Errorf(
					"source artifact quota: reconcile workspace charge: %w",
					err,
				)
			}
			return tag.RowsAffected(), nil
		}
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE source_artifact_workspace_charges
		    SET reconcile_after=GREATEST(reconcile_after,$3),
		        updated_at=$2
		  WHERE workspace_id=$1
		    AND artifact_digest=$4`,
		workspaceID,
		now,
		now.Add(defaultQuotaReconcileRetryInterval),
		digest,
	); err != nil {
		return 0, fmt.Errorf(
			"source artifact quota: defer workspace charge reconciliation: %w",
			err,
		)
	}
	return 0, nil
}

func (s *QuotaStore) otherWorkspaceChargesDigestInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	digest string,
) (bool, error) {
	var charged bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
		    SELECT 1
		      FROM source_artifact_workspace_charges
		     WHERE artifact_digest=$1
		       AND workspace_id<>$2
		 )`,
		digest,
		workspaceID,
	).Scan(&charged); err != nil {
		return false, fmt.Errorf(
			"source artifact quota: inspect global digest charges: %w",
			err,
		)
	}
	return charged, nil
}

// ReconcileExpiredUploadReservations is the janitor-facing proof-based
// adapter. Call it only after a successful, non-skipped lifecycle GC pass.
func (s *PostgresStore) ReconcileExpiredUploadReservations(
	ctx context.Context,
	filesystem *FSStore,
	now time.Time,
	maxItems int,
) (int64, error) {
	if s == nil || s.Pool == nil {
		return 0, errors.New("source artifact quota: Postgres pool is required")
	}
	return NewQuotaStore(s.Pool, DefaultQuotaConfig).ReconcileExpired(
		ctx,
		filesystem,
		now,
		maxItems,
	)
}

func (s *QuotaStore) enforceAdmission(usage quotaUsage, requestedBytes int64) error {
	logicalRequested := requestedBytes
	if usage.DigestAlreadyCharged {
		logicalRequested = 0
	}
	if exceedsInt64Limit(s.Config.WorkspaceBytes, usage.LogicalBytes, logicalRequested) {
		return &LimitError{
			Kind: ErrQuotaExceeded,
			Snapshot: QuotaSnapshot{
				QuotaBytes:     s.Config.WorkspaceBytes,
				UsedBytes:      usage.LogicalBytes,
				ReservedBytes:  usage.LogicalBytes,
				RequestedBytes: requestedBytes,
			},
		}
	}
	if exceedsInt64Limit(s.Config.WorkspaceInflightBytes, usage.WorkspaceInflightBytes, requestedBytes) ||
		usage.WorkspaceInflightCount >= s.Config.WorkspaceInflightCount ||
		exceedsInt64Limit(s.Config.GlobalInflightBytes, usage.GlobalInflightBytes, requestedBytes) ||
		usage.GlobalInflightCount >= s.Config.GlobalInflightCount {
		return &LimitError{
			Kind: ErrUploadBackpressure,
			Snapshot: QuotaSnapshot{
				ReservedBytes:  usage.WorkspaceInflightBytes,
				RequestedBytes: requestedBytes,
			},
		}
	}
	return nil
}

func exceedsInt64Limit(limit, used, requested int64) bool {
	if limit < 0 || used < 0 || requested < 0 || used > limit {
		return true
	}
	return requested > limit-used
}

func (s *QuotaStore) validateAuthority(tx *pg.Tx) error {
	if s == nil || s.Pool == nil || tx == nil {
		return errors.New("source artifact quota: shared Postgres transaction is required")
	}
	if err := s.Config.Validate(DefaultLimits.MaxCompressedBytes, DefaultUploadLease); err != nil {
		return err
	}
	return nil
}

func (s *QuotaStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (s *QuotaStore) newID() (string, error) {
	if s == nil || s.IDFactory == nil {
		return "", errors.New("reservation ID factory is required")
	}
	id, err := s.IDFactory()
	if err != nil {
		return "", err
	}
	if !reservationIDRE.MatchString(id) {
		return "", errors.New("reservation ID factory returned an invalid ID")
	}
	return id, nil
}

func validateReservationRequest(request ReservationRequest) error {
	for name, value := range map[string]string{
		"workspace":       request.WorkspaceID,
		"project":         request.ProjectID,
		"service":         request.ServiceID,
		"environment":     request.EnvironmentID,
		"actor":           request.ActorUserID,
		"session":         request.SessionID,
		"idempotency key": request.IdempotencyKey,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s is invalid", ErrReservationInvalid, name)
		}
	}
	if !reservationIntentHashRE.MatchString(request.IntentHash) ||
		!digestRE.MatchString(request.ExpectedDigest) ||
		request.CompressedBytes <= 0 ||
		len(request.IdempotencyKey) > 255 {
		return ErrReservationInvalid
	}
	return nil
}

func acquireQuotaLocks(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	now time.Time,
) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, QuotaAdvisoryLockKey); err != nil {
		return fmt.Errorf("source artifact quota: acquire global lock: %w", err)
	}
	return lockWorkspaceQuotaState(ctx, tx, workspaceID, now)
}

func lockWorkspaceQuotaState(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	now time.Time,
) error {
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO source_artifact_workspace_quota_state(workspace_id,updated_at)
		 VALUES ($1,$2)
		 ON CONFLICT (workspace_id) DO NOTHING`,
		workspaceID,
		now,
	); err != nil {
		return fmt.Errorf("source artifact quota: initialize workspace state: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT workspace_id
		   FROM source_artifact_workspace_quota_state
		  WHERE workspace_id=$1
		  FOR UPDATE`,
		workspaceID,
	); err != nil {
		return fmt.Errorf("source artifact quota: lock workspace state: %w", err)
	}
	return nil
}

func quotaUsageInTx(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	expectedDigest string,
	now time.Time,
) (quotaUsage, error) {
	var usage quotaUsage
	err := tx.QueryRow(
		ctx,
		`WITH charged_storage AS (
		    SELECT artifact_digest AS digest,
		           compressed_bytes AS bytes
		      FROM source_artifact_workspace_charges
		     WHERE workspace_id=$1
		 ),
		 reserved AS (
		    SELECT expected_digest AS digest,
		           MAX(compressed_bytes)::BIGINT AS bytes
		      FROM source_artifact_upload_reservations
		     WHERE workspace_id=$1
		     GROUP BY expected_digest
		 ),
		 logical AS (
		    SELECT digest,MAX(bytes)::BIGINT AS bytes
		      FROM (
		          SELECT digest,bytes FROM charged_storage
		          UNION ALL
		          SELECT digest,bytes FROM reserved
		      ) AS charged
		     GROUP BY digest
		 ),
		 workspace_inflight AS (
		    SELECT COALESCE(SUM(compressed_bytes),0)::BIGINT AS bytes,
		           COUNT(*)::INTEGER AS count
		      FROM source_artifact_upload_reservations
		     WHERE workspace_id=$1
		       AND lease_expires_at > $3
		 ),
		 global_inflight AS (
		    SELECT COALESCE(SUM(compressed_bytes),0)::BIGINT AS bytes,
		           COUNT(*)::INTEGER AS count
		      FROM source_artifact_upload_reservations
		     WHERE lease_expires_at > $3
		 )
		 SELECT COALESCE((SELECT SUM(bytes) FROM logical),0)::BIGINT,
		        workspace_inflight.bytes,
		        workspace_inflight.count,
		        global_inflight.bytes,
		        global_inflight.count,
		        EXISTS (SELECT 1 FROM logical WHERE digest=$2)
		   FROM workspace_inflight,global_inflight`,
		workspaceID,
		expectedDigest,
		now,
	).Scan(
		&usage.LogicalBytes,
		&usage.WorkspaceInflightBytes,
		&usage.WorkspaceInflightCount,
		&usage.GlobalInflightBytes,
		&usage.GlobalInflightCount,
		&usage.DigestAlreadyCharged,
	)
	if err != nil {
		return quotaUsage{}, fmt.Errorf("source artifact quota: calculate usage: %w", err)
	}
	return usage, nil
}

func reservationForScopeInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ReservationRequest,
) (Reservation, bool, error) {
	var reservation Reservation
	err := tx.QueryRow(
		ctx,
		`SELECT id,workspace_id,project_id,service_id,environment_id,
		        actor_user_id,session_id,idempotency_key,intent_hash,
		        expected_digest,compressed_bytes,lease_expires_at,
		        charge_expires_at,published_at,created_at
		   FROM source_artifact_upload_reservations
		  WHERE actor_user_id=$1
		    AND project_id=$2
		    AND service_id=$3
		    AND idempotency_key=$4
		  FOR UPDATE`,
		request.ActorUserID,
		request.ProjectID,
		request.ServiceID,
		request.IdempotencyKey,
	).Scan(reservationScanTargets(&reservation)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, fmt.Errorf("source artifact quota: find scoped reservation: %w", err)
	}
	return reservation, true, nil
}

func reservationByIDInTx(
	ctx context.Context,
	tx *pg.Tx,
	id string,
	lock bool,
) (Reservation, bool, error) {
	query := `SELECT id,workspace_id,project_id,service_id,environment_id,
	                actor_user_id,session_id,idempotency_key,intent_hash,
	                expected_digest,compressed_bytes,lease_expires_at,
	                charge_expires_at,published_at,created_at
	           FROM source_artifact_upload_reservations
	          WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var reservation Reservation
	err := tx.QueryRow(ctx, query, id).Scan(reservationScanTargets(&reservation)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, fmt.Errorf("source artifact quota: get reservation: %w", err)
	}
	return reservation, true, nil
}

func reservationScanTargets(reservation *Reservation) []any {
	return []any{
		&reservation.ID,
		&reservation.WorkspaceID,
		&reservation.ProjectID,
		&reservation.ServiceID,
		&reservation.EnvironmentID,
		&reservation.ActorUserID,
		&reservation.SessionID,
		&reservation.IdempotencyKey,
		&reservation.IntentHash,
		&reservation.ExpectedDigest,
		&reservation.CompressedBytes,
		&reservation.LeaseExpiresAt,
		&reservation.ChargeExpiresAt,
		&reservation.PublishedAt,
		&reservation.CreatedAt,
	}
}

func reservationMatchesRequest(reservation Reservation, request ReservationRequest) bool {
	return reservation.WorkspaceID == request.WorkspaceID &&
		reservation.ProjectID == request.ProjectID &&
		reservation.ServiceID == request.ServiceID &&
		reservation.EnvironmentID == request.EnvironmentID &&
		reservation.ActorUserID == request.ActorUserID &&
		reservation.SessionID == request.SessionID &&
		reservation.IdempotencyKey == request.IdempotencyKey &&
		reservation.IntentHash == request.IntentHash &&
		reservation.ExpectedDigest == request.ExpectedDigest &&
		reservation.CompressedBytes == request.CompressedBytes
}
