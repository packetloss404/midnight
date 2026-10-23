package router

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	midnightdomain "github.com/midnight/midnight/internal/domain"
)

type PostgresDurableRouteStore struct {
	Pool *dbpg.Pool
	Now  func() time.Time
}

// ManagedRouteRepairResult is support-safe startup evidence. It deliberately
// reports counts rather than tenant hostnames, backend addresses, or route IDs.
type ManagedRouteRepairResult struct {
	GroupsScanned        int
	IntentBindingsAdded  int64
	BackendGroupsChanged int
}

const managedRouteRepairMaxAttempts = 3

// managedRouteOwnershipEligibilityPredicateSQL is shared by managed binding
// selection and controller reads. The controller deliberately applies only
// this ownership portion: it must retain TLS-pending public intent so the
// reconciler can record blocked_tls while atomically removing the route.
const managedRouteOwnershipEligibilityPredicateSQL = `
	d.kind='generated'
	OR (d.kind='custom' AND EXISTS (
		SELECT 1 FROM domain_dns_challenges dc
		 WHERE dc.domain_id=d.id AND dc.state='verified'
	))`

// ManagedServiceRouteEligibilityPredicateSQL is the single database predicate
// for a service-owned route that may participate in deployment routing. Every
// query using it must alias domains as d and http_routes as r. Custom domains
// additionally require durable ownership proof; TLS routes are eligible only
// after certificate readiness, while non-TLS routes must remain disabled.
const ManagedServiceRouteEligibilityPredicateSQL = `
	r.public_enabled=TRUE
	AND (
		(d.tls_desired=FALSE AND d.tls_state='disabled')
		OR (d.tls_desired=TRUE AND d.tls_state='ready')
	)
	AND (` + managedRouteOwnershipEligibilityPredicateSQL + `)`

func NewPostgresDurableRouteStore(pool *dbpg.Pool) *PostgresDurableRouteStore {
	return &PostgresDurableRouteStore{
		Pool: pool,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

func (s *PostgresDurableRouteStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func durableRouteGroupName(projectID, environmentID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + environmentID))
	return "durable-" + hex.EncodeToString(sum[:10])
}

// AcquireRouteEnvironmentMutationLock establishes the global lock order for
// durable route desired-state mutations:
//
//	environment advisory xact lock -> (when needed) sorted
//	service/environment deploy locks -> domain/route/deployment rows
//	-> controller group row
//
// Transaction-scoped advisory locks are reentrant, so composed product
// transactions may call this seam before each route-store operation.
func AcquireRouteEnvironmentMutationLock(
	ctx context.Context,
	tx *dbpg.Tx,
	projectID string,
	environmentID string,
) error {
	if tx == nil {
		return errors.New("postgres durable route transaction is required")
	}
	projectID = strings.TrimSpace(projectID)
	environmentID = strings.TrimSpace(environmentID)
	if projectID == "" || environmentID == "" {
		return fmt.Errorf(
			"%w: project and environment are required",
			ErrDurableRouteInvalid,
		)
	}
	key := durableRouteEnvironmentMutationLockKey(projectID, environmentID)
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock($1::bigint)`,
		key,
	); err != nil {
		return fmt.Errorf("acquire durable route environment mutation lock: %w", err)
	}
	return nil
}

func durableRouteEnvironmentMutationLockKey(
	projectID string,
	environmentID string,
) int64 {
	sum := sha256.Sum256([]byte(
		"midnight:durable-route-environment:v1:" +
			strconv.Itoa(len(projectID)) + ":" + projectID + ":" +
			strconv.Itoa(len(environmentID)) + ":" + environmentID,
	))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// PutDomain creates or idempotently updates domain intent. Scope, kind, and
// apps-domain ownership are immutable; hostname/TLS changes advance the
// environment route revision. TLS-required intent remains unavailable because
// this slice does not contain a certificate issuer.
func (s *PostgresDurableRouteStore) PutDomain(ctx context.Context, value DurableDomain) (DurableDomain, error) {
	if s == nil || s.Pool == nil {
		return DurableDomain{}, errors.New("postgres durable route store pool is required")
	}
	var out DurableDomain
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var err error
		out, err = s.PutDomainInTx(ctx, tx, value)
		return err
	})
	if err != nil {
		return DurableDomain{}, fmt.Errorf("put durable domain: %w", err)
	}
	return out, nil
}

// PutDomainInTx creates or converges one durable domain inside the caller's
// transaction. Tenant deployment intake uses this seam so route intent,
// idempotency, canonical deployment truth, outbox delivery, and audit evidence
// either commit together or leave no artifact.
func (s *PostgresDurableRouteStore) PutDomainInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value DurableDomain,
) (DurableDomain, error) {
	value, err := validateDomainIntent(value)
	if err != nil {
		return DurableDomain{}, err
	}
	if s == nil || tx == nil {
		return DurableDomain{}, errors.New("postgres durable route transaction is required")
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		value.ProjectID,
		value.EnvironmentID,
	); err != nil {
		return DurableDomain{}, err
	}
	current, exists, err := getDomainTx(ctx, tx, value.ID, true)
	if err != nil {
		return DurableDomain{}, err
	}
	now := s.now()
	if exists {
		if current.RouteScope != value.RouteScope || current.Kind != value.Kind || current.AppsDomain != value.AppsDomain {
			return DurableDomain{}, ErrDurableRouteScopeMismatch
		}
		if current.NormalizedHostname == value.NormalizedHostname &&
			current.TLSDesired == value.TLSDesired &&
			current.TLSState == value.TLSState {
			return current, nil
		}
		revision, err := bumpRouteGroupRevision(ctx, tx, value.ProjectID, value.EnvironmentID, now)
		if err != nil {
			return DurableDomain{}, err
		}
		out, err := scanDurableDomain(tx.QueryRow(ctx, `
			UPDATE domains
			   SET hostname = $1,
			       normalized_hostname = $2,
			       tls_desired = $3,
			       tls_state = $4,
			       desired_revision = $5,
			       resource_version = resource_version + 1,
			       updated_at = $6
			 WHERE id = $7
			RETURNING id, project_id, environment_id, service_id, kind,
			          hostname, normalized_hostname, apps_domain, tls_desired,
			          tls_state, desired_revision, created_at, updated_at`,
			value.Hostname, value.NormalizedHostname, value.TLSDesired,
			string(value.TLSState), revision, now, value.ID,
		))
		return out, err
	}
	revision, err := bumpRouteGroupRevision(ctx, tx, value.ProjectID, value.EnvironmentID, now)
	if err != nil {
		return DurableDomain{}, err
	}
	out, err := scanDurableDomain(tx.QueryRow(ctx, `
		INSERT INTO domains (
			id, project_id, environment_id, service_id, kind, hostname,
			normalized_hostname, apps_domain, tls_desired, tls_state,
			desired_revision, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		RETURNING id, project_id, environment_id, service_id, kind,
		          hostname, normalized_hostname, apps_domain, tls_desired,
		          tls_state, desired_revision, created_at, updated_at`,
		value.ID, value.ProjectID, value.EnvironmentID, value.ServiceID,
		string(value.Kind), value.Hostname, value.NormalizedHostname,
		value.AppsDomain, value.TLSDesired, string(value.TLSState), revision, now,
	))
	if err != nil && isRouteUniqueViolation(err) {
		return DurableDomain{}, ErrDurableHostnameOwned
	}
	return out, err
}

func (s *PostgresDurableRouteStore) GetDomain(ctx context.Context, id string) (DurableDomain, error) {
	if s == nil || s.Pool == nil {
		return DurableDomain{}, errors.New("postgres durable route store pool is required")
	}
	out, err := scanDurableDomain(s.Pool.QueryRow(ctx, `
		SELECT id, project_id, environment_id, service_id, kind,
		       hostname, normalized_hostname, apps_domain, tls_desired,
		       tls_state, desired_revision, created_at, updated_at
		  FROM domains WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableDomain{}, ErrDurableRouteNotFound
	}
	if err != nil {
		return DurableDomain{}, fmt.Errorf("get durable domain: %w", err)
	}
	return out, nil
}

func (s *PostgresDurableRouteStore) DeleteDomain(ctx context.Context, id string) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		scopeValue, exists, err := getDomainTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			scopeValue.ProjectID,
			scopeValue.EnvironmentID,
		); err != nil {
			return err
		}
		current, exists, err := getDomainTx(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if _, err := bumpRouteGroupRevision(ctx, tx, current.ProjectID, current.EnvironmentID, s.now()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM domains WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete durable domain: %w", err)
	}
	return nil
}

