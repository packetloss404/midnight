package runtime

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultStartupTimeout pins the floor for startup-probe deadlines.
	DefaultStartupTimeout = 180 * time.Second
	// DefaultHealthInterval matches the panel-locked default probe interval.
	DefaultHealthInterval = 10 * time.Second
	// DefaultHealthTimeout matches the panel-locked default per-probe timeout.
	DefaultHealthTimeout = 5 * time.Second
	// DefaultHealthRetries pins the readiness/liveness retry budget.
	DefaultHealthRetries = 3
)

// HealthCheckType is the panel-locked probe phase.
type HealthCheckType string

const (
	HealthCheckStartup   HealthCheckType = "startup"
	HealthCheckReadiness HealthCheckType = "readiness"
	HealthCheckLiveness  HealthCheckType = "liveness"
)

// HealthCheckShape enumerates the three classic probe shapes the agent
// supports: HTTP, TCP, and exec. The shape is derived from the populated
// fields and is exposed for downstream code that needs to dispatch probes.
type HealthCheckShape string

const (
	HealthCheckShapeUnknown HealthCheckShape = ""
	HealthCheckShapeHTTP    HealthCheckShape = "http"
	HealthCheckShapeTCP     HealthCheckShape = "tcp"
	HealthCheckShapeExec    HealthCheckShape = "exec"
)

// HealthCheckSpec is a pure taxonomy model for container health probes.
// Phase 1 carries enough fields to dispatch the panel-locked HTTP/TCP/exec
// shapes with deterministic timeouts, intervals, and retries.
type HealthCheckSpec struct {
	Type        HealthCheckType
	Command     []string
	HTTPPath    string
	HTTPHost    string
	HTTPPort    int
	HTTPMethod  string
	TCPPort     int
	Timeout     time.Duration
	Interval    time.Duration
	Retries     int
	StartPeriod time.Duration
}

// Shape returns the probe shape implied by populated fields. A valid
// HealthCheckSpec has exactly one shape.
func (h HealthCheckSpec) Shape() HealthCheckShape {
	switch {
	case len(h.Command) > 0:
		return HealthCheckShapeExec
	case strings.TrimSpace(h.HTTPPath) != "":
		return HealthCheckShapeHTTP
	case h.TCPPort > 0:
		return HealthCheckShapeTCP
	default:
		return HealthCheckShapeUnknown
	}
}

// NormalizeHealthChecks returns a deterministic copy and applies Phase 0 defaults.
func NormalizeHealthChecks(checks []HealthCheckSpec) []HealthCheckSpec {
	normalized := make([]HealthCheckSpec, 0, len(checks))
	for _, check := range checks {
		check.Type = HealthCheckType(strings.TrimSpace(string(check.Type)))
		check.HTTPPath = strings.TrimSpace(check.HTTPPath)
		check.HTTPHost = strings.TrimSpace(check.HTTPHost)
		check.HTTPMethod = strings.ToUpper(strings.TrimSpace(check.HTTPMethod))
		check.Command = append([]string(nil), check.Command...)
		if check.Type == HealthCheckStartup && check.Timeout == 0 {
			check.Timeout = DefaultStartupTimeout
		}
		if check.Type != HealthCheckStartup && check.Timeout == 0 {
			check.Timeout = DefaultHealthTimeout
		}
		if check.Interval == 0 {
			check.Interval = DefaultHealthInterval
		}
		if check.Retries == 0 {
			check.Retries = DefaultHealthRetries
		}
		if check.HTTPMethod == "" && check.HTTPPath != "" {
			check.HTTPMethod = "GET"
		}
		normalized = append(normalized, check)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Type < normalized[j].Type })
	return normalized
}

