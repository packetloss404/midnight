// Package tcpproxy plans externally reachable TCP proxy endpoints.
//
// It is intentionally separate from internal/router, which owns HTTP ingress.
package tcpproxy

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultExternalPortStart = 20000
	DefaultExternalPortEnd   = 29999

	DefaultHostPrefix = "tcp"
)

var labelPartPattern = regexp.MustCompile(`[^a-z0-9-]+`)

type SourceIPPolicy struct {
	AllowedCIDRs []string
}

type EndpointRequest struct {
	Enabled               bool
	ProjectID             string
	EnvironmentID         string
	ServiceID             string
	BaseDomain            string
	RequestedHost         string
	RequestedExternalPort int
	BackendHost           string
	BackendPort           int
	SourceIPPolicy        SourceIPPolicy
}

type ExistingEndpoint struct {
	Host         string
	ExternalPort int
}

type PortRange struct {
	Start int
	End   int
}

type PlanInput struct {
	Request  EndpointRequest
	Existing []ExistingEndpoint
	Ports    PortRange
}

type EndpointPlan struct {
	Enabled        bool
	Host           string
	ExternalPort   int
	BackendHost    string
	BackendPort    int
	SourceIPPolicy SourceIPPolicy
}

// EndpointContract is the product-facing TCP proxy shape callers can persist or
// return from APIs after PlanEndpoint succeeds. It deliberately contains no
// backend-specific proxy, DNS, or socket-binding details.
type EndpointContract struct {
	Kind               string         `json:"kind"`
	Host               string         `json:"host"`
	ExternalPort       int            `json:"externalPort"`
	PublicAddress      string         `json:"publicAddress"`
	BackendHost        string         `json:"backendHost"`
	BackendPort        int            `json:"backendPort"`
	AllowedSourceCIDRs []string       `json:"allowedSourceCidrs"`
	SourceIPPolicy     SourceIPPolicy `json:"sourceIpPolicy"`
}

var (
	ErrDisabled                 = errors.New("tcp proxy is disabled")
	ErrNoExternalPortsAvailable = errors.New("no tcp proxy external ports available")
	ErrInvalidSourceIPPolicy    = errors.New("invalid source IP policy")
)

func PlanEndpoint(input PlanInput) (EndpointPlan, error) {
	request := input.Request
	if !request.Enabled {
		return EndpointPlan{Enabled: false}, nil
	}
	if strings.TrimSpace(request.BackendHost) == "" {
		return EndpointPlan{}, errors.New("backend host is required")
	}
	if request.BackendPort <= 0 || request.BackendPort > 65535 {
		return EndpointPlan{}, fmt.Errorf("backend port %d is outside 1-65535", request.BackendPort)
	}
	sourcePolicy, err := normalizeSourceIPPolicy(request.SourceIPPolicy)
	if err != nil {
		return EndpointPlan{}, err
	}

	host := strings.TrimSpace(request.RequestedHost)
	if host == "" {
		generated, err := GeneratedHost(request)
		if err != nil {
			return EndpointPlan{}, err
		}
		host = generated
	}
	if err := validateHostAvailable(host, input.Existing); err != nil {
		return EndpointPlan{}, err
	}

	ports := normalizePortRange(input.Ports)
	externalPort, err := AllocateExternalPort(request.RequestedExternalPort, ports, input.Existing)
	if err != nil {
		return EndpointPlan{}, err
	}

	return EndpointPlan{
		Enabled:        true,
		Host:           host,
		ExternalPort:   externalPort,
		BackendHost:    strings.TrimSpace(request.BackendHost),
		BackendPort:    request.BackendPort,
		SourceIPPolicy: sourcePolicy,
	}, nil
}

func GeneratedHost(request EndpointRequest) (string, error) {
	baseDomain := strings.Trim(strings.ToLower(strings.TrimSpace(request.BaseDomain)), ".")
	if baseDomain == "" {
		return "", errors.New("base domain is required")
	}
	parts := []string{
		sanitizeLabelPart(request.ServiceID),
		sanitizeLabelPart(request.EnvironmentID),
		sanitizeLabelPart(request.ProjectID),
	}
	for _, part := range parts {
		if part == "" {
			return "", errors.New("project, environment, and service IDs are required for generated host")
		}
	}
	return strings.Join(parts, "--") + "." + DefaultHostPrefix + "." + baseDomain, nil
}

