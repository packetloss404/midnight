package observability

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/supportbundle"
)

// PIIForbiddenKeys lists payload-key shapes the panel-locked telemetry schema must never carry.
//
// The check is case-insensitive and matches both top-level and nested map keys, so a label like
// "User_Email" inside an event metric would be rejected.
var PIIForbiddenKeys = []string{
	"email",
	"phone",
	"address",
	"name",
	"username",
	"customer",
	"customerid",
	"user",
	"userid",
	"account",
	"accountid",
	"workspace",
	"workspaceid",
	"project",
	"projectid",
	"service",
	"servicename",
	"hostname",
	"domain",
	"ip",
	"ipaddress",
	"tenant",
	"tenantid",
}

// StrictTelemetryPayload extends the base telemetry payload with the panel-locked Phase 1 fields
// the validator enforces in addition to ValidateTelemetryPayloadJSON's structural checks.
type StrictTelemetryPayload struct {
	TelemetryPayload
	// EventID is the monotonic per-source event identifier; consecutive payloads must report
	// strictly increasing values within one source.
	EventID uint64 `json:"eventId"`
	// OptIn records whether telemetry was explicitly enabled by an operator. The schema is locked
	// to default-off, so a payload built without OptIn=true is rejected.
	OptIn bool `json:"optIn"`
	// Metrics is a free-form numeric metric bag, with key shape restricted by ValidateMetricBag.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

// ValidateStrictTelemetryPayload runs the panel-locked checks: schema-version present, default-off
// opt-in flag explicitly set, no PII keys (top-level or in metric bag), and no values that look
// like recognized secret shapes.
//
// The previous EventID parameter is the highest event-id observed for this source so far. Pass
// zero on the first call. The returned next event-id can be threaded through subsequent calls.
func ValidateStrictTelemetryPayload(p StrictTelemetryPayload, previousEventID uint64) (uint64, error) {
	if err := p.TelemetryPayload.Validate(); err != nil {
		return previousEventID, err
	}
	if !p.OptIn {
		return previousEventID, errors.New("telemetry payload optIn must be true; default-off is locked at the schema")
	}
	if p.EventID == 0 {
		return previousEventID, errors.New("telemetry payload eventId is required and must be greater than zero")
	}
	if p.EventID <= previousEventID {
		return previousEventID, fmt.Errorf("telemetry payload eventId %d must be greater than previous %d (monotonic source ordering)", p.EventID, previousEventID)
	}
	if err := ValidateMetricBag(p.Metrics); err != nil {
		return previousEventID, err
	}
	if err := scanForbiddenKeysInPayload(p); err != nil {
		return previousEventID, err
	}
	if err := scanSecretShapedValuesInPayload(p); err != nil {
		return previousEventID, err
	}
	if err := CheckPayloadForPII(p); err != nil {
		return previousEventID, err
	}
	return p.EventID, nil
}

// ValidateMetricBag rejects metric keys that match the PII forbidden-key list and metric values
// that look like recognized secret shapes.
func ValidateMetricBag(metrics map[string]float64) error {
	for key := range metrics {
		if forbidden := matchForbiddenKey(key); forbidden != "" {
			return fmt.Errorf("metric key %q is forbidden by the locked schema (%s)", key, forbidden)
		}
	}
	return nil
}

func scanForbiddenKeysInPayload(p StrictTelemetryPayload) error {
	for key := range p.Metrics {
		if forbidden := matchForbiddenKey(key); forbidden != "" {
			return fmt.Errorf("payload metric key %q is forbidden by the locked schema (%s)", key, forbidden)
		}
	}
	if forbidden := matchForbiddenKey(p.Source); forbidden != "" {
		return fmt.Errorf("payload source %q is forbidden by the locked schema (%s)", p.Source, forbidden)
	}
	if forbidden := matchForbiddenKey(p.EventName); forbidden != "" {
		return fmt.Errorf("payload eventName %q is forbidden by the locked schema (%s)", p.EventName, forbidden)
	}
	return nil
}

func scanSecretShapedValuesInPayload(p StrictTelemetryPayload) error {
	for _, value := range []string{p.Source, p.EventName} {
		if value == "" {
			continue
		}
		if supportbundle.LooksLikeSecretValue(value) {
			return fmt.Errorf("payload value %q matches a recognized secret shape", value)
		}
	}
	return nil
}

func matchForbiddenKey(key string) string {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return ""
	}
	// Whole-token match: split on `_`, `-`, `.`, and `/` so e.g.
	// `deployment_namespace` does not collide with the forbidden token `name`.
	tokens := splitKeyTokens(normalized)
	for _, forbidden := range PIIForbiddenKeys {
		if normalized == forbidden {
			return forbidden
		}
		for _, t := range tokens {
			if t == forbidden {
				return forbidden
			}
		}
	}
	return ""
}

