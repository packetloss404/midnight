package secrets

import (
	"encoding/json"
	"fmt"
)

// MarshalEnvelope returns a deterministic JSON encoding of env. The
// serialized form is what callers persist to disk (e.g. the bootstrap
// admin row's totp_secret_encrypted column).
func MarshalEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

// UnmarshalEnvelope reverses MarshalEnvelope.
func UnmarshalEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if env.CanonicalForm == "" {
		return Envelope{}, fmt.Errorf("envelope missing canonical_form")
	}
	return env, nil
}
