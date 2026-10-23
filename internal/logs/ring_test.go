package logs

import (
	"testing"
	"time"
)

func TestRingBufferQueryAppliesSinceAndTail(t *testing.T) {
	buffer := NewRingBuffer(5)
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		buffer.Append(Entry{At: base.Add(time.Duration(i) * time.Minute), Message: string(rune('a' + i))})
	}

	entries, err := buffer.Query(Filter{Since: base.Add(2 * time.Minute), Tail: 2})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(entries) != 2 || entries[0].Message != "d" || entries[1].Message != "e" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestRingBufferDropsOldestEntries(t *testing.T) {
	buffer := NewRingBuffer(3)
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		buffer.Append(Entry{At: base.Add(time.Duration(i) * time.Minute), Message: string(rune('a' + i))})
	}

	entries, err := buffer.Query(Filter{})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(entries) != 3 || entries[0].Message != "c" || entries[2].Message != "e" {
		t.Fatalf("unexpected ring entries: %#v", entries)
	}
}

func TestRingBufferQueryRejectsInvalidFilter(t *testing.T) {
	if _, err := NewRingBuffer(1).Query(Filter{Tail: -1}); err == nil {
		t.Fatal("expected invalid filter error")
	}
}

func TestRingBufferQueryResultIncludesCursorAndTruncatedMarker(t *testing.T) {
	buffer := NewRingBuffer(5)
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		buffer.Append(Entry{At: base.Add(time.Duration(i) * time.Minute), Component: "router", Message: string(rune('a' + i))})
	}

	result, err := buffer.QueryResult(Filter{Tail: 2})
	if err != nil {
		t.Fatalf("QueryResult returned error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected truncated result")
	}
	if len(result.Entries) != 2 || result.Entries[0].Message != "b" || result.Entries[1].Message != "c" {
		t.Fatalf("unexpected entries: %#v", result.Entries)
	}
	if result.NextCursor.Stream != "router" || result.NextCursor.At != base.Add(2*time.Minute) {
		t.Fatalf("unexpected next cursor: %#v", result.NextCursor)
	}
}
