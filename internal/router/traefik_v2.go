package router

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// RenderTraefikDynamicV2 renders the Traefik file-provider dynamic config
// including middlewares, TLS options, and per-router mTLS hints. It uses the
// same deterministic key ordering as RenderTraefikDynamicYAML and remains
// dependency-free.
func RenderTraefikDynamicV2(input RouteRenderInput) (string, error) {
	if err := validateRouteRenderInput(input); err != nil {
		return "", err
	}

	for _, def := range input.Middlewares {
		if err := validateMiddleware(def); err != nil {
			return "", err
		}
	}
	for _, opt := range input.TLSOptions {
		if err := validateTLSOption(opt); err != nil {
			return "", err
		}
	}

	tlsOptByName := make(map[string]TLSOptions, len(input.TLSOptions))
	for _, opt := range input.TLSOptions {
		tlsOptByName[opt.Name] = opt
	}
	mwByName := make(map[string]MiddlewareDefinition, len(input.Middlewares))
	for _, def := range input.Middlewares {
		mwByName[def.Name] = def
	}

	var out bytes.Buffer
	out.WriteString("http:\n")
	out.WriteString("  routers:\n")
	writeACMEChallengeRouterV2(&out)

	desiredRoutes := make([]RouteInputV2, len(input.Routes))
	copy(desiredRoutes, input.Routes)
	sort.SliceStable(desiredRoutes, func(i, j int) bool {
		return desiredRoutes[i].Name < desiredRoutes[j].Name
	})
	for _, route := range desiredRoutes {
		normalized := normalizeRoute(route.RouteInput)
		if err := validateRoute(normalized); err != nil {
			return "", err
		}
		if route.TLSOptionName != "" {
			if _, ok := tlsOptByName[route.TLSOptionName]; !ok {
				return "", fmt.Errorf("route %q references unknown TLS option %q", route.Name, route.TLSOptionName)
			}
		}
		for _, mw := range route.Middlewares {
			if _, ok := mwByName[mw]; !ok {
				return "", fmt.Errorf("route %q references unknown middleware %q", route.Name, mw)
			}
		}
		writeRouteRouterV2(&out, route, normalized)
	}

	out.WriteString("  services:\n")
	for _, route := range desiredRoutes {
		normalized := normalizeRoute(route.RouteInput)
		writeServiceBlock(&out, normalized)
	}

	if len(input.Middlewares) > 0 {
		out.WriteString("  middlewares:\n")
		mwNames := sortedMiddlewareNames(input.Middlewares)
		mwLookup := make(map[string]MiddlewareDefinition, len(input.Middlewares))
		for _, def := range input.Middlewares {
			mwLookup[def.Name] = def
		}
		for _, name := range mwNames {
			writeMiddlewareBlock(&out, mwLookup[name])
		}
	}

	if len(input.TLSOptions) > 0 {
		out.WriteString("tls:\n")
		out.WriteString("  options:\n")
		optNames := sortedTLSOptionNames(input.TLSOptions)
		optLookup := make(map[string]TLSOptions, len(input.TLSOptions))
		for _, opt := range input.TLSOptions {
			optLookup[opt.Name] = opt
		}
		for _, name := range optNames {
			writeTLSOptionBlock(&out, optLookup[name])
		}
	}

	return out.String(), nil
}

func validateRouteRenderInput(input RouteRenderInput) error {
	seen := make(map[string]struct{}, len(input.Routes))
	for _, route := range input.Routes {
		if strings.TrimSpace(route.Name) == "" {
			return errors.New("route name is required")
		}
		if _, dup := seen[route.Name]; dup {
			return fmt.Errorf("route %q is duplicated", route.Name)
		}
		seen[route.Name] = struct{}{}
		if route.ServiceURL != "" {
			if _, err := url.ParseRequestURI(route.ServiceURL); err != nil {
				return fmt.Errorf("route %q service URL invalid: %w", route.Name, err)
			}
		}
	}
	mwSeen := make(map[string]struct{}, len(input.Middlewares))
	for _, def := range input.Middlewares {
		if _, dup := mwSeen[def.Name]; dup {
			return fmt.Errorf("middleware %q is duplicated", def.Name)
		}
		mwSeen[def.Name] = struct{}{}
	}
	optSeen := make(map[string]struct{}, len(input.TLSOptions))
	for _, opt := range input.TLSOptions {
		if _, dup := optSeen[opt.Name]; dup {
			return fmt.Errorf("TLS option %q is duplicated", opt.Name)
		}
		optSeen[opt.Name] = struct{}{}
	}
	return nil
}

func writeACMEChallengeRouterV2(out *bytes.Buffer) {
	out.WriteString("    acme-http01-challenge:\n")
	writeYAMLScalar(out, 6, "rule", fmt.Sprintf("PathPrefix(`%s`)", reservedACMEPathPrefix))
	writeYAMLScalar(out, 6, "service", ACMEChallengeServiceName)
	writeYAMLList(out, 6, "entryPoints", []string{ACMEChallengeEntryPoint})
	writeYAMLInt(out, 6, "priority", ACMEChallengePriority)
}

