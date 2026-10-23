package enterprise

import (
	"strings"
	"testing"
)

func validControlsPlanInput() ControlsPlan {
	return ControlsPlan{
		CustomerID:    "cust-123",
		BYOK:          BYOKPolicy{Mode: "customer-managed", RotationDays: 90, KeyRefs: []KeyRef{{Provider: "aws-kms", Region: "us-east-1", URI: "awskms://kms.us-east-1.amazonaws.com/key/abc"}}},
		LTS:           LTSPolicy{Channel: "enterprise-lts", SupportedUntil: "2029-04-26", PatchCadenceDays: 30},
		SupportSLA:    SupportSLA{Tier: "premium", ResponseMinutes: map[string]int{"sev1": 30, "sev2": 120, "sev3": 480, "sev4": 1440}},
		DPA:           DPAControls{DPAAccepted: true, SubprocessorNoticeDays: 30, SubprocessorListURL: "https://trust.midnight.dev/subprocessors", ApprovedSubprocessors: []string{"object-storage"}},
		DataResidency: DataResidency{Mode: "single-region", AllowedRegions: []string{"us-east-1"}, TelemetryExport: "metadata-only"},
	}
}

func TestNewControlsPlanReadyForValidEnterpriseInputs(t *testing.T) {
	plan := NewControlsPlan(validControlsPlanInput())
	if plan.SchemaVersion != ControlsPlanSchemaVersion {
		t.Fatalf("SchemaVersion = %q", plan.SchemaVersion)
	}
	if plan.Status != PlanStatusReady || len(plan.Issues) != 0 {
		t.Fatalf("Status = %q issues = %#v, want ready/no issues", plan.Status, plan.Issues)
	}
	if err := ValidateControlsPlan(plan); err != nil {
		t.Fatalf("ValidateControlsPlan() error = %v", err)
	}
}

func TestNewControlsPlanBlocksInvalidBYOKAndResidency(t *testing.T) {
	input := validControlsPlanInput()
	input.BYOK.Mode = "customer-managed"
	input.BYOK.KeyRefs = nil
	input.DataResidency.Mode = "single-region"
	input.DataResidency.AllowedRegions = []string{"us-east-1", "eu-west-1"}
	plan := NewControlsPlan(input)
	if plan.Status != PlanStatusBlocked {
		t.Fatalf("Status = %q, want blocked", plan.Status)
	}
	joined := ""
	for _, issue := range plan.Issues {
		joined += issue.Field + "\n"
	}
	for _, want := range []string{"byok.key_refs", "data_residency.allowed_regions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %#v, want field %q", plan.Issues, want)
		}
	}
	if err := ValidateControlsPlan(plan); err != nil {
		t.Fatalf("ValidateControlsPlan() error = %v", err)
	}
}

func TestControlsPlanStrictJSONRoundTrip(t *testing.T) {
	plan := NewControlsPlan(validControlsPlanInput())
	data, err := MarshalControlsPlanJSON(plan)
	if err != nil {
		t.Fatalf("MarshalControlsPlanJSON() error = %v", err)
	}
	got, err := UnmarshalControlsPlanJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalControlsPlanJSON() error = %v", err)
	}
	if got.SchemaVersion != ControlsPlanSchemaVersion || got.CustomerID != plan.CustomerID {
		t.Fatalf("round trip = %#v", got)
	}
	bad := append(data[:len(data)-2], []byte(",\n  \"unknown\": true\n}")...)
	if _, err := UnmarshalControlsPlanJSON(bad); err == nil {
		t.Fatalf("UnmarshalControlsPlanJSON() error = nil, want unknown field error")
	}
}

func TestControlsPlanRejectsSecretMaterialInKeyURI(t *testing.T) {
	input := validControlsPlanInput()
	input.BYOK.KeyRefs[0].URI = "https://kms.example/key/abc?secret=plaintext"
	plan := NewControlsPlan(input)
	if plan.Status != PlanStatusBlocked {
		t.Fatalf("Status = %q, want blocked", plan.Status)
	}
	found := false
	for _, issue := range plan.Issues {
		if issue.Field == "byok.key_refs[0].uri" && strings.Contains(issue.Message, "secret material") {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %#v, want secret material issue", plan.Issues)
	}
}

func TestControlsPlanRequiresAbsoluteSubprocessorListURL(t *testing.T) {
	input := validControlsPlanInput()
	input.DPA.SubprocessorListURL = "/subprocessors"
	plan := NewControlsPlan(input)
	if plan.Status != PlanStatusBlocked {
		t.Fatalf("Status = %q, want blocked", plan.Status)
	}
	found := false
	for _, issue := range plan.Issues {
		if issue.Field == "dpa.subprocessor_list_url" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %#v, want subprocessor URL issue", plan.Issues)
	}
}