func (s *PostgresDurableRouteStore) ListDomainRouteStates(ctx context.Context, scope RouteScope) ([]DomainRouteState, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, project_id, environment_id, service_id, kind,
		       hostname, normalized_hostname, apps_domain, tls_desired,
		       tls_state, desired_revision, created_at, updated_at
		  FROM domains
		 WHERE project_id = $1 AND environment_id = $2 AND service_id = $3
		 ORDER BY normalized_hostname, id`,
		scope.ProjectID, scope.EnvironmentID, scope.ServiceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list durable domains: %w", err)
	}
	defer rows.Close()
	var out []DomainRouteState
	for rows.Next() {
		domainValue, err := scanDurableDomain(rows)
		if err != nil {
			return nil, err
		}
		state := DomainRouteState{Domain: domainValue, ApplyState: RouteApplyPending}
		routeRows, err := s.Pool.Query(ctx, `
			SELECT id, domain_id, project_id, environment_id, service_id,
			       name, path_prefix, target_port, public_enabled,
			       desired_revision, created_at, updated_at
			  FROM http_routes
			 WHERE domain_id = $1
			 ORDER BY path_prefix, id`, domainValue.ID)
		if err != nil {
			return nil, fmt.Errorf("list durable domain routes: %w", err)
		}
		for routeRows.Next() {
			routeValue, err := scanDurableHTTPRoute(routeRows)
			if err != nil {
				routeRows.Close()
				return nil, err
			}
			state.Routes = append(state.Routes, routeValue)
		}
		if err := routeRows.Err(); err != nil {
			routeRows.Close()
			return nil, err
		}
		routeRows.Close()
		err = s.Pool.QueryRow(ctx, `
			SELECT apply_state, artifact_hash, last_error_code, last_error_detail
			  FROM route_controller_groups
			 WHERE project_id = $1 AND environment_id = $2`,
			scope.ProjectID, scope.EnvironmentID,
		).Scan(&state.ApplyState, &state.ArtifactHash, &state.ErrorCode, &state.ErrorDetail)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load domain route apply state: %w", err)
		}
		challenge, err := s.getDomainDNSChallenge(ctx, domainValue.ID)
		if err != nil && !errors.Is(err, ErrDurableRouteNotFound) {
			return nil, err
		}
		if err == nil {
			state.Challenge = &challenge
		}
		order, err := s.getDomainCertificateOrder(ctx, domainValue.ID)
		if err != nil && !errors.Is(err, ErrDurableRouteNotFound) {
			return nil, err
		}
		if err == nil {
			state.Certificate = &order
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

// ListGeneratedRouteProjections returns the generated public route bound to
// each requested deployment. It is a single bounded query for product reads
// and never infers route intent from service/environment coincidence.
func (s *PostgresDurableRouteStore) ListGeneratedRouteProjections(
	ctx context.Context,
	projectID string,
	serviceID string,
	deploymentIDs []string,
) ([]GeneratedRouteProjection, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	projectID = strings.TrimSpace(projectID)
	serviceID = strings.TrimSpace(serviceID)
	deploymentIDs = normalizedRouteProjectionIDs(deploymentIDs)
	if projectID == "" || serviceID == "" || len(deploymentIDs) == 0 {
		return []GeneratedRouteProjection{}, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT i.deployment_id,
		       d.project_id, d.environment_id, d.service_id,
		       d.normalized_hostname, r.path_prefix,
		       COALESCE(g.apply_state, 'pending'),
		       COALESCE((
		           EXISTS (
		               SELECT 1
		                 FROM route_backends b
		                WHERE b.route_id = r.id
		                  AND b.deployment_id = i.deployment_id
		                  AND b.project_id = i.project_id
		                  AND b.environment_id = i.environment_id
		                  AND b.service_id = i.service_id
		                  AND b.ready = TRUE
		           )
		           AND g.apply_state = 'ready'
		           AND g.desired_revision = g.observed_revision
		           AND g.desired_revision = g.applied_revision
		           AND o.desired_revision = g.desired_revision
		           AND o.artifact_hash = g.artifact_hash
		           AND o.state = 'ready'
		           AND o.traefik_acknowledged = TRUE
		           AND o.ingress_probe_succeeded = TRUE
		       ), FALSE) AS reachable
		  FROM deployment_route_intents i
		  JOIN http_routes r
		    ON r.id = i.route_id
		   AND r.project_id = i.project_id
		   AND r.environment_id = i.environment_id
		   AND r.service_id = i.service_id
		  JOIN domains d
		    ON d.id = i.domain_id
		   AND d.project_id = i.project_id
		   AND d.environment_id = i.environment_id
		   AND d.service_id = i.service_id
		  LEFT JOIN route_controller_groups g
		    ON g.project_id = i.project_id
		   AND g.environment_id = i.environment_id
		  LEFT JOIN route_publication_observations o
		    ON o.project_id = g.project_id
		   AND o.environment_id = g.environment_id
		   AND o.desired_revision = g.desired_revision
		 WHERE i.project_id = $1
		   AND i.service_id = $2
		   AND i.deployment_id = ANY($3)
		   AND d.kind = 'generated'
		   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
		 ORDER BY i.deployment_id`,
		projectID,
		serviceID,
		deploymentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list generated route projections: %w", err)
	}
	defer rows.Close()
	out := make([]GeneratedRouteProjection, 0, len(deploymentIDs))
	for rows.Next() {
		var value GeneratedRouteProjection
		if err := rows.Scan(
			&value.DeploymentID,
			&value.ProjectID,
			&value.EnvironmentID,
			&value.ServiceID,
			&value.Hostname,
			&value.Path,
			&value.ApplyState,
			&value.Reachable,
		); err != nil {
			return nil, fmt.Errorf("scan generated route projection: %w", err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list generated route projections: %w", err)
	}
	return out, nil
}

func normalizedRouteProjectionIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ListDeploymentPublicRouteStates returns every public route explicitly bound
// to the exact canonical deployment and scope. It deliberately does not hide
// custom or incomplete TLS lifecycle state: promotion validates those states
// fail closed instead of silently dropping a legacy hostname.
func (s *PostgresDurableRouteStore) ListDeploymentPublicRouteStates(
	ctx context.Context,
	deploymentID string,
	scope RouteScope,
) ([]DurableDesiredRoute, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return nil, fmt.Errorf("%w: deployment id is required", ErrDurableRouteInvalid)
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	return s.listBoundRouteStates(ctx, `
		SELECT r.id, r.domain_id, r.project_id, r.environment_id, r.service_id,
		       r.name, r.path_prefix, r.target_port, r.public_enabled,
		       r.desired_revision, r.created_at, r.updated_at,
		       d.id, d.project_id, d.environment_id, d.service_id, d.kind,
		       d.hostname, d.normalized_hostname, d.apps_domain, d.tls_desired,
		       d.tls_state, d.desired_revision, d.created_at, d.updated_at
		  FROM deployment_route_intents i
		  JOIN http_routes r
		    ON r.id = i.route_id
		   AND r.project_id = i.project_id
		   AND r.environment_id = i.environment_id
		   AND r.service_id = i.service_id
		  JOIN domains d
		    ON d.id = i.domain_id
		   AND d.id = r.domain_id
		   AND d.project_id = i.project_id
		   AND d.environment_id = i.environment_id
		   AND d.service_id = i.service_id
		 WHERE i.deployment_id = $1
		   AND i.project_id = $2
		   AND i.environment_id = $3
		   AND i.service_id = $4
		   AND r.public_enabled=TRUE
		   AND (`+managedRouteOwnershipEligibilityPredicateSQL+`)
		 ORDER BY r.id`,
		deploymentID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	)
}

// SelectManagedServiceRoute resolves one operator/automation route selector
// against explicit product ownership. It never derives or persists a hostname.
// BindServiceDomain records the service ownership binding for one
// (domain, route) pair outside an existing transaction. It is the
// non-transactional entry point to BindServiceDomainInTx, used by the
// operator recovery domain-create path so a domain it creates is bound the
// same way the tenant coordinator binds one. Without the binding the domain
// and route exist but no deploy can ever select them, leaving the route
// permanently blocked on a missing backend.
func (s *PostgresDurableRouteStore) BindServiceDomain(
	ctx context.Context,
	domainID string,
	routeID string,
	scope RouteScope,
	createdAt time.Time,
) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	return s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		return s.BindServiceDomainInTx(ctx, tx, domainID, routeID, scope, createdAt)
	})
}

func (s *PostgresDurableRouteStore) SelectManagedServiceRoute(
	ctx context.Context,
	scope RouteScope,
	selector ManagedRouteSelector,
) (DurableDesiredRoute, error) {
	if s == nil || s.Pool == nil {
		return DurableDesiredRoute{}, errors.New("postgres durable route store pool is required")
	}
	if err := scope.validate(); err != nil {
		return DurableDesiredRoute{}, err
	}
	var out DurableDesiredRoute
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var err error
		out, err = selectManagedServiceRouteTx(ctx, tx, scope, selector, false)
		return err
	})
	if err != nil {
		return DurableDesiredRoute{}, fmt.Errorf("select managed service route: %w", err)
	}
	return out, nil
}

