package observability

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildTelemetryPayload(t *testing.T) {
	payload, err := BuildTelemetryPayload("phase0.health", time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), "midnight-api", []FeatureFlagState{
		{Name: FeatureFlagACMEIssuance, Enabled: true},
		{Name: FeatureFlagRegistryGC, Enabled: false},
		{Name: FeatureFlagAIGateway, Enabled: false},
		{Name: FeatureFlagAutoRemediation, Enabled: false},
		{Name: FeatureFlagTelemetry, Enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildTelemetryPayload() error = %v", err)
	}
	if payload.SchemaVersion != "v1" || len(payload.FeatureFlags) != len(AllowedFeatureFlags) {
		t.Fatalf("BuildTelemetryPayload() = %+v, want v1 payload with feature flags", payload)
	}
}

func TestTelemetryPayloadValidateRejectsUnknownFeatureFlag(t *testing.T) {
	payload := TelemetryPayload{
		SchemaVersion: "v1",
		EventName:     "phase0.health",
		EmittedAt:     time.Now(),
		Source:        "midnight-api",
		FeatureFlags:  []FeatureFlagState{{Name: FeatureFlagName("unknown.flag"), Enabled: true}},
	}
	if err := payload.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unknown feature flag error")
	}
}

func TestTelemetryPayloadValidateRejectsDuplicateFeatureFlag(t *testing.T) {
	payload := TelemetryPayload{
		SchemaVersion: "v1",
		EventName:     "phase0.health",
		EmittedAt:     time.Now(),
		Source:        "midnight-api",
		FeatureFlags: []FeatureFlagState{
			{Name: FeatureFlagTelemetry, Enabled: true},
			{Name: FeatureFlagTelemetry, Enabled: false},
		},
	}
	if err := payload.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate feature flag error")
	}
}

func TestValidateTelemetryPayloadJSONRejectsUnknownKey(t *testing.T) {
	data := []byte(`{"schemaVersion":"v1","eventName":"phase0.health","emittedAt":"2026-04-25T12:00:00Z","source":"midnight-api","featureFlags":[],"customerId":"secret"}`)
	if err := ValidateTelemetryPayloadJSON(data); err == nil {
		t.Fatal("ValidateTelemetryPayloadJSON() error = nil, want unknown key error")
	}
}

func TestValidateTelemetryPayloadJSONAcceptsBuilderOutput(t *testing.T) {
	payload, err := BuildTelemetryPayload("phase0.health", time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), "midnight-api", []FeatureFlagState{{Name: FeatureFlagTelemetry, Enabled: true}})
	if err != nil {
		t.Fatalf("BuildTelemetryPayload() error = %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := ValidateTelemetryPayloadJSON(data); err != nil {
		t.Fatalf("ValidateTelemetryPayloadJSON() error = %v", err)
	}
}

func TestInMemoryFeatureFlagStoreListEnableDisableAndDoctorMap(t *testing.T) {
	store, err := NewInMemoryFeatureFlagStore([]FeatureFlagState{{Name: FeatureFlagTelemetry, Enabled: true}})
	if err != nil {
		t.Fatalf("NewInMemoryFeatureFlagStore() error = %v", err)
	}

	if err := store.Enable(FeatureFlagAIGateway); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if err := store.Disable(FeatureFlagTelemetry); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}

	flags := store.List()
	if len(flags) != len(AllowedFeatureFlags) {
		t.Fatalf("List() returned %d flags, want %d", len(flags), len(AllowedFeatureFlags))
	}
	doctor := store.DoctorMap()
	if !doctor[string(FeatureFlagAIGateway)] || doctor[string(FeatureFlagTelemetry)] {
		t.Fatalf("DoctorMap() = %+v, want ai.gateway enabled and telemetry disabled", doctor)
	}
}

func TestInMemoryFeatureFlagStoreRejectsUnknownFlag(t *testing.T) {
	store, err := NewInMemoryFeatureFlagStore(nil)
	if err != nil {
		t.Fatalf("NewInMemoryFeatureFlagStore() error = %v", err)
	}
	if err := store.Enable(FeatureFlagName("unknown.flag")); err == nil {
		t.Fatal("Enable() error = nil, want unknown flag error")
	}
}

func TestValidateTelemetryPayloadJSONRejectsUnknownFeatureFlagField(t *testing.T) {
	data := []byte(`{"schemaVersion":"v1","eventName":"phase0.health","emittedAt":"2026-04-25T12:00:00Z","source":"midnight-api","featureFlags":[{"name":"telemetry","enabled":true,"variant":"beta"}]}`)
	if err := ValidateTelemetryPayloadJSON(data); err == nil {
		t.Fatal("ValidateTelemetryPayloadJSON() error = nil, want unknown feature flag field error")
	}
}

func TestValidateTelemetryPayloadJSONRequiresAllowedFields(t *testing.T) {
	data := []byte(`{"schemaVersion":"v1","eventName":"phase0.health","emittedAt":"2026-04-25T12:00:00Z","source":"midnight-api"}`)
	if err := ValidateTelemetryPayloadJSON(data); err == nil {
		t.Fatal("ValidateTelemetryPayloadJSON() error = nil, want missing featureFlags error")
	}
}

func TestNewConfigDefaultsOff(t *testing.T) {
	cfg := NewConfig()
	if cfg.Enabled() {
		t.Fatal("NewConfig() Enabled = true, want default-off")
	}
	// Zero value is also default-off.
	var zero Config
	if zero.Enabled() {
		t.Fatal("zero Config Enabled = true, want false")
	}
}

func TestConfigWithOptInTrueFlipsEnabled(t *testing.T) {
	cfg := NewConfig(WithOptIn(true))
	if !cfg.Enabled() {
		t.Fatal("NewConfig(WithOptIn(true)) Enabled = false, want true")
	}
}

func TestConfigWithOptInFalseKeepsEnabledOff(t *testing.T) {
	cfg := NewConfig(WithOptIn(false))
	if cfg.Enabled() {
		t.Fatal("NewConfig(WithOptIn(false)) Enabled = true, want false")
	}
}

func TestConfigValidateRejectsSendingWhenDefaultOff(t *testing.T) {
	payload, err := BuildTelemetryPayload("phase0.health", time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), "midnight-api", []FeatureFlagState{{Name: FeatureFlagTelemetry, Enabled: true}})
	if err != nil {
		t.Fatalf("BuildTelemetryPayload() error = %v", err)
	}
	cfg := NewConfig()
	if err := cfg.Validate(payload); err == nil {
		t.Fatal("Config.Validate() error = nil, want default-off rejection")
	}
}

func TestConfigValidateAcceptsSendingWhenOptedIn(t *testing.T) {
	payload, err := BuildTelemetryPayload("phase0.health", time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), "midnight-api", []FeatureFlagState{{Name: FeatureFlagTelemetry, Enabled: true}})
	if err != nil {
		t.Fatalf("BuildTelemetryPayload() error = %v", err)
	}
	cfg := NewConfig(WithOptIn(true))
	if err := cfg.Validate(payload); err != nil {
		t.Fatalf("Config.Validate() error = %v, want acceptance once opted in", err)
	}
}

func TestConfigValidateRejectsInvalidPayloadEvenWhenOptedIn(t *testing.T) {
	cfg := NewConfig(WithOptIn(true))
	if err := cfg.Validate(TelemetryPayload{}); err == nil {
		t.Fatal("Config.Validate() error = nil, want envelope validation error")
	}
}
