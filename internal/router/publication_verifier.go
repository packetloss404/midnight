package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// HTTPTraefikPublicationVerifier verifies the observations a file write cannot
// prove itself: Traefik must expose the exact revision-scoped router rules and
// complete backend sets rendered into the newly written artifact, and the
// explicitly configured ingress address must dispatch every desired Host.
// IngressBaseURL is an operator-trusted address (for example
// http://127.0.0.1:8080), not public DNS.
type HTTPTraefikPublicationVerifier struct {
	TraefikAPIBaseURL string
	IngressBaseURL    string
	Client            *http.Client
	Now               func() time.Time
	// AcknowledgementTimeout and AcknowledgementInterval bound the wait for
	// Traefik's asynchronous file-provider reload. Zero values use defaults.
	AcknowledgementTimeout  time.Duration
	AcknowledgementInterval time.Duration
}

type activeTraefikRouter struct {
	Name         string
	Provider     string
	Status       string
	Rule         string
	Service      string
	EntryPoints  []string
	Middlewares  []string
	TLSEnabled   bool
	CertResolver string
}

type activeTraefikService struct {
	Name         string
	Provider     string
	Status       string
	ServerURLs   []string
	ServerStatus map[string]string
}

func (v *HTTPTraefikPublicationVerifier) VerifyRoutePublication(
	ctx context.Context,
	group DurableRouteGroup,
	routes []RouteInput,
	artifactHash string,
) (RoutePublicationVerification, error) {
	result := RoutePublicationVerification{ObservedAt: v.now()}
	if len(routes) == 0 {
		result.ErrorDetail = "no public routes were eligible for verification"
		return result, errors.New(result.ErrorDetail)
	}
	if strings.TrimSpace(v.TraefikAPIBaseURL) == "" ||
		strings.TrimSpace(v.IngressBaseURL) == "" {
		result.ErrorDetail = "Traefik API and trusted ingress probe addresses are required"
		return result, errors.New(result.ErrorDetail)
	}
	if strings.TrimSpace(group.ProjectID) == "" ||
		strings.TrimSpace(group.EnvironmentID) == "" ||
		group.DesiredRevision < 1 {
		result.ErrorDetail = "route-group identity and positive desired revision are required for verification"
		return result, errors.New(result.ErrorDetail)
	}

	expectedToken := durableRoutePublicationToken(group)
	for _, route := range routes {
		if route.PublicationToken == "" ||
			route.PublicationToken != expectedToken {
			result.ErrorDetail = "route publication token does not match the desired route-group revision"
			return result, errors.New(result.ErrorDetail)
		}
	}
	rendered, err := RenderTraefikDynamicYAML(routes)
	if err != nil {
		result.ErrorDetail = "render expected route artifact for verification failed"
		return result, fmt.Errorf("%s: %w", result.ErrorDetail, err)
	}
	if strings.TrimSpace(artifactHash) == "" ||
		RenderedConfigHash(rendered) != strings.TrimSpace(artifactHash) {
		result.ErrorDetail = "published route artifact hash does not match the desired route set"
		return result, errors.New(result.ErrorDetail)
	}
	expected, err := RenderTraefikFile(routes)
	if err != nil {
		result.ErrorDetail = "build expected Traefik configuration failed"
		return result, fmt.Errorf("%s: %w", result.ErrorDetail, err)
	}

	// Traefik's file provider reloads asynchronously, so the router is not
	// visible the instant the file lands. Checking once made publication fail
	// with "Traefik has not acknowledged the desired route revision" purely on
	// timing, and the promoter then rolled the whole route group back. Retry
	// until Traefik catches up or the budget expires.
	deadline := v.now().Add(v.acknowledgementTimeout())
	for attempt := 0; ; attempt++ {
		result, err = v.verifyOnce(ctx, group, routes, expected)
		if err == nil || !errors.Is(err, errTraefikNotYetAcknowledged) {
			return result, err
		}
		if !v.now().Before(deadline) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(v.acknowledgementInterval()):
		}
	}
}

// errTraefikNotYetAcknowledged marks the verification failures that are just
// Traefik not having reloaded yet, and are therefore worth retrying. A state
// mismatch or a failed ingress probe is not retried: those are real
// disagreements with the desired artifact.
var errTraefikNotYetAcknowledged = errors.New("traefik has not acknowledged the desired revision yet")

func (v *HTTPTraefikPublicationVerifier) acknowledgementTimeout() time.Duration {
	if v != nil && v.AcknowledgementTimeout > 0 {
		return v.AcknowledgementTimeout
	}
	return 15 * time.Second
}

func (v *HTTPTraefikPublicationVerifier) acknowledgementInterval() time.Duration {
	if v != nil && v.AcknowledgementInterval > 0 {
		return v.AcknowledgementInterval
	}
	return 500 * time.Millisecond
}

