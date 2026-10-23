package auth

import "strings"

const (
	// ProductionAuthMissingReason is surfaced when production startup expects
	// API auth but no bearer token has been configured.
	ProductionAuthMissingReason = "AUTH_REQUIRED_MISSING"
)

// IsMutationMethod reports whether an HTTP method is unsafe and can mutate
// server state.
func IsMutationMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// ProductionAuthMissing reports whether a request must fail closed because the
// production API auth baseline is expected but no bearer token is configured.
func ProductionAuthMissing(expected bool, bearerToken, method string) bool {
	return expected && strings.TrimSpace(bearerToken) == "" && IsMutationMethod(method)
}