// BindManagedServiceRouteToDeployment revalidates an already-resolved
// selector under the route-environment lock and copies its exact durable
// identity to one canonical deployment. This is the only worker behavior for
// structured route selectors; workers never create domains from payload host
// strings.
func (s *PostgresDurableRouteStore) BindManagedServiceRouteToDeployment(
	ctx context.Context,
	deploymentID string,
	scope RouteScope,
	selector ManagedRouteSelector,
	createdAt time.Time,
) (DurableDesiredRoute, error) {
	if s == nil || s.Pool == nil {
		return DurableDesiredRoute{}, errors.New("postgres durable route store pool is required")
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return DurableDesiredRoute{}, fmt.Errorf("%w: deployment id is required", ErrDurableRouteInvalid)
	}
	if err := scope.validate(); err != nil {
		return DurableDesiredRoute{}, err
	}
	if createdAt.IsZero() {
		createdAt = s.now()
	} else {
		createdAt = createdAt.UTC()
	}
	var out DurableDesiredRoute
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if err := AcquireRouteEnvironmentMutationLock(ctx, tx, scope.ProjectID, scope.EnvironmentID); err != nil {
			return err
		}
		var deploymentScope RouteScope
		if err := tx.QueryRow(ctx, `
			SELECT project_id,environment_id,service_id
			  FROM deployments
			 WHERE id=$1
			 FOR SHARE`, deploymentID).Scan(
			&deploymentScope.ProjectID,
			&deploymentScope.EnvironmentID,
			&deploymentScope.ServiceID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDurableRouteNotFound
			}
			return err
		}
		if deploymentScope != scope {
			return ErrDurableRouteScopeMismatch
		}
		var err error
		out, err = selectManagedServiceRouteTx(ctx, tx, scope, selector, true)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO deployment_route_intents (
				deployment_id,route_id,domain_id,project_id,
				environment_id,service_id,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (deployment_id,route_id) DO NOTHING`,
			deploymentID,
			out.Route.ID,
			out.Domain.ID,
			scope.ProjectID,
			scope.EnvironmentID,
			scope.ServiceID,
			createdAt,
		)
		return err
	})
	if err != nil {
		return DurableDesiredRoute{}, fmt.Errorf("bind managed service route to deployment: %w", err)
	}
	return out, nil
}

func selectManagedServiceRouteTx(
	ctx context.Context,
	tx *dbpg.Tx,
	scope RouteScope,
	selector ManagedRouteSelector,
	lock bool,
) (DurableDesiredRoute, error) {
	host := strings.TrimSpace(selector.Host)
	kind := string(DomainKindGenerated)
	if host != "" {
		normalized, err := midnightdomain.NormalizeHostname(host)
		if err != nil {
			return DurableDesiredRoute{}, fmt.Errorf("%w: invalid route hostname selector", ErrDurableRouteInvalid)
		}
		host = normalized
		kind = string(DomainKindCustom)
	}
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF b,d,r,g"
	}
	rows, err := tx.Query(ctx, `
		SELECT r.id,r.domain_id,r.project_id,r.environment_id,r.service_id,
		       r.name,r.path_prefix,r.target_port,r.public_enabled,
		       r.desired_revision,r.created_at,r.updated_at,
		       d.id,d.project_id,d.environment_id,d.service_id,d.kind,
		       d.hostname,d.normalized_hostname,d.apps_domain,d.tls_desired,
		       d.tls_state,d.desired_revision,d.created_at,d.updated_at
		  FROM service_domain_bindings b
		  JOIN http_routes r
		    ON r.id=b.route_id AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		  JOIN domains d
		    ON d.id=b.domain_id AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id AND d.service_id=b.service_id
		  JOIN route_controller_groups g
		    ON g.project_id=b.project_id AND g.environment_id=b.environment_id
		 WHERE b.project_id=$1 AND b.environment_id=$2 AND b.service_id=$3
		   AND d.kind=$4
		   AND ($5='' OR d.normalized_hostname=$5)
		   AND ($6='' OR r.name=$6)
		   AND ($7='' OR r.path_prefix=$7)
		   AND d.tls_desired=$8
		   AND ($9='' OR g.route_group=$9)
		   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
		 ORDER BY d.normalized_hostname,r.name,r.id`+lockClause,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
		kind,
		host,
		strings.TrimSpace(selector.Name),
		strings.TrimSpace(selector.PathPrefix),
		selector.TLSDesired,
		strings.TrimSpace(selector.Group),
	)
	if err != nil {
		return DurableDesiredRoute{}, err
	}
	defer rows.Close()
	values := make([]DurableDesiredRoute, 0, 2)
	for rows.Next() {
		var value DurableDesiredRoute
		if err := rows.Scan(
			&value.Route.ID, &value.Route.DomainID,
			&value.Route.ProjectID, &value.Route.EnvironmentID, &value.Route.ServiceID,
			&value.Route.Name, &value.Route.PathPrefix, &value.Route.TargetPort,
			&value.Route.Public, &value.Route.DesiredRevision,
			&value.Route.CreatedAt, &value.Route.UpdatedAt,
			&value.Domain.ID, &value.Domain.ProjectID, &value.Domain.EnvironmentID,
			&value.Domain.ServiceID, &value.Domain.Kind, &value.Domain.Hostname,
			&value.Domain.NormalizedHostname, &value.Domain.AppsDomain,
			&value.Domain.TLSDesired, &value.Domain.TLSState,
			&value.Domain.DesiredRevision, &value.Domain.CreatedAt, &value.Domain.UpdatedAt,
		); err != nil {
			return DurableDesiredRoute{}, err
		}
		values = append(values, value)
		if len(values) > 1 {
			return DurableDesiredRoute{}, ErrDurableRouteAmbiguous
		}
	}
	if err := rows.Err(); err != nil {
		return DurableDesiredRoute{}, err
	}
	if len(values) == 0 {
		return DurableDesiredRoute{}, ErrDurableRouteNotFound
	}
	return values[0], nil
}

// ListServiceGeneratedRouteStates returns only routes present in the explicit
// service_domain_bindings product table. Scope coincidence alone never causes
// a recovery-only route to be inherited by a future deployment.
func (s *PostgresDurableRouteStore) ListServiceGeneratedRouteStates(
	ctx context.Context,
	scope RouteScope,
) ([]DurableDesiredRoute, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	return s.listBoundRouteStates(ctx, `
		SELECT r.id, r.domain_id, r.project_id, r.environment_id, r.service_id,
		       r.name, r.path_prefix, r.target_port, r.public_enabled,
		       r.desired_revision, r.created_at, r.updated_at,
		       d.id, d.project_id, d.environment_id, d.service_id, d.kind,
		       d.hostname, d.normalized_hostname, d.apps_domain, d.tls_desired,
		       d.tls_state, d.desired_revision, d.created_at, d.updated_at
		  FROM service_domain_bindings b
		  JOIN http_routes r
		    ON r.id = b.route_id
		   AND r.domain_id = b.domain_id
		   AND r.project_id = b.project_id
		   AND r.environment_id = b.environment_id
		   AND r.service_id = b.service_id
		  JOIN domains d
		    ON d.id = b.domain_id
		   AND d.id = r.domain_id
		   AND d.project_id = b.project_id
		   AND d.environment_id = b.environment_id
		   AND d.service_id = b.service_id
		 WHERE b.project_id = $1
		   AND b.environment_id = $2
		   AND b.service_id = $3
		   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
		 ORDER BY r.id`,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	)
}

// ReconcileManagedDeploymentRouteState repairs derived route state after an
// upgrade or interrupted process restart. service_domain_bindings remains the
// sole authority: a loose recovery domain is never inherited merely because
// its project/environment/service happens to match a deployment.
//
// Missing exact intents are filled for open deployments. Backend sets are
// rebuilt only from the canonical active deployment and its durably ready,
// running instances. Every group replacement retains the existing strict CAS,
// eligibility, ownership, and route-environment advisory-lock checks.
func (s *PostgresDurableRouteStore) ReconcileManagedDeploymentRouteState(
	ctx context.Context,
) (ManagedRouteRepairResult, error) {
	if s == nil || s.Pool == nil {
		return ManagedRouteRepairResult{}, errors.New("postgres durable route store pool is required")
	}
	type groupScope struct {
		projectID     string
		environmentID string
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT b.project_id, b.environment_id
		  FROM service_domain_bindings b
		  JOIN domains d
		    ON d.id=b.domain_id
		   AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id
		   AND d.service_id=b.service_id
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		 WHERE r.public_enabled=TRUE
		   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
		 ORDER BY b.project_id, b.environment_id`)
	if err != nil {
		return ManagedRouteRepairResult{}, fmt.Errorf("list managed route repair groups: %w", err)
	}
	groups := make([]groupScope, 0)
	for rows.Next() {
		var group groupScope
		if err := rows.Scan(&group.projectID, &group.environmentID); err != nil {
			rows.Close()
			return ManagedRouteRepairResult{}, fmt.Errorf("scan managed route repair group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ManagedRouteRepairResult{}, fmt.Errorf("iterate managed route repair groups: %w", err)
	}
	rows.Close()

	result := ManagedRouteRepairResult{GroupsScanned: len(groups)}
	for _, scope := range groups {
		added, repaired, absent, err := retryManagedRouteRepair(func() (int64, bool, error) {
			return s.reconcileManagedRouteEnvironment(
				ctx,
				scope.projectID,
				scope.environmentID,
			)
		})
		if err != nil {
			return ManagedRouteRepairResult{}, err
		}
		if absent {
			continue
		}
		if repaired {
			result.BackendGroupsChanged++
		}
		result.IntentBindingsAdded += added
	}
	return result, nil
}

func retryManagedRouteRepair(run func() (int64, bool, error)) (int64, bool, bool, error) {
	for attempt := 0; attempt < managedRouteRepairMaxAttempts; attempt++ {
		added, changed, err := run()
		switch {
		case errors.Is(err, ErrDurableRouteNotFound):
			return 0, false, true, nil
		case errors.Is(err, ErrDurableRouteRevisionMoved):
			continue
		case err != nil:
			return 0, false, false, err
		default:
			return added, changed, false, nil
		}
	}
	return 0, false, false, fmt.Errorf(
		"repair managed route backends after %d attempts: %w",
		managedRouteRepairMaxAttempts,
		ErrDurableRouteRevisionMoved,
	)
}

// reconcileManagedRouteEnvironment performs one repair as a single locked
// snapshot. The route-environment lock is always first, followed by sorted
// service/environment deployment locks. Promotion and intake therefore cannot
// move an active pointer between backend selection and replacement.
func (s *PostgresDurableRouteStore) reconcileManagedRouteEnvironment(
	ctx context.Context,
	projectID string,
	environmentID string,
) (int64, bool, error) {
	var added int64
	var changed bool
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if err := AcquireRouteEnvironmentMutationLock(ctx, tx, projectID, environmentID); err != nil {
			return err
		}

		serviceRows, err := tx.Query(ctx, `
			SELECT DISTINCT b.service_id
			  FROM service_domain_bindings b
			  JOIN domains d
			    ON d.id=b.domain_id AND d.project_id=b.project_id
			   AND d.environment_id=b.environment_id AND d.service_id=b.service_id
			  JOIN http_routes r
			    ON r.id=b.route_id AND r.domain_id=b.domain_id
			   AND r.project_id=b.project_id AND r.environment_id=b.environment_id
			   AND r.service_id=b.service_id
			 WHERE b.project_id=$1 AND b.environment_id=$2
			   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
			 ORDER BY b.service_id`, projectID, environmentID)
		if err != nil {
			return err
		}
		serviceIDs := make([]string, 0)
		for serviceRows.Next() {
			var serviceID string
			if err := serviceRows.Scan(&serviceID); err != nil {
				serviceRows.Close()
				return err
			}
			serviceIDs = append(serviceIDs, serviceID)
		}
		if err := serviceRows.Err(); err != nil {
			serviceRows.Close()
			return err
		}
		serviceRows.Close()
		if len(serviceIDs) == 0 {
			return ErrDurableRouteNotFound
		}
		for _, serviceID := range serviceIDs {
			lockKey := "service-environment-deploy:" + serviceID + ":" + environmentID
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
				return fmt.Errorf("repair managed route service/environment lock: %w", err)
			}
		}

		var expectedRevision int64
		if err := tx.QueryRow(ctx, `
			SELECT desired_revision
			  FROM route_controller_groups
			 WHERE project_id=$1 AND environment_id=$2
			 FOR UPDATE`, projectID, environmentID).Scan(&expectedRevision); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDurableRouteNotFound
			}
			return err
		}

		openRows, err := tx.Query(ctx, `
			SELECT id
			  FROM deployments
			 WHERE project_id=$1 AND environment_id=$2
			   AND service_id=ANY($3)
			   AND state IN ('queued','building','deploying','active')
			 ORDER BY service_id,id
			 FOR SHARE`, projectID, environmentID, serviceIDs)
		if err != nil {
			return err
		}
		for openRows.Next() {
			var deploymentID string
			if err := openRows.Scan(&deploymentID); err != nil {
				openRows.Close()
				return err
			}
		}
		if err := openRows.Err(); err != nil {
			openRows.Close()
			return err
		}
		openRows.Close()

		tag, err := tx.Exec(ctx, `
			INSERT INTO deployment_route_intents (
				deployment_id,route_id,domain_id,project_id,
				environment_id,service_id,created_at
			)
			SELECT dep.id,b.route_id,b.domain_id,b.project_id,
			       b.environment_id,b.service_id,$3
			  FROM service_domain_bindings b
			  JOIN domains d
			    ON d.id=b.domain_id AND d.project_id=b.project_id
			   AND d.environment_id=b.environment_id AND d.service_id=b.service_id
			  JOIN http_routes r
			    ON r.id=b.route_id AND r.domain_id=b.domain_id
			   AND r.project_id=b.project_id AND r.environment_id=b.environment_id
			   AND r.service_id=b.service_id
			  JOIN deployments dep
			    ON dep.project_id=b.project_id AND dep.environment_id=b.environment_id
			   AND dep.service_id=b.service_id
			 WHERE b.project_id=$1 AND b.environment_id=$2
			   AND dep.state IN ('queued','building','deploying','active')
			   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
			ON CONFLICT (deployment_id,route_id) DO NOTHING`,
			projectID, environmentID, s.now())
		if err != nil {
			return err
		}
		added = tag.RowsAffected()

		type repairRoute struct {
			value DurableHTTPRoute
		}
		routeRows, err := tx.Query(ctx, `
			SELECT r.id,r.domain_id,r.project_id,r.environment_id,r.service_id,
			       r.name,r.path_prefix,r.target_port,r.public_enabled,
			       r.desired_revision,r.created_at,r.updated_at
			  FROM service_domain_bindings b
			  JOIN domains d
			    ON d.id=b.domain_id AND d.project_id=b.project_id
			   AND d.environment_id=b.environment_id AND d.service_id=b.service_id
			  JOIN http_routes r
			    ON r.id=b.route_id AND r.domain_id=b.domain_id
			   AND r.project_id=b.project_id AND r.environment_id=b.environment_id
			   AND r.service_id=b.service_id
			 WHERE b.project_id=$1 AND b.environment_id=$2
			   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
			 ORDER BY r.id
			 FOR UPDATE OF b,d,r`, projectID, environmentID)
		if err != nil {
			return err
		}
		routes := make([]repairRoute, 0)
		for routeRows.Next() {
			var route repairRoute
			if err := routeRows.Scan(
				&route.value.ID, &route.value.DomainID, &route.value.ProjectID,
				&route.value.EnvironmentID, &route.value.ServiceID, &route.value.Name,
				&route.value.PathPrefix, &route.value.TargetPort, &route.value.Public,
				&route.value.DesiredRevision, &route.value.CreatedAt, &route.value.UpdatedAt,
			); err != nil {
				routeRows.Close()
				return err
			}
			routes = append(routes, route)
		}
		if err := routeRows.Err(); err != nil {
			routeRows.Close()
			return err
		}
		routeRows.Close()

		activeByService := make(map[string]string, len(serviceIDs))
		for _, serviceID := range serviceIDs {
			var pointer *string
			err := tx.QueryRow(ctx, `
				SELECT active_deployment_id
				  FROM service_environment_state
				 WHERE service_id=$1 AND environment_id=$2
				 FOR UPDATE`, serviceID, environmentID).Scan(&pointer)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if pointer == nil || strings.TrimSpace(*pointer) == "" {
				continue
			}
			var activeID string
			err = tx.QueryRow(ctx, `
				SELECT id
				  FROM deployments
				 WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4
				   AND state='active' AND active=TRUE
				 FOR SHARE`, strings.TrimSpace(*pointer), projectID, environmentID, serviceID).Scan(&activeID)
			if errors.Is(err, pgx.ErrNoRows) {
				// An inconsistent pointer cannot authorize backends. Emptying the
				// set is safer than retaining a previously active deployment.
				continue
			}
			if err != nil {
				return err
			}
			activeByService[serviceID] = activeID
		}

		sets := make([]DurableRouteBackendSet, 0, len(routes))
		for _, route := range routes {
			set := DurableRouteBackendSet{RouteID: route.value.ID, Backends: []DurableRouteBackend{}}
			activeID := activeByService[route.value.ServiceID]
			if activeID != "" {
				instanceRows, err := tx.Query(ctx, `
					SELECT instance_id,container_ip
					  FROM deployment_instances
					 WHERE deployment_id=$1
					   AND desired_state='running' AND observed_state='running'
					   AND ready=TRUE AND btrim(container_ip)<>''
					 ORDER BY replica_ordinal,instance_id
					 FOR SHARE`, activeID)
				if err != nil {
					return err
				}
				for instanceRows.Next() {
					var instanceID, containerIP string
					if err := instanceRows.Scan(&instanceID, &containerIP); err != nil {
						instanceRows.Close()
						return err
					}
					sum := sha256.Sum256([]byte(route.value.ID + "\x00" + activeID + "\x00" + instanceID))
					set.Backends = append(set.Backends, DurableRouteBackend{
						ID: "bck_" + hex.EncodeToString(sum[:16]), RouteID: route.value.ID,
						RouteScope: route.value.RouteScope, DeploymentID: activeID,
						InstanceID: instanceID,
						ServiceURL: (&url.URL{Scheme: "http", Host: net.JoinHostPort(strings.TrimSpace(containerIP), strconv.Itoa(route.value.TargetPort))}).String(),
						Ready:      true, ObservedAt: s.now(),
					})
				}
				if err := instanceRows.Err(); err != nil {
					instanceRows.Close()
					return err
				}
				instanceRows.Close()
			}
			sets = append(sets, set)
		}

		var currentRevision int64
		if err := tx.QueryRow(ctx, `
			SELECT desired_revision FROM route_controller_groups
			 WHERE project_id=$1 AND environment_id=$2 FOR UPDATE`,
			projectID, environmentID).Scan(&currentRevision); err != nil {
			return err
		}
		if currentRevision != expectedRevision {
			return ErrDurableRouteRevisionMoved
		}
		equivalent := true
		for _, set := range sets {
			current, err := listRouteBackendsTx(ctx, tx, set.RouteID)
			if err != nil {
				return err
			}
			if !equivalentBackends(current, set.Backends) {
				equivalent = false
			}
		}
		if equivalent {
			return nil
		}
		revision, err := bumpRouteGroupRevision(ctx, tx, projectID, environmentID, s.now())
		if err != nil {
			return err
		}
		for _, set := range sets {
			if _, err := tx.Exec(ctx, `DELETE FROM route_backends WHERE route_id=$1`, set.RouteID); err != nil {
				return err
			}
			for _, backend := range set.Backends {
				backend.DesiredRevision = revision
				if _, err := tx.Exec(ctx, `
					INSERT INTO route_backends (
						id,route_id,project_id,environment_id,service_id,
						deployment_id,instance_id,service_url,ready,
						desired_revision,observed_at
					) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
					backend.ID, backend.RouteID, backend.ProjectID, backend.EnvironmentID,
					backend.ServiceID, backend.DeploymentID, backend.InstanceID,
					backend.ServiceURL, backend.Ready, backend.DesiredRevision, backend.ObservedAt,
				); err != nil {
					return err
				}
			}
		}
		changed = true
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("repair managed route environment: %w", err)
	}
	return added, changed, nil
}

func (s *PostgresDurableRouteStore) listBoundRouteStates(
	ctx context.Context,
	query string,
	args ...any,
) ([]DurableDesiredRoute, error) {
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bound route states: %w", err)
	}
	out := make([]DurableDesiredRoute, 0)
	for rows.Next() {
		var desired DurableDesiredRoute
		if err := rows.Scan(
			&desired.Route.ID, &desired.Route.DomainID,
			&desired.Route.ProjectID, &desired.Route.EnvironmentID,
			&desired.Route.ServiceID, &desired.Route.Name,
			&desired.Route.PathPrefix, &desired.Route.TargetPort,
			&desired.Route.Public, &desired.Route.DesiredRevision,
			&desired.Route.CreatedAt, &desired.Route.UpdatedAt,
			&desired.Domain.ID, &desired.Domain.ProjectID,
			&desired.Domain.EnvironmentID, &desired.Domain.ServiceID,
			&desired.Domain.Kind, &desired.Domain.Hostname,
			&desired.Domain.NormalizedHostname, &desired.Domain.AppsDomain,
			&desired.Domain.TLSDesired, &desired.Domain.TLSState,
			&desired.Domain.DesiredRevision, &desired.Domain.CreatedAt,
			&desired.Domain.UpdatedAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bound route state: %w", err)
		}
		out = append(out, desired)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list bound route states: %w", err)
	}
	rows.Close()
	for index := range out {
		backends, err := s.listRouteBackends(ctx, out[index].Route.ID)
		if err != nil {
			return nil, err
		}
		out[index].Backends = backends
	}
	return out, nil
}

// EnsureDomainLifecycle records the next externally actionable state. Custom
// domains receive a TXT ownership challenge. TLS intent receives a pending
// order with issuer=unconfigured until an operator wires an issuer; this state
// is deliberately not certificate-ready.
func (s *PostgresDurableRouteStore) EnsureDomainLifecycle(ctx context.Context, value DurableDomain) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		return s.EnsureDomainLifecycleInTx(ctx, tx, value)
	})
	if err != nil {
		return fmt.Errorf("ensure domain lifecycle: %w", err)
	}
	return nil
}

// EnsureDomainLifecycleInTx records domain lifecycle intent inside a caller
// transaction. Generated non-TLS routes intentionally create no lifecycle row.
func (s *PostgresDurableRouteStore) EnsureDomainLifecycleInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value DurableDomain,
) error {
	if s == nil || tx == nil {
		return errors.New("postgres durable route transaction is required")
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		value.ProjectID,
		value.EnvironmentID,
	); err != nil {
		return err
	}
	if value.Kind == DomainKindCustom {
		sum := sha256.Sum256([]byte("domain-ownership\x00" + value.ID + "\x00" + value.NormalizedHostname))
		recordValue := "midnight-verification=" + hex.EncodeToString(sum[:16])
		_, err := tx.Exec(ctx, `
			INSERT INTO domain_dns_challenges (
				domain_id, project_id, environment_id, service_id,
				record_name, record_value, state, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,'pending',$7,$7)
			ON CONFLICT (domain_id) DO NOTHING`,
			value.ID, value.ProjectID, value.EnvironmentID, value.ServiceID,
			"_midnight-challenge."+value.NormalizedHostname, recordValue, s.now(),
		)
		if err != nil {
			return fmt.Errorf("ensure domain DNS challenge: %w", err)
		}
	}
	if value.TLSDesired {
		_, err := tx.Exec(ctx, `
			INSERT INTO domain_certificate_orders (
				domain_id, project_id, environment_id, service_id,
				issuer, state, created_at, updated_at
			) VALUES ($1,$2,$3,$4,'unconfigured','pending',$5,$5)
			ON CONFLICT (domain_id) DO NOTHING`,
			value.ID, value.ProjectID, value.EnvironmentID, value.ServiceID, s.now(),
		)
		if err != nil {
			return fmt.Errorf("ensure domain certificate order: %w", err)
		}
	}
	return nil
}

func (s *PostgresDurableRouteStore) getDomainDNSChallenge(ctx context.Context, domainID string) (DomainDNSChallenge, error) {
	var out DomainDNSChallenge
	var observed []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT domain_id, project_id, environment_id, service_id,
		       record_name, record_value, state, observed_values, attempts,
		       last_error, checked_at, created_at, updated_at
		  FROM domain_dns_challenges
		 WHERE domain_id = $1`, domainID,
	).Scan(
		&out.DomainID, &out.ProjectID, &out.EnvironmentID, &out.ServiceID,
		&out.RecordName, &out.RecordValue, &out.State, &observed, &out.Attempts,
		&out.LastError, &out.CheckedAt, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainDNSChallenge{}, ErrDurableRouteNotFound
	}
	if err != nil {
		return DomainDNSChallenge{}, fmt.Errorf("get domain DNS challenge: %w", err)
	}
	if err := json.Unmarshal(observed, &out.ObservedValues); err != nil {
		return DomainDNSChallenge{}, fmt.Errorf("decode domain DNS observations: %w", err)
	}
	return out, nil
}

func (s *PostgresDurableRouteStore) getDomainCertificateOrder(ctx context.Context, domainID string) (DomainCertificateOrder, error) {
	var out DomainCertificateOrder
	err := s.Pool.QueryRow(ctx, `
		SELECT domain_id, project_id, environment_id, service_id, issuer,
		       issuer_order_ref, certificate_ref, state, attempts, last_error,
		       expires_at, created_at, updated_at
		  FROM domain_certificate_orders
		 WHERE domain_id = $1`, domainID,
	).Scan(
		&out.DomainID, &out.ProjectID, &out.EnvironmentID, &out.ServiceID,
		&out.Issuer, &out.IssuerOrderRef, &out.CertificateRef, &out.State,
		&out.Attempts, &out.LastError, &out.ExpiresAt, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainCertificateOrder{}, ErrDurableRouteNotFound
	}
	if err != nil {
		return DomainCertificateOrder{}, fmt.Errorf("get domain certificate order: %w", err)
	}
	return out, nil
}

func (s *PostgresDurableRouteStore) ListPendingDomainLifecycles(ctx context.Context) ([]DomainLifecycleRecord, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, project_id, environment_id, service_id, kind,
		       hostname, normalized_hostname, apps_domain, tls_desired,
		       tls_state, desired_revision, created_at, updated_at
		  FROM domains
		 WHERE (tls_desired = TRUE AND (tls_state <> 'ready' OR EXISTS (
				SELECT 1 FROM domain_certificate_orders renewal
				 WHERE renewal.domain_id = domains.id
				   AND renewal.expires_at IS NOT NULL
				   AND renewal.expires_at <= CURRENT_TIMESTAMP + INTERVAL '30 days'
			)))
		    OR (kind = 'custom' AND EXISTS (
				SELECT 1 FROM domain_dns_challenges dc
				 WHERE dc.domain_id = domains.id AND dc.state <> 'verified'
			))
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list pending domain lifecycles: %w", err)
	}
	defer rows.Close()
	var out []DomainLifecycleRecord
	for rows.Next() {
		domainValue, err := scanDurableDomain(rows)
		if err != nil {
			return nil, err
		}
		value := DomainLifecycleRecord{Domain: domainValue}
		challenge, challengeErr := s.getDomainDNSChallenge(ctx, domainValue.ID)
		if challengeErr == nil {
			value.Challenge = &challenge
		} else if !errors.Is(challengeErr, ErrDurableRouteNotFound) {
			return nil, challengeErr
		}
		order, orderErr := s.getDomainCertificateOrder(ctx, domainValue.ID)
		if orderErr == nil {
			value.Certificate = &order
		} else if !errors.Is(orderErr, ErrDurableRouteNotFound) {
			return nil, orderErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *PostgresDurableRouteStore) RecordDNSChallengeObservation(ctx context.Context, value DomainDNSChallenge) error {
	observed, err := json.Marshal(value.ObservedValues)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE domain_dns_challenges
		   SET state = $1, observed_values = $2, attempts = $3,
		       last_error = $4, checked_at = $5,
		       verified_at = CASE WHEN $1 = 'verified' THEN COALESCE(verified_at, $5) ELSE NULL END,
		       updated_at = $6
		 WHERE domain_id = $7`,
		string(value.State), observed, value.Attempts, value.LastError,
		value.CheckedAt, s.now(), value.DomainID,
	)
	if err != nil {
		return fmt.Errorf("record DNS challenge observation: %w", err)
	}
	return nil
}

func (s *PostgresDurableRouteStore) RecordCertificateOrder(ctx context.Context, value DomainCertificateOrder) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO domain_certificate_orders (
			domain_id, project_id, environment_id, service_id, issuer,
			issuer_order_ref, certificate_ref, state, attempts, last_error,
			expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		ON CONFLICT (domain_id)
		DO UPDATE SET issuer = EXCLUDED.issuer,
			issuer_order_ref = EXCLUDED.issuer_order_ref,
			certificate_ref = EXCLUDED.certificate_ref,
			state = EXCLUDED.state, attempts = EXCLUDED.attempts,
			last_error = EXCLUDED.last_error, expires_at = EXCLUDED.expires_at,
			updated_at = EXCLUDED.updated_at`,
		value.DomainID, value.ProjectID, value.EnvironmentID, value.ServiceID,
		value.Issuer, value.IssuerOrderRef, value.CertificateRef,
		string(value.State), value.Attempts, value.LastError, value.ExpiresAt, s.now(),
	)
	if err != nil {
		return fmt.Errorf("record certificate order: %w", err)
	}
	return nil
}

func (s *PostgresDurableRouteStore) MarkDomainTLSReady(ctx context.Context, domainID, certificateRef string, expiresAt time.Time) error {
	if strings.TrimSpace(certificateRef) == "" || expiresAt.IsZero() {
		return fmt.Errorf("%w: ready TLS requires certificate reference and expiry evidence", ErrDurableRouteInvalid)
	}
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		scopeValue, exists, err := getDomainTx(ctx, tx, domainID, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrDurableRouteNotFound
		}
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			scopeValue.ProjectID,
			scopeValue.EnvironmentID,
		); err != nil {
			return err
		}
		domainValue, exists, err := getDomainTx(ctx, tx, domainID, true)
		if err != nil {
			return err
		}
		if !exists {
			return ErrDurableRouteNotFound
		}
		revision, err := bumpRouteGroupRevision(ctx, tx, domainValue.ProjectID, domainValue.EnvironmentID, s.now())
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE domains SET tls_state = 'ready', desired_revision = $1, updated_at = $2
			 WHERE id = $3 AND tls_desired = TRUE`,
			revision, s.now(), domainID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: domain does not desire TLS", ErrDurableRouteInvalid)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark domain TLS ready: %w", err)
	}
	return nil
}

// PutHTTPRoute preserves private-by-default semantics: callers must set Public
// true explicitly on durable intent before the reconciler can render it.
func (s *PostgresDurableRouteStore) PutHTTPRoute(ctx context.Context, value DurableHTTPRoute) (DurableHTTPRoute, error) {
	if s == nil || s.Pool == nil {
		return DurableHTTPRoute{}, errors.New("postgres durable route store pool is required")
	}
	var out DurableHTTPRoute
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var err error
		out, err = s.PutHTTPRouteInTx(ctx, tx, value)
		return err
	})
	if err != nil {
		return DurableHTTPRoute{}, fmt.Errorf("put durable HTTP route: %w", err)
	}
	return out, nil
}

// PutHTTPRouteInTx creates or converges one durable HTTP route inside the
// caller's transaction.
func (s *PostgresDurableRouteStore) PutHTTPRouteInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value DurableHTTPRoute,
) (DurableHTTPRoute, error) {
	if s == nil || tx == nil {
		return DurableHTTPRoute{}, errors.New("postgres durable route transaction is required")
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		value.ProjectID,
		value.EnvironmentID,
	); err != nil {
		return DurableHTTPRoute{}, err
	}
	domainValue, exists, err := getDomainTx(ctx, tx, value.DomainID, true)
	if err != nil {
		return DurableHTTPRoute{}, err
	}
	if !exists {
		return DurableHTTPRoute{}, ErrDurableRouteNotFound
	}
	if domainValue.RouteScope != value.RouteScope {
		return DurableHTTPRoute{}, ErrDurableRouteScopeMismatch
	}
	value, err = validateHTTPRouteIntent(value, domainValue.NormalizedHostname)
	if err != nil {
		return DurableHTTPRoute{}, err
	}
	current, currentExists, err := getHTTPRouteTx(ctx, tx, value.ID, true)
	if err != nil {
		return DurableHTTPRoute{}, err
	}
	now := s.now()
	if currentExists {
		if current.DomainID != value.DomainID || current.RouteScope != value.RouteScope {
			return DurableHTTPRoute{}, ErrDurableRouteScopeMismatch
		}
		if current.Name == value.Name &&
			current.PathPrefix == value.PathPrefix &&
			current.TargetPort == value.TargetPort &&
			current.Public == value.Public {
			return current, nil
		}
		revision, err := bumpRouteGroupRevision(ctx, tx, value.ProjectID, value.EnvironmentID, now)
		if err != nil {
			return DurableHTTPRoute{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE domains
			   SET resource_version = resource_version + 1,
			       updated_at = $2
			 WHERE id = $1`,
			value.DomainID,
			now,
		); err != nil {
			return DurableHTTPRoute{}, fmt.Errorf(
				"advance owning domain resource version: %w",
				err,
			)
		}
		out, err := scanDurableHTTPRoute(tx.QueryRow(ctx, `
			UPDATE http_routes
			   SET name = $1, path_prefix = $2, target_port = $3,
			       public_enabled = $4, desired_revision = $5, updated_at = $6
			 WHERE id = $7
			RETURNING id, domain_id, project_id, environment_id, service_id,
			          name, path_prefix, target_port, public_enabled,
			          desired_revision, created_at, updated_at`,
			value.Name, value.PathPrefix, value.TargetPort, value.Public,
			revision, now, value.ID,
		))
		return out, err
	}
	revision, err := bumpRouteGroupRevision(ctx, tx, value.ProjectID, value.EnvironmentID, now)
	if err != nil {
		return DurableHTTPRoute{}, err
	}
	out, err := scanDurableHTTPRoute(tx.QueryRow(ctx, `
		INSERT INTO http_routes (
			id, domain_id, project_id, environment_id, service_id, name,
			path_prefix, target_port, public_enabled, desired_revision,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		RETURNING id, domain_id, project_id, environment_id, service_id,
		          name, path_prefix, target_port, public_enabled,
		          desired_revision, created_at, updated_at`,
		value.ID, value.DomainID, value.ProjectID, value.EnvironmentID,
		value.ServiceID, value.Name, value.PathPrefix, value.TargetPort,
		value.Public, revision, now,
	))
	if err != nil && isRouteUniqueViolation(err) {
		return DurableHTTPRoute{}, fmt.Errorf("%w: hostname/path route already exists", ErrDurableRouteInvalid)
	}
	return out, err
}

