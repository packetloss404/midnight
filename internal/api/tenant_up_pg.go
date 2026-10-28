// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	tenantUpIntakePathTemplate = v1alphaPrefix + "/services/{service_id}/up"
	tenantUpMessageMaxBytes    = 256
	// A byte-identical concurrent upload can reach preflight after the first
	// request commits its quota reservation but before it stores the terminal
	// idempotency response. Briefly coalesce that gap so the second request can
	// return the exact replay once the first consumes its reservation. The wait
	// stays bounded so stalled or genuinely long-running uploads retain the
	// documented IDEMPOTENCY_REQUEST_IN_PROGRESS response.
	tenantUpReplayCoalesceWindow = 2 * time.Second
	tenantUpReplayCoalescePoll   = 25 * time.Millisecond
)

// PreflightSourceUp rejects invalid, unauthorized, foreign, or restricted
// targets before an upload body is read or staged. This is an optimization and
// resource-abuse boundary only: DeploySourceArtifact repeats the exact current
// authority check in its accepting SERIALIZABLE transaction.
func (c *PostgresTenantDeployIntakeCoordinator) PreflightSourceUp(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (TenantSourceUpAdmission, error) {
	if err := c.validateCoordinatorAuthority(); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if err := c.validateSourceArtifactAuthority(); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" ||
		strings.TrimSpace(request.EnvironmentID) == "" {
		return TenantSourceUpAdmission{}, fmt.Errorf("%w: service and environment are required", ErrResourceInvalid)
	}
	if err := validateTenantUpMessage(request.CommitMessage); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if !canonicalSHA256Digest(request.ExpectedDigest) ||
		request.CompressedBytes <= 0 ||
		request.CompressedBytes > sourceartifact.DefaultLimits.MaxCompressedBytes {
		return TenantSourceUpAdmission{}, fmt.Errorf("%w: advertised source artifact is invalid", ErrResourceInvalid)
	}
	intentHash, err := tenantUpPreflightIntentHash(request)
	if err != nil {
		return TenantSourceUpAdmission{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantSourceUpAdmission{}, err
	}
	var admission TenantSourceUpAdmission
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			_, authorization, err := authorizeTenantDeployInTx(
				ctx,
				tx,
				request.Principal,
				request.Session,
				string(request.ServiceID),
				strings.TrimSpace(request.EnvironmentID),
				c.now().Truncate(time.Microsecond),
			)
			if err != nil {
				return err
			}
			if err := validateTenantAgentSourceInTx(ctx, tx, request.Principal, authorization, request.EnvironmentID, request.AgentSource); err != nil {
				return err
			}
			reservation, err := c.SourceArtifactQuota.ReserveInTx(
				ctx,
				tx,
				sourceartifact.ReservationRequest{
					WorkspaceID:     authorization.WorkspaceID,
					ProjectID:       authorization.ProjectID,
					ServiceID:       string(authorization.Service.ID),
					EnvironmentID:   strings.TrimSpace(request.EnvironmentID),
					ActorUserID:     request.Principal.UserID,
					SessionID:       request.Session.ID,
					IdempotencyKey:  string(request.Key),
					IntentHash:      intentHash,
					ExpectedDigest:  request.ExpectedDigest,
					CompressedBytes: request.CompressedBytes,
				},
			)
			if err != nil {
				return err
			}
			admission = TenantSourceUpAdmission{
				ReservationID: reservation.ID,
				IntentHash:    intentHash,
			}
			return nil
		})
		if err == nil {
			return admission, nil
		}
		if !isTenantMutationRetryable(err) ||
			attempt == tenantMutationSerializableAttempts-1 {
			break
		}
	}
	if errors.Is(err, sourceartifact.ErrReservationInProgress) {
		return c.coalesceTenantSourceUpReplay(ctx, request)
	}
	return TenantSourceUpAdmission{}, classifyTenantDeployCoordinatorError(err)
}

