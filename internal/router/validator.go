package router

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

type RouteArtifactDiagnosticCode string

const (
	RouteArtifactDuplicateHostPath    RouteArtifactDiagnosticCode = "route_artifact_duplicate_host_path"
	RouteArtifactMissingServiceTarget RouteArtifactDiagnosticCode = "route_artifact_missing_service_target"
	RouteArtifactInvalidHTTPSResolver RouteArtifactDiagnosticCode = "route_artifact_invalid_https_cert_resolver"
	RouteArtifactStaleHash            RouteArtifactDiagnosticCode = "route_artifact_stale_hash"
)

type RouteArtifactValidationResult struct {
	Diagnostics []RouteArtifactDiagnostic
}

func (result RouteArtifactValidationResult) OK() bool {
	return len(result.Diagnostics) == 0
}

func (result RouteArtifactValidationResult) SupportSafeDiagnostics() []string {
	items := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		items = append(items, diagnostic.SupportSafeString())
	}
	return items
}

type RouteArtifactDiagnostic struct {
	Code         RouteArtifactDiagnosticCode
	RouterName   string
	OtherRouter  string
	ServiceName  string
	ExpectedHash string
	ActualHash   string
}

func (diagnostic RouteArtifactDiagnostic) SupportSafeString() string {
	parts := []string{"code=" + string(diagnostic.Code)}
	if diagnostic.RouterName != "" {
		parts = append(parts, "router_hash="+supportHash(diagnostic.RouterName))
	}
	if diagnostic.OtherRouter != "" {
		parts = append(parts, "other_router_hash="+supportHash(diagnostic.OtherRouter))
	}
	if diagnostic.ServiceName != "" {
		parts = append(parts, "service_hash="+supportHash(diagnostic.ServiceName))
	}
	if diagnostic.ExpectedHash != "" {
		parts = append(parts, "expected_hash="+hashPrefix(diagnostic.ExpectedHash))
	}
	if diagnostic.ActualHash != "" {
		parts = append(parts, "actual_hash="+hashPrefix(diagnostic.ActualHash))
	}
	return strings.Join(parts, " ")
}

func ValidateRenderedRouteGroup(file TraefikFile, rendered, expectedHash string) RouteArtifactValidationResult {
	var result RouteArtifactValidationResult
	actualHash := ""
	if rendered != "" {
		actualHash = RenderedConfigHash(rendered)
	}
	if strings.TrimSpace(expectedHash) != "" && actualHash != "" && expectedHash != actualHash {
		result.Diagnostics = append(result.Diagnostics, RouteArtifactDiagnostic{
			Code:         RouteArtifactStaleHash,
			ExpectedHash: expectedHash,
			ActualHash:   actualHash,
		})
	}

	routeByHostPath := make(map[string]string, len(file.HTTP.Routers))
	for _, routerName := range sortedRouterNames(file.HTTP.Routers) {
		router := file.HTTP.Routers[routerName]
		if routerName == ACMEChallengeRouterName {
			continue
		}

		if host, path, ok := hostPathFromRule(router.Rule); ok {
			key := host + "\x00" + path
			if previous, exists := routeByHostPath[key]; exists {
				result.Diagnostics = append(result.Diagnostics, RouteArtifactDiagnostic{
					Code:        RouteArtifactDuplicateHostPath,
					RouterName:  routerName,
					OtherRouter: previous,
				})
			} else {
				routeByHostPath[key] = routerName
			}
		}

		if missingServiceTarget(file.HTTP.Services, router.Service) {
			result.Diagnostics = append(result.Diagnostics, RouteArtifactDiagnostic{
				Code:        RouteArtifactMissingServiceTarget,
				RouterName:  routerName,
				ServiceName: router.Service,
			})
		}

		if router.TLS != nil && strings.TrimSpace(router.TLS.CertResolver) != "" && !hasEntryPoint(router.EntryPoints, "websecure") {
			result.Diagnostics = append(result.Diagnostics, RouteArtifactDiagnostic{
				Code:       RouteArtifactInvalidHTTPSResolver,
				RouterName: routerName,
			})
		}
	}
	return result
}

func missingServiceTarget(services map[string]TraefikService, serviceName string) bool {
	if serviceName == ACMEChallengeServiceName {
		return false
	}
	if strings.TrimSpace(serviceName) == "" {
		return true
	}
	service, ok := services[serviceName]
	if !ok || len(service.LoadBalancer.Servers) == 0 {
		return true
	}
	for _, server := range service.LoadBalancer.Servers {
		parsed, err := url.ParseRequestURI(server.URL)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return false
		}
	}
	return true
}

func hostPathFromRule(rule string) (string, string, bool) {
	host, hostOK := extractRuleValue(rule, "Host(`")
	path, pathOK := extractRuleValue(rule, "PathPrefix(`")
	return host, path, hostOK && pathOK
}

func extractRuleValue(rule, marker string) (string, bool) {
	start := strings.Index(rule, marker)
	if start < 0 {
		return "", false
	}
	start += len(marker)
	end := strings.Index(rule[start:], "`)")
	if end < 0 {
		return "", false
	}
	return rule[start : start+end], true
}

func hasEntryPoint(entryPoints []string, want string) bool {
	for _, entryPoint := range entryPoints {
		if entryPoint == want {
			return true
		}
	}
	return false
}

func supportHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func hashPrefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
