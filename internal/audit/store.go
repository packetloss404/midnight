package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryChain is the in-memory AuditChain implementation Lane 4 ships
// for tests and CLI consumers. Lane 1 supplies a Postgres-backed
// implementation with the same interface; the panel-locked semantics
// (advisory lock on the chain head, INSERT-only, deterministic
// canonical row hashing) are identical.
//
// MemoryChain is safe for concurrent use.
type MemoryChain struct {
	mu          sync.Mutex
	rows        []Row
	checkpoints []Checkpoint
	clock       func() time.Time
}

// NewMemoryChain constructs an empty in-memory chain.
func NewMemoryChain() *MemoryChain {
	return &MemoryChain{clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the recorded_at clock for tests.
func (c *MemoryChain) SetClock(fn func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clock = fn
}

// Append implements AuditChain.
func (c *MemoryChain) Append(ctx context.Context, r Row) (Row, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r.Sequence = int64(len(c.rows) + 1)
	if r.RecordedAt.IsZero() {
		r.RecordedAt = c.clock()
	}
	if r.OccurredAt.IsZero() {
		r.OccurredAt = r.RecordedAt
	}
	prev := GenesisHash()
	if len(c.rows) > 0 {
		prev = append([]byte(nil), c.rows[len(c.rows)-1].RowHash...)
	}
	if err := FillChain(&r, prev); err != nil {
		return Row{}, err
	}
	c.rows = append(c.rows, r)
	return r, nil
}

// Read implements AuditChain.
func (c *MemoryChain) Read(ctx context.Context, q Query) ([]Row, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Row, 0, len(c.rows))
	for _, r := range c.rows {
		if r.Sequence <= q.SinceID {
			continue
		}
		out = append(out, copyRow(r))
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

// ReadProject returns project rows newest first without exposing rows from any
// other project. BeforeID is an exclusive stable cursor over the append-only
// chain.
func (c *MemoryChain) ReadProject(ctx context.Context, q ProjectQuery) ([]Row, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Row, 0)
	for i := len(c.rows) - 1; i >= 0; i-- {
		r := c.rows[i]
		if r.ProjectID != q.ProjectID || (q.BeforeID > 0 && r.Sequence >= q.BeforeID) {
			continue
		}
		out = append(out, copyRow(r))
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// LatestCheckpoint implements AuditChain.
func (c *MemoryChain) LatestCheckpoint(ctx context.Context) (Checkpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.checkpoints) == 0 {
		return Checkpoint{}, ErrNoCheckpoints
	}
	cp := c.checkpoints[len(c.checkpoints)-1]
	return copyCheckpoint(cp), nil
}

// ListCheckpoints implements AuditChain.
func (c *MemoryChain) ListCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Checkpoint, len(c.checkpoints))
	for i, cp := range c.checkpoints {
		out[i] = copyCheckpoint(cp)
	}
	return out, nil
}

// InsertCheckpoint implements AuditChain.
func (c *MemoryChain) InsertCheckpoint(ctx context.Context, cp Checkpoint) (Checkpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp.ID = int64(len(c.checkpoints) + 1)
	c.checkpoints = append(c.checkpoints, copyCheckpoint(cp))
	return cp, nil
}

// Tamper is a TEST-ONLY hook that overwrites a single row in place. It
// exists so tamper-detection tests can simulate a direct-SQL UPDATE
// without needing a real Postgres. Production code paths MUST NEVER
// expose any analogous mutation.
//
// Returns ErrEmptyChain when the chain has no rows.
func (c *MemoryChain) Tamper(seq int64, mutate func(*Row)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) == 0 {
		return ErrEmptyChain
	}
	for i := range c.rows {
		if c.rows[i].Sequence == seq {
			mutate(&c.rows[i])
			return nil
		}
	}
	return fmt.Errorf("audit: tamper: row %d not found", seq)
}

// DeleteSequence is a TEST-ONLY hook that drops the row with the
// supplied sequence id. Used by tamper-detection tests to simulate a
// direct-SQL DELETE.
func (c *MemoryChain) DeleteSequence(seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, r := range c.rows {
		if r.Sequence == seq {
			c.rows = append(c.rows[:i], c.rows[i+1:]...)
			return nil
		}
	}
	return errors.New("audit: delete sequence not found")
}

func copyRow(r Row) Row {
	out := r
	out.PrevHash = append([]byte(nil), r.PrevHash...)
	out.RowHash = append([]byte(nil), r.RowHash...)
	if r.Before != nil {
		out.Before = make(map[string]any, len(r.Before))
		for k, v := range r.Before {
			out.Before[k] = v
		}
	}
	if r.After != nil {
		out.After = make(map[string]any, len(r.After))
		for k, v := range r.After {
			out.After[k] = v
		}
	}
	if r.Attrs != nil {
		out.Attrs = make(map[string]any, len(r.Attrs))
		for k, v := range r.Attrs {
			out.Attrs[k] = v
		}
	}
	return out
}

func copyCheckpoint(cp Checkpoint) Checkpoint {
	out := cp
	out.LastRowHash = append([]byte(nil), cp.LastRowHash...)
	out.Signature = append([]byte(nil), cp.Signature...)
	return out
}
