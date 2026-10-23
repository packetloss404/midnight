package logs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEntryMarshalsAsSingleJSONRecord(t *testing.T) {
	entry := Entry{
		At:        time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Level:     "info",
		Component: "router",
		Message:   "route rendered",
		Fields:    map[string]any{"route": "app"},
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(data), "\n") {
		t.Fatalf("entry contains newline: %q", data)
	}
	if !strings.Contains(string(data), `"component":"router"`) {
		t.Fatalf("entry missing component: %s", data)
	}
}

func TestRedactCopiesFields(t *testing.T) {
	entry := Entry{Fields: map[string]any{"route": "app"}}
	redacted := Redact(entry)
	redacted.Fields["route"] = "changed"

	if entry.Fields["route"] != "app" {
		t.Fatal("Redact did not copy fields map")
	}
}

func TestRedactMasksSensitiveFieldValues(t *testing.T) {
	entry := Entry{Fields: map[string]any{
		"apiToken":      "token-value",
		"client_secret": "secret-value",
		"PASSWORD":      "password-value",
		"route":         "app",
	}}

	redacted := Redact(entry)
	for _, key := range []string{"apiToken", "client_secret", "PASSWORD"} {
		if redacted.Fields[key] != redactedValue {
			t.Fatalf("field %s = %#v, want redacted value", key, redacted.Fields[key])
		}
	}
	if redacted.Fields["route"] != "app" {
		t.Fatalf("non-sensitive field redacted: %#v", redacted.Fields["route"])
	}
}

func TestEncodeQueryResultNDJSONWritesRedactedEntries(t *testing.T) {
	result := QueryResult{Entries: []Entry{
		{At: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), Level: "info", Component: "router", Message: "one", Fields: map[string]any{"token": "secret", "route": "app"}},
		{At: time.Date(2026, 4, 25, 12, 1, 0, 0, time.UTC), Level: "warn", Component: "acme", Message: "two"},
	}}

	var out bytes.Buffer
	if err := EncodeQueryResultNDJSON(&out, result); err != nil {
		t.Fatalf("EncodeQueryResultNDJSON returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("encoded lines = %d, want 2: %q", len(lines), out.String())
	}
	if strings.Contains(out.String(), "secret") || !strings.Contains(out.String(), redactedValue) {
		t.Fatalf("encoded output was not redacted: %s", out.String())
	}
	for _, line := range lines {
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line is not JSON: %q: %v", line, err)
		}
	}
}
