package router

import "strings"

const reservedACMEPathPrefix = "/.well-known/acme-challenge/"

// IsReservedACMEPath reports whether path is owned by ACME HTTP-01 handling.
func IsReservedACMEPath(path string) bool {
	if path == strings.TrimSuffix(reservedACMEPathPrefix, "/") {
		return true
	}
	return strings.HasPrefix(path, reservedACMEPathPrefix)
}
