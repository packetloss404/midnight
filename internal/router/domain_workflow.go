package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	midnightdomain "github.com/midnight/midnight/internal/domain"
)

// DomainRouteState is the public, evidence-shaped projection of one durable
// domain. A file-applied group is intentionally distinct from a ready group.
// Consumers must not infer public reachability from Domain or Route alone.
type DomainRouteState struct {
	Domain       DurableDomain
	Routes       []DurableHTTPRoute
	ApplyState   RouteApplyState
	ArtifactHash string
	ErrorCode    string
	ErrorDetail  string
	Challenge    *DomainDNSChallenge
	Certificate  *DomainCertificateOrder
}

type DNSChallengeState string

const (
	DNSChallengePending  DNSChallengeState = "pending"
	DNSChallengeVerified DNSChallengeState = "verified"
	DNSChallengeFailed   DNSChallengeState = "failed"
)

type DomainDNSChallenge struct {
	DomainID string
	RouteScope
	RecordName     string
	RecordValue    string
	State          DNSChallengeState
	ObservedValues []string
	Attempts       int64
	LastError      string
	CheckedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CertificateOrderState string

const (
	CertificateOrderPending CertificateOrderState = "pending"
	CertificateOrderIssuing CertificateOrderState = "issuing"
	CertificateOrderReady   CertificateOrderState = "ready"
	CertificateOrderFailed  CertificateOrderState = "failed"
)

type DomainCertificateOrder struct {
	DomainID string
	RouteScope
	Issuer         string
	IssuerOrderRef string
	CertificateRef string
	State          CertificateOrderState
	Attempts       int64
	LastError      string
	ExpiresAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const DomainCertificateRenewalWindow = 30 * 24 * time.Hour

// DomainWorkflowStore is the shared REST, GraphQL, CLI, and deploy-intent
// boundary. Implementations own durable scope checks; callers additionally
// validate the API resource graph before reaching it.
type DomainWorkflowStore interface {
	PutDomain(context.Context, DurableDomain) (DurableDomain, error)
	PutHTTPRoute(context.Context, DurableHTTPRoute) (DurableHTTPRoute, error)
	GetDomain(context.Context, string) (DurableDomain, error)
	DeleteDomain(context.Context, string) error
	ListDomainRouteStates(context.Context, RouteScope) ([]DomainRouteState, error)
	EnsureDomainLifecycle(context.Context, DurableDomain) error
}

// ManagedRouteSelector describes an operator/automation request to use one
// existing service-owned route binding. The fields narrow authority; they
// never authorize creating or taking ownership of a hostname. A blank Host
// selects a generated binding, while a non-blank Host selects an exact custom
// binding. Optional route fields must match the authoritative row.
type ManagedRouteSelector struct {
	Host       string
	Name       string
	Group      string
	PathPrefix string
	TLSDesired bool
}

// ManagedServiceRouteStore is the narrow route-authority seam used by broad
// deploy intake and the deploy worker. Intake resolves one exact eligible
// service binding before accepting work; the worker revalidates that same
// selector and copies its durable identity to the canonical deployment.
type ManagedServiceRouteStore interface {
	SelectManagedServiceRoute(context.Context, RouteScope, ManagedRouteSelector) (DurableDesiredRoute, error)
	BindManagedServiceRouteToDeployment(context.Context, string, RouteScope, ManagedRouteSelector, time.Time) (DurableDesiredRoute, error)
}

// ServiceDomainBinder records the service ownership binding for a
// (domain, route) pair. Creating a domain and its route without this binding
// produces a route nothing can ever select: deploy intake resolves a public
// route intent against an existing binding, so an unbound route stays blocked
// on a missing backend forever. DomainWorkflowStore implementations that can
// bind should implement this; callers treat it as optional so a store without
// binding support fails loudly rather than silently creating dead routes.
type ServiceDomainBinder interface {
	BindServiceDomain(ctx context.Context, domainID, routeID string, scope RouteScope, createdAt time.Time) error
}

// GeneratedRouteProjection is the bounded, redaction-safe route state used by
// tenant deployment reads. It deliberately excludes custom-domain challenges,
// controller diagnostics, backend addresses, and certificate material.
type GeneratedRouteProjection struct {
	RouteScope
	DeploymentID string
	Hostname     string
	Path         string
	ApplyState   RouteApplyState
	Reachable    bool
}

// GeneratedRouteProjectionStore binds route state to the exact authorized
// canonical deployments in one bounded query.
type GeneratedRouteProjectionStore interface {
	ListGeneratedRouteProjections(
		context.Context,
		string,
		string,
		[]string,
	) ([]GeneratedRouteProjection, error)
}

type DomainRouteIntentInput struct {
	Scope      RouteScope
	Kind       DomainKind
	Hostname   string
	AppsDomain string
	PathPrefix string
	TargetPort int
	Public     bool
	TLSDesired bool
	RouteName  string
}

// BuildDomainRouteIntent creates collision-resistant, restart-stable ids and
// generated hostnames. Generated names have exactly one label below the
// configured apps domain; custom names never inherit operator ownership.
func BuildDomainRouteIntent(input DomainRouteIntentInput) (DurableDomain, DurableHTTPRoute, error) {
	if err := input.Scope.validate(); err != nil {
		return DurableDomain{}, DurableHTTPRoute{}, err
	}
	if input.TargetPort <= 0 || input.TargetPort > 65535 {
		return DurableDomain{}, DurableHTTPRoute{}, fmt.Errorf("%w: target port must be between 1 and 65535", ErrDurableRouteInvalid)
	}
	kind := input.Kind
	if kind == "" {
		if strings.TrimSpace(input.Hostname) == "" {
			kind = DomainKindGenerated
		} else {
			kind = DomainKindCustom
		}
	}
	hostname := strings.TrimSpace(input.Hostname)
	appsDomain := ""
	if kind == DomainKindGenerated {
		normalizedAppsDomain, err := midnightdomain.NormalizeHostname(input.AppsDomain)
		if err != nil {
			return DurableDomain{}, DurableHTTPRoute{}, fmt.Errorf("%w: a valid apps domain is required for generated domains", ErrDurableRouteInvalid)
		}
		appsDomain = normalizedAppsDomain
		if hostname == "" {
			hostname = generatedHostname(input.Scope, appsDomain)
		}
	}
	normalizedHostname, err := midnightdomain.NormalizeHostname(hostname)
	if err != nil {
		return DurableDomain{}, DurableHTTPRoute{}, fmt.Errorf("%w: %v", ErrDurableRouteInvalid, err)
	}
	identity := stableRouteIdentity(input.Scope, normalizedHostname)
	routeName := strings.TrimSpace(input.RouteName)
	if routeName == "" {
		routeName = "route-" + identity[:20]
	}
	pathPrefix := strings.TrimSpace(input.PathPrefix)
	if pathPrefix == "" {
		pathPrefix = "/"
	}
	domainValue, err := validateDomainIntent(DurableDomain{
		ID:         "dom_" + identity,
		RouteScope: input.Scope,
		Kind:       kind,
		Hostname:   normalizedHostname,
		AppsDomain: appsDomain,
		TLSDesired: input.TLSDesired,
	})
	if err != nil {
		return DurableDomain{}, DurableHTTPRoute{}, err
	}
	routeValue, err := validateHTTPRouteIntent(DurableHTTPRoute{
		ID:         "rte_" + identity,
		DomainID:   domainValue.ID,
		RouteScope: input.Scope,
		Name:       routeName,
		PathPrefix: pathPrefix,
		TargetPort: input.TargetPort,
		Public:     input.Public,
	}, domainValue.NormalizedHostname)
	if err != nil {
		return DurableDomain{}, DurableHTTPRoute{}, err
	}
	return domainValue, routeValue, nil
}

func stableRouteIdentity(scope RouteScope, hostname string) string {
	sum := sha256.Sum256([]byte(scope.ProjectID + "\x00" + scope.EnvironmentID + "\x00" + scope.ServiceID + "\x00" + hostname))
	return hex.EncodeToString(sum[:16])
}

var nonDNSLabel = regexp.MustCompile(`[^a-z0-9-]+`)

func generatedHostname(scope RouteScope, appsDomain string) string {
	label := strings.ToLower(strings.TrimSpace(scope.ServiceID))
	label = nonDNSLabel.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	if label == "" {
		label = "service"
	}
	sum := sha256.Sum256([]byte(scope.ProjectID + "\x00" + scope.EnvironmentID + "\x00" + scope.ServiceID))
	suffix := hex.EncodeToString(sum[:5])
	maxPrefix := 63 - 1 - len(suffix)
	if len(label) > maxPrefix {
		label = strings.Trim(label[:maxPrefix], "-")
	}
	if label == "" {
		label = "service"
	}
	return label + "-" + suffix + "." + appsDomain
}
