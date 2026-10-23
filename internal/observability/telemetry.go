package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Config is the panel-locked telemetry envelope toggle. Default-off is enforced by the
// zero value: the only supported way to set Enabled=true is by passing WithOptIn(true)
// to NewConfig. This indirection makes a refactor that flips the default observable in
// code review and in CI fixtures because no other constructor exists.
type Config struct {
	enabled bool
}

// ConfigOption applies a single explicit setting to Config. Use WithOptIn to flip
// telemetry on; no other public option exists, by design.
type ConfigOption func(*Config)

// WithOptIn sets the explicit operator opt-in toggle. The default is always false; an
// argument of true is the only way to flip Enabled to true. Pass false to keep
// telemetry off explicitly (semantically identical to omitting the option).
func WithOptIn(enabled bool) ConfigOption {
	return func(c *Config) {
		c.enabled = enabled
	}
}

// NewConfig constructs a telemetry Config seeded default-off. Apply WithOptIn(true)
// to flip Enabled to true; the unset zero-value Config is the supported default state.
func NewConfig(options ...ConfigOption) Config {
	cfg := Config{}
	for _, opt := range options {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// Enabled reports whether telemetry sending is allowed by this Config. The zero
// value of Config returns false, by design.
func (c Config) Enabled() bool { return c.enabled }

// Validate rejects sending payloads when Enabled is false. The supplied payload must
// be a previously validated TelemetryPayload; this validator only enforces the
// envelope-level opt-in guarantee that the schema is locked default-off.
func (c Config) Validate(payload TelemetryPayload) error {
	if err := payload.Validate(); err != nil {
		return err
	}
	if !c.enabled {
		return failurecodes.New(failurecodes.TelemetryOptInRequired,
			"telemetry config is default-off; call NewConfig(WithOptIn(true)) before sending payloads")
	}
	return nil
}

// FeatureFlagName identifies a Phase 0 feature flag tracked in telemetry.
type FeatureFlagName string

const (
	FeatureFlagACMEIssuance    FeatureFlagName = "acme.issuance"
	FeatureFlagRegistryGC      FeatureFlagName = "registry.gc"
	FeatureFlagAIGateway       FeatureFlagName = "ai.gateway"
	FeatureFlagAutoRemediation FeatureFlagName = "auto.remediation"
	FeatureFlagTelemetry       FeatureFlagName = "telemetry"
)

// AllowedFeatureFlags is the complete Phase 0 feature flag telemetry set.
var AllowedFeatureFlags = []FeatureFlagName{
	FeatureFlagACMEIssuance,
	FeatureFlagRegistryGC,
	FeatureFlagAIGateway,
	FeatureFlagAutoRemediation,
	FeatureFlagTelemetry,
}

var allowedFeatureFlagNames = map[FeatureFlagName]struct{}{
	FeatureFlagACMEIssuance:    {},
	FeatureFlagRegistryGC:      {},
	FeatureFlagAIGateway:       {},
	FeatureFlagAutoRemediation: {},
	FeatureFlagTelemetry:       {},
}

// FeatureFlagState records whether a feature flag is enabled at emission time.
type FeatureFlagState struct {
	Name    FeatureFlagName `json:"name"`
	Enabled bool            `json:"enabled"`
}

// TelemetryPayload is the strict Phase 0 telemetry envelope.
type TelemetryPayload struct {
	SchemaVersion string             `json:"schemaVersion"`
	EventName     string             `json:"eventName"`
	EmittedAt     time.Time          `json:"emittedAt"`
	Source        string             `json:"source"`
	FeatureFlags  []FeatureFlagState `json:"featureFlags"`
}

var allowedTelemetryPayloadKeys = map[string]struct{}{
	"schemaVersion": {},
	"eventName":     {},
	"emittedAt":     {},
	"source":        {},
	"featureFlags":  {},
}

var allowedFeatureFlagStateKeys = map[string]struct{}{
	"name":    {},
	"enabled": {},
}

var requiredTelemetryPayloadKeys = []string{"schemaVersion", "eventName", "emittedAt", "source", "featureFlags"}

// InMemoryFeatureFlagStore is a process-local feature flag store for Phase 0 diagnostics.
type InMemoryFeatureFlagStore struct {
	mu    sync.RWMutex
	flags map[FeatureFlagName]bool
}

// NewInMemoryFeatureFlagStore returns a store initialized with every allowed flag disabled.
func NewInMemoryFeatureFlagStore(initial []FeatureFlagState) (*InMemoryFeatureFlagStore, error) {
	store := &InMemoryFeatureFlagStore{flags: make(map[FeatureFlagName]bool, len(AllowedFeatureFlags))}
	for _, name := range AllowedFeatureFlags {
		store.flags[name] = false
	}
	for i, flag := range initial {
		if err := flag.Validate(); err != nil {
			return nil, fmt.Errorf("initial[%d]: %w", i, err)
		}
		store.flags[flag.Name] = flag.Enabled
	}
	return store, nil
}

// List returns all allowed feature flags in deterministic allowlist order.
func (s *InMemoryFeatureFlagStore) List() []FeatureFlagState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	flags := make([]FeatureFlagState, 0, len(AllowedFeatureFlags))
	for _, name := range AllowedFeatureFlags {
		flags = append(flags, FeatureFlagState{Name: name, Enabled: s.flags[name]})
	}
	return flags
}

// Enable turns on an allowed feature flag.
func (s *InMemoryFeatureFlagStore) Enable(name FeatureFlagName) error {
	return s.set(name, true)
}

// Disable turns off an allowed feature flag.
func (s *InMemoryFeatureFlagStore) Disable(name FeatureFlagName) error {
	return s.set(name, false)
}

// DoctorMap returns a map suitable for doctor output and support diagnostics.
func (s *InMemoryFeatureFlagStore) DoctorMap() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	flags := make(map[string]bool, len(AllowedFeatureFlags))
	for _, name := range AllowedFeatureFlags {
		flags[string(name)] = s.flags[name]
	}
	return flags
}

