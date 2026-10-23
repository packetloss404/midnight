package router

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
)

// SanitizeRouteGroupFilename returns a path-segment-safe filename stem for a route group.
func SanitizeRouteGroupFilename(routeGroup string) (string, error) {
	var b strings.Builder
	lastDash := false

	for _, r := range strings.TrimSpace(routeGroup) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), ".-")
	if name == "" || name == "." || name == ".." {
		return "", errors.New("route group filename is empty after sanitization")
	}
	return name, nil
}

// SanitizeRouteFilename returns a collision-safe filename stem for a single
// route within a route group. Two distinct inputs that sanitize to the same
// stem (e.g. "svc a" and "svc-a") would otherwise map to the same file and
// clobber each other; appending a short hash of the input keeps them distinct.
// The production caller trims the route name before calling, so route names
// differing only in surrounding whitespace map to the same route (and file);
// names differing in interior characters (including interior whitespace) hash
// distinctly.
func SanitizeRouteFilename(routeName string) (string, error) {
	stem, err := SanitizeRouteGroupFilename(routeName)
	if err != nil {
		return "", err
	}
	return stem + "-" + shortNameHash(routeName), nil
}

// shortNameHash returns the first 10 hex characters of the SHA-256 digest of
// name. It mirrors light's shortKeyHash/shortHostHash: enough entropy to make
// accidental filename collisions between sanitized route names implausible
// without producing unwieldy filenames.
func shortNameHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:10]
}
