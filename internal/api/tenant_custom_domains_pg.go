// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/domain"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// CustomDNSResolver is intentionally a tiny seam: production uses the
// configured Go resolver and tests can inject a deterministic authoritative
// response. Only one exact TXT owner is queried; routing DNS and public
// reachability remain separate evidence lanes.
type CustomDNSResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}

func (c *PostgresTenantMutationCoordinator) ListCustomDomains(ctx context.Context, request TenantGeneratedDomainReadRequest) ([]TenantCustomDomainView, error) {
	if err := c.validateTenantCustomDomainAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, false); err != nil {
		return nil, err
	}
	var out []TenantCustomDomainView
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		mutationCtx, auth, err := authorizeTenantDeployCapabilityInTx(ctx, tx, request.Principal, request.Session, request.ServiceID, request.EnvironmentID, c.now().Truncate(time.Microsecond), identity.CapabilityDomainsRead)
		if err != nil {
			return err
		}
		out, err = listTenantCustomDomainViewsInTx(mutationCtx, tx, auth, request.EnvironmentID, c.AppsDomain, "", false)
		return err
	})
	if err != nil {
		return nil, normalizeTenantCustomDomainError(err)
	}
	if out == nil {
		out = []TenantCustomDomainView{}
	}
	return out, nil
}

func (c *PostgresTenantMutationCoordinator) GetCustomDomain(ctx context.Context, request TenantGeneratedDomainReadRequest) (TenantCustomDomainView, error) {
	if strings.TrimSpace(request.DomainID) == "" {
		return TenantCustomDomainView{}, ErrTenantCustomDomainNotFound
	}
	if err := c.validateTenantCustomDomainAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, false); err != nil {
		return TenantCustomDomainView{}, err
	}
	var out TenantCustomDomainView
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		mutationCtx, auth, err := authorizeTenantDeployCapabilityInTx(ctx, tx, request.Principal, request.Session, request.ServiceID, request.EnvironmentID, c.now().Truncate(time.Microsecond), identity.CapabilityDomainsRead)
		if err != nil {
			return err
		}
		values, err := listTenantCustomDomainViewsInTx(mutationCtx, tx, auth, request.EnvironmentID, c.AppsDomain, request.DomainID, false)
		if err != nil {
			return err
		}
		if len(values) != 1 {
			return ErrTenantCustomDomainNotFound
		}
		out = values[0]
		return nil
	})
	if err != nil {
		return TenantCustomDomainView{}, normalizeTenantCustomDomainError(err)
	}
	return out, nil
}

