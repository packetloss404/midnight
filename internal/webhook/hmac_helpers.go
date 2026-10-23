package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// sha256Hex returns the lowercase hex sha256 of the supplied bytes.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256Hex returns the lowercase hex HMAC-SHA256 of body under key.
func hmacSHA256Hex(key, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
