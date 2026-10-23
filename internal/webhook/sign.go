package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader is the panel-locked header name.
const SignatureHeader = "Midnight-Signature"

// DeliveryHeader is the panel-locked delivery-id header.
const DeliveryHeader = "Midnight-Delivery-Id"

// EventIDHeader is the panel-locked event-id header.
const EventIDHeader = "Midnight-Event-Id"

// EventTypeHeader is the panel-locked event-type header.
const EventTypeHeader = "Midnight-Event-Type"

// ReplayWindow is the panel-locked acceptance window for inbound
// signatures: |now - t| MUST be <= 300s.
const ReplayWindow = 300 * time.Second

// Key is one HMAC-SHA256 signing key. KID is the panel-locked key id
// surfaced in the kid= signature segment. Material is the raw secret
// bytes; callers that pull material from a sealed variable MUST treat
// the byte slice as short-lived.
type Key struct {
	KID      string
	Material []byte
}

// Signer signs a body+timestamp pair under one or more keys (the latter
// supports the panel-locked rotation overlap).
type Signer struct{}

// SignDelivery returns a Midnight-Signature header bound to one delivery ID.
// The v2 signing input is f"{deliveryID}.{ts.Unix()}.{body}". Binding the
// delivery identity prevents a valid signature from being replayed under a
// different Midnight-Delivery-Id value.
func (Signer) SignDelivery(keys []Key, deliveryID string, body []byte, ts time.Time) (string, error) {
	if len(keys) == 0 {
		return "", ErrNoKeys
	}
	if !validDeliveryID(deliveryID) {
		return "", ErrDeliveryIDInvalid
	}
	canonical := canonicalDeliverySigningInput(deliveryID, ts, body)
	var b strings.Builder
	b.WriteString("t=")
	b.WriteString(strconv.FormatInt(ts.UTC().Unix(), 10))
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.Material)
		_, _ = mac.Write(canonical)
		b.WriteString(",v2=")
		b.WriteString(hex.EncodeToString(mac.Sum(nil)))
	}
	b.WriteString(",kid=")
	b.WriteString(keys[0].KID)
	return b.String(), nil
}

// SignBody returns a Midnight-Signature header value computed over
// f"{ts.Unix()}.{body}" for every key in keys. The first key is treated
// as the active key (kid= references it). When more than one key is
// passed, every key contributes a v1= segment, supporting the panel-
// locked rotation overlap.
//
// Returns ErrNoKeys when keys is empty.
func (Signer) SignBody(keys []Key, body []byte, ts time.Time) (string, error) {
	if len(keys) == 0 {
		return "", ErrNoKeys
	}
	canonical := canonicalSigningInput(ts, body)
	var b strings.Builder
	b.WriteString("t=")
	b.WriteString(strconv.FormatInt(ts.UTC().Unix(), 10))
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.Material)
		_, _ = mac.Write(canonical)
		b.WriteString(",v1=")
		b.WriteString(hex.EncodeToString(mac.Sum(nil)))
	}
	b.WriteString(",kid=")
	b.WriteString(keys[0].KID)
	return b.String(), nil
}

// ErrNoKeys is returned when SignBody/Verify is called with no keys.
var ErrNoKeys = errors.New("webhook: no signing keys supplied")

// ErrDeliveryIDInvalid is returned when a v2 signature is requested without a
// bounded, printable delivery identity.
var ErrDeliveryIDInvalid = errors.New("webhook: delivery id invalid")

func validDeliveryID(deliveryID string) bool {
	if deliveryID == "" || len(deliveryID) > 256 || strings.TrimSpace(deliveryID) != deliveryID {
		return false
	}
	for _, character := range deliveryID {
		if character < 0x21 || character > 0x7e || character == '.' {
			return false
		}
	}
	return true
}

// canonicalSigningInput returns the panel-locked f"{timestamp}.{body}".
func canonicalSigningInput(ts time.Time, body []byte) []byte {
	out := make([]byte, 0, 16+1+len(body))
	out = strconv.AppendInt(out, ts.UTC().Unix(), 10)
	out = append(out, '.')
	out = append(out, body...)
	return out
}

func canonicalDeliverySigningInput(deliveryID string, ts time.Time, body []byte) []byte {
	out := make([]byte, 0, len(deliveryID)+17+len(body))
	out = append(out, deliveryID...)
	out = append(out, '.')
	out = strconv.AppendInt(out, ts.UTC().Unix(), 10)
	out = append(out, '.')
	out = append(out, body...)
	return out
}