func (s *InMemoryFeatureFlagStore) set(name FeatureFlagName, enabled bool) error {
	if _, ok := allowedFeatureFlagNames[name]; !ok {
		return fmt.Errorf("feature flag %q is not allowed", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flags[name] = enabled
	return nil
}

// BuildTelemetryPayload returns a validated telemetry payload with a fixed key set.
func BuildTelemetryPayload(eventName string, emittedAt time.Time, source string, flags []FeatureFlagState) (TelemetryPayload, error) {
	payload := TelemetryPayload{
		SchemaVersion: "v1",
		EventName:     eventName,
		EmittedAt:     emittedAt,
		Source:        source,
		FeatureFlags:  append([]FeatureFlagState(nil), flags...),
	}
	if err := payload.Validate(); err != nil {
		return TelemetryPayload{}, err
	}
	return payload, nil
}

// ValidateTelemetryPayloadJSON rejects telemetry JSON with unknown top-level keys.
func ValidateTelemetryPayloadJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	for key := range raw {
		if _, ok := allowedTelemetryPayloadKeys[key]; !ok {
			return fmt.Errorf("telemetry payload key %q is not allowed", key)
		}
	}
	if err := validateTelemetryPayloadAllowedFields(raw); err != nil {
		return err
	}
	var payload TelemetryPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	return payload.Validate()
}

func validateTelemetryPayloadAllowedFields(raw map[string]json.RawMessage) error {
	for _, key := range requiredTelemetryPayloadKeys {
		if _, ok := raw[key]; !ok {
			return fmt.Errorf("telemetry payload key %q is required", key)
		}
	}
	var rawFlags []map[string]json.RawMessage
	if err := json.Unmarshal(raw["featureFlags"], &rawFlags); err != nil {
		return fmt.Errorf("featureFlags: %w", err)
	}
	for i, rawFlag := range rawFlags {
		for key := range rawFlag {
			if _, ok := allowedFeatureFlagStateKeys[key]; !ok {
				return fmt.Errorf("featureFlags[%d] key %q is not allowed", i, key)
			}
		}
		if _, ok := rawFlag["name"]; !ok {
			return fmt.Errorf("featureFlags[%d] key %q is required", i, "name")
		}
		if _, ok := rawFlag["enabled"]; !ok {
			return fmt.Errorf("featureFlags[%d] key %q is required", i, "enabled")
		}
	}
	return nil
}

// Validate checks required envelope fields and the closed feature flag set.
func (p TelemetryPayload) Validate() error {
	if p.SchemaVersion != "v1" {
		return errors.New("telemetry schemaVersion must be v1")
	}
	if strings.TrimSpace(p.EventName) == "" {
		return errors.New("telemetry eventName is required")
	}
	if p.EmittedAt.IsZero() {
		return errors.New("telemetry emittedAt is required")
	}
	if strings.TrimSpace(p.Source) == "" {
		return errors.New("telemetry source is required")
	}
	seen := make(map[FeatureFlagName]struct{}, len(p.FeatureFlags))
	for i, flag := range p.FeatureFlags {
		if err := flag.Validate(); err != nil {
			return fmt.Errorf("featureFlags[%d]: %w", i, err)
		}
		if _, ok := seen[flag.Name]; ok {
			return fmt.Errorf("featureFlags[%d]: duplicate feature flag %q", i, flag.Name)
		}
		seen[flag.Name] = struct{}{}
	}
	return nil
}

// Validate checks that the feature flag name is in the Phase 0 allowlist.
func (s FeatureFlagState) Validate() error {
	if _, ok := allowedFeatureFlagNames[s.Name]; !ok {
		return fmt.Errorf("feature flag %q is not allowed", s.Name)
	}
	return nil
}
