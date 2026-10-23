// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package cloudevents

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		SpecVersion:       SpecVersion,
		ID:                "evt_01HZX9C6F7G8H9JKMNPQRSTVWX",
		Source:            "urn:midnight:midnight-prod-001:project:prj_01HZX5MZZZZZZZZZZ0123456BB",
		Subject:           "deployment/dep_01HZX9C6F7G8H9JKMNPQRSTVWX",
		Type:              TypeDeploymentFailed,
		DataSchema:        "https://midnight.dev/schemas/events/dev.midnight.deployment.failed/1.json",
		Time:              time.Date(2026, 4, 25, 12, 3, 14, 0, time.UTC),
		TraceParent:       "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		MidnightRequestID: "req_01HZXACCEPTED12345678ABCDE",
		MidnightTenantID:  "wsp_01HZXWORKSPACEID0000000000",
		Data: map[string]any{
			"deployment_id": "dep_01HZX9C6F7G8H9JKMNPQRSTVWX",
			"reason_code":   "HEALTHCHECK_FAILED", // not-a-failure-code (test data)
			"sequence":      142,
			"alpha":         "first",
		},
	}
}

func TestEncodeStructuredProducesStableKeyOrdering(t *testing.T) {
	event := validEvent()
	once, err := EncodeStructured(event)
	if err != nil {
		t.Fatalf("EncodeStructured() error = %v", err)
	}
	twice, err := EncodeStructured(event)
	if err != nil {
		t.Fatalf("EncodeStructured() error = %v", err)
	}
	if string(once) != string(twice) {
		t.Fatalf("encoder is non-deterministic\nfirst:  %s\nsecond: %s", once, twice)
	}

	output := string(once)
	keys := []string{
		`"data":`,
		`"datacontenttype":`,
		`"dataschema":`,
		`"id":`,
		`"midnightrequestid":`,
		`"midnighttenantid":`,
		`"source":`,
		`"specversion":`,
		`"subject":`,
		`"time":`,
		`"traceparent":`,
		`"type":`,
	}
	last := -1
	for _, key := range keys {
		idx := strings.Index(output, key)
		if idx < 0 {
			t.Fatalf("encoded output missing %s\n%s", key, output)
		}
		if idx <= last {
			t.Fatalf("keys not in alphabetical order at %s\n%s", key, output)
		}
		last = idx
	}
}

func TestEncodeStructuredCanonicalisesNestedDataKeys(t *testing.T) {
	event := validEvent()
	got, err := EncodeStructured(event)
	if err != nil {
		t.Fatalf("EncodeStructured() error = %v", err)
	}
	gotStr := string(got)
	// alpha < deployment_id < reason_code < sequence sorted lexicographically.
	idxAlpha := strings.Index(gotStr, `"alpha":`)
	idxDeploymentID := strings.Index(gotStr, `"deployment_id":`)
	idxReason := strings.Index(gotStr, `"reason_code":`)
	idxSequence := strings.Index(gotStr, `"sequence":`)
	if idxAlpha < 0 || idxDeploymentID < 0 || idxReason < 0 || idxSequence < 0 {
		t.Fatalf("missing expected data keys in %s", gotStr)
	}
	if !(idxAlpha < idxDeploymentID && idxDeploymentID < idxReason && idxReason < idxSequence) {
		t.Fatalf("data keys not sorted: alpha=%d deployment_id=%d reason_code=%d sequence=%d",
			idxAlpha, idxDeploymentID, idxReason, idxSequence)
	}
}

func TestEncodeStructuredDefaultsDataContentType(t *testing.T) {
	event := validEvent()
	got, err := EncodeStructured(event)
	if err != nil {
		t.Fatalf("EncodeStructured() error = %v", err)
	}
	if !strings.Contains(string(got), `"datacontenttype":"application/json"`) {
		t.Fatalf("default datacontenttype missing in %s", got)
	}
}

func TestValidateEventRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Event)
		wantErr error
	}{
		{name: "missing_specversion", mutate: func(e *Event) { e.SpecVersion = "" }, wantErr: ErrEventInvalid},
		{name: "wrong_specversion", mutate: func(e *Event) { e.SpecVersion = "0.3" }, wantErr: ErrEventInvalid},
		{name: "missing_id", mutate: func(e *Event) { e.ID = "" }, wantErr: ErrEventInvalid},
		{name: "missing_source", mutate: func(e *Event) { e.Source = "" }, wantErr: ErrEventInvalid},
		{name: "relative_source", mutate: func(e *Event) { e.Source = "//midnight/projects/prj_1" }, wantErr: ErrEventInvalid},
		{name: "missing_type", mutate: func(e *Event) { e.Type = "" }, wantErr: ErrEventInvalid},
		{name: "unknown_type", mutate: func(e *Event) { e.Type = "dev.midnight.unknown.event" }, wantErr: ErrUnknownType},
		{name: "missing_time", mutate: func(e *Event) { e.Time = time.Time{} }, wantErr: ErrEventInvalid},
		{name: "missing_traceparent", mutate: func(e *Event) { e.TraceParent = "" }, wantErr: ErrEventInvalid},
		{name: "missing_request_id", mutate: func(e *Event) { e.MidnightRequestID = "" }, wantErr: ErrEventInvalid},
		{name: "missing_tenant_id", mutate: func(e *Event) { e.MidnightTenantID = "" }, wantErr: ErrEventInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := validEvent()
			tc.mutate(&event)
			if err := ValidateEvent(event); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateEvent() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
			if _, err := EncodeStructured(event); !errors.Is(err, tc.wantErr) {
				t.Fatalf("EncodeStructured() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

func TestCatalogIsDeterministic(t *testing.T) {
	first := All()
	second := All()
	if len(first) != len(second) {
		t.Fatalf("catalog length drift: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("catalog drift at %d: %s vs %s", i, first[i], second[i])
		}
	}
	if !IsRegistered(TypeAuditCheckpoint) {
		t.Fatalf("audit checkpoint must be registered")
	}
	if IsRegistered(Type("dev.midnight.unknown.event")) {
		t.Fatalf("unknown event must not be registered")
	}
	if SinceVersion(TypeAuditCheckpoint) != "v1alpha" {
		t.Fatalf("audit checkpoint since-version drift")
	}
}

func TestRequiredFieldsAreSorted(t *testing.T) {
	got := RequiredFields()
	want := []string{
		"id", "midnightrequestid", "midnighttenantid", "source",
		"specversion", "time", "traceparent", "type",
	}
	if len(got) != len(want) {
		t.Fatalf("RequiredFields() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("RequiredFields()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