func (c *PostgresTenantMutationCoordinator) CreateCustomDomain(ctx context.Context, request TenantCustomDomainCreateRequest) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	host, err := domain.NormalizeHostname(request.Hostname)
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("%w: %v", ErrTenantCustomDomainValidation, err)
	}
	if err := c.validateTenantCustomDomainAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, true); err != nil {
		return TenantMutationResult{}, err
	}
	if c.AppsDomain != "" {
		apps, _ := domain.NormalizeHostname(c.AppsDomain)
		if host == apps || strings.HasSuffix(host, "."+apps) {
			return TenantMutationResult{}, fmt.Errorf("%w: hostname cannot be below the configured apps domain", ErrTenantCustomDomainValidation)
		}
	}
	proposedID := string(c.IDFactory(orailtypes.IDPrefixDomain))
	now := c.now().Truncate(time.Microsecond)
	var out TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var inner error
			out, inner = c.createTenantCustomDomainInTx(ctx, tx, request, host, proposedID, now)
			return inner
		})
		if err == nil {
			return cloneTenantMutationResult(out), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantCustomDomainError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant custom domain create serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) createTenantCustomDomainInTx(ctx context.Context, tx *pg.Tx, request TenantCustomDomainCreateRequest, host, proposedID string, now time.Time) (TenantMutationResult, error) {
	mutationCtx, auth, reservation, err := c.authorizeAndReserveTenantCustomDomain(ctx, tx, request.TenantGeneratedDomainMutationMeta, http.MethodPost, now)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantGeneratedDomainReplay(reservation); ok || err != nil {
		return replay, err
	}
	if exists, err := customHostnameExistsInTx(mutationCtx, tx, host); err != nil {
		return TenantMutationResult{}, err
	} else if exists {
		return TenantMutationResult{}, ErrTenantCustomDomainConflict
	}
	activeID, targetPort, err := c.resolveTenantGeneratedDomainTargetInTx(mutationCtx, tx, auth, request.EnvironmentID, now)
	if err != nil {
		return TenantMutationResult{}, err
	}
	_ = activeID
	scope := router.RouteScope{ProjectID: auth.ProjectID, EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID}
	d, r, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{Scope: scope, Kind: router.DomainKindCustom, Hostname: host, PathPrefix: "/", TargetPort: targetPort, Public: false, TLSDesired: request.TLS})
	if err != nil {
		return TenantMutationResult{}, err
	}
	d.ID = proposedID
	r.DomainID = proposedID
	d, err = c.Routes.PutDomainInTx(mutationCtx, tx, d)
	if err != nil {
		return TenantMutationResult{}, err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return TenantMutationResult{}, fmt.Errorf("mint custom ownership token: %w", err)
	}
	challenge := "midnight-verification=" + hex.EncodeToString(token)
	recordName := "_midnight-challenge." + d.NormalizedHostname
	if len(recordName) > 253 {
		return TenantMutationResult{}, fmt.Errorf("%w: hostname leaves no room for ownership record", ErrTenantCustomDomainValidation)
	}
	if _, err := tx.Exec(mutationCtx, `INSERT INTO domain_dns_challenges(domain_id,project_id,environment_id,service_id,record_name,record_value,state,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'pending',$7,$7)`, d.ID, d.ProjectID, d.EnvironmentID, d.ServiceID, recordName, challenge, now); err != nil {
		return TenantMutationResult{}, fmt.Errorf("custom ownership challenge: %w", err)
	}
	r, err = c.Routes.PutHTTPRouteInTx(mutationCtx, tx, r)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.Routes.BindServiceDomainInTx(mutationCtx, tx, d.ID, r.ID, scope, now); err != nil {
		return TenantMutationResult{}, err
	}
	values, err := listTenantCustomDomainViewsInTx(mutationCtx, tx, auth, request.EnvironmentID, c.AppsDomain, d.ID, false)
	if err != nil || len(values) != 1 {
		if err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{}, errors.New("custom domain was not observable after create")
	}
	created := values[0]
	response, err := tenantCustomDomainJSONResponse(http.StatusCreated, created, tenantCustomDomainItemPath(request.ServiceID, request.EnvironmentID, created.ID))
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.appendTenantCustomAudit(mutationCtx, tx, request.TenantGeneratedDomainMutationMeta, auth, nil, &created, now, "domain.custom.create", "", "allowed"); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) VerifyCustomDomain(ctx context.Context, request TenantCustomDomainVerifyRequest) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if request.ExpectedVersion < 1 {
		return TenantMutationResult{}, ErrTenantCustomDomainVersionConflict
	}
	if err := c.validateTenantCustomDomainAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, true); err != nil {
		return TenantMutationResult{}, err
	}
	// Replays are checked before any DNS I/O. A completed idempotency key
	// returns its exact stored bytes; an in-flight key never starts a second
	// resolver query.
	fingerprint := idempotency.RequestFingerprint{
		Method:     http.MethodPost,
		Path:       tenantCustomDomainItemPath(request.ServiceID, request.EnvironmentID, request.DomainID),
		BodySHA256: idempotency.BodyHashSHA256(request.RawBody),
	}
	var replay TenantMutationResult
	loadErr := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		mutationCtx, _, err := authorizeTenantDeployCapabilityInTx(ctx, tx, request.Principal, request.Session, request.ServiceID, request.EnvironmentID, c.now().Truncate(time.Microsecond), identity.CapabilityDomainsWrite)
		if err != nil {
			return err
		}
		reservation, err := c.Idempotency.LoadInTx(mutationCtx, tx, request.Key, fingerprint)
		if err != nil {
			return err
		}
		if !reservation.IsTerminal() {
			return idempotency.ErrRequestInProgress
		}
		replay = TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}
		return nil
	})
	if loadErr == nil {
		return replay, nil
	}
	if !errors.Is(loadErr, idempotency.ErrReservationNotFound) {
		return TenantMutationResult{}, normalizeTenantCustomDomainError(loadErr)
	}
	var domainID, recordName, recordValue, state string
	var version int64
	err := c.Pool.QueryRow(ctx, `SELECT d.id,d.resource_version,dc.record_name,dc.record_value,dc.state FROM domains d JOIN domain_dns_challenges dc ON dc.domain_id=d.id WHERE d.id=$1 AND d.project_id=(SELECT project_id FROM services WHERE id=$2) AND d.environment_id=$3 AND d.service_id=$2 AND d.kind='custom'`, request.DomainID, request.ServiceID, request.EnvironmentID).Scan(&domainID, &version, &recordName, &recordValue, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantMutationResult{}, ErrTenantCustomDomainNotFound
	}
	if err != nil {
		return TenantMutationResult{}, err
	}
	if version != request.ExpectedVersion {
		return TenantMutationResult{}, ErrTenantCustomDomainVersionConflict
	}
	_ = state
	resolver := c.CustomDNSResolver
	if resolver == nil {
		resolver = &net.Resolver{}
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	values, lookupErr := resolver.LookupTXT(dnsCtx, recordName)
	matched := false
	if lookupErr == nil {
		if len(values) > 64 {
			lookupErr = errors.New("dns_response_too_large")
		} else {
			bytes := 0
			for _, v := range values {
				bytes += len(v)
				if bytes > 4096 {
					lookupErr = errors.New("dns_response_too_large")
					break
				}
				if strings.TrimSpace(v) == recordValue {
					matched = true
				}
			}
		}
	}
	now := c.now().Truncate(time.Microsecond)
	var out TenantMutationResult
	err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		return c.finalizeCustomVerificationInTx(ctx, tx, request, recordName, recordValue, matched, lookupErr, now, &out)
	})
	if err != nil {
		return TenantMutationResult{}, normalizeTenantCustomDomainError(err)
	}
	return cloneTenantMutationResult(out), nil
}

