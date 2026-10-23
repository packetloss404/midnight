package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// FeatureFlagDoctorSchemaVersion is the locked schema version emitted by
// MarshalFeatureFlagDoctorView and required by UnmarshalFeatureFlagDoctorView.
const FeatureFlagDoctorSchemaVersion = "1"

// featureFlagDoctorEnvelope is the on-the-wire JSON shape: schemaVersion + view.
type featureFlagDoctorEnvelope struct {
	SchemaVersion string                `json:"schemaVersion"`
	View          FeatureFlagDoctorView `json:"view"`
}

// MarshalFeatureFlagDoctorView returns a stable JSON byte representation of the
// doctor view, wrapped in a `{"schemaVersion":"1","view":{...}}` envelope.
//
// Entries are emitted in name-sorted order (matching FeatureFlagDoctorView.View)
// so two equivalent views always produce byte-identical output. Timestamps are
// serialized in RFC3339Nano via the standard time encoder, which produces
// deterministic output for any given moment.
func MarshalFeatureFlagDoctorView(view FeatureFlagDoctorView) ([]byte, error) {
	if err := view.Validate(); err != nil {
		return nil, fmt.Errorf("feature flag doctor view: %w", err)
	}
	stable := FeatureFlagDoctorView{Entries: append([]FeatureFlagDoctorEntry(nil), view.Entries...)}
	sort.SliceStable(stable.Entries, func(i, j int) bool { return stable.Entries[i].Name < stable.Entries[j].Name })
	envelope := featureFlagDoctorEnvelope{SchemaVersion: FeatureFlagDoctorSchemaVersion, View: stable}
	return json.MarshalIndent(envelope, "", "  ")
}

// UnmarshalFeatureFlagDoctorView decodes the stable JSON byte representation
// produced by MarshalFeatureFlagDoctorView. The envelope schemaVersion must
// equal FeatureFlagDoctorSchemaVersion; unknown fields are rejected.
func UnmarshalFeatureFlagDoctorView(data []byte) (FeatureFlagDoctorView, error) {
	if len(data) == 0 {
		return FeatureFlagDoctorView{}, errors.New("feature flag doctor view json is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope featureFlagDoctorEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return FeatureFlagDoctorView{}, fmt.Errorf("feature flag doctor view: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return FeatureFlagDoctorView{}, errors.New("feature flag doctor view json must contain a single document")
		}
		return FeatureFlagDoctorView{}, err
	}
	if envelope.SchemaVersion != FeatureFlagDoctorSchemaVersion {
		return FeatureFlagDoctorView{}, fmt.Errorf("feature flag doctor view schemaVersion %q is not supported; expected %q", envelope.SchemaVersion, FeatureFlagDoctorSchemaVersion)
	}
	if err := envelope.View.Validate(); err != nil {
		return FeatureFlagDoctorView{}, fmt.Errorf("feature flag doctor view: %w", err)
	}
	return envelope.View, nil
}

// FeatureFlagSource records where the most recent state for a flag came from.
type FeatureFlagSource string

const (
	// FeatureFlagSourceDefault marks the panel-locked default state (default-off for Phase 0).
	FeatureFlagSourceDefault FeatureFlagSource = "default"
	// FeatureFlagSourceConfigFile marks values loaded from a packaged or operator config file.
	FeatureFlagSourceConfigFile FeatureFlagSource = "config-file"
	// FeatureFlagSourceEnv marks values loaded from environment variables at boot.
	FeatureFlagSourceEnv FeatureFlagSource = "env"
	// FeatureFlagSourceAPI marks values toggled via the live operator API/CLI.
	FeatureFlagSourceAPI FeatureFlagSource = "api"
)

// FeatureFlagDoctorEntry is one row in the doctor view: name, current/default state, last-toggled
// timestamp, and the source that last set the value.
type FeatureFlagDoctorEntry struct {
	Name           FeatureFlagName   `json:"name"`
	Enabled        bool              `json:"enabled"`
	DefaultEnabled bool              `json:"defaultEnabled"`
	Source         FeatureFlagSource `json:"source"`
	LastToggledAt  time.Time         `json:"lastToggledAt"`
}

// FeatureFlagDoctorView is the deterministic ordered view used by `midnight doctor` output.
type FeatureFlagDoctorView struct {
	Entries []FeatureFlagDoctorEntry `json:"entries"`
}

