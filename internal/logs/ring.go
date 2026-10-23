package logs

import "sync"

// RingBuffer stores recent log entries in memory without owning any output IO.
type RingBuffer struct {
	mu       sync.Mutex
	entries  []Entry
	next     int
	wrapped  bool
	capacity int
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 0 {
		capacity = 0
	}
	return &RingBuffer{capacity: capacity}
}

func (buffer *RingBuffer) Append(entry Entry) {
	if buffer == nil || buffer.capacity == 0 {
		return
	}

	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	if len(buffer.entries) < buffer.capacity {
		buffer.entries = append(buffer.entries, entry)
		buffer.next = len(buffer.entries) % buffer.capacity
		return
	}
	buffer.entries[buffer.next] = entry
	buffer.next = (buffer.next + 1) % buffer.capacity
	buffer.wrapped = true
}

func (buffer *RingBuffer) Query(filter Filter) ([]Entry, error) {
	result, err := buffer.QueryResult(filter)
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

func (buffer *RingBuffer) QueryResult(filter Filter) (QueryResult, error) {
	if err := filter.Validate(); err != nil {
		return QueryResult{}, err
	}
	if buffer == nil {
		return QueryResult{}, nil
	}

	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	entries := buffer.orderedLocked()
	filtered := entries[:0]
	for _, entry := range entries {
		if filter.Since.IsZero() || !entry.At.Before(filter.Since) {
			filtered = append(filtered, entry)
		}
	}
	truncated := false
	if filter.Tail > 0 && filter.Tail < len(filtered) {
		filtered = filtered[len(filtered)-filter.Tail:]
		truncated = true
	}
	result := QueryResult{
		Entries:   append([]Entry(nil), filtered...),
		Truncated: truncated,
	}
	if len(result.Entries) > 0 {
		last := result.Entries[len(result.Entries)-1]
		result.NextCursor = Cursor{Stream: last.Component, At: last.At}
	}
	return result, nil
}

func (buffer *RingBuffer) orderedLocked() []Entry {
	if !buffer.wrapped {
		return append([]Entry(nil), buffer.entries...)
	}
	entries := make([]Entry, 0, len(buffer.entries))
	entries = append(entries, buffer.entries[buffer.next:]...)
	entries = append(entries, buffer.entries[:buffer.next]...)
	return entries
}
