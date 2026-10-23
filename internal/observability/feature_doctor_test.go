package observability

import (
	"strings"
	"testing"
	"time"
)

func TestNewFeatureFlagDoctorSeedsDefaultsForAllAllowedFlags(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) }
	doctor := NewFeatureFlagDoctor(clock)
	view := doctor.View()
	if len(view.Entries) != len(AllowedFeatureFlags) {
		t.Fatalf("View() entries = %d, want %d", len(view.Entries), len(AllowedFeatureFlags))
	}
	for _, entry := range view.Entries {
		if entry.Enabled || entry.Source != FeatureFlagSourceDefault {
			t.Fatalf("entry %+v should be default-off", entry)
		}
		if !entry.LastToggledAt.Equal(clock()) {
			t.Fatalf("entry %+v lastToggledAt = %v, want %v", entry, entry.LastToggledAt, clock())
		}
	}
	if err := view.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestFeatureFlagDoctorSetUpdatesEntry(t *testing.T) {
	tick := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	doctor := NewFeatureFlagDoctor(func() time.Time { return tick })
	tick = tick.Add(2 * time.Hour)
	if err := doctor.Set(FeatureFlagAIGateway, true, FeatureFlagSourceAPI); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	view := doctor.View()
	for _, entry := range view.Entries {
		if entry.Name == FeatureFlagAIGateway {
			if !entry.Enabled || entry.Source != FeatureFlagSourceAPI || !entry.LastToggledAt.Equal(tick) {
				t.Fatalf("entry %+v not updated by Set()", entry)
			}
		}
	}
}

func TestFeatureFlagDoctorSetRejectsUnknownFlag(t *testing.T) {
	doctor := NewFeatureFlagDoctor(nil)
	if err := doctor.Set(FeatureFlagName("rogue.flag"), true, FeatureFlagSourceAPI); err == nil {
		t.Fatal("Set() error = nil, want unknown flag error")
	}
}

func TestFeatureFlagDoctorSetRejectsUnknownSource(t *testing.T) {
	doctor := NewFeatureFlagDoctor(nil)
	if err := doctor.Set(FeatureFlagAIGateway, true, FeatureFlagSource("rogue")); err == nil {
		t.Fatal("Set() error = nil, want invalid source error")
	}
}

func TestFeatureFlagDoctorViewIsSortedByName(t *testing.T) {
	doctor := NewFeatureFlagDoctor(nil)
	view := doctor.View()
	for i := 1; i < len(view.Entries); i++ {
		if view.Entries[i-1].Name >= view.Entries[i].Name {
			t.Fatalf("View() entries not sorted: %q >= %q", view.Entries[i-1].Name, view.Entries[i].Name)
		}
	}
}

func TestFeatureFlagDoctorEntryStringIncludesSource(t *testing.T) {
	doctor := NewFeatureFlagDoctor(func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) })
	if err := doctor.Set(FeatureFlagAIGateway, true, FeatureFlagSourceAPI); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	for _, entry := range doctor.View().Entries {
		if entry.Name == FeatureFlagAIGateway {
			s := entry.String()
			if !strings.Contains(s, "enabled") || !strings.Contains(s, "source=api") {
				t.Fatalf("Entry.String() = %q, want enabled and source=api", s)
			}
		}
	}
}

func TestFeatureFlagDoctorViewValidateRejectsDuplicates(t *testing.T) {
	doctor := NewFeatureFlagDoctor(nil)
	view := doctor.View()
	view.Entries[1] = view.Entries[0]
	if err := view.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate error")
	}
}

func TestMarshalFeatureFlagDoctorViewIsStable(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) }
	doctor := NewFeatureFlagDoctor(clock)
	view := doctor.View()
	a, err := MarshalFeatureFlagDoctorView(view)
	if err != nil {
		t.Fatalf("MarshalFeatureFlagDoctorView() error = %v", err)
	}
	b, err := MarshalFeatureFlagDoctorView(view)
	if err != nil {
		t.Fatalf("MarshalFeatureFlagDoctorView() error = %v", err)
	}
	if !strings.EqualFold(string(a), string(b)) {
		t.Fatalf("MarshalFeatureFlagDoctorView() not stable: %q vs %q", a, b)
	}
	for _, want := range []string{
		`"schemaVersion": "1"`,
		`"name":`,
		`"defaultEnabled":`,
		`"lastToggledAt":`,
		`"source":`,
	} {
		if !strings.Contains(string(a), want) {
			t.Fatalf("MarshalFeatureFlagDoctorView() output missing %q: %s", want, a)
		}
	}
}