// splitKeyTokens splits a normalized key on `_`, `-`, `.`, and `/` so the
// forbidden-key matcher compares whole tokens rather than substrings.
func splitKeyTokens(normalized string) []string {
	if normalized == "" {
		return nil
	}
	out := make([]string, 0, 4)
	current := strings.Builder{}
	for _, r := range normalized {
		switch r {
		case '_', '-', '.', '/':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// PIIShape names a recognized PII or secret shape that the locked telemetry schema
// rejects. The names are stable identifiers used in error envelopes and tests.
type PIIShape string

const (
	// PIIShapeEmail matches RFC-shaped email addresses.
	PIIShapeEmail PIIShape = "email"
	// PIIShapeIPv4 matches IPv4 dotted-quad addresses.
	PIIShapeIPv4 PIIShape = "ipv4"
	// PIIShapeIPv6 matches IPv6 colon-separated addresses (full form).
	PIIShapeIPv6 PIIShape = "ipv6"
	// PIIShapeAWSAccessKey matches AWS access key IDs (`AKIA*`/`ASIA*`).
	PIIShapeAWSAccessKey PIIShape = "aws-access-key"
	// PIIShapeMidnightToken matches Midnight platform tokens (`midn_*`).
	PIIShapeMidnightToken PIIShape = "midn-token"
)

// piiShapeMatchers is the panel-locked ordered list of shape matchers run by
// ScanForPIIShapes. The order is stable: the first match wins.
var piiShapeMatchers = []struct {
	shape   PIIShape
	pattern *regexp.Regexp
}{
	{shape: PIIShapeMidnightToken, pattern: regexp.MustCompile(`midn_[A-Za-z0-9_\-]{8,}`)},
	{shape: PIIShapeAWSAccessKey, pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{shape: PIIShapeEmail, pattern: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)},
	{shape: PIIShapeIPv6, pattern: regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}\b`)},
	{shape: PIIShapeIPv4, pattern: regexp.MustCompile(`\b(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(?:\.(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}\b`)},
}

// PIIShapeMatch records one PII shape hit in a payload field.
type PIIShapeMatch struct {
	Field string
	Shape PIIShape
	// Sample is the matched substring; callers may log it for debugging but
	// must never forward it as the original payload's value.
	Sample string
}

// PIIPayloadFields collects every string-valued field on the strict payload that
// the PII sweep should scan. The map is deliberate (not reflection-driven) so a
// future schema change is reviewable in code, not silently broadened.
func PIIPayloadFields(p StrictTelemetryPayload) map[string]string {
	fields := map[string]string{
		"source":    p.Source,
		"eventName": p.EventName,
	}
	for _, flag := range p.FeatureFlags {
		fields["featureFlags."+string(flag.Name)] = string(flag.Name)
	}
	return fields
}

// ScanForPIIShapes walks every string-valued field on the payload and reports
// any PII or secret shape hits. Matches are returned in stable order: by
// (field name, shape name) ascending, so callers can render reproducible output.
func ScanForPIIShapes(p StrictTelemetryPayload) []PIIShapeMatch {
	fields := PIIPayloadFields(p)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var matches []PIIShapeMatch
	for _, key := range keys {
		value := fields[key]
		if value == "" {
			continue
		}
		for _, m := range piiShapeMatchers {
			if loc := m.pattern.FindString(value); loc != "" {
				matches = append(matches, PIIShapeMatch{Field: key, Shape: m.shape, Sample: loc})
			}
		}
	}
	return matches
}

// CheckPayloadForPII runs the PII shape sweep over every string-valued field of
// the payload and returns a structured failurecodes error keyed by
// TELEMETRY_PAYLOAD_PII_DETECTED when any matches are found.
//
// The returned error message lists every match in stable order so triage knows
// exactly which field to scrub. CheckPayloadForPII is the panel-locked refusal
// point: telemetry callers must call it before sending and abort on any error.
func CheckPayloadForPII(p StrictTelemetryPayload) error {
	matches := ScanForPIIShapes(p)
	if len(matches) == 0 {
		return nil
	}
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		parts = append(parts, fmt.Sprintf("%s=%s", match.Field, match.Shape))
	}
	return failurecodes.New(failurecodes.TelemetryPayloadPIIDetected, "telemetry payload field values contain PII shapes: "+strings.Join(parts, ", "))
}
