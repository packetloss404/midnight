// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type productPrincipalKind string

const (
	productPrincipalOperator   productPrincipalKind = "operator"
	productPrincipalDeveloper  productPrincipalKind = "developer"
	productPrincipalHuman      productPrincipalKind = "human"
	productPrincipalAutomation productPrincipalKind = "automation"
)

// productPrincipal is the typed authentication result consumed by the product
// service boundary. Human and automation identities are present only after
// their respective durable verifier has authenticated the credential.
type productPrincipal struct {
	Kind       productPrincipalKind
	Human      identity.Principal
	Automation automationtoken.Principal
}

type productPrincipalContextKey struct{}

// AutomationTokenAuthenticator is the verification seam consumed by product
// authentication. Route authorization remains a separate, deny-by-default
// middleware so verification alone never conveys a capability.
type AutomationTokenAuthenticator interface {
	Authenticate(context.Context, string) (automationtoken.Principal, error)
}

func productPrincipalFromContext(ctx context.Context) (productPrincipal, bool) {
	principal, ok := ctx.Value(productPrincipalContextKey{}).(productPrincipal)
	return principal, ok
}

func withProductPrincipal(ctx context.Context, principal productPrincipal) context.Context {
	return context.WithValue(ctx, productPrincipalContextKey{}, principal)
}

// humanProductPrincipalContextMiddleware promotes an already verified identity
// session into the typed product-principal context. It performs no credential
// verification itself and is therefore used only immediately inside
// identitySessionMiddleware on human-only product management routes.
func humanProductPrincipalContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK || principal.UserID == "" || session.UserID != principal.UserID {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
			return
		}
		ctx := withProductPrincipal(r.Context(), productPrincipal{
			Kind:  productPrincipalHuman,
			Human: principal,
		})
		ctx = idempotency.WithActor(ctx, idempotency.UserActor(principal.UserID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func productAuthenticationMiddleware(operatorToken string, productionAuthRequired bool, sessions *identity.Manager, automationTokens AutomationTokenAuthenticator) func(http.Handler) http.Handler {
	operatorToken = strings.TrimSpace(operatorToken)
	operatorTokenConfigured := operatorToken != ""
	operatorTokenValid := auth.ValidateOperatorRecoveryToken(operatorToken) == nil
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken, hasBearer := singleBearerToken(r.Header)
			// Token class is resolved before value comparison. A session-shaped
			// credential can never become an installation-wide operator token,
			// even if an operator accidentally configures the same value.
			if hasBearer && strings.HasPrefix(rawToken, identity.SessionTokenPrefix) {
				if sessions == nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session credentials are not accepted by this route", nil)
					return
				}
				principal, session, err := sessions.Authenticate(r.Context(), r.Header.Get("Authorization"))
				if err != nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					if errors.Is(err, identity.ErrSessionInvalid) {
						writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
						return
					}
					writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
					return
				}
				ctx := withProductPrincipal(r.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal})
				ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
				ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
				ctx = idempotency.WithActor(ctx, idempotency.UserActor(principal.UserID))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if hasBearer && strings.HasPrefix(rawToken, automationtoken.TokenPrefix) {
				if automationTokens == nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "automation credentials are not accepted by this server", nil)
					return
				}
				principal, err := automationTokens.Authenticate(r.Context(), r.Header.Get("Authorization"))
				if err != nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					if errors.Is(err, automationtoken.ErrInvalidToken) {
						writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "automation credential is invalid, expired, or revoked", nil)
						return
					}
					writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "automation authentication is unavailable", nil)
					return
				}
				if principal.Kind != "automation" ||
					principal.Profile != automationtoken.ProfileCIDeployV1 ||
					strings.TrimSpace(principal.ServiceAccountID) == "" ||
					strings.TrimSpace(principal.WorkspaceID) == "" ||
					strings.TrimSpace(principal.ProjectID) == "" {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "automation credential is invalid, expired, or revoked", nil)
					return
				}
				ctx := withProductPrincipal(r.Context(), productPrincipal{
					Kind:       productPrincipalAutomation,
					Automation: principal,
				})
				ctx = idempotency.WithActor(ctx, idempotency.Actor("service-account:"+principal.ServiceAccountID))
				ctx = idempotency.WithTenant(ctx, projectScopedIdempotencyTenant(
					principal.WorkspaceID,
					principal.ProjectID,
				))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if operatorTokenValid && operatorToken != "" && bearerTokenAuthorized(r.Header, operatorToken) {
				ctx := withProductPrincipal(r.Context(), productPrincipal{Kind: productPrincipalOperator})
				ctx = idempotency.WithActor(ctx, idempotency.ActorRecoveryOperator)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if !operatorTokenConfigured && !productionAuthRequired && !hasBearer {
				ctx := withProductPrincipal(r.Context(), productPrincipal{Kind: productPrincipalDeveloper})
				ctx = idempotency.WithActor(ctx, idempotency.ActorLocalDeveloper)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			w.Header().Set("WWW-Authenticate", "Bearer")
			if !hasBearer {
				writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthRequiredMissing, "a product bearer token or human session is required", map[string]any{
					"reason_code":   auth.ProductionAuthMissingReason,
					"recovery_hint": failurecodes.RecoveryHint(failurecodes.AuthRequiredMissing),
				})
				return
			}
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "missing or invalid bearer token", nil)
		})
	}
}

