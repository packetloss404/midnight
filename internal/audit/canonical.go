package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// CanonicalRow returns the byte-identical canonicalization of a Row's
// integrity-protected fields. The encoding is a deterministic JSON
// produced by:
//
//   - sorting object keys lexicographically (RFC 8785 / JCS subset),
//   - encoding strings with the standard json.Encoder semantics with
//     html escaping disabled,
//   - timestamps as RFC 3339 nano (UTC),
//   - nil/empty maps as null (omitted from output entirely if empty so
//     the canonical form is stable across "field absent" vs "field set
//     to empty map").
//
// PrevHash, RowHash, and CheckpointID are EXCLUDED from canonical_row by
// definition (panel: line 121).
//
// This is not a full RFC 8785 implementation. It is byte-identical for
// the panel-locked field set declared on Row, which is what the
// hash-chain contract pins.
func CanonicalRow(r Row) ([]byte, error) {
	if r.CanonicalFormVersion == "" {
		r.CanonicalFormVersion = CanonicalFormVersion
	}
	if r.CanonicalFormVersion != CanonicalFormVersion {
		return nil, fmt.Errorf("audit: unknown canonical form %q", r.CanonicalFormVersion)
	}
	fields := map[string]any{
		"id":                     r.Sequence,
		"request_id":             r.RequestID,
		"trace_id":               r.TraceID,
		"occurred_at":            r.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_id":               r.ActorID,
		"actor_type":             r.ActorType,
		"auth_method":            r.AuthMethod,
		"source_ip":              r.SourceIP,
		"user_agent":             r.UserAgent,
		"workspace_id":           r.WorkspaceID,
		"project_id":             r.ProjectID,
		"environment_id":         r.EnvironmentID,
		"resource_kind":          r.ResourceKind,
		"resource_id":            r.ResourceID,
		"action":                 r.Action,
		"before":                 normalizeMap(r.Before),
		"after":                  normalizeMap(r.After),
		"reason_code":            r.ReasonCode,
		"policy_decision":        r.PolicyDecision,
		"attrs":                  normalizeMap(r.Attrs),
		"canonical_form_version": r.CanonicalFormVersion,
	}
	return canonicalEncode(fields)
}

// normalizeMap returns nil when the supplied map is empty so the
// canonical form is stable across "absent" and "set to empty map".
func normalizeMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// canonicalEncode renders a value into deterministic JSON bytes. Maps
// have their keys sorted lexicographically (UTF-8 byte order, which
// equals codepoint order for ASCII keys); slices preserve insertion
// order; numbers use json.Encoder default; html escape is disabled.
func canonicalEncode(value any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case map[string]any:
		return writeCanonicalMap(buf, v)
	case []any:
		return writeCanonicalSlice(buf, v)
	case string:
		return writeCanonicalString(buf, v)
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case int:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case int32:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case int64:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case uint:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case uint32:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case uint64:
		_, err := fmt.Fprintf(buf, "%d", v)
		return err
	case float32:
		return writeCanonicalFloat(buf, float64(v))
	case float64:
		return writeCanonicalFloat(buf, v)
	default:
		// Fall back to json.Marshal for any unforeseen types and trim
		// the trailing newline json.Encoder.Encode would otherwise
		// append. The audit payload schema only uses string, int64,
		// time, and map[string]any in practice.
		out, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(out)
		return nil
	}
}

// writeCanonicalFloat normalizes the float64 path so a JSON roundtrip
// through the export does not change the canonical bytes. Integer-valued
// floats encode the same way an int64 of the same value would.
func writeCanonicalFloat(buf *bytes.Buffer, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("audit: canonical float disallowed: %v", v)
	}
	if v == math.Trunc(v) && !math.Signbit(v) && v <= math.MaxInt64 && v >= -math.MaxInt64 {
		_, err := fmt.Fprintf(buf, "%d", int64(v))
		return err
	}
	if v == math.Trunc(v) && math.Signbit(v) && v <= math.MaxInt64 && v >= -math.MaxInt64 {
		// Negative integer-valued float (preserve negative-zero
		// roundtrip path: signbit only set on -0.0; explicitly fall
		// through to FormatFloat for that edge.)
		if v == 0 {
			buf.WriteString("0")
			return nil
		}
		_, err := fmt.Fprintf(buf, "%d", int64(v))
		return err
	}
	buf.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	return nil
}

func writeCanonicalMap(buf *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeCanonicalString(buf, k); err != nil {
			return err
		}
		buf.WriteByte(':')
		if err := writeCanonical(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func writeCanonicalSlice(buf *bytes.Buffer, s []any) error {
	buf.WriteByte('[')
	for i, v := range s {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeCanonical(buf, v); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func writeCanonicalString(buf *bytes.Buffer, s string) error {
	out, err := json.Marshal(s)
	if err != nil {
		return errors.New("audit: canonical encode failed")
	}
	buf.Write(out)
	return nil
}