// ErrSignatureMalformed is returned when the Midnight-Signature header
// cannot be parsed.
var ErrSignatureMalformed = errors.New("webhook: signature header malformed")

// ErrSignatureMismatch is returned when no v1= segment matches any key.
var ErrSignatureMismatch = errors.New("webhook: signature mismatch")

// ErrReplayRejected is returned when |now - t| > ReplayWindow.
var ErrReplayRejected = errors.New("webhook: replay window violated")

// ParsedSignature is the parsed Midnight-Signature header.
type ParsedSignature struct {
	Timestamp time.Time
	V1        []string
	V2        []string
	KID       string
}

// ParseSignature parses a Midnight-Signature header.
func ParseSignature(header string) (ParsedSignature, error) {
	if header == "" {
		return ParsedSignature{}, ErrSignatureMalformed
	}
	out := ParsedSignature{}
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ParsedSignature{}, fmt.Errorf("%w: segment %q", ErrSignatureMalformed, part)
		}
		switch k {
		case "t":
			ts, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ParsedSignature{}, fmt.Errorf("%w: timestamp: %v", ErrSignatureMalformed, err)
			}
			out.Timestamp = time.Unix(ts, 0).UTC()
		case "v1":
			if len(v) != 2*sha256.Size {
				return ParsedSignature{}, fmt.Errorf("%w: v1 length", ErrSignatureMalformed)
			}
			out.V1 = append(out.V1, v)
		case "v2":
			if len(v) != 2*sha256.Size {
				return ParsedSignature{}, fmt.Errorf("%w: v2 length", ErrSignatureMalformed)
			}
			out.V2 = append(out.V2, v)
		case "kid":
			out.KID = v
		default:
			// Forward-compatibility: ignore unknown segments.
		}
	}
	if out.Timestamp.IsZero() || len(out.V1)+len(out.V2) == 0 {
		return ParsedSignature{}, ErrSignatureMalformed
	}
	return out, nil
}

// VerifySignature verifies a parsed Midnight-Signature header against
// the supplied body and key set. The check passes when ANY v1 segment
// matches ANY key — this matches the panel-locked rotation overlap.
//
// Replay window check is included so callers can rely on a single API.
// Pass a stable now() (e.g. time.Now()) to keep the test fixture deterministic.
func VerifySignature(parsed ParsedSignature, keys []Key, body []byte, now time.Time) error {
	if len(keys) == 0 {
		return ErrNoKeys
	}
	if delta := now.Sub(parsed.Timestamp); delta > ReplayWindow || delta < -ReplayWindow {
		return fmt.Errorf("%w: |delta|=%s", ErrReplayRejected, delta)
	}
	canonical := canonicalSigningInput(parsed.Timestamp, body)
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.Material)
		_, _ = mac.Write(canonical)
		expected := hex.EncodeToString(mac.Sum(nil))
		for _, candidate := range parsed.V1 {
			if hmac.Equal([]byte(expected), []byte(candidate)) {
				return nil
			}
		}
	}
	return ErrSignatureMismatch
}

// VerifyDeliverySignature verifies a delivery-bound v2 signature. A header
// containing only v1 segments is accepted through VerifySignature for a
// bounded migration period. When any v2 segment is present, verification never
// falls back to v1, preventing a downgrade attack.
func VerifyDeliverySignature(parsed ParsedSignature, keys []Key, deliveryID string, body []byte, now time.Time) error {
	if len(keys) == 0 {
		return ErrNoKeys
	}
	if !validDeliveryID(deliveryID) {
		return ErrDeliveryIDInvalid
	}
	if len(parsed.V2) == 0 {
		return VerifySignature(parsed, keys, body, now)
	}
	if delta := now.Sub(parsed.Timestamp); delta > ReplayWindow || delta < -ReplayWindow {
		return fmt.Errorf("%w: |delta|=%s", ErrReplayRejected, delta)
	}
	canonical := canonicalDeliverySigningInput(deliveryID, parsed.Timestamp, body)
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.Material)
		_, _ = mac.Write(canonical)
		expected := hex.EncodeToString(mac.Sum(nil))
		for _, candidate := range parsed.V2 {
			if hmac.Equal([]byte(expected), []byte(candidate)) {
				return nil
			}
		}
	}
	return ErrSignatureMismatch
}
