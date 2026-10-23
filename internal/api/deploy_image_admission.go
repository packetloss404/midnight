// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/registry"
)

// Deploy image admission turns a caller-supplied image reference into the
// immutable digest Midnight will actually run, before any durable row or
// queue job exists.
//
// Two honesty problems are fixed here:
//
//   - A mutable tag (`nginx:1.27`) used to be rejected outright, which forced
//     every caller to hand-resolve a digest. The tag is now resolved against
//     the registry and stored as `nginx:1.27@sha256:<hex>`: the platform pins
//     the digest, so the immutability guarantee is unchanged, and the tag
//     survives for display.
//   - A digest the registry does not hold used to be accepted and queued,
//     failing minutes later inside the deploy worker. It is now rejected at
//     intake with RESOURCE_VALIDATION_FAILED.
//
// Registry reads go through internal/ssrf and honour the operator's
// local-registry endpoint. A registry that cannot be reached fails the intake
// with CAPABILITY_UNAVAILABLE rather than silently queueing work that is
// already known to be unresolvable.

// DeployImageResolver is the registry seam used by deploy intake. The
// production implementation is *registry.Resolver; tests install a fake.
type DeployImageResolver interface {
	Resolve(ctx context.Context, reference string) (registry.Resolution, error)
}

// Environment variables that configure image admission.
//
//	MIDNIGHT_DEPLOY_IMAGE_RESOLVE  on|off, default auto
//	MIDNIGHT_REGISTRY_ENDPOINT     host[:port] of the private registry
//	MIDNIGHT_REGISTRY_ADDR         historical spelling of the same value
//	MIDNIGHT_REGISTRY_INSECURE     serve the private registry over http
//
// Auto means: enabled when this instance has a registry endpoint configured.
// Leaving it disabled keeps the historical contract exactly (digest required,
// no registry read), so an instance with no registry wiring behaves as it
// always did instead of failing closed on every deploy.
const (
	envDeployImageResolve = "MIDNIGHT_DEPLOY_IMAGE_RESOLVE" // not-a-failure-code
	envRegistryEndpoint   = "MIDNIGHT_REGISTRY_ENDPOINT"    // not-a-failure-code
	envRegistryAddr       = "MIDNIGHT_REGISTRY_ADDR"        // not-a-failure-code
	envRegistryInsecure   = "MIDNIGHT_REGISTRY_INSECURE"    // not-a-failure-code
)

var (
	deployImageResolverMu    sync.RWMutex
	deployImageResolverSet   bool
	deployImageResolverValue DeployImageResolver
)

// SetDeployImageResolver installs (or, with nil, clears) the resolver deploy
// intake uses. Server wiring calls it at boot; tests call it to install a fake
// registry. It returns a restore function so a caller can put back whatever
// was configured before.
func SetDeployImageResolver(resolver DeployImageResolver) func() {
	deployImageResolverMu.Lock()
	previousValue, previousSet := deployImageResolverValue, deployImageResolverSet
	deployImageResolverValue, deployImageResolverSet = resolver, true
	deployImageResolverMu.Unlock()
	return func() {
		deployImageResolverMu.Lock()
		deployImageResolverValue, deployImageResolverSet = previousValue, previousSet
		deployImageResolverMu.Unlock()
	}
}

// deployImageResolver returns the active resolver, or nil when image
// admission is not configured for this instance.
func deployImageResolver() DeployImageResolver {
	deployImageResolverMu.RLock()
	resolver, explicit := deployImageResolverValue, deployImageResolverSet
	deployImageResolverMu.RUnlock()
	if explicit {
		return resolver
	}
	return environmentDeployImageResolver()
}

var environmentDeployImageResolver = sync.OnceValue(func() DeployImageResolver {
	endpoint := strings.TrimSpace(os.Getenv(envRegistryEndpoint))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv(envRegistryAddr))
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envDeployImageResolve))) {
	case "0", "false", "off", "no":
		return nil
	case "1", "true", "on", "yes":
	default:
		if endpoint == "" {
			return nil
		}
	}
	return registry.NewResolver(registry.ResolverConfig{
		LocalEndpoint: endpoint,
		LocalInsecure: envTruthy(os.Getenv(envRegistryInsecure)),
	})
})

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// deployImageAdmissionAvailable reports whether this instance can pin a
// mutable tag. Callers use it only to choose the error copy they show when a
// caller supplies a tag; the pin itself always happens through
// admitDeployIntakeImage.
func deployImageAdmissionAvailable() bool {
	return deployImageResolver() != nil
}

// deployIntakeError carries an admission rejection to the HTTP layer.
type deployIntakeError struct {
	Status  int
	Code    failurecodes.Code
	Message string
	Details map[string]any
}

// admitDeployIntakeImage resolves and verifies request.Image in place.
//
// It is a no-op when the request carries no image (source builds resolve their
// own digest inside the build worker) or when no resolver is configured. On
// success request.Image is an immutable, registry-confirmed reference.
func admitDeployIntakeImage(ctx context.Context, request *DeployIntakeRequest) *deployIntakeError {
	if request == nil {
		return nil
	}
	image := strings.TrimSpace(request.Image)
	if image == "" {
		return nil
	}
	resolver := deployImageResolver()
	if resolver == nil {
		return nil
	}

	resolution, err := resolver.Resolve(ctx, image)
	switch {
	case err == nil:
		request.Image = resolution.Reference
		return nil
	case errors.Is(err, registry.ErrReferenceInvalid):
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "deploy image is not a valid image reference",
			Details: map[string]any{
				"field":  "image",
				"image":  image,
				"reason": "expected [registry/]repository:tag or [registry/]repository@sha256:<64 lowercase hex chars>",
			},
		}
	case errors.Is(err, registry.ErrManifestNotFound):
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "deploy image does not exist in its registry",
			Details: map[string]any{
				"field":  "image",
				"image":  image,
				"reason": "the registry does not hold this tag or digest; nothing was queued",
			},
		}
	case errors.Is(err, registry.ErrRegistryDenied):
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "deploy image could not be read from its registry",
			Details: map[string]any{
				"field":  "image",
				"image":  image,
				"reason": "the registry refused an anonymous read; Midnight cannot pin a digest it cannot see",
			},
		}
	default:
		// Unreachable registry, timeout, SSRF refusal, 5xx. This is an
		// instance-side condition and must never be reported as a bad
		// request, but the intake still fails rather than queueing work whose
		// image was never confirmed.
		return &deployIntakeError{
			Status:  http.StatusServiceUnavailable,
			Code:    failurecodes.CapabilityUnavailable,
			Message: "image registry is unreachable; the deploy was not queued",
			Details: map[string]any{
				"field":  "image",
				"image":  image,
				"reason": registryFailureReason(err),
			},
		}
	}
}

// registryFailureReason keeps the operator-facing detail short and free of
// any URL that could name internal infrastructure.
func registryFailureReason(err error) string {
	reason := err.Error()
	if _, tail, ok := strings.Cut(reason, ": "); ok && strings.TrimSpace(tail) != "" {
		reason = tail
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return reason
}

// deployImageAdmissionTimeout bounds the whole admission step so a slow
// registry cannot hold a deploy-intake request open indefinitely.
const deployImageAdmissionTimeout = 15 * time.Second
