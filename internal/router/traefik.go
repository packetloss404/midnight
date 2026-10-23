package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultIngressReadTimeout        = 30 * time.Second
	DefaultIngressWriteTimeout       = 30 * time.Second
	DefaultIngressIdleTimeout        = 120 * time.Second
	DefaultIngressMaxBodyBytes int64 = 32 << 20

	ACMEChallengeRouterName  = "acme-http01-challenge"
	ACMEChallengeServiceName = "acme-http@internal"
	ACMEChallengeEntryPoint  = "web"
	ACMEChallengePriority    = 2147483647

	maxRouteIdentifierLength = 128
	maxRouteHostLength       = 253
	maxRoutePathPrefixLength = 2048
)

// RouteInput is the control-plane contract for a single tenant ingress route.
type RouteInput struct {
	Name       string
	Host       string
	PathPrefix string
	ServiceURL string
	// ServiceURLs is the complete ready-backend set for a durable route.
	// ServiceURL remains the backwards-compatible single-backend form. When
	// both are set, ServiceURLs is authoritative and ServiceURL must be one of
	// its values.
	ServiceURLs  []string
	TLS          bool
	CertResolver string
	Middlewares  []string
	// PublicationToken scopes the rendered Traefik router and service resource
	// identities to one durable route-group revision. The human-facing route
	// name remains stable, while Traefik acknowledgement can distinguish the
	// exact newly rendered revision from an older active configuration that used
	// the same route name.
	PublicationToken string
}

// TraefikFile is the in-memory shape rendered for Traefik's file provider.
type TraefikFile struct {
	HTTP TraefikHTTP `json:"http"`
}

type TraefikHTTP struct {
	Routers  map[string]TraefikRouter  `json:"routers"`
	Services map[string]TraefikService `json:"services"`
}

type TraefikRouter struct {
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	TLS         *TLS     `json:"tls,omitempty"`
	Middlewares []string `json:"middlewares,omitempty"`
}

type TLS struct {
	CertResolver string `json:"certResolver,omitempty"`
}

type TraefikService struct {
	LoadBalancer TraefikLoadBalancer `json:"loadBalancer"`
}

type TraefikLoadBalancer struct {
	Servers []TraefikServer `json:"servers"`
}

type TraefikServer struct {
	URL string `json:"url"`
}

func RenderTraefikFile(routes []RouteInput) (TraefikFile, error) {
	file := TraefikFile{HTTP: TraefikHTTP{
		Routers:  make(map[string]TraefikRouter, len(routes)+1),
		Services: make(map[string]TraefikService, len(routes)),
	}}
	file.HTTP.Routers[ACMEChallengeRouterName] = TraefikRouter{
		Rule:        fmt.Sprintf("PathPrefix(`%s`)", reservedACMEPathPrefix),
		Service:     ACMEChallengeServiceName,
		EntryPoints: []string{ACMEChallengeEntryPoint},
		Priority:    ACMEChallengePriority,
	}

	for _, route := range routes {
		route = normalizeRoute(route)
		if err := validateRoute(route); err != nil {
			return TraefikFile{}, err
		}

		routerName, serviceName := routePublicationResourceNames(route)
		router := TraefikRouter{
			Rule:        fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", route.Host, route.PathPrefix),
			Service:     serviceName,
			EntryPoints: []string{"web"},
			Middlewares: append([]string(nil), route.Middlewares...),
		}
		if route.TLS {
			router.EntryPoints = []string{"websecure"}
			router.TLS = &TLS{CertResolver: route.CertResolver}
		}

		file.HTTP.Routers[routerName] = router
		servers := make([]TraefikServer, 0, len(route.ServiceURLs))
		for _, serviceURL := range route.ServiceURLs {
			servers = append(servers, TraefikServer{URL: serviceURL})
		}
		file.HTTP.Services[serviceName] = TraefikService{LoadBalancer: TraefikLoadBalancer{Servers: servers}}
	}

	return file, nil
}

