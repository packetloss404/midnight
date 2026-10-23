// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package cloudevents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"
)

// SpecVersion is the panel-locked CloudEvents bindings version emitted by
// EncodeStructured.
const SpecVersion = "1.0"

// ContentType is the panel-locked structured-mode HTTP content-type.
const ContentType = "application/cloudevents+json"

// DataContentTypeJSON is the default `datacontenttype` field value used
// when an Event omits its own override.
const DataContentTypeJSON = "application/json"

// ErrEventInvalid is returned for missing required fields or unknown types.
var ErrEventInvalid = errors.New("cloudevents event invalid")

// ErrUnknownType is the canonical error returned when an Event carries a
// `type` outside the panel-locked catalog. It is surfaced through
// ErrEventInvalid via errors.Is.
var ErrUnknownType = fmt.Errorf("%w: type not in catalog", ErrEventInvalid)

// Event is the structured CloudEvents 1.0 envelope used by every Midnight
// outbound webhook and persisted event row. Field naming mirrors the
// CloudEvents JSON binding so a marshaled Event satisfies the spec out of
// the box.
type Event struct {
	// SpecVersion MUST be "1.0".
	SpecVersion string `json:"specversion"`
	// ID is the stable evt_* identifier; idempotent across redeliveries.
	ID string `json:"id"`
	// Source is an absolute producer identifier, normally
	// `urn:midnight:<instance>:project:<project>`.
	Source string `json:"source"`
	// Subject is `<kind>/<id>`.
	Subject string `json:"subject,omitempty"`
	// Type is the panel-locked CloudEvents `type` enumeration value.
	Type Type `json:"type"`
	// DataContentType is the MIME type for `data`; defaults to JSON.
	DataContentType string `json:"datacontenttype,omitempty"`
	// DataSchema is the dataschema URL convention.
	DataSchema string `json:"dataschema,omitempty"`
	// Time is the canonical event time in UTC.
	Time time.Time `json:"time"`
	// TraceParent is the W3C Trace Context propagated from the originating request.
	TraceParent string `json:"traceparent"`
	// MidnightRequestID correlates to API audit/operation events.
	MidnightRequestID string `json:"midnightrequestid"`
	// MidnightTenantID is the workspace id (tenant boundary).
	MidnightTenantID string `json:"midnighttenantid"`
	// Data is the per-type payload. The envelope encoder canonicalises the
	// data value so structured-mode bytes are deterministic.
	Data any `json:"data,omitempty"`
}

// requiredFields lists the panel-locked required CloudEvents and Midnight
// extension fields.
var requiredFields = []string{"specversion", "id", "source", "type", "time", "traceparent", "midnightrequestid", "midnighttenantid"}

// ValidateEvent enforces the panel-locked required fields and the
// catalog-only `type` constraint. The validator is deliberately strict so
// that callers cannot accidentally ship a malformed envelope.
func ValidateEvent(event Event) error {
	if event.SpecVersion == "" {
		return fmt.Errorf("%w: specversion is required", ErrEventInvalid)
	}
	if event.SpecVersion != SpecVersion {
		return fmt.Errorf("%w: specversion = %q, want %q", ErrEventInvalid, event.SpecVersion, SpecVersion)
	}
	if event.ID == "" {
		return fmt.Errorf("%w: id is required", ErrEventInvalid)
	}
	if event.Source == "" {
		return fmt.Errorf("%w: source is required", ErrEventInvalid)
	}
	source, err := url.Parse(event.Source)
	if err != nil || !source.IsAbs() {
		return fmt.Errorf("%w: source must be an absolute URI", ErrEventInvalid)
	}
	if event.Type == "" {
		return fmt.Errorf("%w: type is required", ErrEventInvalid)
	}
	if !IsRegistered(event.Type) {
		return fmt.Errorf("%w: %s", ErrUnknownType, event.Type)
	}
	if event.Time.IsZero() {
		return fmt.Errorf("%w: time is required", ErrEventInvalid)
	}
	if event.TraceParent == "" {
		return fmt.Errorf("%w: traceparent extension is required", ErrEventInvalid)
	}
	if event.MidnightRequestID == "" {
		return fmt.Errorf("%w: midnightrequestid extension is required", ErrEventInvalid)
	}
	if event.MidnightTenantID == "" {
		return fmt.Errorf("%w: midnighttenantid extension is required", ErrEventInvalid)
	}
	return nil
}