// BindServiceDomainInTx marks one exact generated or custom route as a
// service-managed product domain. Only this explicit binding is inherited by
// deployments; durable routes created through recovery APIs are not. Generated
// routes must already be public. Custom routes may bind while private, but
// cannot become eligible for deployment until ownership is verified, or for
// publication until requested TLS is also ready.
func (s *PostgresDurableRouteStore) BindServiceDomainInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	domainID string,
	routeID string,
	scope RouteScope,
	createdAt time.Time,
) error {
	if s == nil || tx == nil {
		return errors.New("postgres durable route transaction is required")
	}
	domainID = strings.TrimSpace(domainID)
	routeID = strings.TrimSpace(routeID)
	if domainID == "" || routeID == "" {
		return fmt.Errorf("%w: domain and route ids are required", ErrDurableRouteInvalid)
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		scope.ProjectID,
		scope.EnvironmentID,
	); err != nil {
		return err
	}
	if createdAt.IsZero() {
		createdAt = s.now()
	} else {
		createdAt = createdAt.UTC()
	}
	if err := validateServiceDomainBindingTx(ctx, tx, domainID, routeID, scope); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO service_domain_bindings (
			route_id, domain_id, project_id, environment_id, service_id,
			created_at
		) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (route_id) DO NOTHING`,
		routeID,
		domainID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
		createdAt,
	)
	if err != nil {
		if isRouteUniqueViolation(err) {
			return fmt.Errorf("%w: generated domain already has a service binding", ErrDurableRouteInvalid)
		}
		return fmt.Errorf("bind service generated domain: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var currentDomainID, projectID, environmentID, serviceID string
	err = tx.QueryRow(ctx, `
		SELECT domain_id, project_id, environment_id, service_id
		  FROM service_domain_bindings
		 WHERE route_id = $1
		 FOR UPDATE`,
		routeID,
	).Scan(&currentDomainID, &projectID, &environmentID, &serviceID)
	if err != nil {
		return fmt.Errorf("load service generated domain binding: %w", err)
	}
	if currentDomainID != domainID ||
		(RouteScope{
			ProjectID:     projectID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
		}) != scope {
		return ErrDurableRouteScopeMismatch
	}
	return nil
}

// BindServiceGeneratedRoutesToDeploymentInTx copies every currently eligible
// service-managed generated route to one exact canonical deployment. The
// returned count is the total matching deployment bindings after the
// idempotent insert, not merely the number newly inserted by this call.
func (s *PostgresDurableRouteStore) BindServiceGeneratedRoutesToDeploymentInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	deploymentID string,
	scope RouteScope,
	createdAt time.Time,
) (int64, error) {
	if s == nil || tx == nil {
		return 0, errors.New("postgres durable route transaction is required")
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return 0, fmt.Errorf("%w: deployment id is required", ErrDurableRouteInvalid)
	}
	if err := scope.validate(); err != nil {
		return 0, err
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		scope.ProjectID,
		scope.EnvironmentID,
	); err != nil {
		return 0, err
	}
	if createdAt.IsZero() {
		createdAt = s.now()
	} else {
		createdAt = createdAt.UTC()
	}
	var deploymentExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM deployments
			 WHERE id = $1
			   AND project_id = $2
			   AND environment_id = $3
			   AND service_id = $4
		)`,
		deploymentID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	).Scan(&deploymentExists); err != nil {
		return 0, fmt.Errorf("validate generated route deployment scope: %w", err)
	}
	if !deploymentExists {
		return 0, ErrDurableRouteNotFound
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id, route_id, domain_id, project_id,
			environment_id, service_id, created_at
		)
		SELECT $1, b.route_id, b.domain_id, b.project_id,
		       b.environment_id, b.service_id, $5
		  FROM service_domain_bindings b
		  JOIN http_routes r
		    ON r.id = b.route_id
		   AND r.domain_id = b.domain_id
		   AND r.project_id = b.project_id
		   AND r.environment_id = b.environment_id
		   AND r.service_id = b.service_id
		  JOIN domains d
		    ON d.id = b.domain_id
		   AND d.project_id = b.project_id
		   AND d.environment_id = b.environment_id
		   AND d.service_id = b.service_id
		 WHERE b.project_id = $2
		   AND b.environment_id = $3
		   AND b.service_id = $4
		   AND `+ManagedServiceRouteEligibilityPredicateSQL+`
		ON CONFLICT (deployment_id, route_id) DO NOTHING`,
		deploymentID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
		createdAt,
	); err != nil {
		return 0, fmt.Errorf("bind service generated routes to deployment: %w", err)
	}
	var count int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM deployment_route_intents i
		  JOIN service_domain_bindings b
		    ON b.route_id = i.route_id
		   AND b.domain_id = i.domain_id
		   AND b.project_id = i.project_id
		   AND b.environment_id = i.environment_id
		   AND b.service_id = i.service_id
		  JOIN http_routes r
		    ON r.id = b.route_id
		   AND r.domain_id = b.domain_id
		   AND r.project_id = b.project_id
		   AND r.environment_id = b.environment_id
		   AND r.service_id = b.service_id
		  JOIN domains d
		    ON d.id = b.domain_id
		   AND d.project_id = b.project_id
		   AND d.environment_id = b.environment_id
		   AND d.service_id = b.service_id
		 WHERE i.deployment_id = $1
		   AND i.project_id = $2
		   AND i.environment_id = $3
		   AND i.service_id = $4
		   AND `+ManagedServiceRouteEligibilityPredicateSQL,
		deploymentID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count deployment generated route bindings: %w", err)
	}
	return count, nil
}

