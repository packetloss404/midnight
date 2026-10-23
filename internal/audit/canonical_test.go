package audit

import (
	"bytes"
	"testing"
	"time"
)

func TestCanonicalRowDeterministic(t *testing.T) {
	r := Row{
		Sequence:       42,
		RequestID:      "req_abc",
		OccurredAt:     time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		ActorType:      "user",
		ActorID:        "usr_alice",
		ResourceKind:   "variable",
		ResourceID:     "var_dbpw",
		Action:         "rotate",
		Before:         map[string]any{"length_bucket": "17-64"},
		After:          map[string]any{"length_bucket": "17-64"},
		Attrs:          map[string]any{"reason": "scheduled-rotation"},
		PolicyDecision: "allow",
	}
	a, err := CanonicalRow(r)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	b, err := CanonicalRow(r)
	if err != nil {
		t.Fatalf("encode2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical row not deterministic: %s vs %s", a, b)
	}
}

func TestCanonicalRowOrderIndependent(t *testing.T) {
	r1 := Row{
		Sequence:     1,
		ActorType:    "system",
		ResourceKind: "deployment",
		Action:       "create",
		Before:       map[string]any{"a": 1, "b": 2, "c": 3},
		After:        map[string]any{"c": 3, "a": 1, "b": 2},
	}
	r2 := r1
	r2.Before = map[string]any{"c": 3, "b": 2, "a": 1}
	r2.After = map[string]any{"a": 1, "b": 2, "c": 3}
	a, _ := CanonicalRow(r1)
	b, _ := CanonicalRow(r2)
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical row depends on map iteration order: \n%s\n%s", a, b)
	}
}

func TestCanonicalRowExcludesChainFields(t *testing.T) {
	base := Row{
		ActorType:    "system",
		ResourceKind: "deployment",
		Action:       "noop",
	}
	a, _ := CanonicalRow(base)
	mut := base
	mut.PrevHash = []byte{1, 2, 3}
	mut.RowHash = []byte{4, 5, 6}
	mut.CheckpointID = 99
	b, _ := CanonicalRow(mut)
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical row leaked prev/row/checkpoint fields:\n%s\n%s", a, b)
	}
}

func TestRowHashRecompute(t *testing.T) {
	rows := make([]Row, 1024)
	for i := range rows {
		rows[i] = Row{
			Sequence:     int64(i + 1),
			ActorType:    "system",
			ResourceKind: "deployment",
			Action:       "transition",
			RequestID:    "req",
			Attrs:        map[string]any{"i": int64(i)},
		}
	}
	prev := GenesisHash()
	hashes := make([][]byte, len(rows))
	for i := range rows {
		canonical, err := CanonicalRow(rows[i])
		if err != nil {
			t.Fatalf("canonical[%d]: %v", i, err)
		}
		h := ComputeRowHash(prev, canonical)
		hashes[i] = h
		prev = h
	}
	// Recompute and ensure determinism.
	prev = GenesisHash()
	for i, r := range rows {
		canonical, err := CanonicalRow(r)
		if err != nil {
			t.Fatalf("canonical[%d]: %v", i, err)
		}
		h := ComputeRowHash(prev, canonical)
		if !bytes.Equal(h, hashes[i]) {
			t.Fatalf("row_hash diverged at %d", i)
		}
		prev = h
	}
}
