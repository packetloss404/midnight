package audit

import (
	"context"
	"errors"
	"time"
)

// Checkpointer signs Ed25519 checkpoint metadata over the chain head.
// CheckpointSigningInput defines the compatible authenticated fields; the
// checkpoint's own database ID is not part of that signed message.
type Checkpointer struct {
	chain AuditChain
	key   SigningKey
	clock func() time.Time
}

// AtomicCheckpointChain chooses the predecessor and head, signs, and inserts
// under the same chain lock used by Append. daily suppresses another checkpoint
// in the same UTC day; Sign uses daily=false for explicit checkpoints.
type AtomicCheckpointChain interface {
	SignAuditCheckpoint(context.Context, SigningKey, func() time.Time, bool) (Checkpoint, bool, error)
}

// NewCheckpointer constructs a Checkpointer.
func NewCheckpointer(chain AuditChain, key SigningKey) *Checkpointer {
	return &Checkpointer{chain: chain, key: key, clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the wall clock used to stamp StartedAt/EndedAt.
func (c *Checkpointer) SetClock(fn func() time.Time) {
	if fn != nil {
		c.clock = fn
	}
}

// Sign produces and persists a checkpoint covering every row appended
// since the previous checkpoint. The returned Checkpoint has its
// signature + fingerprint populated. Returns nil + ErrEmptyChain when
// no rows have been appended at all.
func (c *Checkpointer) Sign(ctx context.Context) (Checkpoint, error) {
	if chain, ok := c.chain.(AtomicCheckpointChain); ok {
		cp, _, err := chain.SignAuditCheckpoint(ctx, c.key, c.clock, false)
		return cp, err
	}
	rows, err := c.chain.Read(ctx, Query{})
	if err != nil {
		return Checkpoint{}, err
	}
	if len(rows) == 0 {
		return Checkpoint{}, ErrEmptyChain
	}
	previous, err := c.chain.LatestCheckpoint(ctx)
	if err != nil && !errors.Is(err, ErrNoCheckpoints) {
		return Checkpoint{}, err
	}
	first := int64(1)
	if previous.LastRowID > 0 {
		first = previous.LastRowID + 1
	}
	if first > rows[len(rows)-1].Sequence {
		return Checkpoint{}, ErrEmptyChain
	}
	now := checkpointTime(c.clock())
	cp := Checkpoint{
		PartitionLabel:   now.Format("2006-01"),
		StartedAt:        now,
		EndedAt:          now,
		FirstRowID:       first,
		LastRowID:        rows[len(rows)-1].Sequence,
		LastRowHash:      append([]byte(nil), rows[len(rows)-1].RowHash...),
		PrevCheckpointID: previous.ID,
	}
	signed, err := SignCheckpoint(cp, c.key)
	if err != nil {
		return Checkpoint{}, err
	}
	return c.chain.InsertCheckpoint(ctx, signed)
}

// SignDue performs at most one nonempty checkpoint per UTC day. An initial
// checkpoint covers existing history immediately; restart catch-up signs at
// the actual current time, never pretending a missed signature happened earlier.
func (c *Checkpointer) SignDue(ctx context.Context) (Checkpoint, bool, error) {
	chain, ok := c.chain.(AtomicCheckpointChain)
	if !ok {
		return Checkpoint{}, false, errors.New("audit: scheduled checkpoints require atomic chain signing")
	}
	return chain.SignAuditCheckpoint(ctx, c.key, c.clock, true)
}

func checkpointTime(now time.Time) time.Time { return now.UTC().Truncate(time.Microsecond) }

func checkpointDue(previous Checkpoint, now time.Time, daily bool) bool {
	if !daily || previous.ID == 0 {
		return true
	}
	y, m, d := now.UTC().Date()
	return previous.EndedAt.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}