// ValidateHealthChecks validates health probe taxonomy without performing probes.
func ValidateHealthChecks(checks []HealthCheckSpec) error {
	seen := make(map[HealthCheckType]struct{}, len(checks))
	for _, check := range NormalizeHealthChecks(checks) {
		switch check.Type {
		case HealthCheckStartup, HealthCheckReadiness, HealthCheckLiveness:
		default:
			return fmt.Errorf("health check type %q is not allowed", check.Type)
		}
		if _, ok := seen[check.Type]; ok {
			return fmt.Errorf("health check type %q must be unique", check.Type)
		}
		seen[check.Type] = struct{}{}

		if check.Type == HealthCheckStartup && check.Timeout < DefaultStartupTimeout {
			return fmt.Errorf("startup health check timeout must be at least %s", DefaultStartupTimeout)
		}
		if check.Timeout < 0 || check.Interval < 0 || check.StartPeriod < 0 {
			return fmt.Errorf("health check %q durations must not be negative", check.Type)
		}
		if check.Retries < 0 {
			return fmt.Errorf("health check %q retries must not be negative", check.Type)
		}
		if probeTargetCount(check) != 1 {
			return fmt.Errorf("health check %q must define exactly one probe target", check.Type)
		}
		switch check.Shape() {
		case HealthCheckShapeHTTP:
			if check.HTTPPort < 0 || check.HTTPPort > 65535 {
				return fmt.Errorf("health check %q http port %d is out of range", check.Type, check.HTTPPort)
			}
			if !strings.HasPrefix(check.HTTPPath, "/") {
				return fmt.Errorf("health check %q http path must start with /", check.Type)
			}
			switch check.HTTPMethod {
			case "GET", "HEAD":
			default:
				return fmt.Errorf("health check %q http method %q is not allowed", check.Type, check.HTTPMethod)
			}
		case HealthCheckShapeTCP:
			if check.TCPPort <= 0 || check.TCPPort > 65535 {
				return fmt.Errorf("health check %q tcp port %d is out of range", check.Type, check.TCPPort)
			}
		case HealthCheckShapeExec:
			if len(check.Command) == 0 {
				return fmt.Errorf("health check %q exec command must not be empty", check.Type)
			}
		default:
			return fmt.Errorf("health check %q has no probe target", check.Type)
		}
	}
	return nil
}

// ParseHTTPHealthCheck builds an HTTP health check from a raw URL such as
// "http://localhost:8080/healthz" with optional method override.
func ParseHTTPHealthCheck(checkType HealthCheckType, raw, method string) (HealthCheckSpec, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return HealthCheckSpec{}, fmt.Errorf("parse http health check url %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return HealthCheckSpec{}, fmt.Errorf("http health check url %q must be http or https", raw)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	port := 0
	if portString := parsed.Port(); portString != "" {
		parsedPort, err := strconv.Atoi(portString)
		if err != nil {
			return HealthCheckSpec{}, fmt.Errorf("parse http health check port %q: %w", portString, err)
		}
		port = parsedPort
	}
	if method == "" {
		method = "GET"
	}
	return HealthCheckSpec{
		Type:       checkType,
		HTTPPath:   parsed.Path,
		HTTPHost:   parsed.Hostname(),
		HTTPPort:   port,
		HTTPMethod: strings.ToUpper(strings.TrimSpace(method)),
	}, nil
}

// ParseTCPHealthCheck builds a TCP health check from a "host:port" target.
func ParseTCPHealthCheck(checkType HealthCheckType, raw string) (HealthCheckSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return HealthCheckSpec{}, fmt.Errorf("tcp health check target is required")
	}
	host, port, err := splitTCPTarget(raw)
	if err != nil {
		return HealthCheckSpec{}, err
	}
	return HealthCheckSpec{
		Type:     checkType,
		HTTPHost: host,
		TCPPort:  port,
	}, nil
}

// ParseExecHealthCheck builds an exec health check from an argv slice.
func ParseExecHealthCheck(checkType HealthCheckType, command []string) (HealthCheckSpec, error) {
	if len(command) == 0 {
		return HealthCheckSpec{}, fmt.Errorf("exec health check command must not be empty")
	}
	return HealthCheckSpec{Type: checkType, Command: append([]string(nil), command...)}, nil
}

func splitTCPTarget(raw string) (string, int, error) {
	idx := strings.LastIndex(raw, ":")
	if idx <= 0 || idx == len(raw)-1 {
		return "", 0, fmt.Errorf("tcp health check target %q must be host:port", raw)
	}
	host := strings.TrimSpace(raw[:idx])
	portString := strings.TrimSpace(raw[idx+1:])
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, fmt.Errorf("tcp health check port %q: %w", portString, err)
	}
	if port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("tcp health check port %d is out of range", port)
	}
	return host, port, nil
}

func probeTargetCount(check HealthCheckSpec) int {
	count := 0
	if len(check.Command) > 0 {
		count++
	}
	if check.HTTPPath != "" {
		count++
	}
	if check.TCPPort > 0 {
		count++
	}
	return count
}