// RenderTraefikDynamicYAML renders the Traefik file-provider dynamic config in
// deterministic key order without taking a dependency on a YAML library.
func RenderTraefikDynamicYAML(routes []RouteInput) (string, error) {
	file, err := RenderTraefikFile(routes)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	out.WriteString("http:\n")
	out.WriteString("  routers:\n")
	for _, name := range sortedRouterNames(file.HTTP.Routers) {
		router := file.HTTP.Routers[name]
		writeYAMLKey(&out, 4, name)
		writeYAMLScalar(&out, 6, "rule", router.Rule)
		writeYAMLScalar(&out, 6, "service", router.Service)
		writeYAMLList(&out, 6, "entryPoints", router.EntryPoints)
		if router.Priority != 0 {
			writeYAMLInt(&out, 6, "priority", router.Priority)
		}
		if router.TLS != nil {
			if router.TLS.CertResolver == "" {
				out.WriteString("      tls: {}\n")
			} else {
				out.WriteString("      tls:\n")
				writeYAMLScalar(&out, 8, "certResolver", router.TLS.CertResolver)
			}
		}
		writeYAMLList(&out, 6, "middlewares", router.Middlewares)
	}

	out.WriteString("  services:\n")
	for _, name := range sortedServiceNames(file.HTTP.Services) {
		service := file.HTTP.Services[name]
		writeYAMLKey(&out, 4, name)
		out.WriteString("      loadBalancer:\n")
		out.WriteString("        servers:\n")
		for _, server := range service.LoadBalancer.Servers {
			out.WriteString("          - url: ")
			out.WriteString(yamlQuote(server.URL))
			out.WriteByte('\n')
		}
	}

	return out.String(), nil
}

func normalizeRoute(route RouteInput) RouteInput {
	if route.PathPrefix == "" {
		route.PathPrefix = "/"
	}
	if len(route.ServiceURLs) == 0 && route.ServiceURL != "" {
		route.ServiceURLs = []string{route.ServiceURL}
	}
	return route
}

// ValidateRouteInput validates every value that can influence a Traefik YAML
// key, rule, reference, or backend URL. Renderers call the same validator
// immediately before constructing rules so callers cannot bypass it.
func ValidateRouteInput(route RouteInput) error {
	return validateRoute(normalizeRoute(route))
}

// ValidateRouteGroup validates a publication group before it is used as an
// artifact directory name. Publication rejects unsafe input instead of
// silently sanitizing two distinct tenant values into the same group.
func ValidateRouteGroup(routeGroup string) error {
	return validateRouteIdentifier("route group", routeGroup)
}

// ValidateRouteName applies the same strict identifier contract used by the
// renderer before a route name is used as a YAML key or Traefik reference.
func ValidateRouteName(routeName string) error {
	return validateRouteIdentifier("route name", routeName)
}

func validateRoute(route RouteInput) error {
	if err := ValidateRouteName(route.Name); err != nil {
		return err
	}
	if route.PublicationToken != "" {
		if err := validateRouteIdentifier("publication token", route.PublicationToken); err != nil {
			return fmt.Errorf("route %q %w", route.Name, err)
		}
	}
	if err := validateRouteHost(route.Host); err != nil {
		return fmt.Errorf("route %q %w", route.Name, err)
	}
	if err := validateRoutePathPrefix(route.PathPrefix); err != nil {
		return fmt.Errorf("route %q %w", route.Name, err)
	}
	if IsReservedACMEPath(route.PathPrefix) {
		return fmt.Errorf("route %q path prefix %q is reserved for ACME challenges", route.Name, route.PathPrefix)
	}
	if strings.TrimSpace(route.CertResolver) != "" && !route.TLS {
		return fmt.Errorf("route %q cert resolver requires TLS", route.Name)
	}
	if route.CertResolver != "" {
		if err := validateRouteIdentifier("cert resolver", route.CertResolver); err != nil {
			return fmt.Errorf("route %q %w", route.Name, err)
		}
	}
	for _, middleware := range route.Middlewares {
		if err := validateRouteIdentifier("middleware", middleware); err != nil {
			return fmt.Errorf("route %q %w", route.Name, err)
		}
	}
	if len(route.ServiceURLs) == 0 {
		return fmt.Errorf("route %q service URL is required", route.Name)
	}
	seenServiceURLs := make(map[string]struct{}, len(route.ServiceURLs))
	for _, serviceURL := range route.ServiceURLs {
		if err := validateRouteServiceURL(serviceURL); err != nil {
			return fmt.Errorf("route %q %w", route.Name, err)
		}
		if _, duplicate := seenServiceURLs[serviceURL]; duplicate {
			return fmt.Errorf("route %q service URL is duplicated", route.Name)
		}
		seenServiceURLs[serviceURL] = struct{}{}
	}
	if route.ServiceURL != "" {
		if _, included := seenServiceURLs[route.ServiceURL]; !included {
			return fmt.Errorf("route %q primary service URL is not in service URLs", route.Name)
		}
	}
	return nil
}

// routePublicationResourceNames returns the Traefik API identities rendered for
// a route. Non-durable callers retain the historical stable names. Durable
// route publications use fixed-length, revision-scoped identities so a stale
// Traefik configuration cannot masquerade as acknowledgement of a newer file.
func routePublicationResourceNames(route RouteInput) (string, string) {
	if route.PublicationToken == "" {
		return route.Name, route.Name + "-service"
	}
	sum := sha256.Sum256([]byte(route.Name + "\x00" + route.PublicationToken))
	suffix := hex.EncodeToString(sum[:16])
	return "mrt-" + suffix, "mrs-" + suffix
}