func (c *PostgresTenantMutationCoordinator) finalizeCustomVerificationInTx(ctx context.Context, tx *pg.Tx, request TenantCustomDomainVerifyRequest, recordName, recordValue string, matched bool, lookupErr error, now time.Time, out *TenantMutationResult) error {
	mutationCtx, auth, reservation, err := c.authorizeAndReserveTenantCustomDomain(ctx, tx, request.TenantGeneratedDomainMutationMeta, http.MethodPost, now)
	if err != nil {
		return err
	}
	if replay, ok, err := tenantGeneratedDomainReplay(reservation); ok || err != nil {
		*out = replay
		return err
	}
	var currentVersion int64
	var currentName, currentValue, state string
	err = tx.QueryRow(mutationCtx, `SELECT d.resource_version,dc.record_name,dc.record_value,dc.state FROM domains d JOIN domain_dns_challenges dc ON dc.domain_id=d.id WHERE d.id=$1 AND d.project_id=$2 AND d.environment_id=$3 AND d.service_id=$4 AND d.kind='custom' FOR UPDATE OF d,dc`, request.DomainID, auth.ProjectID, request.EnvironmentID, request.ServiceID).Scan(&currentVersion, &currentName, &currentValue, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTenantCustomDomainNotFound
	}
	if err != nil {
		return err
	}
	if currentVersion != request.ExpectedVersion || currentName != recordName || currentValue != recordValue {
		return ErrTenantCustomDomainVersionConflict
	}
	newState := "pending"
	lastError := ""
	if state == "verified" && (lookupErr != nil || !matched) {
		// A failed manual re-check cannot let an old/stale resolver response
		// withdraw a proof that is still the current incarnation. Renewal and
		// expiry are a separate controller gate.
		newState = "verified"
	} else if lookupErr != nil {
		newState = "failed"
		lastError = "dns_check_unavailable"
	} else if matched {
		newState = "verified"
	} else {
		lastError = "expected TXT ownership value was not observed"
	}
	if _, err := tx.Exec(mutationCtx, `UPDATE domain_dns_challenges SET state=$1,attempts=attempts+1,observed_values='[]'::jsonb,last_error=$2,checked_at=$3,verified_at=CASE WHEN $1='verified' THEN COALESCE(verified_at,$3) ELSE NULL END,updated_at=$3 WHERE domain_id=$4`, newState, lastError, now, request.DomainID); err != nil {
		return err
	}
	if matched && state != "verified" {
		if _, err := tx.Exec(mutationCtx, `UPDATE domains SET resource_version=resource_version+1,updated_at=$1 WHERE id=$2`, now, request.DomainID); err != nil {
			return err
		}
		route, exists, err := getHTTPRouteForCustomInTx(mutationCtx, tx, request.DomainID, true)
		if err != nil {
			return err
		}
		if !exists {
			return ErrTenantCustomDomainNotFound
		}
		route.Public = true
		route, err = c.Routes.PutHTTPRouteInTx(mutationCtx, tx, route)
		if err != nil {
			return err
		}
		scope := router.RouteScope{ProjectID: auth.ProjectID, EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID}
		if err := c.Routes.BindServiceDomainInTx(mutationCtx, tx, request.DomainID, route.ID, scope, now); err != nil {
			return err
		}
		if _, err := bindTenantCustomOpenDeploymentsInTx(mutationCtx, tx, scope, now); err != nil {
			return err
		}
		active, targetErrPort, err := c.resolveTenantGeneratedDomainTargetInTx(mutationCtx, tx, auth, request.EnvironmentID, now)
		if err != nil {
			return err
		}
		_ = targetErrPort
		backends, err := tenantGeneratedDomainReadyBackendsInTx(mutationCtx, tx, active, route, now)
		if err != nil {
			return err
		}
		rev, err := advanceTenantGeneratedRouteGroupInTx(mutationCtx, tx, auth.ProjectID, request.EnvironmentID, now)
		if err != nil {
			return err
		}
		if err := replaceTenantGeneratedDomainBackendsInTx(mutationCtx, tx, route, backends, rev); err != nil {
			return err
		}
	}
	values, err := listTenantCustomDomainViewsInTx(mutationCtx, tx, auth, request.EnvironmentID, c.AppsDomain, request.DomainID, false)
	if err != nil || len(values) != 1 {
		return errors.New("custom domain was not observable after verification")
	}
	response, err := tenantCustomDomainJSONResponse(http.StatusOK, values[0], "")
	if err != nil {
		return err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
		return err
	}
	if err := c.appendTenantCustomAudit(mutationCtx, tx, request.TenantGeneratedDomainMutationMeta, auth, nil, &values[0], now, "domain.custom.verify", "", "allowed"); err != nil {
		return err
	}
	*out = TenantMutationResult{Response: response}
	return nil
}