func (v *HTTPTraefikPublicationVerifier) verifyOnce(
	ctx context.Context,
	_ DurableRouteGroup,
	routes []RouteInput,
	expected TraefikFile,
) (RoutePublicationVerification, error) {
	result := RoutePublicationVerification{ObservedAt: v.now()}
	activeRouters, err := v.activeFileRouters(ctx)
	if err != nil {
		result.ErrorDetail = err.Error()
		return result, err
	}
	activeServices, err := v.activeFileServices(ctx)
	if err != nil {
		result.ErrorDetail = err.Error()
		return result, err
	}
	for _, route := range routes {
		routerName, serviceName := routePublicationResourceNames(route)
		expectedRouter := expected.HTTP.Routers[routerName]
		activeRouter, ok := activeRouters[routerName]
		if !ok {
			result.ErrorDetail = "Traefik has not acknowledged the desired route revision"
			return result, fmt.Errorf("%w: %s", errTraefikNotYetAcknowledged, result.ErrorDetail)
		}
		if !equivalentActiveTraefikRouter(activeRouter, expectedRouter) {
			result.ErrorDetail = "Traefik router state does not match the desired route artifact"
			return result, errors.New(result.ErrorDetail)
		}
		expectedService := expected.HTTP.Services[serviceName]
		activeService, ok := activeServices[serviceName]
		if !ok {
			result.ErrorDetail = "Traefik has not acknowledged the desired route backend service"
			return result, fmt.Errorf("%w: %s", errTraefikNotYetAcknowledged, result.ErrorDetail)
		}
		if !equivalentActiveTraefikService(activeService, expectedService) {
			result.ErrorDetail = "Traefik backend state does not match the desired route artifact"
			return result, errors.New(result.ErrorDetail)
		}
		for otherName, other := range activeRouters {
			if otherName != routerName && other.Rule == expectedRouter.Rule {
				result.ErrorDetail = "Traefik still has a conflicting stale route revision active"
				return result, fmt.Errorf("%w: %s", errTraefikNotYetAcknowledged, result.ErrorDetail)
			}
		}
	}
	result.TraefikAcknowledged = true

	for _, route := range routes {
		probeURL, status, err := v.probeIngress(ctx, route)
		result.ProbeURL = probeURL
		result.ProbeStatus = status
		if err != nil {
			result.ErrorDetail = fmt.Sprintf(
				"route %s verification failed: %v",
				route.Name,
				err,
			)
			return result, errors.New(result.ErrorDetail)
		}
	}
	result.IngressProbeSucceeded = true
	return result, nil
}

