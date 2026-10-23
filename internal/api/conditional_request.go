package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// strongVersionETag returns the canonical strong validator for a non-negative
// resource version. Existing v1alpha validators used the quoted decimal form,
// so stable conditional requests retain that representation.
func strongVersionETag(version int64) string {
	if version < 0 {
		return ""
	}
	return `"` + strconv.FormatInt(version, 10) + `"`
}

// requireIfMatchVersion admits exactly one strong, non-negative decimal resource
// validator. Missing preconditions are 428; malformed, weak, wildcard, stale,
// repeated, or body/header-disagreeing validators fail with 412.
func requireIfMatchVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		writeRequestError(w, r, http.StatusPreconditionRequired, failurecodes.PreconditionRequired, "If-Match is required", nil)
		return 0, false
	}
	if len(values) != 1 {
		writeIfMatchFailed(w, r, "If-Match must contain exactly one strong validator", nil)
		return 0, false
	}
	raw := strings.TrimSpace(values[0])
	if strings.Contains(raw, ",") || len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' || strings.HasPrefix(raw, "W/") {
		writeIfMatchFailed(w, r, "If-Match must be one strong resource-version ETag", nil)
		return 0, false
	}
	digits := raw[1 : len(raw)-1]
	if len(digits) > 1 && digits[0] == '0' {
		writeIfMatchFailed(w, r, "If-Match does not identify a canonical resource version", nil)
		return 0, false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			writeIfMatchFailed(w, r, "If-Match does not identify a canonical resource version", nil)
			return 0, false
		}
	}
	version, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		writeIfMatchFailed(w, r, "If-Match does not identify a valid resource version", nil)
		return 0, false
	}
	return version, true
}

func requireMatchingExpectedVersion(w http.ResponseWriter, r *http.Request, expected int64) bool {
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return false
	}
	return expectedVersionMatchesIfMatch(w, r, expected, ifMatch)
}

func expectedVersionMatchesIfMatch(w http.ResponseWriter, r *http.Request, expected, ifMatch int64) bool {
	if expected < 0 || ifMatch != expected {
		writeIfMatchFailed(w, r, "If-Match and expected_version must identify the same resource version", map[string]any{
			"expected_version": expected,
		})
		return false
	}
	return true
}

func writeIfMatchFailed(w http.ResponseWriter, r *http.Request, message string, details map[string]any) {
	writeRequestError(w, r, http.StatusPreconditionFailed, failurecodes.PreconditionFailed, message, details)
}
