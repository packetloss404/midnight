package logs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFilterMarshalsSinceTailFollowShape(t *testing.T) {
	filter := Filter{
		Since:  time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		Tail:   100,
		Follow: true,
	}

	data, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"since":"2026-04-25T12:00:00Z"`, `"tail":100`, `"follow":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("filter JSON missing %s: %s", want, text)
		}
	}
}

func TestFilterRejectsNegativeTail(t *testing.T) {
	if err := (Filter{Tail: -1}).Validate(); err == nil {
		t.Fatal("expected negative tail error")
	}
}

func TestCursorMarshalsPositionShape(t *testing.T) {
	cursor := Cursor{Stream: "router", Token: "opaque"}
	data, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"stream":"router"`, `"token":"opaque"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("cursor JSON missing %s: %s", want, text)
		}
	}
}

func TestQueryResultMarshalsCursorAndTruncatedShape(t *testing.T) {
	result := QueryResult{
		Entries:    []Entry{{Message: "ready"}},
		NextCursor: Cursor{Stream: "router", Token: "next"},
		Truncated:  true,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"entries"`, `"next_cursor"`, `"truncated":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("query result JSON missing %s: %s", want, text)
		}
	}
}
