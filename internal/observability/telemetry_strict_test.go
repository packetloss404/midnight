package observability

import (
	"strings"
	"testing"
	"time"
)

func validStrictPayload() StrictTelemetryPayload {
	return StrictTelemetryPayload{
		TelemetryPayload: TelemetryPayload{
			SchemaVersion: "v1",
			EventName:     "phase0.health",
			EmittedAt:     time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
			Source:        "midnight-api",
			FeatureFlags:  []FeatureFlagState{{Name: FeatureFlagTelemetry, Enabled: true}},
		},
		EventID: 1,
		OptIn:   true,
		Metrics: map[string]float64{"deploys_total": 12, "errors_total": 0},
	}
}

func TestValidateStrictTelemetryPayloadAcceptsCleanPayload(t *testing.T) {
	next, err := ValidateStrictTelemetryPayload(validStrictPayload(), 0)
	if err != nil {
		t.Fatalf("ValidateStrictTelemetryPayload() error = %v", err)
	}
	if next != 1 {
		t.Fatalf("ValidateStrictTelemetryPayload() next id = %d, want 1", next)
	}
}

func TestValidateStrictTelemetryPayloadRejectsOptOut(t *testing.T) {
	p := validStrictPayload()
	p.OptIn = false
	if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want default-off opt-in error")
	}
}

func TestValidateStrictTelemetryPayloadEnforcesMonotonicEventID(t *testing.T) {
	if _, err := ValidateStrictTelemetryPayload(validStrictPayload(), 5); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want monotonic eventId error")
	}
	p := validStrictPayload()
	p.EventID = 6
	if _, err := ValidateStrictTelemetryPayload(p, 5); err != nil {
		t.Fatalf("ValidateStrictTelemetryPayload() error = %v, want acceptance for monotonic id", err)
	}
}

func TestValidateStrictTelemetryPayloadRejectsZeroEventID(t *testing.T) {
	p := validStrictPayload()
	p.EventID = 0
	if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want zero eventId error")
	}
}

func TestValidateStrictTelemetryPayloadRejectsPIIMetricKey(t *testing.T) {
	p := validStrictPayload()
	p.Metrics["customer_id"] = 7
	if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want PII key error")
	}
}

func TestValidateStrictTelemetryPayloadRejectsPIIInSource(t *testing.T) {
	p := validStrictPayload()
	p.Source = "customer-portal"
	if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want PII source error")
	}
}

func TestValidateStrictTelemetryPayloadRejectsSecretShapedValues(t *testing.T) {
	cases := []struct {
		name string
		mut  func(p *StrictTelemetryPayload)
	}{
		{name: "midnight token in eventName", mut: func(p *StrictTelemetryPayload) { p.EventName = "midn_TOKENVALUE12345" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := validStrictPayload()
			tt.mut(&p)
			if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
				t.Fatal("ValidateStrictTelemetryPayload() error = nil, want secret shape error")
			}
		})
	}
}

func TestValidateStrictTelemetryPayloadRequiresSchemaVersion(t *testing.T) {
	p := validStrictPayload()
	p.SchemaVersion = ""
	if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
		t.Fatal("ValidateStrictTelemetryPayload() error = nil, want schemaVersion error")
	}
}

func TestValidateMetricBagRejectsForbiddenKey(t *testing.T) {
	if err := ValidateMetricBag(map[string]float64{"user_email_total": 1}); err == nil {
		t.Fatal("ValidateMetricBag() error = nil, want forbidden key error")
	}
}

func TestValidateMetricBagAcceptsCountKeys(t *testing.T) {
	if err := ValidateMetricBag(map[string]float64{"deploys_total": 1, "operations_total": 2}); err != nil {
		t.Fatalf("ValidateMetricBag() error = %v", err)
	}
}

func TestScanForPIIShapesDetectsAllShapes(t *testing.T) {
	cases := []struct {
		name  string
		field string
		set   func(*StrictTelemetryPayload)
		shape PIIShape
	}{
		{name: "email in source", field: "source", set: func(p *StrictTelemetryPayload) { p.Source = "ops@midnight.example" }, shape: PIIShapeEmail},
		{name: "ipv4 in eventName", field: "eventName", set: func(p *StrictTelemetryPayload) { p.EventName = "scan-from-10.0.0.5" }, shape: PIIShapeIPv4},
		{name: "ipv6 in source", field: "source", set: func(p *StrictTelemetryPayload) { p.Source = "src-2001:0db8:85a3:0000:0000:8a2e:0370:7334" }, shape: PIIShapeIPv6},
		{name: "aws access key in eventName", field: "eventName", set: func(p *StrictTelemetryPayload) { p.EventName = "key-AKIAIOSFODNN7EXAMPLE" }, shape: PIIShapeAWSAccessKey},
		{name: "midn token in source", field: "source", set: func(p *StrictTelemetryPayload) { p.Source = "midn_TOKENVALUE12345" }, shape: PIIShapeMidnightToken},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := validStrictPayload()
			tt.set(&p)
			matches := ScanForPIIShapes(p)
			if len(matches) == 0 {
				t.Fatalf("ScanForPIIShapes() = none, want %s match", tt.shape)
			}
			found := false
			for _, m := range matches {
				if m.Shape == tt.shape && m.Field == tt.field {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("ScanForPIIShapes() = %+v, want %s match on %s", matches, tt.shape, tt.field)
			}
		})
	}
}

func TestCheckPayloadForPIIRejectsAndReturnsStructuredError(t *testing.T) {
	p := validStrictPayload()
	p.Source = "ops@midnight.example"
	err := CheckPayloadForPII(p)
	if err == nil {
		t.Fatal("CheckPayloadForPII() error = nil, want PII detection")
	}
	if !strings.Contains(err.Error(), "TELEMETRY_PAYLOAD_PII_DETECTED") {
		t.Fatalf("CheckPayloadForPII() error = %v, want TELEMETRY_PAYLOAD_PII_DETECTED", err)
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("CheckPayloadForPII() error = %v, want mention of email shape", err)
	}
}

func TestCheckPayloadForPIIAcceptsCleanPayload(t *testing.T) {
	if err := CheckPayloadForPII(validStrictPayload()); err != nil {
		t.Fatalf("CheckPayloadForPII() error = %v, want nil for clean payload", err)
	}
}

func TestValidateStrictTelemetryPayloadRejectsPIIShapes(t *testing.T) {
	cases := []struct {
		name string
		mut  func(p *StrictTelemetryPayload)
	}{
		{name: "email in source", mut: func(p *StrictTelemetryPayload) {
			// Change source to one with email shape; matchForbiddenKey check on "source"
			// uses forbidden-key matching against short keywords (e.g. "name", "user"), so
			// we pick a value not containing those keywords.
			p.Source = "service.dispatch+ops@midnight.example"
		}},
		{name: "midn token in source", mut: func(p *StrictTelemetryPayload) {
			// Avoid forbidden-key keywords in the value text.
			p.Source = "src midn_TOKENVALUE12345"
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := validStrictPayload()
			tt.mut(&p)
			if _, err := ValidateStrictTelemetryPayload(p, 0); err == nil {
				t.Fatal("ValidateStrictTelemetryPayload() error = nil, want PII rejection")
			}
		})
	}
}