func (c *PostgresTenantMutationCoordinator) DeleteCustomDomain(ctx context.Context, request TenantCustomDomainDeleteRequest) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantCustomDomainAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, true); err != nil {
		return TenantMutationResult{}, err
	}
	now := c.now().Truncate(time.Microsecond)
	var out TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var inner error
			out, inner = c.deleteTenantCustomDomainInTx(ctx, tx, request, now)
			return inner
		})
		if err == nil {
			return cloneTenantMutationResult(out), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantCustomDomainError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant custom domain delete serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) deleteTenantCustomDomainInTx(ctx context.Context, tx *pg.Tx, request TenantCustomDomainDeleteRequest, now time.Time) (TenantMutationResult, error) {
	mutationCtx, auth, reservation, err := c.authorizeAndReserveTenantCustomDomain(ctx, tx, request.TenantGeneratedDomainMutationMeta, http.MethodDelete, now)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantGeneratedDomainReplay(reservation); ok || err != nil {
		return replay, err
	}
	values, err := listTenantCustomDomainViewsInTx(mutationCtx, tx, auth, request.EnvironmentID, c.AppsDomain, request.DomainID, true)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if len(values) != 1 {
		return TenantMutationResult{}, ErrTenantCustomDomainNotFound
	}
	before := values[0]
	if before.Version != request.ExpectedVersion {
		return TenantMutationResult{}, ErrTenantCustomDomainVersionConflict
	}
	if _, err := tx.Exec(mutationCtx, `INSERT INTO retired_product_domain_ids(domain_id,project_id,environment_id,service_id,retired_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, before.ID, before.ProjectID, before.EnvironmentID, before.ServiceID, now); err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := advanceTenantGeneratedRouteGroupInTx(mutationCtx, tx, auth.ProjectID, request.EnvironmentID, now); err != nil {
		return TenantMutationResult{}, err
	}
	tag, err := tx.Exec(mutationCtx, `DELETE FROM domains WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4 AND kind='custom' AND resource_version=$5`, before.ID, before.ProjectID, before.EnvironmentID, before.ServiceID, request.ExpectedVersion)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return TenantMutationResult{}, ErrTenantCustomDomainVersionConflict
	}
	response := idempotency.StoredResponse{StatusCode: http.StatusNoContent, Header: make(http.Header)}
	if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.appendTenantCustomAudit(mutationCtx, tx, request.TenantGeneratedDomainMutationMeta, auth, &before, nil, now, "domain.custom.delete", "", "allowed"); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) validateTenantCustomDomainAuthority(principal identity.Principal, session identity.Session, serviceID, environmentID string, write bool) error {
	if c == nil || c.Pool == nil || c.Routes == nil || c.Routes.Pool != c.Pool {
		return ErrTenantCustomDomainUnavailable
	}
	if write && (c.Idempotency == nil || c.Idempotency.Pool != c.Pool || c.Audit == nil || c.Audit.Pool != c.Pool || c.IDFactory == nil) {
		return ErrTenantCustomDomainUnavailable
	}
	return c.validateTenantGeneratedDomainAuthority(principal, session, serviceID, environmentID, write)
}
func (c *PostgresTenantMutationCoordinator) authorizeAndReserveTenantCustomDomain(ctx context.Context, tx *pg.Tx, request TenantGeneratedDomainMutationMeta, method string, now time.Time) (context.Context, tenantDeployAuthorization, idempotency.Reservation, error) {
	m, a, err := authorizeTenantDeployCapabilityInTx(ctx, tx, request.Principal, request.Session, request.ServiceID, request.EnvironmentID, now, identity.CapabilityDomainsWrite)
	if err != nil {
		return m, a, idempotency.Reservation{}, err
	}
	path := tenantCustomDomainCollectionPath(request.ServiceID, request.EnvironmentID)
	if request.DomainID != "" {
		path = tenantCustomDomainItemPath(request.ServiceID, request.EnvironmentID, request.DomainID)
	}
	res, err := c.Idempotency.ReserveInTx(m, tx, request.Key, idempotency.RequestFingerprint{Method: method, Path: path, BodySHA256: idempotency.BodyHashSHA256(request.RawBody)}, 24*time.Hour)
	return m, a, res, err
}

func customHostnameExistsInTx(ctx context.Context, tx *pg.Tx, host string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domains WHERE normalized_hostname=$1)`, host).Scan(&exists)
	return exists, err
}