func validateServiceDomainBindingTx(
	ctx context.Context,
	tx *dbpg.Tx,
	domainID string,
	routeID string,
	scope RouteScope,
) error {
	routeValue, exists, err := getHTTPRouteTx(ctx, tx, routeID, true)
	if err != nil {
		return err
	}
	if !exists {
		return ErrDurableRouteNotFound
	}
	domainValue, exists, err := getDomainTx(ctx, tx, domainID, true)
	if err != nil {
		return err
	}
	if !exists {
		return ErrDurableRouteNotFound
	}
	if routeValue.DomainID != domainID ||
		routeValue.RouteScope != scope ||
		domainValue.RouteScope != scope {
		return ErrDurableRouteScopeMismatch
	}
	if !domainValue.TLSDesired && domainValue.TLSState != DomainTLSDisabled {
		return fmt.Errorf(
			"%w: service domains must be TLS-disabled",
			ErrDurableRouteInvalid,
		)
	}
	if domainValue.TLSDesired && domainValue.Kind != DomainKindCustom && domainValue.TLSState != DomainTLSReady {
		return fmt.Errorf(
			"%w: generated service domains require ready TLS evidence",
			ErrDurableRouteInvalid,
		)
	}
	if domainValue.Kind == DomainKindGenerated && !routeValue.Public {
		return fmt.Errorf("%w: generated service route must be public", ErrDurableRouteInvalid)
	}
	if domainValue.Kind == DomainKindCustom && routeValue.Public {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM domain_dns_challenges WHERE domain_id=$1 FOR SHARE`, domainID).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: custom domain has no ownership challenge", ErrDurableRouteInvalid)
			}
			return fmt.Errorf("load custom ownership challenge: %w", err)
		}
		if state != "verified" {
			return fmt.Errorf("%w: custom domain ownership is not verified", ErrDurableRouteUnverified)
		}
	}
	if domainValue.Kind != DomainKindGenerated && domainValue.Kind != DomainKindCustom {
		return fmt.Errorf("%w: unsupported service domain kind", ErrDurableRouteInvalid)
	}
	return nil
}

// BindDeploymentRouteIntentInTx records that one exact canonical deployment
// requested one generated route. The deployment row must already exist in the
// caller transaction; immutable scope FKs prevent cross-tenant association.
func (s *PostgresDurableRouteStore) BindDeploymentRouteIntentInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	deploymentID string,
	domainID string,
	routeID string,
	scope RouteScope,
	createdAt time.Time,
) error {
	if s == nil || tx == nil {
		return errors.New("postgres durable route transaction is required")
	}
	deploymentID = strings.TrimSpace(deploymentID)
	domainID = strings.TrimSpace(domainID)
	routeID = strings.TrimSpace(routeID)
	if deploymentID == "" || domainID == "" || routeID == "" {
		return fmt.Errorf("%w: deployment, domain, and route ids are required", ErrDurableRouteInvalid)
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if err := AcquireRouteEnvironmentMutationLock(
		ctx,
		tx,
		scope.ProjectID,
		scope.EnvironmentID,
	); err != nil {
		return err
	}
	if createdAt.IsZero() {
		createdAt = s.now()
	} else {
		createdAt = createdAt.UTC()
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id, route_id, domain_id, project_id,
			environment_id, service_id, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (deployment_id, route_id) DO NOTHING`,
		deploymentID,
		routeID,
		domainID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("bind deployment route intent: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var currentRouteID, currentDomainID, projectID, environmentID, serviceID string
	err = tx.QueryRow(ctx, `
		SELECT route_id, domain_id, project_id, environment_id, service_id
		  FROM deployment_route_intents
		 WHERE deployment_id = $1
		   AND route_id = $2
		 FOR UPDATE`,
		deploymentID,
		routeID,
	).Scan(
		&currentRouteID,
		&currentDomainID,
		&projectID,
		&environmentID,
		&serviceID,
	)
	if err != nil {
		return fmt.Errorf("load deployment route intent: %w", err)
	}
	if currentRouteID != routeID ||
		currentDomainID != domainID ||
		(RouteScope{
			ProjectID:     projectID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
		}) != scope {
		return ErrDurableRouteScopeMismatch
	}
	return nil
}

func (s *PostgresDurableRouteStore) GetHTTPRoute(ctx context.Context, id string) (DurableHTTPRoute, error) {
	if s == nil || s.Pool == nil {
		return DurableHTTPRoute{}, errors.New("postgres durable route store pool is required")
	}
	out, err := scanDurableHTTPRoute(s.Pool.QueryRow(ctx, `
		SELECT id, domain_id, project_id, environment_id, service_id,
		       name, path_prefix, target_port, public_enabled,
		       desired_revision, created_at, updated_at
		  FROM http_routes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableHTTPRoute{}, ErrDurableRouteNotFound
	}
	if err != nil {
		return DurableHTTPRoute{}, fmt.Errorf("get durable HTTP route: %w", err)
	}
	return out, nil
}

func (s *PostgresDurableRouteStore) DeleteHTTPRoute(ctx context.Context, id string) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		scopeValue, exists, err := getHTTPRouteTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			scopeValue.ProjectID,
			scopeValue.EnvironmentID,
		); err != nil {
			return err
		}
		current, exists, err := getHTTPRouteTx(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if _, err := bumpRouteGroupRevision(ctx, tx, current.ProjectID, current.EnvironmentID, s.now()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM http_routes WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete durable HTTP route: %w", err)
	}
	return nil
}

// ReplaceRouteBackends is idempotent for an identical sorted backend set. It
// persists both ready and not-ready observations; only ready rows are rendered.
func (s *PostgresDurableRouteStore) ReplaceRouteBackends(ctx context.Context, routeID string, backends []DurableRouteBackend) ([]DurableRouteBackend, error) {
	out, _, err := s.replaceRouteBackends(ctx, routeID, nil, backends)
	return out, err
}

// ReplaceRouteBackendsCAS mutates a route only while its environment route
// group remains at expectedRevision. It returns the revision owned by the
// resulting backend set. Promotion rollback uses this token so it can never
// overwrite a concurrent route winner.
func (s *PostgresDurableRouteStore) ReplaceRouteBackendsCAS(ctx context.Context, routeID string, expectedRevision int64, backends []DurableRouteBackend) ([]DurableRouteBackend, int64, error) {
	return s.replaceRouteBackends(ctx, routeID, &expectedRevision, backends)
}

// ReplaceRouteGroupBackendsCAS replaces every supplied route backend set in one
// transaction and advances the environment group exactly once. It is a strict
// optimistic write: any group movement rejects the complete replacement.
func (s *PostgresDurableRouteStore) ReplaceRouteGroupBackendsCAS(
	ctx context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []DurableRouteBackendSet,
) ([]DurableRouteBackendSet, int64, error) {
	return s.replaceRouteGroupBackendsCAS(
		ctx,
		projectID,
		environmentID,
		expectedRevision,
		sets,
		false,
		false,
		false,
	)
}

// ReplaceEligibleRouteGroupBackendsCAS is the promotion-stage/commit boundary.
// In addition to strict group CAS, it locks and revalidates every affected
// route's current public and domain lifecycle state.
func (s *PostgresDurableRouteStore) ReplaceEligibleRouteGroupBackendsCAS(
	ctx context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []DurableRouteBackendSet,
) ([]DurableRouteBackendSet, int64, error) {
	return s.replaceRouteGroupBackendsCAS(
		ctx,
		projectID,
		environmentID,
		expectedRevision,
		sets,
		false,
		true,
		false,
	)
}

// RestoreEligibleRouteGroupBackendsWithOwnershipCAS is only for compensating a
// promotion write. A moved group is accepted solely when every current,
// non-empty affected backend set still carries the caller's expected revision.
// Current route/domain eligibility is revalidated before restoration.
func (s *PostgresDurableRouteStore) RestoreEligibleRouteGroupBackendsWithOwnershipCAS(
	ctx context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []DurableRouteBackendSet,
) ([]DurableRouteBackendSet, int64, error) {
	return s.replaceRouteGroupBackendsCAS(
		ctx,
		projectID,
		environmentID,
		expectedRevision,
		sets,
		true,
		true,
		true,
	)
}

func (s *PostgresDurableRouteStore) replaceRouteGroupBackendsCAS(
	ctx context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []DurableRouteBackendSet,
	allowOwnedMovedRevision bool,
	requireEligiblePublicRoute bool,
	restoreCapturedBackendSet bool,
) ([]DurableRouteBackendSet, int64, error) {
	if s == nil || s.Pool == nil {
		return nil, 0, errors.New("postgres durable route store pool is required")
	}
	projectID = strings.TrimSpace(projectID)
	environmentID = strings.TrimSpace(environmentID)
	if projectID == "" || environmentID == "" {
		return nil, 0, fmt.Errorf(
			"%w: project and environment are required",
			ErrDurableRouteInvalid,
		)
	}
	if expectedRevision < 1 {
		return nil, 0, fmt.Errorf(
			"%w: expected revision must be positive",
			ErrDurableRouteInvalid,
		)
	}
	normalizedSets, err := normalizeDurableRouteBackendSets(sets)
	if err != nil {
		return nil, 0, err
	}

	var out []DurableRouteBackendSet
	var outRevision int64
	err = s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			projectID,
			environmentID,
		); err != nil {
			return err
		}
		var currentRevision int64
		err := tx.QueryRow(ctx, `
			SELECT desired_revision
			  FROM route_controller_groups
			 WHERE project_id = $1
			   AND environment_id = $2
			 FOR UPDATE`,
			projectID,
			environmentID,
		).Scan(&currentRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDurableRouteNotFound
		}
		if err != nil {
			return err
		}
		if currentRevision != expectedRevision && !allowOwnedMovedRevision {
			return fmt.Errorf(
				"%w: expected revision %d, found group %d",
				ErrDurableRouteRevisionMoved,
				expectedRevision,
				currentRevision,
			)
		}
		if len(normalizedSets) == 0 {
			if currentRevision != expectedRevision {
				return fmt.Errorf(
					"%w: expected revision %d, found group %d and no owned backend sets",
					ErrDurableRouteRevisionMoved,
					expectedRevision,
					currentRevision,
				)
			}
			out = []DurableRouteBackendSet{}
			outRevision = currentRevision
			return nil
		}

		type routeBackendSetState struct {
			routeValue DurableHTTPRoute
			current    []DurableRouteBackend
			desired    []DurableRouteBackend
		}
		states := make([]routeBackendSetState, len(normalizedSets))
		seenBackendIDs := make(map[string]struct{})
		for index := range normalizedSets {
			routeValue, exists, err := getHTTPRouteTx(
				ctx,
				tx,
				normalizedSets[index].RouteID,
				true,
			)
			if err != nil {
				return err
			}
			if !exists {
				return ErrDurableRouteNotFound
			}
			if routeValue.ProjectID != projectID ||
				routeValue.EnvironmentID != environmentID {
				return ErrDurableRouteScopeMismatch
			}
			domainValue, exists, err := getDomainTx(
				ctx,
				tx,
				routeValue.DomainID,
				requireEligiblePublicRoute,
			)
			if err != nil {
				return err
			}
			if !exists {
				return ErrDurableRouteNotFound
			}
			if requireEligiblePublicRoute {
				if err := validateEligiblePublicRouteState(routeValue, domainValue); err != nil {
					return err
				}
				if domainValue.Kind == DomainKindCustom {
					var ownershipState string
					if err := tx.QueryRow(ctx, `SELECT state FROM domain_dns_challenges WHERE domain_id=$1`, domainValue.ID).Scan(&ownershipState); err != nil {
						if errors.Is(err, pgx.ErrNoRows) {
							return fmt.Errorf("%w: custom route has no ownership evidence", ErrDurableRouteUnverified)
						}
						return fmt.Errorf("load custom route ownership evidence: %w", err)
					}
					if ownershipState != "verified" {
						return fmt.Errorf("%w: custom route ownership is not verified", ErrDurableRouteUnverified)
					}
				}
			}
			current, err := listRouteBackendsTx(ctx, tx, routeValue.ID)
			if err != nil {
				return err
			}
			desired := normalizedSets[index].Backends
			sort.Slice(desired, func(i, j int) bool {
				return desired[i].ID < desired[j].ID
			})
			seenRouteInstances := make(map[string]struct{}, len(desired))
			for backendIndex := range desired {
				backend := &desired[backendIndex]
				backend.ID = strings.TrimSpace(backend.ID)
				backend.DeploymentID = strings.TrimSpace(backend.DeploymentID)
				backend.InstanceID = strings.TrimSpace(backend.InstanceID)
				if backend.ID == "" ||
					backend.DeploymentID == "" ||
					backend.InstanceID == "" {
					return fmt.Errorf(
						"%w: backend, deployment, and instance ids are required",
						ErrDurableRouteInvalid,
					)
				}
				if _, duplicate := seenBackendIDs[backend.ID]; duplicate {
					return fmt.Errorf(
						"%w: duplicate backend id across route group",
						ErrDurableRouteInvalid,
					)
				}
				seenBackendIDs[backend.ID] = struct{}{}
				instanceKey := backend.DeploymentID + "\x00" + backend.InstanceID
				if _, duplicate := seenRouteInstances[instanceKey]; duplicate {
					return fmt.Errorf(
						"%w: duplicate deployment instance in route backend set",
						ErrDurableRouteInvalid,
					)
				}
				seenRouteInstances[instanceKey] = struct{}{}
				if backend.RouteScope != routeValue.RouteScope {
					return ErrDurableRouteScopeMismatch
				}
				backend.RouteID = routeValue.ID
				if backend.ObservedAt.IsZero() {
					backend.ObservedAt = s.now()
				} else {
					backend.ObservedAt = backend.ObservedAt.UTC()
				}
				allowHistoricalPort := allowHistoricalBackendPort(
					*backend,
					current,
					expectedRevision,
					restoreCapturedBackendSet,
				)
				if err := validateBackendURLTarget(
					routeValue,
					domainValue,
					backend.ServiceURL,
					allowHistoricalPort,
				); err != nil {
					return err
				}
			}
			states[index] = routeBackendSetState{
				routeValue: routeValue,
				current:    current,
				desired:    desired,
			}
		}

		if currentRevision != expectedRevision {
			// Compensation after a moved group is allowed only for the writer
			// that still owns every affected set. Empty sets have no durable
			// stamp and therefore cannot prove ownership.
			currentSets := make([]DurableRouteBackendSet, len(states))
			for index, state := range states {
				currentSets[index] = DurableRouteBackendSet{
					RouteID:  state.routeValue.ID,
					Backends: state.current,
				}
			}
			owned, err := durableRouteBackendSetsOwnRevision(
				currentSets,
				expectedRevision,
			)
			if err != nil {
				return err
			}
			if !owned {
				return fmt.Errorf(
					"%w: expected revision %d, found group %d and at least one unowned backend set",
					ErrDurableRouteRevisionMoved,
					expectedRevision,
					currentRevision,
				)
			}
		}

		equivalent := true
		out = make([]DurableRouteBackendSet, len(states))
		for index, state := range states {
			if !equivalentBackends(state.current, state.desired) {
				equivalent = false
			}
			out[index] = DurableRouteBackendSet{
				RouteID:  state.routeValue.ID,
				Backends: append([]DurableRouteBackend(nil), state.current...),
			}
		}
		if equivalent {
			if currentRevision == expectedRevision {
				outRevision = currentRevision
			} else {
				outRevision = expectedRevision
			}
			return nil
		}

		revision, err := bumpRouteGroupRevision(
			ctx,
			tx,
			projectID,
			environmentID,
			s.now(),
		)
		if err != nil {
			return err
		}
		out = make([]DurableRouteBackendSet, len(states))
		for index, state := range states {
			if _, err := tx.Exec(
				ctx,
				`DELETE FROM route_backends WHERE route_id = $1`,
				state.routeValue.ID,
			); err != nil {
				return err
			}
			stamped := make([]DurableRouteBackend, len(state.desired))
			for backendIndex := range state.desired {
				backend := state.desired[backendIndex]
				backend.DesiredRevision = revision
				if _, err := tx.Exec(ctx, `
					INSERT INTO route_backends (
						id, route_id, project_id, environment_id, service_id,
						deployment_id, instance_id, service_url, ready,
						desired_revision, observed_at
					) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
					backend.ID,
					state.routeValue.ID,
					backend.ProjectID,
					backend.EnvironmentID,
					backend.ServiceID,
					backend.DeploymentID,
					backend.InstanceID,
					backend.ServiceURL,
					backend.Ready,
					backend.DesiredRevision,
					backend.ObservedAt,
				); err != nil {
					return err
				}
				stamped[backendIndex] = backend
			}
			out[index] = DurableRouteBackendSet{
				RouteID:  state.routeValue.ID,
				Backends: stamped,
			}
		}
		outRevision = revision
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("replace durable route group backends: %w", err)
	}
	return out, outRevision, nil
}

