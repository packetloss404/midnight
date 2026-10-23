package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/strictjson"
)

const (
	defaultJSONBodyLimitBytes   int64 = 1 << 20
	sensitiveJSONBodyLimitBytes int64 = 64 << 10
)

// decodeStrictJSONRequest is the shared JSON trust boundary for ordinary API
// requests. Upload and signed-webhook routes keep their purpose-built raw-body
// readers because their limits and byte-preservation requirements differ.
func decodeStrictJSONRequest(w http.ResponseWriter, r *http.Request, target any, message string) bool {
	_, ok := decodeStrictJSONRequestBody(w, r, target, message)
	return ok
}

// decodeStrictJSONRequestBody applies the same trust boundary while retaining
// the exact bytes for idempotency fingerprints and signed payload workflows.
// Callers must not decode the returned bytes a second, less-strict way.
func decodeStrictJSONRequestBody(w http.ResponseWriter, r *http.Request, target any, message string) ([]byte, bool) {
	return decodeStrictJSONRequestBodyWithLimit(w, r, target, message, defaultJSONBodyLimitBytes)
}

// decodeStrictJSONRequestBodyWithLimit applies the shared strict JSON policy
// with a route-specific byte ceiling. Sensitive authentication and credential
// endpoints use a smaller limit than ordinary API documents.
func decodeStrictJSONRequestBodyWithLimit(w http.ResponseWriter, r *http.Request, target any, message string, limit int64) ([]byte, bool) {
	body, ok := readBoundedJSONRequestBodyWithLimit(w, r, message, limit)
	if !ok {
		return nil, false
	}
	if !decodeStrictJSONRequestBytes(w, r, body, target, message) {
		return nil, false
	}
	return body, true
}

// readBoundedJSONRequestBody is the only raw-body seam for JSON requests that
// must retain the exact octets used by an idempotency fingerprint or a
// signature. Callers must pass the result to decodeStrictJSONRequestBytes (or
// decodeOptionalStrictJSONRequestBytes) instead of decoding it ad hoc.
func readBoundedJSONRequestBody(w http.ResponseWriter, r *http.Request, message string) ([]byte, bool) {
	return readBoundedJSONRequestBodyWithLimit(w, r, message, defaultJSONBodyLimitBytes)
}

func readBoundedJSONRequestBodyWithLimit(w http.ResponseWriter, r *http.Request, message string, limit int64) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, limit)
	defer limited.Close()

	body, err := io.ReadAll(limited)
	if err != nil {
		writeJSONDecodeError(w, r, err, message)
		return nil, false
	}
	return body, true
}

// decodeStrictJSONRequestBytes validates a previously bounded raw body as one
// JSON document, rejecting duplicate object members and unknown fields.
func decodeStrictJSONRequestBytes(w http.ResponseWriter, r *http.Request, body []byte, target any, message string) bool {
	if err := strictJSONDecode(body, target); err != nil {
		writeJSONDecodeError(w, r, err, message)
		return false
	}
	return true
}

func strictJSONDecode(body []byte, target any) error {
	return strictjson.Decode(body, target)
}

// decodeOptionalStrictJSONRequestBytes preserves the handful of v1alpha
// endpoints whose documented request body is optional. When bytes are
// present, they still must form exactly one strict JSON document.
func decodeOptionalStrictJSONRequestBytes(w http.ResponseWriter, r *http.Request, body []byte, target any, message string) bool {
	if len(body) == 0 {
		return true
	}
	return decodeStrictJSONRequestBytes(w, r, body, target, message)
}

func writeJSONDecodeError(w http.ResponseWriter, r *http.Request, err error, message string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "request body exceeds the configured limit", map[string]any{
			"limit": tooLarge.Limit,
		})
		return
	}
	writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, message, map[string]any{"reason": err.Error()})
}