func listTenantCustomDomainViewsInTx(ctx context.Context, tx *pg.Tx, auth tenantDeployAuthorization, environmentID, appsDomain, domainID string, lock bool) ([]TenantCustomDomainView, error) {
	q := `SELECT d.id,d.project_id,d.service_id,d.environment_id,d.normalized_hostname,r.path_prefix,r.target_port,r.public_enabled,d.tls_desired,d.resource_version,COALESCE(dc.state,'pending'),dc.record_name,dc.record_value,dc.attempts,dc.checked_at,dc.verified_at,dc.last_error,CASE WHEN r.public_enabled=FALSE THEN 'private' ELSE COALESCE(g.apply_state,'pending') END,COALESCE((r.public_enabled AND g.apply_state='ready' AND g.desired_revision=g.observed_revision AND g.desired_revision=g.applied_revision AND EXISTS(SELECT 1 FROM route_publication_observations o WHERE o.project_id=g.project_id AND o.environment_id=g.environment_id AND o.desired_revision=g.desired_revision AND o.artifact_hash=g.artifact_hash AND o.state='ready' AND o.traefik_acknowledged AND o.ingress_probe_succeeded)),FALSE),d.created_at,d.updated_at FROM service_domain_bindings b JOIN domains d ON d.id=b.domain_id JOIN http_routes r ON r.id=b.route_id LEFT JOIN domain_dns_challenges dc ON dc.domain_id=d.id LEFT JOIN route_controller_groups g ON g.project_id=d.project_id AND g.environment_id=d.environment_id WHERE b.project_id=$1 AND b.environment_id=$2 AND b.service_id=$3 AND ($4='' OR d.id=$4) AND d.kind='custom' ORDER BY d.normalized_hostname,d.id`
	if lock {
		q += ` FOR UPDATE OF b,d,r`
	}
	rows, err := tx.Query(ctx, q, auth.ProjectID, environmentID, auth.Service.ID, strings.TrimSpace(domainID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TenantCustomDomainView{}
	for rows.Next() {
		var v TenantCustomDomainView
		var state, name, value, last, apply string
		var attempts int64
		var checked, verified *time.Time
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.ServiceID, &v.EnvironmentID, &v.Hostname, &v.Path, &v.TargetPort, &v.Public, &v.TLS, &v.Version, &state, &name, &value, &attempts, &checked, &verified, &last, &apply, &v.RouteReachable, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.WorkspaceID = auth.WorkspaceID
		v.Kind = router.DomainKindCustom
		v.OwnershipState = state
		v.RouteApplyState = router.RouteApplyState(apply)
		v.RoutingDNS = customDomainRoutingDNSView(v.Hostname, appsDomain)
		v.DNSChallenge = TenantCustomDomainDNSView{RecordName: name, RecordValue: value, State: state, Attempts: attempts, CheckedAt: checked, VerifiedAt: verified, LastError: last}
		v.CreatedAt = v.CreatedAt.UTC()
		v.UpdatedAt = v.UpdatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func customDomainRoutingDNSView(hostname, appsDomain string) TenantCustomDomainRoutingDNSView {
	hostname = strings.TrimSpace(hostname)
	appsDomain = strings.Trim(strings.ToLower(strings.TrimSpace(appsDomain)), ".")
	if appsDomain == "" {
		return TenantCustomDomainRoutingDNSView{
			RecordType: "CNAME",
			RecordName: hostname,
			State:      "unavailable",
			Note:       "MIDNIGHT_APPS_DOMAIN is not configured; Midnight cannot provide a routing target.",
		}
	}
	if strings.Count(hostname, ".") == 1 {
		return TenantCustomDomainRoutingDNSView{
			RecordType: "A/ALIAS/ANAME",
			RecordName: hostname,
			State:      "operator_required",
			Note:       "An apex hostname needs an A record or provider-supported ALIAS/ANAME/CNAME flattening target; Midnight does not manage edge IPs.",
		}
	}
	return TenantCustomDomainRoutingDNSView{
		RecordType:  "CNAME",
		RecordName:  hostname,
		RecordValue: appsDomain,
		State:       "operator_required",
		Note:        "Create this CNAME at your DNS provider. Midnight does not configure or verify provider DNS in this lane.",
	}
}

func getHTTPRouteForCustomInTx(ctx context.Context, tx *pg.Tx, domainID string, lock bool) (router.DurableHTTPRoute, bool, error) {
	q := `SELECT id,domain_id,project_id,environment_id,service_id,name,path_prefix,target_port,public_enabled,desired_revision,created_at,updated_at FROM http_routes WHERE domain_id=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	var r router.DurableHTTPRoute
	err := tx.QueryRow(ctx, q, domainID).Scan(&r.ID, &r.DomainID, &r.ProjectID, &r.EnvironmentID, &r.ServiceID, &r.Name, &r.PathPrefix, &r.TargetPort, &r.Public, &r.DesiredRevision, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func bindTenantCustomOpenDeploymentsInTx(ctx context.Context, tx *pg.Tx, scope router.RouteScope, now time.Time) (int64, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO deployment_route_intents(deployment_id,route_id,domain_id,project_id,environment_id,service_id,created_at) SELECT d.id,b.route_id,b.domain_id,b.project_id,b.environment_id,b.service_id,$4 FROM deployments d JOIN service_domain_bindings b ON b.project_id=d.project_id AND b.environment_id=d.environment_id AND b.service_id=d.service_id JOIN http_routes r ON r.id=b.route_id AND r.domain_id=b.domain_id JOIN domains dom ON dom.id=b.domain_id JOIN domain_dns_challenges dc ON dc.domain_id=dom.id WHERE d.project_id=$1 AND d.environment_id=$2 AND d.service_id=$3 AND d.state IN ('queued','building','deploying','active') AND dom.kind='custom' AND dc.state='verified' AND r.public_enabled ON CONFLICT(deployment_id,route_id) DO NOTHING`, scope.ProjectID, scope.EnvironmentID, scope.ServiceID, now.UTC())
	return tag.RowsAffected(), err
}

func tenantCustomDomainCollectionPath(serviceID, environmentID string) string {
	return v1alphaPrefix + "/services/" + serviceID + "/environments/" + environmentID + "/custom-domains"
}
func tenantCustomDomainItemPath(serviceID, environmentID, domainID string) string {
	return tenantCustomDomainCollectionPath(serviceID, environmentID) + "/" + domainID
}
func tenantCustomDomainJSONResponse(status int, value TenantCustomDomainView, location string) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	body = append(body, '\n')
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if location != "" {
		h.Set("Location", location)
	}
	return idempotency.StoredResponse{StatusCode: status, Header: h, Body: body}, nil
}
func (c *PostgresTenantMutationCoordinator) appendTenantCustomAudit(ctx context.Context, tx *pg.Tx, request TenantGeneratedDomainMutationMeta, auth tenantDeployAuthorization, before, after *TenantCustomDomainView, now time.Time, action, reason, decision string) error {
	row := audit.Row{RequestID: request.RequestID, OccurredAt: now, RecordedAt: now, ActorID: request.Principal.UserID, ActorType: "user", AuthMethod: request.Principal.AuthMethod, SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent, WorkspaceID: auth.WorkspaceID, ProjectID: auth.ProjectID, EnvironmentID: request.EnvironmentID, ResourceKind: "domain", ResourceID: request.DomainID, Action: action, ReasonCode: reason, PolicyDecision: decision, Attrs: map[string]any{"capability": string(identity.CapabilityDomainsWrite), "service_id": request.ServiceID}}
	if after != nil {
		row.ResourceID = after.ID
		row.After = map[string]any{"id": after.ID, "hostname": after.Hostname, "kind": "custom", "tls": after.TLS, "ownership_state": after.OwnershipState, "version": after.Version}
	}
	if before != nil {
		row.Before = map[string]any{"id": before.ID, "hostname": before.Hostname, "kind": "custom", "tls": before.TLS, "ownership_state": before.OwnershipState, "version": before.Version}
	}
	_, err := c.Audit.AppendInTx(ctx, tx, row)
	return err
}
func normalizeTenantCustomDomainError(err error) error {
	switch {
	case errors.Is(err, router.ErrDurableRouteNotFound):
		return ErrTenantCustomDomainNotFound
	case errors.Is(err, router.ErrDurableHostnameOwned):
		return ErrTenantCustomDomainConflict
	case errors.Is(err, identity.ErrAccessNotFound):
		return identity.ErrAccessNotFound
	default:
		return err
	}
}

var _ TenantCustomDomainCoordinator = (*PostgresTenantMutationCoordinator)(nil)
