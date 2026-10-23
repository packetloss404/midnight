package observability

import (
	"strings"
	"testing"
)

func TestValidateAlertMetadataSetIsClean(t *testing.T) {
	if err := ValidateAlertMetadataSet(LockedAlertMetadata); err != nil {
		t.Fatalf("ValidateAlertMetadataSet() error = %v", err)
	}
}

func TestValidateAlertMetadataSetCoversEveryLockedAlert(t *testing.T) {
	for _, alert := range LockedAlerts {
		if _, ok := AlertMetadataByName(alert.Name); !ok {
			t.Fatalf("AlertMetadataByName(%q) = nil, want metadata for every locked alert", alert.Name)
		}
	}
}

func TestValidateAlertMetadataSetRejectsMissingFields(t *testing.T) {
	cases := map[string]func(*AlertMetadata){
		"missing id":           func(m *AlertMetadata) { m.AlertID = "" },
		"non-kebab id":         func(m *AlertMetadata) { m.AlertID = "Alert_DiskPressure" },
		"missing name":         func(m *AlertMetadata) { m.Name = "" },
		"missing severity":     func(m *AlertMetadata) { m.Severity = "" },
		"unknown severity":     func(m *AlertMetadata) { m.Severity = "rumor" },
		"missing runbook":      func(m *AlertMetadata) { m.RunbookLink = "" },
		"runbook bad scheme":   func(m *AlertMetadata) { m.RunbookLink = "ftp://docs.example/run" },
		"missing first action": func(m *AlertMetadata) { m.SuggestedFirstAction = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			set := append([]AlertMetadata(nil), LockedAlertMetadata...)
			mut(&set[0])
			if err := ValidateAlertMetadataSet(set); err == nil {
				t.Fatal("ValidateAlertMetadataSet() error = nil, want validation error")
			}
		})
	}
}

func TestValidateAlertMetadataSetRejectsCountMismatch(t *testing.T) {
	if err := ValidateAlertMetadataSet(LockedAlertMetadata[:7]); err == nil {
		t.Fatal("ValidateAlertMetadataSet() error = nil, want count mismatch error")
	}
}

func TestValidateAlertMetadataSetRejectsDuplicateID(t *testing.T) {
	set := append([]AlertMetadata(nil), LockedAlertMetadata...)
	set[1].AlertID = set[0].AlertID
	if err := ValidateAlertMetadataSet(set); err == nil {
		t.Fatal("ValidateAlertMetadataSet() error = nil, want duplicate id error")
	}
}

func TestAlertMetadataValidateAcceptsHTTPSRunbook(t *testing.T) {
	m := LockedAlertMetadata[0]
	m.RunbookLink = "https://runbooks.example/disk-pressure"
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAlertMetadataValidateRejectsTrailingDashID(t *testing.T) {
	m := LockedAlertMetadata[0]
	m.AlertID = "alert-disk-"
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("Validate() error = %v, want kebab-case error", err)
	}
}

func TestValidateStrictAcceptsLockedMetadata(t *testing.T) {
	for _, meta := range LockedAlertMetadata {
		if err := ValidateStrict(meta); err != nil {
			t.Fatalf("ValidateStrict(%q) error = %v, want acceptance for locked metadata", meta.Name, err)
		}
	}
}

func TestValidateStrictRejectsBadInputs(t *testing.T) {
	cases := map[string]struct {
		mut  func(*AlertMetadata)
		want string
	}{
		"missing alert_id":     {mut: func(m *AlertMetadata) { m.AlertID = "" }, want: "alert_id"},
		"missing severity":     {mut: func(m *AlertMetadata) { m.Severity = "" }, want: "severity"},
		"missing runbook_link": {mut: func(m *AlertMetadata) { m.RunbookLink = "" }, want: "runbook_link"},
		"missing first_action": {mut: func(m *AlertMetadata) { m.SuggestedFirstAction = "" }, want: "first_action"},
		"empty summary":        {mut: func(m *AlertMetadata) { m.Summary = "" }, want: "summary"},
		"whitespace summary":   {mut: func(m *AlertMetadata) { m.Summary = "   " }, want: "summary"},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			m := LockedAlertMetadata[0]
			tt.mut(&m)
			err := ValidateStrict(m)
			if err == nil {
				t.Fatalf("ValidateStrict() error = nil, want missing %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateStrict() error = %v, want mention of %q", err, tt.want)
			}
		})
	}
}

func TestValidateStrictRejectsZeroValueMetadata(t *testing.T) {
	err := ValidateStrict(AlertMetadata{})
	if err == nil {
		t.Fatal("ValidateStrict(zero) error = nil, want missing-fields error")
	}
	for _, want := range []string{"alert_id", "severity", "runbook_link", "first_action", "summary"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateStrict(zero) error = %v, want mention of %q", err, want)
		}
	}
}