// coalesceTenantSourceUpReplay follows an already-admitted exact-intent
// request until its atomic idempotency response becomes terminal. It never
// renews or creates a quota reservation and never publishes source bytes.
// The handler still stages, archive-validates, hashes, and removes the complete
// follower body before it returns this response.
func (c *PostgresTenantDeployIntakeCoordinator) coalesceTenantSourceUpReplay(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (TenantSourceUpAdmission, error) {
	fingerprint, err := tenantSourceUpReplayFingerprint(request)
	if err != nil {
		return TenantSourceUpAdmission{}, err
	}
	coalesceUntil := time.Now().Add(tenantUpReplayCoalesceWindow)
	for {
		var reservation idempotency.Reservation
		for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
			err = c.Pool.WithTxOpts(
				ctx,
				pgx.TxOptions{IsoLevel: pgx.Serializable},
				func(tx *pg.Tx) error {
					mutationCtx, authorization, authorizeErr := authorizeTenantDeployInTx(
						ctx,
						tx,
						request.Principal,
						request.Session,
						string(request.ServiceID),
						strings.TrimSpace(request.EnvironmentID),
						c.now().Truncate(time.Microsecond),
					)
					if authorizeErr != nil {
						return authorizeErr
					}
					if err := validateTenantAgentSourceInTx(mutationCtx, tx, request.Principal, authorization, request.EnvironmentID, request.AgentSource); err != nil {
						return err
					}
					reservation, err = c.Idempotency.LoadInTx(
						mutationCtx,
						tx,
						request.Key,
						fingerprint,
					)
					return err
				},
			)
			if err == nil {
				break
			}
			if !isTenantMutationRetryable(err) ||
				attempt == tenantMutationSerializableAttempts-1 {
				break
			}
		}
		if err == nil && reservation.IsTerminal() {
			if reservation.Response == nil {
				return TenantSourceUpAdmission{}, errors.New(
					"tenant source upload replay has no stored response",
				)
			}
			return TenantSourceUpAdmission{Replay: true}, nil
		}
		if err != nil &&
			!errors.Is(err, idempotency.ErrReservationNotFound) &&
			!errors.Is(err, idempotency.ErrRequestInProgress) {
			return TenantSourceUpAdmission{}, classifyTenantDeployCoordinatorError(err)
		}
		remaining := time.Until(coalesceUntil)
		if remaining <= 0 {
			return TenantSourceUpAdmission{}, sourceartifact.ErrReservationInProgress
		}
		delay := min(tenantUpReplayCoalescePoll, remaining)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return TenantSourceUpAdmission{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// FinalizeSourceUpReplay repeats current authorization and reloads the exact
// terminal fingerprint after the handler has validated and hashed the complete
// follower body. A session or grant revoked while that body was in flight
// therefore cannot inherit the leader's success response.
func (c *PostgresTenantDeployIntakeCoordinator) FinalizeSourceUpReplay(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (idempotency.StoredResponse, error) {
	if err := c.validateCoordinatorAuthority(); err != nil {
		return idempotency.StoredResponse{}, err
	}
	if err := c.validateSourceArtifactAuthority(); err != nil {
		return idempotency.StoredResponse{}, err
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return idempotency.StoredResponse{}, err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" ||
		strings.TrimSpace(request.EnvironmentID) == "" {
		return idempotency.StoredResponse{}, fmt.Errorf(
			"%w: service and environment are required",
			ErrResourceInvalid,
		)
	}
	if err := validateTenantUpMessage(request.CommitMessage); err != nil {
		return idempotency.StoredResponse{}, err
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return idempotency.StoredResponse{}, err
	}
	if !canonicalSHA256Digest(request.ExpectedDigest) ||
		request.CompressedBytes <= 0 ||
		request.CompressedBytes > sourceartifact.DefaultLimits.MaxCompressedBytes {
		return idempotency.StoredResponse{}, fmt.Errorf(
			"%w: advertised source artifact is invalid",
			ErrResourceInvalid,
		)
	}
	fingerprint, err := tenantSourceUpReplayFingerprint(request)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		var reservation idempotency.Reservation
		err = c.Pool.WithTxOpts(
			ctx,
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			func(tx *pg.Tx) error {
				mutationCtx, authorization, authorizeErr := authorizeTenantDeployInTx(
					ctx,
					tx,
					request.Principal,
					request.Session,
					string(request.ServiceID),
					strings.TrimSpace(request.EnvironmentID),
					c.now().Truncate(time.Microsecond),
				)
				if authorizeErr != nil {
					return authorizeErr
				}
				if err := validateTenantAgentSourceInTx(mutationCtx, tx, request.Principal, authorization, request.EnvironmentID, request.AgentSource); err != nil {
					return err
				}
				reservation, err = c.Idempotency.LoadInTx(
					mutationCtx,
					tx,
					request.Key,
					fingerprint,
				)
				if err != nil {
					return err
				}
				if !reservation.IsTerminal() {
					return idempotency.ErrRequestInProgress
				}
				if reservation.Response == nil {
					return errors.New("tenant source upload replay has no stored response")
				}
				return nil
			},
		)
		if err == nil {
			return reservation.Response.Clone(), nil
		}
		if !isTenantMutationRetryable(err) ||
			attempt == tenantMutationSerializableAttempts-1 {
			return idempotency.StoredResponse{}, classifyTenantDeployCoordinatorError(err)
		}
	}
	return idempotency.StoredResponse{}, errors.New(
		"tenant source upload replay serializable retries exhausted",
	)
}

func tenantSourceUpReplayFingerprint(
	request TenantSourceUpPreflightRequest,
) (idempotency.RequestFingerprint, error) {
	fingerprintDigest, err := tenantUpFingerprintDigest(
		request.ServiceID,
		request.EnvironmentID,
		request.CommitMessage,
		TenantSourceArtifact{
			Digest:          request.ExpectedDigest,
			CompressedBytes: request.CompressedBytes,
		},
		request.AgentSource,
	)
	if err != nil {
		return idempotency.RequestFingerprint{}, err
	}
	return idempotency.RequestFingerprint{
		Method:     http.MethodPost,
		Path:       tenantUpIdempotencyPath(request.ServiceID),
		BodySHA256: fingerprintDigest,
	}, nil
}

// PublishSourceUp reauthorizes and publishes the exact staged body while one
// transaction holds quota global -> workspace -> artifact lifecycle locks.
// This method is intentionally not retried after a serialization/commit
// failure because filesystem publication is an external side effect; the
// durable reservation and proof reconciler safely converge that crash window.
func (c *PostgresTenantDeployIntakeCoordinator) PublishSourceUp(
	ctx context.Context,
	request TenantSourceUpPublishRequest,
) (TenantSourceArtifact, error) {
	preflight := request.Preflight
	if err := c.validateCoordinatorAuthority(); err != nil {
		return TenantSourceArtifact{}, err
	}
	if err := c.validateSourceArtifactAuthority(); err != nil {
		return TenantSourceArtifact{}, err
	}
	if err := validateTenantHumanTuple(preflight.Principal, preflight.Session); err != nil {
		return TenantSourceArtifact{}, err
	}
	if strings.TrimSpace(string(preflight.ServiceID)) == "" ||
		strings.TrimSpace(preflight.EnvironmentID) == "" {
		return TenantSourceArtifact{}, fmt.Errorf(
			"%w: service and environment are required",
			ErrResourceInvalid,
		)
	}
	if err := validateTenantUpMessage(preflight.CommitMessage); err != nil {
		return TenantSourceArtifact{}, err
	}
	if err := idempotency.ValidateKey(preflight.Key); err != nil {
		return TenantSourceArtifact{}, err
	}
	if !canonicalSHA256Digest(preflight.ExpectedDigest) ||
		preflight.CompressedBytes <= 0 ||
		preflight.CompressedBytes > sourceartifact.DefaultLimits.MaxCompressedBytes {
		return TenantSourceArtifact{}, fmt.Errorf(
			"%w: advertised source artifact is invalid",
			ErrResourceInvalid,
		)
	}
	intentHash, err := tenantUpPreflightIntentHash(preflight)
	if err != nil {
		return TenantSourceArtifact{}, err
	}
	if request.Admission.ReservationID == "" ||
		request.Admission.IntentHash != intentHash {
		return TenantSourceArtifact{}, sourceartifact.ErrReservationInvalid
	}

	var published sourceartifact.Artifact
	err = c.Pool.WithTxOpts(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.Serializable},
		func(tx *pg.Tx) error {
			mutationCtx, authorization, err := authorizeTenantDeployInTx(
				ctx,
				tx,
				preflight.Principal,
				preflight.Session,
				string(preflight.ServiceID),
				strings.TrimSpace(preflight.EnvironmentID),
				c.now().Truncate(time.Microsecond),
			)
			if err != nil {
				return err
			}
			if err := validateTenantAgentSourceInTx(mutationCtx, tx, preflight.Principal, authorization, preflight.EnvironmentID, preflight.AgentSource); err != nil {
				return err
			}
			published, err = c.SourceArtifactQuota.PublishInTx(
				mutationCtx,
				tx,
				c.SourceArtifactFS,
				request.Staged,
				request.Admission.ReservationID,
				sourceartifact.ReservationRequest{
					WorkspaceID:     authorization.WorkspaceID,
					ProjectID:       authorization.ProjectID,
					ServiceID:       string(authorization.Service.ID),
					EnvironmentID:   strings.TrimSpace(preflight.EnvironmentID),
					ActorUserID:     preflight.Principal.UserID,
					SessionID:       preflight.Session.ID,
					IdempotencyKey:  string(preflight.Key),
					IntentHash:      intentHash,
					ExpectedDigest:  preflight.ExpectedDigest,
					CompressedBytes: preflight.CompressedBytes,
				},
			)
			return err
		},
	)
	if err != nil {
		return TenantSourceArtifact{}, classifyTenantDeployCoordinatorError(err)
	}
	return TenantSourceArtifact{
		Digest:            published.Digest,
		URI:               published.URI,
		StorageKey:        published.StorageKey,
		CompressedBytes:   published.CompressedBytes,
		UncompressedBytes: published.UncompressedBytes,
		EntryCount:        published.EntryCount,
	}, nil
}

// DeploySourceArtifact admits one already-staged content-addressed source
// archive. The API never extracts or materializes it. Worker materialization
// starts only after the atomic outbox handoff becomes visible.
func (c *PostgresTenantDeployIntakeCoordinator) DeploySourceArtifact(
	ctx context.Context,
	request TenantSourceUpRequest,
) (TenantDeployIntakeResult, error) {
	if err := c.validateSourceUpRequest(request); err != nil {
		return TenantDeployIntakeResult{}, err
	}
	artifact := request.Artifact
	return c.deployTenantIntent(ctx, tenantDeployTransactionRequest{
		AgentSource: request.AgentSource,
		Principal:   request.Principal,
		Session:     request.Session,
		ServiceID:   request.ServiceID,
		Intent: DeployIntakeRequest{
			EnvironmentID: strings.TrimSpace(request.EnvironmentID),
			Source: &DeploySourceIntent{
				Provider: "artifact",
				Repo:     artifact.URI,
				Ref:      artifact.Digest,
			},
		},
		Key:           request.Key,
		Fingerprint:   request.Fingerprint,
		RequestID:     request.RequestID,
		SourceIP:      request.SourceIP,
		UserAgent:     request.UserAgent,
		OperationKind: "build",
		Queue:         queue.Build,
		ResponseKind:  "up",
		Artifact:      &artifact,
		Admission:     &request.Admission,
		HasMessage:    strings.TrimSpace(request.CommitMessage) != "",
		Message:       request.CommitMessage,
	})
}

func (c *PostgresTenantDeployIntakeCoordinator) validateSourceUpRequest(
	request TenantSourceUpRequest,
) error {
	if err := c.validateCoordinatorAuthority(); err != nil {
		return err
	}
	if err := c.validateSourceArtifactAuthority(); err != nil {
		return err
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" ||
		strings.TrimSpace(request.EnvironmentID) == "" {
		return fmt.Errorf("%w: service and environment are required", ErrResourceInvalid)
	}
	if err := validateTenantUpMessage(request.CommitMessage); err != nil {
		return err
	}
	artifact := request.Artifact
	expectedURI, uriErr := sourceartifact.URI(artifact.Digest)
	expectedStorageKey, storageKeyErr := sourceartifact.StorageKey(artifact.Digest)
	if !canonicalSHA256Digest(artifact.Digest) ||
		uriErr != nil ||
		storageKeyErr != nil ||
		artifact.URI != expectedURI ||
		artifact.StorageKey != expectedStorageKey ||
		artifact.CompressedBytes <= 0 ||
		artifact.UncompressedBytes < 0 ||
		artifact.EntryCount <= 0 {
		return fmt.Errorf("%w: source artifact metadata is invalid", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	canonicalPath := tenantUpIdempotencyPath(request.ServiceID)
	if request.Fingerprint.Method != http.MethodPost ||
		request.Fingerprint.Path != canonicalPath {
		return fmt.Errorf(
			"%w: tenant up fingerprint must use concrete POST %s (template %s)",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
			tenantUpIntakePathTemplate,
		)
	}
	expectedDigest, err := tenantUpFingerprintDigest(
		request.ServiceID,
		request.EnvironmentID,
		request.CommitMessage,
		artifact,
		request.AgentSource,
	)
	if err != nil {
		return err
	}
	if request.Fingerprint.BodySHA256 != expectedDigest {
		return fmt.Errorf("%w: tenant up fingerprint does not bind accepted artifact metadata", idempotency.ErrInvalidFingerprint)
	}
	expectedIntentHash, err := tenantUpPreflightIntentHash(TenantSourceUpPreflightRequest{
		Principal:       request.Principal,
		Session:         request.Session,
		ServiceID:       request.ServiceID,
		EnvironmentID:   request.EnvironmentID,
		CommitMessage:   request.CommitMessage,
		Key:             request.Key,
		ExpectedDigest:  artifact.Digest,
		CompressedBytes: artifact.CompressedBytes,
		AgentSource:     request.AgentSource,
	})
	if err != nil {
		return err
	}
	if request.Admission.ReservationID == "" ||
		request.Admission.IntentHash != expectedIntentHash {
		return fmt.Errorf("%w: source artifact admission is invalid", ErrResourceInvalid)
	}
	return nil
}

func (c *PostgresTenantDeployIntakeCoordinator) validateCoordinatorAuthority() error {
	if c == nil ||
		c.Pool == nil ||
		c.Idempotency == nil ||
		c.Idempotency.Pool == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool == nil ||
		c.Audit.Pool != c.Pool ||
		c.Outbox == nil ||
		c.Outbox.Pool == nil ||
		c.Outbox.Pool != c.Pool ||
		c.Routes == nil ||
		c.Routes.Pool == nil ||
		c.Routes.Pool != c.Pool ||
		c.OperationIDFactory == nil ||
		c.DeploymentIDFactory == nil ||
		c.DomainIDFactory == nil ||
		c.OutboxIDFactory == nil ||
		c.EventIDFactory == nil {
		return errors.New("tenant deploy intake coordinator does not share one postgres authority")
	}
	return nil
}

func (c *PostgresTenantDeployIntakeCoordinator) validateSourceArtifactAuthority() error {
	if c == nil ||
		c.SourceArtifacts == nil ||
		c.SourceArtifacts.Pool == nil ||
		c.SourceArtifacts.Pool != c.Pool ||
		c.SourceArtifactFS == nil ||
		c.SourceArtifactQuota == nil ||
		c.SourceArtifactQuota.Pool == nil ||
		c.SourceArtifactQuota.Pool != c.Pool {
		return errors.New("tenant source upload does not share the postgres artifact authority")
	}
	return nil
}

func validateTenantHumanTuple(principal identity.Principal, session identity.Session) error {
	if principal.Kind != "user" ||
		principal.UserID == "" ||
		principal.SessionID == "" ||
		session.ID != principal.SessionID ||
		session.UserID != principal.UserID ||
		session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	return nil
}

func validateTenantUpMessage(message string) error {
	if len(message) > tenantUpMessageMaxBytes {
		return fmt.Errorf("%w: commit message must be valid UTF-8 and no more than %d bytes", ErrResourceInvalid, tenantUpMessageMaxBytes)
	}
	if err := sourceartifact.ValidateMessage(message); err != nil {
		return fmt.Errorf("%w: %v", ErrResourceInvalid, err)
	}
	return nil
}

func canonicalSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func tenantUpIdempotencyPath(serviceID orailtypes.ID) string {
	return v1alphaPrefix + "/services/" + string(serviceID) + "/up"
}

func tenantUpFingerprintDigest(
	serviceID orailtypes.ID,
	environmentID string,
	message string,
	artifact TenantSourceArtifact,
	constraints ...*TenantAgentSourceConstraints,
) (string, error) {
	var agentSource *TenantAgentSourceConstraints
	if len(constraints) > 0 {
		agentSource = constraints[0]
	}
	body, err := json.Marshal(struct {
		ServiceID       string                        `json:"service_id"`
		EnvironmentID   string                        `json:"environment_id"`
		CommitMessage   string                        `json:"commit_message"`
		ArchiveDigest   string                        `json:"archive_digest"`
		CompressedBytes int64                         `json:"compressed_bytes"`
		AgentSource     *TenantAgentSourceConstraints `json:"agent_source,omitempty"`
	}{
		ServiceID:       string(serviceID),
		EnvironmentID:   strings.TrimSpace(environmentID),
		CommitMessage:   message,
		ArchiveDigest:   artifact.Digest,
		CompressedBytes: artifact.CompressedBytes,
		AgentSource:     agentSource,
	})
	if err != nil {
		return "", fmt.Errorf("tenant up fingerprint encode: %w", err)
	}
	return idempotency.BodyHashSHA256(body), nil
}

func tenantUpPreflightIntentHash(request TenantSourceUpPreflightRequest) (string, error) {
	body, err := json.Marshal(struct {
		Method          string                        `json:"method"`
		Path            string                        `json:"path"`
		EnvironmentID   string                        `json:"environment_id"`
		CommitMessage   string                        `json:"commit_message"`
		ArchiveDigest   string                        `json:"archive_digest"`
		CompressedBytes int64                         `json:"compressed_bytes"`
		AgentSource     *TenantAgentSourceConstraints `json:"agent_source,omitempty"`
	}{
		Method:          http.MethodPost,
		Path:            tenantUpIdempotencyPath(request.ServiceID),
		EnvironmentID:   strings.TrimSpace(request.EnvironmentID),
		CommitMessage:   request.CommitMessage,
		ArchiveDigest:   request.ExpectedDigest,
		CompressedBytes: request.CompressedBytes,
		AgentSource:     request.AgentSource,
	})
	if err != nil {
		return "", fmt.Errorf("tenant up preflight fingerprint encode: %w", err)
	}
	return idempotency.BodyHashSHA256(body), nil
}

func (c *PostgresTenantDeployIntakeCoordinator) FinishSourceUpAdmission(
	ctx context.Context,
	admission TenantSourceUpAdmission,
	published bool,
) error {
	if c == nil || c.SourceArtifactQuota == nil {
		return errors.New("tenant source upload quota authority is unavailable")
	}
	return c.SourceArtifactQuota.Finish(ctx, admission.ReservationID, published)
}

func (c *PostgresTenantDeployIntakeCoordinator) bindTenantSourceArtifactInTx(
	ctx context.Context,
	tx *pg.Tx,
	artifact TenantSourceArtifact,
	commit DeployIntakeCommitResult,
	projectID string,
	serviceID string,
	environmentID string,
	message string,
	now time.Time,
) error {
	if c == nil ||
		c.SourceArtifacts == nil ||
		c.SourceArtifacts.Pool == nil ||
		c.SourceArtifacts.Pool != c.Pool {
		return errors.New("tenant source artifact binder does not share the postgres authority")
	}
	err := c.SourceArtifacts.BindVerifiedInTx(
		ctx,
		tx,
		c.SourceArtifactFS,
		sourceartifact.Artifact{
			Digest:            artifact.Digest,
			CompressedBytes:   artifact.CompressedBytes,
			UncompressedBytes: artifact.UncompressedBytes,
			EntryCount:        artifact.EntryCount,
			StorageKey:        artifact.StorageKey,
			URI:               artifact.URI,
		},
		sourceartifact.Reference{
			OperationID:   string(commit.Operation.ID),
			DeploymentID:  commit.Deployment.ID,
			Digest:        artifact.Digest,
			ProjectID:     projectID,
			ServiceID:     serviceID,
			EnvironmentID: strings.TrimSpace(environmentID),
			Message:       message,
			CreatedAt:     now,
		},
	)
	if err != nil {
		return fmt.Errorf("tenant up artifact ownership bind: %w", err)
	}
	return nil
}

func tenantUpAcceptedStoredResponse(
	operation orailtypes.Operation,
	deploymentID string,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(upOperationResponse{
		OperationID:  string(operation.ID),
		DeploymentID: deploymentID,
		Operation:    operation,
	})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant up response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+"/operations/"+string(operation.ID))
	headers.Set("X-Midnight-Delivery-State", "pending")
	return idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantUpAcceptedAuditRow(
	request tenantDeployTransactionRequest,
	now time.Time,
	workspaceID string,
	projectID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	service Service,
	intent DeployIntakeRequest,
	commit DeployIntakeCommitResult,
) audit.Row {
	artifact := TenantSourceArtifact{}
	if request.Artifact != nil {
		artifact = *request.Artifact
	}
	return audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    workspaceID,
		ProjectID:      projectID,
		EnvironmentID:  intent.EnvironmentID,
		ResourceKind:   "operation",
		ResourceID:     string(commit.Operation.ID),
		Action:         "service.up.accept",
		PolicyDecision: "allowed",
		After: map[string]any{
			"service_id":                string(service.ID),
			"deployment_id":             commit.Deployment.ID,
			"operation_id":              string(commit.Operation.ID),
			"outbox_id":                 commit.OutboxID,
			"queue":                     string(queue.Build),
			"delivery_state":            "pending",
			"expected_active_id":        commit.Deployment.ExpectedActiveID,
			"source_artifact_digest":    artifact.Digest,
			"source_compressed_bytes":   artifact.CompressedBytes,
			"source_uncompressed_bytes": artifact.UncompressedBytes,
			"source_entry_count":        artifact.EntryCount,
			"has_client_commit_message": request.HasMessage,
		},
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityDeploymentTriggerCLI),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
}