func normalizeDurableRouteBackendSets(
	sets []DurableRouteBackendSet,
) ([]DurableRouteBackendSet, error) {
	normalized := make([]DurableRouteBackendSet, len(sets))
	for index := range sets {
		normalized[index] = DurableRouteBackendSet{
			RouteID:  strings.TrimSpace(sets[index].RouteID),
			Backends: append([]DurableRouteBackend(nil), sets[index].Backends...),
		}
		if normalized[index].RouteID == "" {
			return nil, fmt.Errorf(
				"%w: route id is required for every backend set",
				ErrDurableRouteInvalid,
			)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].RouteID < normalized[j].RouteID
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].RouteID == normalized[index].RouteID {
			return nil, fmt.Errorf(
				"%w: duplicate route backend set",
				ErrDurableRouteInvalid,
			)
		}
	}
	return normalized, nil
}

func validateEligiblePublicRouteState(
	routeValue DurableHTTPRoute,
	domainValue DurableDomain,
) error {
	if routeValue.DomainID != domainValue.ID ||
		routeValue.RouteScope != domainValue.RouteScope {
		return ErrDurableRouteScopeMismatch
	}
	if !routeValue.Public {
		return fmt.Errorf(
			"%w: route %s is no longer public",
			ErrDurableRouteInvalid,
			routeValue.ID,
		)
	}
	switch domainValue.Kind {
	case DomainKindGenerated, DomainKindCustom:
	default:
		return fmt.Errorf(
			"%w: route %s has an unsupported domain kind",
			ErrDurableRouteInvalid,
			routeValue.ID,
		)
	}
	if domainValue.TLSDesired {
		if domainValue.TLSState != DomainTLSReady {
			return fmt.Errorf(
				"%w: route %s requires TLS without ready certificate evidence",
				ErrDurableRouteInvalid,
				routeValue.ID,
			)
		}
		return nil
	}
	if domainValue.TLSState != DomainTLSDisabled {
		return fmt.Errorf(
			"%w: route %s has inconsistent disabled TLS lifecycle state",
			ErrDurableRouteInvalid,
			routeValue.ID,
		)
	}
	return nil
}

