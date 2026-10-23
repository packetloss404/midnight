// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/secrets"
)

const postgresSerializableAttempts = 3

const (
	responseStoragePlaintext = "plaintext-v1"
	responseStorageEnvelope  = "envelope-v1"
)

// PostgresStore persists immutable idempotency attempts. A client key is
// scoped by the authenticated actor, canonical tenant, method, and route
// template. Every new attempt receives a generation fence; expired attempts
// remain in history and can never be mutated by a late request.
type PostgresStore struct {
	Pool         *pg.Pool
	ResponseKeys secrets.KeyProvider
	Now          func() time.Time
}

// NewPostgresStore wires a PostgresStore using the supplied pool and UTC
// system clock.
func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

// NewPostgresStoreWithSensitiveResponseKeys wires the durable store with the
// KEK provider required to seal and replay sensitive terminal responses.
// Ordinary idempotency remains usable through NewPostgresStore.
func NewPostgresStoreWithSensitiveResponseKeys(
	pool *pg.Pool,
	keys secrets.KeyProvider,
) *PostgresStore {
	store := NewPostgresStore(pool)
	store.ResponseKeys = keys
	return store
}

type postgresScope struct {
	Actor       Actor
	Tenant      Tenant
	Key         Key
	Fingerprint RequestFingerprint
}

// Get is a thin alias for Load.
func (s *PostgresStore) Get(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error) {
	return s.Load(ctx, key, fingerprint)
}

// Put is a thin alias for Reserve.
func (s *PostgresStore) Put(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error) {
	return s.Reserve(ctx, key, fingerprint, ttl)
}

// Reserve creates a new immutable attempt or returns the active attempt for the
// exact actor+tenant+method+path+key scope. It uses a serializable transaction
// and a transaction-scoped advisory lock so replicas agree on one active
// generation. Expired attempts transition to EXPIRED and remain queryable as
// history before a new generation is inserted.
func (s *PostgresStore) Reserve(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error) {
	scope, err := postgresScopeFromContext(ctx, key, fingerprint)
	if err != nil {
		return Reservation{}, err
	}
	generation, err := newGeneration()
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency generation: %w", err)
	}
	now := s.now()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.reserveInTx(ctx, tx, scope, generation, now, now.Add(ttl))
	})
}

// ReserveInTx is the transaction-aware coordinator seam. The caller must
// supply an already-open transaction and is responsible for committing or
// rolling it back. This method never begins or commits a transaction.
//
// The transaction should use SERIALIZABLE isolation when authorization,
// resource mutation, idempotency, and outbox state must linearize together.
func (s *PostgresStore) ReserveInTx(ctx context.Context, tx *pg.Tx, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency ReserveInTx: transaction is required")
	}
	scope, err := postgresScopeFromContext(ctx, key, fingerprint)
	if err != nil {
		return Reservation{}, err
	}
	generation, err := newGeneration()
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency generation: %w", err)
	}
	now := s.now()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return s.reserveInTx(ctx, tx, scope, generation, now, now.Add(ttl))
}