func (v *HTTPTraefikPublicationVerifier) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func (v *HTTPTraefikPublicationVerifier) httpClient() *http.Client {
	if v != nil && v.Client != nil {
		return v.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (v *HTTPTraefikPublicationVerifier) activeFileRouters(
	ctx context.Context,
) (map[string]activeTraefikRouter, error) {
	endpoint := strings.TrimRight(
		strings.TrimSpace(v.TraefikAPIBaseURL),
		"/",
	) + "/api/http/routers"
	var rows []struct {
		Name        string   `json:"name"`
		Provider    string   `json:"provider"`
		Status      string   `json:"status"`
		Rule        string   `json:"rule"`
		Service     string   `json:"service"`
		EntryPoints []string `json:"entryPoints"`
		Middlewares []string `json:"middlewares"`
		TLS         *struct {
			CertResolver string `json:"certResolver"`
		} `json:"tls"`
	}
	if err := v.getTraefikAPIJSON(ctx, endpoint, &rows); err != nil {
		return nil, fmt.Errorf("Traefik router acknowledgement: %w", err)
	}
	out := make(map[string]activeTraefikRouter, len(rows))
	for _, row := range rows {
		name, provider := normalizedTraefikFileIdentity(row.Name, row.Provider)
		if provider != "file" ||
			!strings.EqualFold(strings.TrimSpace(row.Status), "enabled") {
			continue
		}
		if name == "" {
			continue
		}
		if _, duplicate := out[name]; duplicate {
			return nil, errors.New("Traefik router acknowledgement returned duplicate file resources")
		}
		value := activeTraefikRouter{
			Name:        name,
			Provider:    provider,
			Status:      "enabled",
			Rule:        strings.TrimSpace(row.Rule),
			Service:     normalizedTraefikFileReference(row.Service),
			EntryPoints: normalizedStringSet(row.EntryPoints, false),
			Middlewares: normalizedStringSet(row.Middlewares, true),
			TLSEnabled:  row.TLS != nil,
		}
		if row.TLS != nil {
			value.CertResolver = normalizedTraefikFileReference(
				row.TLS.CertResolver,
			)
		}
		out[name] = value
	}
	return out, nil
}

func (v *HTTPTraefikPublicationVerifier) activeFileServices(
	ctx context.Context,
) (map[string]activeTraefikService, error) {
	endpoint := strings.TrimRight(
		strings.TrimSpace(v.TraefikAPIBaseURL),
		"/",
	) + "/api/http/services"
	var rows []struct {
		Name         string `json:"name"`
		Provider     string `json:"provider"`
		Status       string `json:"status"`
		LoadBalancer struct {
			Servers []struct {
				URL string `json:"url"`
			} `json:"servers"`
		} `json:"loadBalancer"`
		ServerStatus map[string]string `json:"serverStatus"`
	}
	if err := v.getTraefikAPIJSON(ctx, endpoint, &rows); err != nil {
		return nil, fmt.Errorf("Traefik service acknowledgement: %w", err)
	}
	out := make(map[string]activeTraefikService, len(rows))
	for _, row := range rows {
		name, provider := normalizedTraefikFileIdentity(row.Name, row.Provider)
		if provider != "file" ||
			!strings.EqualFold(strings.TrimSpace(row.Status), "enabled") {
			continue
		}
		if name == "" {
			continue
		}
		if _, duplicate := out[name]; duplicate {
			return nil, errors.New("Traefik service acknowledgement returned duplicate file resources")
		}
		value := activeTraefikService{
			Name:         name,
			Provider:     provider,
			Status:       "enabled",
			ServerURLs:   make([]string, 0, len(row.LoadBalancer.Servers)),
			ServerStatus: make(map[string]string, len(row.ServerStatus)),
		}
		for _, server := range row.LoadBalancer.Servers {
			value.ServerURLs = append(
				value.ServerURLs,
				normalizedBackendURL(server.URL),
			)
		}
		sort.Strings(value.ServerURLs)
		for raw, status := range row.ServerStatus {
			value.ServerStatus[normalizedBackendURL(raw)] = strings.ToUpper(
				strings.TrimSpace(status),
			)
		}
		out[name] = value
	}
	return out, nil
}

func (v *HTTPTraefikPublicationVerifier) getTraefikAPIJSON(
	ctx context.Context,
	endpoint string,
	target any,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	res, err := v.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("request returned %s", res.Status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func equivalentActiveTraefikRouter(
	active activeTraefikRouter,
	expected TraefikRouter,
) bool {
	expectedTLS := expected.TLS != nil
	expectedResolver := ""
	if expected.TLS != nil {
		expectedResolver = normalizedTraefikFileReference(
			expected.TLS.CertResolver,
		)
	}
	return active.Provider == "file" &&
		active.Status == "enabled" &&
		active.Rule == expected.Rule &&
		active.Service == normalizedTraefikFileReference(expected.Service) &&
		equivalentStringSets(
			active.EntryPoints,
			normalizedStringSet(expected.EntryPoints, false),
		) &&
		equivalentStringSets(
			active.Middlewares,
			normalizedStringSet(expected.Middlewares, true),
		) &&
		active.TLSEnabled == expectedTLS &&
		active.CertResolver == expectedResolver
}

func equivalentActiveTraefikService(
	active activeTraefikService,
	expected TraefikService,
) bool {
	if active.Provider != "file" || active.Status != "enabled" {
		return false
	}
	expectedURLs := make([]string, 0, len(expected.LoadBalancer.Servers))
	for _, server := range expected.LoadBalancer.Servers {
		expectedURLs = append(expectedURLs, normalizedBackendURL(server.URL))
	}
	sort.Strings(expectedURLs)
	if !equivalentStringSets(active.ServerURLs, expectedURLs) {
		return false
	}
	for _, raw := range expectedURLs {
		if active.ServerStatus[raw] != "UP" {
			return false
		}
	}
	return true
}

func normalizedTraefikFileIdentity(name, provider string) (string, string) {
	name = strings.TrimSpace(name)
	provider = strings.TrimSpace(provider)
	if strings.HasSuffix(name, "@file") {
		name = strings.TrimSuffix(name, "@file")
		provider = "file"
	}
	return name, provider
}

func normalizedTraefikFileReference(value string) string {
	value = strings.TrimSpace(value)
	return strings.TrimSuffix(value, "@file")
}

func normalizedStringSet(values []string, trimFileProvider bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if trimFileProvider {
			value = normalizedTraefikFileReference(value)
		}
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func equivalentStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizedBackendURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

func (v *HTTPTraefikPublicationVerifier) probeIngress(
	ctx context.Context,
	route RouteInput,
) (string, int, error) {
	base, err := url.Parse(strings.TrimSpace(v.IngressBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", 0, errors.New("trusted ingress probe address is invalid")
	}
	path := route.PathPrefix
	if path == "" {
		path = "/"
	}
	base.Path = path
	base.RawQuery = ""
	base.Fragment = ""
	probeURL := base.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return probeURL, 0, fmt.Errorf("build ingress probe: %w", err)
	}
	req.Host = route.Host
	req.Header.Set("X-Midnight-Route-Probe", "1")
	res, err := v.httpClient().Do(req)
	if err != nil {
		return probeURL, 0, fmt.Errorf("ingress probe failed: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	// The exact active router/service/backend comparison above proves which
	// revision owns this Host. A non-5xx response can therefore be accepted as
	// application dispatch evidence without pretending a user's root path must
	// return 2xx.
	if res.StatusCode < 200 || res.StatusCode >= 500 {
		return probeURL, res.StatusCode, fmt.Errorf(
			"ingress probe returned %s",
			res.Status,
		)
	}
	return probeURL, res.StatusCode, nil
}
