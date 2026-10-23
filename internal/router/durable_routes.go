package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/domain"
)

var (
	ErrDurableRouteInvalid       = errors.New("durable route intent is invalid")
	ErrDurableRouteNotFound      = errors.New("durable route intent not found")
	ErrDurableHostnameOwned      = errors.New("normalized hostname is already owned")
	ErrDurableRouteScopeMismatch = errors.New("durable route scope mismatch")
	ErrDurableRouteRevisionMoved = errors.New("durable route revision changed during reconciliation")
	ErrDurableRouteAmbiguous     = errors.New("durable route selector is ambiguous")
	ErrDurableRouteBlocked       = errors.New("durable route publication is blocked")
	ErrDurableRouteUnverified    = errors.New("durable route publication is unverified")
)

type DomainKind string

const (
	DomainKindGenerated DomainKind = "generated"
	DomainKindCustom    DomainKind = "custom"
)

// DomainTLSState is deliberately evidence-shaped. The router records whether
// a TLS requirement is disabled, unavailable, or backed by durable ready
// certificate evidence; issuance remains owned by the worker lifecycle.
type DomainTLSState string

const (
	DomainTLSDisabled    DomainTLSState = "disabled"
	DomainTLSUnavailable DomainTLSState = "unavailable"
	DomainTLSReady       DomainTLSState = "ready"
)

type RouteApplyState string

const (
	RouteApplyPending        RouteApplyState = "pending"
	RouteApplyPrivate        RouteApplyState = "private"
	RouteApplyBlockedTLS     RouteApplyState = "blocked_tls"
	RouteApplyBlockedBackend RouteApplyState = "blocked_backend"
	RouteApplyFileApplied    RouteApplyState = "file_applied"
	RouteApplyReady          RouteApplyState = "ready"
	RouteApplyFailed         RouteApplyState = "failed"
)

type RouteScope struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
}

func (s RouteScope) validate() error {
	if strings.TrimSpace(s.ProjectID) == "" ||
		strings.TrimSpace(s.EnvironmentID) == "" ||
		strings.TrimSpace(s.ServiceID) == "" {
		return fmt.Errorf("%w: project, environment, and service are required", ErrDurableRouteInvalid)
	}
	return nil
}