func TestMarshalUnmarshalFeatureFlagDoctorViewRoundTrip(t *testing.T) {
	tick := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	doctor := NewFeatureFlagDoctor(func() time.Time { return tick })
	tick = tick.Add(2 * time.Hour)
	if err := doctor.Set(FeatureFlagAIGateway, true, FeatureFlagSourceAPI); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	view := doctor.View()
	data, err := MarshalFeatureFlagDoctorView(view)
	if err != nil {
		t.Fatalf("MarshalFeatureFlagDoctorView() error = %v", err)
	}
	got, err := UnmarshalFeatureFlagDoctorView(data)
	if err != nil {
		t.Fatalf("UnmarshalFeatureFlagDoctorView() error = %v", err)
	}
	if len(got.Entries) != len(view.Entries) {
		t.Fatalf("UnmarshalFeatureFlagDoctorView() entry count = %d, want %d", len(got.Entries), len(view.Entries))
	}
	for i := range view.Entries {
		want := view.Entries[i]
		have := got.Entries[i]
		if want.Name != have.Name || want.Enabled != have.Enabled || want.Source != have.Source || !want.LastToggledAt.Equal(have.LastToggledAt) || want.DefaultEnabled != have.DefaultEnabled {
			t.Fatalf("UnmarshalFeatureFlagDoctorView() entry[%d] mismatch: %+v vs %+v", i, have, want)
		}
	}
}

func TestUnmarshalFeatureFlagDoctorViewRejectsUnknownSchemaVersion(t *testing.T) {
	data := []byte(`{"schemaVersion":"2","view":{"entries":[]}}`)
	if _, err := UnmarshalFeatureFlagDoctorView(data); err == nil {
		t.Fatal("UnmarshalFeatureFlagDoctorView() error = nil, want unsupported schemaVersion error")
	}
}

func TestUnmarshalFeatureFlagDoctorViewRejectsUnknownFields(t *testing.T) {
	doctor := NewFeatureFlagDoctor(func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) })
	data, err := MarshalFeatureFlagDoctorView(doctor.View())
	if err != nil {
		t.Fatalf("MarshalFeatureFlagDoctorView() error = %v", err)
	}
	tampered := strings.Replace(string(data), `"schemaVersion": "1"`, `"schemaVersion": "1","extra":"x"`, 1)
	if _, err := UnmarshalFeatureFlagDoctorView([]byte(tampered)); err == nil {
		t.Fatal("UnmarshalFeatureFlagDoctorView() error = nil, want unknown field error")
	}
}

func TestUnmarshalFeatureFlagDoctorViewRejectsEmptyInput(t *testing.T) {
	if _, err := UnmarshalFeatureFlagDoctorView(nil); err == nil {
		t.Fatal("UnmarshalFeatureFlagDoctorView(nil) error = nil, want empty input error")
	}
}

func TestMarshalFeatureFlagDoctorViewListsEveryFlagWithDefaultsAndSource(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) }
	doctor := NewFeatureFlagDoctor(clock)
	view := doctor.View()
	data, err := MarshalFeatureFlagDoctorView(view)
	if err != nil {
		t.Fatalf("MarshalFeatureFlagDoctorView() error = %v", err)
	}
	for _, name := range AllowedFeatureFlags {
		if !strings.Contains(string(data), `"name": "`+string(name)+`"`) {
			t.Fatalf("MarshalFeatureFlagDoctorView() output missing flag %q: %s", name, data)
		}
	}
	if !strings.Contains(string(data), `"source": "default"`) {
		t.Fatalf("MarshalFeatureFlagDoctorView() output missing default source: %s", data)
	}
}