func (s *PostgresStore) reserveInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope postgresScope,
	generation string,
	now time.Time,
	expiresAt time.Time,
) (Reservation, error) {
	if err := lockPostgresScope(ctx, tx, scope); err != nil {
		return Reservation{}, err
	}
	if err := expirePostgresScope(ctx, tx, scope, now); err != nil {
		return Reservation{}, err
	}

	existing, err := s.scanActiveReservation(ctx, tx, scope)
	if err == nil {
		return matchExistingReservation(existing, scope.Fingerprint)
	}
	if !errors.Is(err, ErrReservationNotFound) {
		return Reservation{}, err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO idempotency_keys (
		    actor, tenant, method, path_template, key, generation,
		    body_hash, state, scope, request_hash,
		    expires_at, created_at, updated_at
		 ) VALUES (
		    $1,$2,$3,$4,$5,$6,$7,'RESERVED',$8,$7,$9,$10,$10
		 )`,
		string(scope.Actor), string(scope.Tenant),
		scope.Fingerprint.Method, scope.Fingerprint.Path, string(scope.Key),
		generation, scope.Fingerprint.BodySHA256,
		postgresScopeLabel(scope), expiresAt, now,
	)
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency reserve insert: %w", err)
	}
	return s.scanReservationGeneration(ctx, tx, Reference{
		Key:        scope.Key,
		Actor:      scope.Actor,
		Tenant:     scope.Tenant,
		Method:     scope.Fingerprint.Method,
		Path:       scope.Fingerprint.Path,
		BodySHA256: scope.Fingerprint.BodySHA256,
		Generation: generation,
	})
}

// Load returns the active generation for the exact actor+tenant+method+path+key
// scope and verifies that the body fingerprint is still bound to it.
func (s *PostgresStore) Load(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error) {
	scope, err := postgresScopeFromContext(ctx, key, fingerprint)
	if err != nil {
		return Reservation{}, err
	}
	now := s.now()
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.loadInTx(ctx, tx, scope, now)
	})
}

// LoadInTx is the transaction-aware form of Load. It never begins or commits a
// transaction.
func (s *PostgresStore) LoadInTx(ctx context.Context, tx *pg.Tx, key Key, fingerprint RequestFingerprint) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency LoadInTx: transaction is required")
	}
	scope, err := postgresScopeFromContext(ctx, key, fingerprint)
	if err != nil {
		return Reservation{}, err
	}
	return s.loadInTx(ctx, tx, scope, s.now())
}

func (s *PostgresStore) loadInTx(ctx context.Context, tx *pg.Tx, scope postgresScope, now time.Time) (Reservation, error) {
	if err := lockPostgresScope(ctx, tx, scope); err != nil {
		return Reservation{}, err
	}
	if err := expirePostgresScope(ctx, tx, scope, now); err != nil {
		return Reservation{}, err
	}
	reservation, err := s.scanActiveReservation(ctx, tx, scope)
	if err != nil {
		return Reservation{}, err
	}
	return matchLoadedReservation(reservation, scope.Fingerprint)
}

// AttachOperation transitions exactly one fenced RESERVED generation to
// RUNNING. A repeated attach of the same operation is idempotent; a different
// operation, expired generation, or terminal attempt is fenced.
func (s *PostgresStore) AttachOperation(ctx context.Context, ref Reference, operationID string) (Reservation, error) {
	if operationID == "" {
		return Reservation{}, ErrReservationFenced
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.attachOperationInTx(ctx, tx, ref, operationID, s.now())
	})
}

// AttachOperationInTx is the transaction-aware form of AttachOperation. It
// never begins or commits a transaction.
func (s *PostgresStore) AttachOperationInTx(ctx context.Context, tx *pg.Tx, ref Reference, operationID string) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency AttachOperationInTx: transaction is required")
	}
	if operationID == "" {
		return Reservation{}, ErrReservationFenced
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.attachOperationInTx(ctx, tx, ref, operationID, s.now())
}

func (s *PostgresStore) attachOperationInTx(ctx context.Context, tx *pg.Tx, ref Reference, operationID string, now time.Time) (Reservation, error) {
	scope := postgresScopeFromReference(ref)
	if err := lockPostgresScope(ctx, tx, scope); err != nil {
		return Reservation{}, err
	}
	if err := expirePostgresScope(ctx, tx, scope, now); err != nil {
		return Reservation{}, err
	}
	current, err := s.scanReservationGeneration(ctx, tx, ref)
	if err != nil {
		return Reservation{}, ErrReservationFenced
	}
	switch current.State {
	case StateRunning:
		if current.OperationID == operationID {
			return current, nil
		}
		return current, ErrKeyReused
	case StateReserved:
		// Continue into the fenced compare-and-set below.
	case StateSucceeded, StateFailed:
		// Terminal attempts are immutable. Treat a duplicate attach as an
		// idempotent read, never as permission to replace operation_id.
		return current, nil
	default:
		return current, ErrReservationFenced
	}

	tag, err := tx.Exec(ctx,
		`UPDATE idempotency_keys
		    SET state='RUNNING', operation_id=$1, updated_at=$2
		  WHERE actor=$3 AND tenant=$4 AND method=$5 AND path_template=$6
		    AND key=$7 AND generation=$8 AND body_hash=$9
		    AND state='RESERVED' AND expires_at > $2`,
		operationID, now,
		string(ref.Actor), string(ref.Tenant), ref.Method, ref.Path,
		string(ref.Key), ref.Generation, ref.BodySHA256,
	)
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency attach operation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return current, ErrReservationFenced
	}
	return s.scanReservationGeneration(ctx, tx, ref)
}

// CompleteSuccess stores one immutable terminal 2xx response.
func (s *PostgresStore) CompleteSuccess(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error) {
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.completeInTx(ctx, tx, ref, StateSucceeded, response, false, s.now())
	})
}

// CompleteSuccessInTx is the transaction-aware form of CompleteSuccess. It
// never begins or commits a transaction.
func (s *PostgresStore) CompleteSuccessInTx(ctx context.Context, tx *pg.Tx, ref Reference, response StoredResponse) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency CompleteSuccessInTx: transaction is required")
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.completeInTx(ctx, tx, ref, StateSucceeded, response, false, s.now())
}

// CompleteFailure stores one immutable terminal 4xx/5xx response.
func (s *PostgresStore) CompleteFailure(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error) {
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.completeInTx(ctx, tx, ref, StateFailed, response, false, s.now())
	})
}

// CompleteFailureInTx is the transaction-aware form of CompleteFailure. It
// never begins or commits a transaction.
func (s *PostgresStore) CompleteFailureInTx(ctx context.Context, tx *pg.Tx, ref Reference, response StoredResponse) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency CompleteFailureInTx: transaction is required")
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.completeInTx(ctx, tx, ref, StateFailed, response, false, s.now())
}

// CompleteSensitiveSuccess stores one immutable encrypted 2xx response.
func (s *PostgresStore) CompleteSensitiveSuccess(
	ctx context.Context,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.completeInTx(ctx, tx, ref, StateSucceeded, response, true, s.now())
	})
}

// CompleteSensitiveSuccessInTx is the transaction-aware form of
// CompleteSensitiveSuccess. It never begins or commits a transaction.
func (s *PostgresStore) CompleteSensitiveSuccessInTx(
	ctx context.Context,
	tx *pg.Tx,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency CompleteSensitiveSuccessInTx: transaction is required")
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.completeInTx(ctx, tx, ref, StateSucceeded, response, true, s.now())
}

// CompleteSensitiveFailure stores one immutable encrypted 4xx/5xx response.
func (s *PostgresStore) CompleteSensitiveFailure(
	ctx context.Context,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.runSerializable(ctx, func(tx *pg.Tx) (Reservation, error) {
		return s.completeInTx(ctx, tx, ref, StateFailed, response, true, s.now())
	})
}

// CompleteSensitiveFailureInTx is the transaction-aware form of
// CompleteSensitiveFailure. It never begins or commits a transaction.
func (s *PostgresStore) CompleteSensitiveFailureInTx(
	ctx context.Context,
	tx *pg.Tx,
	ref Reference,
	response StoredResponse,
) (Reservation, error) {
	if tx == nil {
		return Reservation{}, errors.New("idempotency CompleteSensitiveFailureInTx: transaction is required")
	}
	if err := ValidateReference(ctx, ref); err != nil {
		return Reservation{}, err
	}
	return s.completeInTx(ctx, tx, ref, StateFailed, response, true, s.now())
}

func (s *PostgresStore) completeInTx(
	ctx context.Context,
	tx *pg.Tx,
	ref Reference,
	state State,
	response StoredResponse,
	sensitive bool,
	now time.Time,
) (Reservation, error) {
	if err := validateTerminalResponse(state, response); err != nil {
		return Reservation{}, err
	}

	scope := postgresScopeFromReference(ref)
	if err := lockPostgresScope(ctx, tx, scope); err != nil {
		return Reservation{}, err
	}
	if err := expirePostgresScope(ctx, tx, scope, now); err != nil {
		return Reservation{}, err
	}
	current, err := s.scanReservationGeneration(ctx, tx, ref)
	if err != nil {
		return Reservation{}, ErrReservationFenced
	}
	if current.IsTerminal() {
		// Terminal attempts are immutable. A duplicate completion observes the
		// first committed response without issuing an UPDATE.
		return current, nil
	}
	if current.State == StateExpired {
		return current, ErrReservationFenced
	}
	if current.State != StateReserved && current.State != StateRunning {
		return current, ErrReservationFenced
	}

	var tag pgconn.CommandTag
	if sensitive {
		sealed, err := sealSensitiveResponse(ctx, s.ResponseKeys, ref, state, response)
		if err != nil {
			return Reservation{}, err
		}
		tag, err = tx.Exec(ctx,
			`UPDATE idempotency_keys
			    SET state=$1,
			        response_status=NULL, response_body=NULL,
			        response_headers=NULL, response_storage=$2,
			        response_kek_id=$3, response_wrapped_dek=$4,
			        response_ciphertext=$5, response_envelope_form=$6,
			        response_aad_sha256=$7, updated_at=$8
			  WHERE actor=$9 AND tenant=$10 AND method=$11 AND path_template=$12
			    AND key=$13 AND generation=$14 AND body_hash=$15
			    AND state=$16 AND expires_at > $8`,
			string(state), responseStorageEnvelope,
			string(sealed.Envelope.KEKID), sealed.Envelope.WrappedDEK,
			sealed.Envelope.Ciphertext, sealed.Envelope.CanonicalForm,
			sealed.AADSHA256, now,
			string(ref.Actor), string(ref.Tenant), ref.Method, ref.Path,
			string(ref.Key), ref.Generation, ref.BodySHA256, string(current.State),
		)
	} else {
		headers, marshalErr := json.Marshal(response.Header)
		if marshalErr != nil {
			return Reservation{}, fmt.Errorf("idempotency response headers: %w", marshalErr)
		}
		tag, err = tx.Exec(ctx,
			`UPDATE idempotency_keys
			    SET state=$1, response_status=$2, response_body=$3,
			        response_headers=$4::jsonb, response_storage=$5,
			        response_kek_id=NULL, response_wrapped_dek=NULL,
			        response_ciphertext=NULL, response_envelope_form=NULL,
			        response_aad_sha256=NULL, updated_at=$6
			  WHERE actor=$7 AND tenant=$8 AND method=$9 AND path_template=$10
			    AND key=$11 AND generation=$12 AND body_hash=$13
			    AND state=$14 AND expires_at > $6`,
			string(state), response.StatusCode, response.Body, string(headers),
			responseStoragePlaintext, now,
			string(ref.Actor), string(ref.Tenant), ref.Method, ref.Path,
			string(ref.Key), ref.Generation, ref.BodySHA256, string(current.State),
		)
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency complete: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return current, ErrReservationFenced
	}
	return s.scanReservationGeneration(ctx, tx, ref)
}

// runSerializable provides the standalone Store methods with a retryable
// serializable transaction. Semantic state-machine errors are returned only
// after the transaction commits, preserving any EXPIRED history transition.
func (s *PostgresStore) runSerializable(
	ctx context.Context,
	fn func(*pg.Tx) (Reservation, error),
) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if s == nil || s.Pool == nil {
		return Reservation{}, errors.New("idempotency PostgresStore: pool is required")
	}

	var (
		result      Reservation
		semanticErr error
	)
	for attempt := 0; attempt < postgresSerializableAttempts; attempt++ {
		semanticErr = nil
		err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = fn(tx)
			if isIdempotencySemanticError(err) {
				semanticErr = err
				return nil
			}
			return err
		})
		if err == nil {
			return result, semanticErr
		}
		if !isRetryablePostgresTransactionError(err) || attempt == postgresSerializableAttempts-1 {
			return Reservation{}, err
		}
	}
	return Reservation{}, errors.New("idempotency serializable transaction retries exhausted")
}

func postgresScopeFromContext(ctx context.Context, key Key, fingerprint RequestFingerprint) (postgresScope, error) {
	if err := ValidateKey(key); err != nil {
		return postgresScope{}, err
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		return postgresScope{}, err
	}
	return postgresScope{
		Actor:       ActorFromContext(ctx),
		Tenant:      TenantFromContext(ctx),
		Key:         key,
		Fingerprint: fingerprint,
	}, nil
}

func postgresScopeFromReference(ref Reference) postgresScope {
	return postgresScope{
		Actor: ref.Actor, Tenant: ref.Tenant, Key: ref.Key,
		Fingerprint: RequestFingerprint{
			Method: ref.Method, Path: ref.Path, BodySHA256: ref.BodySHA256,
		},
	}
}

func postgresScopeLabel(scope postgresScope) string {
	return fmt.Sprintf(
		"%d:%s|%d:%s|%d:%s|%d:%s|%d:%s",
		len(scope.Actor), scope.Actor,
		len(scope.Tenant), scope.Tenant,
		len(scope.Fingerprint.Method), scope.Fingerprint.Method,
		len(scope.Fingerprint.Path), scope.Fingerprint.Path,
		len(scope.Key), scope.Key,
	)
}

func lockPostgresScope(ctx context.Context, tx *pg.Tx, scope postgresScope) error {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		postgresScopeLabel(scope),
	); err != nil {
		return fmt.Errorf("idempotency scope lock: %w", err)
	}
	return nil
}

func expirePostgresScope(ctx context.Context, tx *pg.Tx, scope postgresScope, now time.Time) error {
	if _, err := tx.Exec(ctx,
		`UPDATE idempotency_keys
		    SET state='EXPIRED',
		        response_status=CASE
		            WHEN response_storage='envelope-v1' THEN NULL
		            ELSE response_status
		        END,
		        response_body=CASE
		            WHEN response_storage='envelope-v1' THEN NULL
		            ELSE response_body
		        END,
		        response_headers=CASE
		            WHEN response_storage='envelope-v1' THEN NULL
		            ELSE response_headers
		        END,
		        response_storage=CASE
		            WHEN response_storage='envelope-v1' THEN 'plaintext-v1'
		            ELSE response_storage
		        END,
		        response_kek_id=NULL,
		        response_wrapped_dek=NULL,
		        response_ciphertext=NULL,
		        response_envelope_form=NULL,
		        response_aad_sha256=NULL,
		        updated_at=$1
		  WHERE actor=$2 AND tenant=$3 AND method=$4 AND path_template=$5
		    AND key=$6 AND state IN ('RESERVED','RUNNING','SUCCEEDED','FAILED')
		    AND expires_at <= $1`,
		now, string(scope.Actor), string(scope.Tenant),
		scope.Fingerprint.Method, scope.Fingerprint.Path, string(scope.Key),
	); err != nil {
		return fmt.Errorf("idempotency expire scope: %w", err)
	}
	return nil
}

func matchExistingReservation(reservation Reservation, fingerprint RequestFingerprint) (Reservation, error) {
	if reservation.Fingerprint.Equal(fingerprint) {
		switch reservation.State {
		case StateReserved, StateRunning:
			return reservation, ErrRequestInProgress
		case StateSucceeded, StateFailed:
			return reservation, nil
		default:
			return reservation, ErrReservationNotFound
		}
	}
	switch reservation.State {
	case StateReserved, StateRunning:
		return reservation, ErrRequestInProgress
	case StateSucceeded, StateFailed:
		return reservation, ErrKeyReusedDifferentBody
	default:
		return reservation, ErrReservationNotFound
	}
}

func matchLoadedReservation(reservation Reservation, fingerprint RequestFingerprint) (Reservation, error) {
	if reservation.Fingerprint.Equal(fingerprint) {
		return reservation, nil
	}
	switch reservation.State {
	case StateReserved, StateRunning, StateSucceeded, StateFailed:
		return reservation, ErrKeyReusedDifferentBody
	default:
		return reservation, ErrReservationNotFound
	}
}

func (s *PostgresStore) scanActiveReservation(ctx context.Context, conn pg.Conn, scope postgresScope) (Reservation, error) {
	return s.scanPostgresReservation(ctx, conn.QueryRow(ctx,
		`SELECT key, actor, tenant, method, path_template, body_hash, generation,
		        state, COALESCE(operation_id,''), response_status, response_body,
		        response_headers, response_storage, response_kek_id,
		        response_wrapped_dek, response_ciphertext,
		        response_envelope_form, response_aad_sha256,
		        expires_at, created_at
		   FROM idempotency_keys
		  WHERE actor=$1 AND tenant=$2 AND method=$3 AND path_template=$4
		    AND key=$5 AND state IN ('RESERVED','RUNNING','SUCCEEDED','FAILED')
		  ORDER BY created_at DESC, generation DESC
		  LIMIT 1
		  FOR UPDATE`,
		string(scope.Actor), string(scope.Tenant),
		scope.Fingerprint.Method, scope.Fingerprint.Path, string(scope.Key),
	))
}

func (s *PostgresStore) scanReservationGeneration(ctx context.Context, conn pg.Conn, ref Reference) (Reservation, error) {
	return s.scanPostgresReservation(ctx, conn.QueryRow(ctx,
		`SELECT key, actor, tenant, method, path_template, body_hash, generation,
		        state, COALESCE(operation_id,''), response_status, response_body,
		        response_headers, response_storage, response_kek_id,
		        response_wrapped_dek, response_ciphertext,
		        response_envelope_form, response_aad_sha256,
		        expires_at, created_at
		   FROM idempotency_keys
		  WHERE actor=$1 AND tenant=$2 AND method=$3 AND path_template=$4
		    AND key=$5 AND generation=$6 AND body_hash=$7
		  FOR UPDATE`,
		string(ref.Actor), string(ref.Tenant), ref.Method, ref.Path,
		string(ref.Key), ref.Generation, ref.BodySHA256,
	))
}

func (s *PostgresStore) scanPostgresReservation(ctx context.Context, row pgx.Row) (Reservation, error) {
	var (
		key         string
		actor       string
		tenant      string
		method      string
		path        string
		bodyHash    string
		generation  string
		state       string
		operationID string
		respStatus  pgxNullableInt
		respBody    []byte
		respHeaders []byte
		respStorage string
		respKEKID   pgxNullableString
		respWrapped []byte
		respCipher  []byte
		respForm    pgxNullableString
		respAADHash pgxNullableString
		expiresAt   time.Time
		createdAt   time.Time
	)
	err := row.Scan(
		&key, &actor, &tenant, &method, &path, &bodyHash, &generation,
		&state, &operationID, &respStatus, &respBody, &respHeaders,
		&respStorage, &respKEKID, &respWrapped, &respCipher, &respForm,
		&respAADHash,
		&expiresAt, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ErrReservationNotFound
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("idempotency scan reservation: %w", err)
	}

	reservation := Reservation{
		Key: Key(key), Actor: Actor(actor), Tenant: Tenant(tenant),
		Generation: generation, State: State(state),
		Fingerprint: RequestFingerprint{
			Method: method, Path: path, BodySHA256: bodyHash,
		},
		OperationID: operationID,
		CreatedAt:   createdAt.UTC(),
		ExpiresAt:   expiresAt.UTC(),
	}
	switch respStorage {
	case responseStoragePlaintext:
		if respKEKID.Valid || len(respWrapped) > 0 || len(respCipher) > 0 ||
			respForm.Valid || respAADHash.Valid {
			return Reservation{}, ErrSensitiveResponseUnavailable
		}
		if !respStatus.Valid {
			return reservation, nil
		}
		response := StoredResponse{
			StatusCode: int(respStatus.Int),
			Body:       append([]byte(nil), respBody...),
		}
		if len(respHeaders) > 0 && string(respHeaders) != "null" {
			if err := json.Unmarshal(respHeaders, &response.Header); err != nil {
				return Reservation{}, fmt.Errorf("idempotency decode response headers: %w", err)
			}
		}
		reservation.Response = &response
	case responseStorageEnvelope:
		if respStatus.Valid || respBody != nil || respHeaders != nil ||
			!respKEKID.Valid || respKEKID.String == "" ||
			len(respWrapped) == 0 || len(respCipher) == 0 ||
			!respForm.Valid || respForm.String == "" ||
			!respAADHash.Valid || respAADHash.String == "" {
			return Reservation{}, ErrSensitiveResponseUnavailable
		}
		response, err := openSensitiveResponse(ctx, s.ResponseKeys, reservation.Reference(), reservation.State, sealedResponse{
			Envelope: secrets.Envelope{
				KEKID:         secrets.KEKID(respKEKID.String),
				WrappedDEK:    append([]byte(nil), respWrapped...),
				Ciphertext:    append([]byte(nil), respCipher...),
				CanonicalForm: respForm.String,
			},
			AADSHA256: respAADHash.String,
		})
		if err != nil {
			return Reservation{}, ErrSensitiveResponseUnavailable
		}
		reservation.Response = &response
	default:
		return Reservation{}, ErrSensitiveResponseUnavailable
	}
	return reservation, nil
}

func isIdempotencySemanticError(err error) bool {
	return errors.Is(err, ErrKeyReused) ||
		errors.Is(err, ErrKeyReusedDifferentBody) ||
		errors.Is(err, ErrRequestInProgress) ||
		errors.Is(err, ErrReservationNotFound) ||
		errors.Is(err, ErrReservationFenced)
}

func isRetryablePostgresTransactionError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

func (s *PostgresStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// pgxNullableInt scans nullable INTEGER columns without coupling the store to
// database/sql.
type pgxNullableInt struct {
	Int   int64
	Valid bool
}

// pgxNullableString scans nullable TEXT columns without coupling the store to
// database/sql.
type pgxNullableString struct {
	String string
	Valid  bool
}

// Scan implements sql.Scanner.
func (n *pgxNullableString) Scan(src any) error {
	if src == nil {
		n.String = ""
		n.Valid = false
		return nil
	}
	switch value := src.(type) {
	case string:
		n.String = value
	case []byte:
		n.String = string(value)
	default:
		return fmt.Errorf("unsupported nullable string source %T", src)
	}
	n.Valid = true
	return nil
}

// Scan implements sql.Scanner.
func (n *pgxNullableInt) Scan(src any) error {
	if src == nil {
		n.Int = 0
		n.Valid = false
		return nil
	}
	switch v := src.(type) {
	case int64:
		n.Int = v
	case int32:
		n.Int = int64(v)
	case int:
		n.Int = int64(v)
	default:
		return fmt.Errorf("unsupported nullable int source %T", src)
	}
	n.Valid = true
	return nil
}
