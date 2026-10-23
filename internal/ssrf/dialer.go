// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrUnsafeAddress identifies an outbound target that must not be reached.
// Callers should keep the error message generic rather than reflecting a
// secret-bearing URL back into logs or durable delivery state.
var ErrUnsafeAddress = errors.New("outbound target resolves to a non-public address")

// Resolver is the subset of net.Resolver used by Dialer. It is deliberately
// injectable so callers can prove DNS-rebinding behavior without network DNS.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// DialContextFunc matches net.Dialer.DialContext.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// Dialer resolves a hostname once per connection, validates the complete
// answer set, and then dials the approved IP literals. DNS is never consulted
// again between validation and connect, closing the usual rebinding window.
// TLS remains the responsibility of http.Transport, which therefore verifies
// the certificate against the original request hostname rather than the
// pinned address.
type Dialer struct {
	Resolver      Resolver
	DialContext   DialContextFunc
	ExpectedHost  string
	AllowLoopback bool
}

func (dialer Dialer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("ssrf: invalid dial address: %w", err)
	}
	if expected := normalizeHost(dialer.ExpectedHost); expected != "" && !sameHost(expected, normalizeHost(host)) {
		return nil, ErrUnsafeAddress
	}

	addresses, err := dialer.resolve(ctx, network, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("ssrf: target did not resolve")
	}
	for _, address := range addresses {
		if err := ValidateAddress(address, dialer.AllowLoopback); err != nil {
			return nil, err
		}
	}

	dial := dialer.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	var lastErr error
	for _, ip := range addresses {
		connection, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ssrf: connect approved target: %w", lastErr)
}

func (dialer Dialer) resolve(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	resolver := dialer.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	values, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("ssrf: resolve outbound target")
	}
	want4 := network == "tcp4"
	want6 := network == "tcp6"
	addresses := make([]netip.Addr, 0, len(values))
	seen := make(map[netip.Addr]struct{}, len(values))
	for _, value := range values {
		address, ok := netip.AddrFromSlice(value.IP)
		if !ok {
			continue
		}
		address = address.Unmap()
		if (want4 && !address.Is4()) || (want6 && !address.Is6()) {
			continue
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

// ValidateAddress permits only ordinary public unicast addresses. The
// explicit loopback allowance exists for local tests; persistent production
// monitor channels reject that setting before they reach delivery.
func ValidateAddress(address netip.Addr, allowLoopback bool) error {
	address = address.Unmap()
	if !address.IsValid() {
		return ErrUnsafeAddress
	}
	if allowLoopback && address.IsLoopback() {
		return nil
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsUnspecified() || address.IsMulticast() {
		return ErrUnsafeAddress
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return ErrUnsafeAddress
		}
	}
	return nil
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
}

func sameHost(left, right string) bool {
	leftIP, leftErr := netip.ParseAddr(left)
	rightIP, rightErr := netip.ParseAddr(right)
	if leftErr == nil || rightErr == nil {
		return leftErr == nil && rightErr == nil && leftIP.Unmap() == rightIP.Unmap()
	}
	return left == right
}

var nonPublicPrefixes = mustPrefixes(
	"0.0.0.0/8",       // current network / unspecified
	"10.0.0.0/8",      // private use
	"100.64.0.0/10",   // shared address space
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local
	"172.16.0.0/12",   // private use
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.31.196.0/24", // AS112
	"192.52.193.0/24", // AMT
	"192.88.99.0/24",  // deprecated 6to4 relay anycast
	"192.168.0.0/16",  // private use
	"192.175.48.0/24", // AS112
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved and limited broadcast
	"::/128",          // unspecified
	"::1/128",         // loopback
	"64:ff9b::/96",    // IPv4/IPv6 translation
	"64:ff9b:1::/48",  // local-use translation
	"100::/64",        // discard-only
	"2001::/23",       // IETF protocol assignments
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4
	"fc00::/7",        // unique-local
	"fe80::/10",       // link-local
	"fec0::/10",       // deprecated site-local
	"ff00::/8",        // multicast
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}