func durableRouteBackendSetsOwnRevision(
	sets []DurableRouteBackendSet,
	expectedRevision int64,
) (bool, error) {
	if len(sets) == 0 || expectedRevision < 1 {
		return false, nil
	}
	for _, set := range sets {
		revision, err := durableBackendSetRevision(set.Backends)
		if err != nil {
			return false, err
		}
		if revision != expectedRevision {
			return false, nil
		}
	}
	return true, nil
}

func (s *PostgresDurableRouteStore) replaceRouteBackends(ctx context.Context, routeID string, expectedRevision *int64, backends []DurableRouteBackend) ([]DurableRouteBackend, int64, error) {
	if s == nil || s.Pool == nil {
		return nil, 0, errors.New("postgres durable route store pool is required")
	}
	var out []DurableRouteBackend
	var outRevision int64
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		scopeValue, exists, err := getHTTPRouteTx(ctx, tx, routeID, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrDurableRouteNotFound
		}
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			scopeValue.ProjectID,
			scopeValue.EnvironmentID,
		); err != nil {
			return err
		}
		routeValue, exists, err := getHTTPRouteTx(ctx, tx, routeID, true)
		if err != nil {
			return err
		}
		if !exists {
			return ErrDurableRouteNotFound
		}
		var currentRevision int64
		if err := tx.QueryRow(ctx, `SELECT desired_revision FROM route_controller_groups WHERE project_id=$1 AND environment_id=$2 FOR UPDATE`, routeValue.ProjectID, routeValue.EnvironmentID).Scan(&currentRevision); err != nil {
			return err
		}
		domainValue, exists, err := getDomainTx(ctx, tx, routeValue.DomainID, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrDurableRouteNotFound
		}
		normalized := append([]DurableRouteBackend(nil), backends...)
		sort.Slice(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
		seenIDs := make(map[string]struct{}, len(normalized))
		for index := range normalized {
			backend := &normalized[index]
			if strings.TrimSpace(backend.ID) == "" ||
				strings.TrimSpace(backend.DeploymentID) == "" ||
				strings.TrimSpace(backend.InstanceID) == "" {
				return fmt.Errorf("%w: backend, deployment, and instance ids are required", ErrDurableRouteInvalid)
			}
			if _, duplicate := seenIDs[backend.ID]; duplicate {
				return fmt.Errorf("%w: duplicate backend id", ErrDurableRouteInvalid)
			}
			seenIDs[backend.ID] = struct{}{}
			if backend.RouteScope != routeValue.RouteScope {
				return ErrDurableRouteScopeMismatch
			}
			backend.RouteID = routeID
			if backend.ObservedAt.IsZero() {
				backend.ObservedAt = s.now()
			}
			if err := validateBackendURL(routeValue, domainValue, backend.ServiceURL); err != nil {
				return err
			}
		}
		current, err := listRouteBackendsTx(ctx, tx, routeID)
		if err != nil {
			return err
		}
		backendRevision, err := durableBackendSetRevision(current)
		if err != nil {
			return err
		}
		// The group revision authorizes the first write after LoadRouteGroup.
		// Once this caller has written a backend set, that set's stamped
		// revision remains its ownership token even if an unrelated route or
		// domain advances the group. A same-route writer necessarily replaces
		// the rows (or clears them), so it cannot satisfy this check.
		if expectedRevision != nil &&
			currentRevision != *expectedRevision &&
			backendRevision != *expectedRevision {
			return fmt.Errorf("%w: expected revision %d, found group %d and backend set %d", ErrDurableRouteRevisionMoved, *expectedRevision, currentRevision, backendRevision)
		}
		if equivalentBackends(current, normalized) {
			out = current
			outRevision = backendRevision
			if outRevision == 0 {
				outRevision = currentRevision
			}
			return nil
		}
		now := s.now()
		revision, err := bumpRouteGroupRevision(ctx, tx, routeValue.ProjectID, routeValue.EnvironmentID, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM route_backends WHERE route_id = $1`, routeID); err != nil {
			return err
		}
		for index := range normalized {
			normalized[index].DesiredRevision = revision
			_, err := tx.Exec(ctx, `
				INSERT INTO route_backends (
					id, route_id, project_id, environment_id, service_id,
					deployment_id, instance_id, service_url, ready,
					desired_revision, observed_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				normalized[index].ID, routeID, normalized[index].ProjectID,
				normalized[index].EnvironmentID, normalized[index].ServiceID,
				normalized[index].DeploymentID, normalized[index].InstanceID,
				normalized[index].ServiceURL, normalized[index].Ready,
				revision, normalized[index].ObservedAt,
			)
			if err != nil {
				return err
			}
		}
		out = normalized
		outRevision = revision
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("replace durable route backends: %w", err)
	}
	return out, outRevision, nil
}

func durableBackendSetRevision(backends []DurableRouteBackend) (int64, error) {
	if len(backends) == 0 {
		return 0, nil
	}
	revision := backends[0].DesiredRevision
	if revision < 1 {
		return 0, fmt.Errorf("%w: backend set has no durable revision", ErrDurableRouteInvalid)
	}
	for _, backend := range backends[1:] {
		if backend.DesiredRevision != revision {
			return 0, fmt.Errorf("%w: backend set spans revisions %d and %d", ErrDurableRouteInvalid, revision, backend.DesiredRevision)
		}
	}
	return revision, nil
}

func (s *PostgresDurableRouteStore) ListRouteGroups(ctx context.Context) ([]DurableRouteGroup, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres durable route store pool is required")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT route_group, project_id, environment_id, desired_revision,
		       observed_revision, applied_revision, apply_state, artifact_hash,
		       last_error_code, last_error_detail
		  FROM route_controller_groups
		 ORDER BY project_id, environment_id`)
	if err != nil {
		return nil, fmt.Errorf("list durable route groups: %w", err)
	}
	defer rows.Close()
	var groups []DurableRouteGroup
	for rows.Next() {
		var group DurableRouteGroup
		if err := rows.Scan(
			&group.RouteGroup, &group.ProjectID, &group.EnvironmentID,
			&group.DesiredRevision, &group.ObservedRevision, &group.AppliedRevision,
			&group.ApplyState, &group.ArtifactHash, &group.LastErrorCode,
			&group.LastErrorDetail,
		); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *PostgresDurableRouteStore) LoadRouteGroup(ctx context.Context, projectID, environmentID string) (DurableRouteGroup, error) {
	if s == nil || s.Pool == nil {
		return DurableRouteGroup{}, errors.New("postgres durable route store pool is required")
	}
	var group DurableRouteGroup
	err := s.Pool.QueryRow(ctx, `
		SELECT route_group, project_id, environment_id, desired_revision,
		       observed_revision, applied_revision, apply_state, artifact_hash,
		       last_error_code, last_error_detail
		  FROM route_controller_groups
		 WHERE project_id = $1 AND environment_id = $2`,
		projectID, environmentID,
	).Scan(
		&group.RouteGroup, &group.ProjectID, &group.EnvironmentID,
		&group.DesiredRevision, &group.ObservedRevision, &group.AppliedRevision,
		&group.ApplyState, &group.ArtifactHash, &group.LastErrorCode,
		&group.LastErrorDetail,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableRouteGroup{}, ErrDurableRouteNotFound
	}
	if err != nil {
		return DurableRouteGroup{}, fmt.Errorf("load durable route group: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT r.id, r.domain_id, r.project_id, r.environment_id, r.service_id,
		       r.name, r.path_prefix, r.target_port, r.public_enabled,
		       r.desired_revision, r.created_at, r.updated_at,
		       d.id, d.project_id, d.environment_id, d.service_id, d.kind,
		       d.hostname, d.normalized_hostname, d.apps_domain, d.tls_desired,
		       d.tls_state, d.desired_revision, d.created_at, d.updated_at,
		       (`+managedRouteOwnershipEligibilityPredicateSQL+`) AS ownership_eligible
		  FROM http_routes r
		  JOIN domains d ON d.id = r.domain_id
		 WHERE r.project_id = $1 AND r.environment_id = $2
		 ORDER BY r.name, r.id`,
		projectID, environmentID,
	)
	if err != nil {
		return DurableRouteGroup{}, fmt.Errorf("load durable routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var desired DurableDesiredRoute
		var ownershipEligible bool
		if err := rows.Scan(
			&desired.Route.ID, &desired.Route.DomainID,
			&desired.Route.ProjectID, &desired.Route.EnvironmentID,
			&desired.Route.ServiceID, &desired.Route.Name,
			&desired.Route.PathPrefix, &desired.Route.TargetPort,
			&desired.Route.Public, &desired.Route.DesiredRevision,
			&desired.Route.CreatedAt, &desired.Route.UpdatedAt,
			&desired.Domain.ID, &desired.Domain.ProjectID,
			&desired.Domain.EnvironmentID, &desired.Domain.ServiceID,
			&desired.Domain.Kind, &desired.Domain.Hostname,
			&desired.Domain.NormalizedHostname, &desired.Domain.AppsDomain,
			&desired.Domain.TLSDesired, &desired.Domain.TLSState,
			&desired.Domain.DesiredRevision, &desired.Domain.CreatedAt,
			&desired.Domain.UpdatedAt, &ownershipEligible,
		); err != nil {
			return DurableRouteGroup{}, err
		}
		if !ownershipEligible {
			desired.Route.Public = false
		}
		backends, err := s.listRouteBackends(ctx, desired.Route.ID)
		if err != nil {
			return DurableRouteGroup{}, err
		}
		desired.Backends = backends
		group.Routes = append(group.Routes, desired)
	}
	if err := rows.Err(); err != nil {
		return DurableRouteGroup{}, err
	}
	return group, nil
}

