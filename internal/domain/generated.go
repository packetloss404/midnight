package domain

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
)

// NormalizeHostname converts a user-supplied DNS hostname to the canonical
// ASCII form used for durable ownership. A trailing root dot is accepted but
// not persisted. Wildcards, IP literals, ports, and empty labels are refused.
func NormalizeHostname(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if raw == "" {
		return "", errors.New("hostname is required")
	}
	if strings.ContainsAny(raw, "*:/\\`") {
		return "", errors.New("hostname contains an unsupported character")
	}
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil {
		return "", fmt.Errorf("normalize hostname: %w", err)
	}
	ascii = strings.ToLower(ascii)
	if net.ParseIP(ascii) != nil {
		return "", errors.New("hostname must not be an IP address")
	}
	if len(ascii) > 253 {
		return "", errors.New("hostname exceeds 253 octets")
	}
	labels := strings.Split(ascii, ".")
	if len(labels) < 2 {
		return "", errors.New("hostname must contain at least two labels")
	}
	for _, label := range labels {
		if !validDNSLabel(label) {
			return "", errors.New("hostname contains an invalid DNS label")
		}
	}
	return ascii, nil
}

// GeneratedHostname produces exactly one wildcard-covered label beneath the
// configured apps domain. For example, seed "web--production" and apps domain
// "apps.example.com" produce "web-production.apps.example.com"; the generated
// prefix can never become multiple DNS labels.
func GeneratedHostname(seed, collisionSuffix, appsDomain string) (string, error) {
	base, err := NormalizeHostname(appsDomain)
	if err != nil {
		return "", fmt.Errorf("apps domain: %w", err)
	}
	label := DNSLabelWithCollisionSuffix(strings.ReplaceAll(seed, ".", "-"), collisionSuffix)
	if !validDNSLabel(label) {
		return "", errors.New("generated hostname label is invalid")
	}
	return label + "." + base, nil
}

// IsSingleLabelBelow reports whether hostname is exactly one label beneath
// appsDomain. It is used before persisting a generated-domain intent so a
// wildcard for *.appsDomain is never represented as covering multiple labels.
func IsSingleLabelBelow(hostname, appsDomain string) bool {
	host, err := NormalizeHostname(hostname)
	if err != nil {
		return false
	}
	base, err := NormalizeHostname(appsDomain)
	if err != nil {
		return false
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	left := strings.TrimSuffix(host, suffix)
	return validDNSLabel(left) && !strings.Contains(left, ".")
}

func validDNSLabel(label string) bool {
	if label == "" || len(label) > MaxDNSLabelOctets {
		return false
	}
	for index, char := range []byte(label) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		if char == '-' && index > 0 && index < len(label)-1 {
			continue
		}
		return false
	}
	return true
}