func singleBearerToken(header http.Header) (string, bool) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// humanReadOnlyProductMiddleware keeps the first product-session slice honest:
// operators retain the existing mutation surface, while human sessions may
// reach only handlers whose reads are scoped by tenantReadStores.
func humanReadOnlyProductMiddleware(next http.Handler) http.Handler {
	return humanProductMutationMiddleware(nil, next)
}

// humanTenantAuthorityMiddleware fails closed when a valid human session was
// authenticated but the product resources and access grants do not share one
// verified authority. Operators and tokenless local-development callers retain
// their existing paths. Authentication runs outside this middleware, so an
// invalid or revoked session remains a 401 rather than being reported as a
// capability outage.
func humanTenantAuthorityMiddleware(available bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if ok && principal.Kind == productPrincipalHuman && !available {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant authorization is unavailable", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// humanProductMutationMiddleware retains the deny-by-default human product
// boundary while allowing an explicitly coordinated method/route pair to
// advance. The predicate is router-owned; handlers cannot opt themselves in.
func humanProductMutationMiddleware(allowed func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if ok && principal.Kind == productPrincipalHuman &&
			r.Method != http.MethodGet &&
			(allowed == nil || !allowed(r)) {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "human product mutations are not enabled until atomic tenant authorization is installed", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// humanAutomationRouteBoundaryMiddleware prevents wiring automation admission
// onto an existing operator route from expanding the human-session surface.
// Operators and local-development callers retain their compatibility path;
// automation credentials are evaluated by automationAdmissionMiddleware.
func humanAutomationRouteBoundaryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if ok && principal.Kind == productPrincipalHuman {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "human sessions are not admitted on this automation route", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// humanServiceDeploymentReadBoundaryMiddleware admits canonical deployment
// collection/item GETs below /services/{id}/ for a human session. Its only
// write exceptions are independently installed exact immutable-image
// deployment and content-addressed `/up` POST coordinators. Source linking
// and every other deeper service route remain closed here. Deployment actions
// use a more-specific router pattern and their own coordinator/boundary.
func humanServiceDeploymentReadBoundaryMiddleware(next http.Handler, admittedMutations ...bool) http.Handler {
	deployEnabled := len(admittedMutations) >= 1 && admittedMutations[0]
	upEnabled := len(admittedMutations) >= 2 && admittedMutations[1]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if !ok || principal.Kind != productPrincipalHuman {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet {
			rest := strings.TrimPrefix(
				strings.TrimPrefix(r.URL.Path, v1alphaPrefix),
				resourceCollectionServices+"/",
			)
			if _, collection := parseServiceDeploymentsCollectionPath(rest); collection {
				next.ServeHTTP(w, r)
				return
			}
			if _, previews := parseServicePreviewEnvironmentsPath(rest); previews {
				next.ServeHTTP(w, r)
				return
			}
			if _, _, _, resource := parseServiceDeploymentResourcePath(rest); resource {
				next.ServeHTTP(w, r)
				return
			}
		}
		if deployEnabled && r.Method == http.MethodPost {
			rest := strings.TrimPrefix(
				strings.TrimPrefix(r.URL.Path, v1alphaPrefix),
				resourceCollectionServices+"/",
			)
			if _, collection := parseServiceDeploymentsCollectionPath(rest); collection {
				next.ServeHTTP(w, r)
				return
			}
		}
		if upEnabled && r.Method == http.MethodPost {
			rest := strings.TrimPrefix(
				strings.TrimPrefix(r.URL.Path, v1alphaPrefix),
				resourceCollectionServices+"/",
			)
			if _, up := parseServiceUpPath(rest); up {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeRequestError(
			w,
			r,
			http.StatusForbidden,
			failurecodes.AuthorizationDenied,
			"human sessions are not admitted on this service subroute",
			nil,
		)
	})
}