// FeatureFlagDoctor is the in-memory state store for the doctor view. It is concurrency-safe.
//
// Construction defaults every allowed flag to disabled, source FeatureFlagSourceDefault, and
// LastToggledAt set to the doctor's construction time. Subsequent toggles update both source and
// timestamp. The doctor exposes Set so installers and CLI surfaces can record flag provenance.
type FeatureFlagDoctor struct {
	mu       sync.RWMutex
	clock    func() time.Time
	defaults map[FeatureFlagName]bool
	state    map[FeatureFlagName]FeatureFlagDoctorEntry
}

// NewFeatureFlagDoctor returns a doctor seeded with the Phase 0 default-off state for every allowed flag.
func NewFeatureFlagDoctor(clock func() time.Time) *FeatureFlagDoctor {
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	doctor := &FeatureFlagDoctor{
		clock:    clock,
		defaults: make(map[FeatureFlagName]bool, len(AllowedFeatureFlags)),
		state:    make(map[FeatureFlagName]FeatureFlagDoctorEntry, len(AllowedFeatureFlags)),
	}
	for _, name := range AllowedFeatureFlags {
		doctor.defaults[name] = false
		doctor.state[name] = FeatureFlagDoctorEntry{
			Name:           name,
			Enabled:        false,
			DefaultEnabled: false,
			Source:         FeatureFlagSourceDefault,
			LastToggledAt:  now,
		}
	}
	return doctor
}

// Set updates the doctor's state for one flag, recording the source and current clock time.
func (d *FeatureFlagDoctor) Set(name FeatureFlagName, enabled bool, source FeatureFlagSource) error {
	if err := validateDoctorSource(source); err != nil {
		return err
	}
	if _, ok := allowedFeatureFlagNames[name]; !ok {
		return fmt.Errorf("feature flag %q is not allowed", name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state[name] = FeatureFlagDoctorEntry{
		Name:           name,
		Enabled:        enabled,
		DefaultEnabled: d.defaults[name],
		Source:         source,
		LastToggledAt:  d.clock(),
	}
	return nil
}

// View returns the deterministic ordered doctor view, sorted by flag name.
func (d *FeatureFlagDoctor) View() FeatureFlagDoctorView {
	d.mu.RLock()
	defer d.mu.RUnlock()
	entries := make([]FeatureFlagDoctorEntry, 0, len(d.state))
	for _, name := range AllowedFeatureFlags {
		entries = append(entries, d.state[name])
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return FeatureFlagDoctorView{Entries: entries}
}

// Validate checks that a doctor view is internally consistent and lists every allowed feature flag exactly once.
func (v FeatureFlagDoctorView) Validate() error {
	if len(v.Entries) != len(AllowedFeatureFlags) {
		return fmt.Errorf("feature flag doctor view must include %d entries", len(AllowedFeatureFlags))
	}
	seen := make(map[FeatureFlagName]struct{}, len(v.Entries))
	for i, entry := range v.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("entries[%d]: %w", i, err)
		}
		if _, dup := seen[entry.Name]; dup {
			return fmt.Errorf("entries[%d]: duplicate feature flag %q", i, entry.Name)
		}
		seen[entry.Name] = struct{}{}
	}
	return nil
}

// Validate checks that one doctor entry has a known flag name, source, and recorded toggle time.
func (e FeatureFlagDoctorEntry) Validate() error {
	if _, ok := allowedFeatureFlagNames[e.Name]; !ok {
		return fmt.Errorf("feature flag %q is not allowed", e.Name)
	}
	if err := validateDoctorSource(e.Source); err != nil {
		return err
	}
	if e.LastToggledAt.IsZero() {
		return errors.New("doctor entry lastToggledAt is required")
	}
	return nil
}

func validateDoctorSource(source FeatureFlagSource) error {
	switch source {
	case FeatureFlagSourceDefault, FeatureFlagSourceConfigFile, FeatureFlagSourceEnv, FeatureFlagSourceAPI:
		return nil
	default:
		return fmt.Errorf("feature flag source %q is invalid", source)
	}
}

// String returns a human-readable summary of one doctor entry.
func (e FeatureFlagDoctorEntry) String() string {
	state := "disabled"
	if e.Enabled {
		state = "enabled"
	}
	return strings.Join([]string{string(e.Name), state, "source=" + string(e.Source), "last-toggled=" + e.LastToggledAt.UTC().Format(time.RFC3339)}, " ")
}
