package domain

import "strings"

const (
	// MaxDNSLabelOctets is the RFC 1035 limit for a single DNS label.
	MaxDNSLabelOctets       = 63
	defaultGeneratedHostKey = "app"
)

// DNSLabel sanitizes seed into a single DNS label: ASCII-only lowercase LDH
// (a-z0-9-), capped at MaxDNSLabelOctets with trailing hyphens trimmed, and
// falling back to defaultGeneratedHostKey ("app") when seed carries no
// DNS-safe characters. Callers that build a generated hostname use this for
// the left label and append their own base domain.
func DNSLabel(seed string) string {
	return generatedHostLabel(seed, "")
}

// DNSLabelWithCollisionSuffix behaves like DNSLabel but reserves budget for a
// collision-breaking suffix, guaranteeing the combined "<base>-<suffix>" label
// still fits within MaxDNSLabelOctets.
func DNSLabelWithCollisionSuffix(seed, collisionSuffix string) string {
	return generatedHostLabel(seed, collisionSuffix)
}

func generatedHostLabel(seed string, collisionSuffix string) string {
	base := sanitizeDNSLabel(seed)
	base = fallbackLabel(base, MaxDNSLabelOctets)

	suffix := sanitizeDNSLabel(collisionSuffix)
	if suffix == "" {
		return truncateLabel(base, MaxDNSLabelOctets)
	}

	suffix = truncateLabel(suffix, MaxDNSLabelOctets-2)
	if suffix == "" {
		return truncateLabel(base, MaxDNSLabelOctets)
	}

	maxBase := MaxDNSLabelOctets - len(suffix) - 1
	base = fallbackLabel(truncateLabel(base, maxBase), maxBase)
	return base + "-" + suffix
}

func sanitizeDNSLabel(raw string) string {
	var label strings.Builder
	previousHyphen := false

	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'A' && r <= 'Z':
			label.WriteByte(byte(r + ('a' - 'A')))
			previousHyphen = false
		case r >= 'a' && r <= 'z':
			label.WriteByte(byte(r))
			previousHyphen = false
		case r >= '0' && r <= '9':
			label.WriteByte(byte(r))
			previousHyphen = false
		default:
			if label.Len() > 0 && !previousHyphen {
				label.WriteByte('-')
				previousHyphen = true
			}
		}
	}

	return strings.Trim(label.String(), "-")
}

func truncateLabel(label string, maxOctets int) string {
	if maxOctets <= 0 {
		return ""
	}
	if len(label) > maxOctets {
		label = label[:maxOctets]
	}
	return strings.Trim(label, "-")
}

func fallbackLabel(label string, maxOctets int) string {
	if label != "" {
		return label
	}
	return truncateLabel(defaultGeneratedHostKey, maxOctets)
}
