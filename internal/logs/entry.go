package logs

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/redaction"
)

const redactedValue = "[REDACTED]"

// Entry is one structured record intended for newline-delimited JSON output.
type Entry struct {
	At        time.Time      `json:"at"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// Redact returns a copy of entry after applying the legacy field-name redactor.
// It preserves schema (no fields are dropped) and replaces sensitive-field
// values with a constant placeholder.
func Redact(entry Entry) Entry {
	if entry.Fields == nil {
		return entry
	}

	redacted := entry
	redacted.Fields = make(map[string]any, len(entry.Fields))
	for key, value := range entry.Fields {
		if isSensitiveField(key) {
			redacted.Fields[key] = redactedValue
			continue
		}
		redacted.Fields[key] = value
	}
	return redacted
}

// RedactWith applies redactor to the entry's Message and Fields, preserving
// schema. When redactor is nil the legacy Redact behavior is used.
func RedactWith(redactor *redaction.Redactor, entry Entry) Entry {
	if redactor == nil {
		return Redact(entry)
	}
	out := entry
	out.Message = redactor.RedactString(entry.Message)
	if entry.Fields != nil {
		out.Fields = redactor.RedactMap(entry.Fields)
	}
	return out
}

// EncodeQueryResultNDJSON writes the entries as newline-delimited JSON,
// applying the legacy field-name redactor.
func EncodeQueryResultNDJSON(writer io.Writer, result QueryResult) error {
	return EncodeQueryResultNDJSONWith(writer, result, nil)
}

// EncodeQueryResultNDJSONWith writes the entries as NDJSON, applying redactor
// to Message and Fields. When redactor is nil the legacy redactor is used.
func EncodeQueryResultNDJSONWith(writer io.Writer, result QueryResult, redactor *redaction.Redactor) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, entry := range result.Entries {
		if err := encoder.Encode(RedactWith(redactor, entry)); err != nil {
			return err
		}
	}
	return nil
}

func isSensitiveField(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password")
}