// RequiredFields returns the slice of required JSON keys per the panel
// lock. Returned in stable, alphabetic order so other linters can reuse
// it without reimplementing the list.
func RequiredFields() []string {
	out := make([]string, len(requiredFields))
	copy(out, requiredFields)
	sort.Strings(out)
	return out
}

// EncodeStructured serialises the supplied Event to CloudEvents structured
// mode JSON with stable key ordering. The output is JSON object encoded
// with sorted keys; data values are re-serialised with the same key
// ordering rules so byte-for-byte equality across runs is guaranteed.
func EncodeStructured(event Event) ([]byte, error) {
	if err := ValidateEvent(event); err != nil {
		return nil, err
	}
	if event.DataContentType == "" {
		event.DataContentType = DataContentTypeJSON
	}

	keys := []string{
		"specversion", "id", "source", "type", "datacontenttype", "dataschema", "subject",
		"time", "traceparent", "midnightrequestid", "midnighttenantid", "data",
	}
	sort.Strings(keys)

	values := map[string]any{
		"specversion":       event.SpecVersion,
		"id":                event.ID,
		"source":            event.Source,
		"type":              string(event.Type),
		"datacontenttype":   event.DataContentType,
		"time":              event.Time.UTC().Format(time.RFC3339Nano),
		"traceparent":       event.TraceParent,
		"midnightrequestid": event.MidnightRequestID,
		"midnighttenantid":  event.MidnightTenantID,
	}
	if event.DataSchema != "" {
		values["dataschema"] = event.DataSchema
	}
	if event.Subject != "" {
		values["subject"] = event.Subject
	}
	if event.Data != nil {
		canonical, err := canonicaliseJSON(event.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: data canonicalisation failed: %w", ErrEventInvalid, err)
		}
		values["data"] = json.RawMessage(canonical)
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, key := range keys {
		v, ok := values[key]
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		keyBytes, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')

		if raw, isRaw := v.(json.RawMessage); isRaw {
			buf.Write(raw)
			continue
		}
		valueBytes, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buf.Write(valueBytes)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// canonicaliseJSON walks the supplied value and re-emits its JSON
// representation with sorted object keys. Arrays preserve their element
// order. Numbers are preserved verbatim via json.Number to avoid
// float-vs-integer drift.
func canonicaliseJSON(value any) ([]byte, error) {
	bytesIn, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(bytesIn))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeCanonical emits a value with sorted object keys.
func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case json.Number:
		buf.WriteString(v.String())
		return nil
	case string:
		out, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(out)
		return nil
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			keyBytes, err := json.Marshal(key)
			if err != nil {
				return err
			}
			buf.Write(keyBytes)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	}
	// Fallback: re-marshal unknown types.
	out, err := json.Marshal(value)
	if err != nil {
		return err
	}
	buf.Write(out)
	return nil
}

// Decode parses inbound structured CloudEvents JSON (webhook receiver).
func Decode(body []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrEventInvalid, err)
	}
	if err := ValidateEvent(ev); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// Hash returns SHA-256 hex of the canonical structured-mode bytes for dedupe.
func Hash(ev Event) (string, error) {
	canonical, err := EncodeStructured(ev)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Encode serializes an Event using EncodeStructured (stable key ordering).
func Encode(ev Event) ([]byte, error) {
	return EncodeStructured(ev)
}

// NewEvent returns CloudEvents defaults for tests.
func NewEvent() Event {
	return Event{
		SpecVersion:     SpecVersion,
		DataContentType: DataContentTypeJSON,
		Time:            time.Now().UTC(),
	}
}