type DurableDomain struct {
	ID string
	RouteScope
	Kind               DomainKind
	Hostname           string
	NormalizedHostname string
	AppsDomain         string
	TLSDesired         bool
	TLSState           DomainTLSState
	DesiredRevision    int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type DurableHTTPRoute struct {
	ID       string
	DomainID string
	RouteScope
	Name            string
	PathPrefix      string
	TargetPort      int
	Public          bool
	DesiredRevision int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type DurableRouteBackend struct {
	ID      string
	RouteID string
	RouteScope
	DeploymentID    string
	InstanceID      string
	ServiceURL      string
	Ready           bool
	DesiredRevision int64
	ObservedAt      time.Time
}

// DurableRouteBackendSet is one route's complete desired backend set. Batch
// replacement treats every item in a slice as one environment route-group
// mutation, so callers can stage, commit, or restore multiple hostnames without
// exposing a partially updated group.
type DurableRouteBackendSet struct {
	RouteID  string
	Backends []DurableRouteBackend
}

type DurableDesiredRoute struct {
	Domain   DurableDomain
	Route    DurableHTTPRoute
	Backends []DurableRouteBackend
}

type DurableRouteGroup struct {
	RouteGroup       string
	ProjectID        string
	EnvironmentID    string
	DesiredRevision  int64
	ObservedRevision int64
	AppliedRevision  int64
	ApplyState       RouteApplyState
	ArtifactHash     string
	LastErrorCode    string
	LastErrorDetail  string
	Routes           []DurableDesiredRoute
}

type RouteApplyResult struct {
	State         RouteApplyState
	ArtifactHash  string
	ErrorCode     string
	ErrorDetail   string
	FilePublished bool
	// ArtifactChanged distinguishes a real filesystem repair/replacement from
	// an idempotent observation of the already-correct artifact. The durable
	// store uses it to avoid rewriting an unchanged private controller row on
	// every periodic reconcile without weakening revision CAS or self-healing.
	ArtifactChanged bool
	AttemptedAt     time.Time
}

type DurableRouteBlockedError struct {
	State       RouteApplyState
	ErrorCode   string
	ErrorDetail string
}

func (e *DurableRouteBlockedError) Error() string {
	if e == nil {
		return ErrDurableRouteBlocked.Error()
	}
	detail := strings.TrimSpace(e.ErrorDetail)
	if detail == "" {
		detail = strings.TrimSpace(e.ErrorCode)
	}
	if detail == "" {
		return ErrDurableRouteBlocked.Error()
	}
	return ErrDurableRouteBlocked.Error() + ": " + detail
}

func (*DurableRouteBlockedError) Unwrap() error {
	return ErrDurableRouteBlocked
}

// DurableRouteStore owns desired and observed routing truth. RecordApply uses
// an expected revision CAS so an older render can never mark newer intent as
// applied.
type DurableRouteStore interface {
	ListRouteGroups(context.Context) ([]DurableRouteGroup, error)
	LoadRouteGroup(context.Context, string, string) (DurableRouteGroup, error)
	RecordRouteApply(context.Context, string, string, int64, RouteApplyResult) (bool, error)
}

type RouteSetPublisher interface {
	PublishRouteSet(routeGroup string, routes []RouteInput) (RouteArtifactPublicationApplyEvidence, string, error)
	// WithRouteGroupPublicationLock serializes the complete external
	// publication window for one project/environment. The lock must cover the
	// desired-state read, artifact replacement, Traefik acknowledgement,
	// ingress probes, and the final revision CAS. Serializing only the file
	// write permits a stale reconciler to overwrite the artifact after a newer
	// reconciler has observed it but before that newer revision records ready.
	WithRouteGroupPublicationLock(context.Context, string, string, func() error) error
}

type RoutePublicationVerification struct {
	TraefikAcknowledged   bool
	IngressProbeSucceeded bool
	ProbeURL              string
	ProbeStatus           int
	ErrorDetail           string
	ObservedAt            time.Time
}

func (v RoutePublicationVerification) Ready() bool {
	return v.TraefikAcknowledged && v.IngressProbeSucceeded
}

// RoutePublicationVerifier observes the configured ingress after file
// publication. Implementations must verify both Traefik's active config and a
// Host-header ingress request; a successful file write alone is never ready.
type RoutePublicationVerifier interface {
	VerifyRoutePublication(context.Context, DurableRouteGroup, []RouteInput, string) (RoutePublicationVerification, error)
}

type RoutePublicationObservationRecorder interface {
	RecordRoutePublicationObservation(context.Context, DurableRouteGroup, string, RoutePublicationVerification) error
}

type IngressEnvironmentEnroller interface {
	EnsureIngressEnrollment(context.Context, DurableRouteGroup) error
}

type DurableRouteReconciler struct {
	Store     DurableRouteStore
	Publisher RouteSetPublisher
	Verifier  RoutePublicationVerifier
	Enroller  IngressEnvironmentEnroller
	// CertResolver is operator-owned Traefik static configuration. Durable
	// TLS intent is never published with an invented resolver; an empty value
	// keeps the existing TLS-blocked/file-only behavior.
	CertResolver string
	Now          func() time.Time
	MaxRetries   int
}

const DefaultDurableRouteReconcileInterval = 15 * time.Second

func (r *DurableRouteReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// ReconcileAll intentionally includes already-applied groups. Re-observing the
// file provider artifact is how the controller repairs deletion or corruption
// after an API, worker, or host process restart.
func (r *DurableRouteReconciler) ReconcileAll(ctx context.Context) error {
	if r.Store == nil {
		return errors.New("durable route reconciler store is required")
	}
	groups, err := r.Store.ListRouteGroups(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, group := range groups {
		if err := r.ReconcileGroup(ctx, group.ProjectID, group.EnvironmentID); err != nil {
			errs = append(errs, fmt.Errorf("reconcile route group %s: %w", group.RouteGroup, err))
		}
	}
	return errors.Join(errs...)
}

// RunDurableRouteController performs an immediate startup reconciliation and
// continues until ctx is canceled. The initial pass is what repairs durable
// desired state after an API or worker restart; periodic passes repair missing
// or corrupted file-provider artifacts even when the desired revision did not
// change.
func RunDurableRouteController(ctx context.Context, reconciler *DurableRouteReconciler, interval time.Duration, onError func(error)) {
	if reconciler == nil {
		if onError != nil {
			onError(errors.New("durable route reconciler is required"))
		}
		return
	}
	if interval <= 0 {
		interval = DefaultDurableRouteReconcileInterval
	}
	reconcile := func() {
		if err := reconciler.ReconcileAll(ctx); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
			onError(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (r *DurableRouteReconciler) ReconcileGroup(ctx context.Context, projectID, environmentID string) error {
	if r.Store == nil {
		return errors.New("durable route reconciler store is required")
	}
	if r.Publisher == nil {
		return errors.New("durable route reconciler publisher is required")
	}
	return r.Publisher.WithRouteGroupPublicationLock(
		ctx,
		projectID,
		environmentID,
		func() error {
			return r.reconcileGroupLocked(ctx, projectID, environmentID)
		},
	)
}

func (r *DurableRouteReconciler) reconcileGroupLocked(
	ctx context.Context,
	projectID string,
	environmentID string,
) error {
	retries := r.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	for attempt := 0; attempt < retries; attempt++ {
		group, err := r.Store.LoadRouteGroup(ctx, projectID, environmentID)
		if err != nil {
			return err
		}
		routes, blocked, publicCount := durableRouteInputs(group)
		// A blocked route describes the whole group only when the group has
		// nothing else to serve: every public route is blocked, or the group
		// is entirely private. Route groups are per environment, so treating
		// any single blocked route as a group failure let one service's route
		// with no ready backend fail every other service's deploy in that
		// environment — and it skipped verification for the routes that had
		// published perfectly well.
		blockedIsGroupWide := blocked != nil &&
			(blocked.State == RouteApplyPrivate || (len(routes) == 0 && publicCount > 0))
		if strings.TrimSpace(r.CertResolver) != "" {
			for index := range routes {
				routes[index].CertResolver = r.CertResolver
			}
		}
		if len(routes) > 0 && r.Enroller != nil {
			if enrollErr := r.Enroller.EnsureIngressEnrollment(ctx, group); enrollErr != nil {
				result := RouteApplyResult{
					State: RouteApplyFailed, ErrorCode: "trusted_ingress_enrollment_failed",
					ErrorDetail: enrollErr.Error(), AttemptedAt: r.now(),
				}
				applied, recordErr := r.Store.RecordRouteApply(ctx, projectID, environmentID, group.DesiredRevision, result)
				if recordErr != nil {
					return recordErr
				}
				if !applied {
					continue
				}
				return enrollErr
			}
		}
		evidence, hash, publishErr := r.Publisher.PublishRouteSet(group.RouteGroup, routes)
		result := RouteApplyResult{
			State:         RouteApplyFileApplied,
			ArtifactHash:  hash,
			FilePublished: publishErr == nil && evidence.Status == RouteArtifactPublicationApplyStatusReady,
			ArtifactChanged: publishErr == nil &&
				evidence.Status == RouteArtifactPublicationApplyStatusReady &&
				evidence.Writes > 0,
			AttemptedAt: r.now(),
		}
		var verificationErr error
		if result.FilePublished && blockedIsGroupWide {
			result.State = blocked.State
			result.ErrorCode = blocked.ErrorCode
			result.ErrorDetail = blocked.ErrorDetail
		} else if publishErr != nil || evidence.Status != RouteArtifactPublicationApplyStatusReady {
			result.State = RouteApplyFailed
			result.ErrorCode = "route_file_publication_failed"
			result.ErrorDetail = "validated Traefik file publication failed"
		} else if len(routes) > 0 && r.Verifier == nil {
			result.State = RouteApplyFileApplied
			result.ErrorCode = "route_publication_unverified"
			result.ErrorDetail = "route publication verifier is not configured"
			verificationErr = fmt.Errorf(
				"%w: %s",
				ErrDurableRouteUnverified,
				result.ErrorDetail,
			)
		} else if r.Verifier != nil {
			verification, verifyErr := r.Verifier.VerifyRoutePublication(ctx, group, routes, hash)
			if verification.ObservedAt.IsZero() {
				verification.ObservedAt = r.now()
			}
			if recorder, ok := r.Store.(RoutePublicationObservationRecorder); ok {
				if recordErr := recorder.RecordRoutePublicationObservation(ctx, group, hash, verification); recordErr != nil {
					return recordErr
				}
			}
			if verifyErr != nil || !verification.Ready() {
				result.State = RouteApplyFileApplied
				result.ErrorCode = "route_publication_unverified"
				result.ErrorDetail = strings.TrimSpace(verification.ErrorDetail)
				if result.ErrorDetail == "" {
					result.ErrorDetail = "Traefik acknowledgement and ingress probe evidence are not both ready"
				}
				verificationErr = fmt.Errorf(
					"%w: %s",
					ErrDurableRouteUnverified,
					result.ErrorDetail,
				)
			} else {
				result.State = RouteApplyReady
			}
		}
		// Surface a partial block without hiding it: the routes that could be
		// published are applied and verified, and the group still records why
		// the remaining ones were withheld.
		if blocked != nil && !blockedIsGroupWide && result.ErrorCode == "" {
			result.ErrorCode = blocked.ErrorCode
			result.ErrorDetail = blocked.ErrorDetail
		}
		applied, err := r.Store.RecordRouteApply(ctx, projectID, environmentID, group.DesiredRevision, result)
		if err != nil {
			return err
		}
		if !applied {
			continue
		}
		if publishErr != nil {
			return publishErr
		}
		if evidence.Status != RouteArtifactPublicationApplyStatusReady {
			return errors.New("route file publication did not return ready evidence")
		}
		if blockedIsGroupWide && blocked.State != RouteApplyPrivate {
			return &DurableRouteBlockedError{
				State:       blocked.State,
				ErrorCode:   blocked.ErrorCode,
				ErrorDetail: blocked.ErrorDetail,
			}
		}
		if verificationErr != nil {
			return verificationErr
		}
		return nil
	}
	return ErrDurableRouteRevisionMoved
}

// durableRouteInputs splits a route group into the routes that can be
// published now and the first reason any public route had to be withheld. The
// returned count of public routes lets the caller tell "one service is
// blocked" apart from "the whole group has nothing to serve".
func durableRouteInputs(group DurableRouteGroup) ([]RouteInput, *RouteApplyResult, int) {
	routes := append([]DurableDesiredRoute(nil), group.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Route.Name != routes[j].Route.Name {
			return routes[i].Route.Name < routes[j].Route.Name
		}
		return routes[i].Route.ID < routes[j].Route.ID
	})
	out := make([]RouteInput, 0, len(routes))
	var blocked *RouteApplyResult
	publicCount := 0
	publicationToken := durableRoutePublicationToken(group)
	for _, desired := range routes {
		if !desired.Route.Public {
			continue
		}
		publicCount++
		if desired.Domain.TLSDesired && desired.Domain.TLSState != DomainTLSReady {
			if blocked == nil {
				blocked = &RouteApplyResult{
					State:       RouteApplyBlockedTLS,
					ErrorCode:   "certificate_issuance_unavailable",
					ErrorDetail: "TLS is required but no durable ready certificate is recorded; the route was removed from the published file",
				}
			}
			continue
		}
		backends := append([]DurableRouteBackend(nil), desired.Backends...)
		sort.Slice(backends, func(i, j int) bool {
			if backends[i].ServiceURL != backends[j].ServiceURL {
				return backends[i].ServiceURL < backends[j].ServiceURL
			}
			return backends[i].ID < backends[j].ID
		})
		// Two ready backends can name the same address: a redeploy registers a
		// backend per deployment, and the replacement container routinely
		// reuses the address the removed one had. That is one server, not a
		// conflict, but emitting it twice trips the renderer's own
		// duplicate-URL validation and fails the whole publication. Backends
		// are already sorted by URL then ID, so keeping the first occurrence is
		// deterministic.
		serviceURLs := make([]string, 0, len(backends))
		seenServiceURLs := make(map[string]struct{}, len(backends))
		for _, backend := range backends {
			if !backend.Ready {
				continue
			}
			if _, duplicate := seenServiceURLs[backend.ServiceURL]; duplicate {
				continue
			}
			seenServiceURLs[backend.ServiceURL] = struct{}{}
			serviceURLs = append(serviceURLs, backend.ServiceURL)
		}
		if len(serviceURLs) == 0 {
			if blocked == nil {
				blocked = &RouteApplyResult{
					State:       RouteApplyBlockedBackend,
					ErrorCode:   "ready_route_backend_unavailable",
					ErrorDetail: "public route has no durably ready backend; the route was removed from the published file",
				}
			}
			continue
		}
		out = append(out, RouteInput{
			Name:             desired.Route.Name,
			Host:             desired.Domain.NormalizedHostname,
			PathPrefix:       desired.Route.PathPrefix,
			ServiceURLs:      serviceURLs,
			TLS:              desired.Domain.TLSDesired,
			PublicationToken: publicationToken,
		})
	}
	if publicCount == 0 && len(routes) > 0 {
		blocked = &RouteApplyResult{State: RouteApplyPrivate}
	}
	// Publishing an ACME-only group file atomically removes private or blocked
	// user routers without deleting sibling groups.
	return out, blocked, publicCount
}

// durableRoutePublicationToken is an opaque, support-safe identity for one
// environment route-group revision. It is deliberately independent from tenant
// hostnames and backend addresses; the active-config verifier separately
// compares the complete rendered rule and backend set.
func durableRoutePublicationToken(group DurableRouteGroup) string {
	sum := sha256.Sum256([]byte(
		group.ProjectID + "\x00" +
			group.EnvironmentID + "\x00" +
			strconv.FormatInt(group.DesiredRevision, 10),
	))
	return "pub-" + hex.EncodeToString(sum[:16])
}

func validateDomainIntent(value DurableDomain) (DurableDomain, error) {
	if strings.TrimSpace(value.ID) == "" {
		return DurableDomain{}, fmt.Errorf("%w: domain id is required", ErrDurableRouteInvalid)
	}
	if err := value.RouteScope.validate(); err != nil {
		return DurableDomain{}, err
	}
	switch value.Kind {
	case DomainKindGenerated, DomainKindCustom:
	default:
		return DurableDomain{}, fmt.Errorf("%w: unsupported domain kind", ErrDurableRouteInvalid)
	}
	hostname, err := domain.NormalizeHostname(value.Hostname)
	if err != nil {
		return DurableDomain{}, fmt.Errorf("%w: %v", ErrDurableRouteInvalid, err)
	}
	value.Hostname = hostname
	value.NormalizedHostname = hostname
	if value.Kind == DomainKindGenerated {
		appsDomain, err := domain.NormalizeHostname(value.AppsDomain)
		if err != nil {
			return DurableDomain{}, fmt.Errorf("%w: invalid apps domain", ErrDurableRouteInvalid)
		}
		if !domain.IsSingleLabelBelow(hostname, appsDomain) {
			return DurableDomain{}, fmt.Errorf("%w: generated hostname must be one label below apps domain", ErrDurableRouteInvalid)
		}
		value.AppsDomain = appsDomain
	} else if strings.TrimSpace(value.AppsDomain) != "" {
		return DurableDomain{}, fmt.Errorf("%w: custom domain cannot carry an apps domain", ErrDurableRouteInvalid)
	}
	if value.TLSDesired {
		// No issuer is implemented in this slice. The durable intent is honest
		// about that gap until a later certificate controller owns the state.
		value.TLSState = DomainTLSUnavailable
	} else {
		value.TLSState = DomainTLSDisabled
	}
	return value, nil
}

func validateHTTPRouteIntent(value DurableHTTPRoute, hostname string) (DurableHTTPRoute, error) {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.DomainID) == "" {
		return DurableHTTPRoute{}, fmt.Errorf("%w: route and domain ids are required", ErrDurableRouteInvalid)
	}
	if err := value.RouteScope.validate(); err != nil {
		return DurableHTTPRoute{}, err
	}
	if value.TargetPort <= 0 || value.TargetPort > 65535 {
		return DurableHTTPRoute{}, fmt.Errorf("%w: target port must be between 1 and 65535", ErrDurableRouteInvalid)
	}
	value.PathPrefix = strings.TrimSpace(value.PathPrefix)
	if value.PathPrefix == "" {
		value.PathPrefix = "/"
	}
	if err := ValidateRouteInput(RouteInput{
		Name:       value.Name,
		Host:       hostname,
		PathPrefix: value.PathPrefix,
		ServiceURL: fmt.Sprintf("http://127.0.0.1:%d", value.TargetPort),
	}); err != nil {
		return DurableHTTPRoute{}, fmt.Errorf("%w: %v", ErrDurableRouteInvalid, err)
	}
	return value, nil
}