func (s *PostgresDurableRouteStore) RecordRouteApply(ctx context.Context, projectID, environmentID string, expectedRevision int64, result RouteApplyResult) (bool, error) {
	if s == nil || s.Pool == nil {
		return false, errors.New("postgres durable route store pool is required")
	}
	if result.AttemptedAt.IsZero() {
		result.AttemptedAt = s.now()
	}
	if result.FilePublished &&
		result.State != RouteApplyFileApplied &&
		result.State != RouteApplyReady &&
		result.State != RouteApplyPrivate &&
		result.State != RouteApplyBlockedTLS &&
		result.State != RouteApplyBlockedBackend {
		return false, fmt.Errorf("%w: published file has an unsupported apply state", ErrDurableRouteInvalid)
	}
	if result.FilePublished && result.ArtifactHash == "" {
		return false, fmt.Errorf("%w: published file requires an artifact hash", ErrDurableRouteInvalid)
	}
	if result.ArtifactChanged && !result.FilePublished {
		return false, fmt.Errorf("%w: changed artifact requires publication evidence", ErrDurableRouteInvalid)
	}
	if (result.State == RouteApplyFileApplied || result.State == RouteApplyReady) && !result.FilePublished {
		return false, fmt.Errorf("%w: applied/ready requires publication evidence", ErrDurableRouteInvalid)
	}
	result.ErrorCode = strings.TrimSpace(result.ErrorCode)
	result.ErrorDetail = strings.TrimSpace(result.ErrorDetail)
	if len(result.ErrorDetail) > 512 {
		result.ErrorDetail = result.ErrorDetail[:512]
	}
	var rowsAffected int64
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if err := AcquireRouteEnvironmentMutationLock(
			ctx,
			tx,
			projectID,
			environmentID,
		); err != nil {
			return err
		}
		// A periodic reconcile must still observe the artifact and perform a
		// revision-safe database check, but an already-correct private group has
		// no external acknowledgement to refresh. Avoid creating a new Postgres
		// row version, timestamp, and apply-attempt every 15 seconds. Holding the
		// same environment advisory lock as desired-state writers keeps this
		// comparison equivalent to the UPDATE CAS below.
		if result.State == RouteApplyPrivate &&
			result.FilePublished &&
			!result.ArtifactChanged {
			var desiredRevision, observedRevision, appliedRevision int64
			var applyState RouteApplyState
			var artifactHash, errorCode, errorDetail string
			err := tx.QueryRow(ctx, `
				SELECT desired_revision, observed_revision, applied_revision,
				       apply_state, artifact_hash,
				       last_error_code, last_error_detail
				  FROM route_controller_groups
				 WHERE project_id = $1
				   AND environment_id = $2
				 FOR UPDATE`,
				projectID,
				environmentID,
			).Scan(
				&desiredRevision,
				&observedRevision,
				&appliedRevision,
				&applyState,
				&artifactHash,
				&errorCode,
				&errorDetail,
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if desiredRevision != expectedRevision {
				return nil
			}
			if observedRevision == expectedRevision &&
				appliedRevision == expectedRevision &&
				applyState == result.State &&
				artifactHash == result.ArtifactHash &&
				errorCode == result.ErrorCode &&
				errorDetail == result.ErrorDetail {
				rowsAffected = 1
				return nil
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE route_controller_groups
			   SET observed_revision = $1,
			       applied_revision = CASE WHEN $2 THEN $1 ELSE applied_revision END,
			       apply_state = $3,
			       artifact_hash = CASE WHEN $2 THEN $4 ELSE artifact_hash END,
			       last_error_code = $5,
			       last_error_detail = $6,
			       apply_attempts = apply_attempts + 1,
			       last_attempt_at = $7,
			       updated_at = $7
			 WHERE project_id = $8
			   AND environment_id = $9
			   AND desired_revision = $1`,
			expectedRevision, result.FilePublished, string(result.State),
			result.ArtifactHash, result.ErrorCode, result.ErrorDetail,
			result.AttemptedAt, projectID, environmentID,
		)
		if err == nil {
			rowsAffected = tag.RowsAffected()
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record durable route apply: %w", err)
	}
	return rowsAffected == 1, nil
}

func (s *PostgresDurableRouteStore) RecordRoutePublicationObservation(ctx context.Context, group DurableRouteGroup, artifactHash string, verification RoutePublicationVerification) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	if len(artifactHash) != sha256.Size*2 {
		return fmt.Errorf("%w: publication observation requires a SHA-256 artifact hash", ErrDurableRouteInvalid)
	}
	if verification.ObservedAt.IsZero() {
		verification.ObservedAt = s.now()
	}
	state := "pending"
	if verification.Ready() {
		state = "ready"
	} else if verification.TraefikAcknowledged {
		state = "acknowledged"
	} else if strings.TrimSpace(verification.ErrorDetail) != "" {
		state = "failed"
	}
	detail := strings.TrimSpace(verification.ErrorDetail)
	if len(detail) > 512 {
		detail = detail[:512]
	}
	var probeStatus any
	if verification.ProbeStatus != 0 {
		probeStatus = verification.ProbeStatus
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO route_publication_observations (
			project_id, environment_id, desired_revision, artifact_hash,
			traefik_acknowledged, ingress_probe_succeeded, probe_url,
			probe_status, state, last_error, observed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (project_id, environment_id, desired_revision)
		DO UPDATE SET
			artifact_hash = EXCLUDED.artifact_hash,
			traefik_acknowledged = EXCLUDED.traefik_acknowledged,
			ingress_probe_succeeded = EXCLUDED.ingress_probe_succeeded,
			probe_url = EXCLUDED.probe_url,
			probe_status = EXCLUDED.probe_status,
			state = EXCLUDED.state,
			last_error = EXCLUDED.last_error,
			observed_at = EXCLUDED.observed_at`,
		group.ProjectID, group.EnvironmentID, group.DesiredRevision, artifactHash,
		verification.TraefikAcknowledged, verification.IngressProbeSucceeded,
		verification.ProbeURL, probeStatus, state, detail, verification.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf("record route publication observation: %w", err)
	}
	return nil
}

func (s *PostgresDurableRouteStore) RecordIngressEnrollment(ctx context.Context, enrollment IngressEnrollment) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres durable route store pool is required")
	}
	if strings.TrimSpace(enrollment.ProjectID) == "" || strings.TrimSpace(enrollment.EnvironmentID) == "" ||
		strings.TrimSpace(enrollment.IngressID) == "" || strings.TrimSpace(enrollment.NetworkName) == "" {
		return fmt.Errorf("%w: ingress enrollment scope, ingress, and network are required", ErrDurableRouteInvalid)
	}
	now := s.now()
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO ingress_environment_enrollments (
			project_id, environment_id, ingress_id, network_name, state,
			last_error, observed_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (project_id, environment_id)
		DO UPDATE SET ingress_id = EXCLUDED.ingress_id,
			network_name = EXCLUDED.network_name, state = EXCLUDED.state,
			last_error = EXCLUDED.last_error, observed_at = EXCLUDED.observed_at,
			updated_at = EXCLUDED.updated_at`,
		enrollment.ProjectID, enrollment.EnvironmentID, enrollment.IngressID,
		enrollment.NetworkName, string(enrollment.State), enrollment.LastError,
		enrollment.ObservedAt, now,
	)
	if err != nil {
		return fmt.Errorf("record ingress enrollment: %w", err)
	}
	return nil
}

func (s *PostgresDurableRouteStore) listRouteBackends(ctx context.Context, routeID string) ([]DurableRouteBackend, error) {
	rows, err := s.Pool.Query(ctx, routeBackendsSelect+` WHERE route_id = $1 ORDER BY service_url, id`, routeID)
	if err != nil {
		return nil, fmt.Errorf("list durable route backends: %w", err)
	}
	defer rows.Close()
	var out []DurableRouteBackend
	for rows.Next() {
		backend, err := scanDurableRouteBackend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, backend)
	}
	return out, rows.Err()
}

func bumpRouteGroupRevision(ctx context.Context, tx *dbpg.Tx, projectID, environmentID string, now time.Time) (int64, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO route_controller_groups (
			project_id, environment_id, route_group, desired_revision,
			observed_revision, applied_revision, apply_state, created_at, updated_at
		) VALUES ($1,$2,$3,0,0,0,'pending',$4,$4)
		ON CONFLICT (project_id, environment_id) DO NOTHING`,
		projectID, environmentID, durableRouteGroupName(projectID, environmentID), now,
	); err != nil {
		return 0, err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `
		UPDATE route_controller_groups
		   SET desired_revision = desired_revision + 1,
		       apply_state = 'pending',
		       last_error_code = '',
		       last_error_detail = '',
		       updated_at = $1
		 WHERE project_id = $2 AND environment_id = $3
		RETURNING desired_revision`,
		now, projectID, environmentID,
	).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

func getDomainTx(ctx context.Context, tx *dbpg.Tx, id string, lock bool) (DurableDomain, bool, error) {
	query := `
		SELECT id, project_id, environment_id, service_id, kind,
		       hostname, normalized_hostname, apps_domain, tls_desired,
		       tls_state, desired_revision, created_at, updated_at
		  FROM domains WHERE id = $1`
	if lock {
		query += " FOR UPDATE"
	}
	out, err := scanDurableDomain(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableDomain{}, false, nil
	}
	return out, err == nil, err
}

func getHTTPRouteTx(ctx context.Context, tx *dbpg.Tx, id string, lock bool) (DurableHTTPRoute, bool, error) {
	query := `
		SELECT id, domain_id, project_id, environment_id, service_id,
		       name, path_prefix, target_port, public_enabled,
		       desired_revision, created_at, updated_at
		  FROM http_routes WHERE id = $1`
	if lock {
		query += " FOR UPDATE"
	}
	out, err := scanDurableHTTPRoute(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableHTTPRoute{}, false, nil
	}
	return out, err == nil, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanDurableDomain(row rowScanner) (DurableDomain, error) {
	var out DurableDomain
	err := row.Scan(
		&out.ID, &out.ProjectID, &out.EnvironmentID, &out.ServiceID,
		&out.Kind, &out.Hostname, &out.NormalizedHostname, &out.AppsDomain,
		&out.TLSDesired, &out.TLSState, &out.DesiredRevision,
		&out.CreatedAt, &out.UpdatedAt,
	)
	return out, err
}

func scanDurableHTTPRoute(row rowScanner) (DurableHTTPRoute, error) {
	var out DurableHTTPRoute
	err := row.Scan(
		&out.ID, &out.DomainID, &out.ProjectID, &out.EnvironmentID,
		&out.ServiceID, &out.Name, &out.PathPrefix, &out.TargetPort,
		&out.Public, &out.DesiredRevision, &out.CreatedAt, &out.UpdatedAt,
	)
	return out, err
}

const routeBackendsSelect = `
	SELECT id, route_id, project_id, environment_id, service_id,
	       deployment_id, instance_id, service_url, ready,
	       desired_revision, observed_at
	  FROM route_backends`

func scanDurableRouteBackend(row rowScanner) (DurableRouteBackend, error) {
	var out DurableRouteBackend
	err := row.Scan(
		&out.ID, &out.RouteID, &out.ProjectID, &out.EnvironmentID,
		&out.ServiceID, &out.DeploymentID, &out.InstanceID,
		&out.ServiceURL, &out.Ready, &out.DesiredRevision, &out.ObservedAt,
	)
	return out, err
}

func listRouteBackendsTx(ctx context.Context, tx *dbpg.Tx, routeID string) ([]DurableRouteBackend, error) {
	rows, err := tx.Query(ctx, routeBackendsSelect+` WHERE route_id = $1 ORDER BY id`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DurableRouteBackend
	for rows.Next() {
		item, err := scanDurableRouteBackend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func equivalentBackends(left, right []DurableRouteBackend) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]DurableRouteBackend(nil), left...)
	right = append([]DurableRouteBackend(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].ID < left[j].ID })
	sort.Slice(right, func(i, j int) bool { return right[i].ID < right[j].ID })
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].RouteID != right[index].RouteID ||
			left[index].RouteScope != right[index].RouteScope ||
			left[index].DeploymentID != right[index].DeploymentID ||
			left[index].InstanceID != right[index].InstanceID ||
			left[index].ServiceURL != right[index].ServiceURL ||
			left[index].Ready != right[index].Ready {
			return false
		}
	}
	return true
}

func validateBackendURL(routeValue DurableHTTPRoute, domainValue DurableDomain, raw string) error {
	return validateBackendURLTarget(routeValue, domainValue, raw, false)
}

func validateBackendURLTarget(
	routeValue DurableHTTPRoute,
	domainValue DurableDomain,
	raw string,
	allowHistoricalPort bool,
) error {
	if err := ValidateRouteInput(RouteInput{
		Name:       routeValue.Name,
		Host:       domainValue.NormalizedHostname,
		PathPrefix: routeValue.PathPrefix,
		ServiceURL: raw,
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrDurableRouteInvalid, err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: backend URL is invalid", ErrDurableRouteInvalid)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || (!allowHistoricalPort && port != routeValue.TargetPort) {
		return fmt.Errorf("%w: backend port does not match route target port", ErrDurableRouteInvalid)
	}
	return nil
}

// allowHistoricalBackendPort preserves zero-downtime target-port changes
// without weakening candidate validation. A normal stage may retain only the
// exact address and identity already persisted for an old deployment. A
// compensation write may also restore a caller-captured backend stamped by an
// earlier revision. New candidates have no earlier stamp and must use the
// route's current target port.
func allowHistoricalBackendPort(
	desired DurableRouteBackend,
	persisted []DurableRouteBackend,
	expectedRevision int64,
	restoreCapturedBackendSet bool,
) bool {
	for _, current := range persisted {
		if desired.ID == current.ID &&
			desired.RouteID == current.RouteID &&
			desired.RouteScope == current.RouteScope &&
			desired.DeploymentID == current.DeploymentID &&
			desired.InstanceID == current.InstanceID &&
			desired.ServiceURL == current.ServiceURL {
			return true
		}
	}
	return restoreCapturedBackendSet &&
		desired.DesiredRevision > 0 &&
		desired.DesiredRevision < expectedRevision
}

func isRouteUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