func validateRouteIdentifier(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain surrounding whitespace", field)
	}
	if len(value) > maxRouteIdentifierLength {
		return fmt.Errorf("%s exceeds %d characters", field, maxRouteIdentifierLength)
	}
	for index, char := range []byte(value) {
		valid := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			(index > 0 && (char == '.' || char == '_' || char == '-'))
		if !valid {
			return fmt.Errorf("%s contains an invalid character", field)
		}
	}
	last := value[len(value)-1]
	if !(last >= 'a' && last <= 'z' || last >= 'A' && last <= 'Z' || last >= '0' && last <= '9') {
		return fmt.Errorf("%s must end with a letter or digit", field)
	}
	return nil
}

func validateRouteHost(host string) error {
	if host == "" {
		return errors.New("host is required")
	}
	if host != strings.TrimSpace(host) {
		return errors.New("host must not contain surrounding whitespace")
	}
	if len(host) > maxRouteHostLength {
		return fmt.Errorf("host exceeds %d characters", maxRouteHostLength)
	}
	if strings.Contains(host, "*") {
		return errors.New("host wildcards are not supported")
	}
	if strings.Contains(host, ":") {
		return errors.New("host must not include a port")
	}
	if net.ParseIP(host) != nil {
		return errors.New("host must be a DNS name")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return errors.New("host contains an invalid DNS label")
		}
		for index, char := range []byte(label) {
			if !(char >= 'a' && char <= 'z' ||
				char >= 'A' && char <= 'Z' ||
				char >= '0' && char <= '9' ||
				(char == '-' && index > 0 && index < len(label)-1)) {
				return errors.New("host contains an invalid DNS label")
			}
		}
	}
	return nil
}

func validateRoutePathPrefix(pathPrefix string) error {
	if pathPrefix == "" {
		return errors.New("path prefix is required")
	}
	if pathPrefix != strings.TrimSpace(pathPrefix) {
		return errors.New("path prefix must not contain surrounding whitespace")
	}
	if len(pathPrefix) > maxRoutePathPrefixLength {
		return fmt.Errorf("path prefix exceeds %d characters", maxRoutePathPrefixLength)
	}
	if !strings.HasPrefix(pathPrefix, "/") {
		return errors.New("path prefix must start with /")
	}
	if strings.ContainsAny(pathPrefix, "`\\*?#") || containsControl(pathPrefix) {
		return errors.New("path prefix contains an invalid rule character")
	}
	parsed, err := url.ParseRequestURI(pathPrefix)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("path prefix must be an absolute URL path")
	}
	decoded, err := url.PathUnescape(pathPrefix)
	if err != nil || strings.ContainsAny(decoded, "`\\*?#") || containsControl(decoded) {
		return errors.New("path prefix contains an invalid encoded rule character")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return errors.New("path prefix must not contain dot segments")
		}
	}
	return nil
}

func validateRouteServiceURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("service URL must be absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("service URL scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("service URL must not contain credentials, query, or fragment")
	}
	portText := parsed.Port()
	if portText == "" {
		return errors.New("service URL must include a port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("service URL port must be between 1 and 65535")
	}
	return nil
}

func containsControl(value string) bool {
	for _, char := range []byte(value) {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func sortedRouterNames(routers map[string]TraefikRouter) []string {
	names := make([]string, 0, len(routers))
	for name := range routers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedServiceNames(services map[string]TraefikService) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func writeYAMLKey(out *bytes.Buffer, indent int, key string) {
	out.WriteString(strings.Repeat(" ", indent))
	out.WriteString(key)
	out.WriteString(":\n")
}

func writeYAMLScalar(out *bytes.Buffer, indent int, key, value string) {
	out.WriteString(strings.Repeat(" ", indent))
	out.WriteString(key)
	out.WriteString(": ")
	out.WriteString(yamlQuote(value))
	out.WriteByte('\n')
}

func writeYAMLInt(out *bytes.Buffer, indent int, key string, value int) {
	out.WriteString(strings.Repeat(" ", indent))
	out.WriteString(key)
	out.WriteString(": ")
	out.WriteString(fmt.Sprintf("%d", value))
	out.WriteByte('\n')
}

func writeYAMLList(out *bytes.Buffer, indent int, key string, values []string) {
	if len(values) == 0 {
		return
	}
	out.WriteString(strings.Repeat(" ", indent))
	out.WriteString(key)
	out.WriteString(":\n")
	for _, value := range values {
		out.WriteString(strings.Repeat(" ", indent+2))
		out.WriteString("- ")
		out.WriteString(yamlQuote(value))
		out.WriteByte('\n')
	}
}

func yamlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