func AllocateExternalPort(requested int, ports PortRange, existing []ExistingEndpoint) (int, error) {
	ports = normalizePortRange(ports)
	used := usedPorts(existing)
	if requested != 0 {
		if requested < ports.Start || requested > ports.End {
			return 0, fmt.Errorf("requested external port %d is outside %d-%d", requested, ports.Start, ports.End)
		}
		if used[requested] {
			return 0, fmt.Errorf("requested external port %d is already allocated", requested)
		}
		return requested, nil
	}
	for port := ports.Start; port <= ports.End; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, ErrNoExternalPortsAvailable
}

func ValidateSourceIPPolicy(policy SourceIPPolicy) error {
	_, err := normalizeSourceIPPolicy(policy)
	return err
}

// ProductContract validates and projects an enabled endpoint plan into the
// stable TCP proxy product contract. Disabled plans have no public contract.
func ProductContract(plan EndpointPlan) (EndpointContract, error) {
	if !plan.Enabled {
		return EndpointContract{}, ErrDisabled
	}
	host := strings.ToLower(strings.TrimSpace(plan.Host))
	if err := validateHostAvailable(host, nil); err != nil {
		return EndpointContract{}, err
	}
	if plan.ExternalPort <= 0 || plan.ExternalPort > 65535 {
		return EndpointContract{}, fmt.Errorf("external port %d is outside 1-65535", plan.ExternalPort)
	}
	if strings.TrimSpace(plan.BackendHost) == "" {
		return EndpointContract{}, errors.New("backend host is required")
	}
	if plan.BackendPort <= 0 || plan.BackendPort > 65535 {
		return EndpointContract{}, fmt.Errorf("backend port %d is outside 1-65535", plan.BackendPort)
	}
	policy, err := normalizeSourceIPPolicy(plan.SourceIPPolicy)
	if err != nil {
		return EndpointContract{}, err
	}
	return EndpointContract{
		Kind:               "tcp-proxy",
		Host:               host,
		ExternalPort:       plan.ExternalPort,
		PublicAddress:      fmt.Sprintf("%s:%d", host, plan.ExternalPort),
		BackendHost:        strings.TrimSpace(plan.BackendHost),
		BackendPort:        plan.BackendPort,
		AllowedSourceCIDRs: append([]string(nil), policy.AllowedCIDRs...),
		SourceIPPolicy:     policy,
	}, nil
}

func normalizeSourceIPPolicy(policy SourceIPPolicy) (SourceIPPolicy, error) {
	if len(policy.AllowedCIDRs) == 0 {
		// An omitted policy is the Railway-like public TCP behavior. Operators
		// may add CIDRs to restrict the peer address observed by the listener,
		// but Docker bridge NAT can replace the original client address.
		return SourceIPPolicy{AllowedCIDRs: []string{}}, nil
	}
	seen := map[string]bool{}
	allowed := make([]string, 0, len(policy.AllowedCIDRs))
	for _, cidr := range policy.AllowedCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			return SourceIPPolicy{}, fmt.Errorf("%w: empty CIDR", ErrInvalidSourceIPPolicy)
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return SourceIPPolicy{}, fmt.Errorf("%w: %q is not CIDR notation", ErrInvalidSourceIPPolicy, cidr)
		}
		canonical := prefix.Masked().String()
		if seen[canonical] {
			return SourceIPPolicy{}, fmt.Errorf("%w: duplicate CIDR %q", ErrInvalidSourceIPPolicy, canonical)
		}
		seen[canonical] = true
		allowed = append(allowed, canonical)
	}
	sort.Strings(allowed)
	return SourceIPPolicy{AllowedCIDRs: allowed}, nil
}

func normalizePortRange(ports PortRange) PortRange {
	if ports.Start == 0 && ports.End == 0 {
		return PortRange{Start: DefaultExternalPortStart, End: DefaultExternalPortEnd}
	}
	return ports
}

func validateHostAvailable(host string, existing []ExistingEndpoint) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, " /:") {
		return fmt.Errorf("tcp proxy host %q is invalid", host)
	}
	for _, endpoint := range existing {
		if strings.EqualFold(strings.TrimSpace(endpoint.Host), host) {
			return fmt.Errorf("tcp proxy host %q is already allocated", host)
		}
	}
	return nil
}

func usedPorts(existing []ExistingEndpoint) map[int]bool {
	ports := make([]int, 0, len(existing))
	for _, endpoint := range existing {
		if endpoint.ExternalPort > 0 {
			ports = append(ports, endpoint.ExternalPort)
		}
	}
	sort.Ints(ports)
	used := make(map[int]bool, len(ports))
	for _, port := range ports {
		used[port] = true
	}
	return used
}

func sanitizeLabelPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = labelPartPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	if len(value) > 63 {
		value = strings.Trim(value[:63], "-")
	}
	return value
}
