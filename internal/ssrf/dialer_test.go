// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package ssrf

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type resolverFake map[string][]net.IPAddr

func (resolver resolverFake) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	values, ok := resolver[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return values, nil
}

func TestValidateAddressRejectsNonPublicRanges(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1",
		"169.254.169.254", "172.16.1.2", "192.0.2.1", "192.168.1.2",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1",
		"240.0.0.1", "255.255.255.255", "::", "::1", "fc00::1",
		"fe80::1", "fec0::1", "ff02::1", "2001:db8::1", "::ffff:127.0.0.1",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if err := ValidateAddress(netip.MustParseAddr(value), false); !errors.Is(err, ErrUnsafeAddress) {
				t.Fatalf("ValidateAddress(%s) error = %v, want ErrUnsafeAddress", value, err)
			}
		})
	}
}

func TestValidateAddressAllowsPublicAndExplicitTestLoopback(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if err := ValidateAddress(netip.MustParseAddr(value), false); err != nil {
			t.Fatalf("ValidateAddress(%s) error = %v", value, err)
		}
	}
	if err := ValidateAddress(netip.MustParseAddr("127.0.0.1"), true); err != nil {
		t.Fatalf("test loopback error = %v", err)
	}
}

func TestDialerRejectsEntireMixedDNSAnswerBeforeDial(t *testing.T) {
	t.Parallel()
	dialed := false
	dialer := Dialer{
		Resolver: resolverFake{"hooks.example": {
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("127.0.0.1")},
		}},
		ExpectedHost: "hooks.example",
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
	}
	_, err := dialer.Dial(context.Background(), "tcp", "hooks.example:443")
	if !errors.Is(err, ErrUnsafeAddress) {
		t.Fatalf("Dial error = %v, want ErrUnsafeAddress", err)
	}
	if dialed {
		t.Fatal("unsafe mixed answer reached the network dialer")
	}
}

func TestDialerPinsApprovedDNSAddress(t *testing.T) {
	t.Parallel()
	var dialAddress string
	dialer := Dialer{
		Resolver:     resolverFake{"hooks.example": {{IP: net.ParseIP("93.184.216.34")}}},
		ExpectedHost: "hooks.example",
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			dialAddress = address
			return nil, errors.New("stop after recording")
		},
	}
	_, _ = dialer.Dial(context.Background(), "tcp", "hooks.example:443")
	if dialAddress != "93.184.216.34:443" {
		t.Fatalf("dial address = %q, want pinned IP", dialAddress)
	}
}