func writeRouteRouterV2(out *bytes.Buffer, route RouteInputV2, normalized RouteInput) {
	routerName, serviceName := routePublicationResourceNames(normalized)
	writeYAMLKey(out, 4, routerName)
	writeYAMLScalar(out, 6, "rule", fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", normalized.Host, normalized.PathPrefix))
	writeYAMLScalar(out, 6, "service", serviceName)
	writeYAMLList(out, 6, "entryPoints", []string{"websecure"})
	if normalized.TLS {
		out.WriteString("      tls:\n")
		if normalized.CertResolver != "" {
			writeYAMLScalar(out, 8, "certResolver", normalized.CertResolver)
		}
		if route.TLSOptionName != "" {
			writeYAMLScalar(out, 8, "options", route.TLSOptionName)
		}
		if route.MTLSHint != "" {
			writeYAMLScalar(out, 8, "clientAuthHint", route.MTLSHint)
		}
		if normalized.CertResolver == "" && route.TLSOptionName == "" && route.MTLSHint == "" {
			// Force {} only when no nested keys are written.
			truncateLastNewline(out)
			out.WriteString(" {}\n")
		}
	}
	writeYAMLList(out, 6, "middlewares", normalized.Middlewares)
}

func writeServiceBlock(out *bytes.Buffer, normalized RouteInput) {
	_, serviceName := routePublicationResourceNames(normalized)
	writeYAMLKey(out, 4, serviceName)
	out.WriteString("      loadBalancer:\n")
	out.WriteString("        servers:\n")
	out.WriteString("          - url: ")
	out.WriteString(yamlQuote(normalized.ServiceURL))
	out.WriteByte('\n')
}

func writeMiddlewareBlock(out *bytes.Buffer, def MiddlewareDefinition) {
	writeYAMLKey(out, 4, def.Name)
	switch def.Kind {
	case MiddlewareKindCompress:
		out.WriteString("      compress:\n")
		if def.Compress != nil {
			if def.Compress.MinResponseBytes > 0 {
				writeYAMLInt(out, 8, "minResponseBodyBytes", def.Compress.MinResponseBytes)
			}
			if len(def.Compress.ExcludedContentTypes) > 0 {
				excluded := append([]string(nil), def.Compress.ExcludedContentTypes...)
				sort.Strings(excluded)
				writeYAMLList(out, 8, "excludedContentTypes", excluded)
			}
		}
	case MiddlewareKindRedirectScheme:
		out.WriteString("      redirectScheme:\n")
		writeYAMLScalar(out, 8, "scheme", def.RedirectScheme.Scheme)
		writeYAMLBool(out, 8, "permanent", def.RedirectScheme.Permanent)
		if def.RedirectScheme.Port != "" {
			writeYAMLScalar(out, 8, "port", def.RedirectScheme.Port)
		}
	case MiddlewareKindHeaders:
		out.WriteString("      headers:\n")
		if def.Headers.HSTSSeconds > 0 {
			writeYAMLInt(out, 8, "stsSeconds", def.Headers.HSTSSeconds)
			writeYAMLBool(out, 8, "stsIncludeSubdomains", def.Headers.HSTSIncludeSubdomains)
			writeYAMLBool(out, 8, "stsPreload", def.Headers.HSTSPreload)
		}
		if len(def.Headers.CustomRequestHeaders) > 0 {
			out.WriteString("        customRequestHeaders:\n")
			for _, key := range sortedHeaderKeys(def.Headers.CustomRequestHeaders) {
				writeYAMLScalar(out, 10, key, def.Headers.CustomRequestHeaders[key])
			}
		}
		if len(def.Headers.CustomResponseHeaders) > 0 {
			out.WriteString("        customResponseHeaders:\n")
			for _, key := range sortedHeaderKeys(def.Headers.CustomResponseHeaders) {
				writeYAMLScalar(out, 10, key, def.Headers.CustomResponseHeaders[key])
			}
		}
	}
}

func writeTLSOptionBlock(out *bytes.Buffer, opt TLSOptions) {
	writeYAMLKey(out, 4, opt.Name)
	if opt.MinVersion != "" {
		writeYAMLScalar(out, 6, "minVersion", opt.MinVersion)
	}
	if len(opt.CipherSuites) > 0 {
		suites := append([]string(nil), opt.CipherSuites...)
		sort.Strings(suites)
		writeYAMLList(out, 6, "cipherSuites", suites)
	}
	clientAuthType := strings.TrimSpace(opt.ClientAuthType)
	if clientAuthType == "" && opt.ClientAuthRequireMTLS {
		clientAuthType = "RequireAndVerifyClientCert"
	}
	if clientAuthType != "" {
		out.WriteString("      clientAuth:\n")
		writeYAMLScalar(out, 8, "clientAuthType", clientAuthType)
		if len(opt.ClientCAFiles) > 0 {
			files := append([]string(nil), opt.ClientCAFiles...)
			sort.Strings(files)
			writeYAMLList(out, 8, "caFiles", files)
		}
	}
}

func writeYAMLBool(out *bytes.Buffer, indent int, key string, value bool) {
	out.WriteString(strings.Repeat(" ", indent))
	out.WriteString(key)
	out.WriteString(": ")
	if value {
		out.WriteString("true")
	} else {
		out.WriteString("false")
	}
	out.WriteByte('\n')
}

func truncateLastNewline(out *bytes.Buffer) {
	bs := out.Bytes()
	if len(bs) == 0 {
		return
	}
	if bs[len(bs)-1] == '\n' {
		out.Truncate(out.Len() - 1)
	}
}
