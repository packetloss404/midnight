package audit

import (
	"context"
	"errors"
	"time"
)

// Checkpointer signs a daily Ed25519 checkpoint over the chain head.
//
// Panel decision pinning (line 121 / FEATURE_AUDIT_HASH_CHAIN.md
// §"Daily checkpoint"):
//
//	"An audit_checkpoint worker creates an audit_checkpoint row at UTC
//	 midnight that signs (checkpoint_id, last_row_hash, last_row_id,
//	 started_at, ended_at) with Ed25519."
//
// Lane 4 ships the synchronous primitive; Lane 1 wraps it as a worker.
type Checkpointer struct {
	chain AuditChain
	key   SigningKey
	clock func() time.Time
}

// NewCheckpointer constructs a Checkpointer.
func NewCheckpointer(chain AuditChain, key SigningKey) *Checkpointer {
	return &Checkpointer{chain: chain, key: key, clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the wall clock used to stamp StartedAt/EndedAt.
func (c *Checkpointer) SetClock(fn func() time.Time) { c.clock = fn }

// Sign produces and persists a checkpoint covering every row appended
// since the previous checkpoint. The returned Checkpoint has its
// signature + fingerprint populated. Returns nil + ErrEmptyChain when
// no rows have been appended at all.
func (c *Checkpointer) Sign(ctx context.Context) (Checkpoint, error) {
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
	cp := Checkpoint{
		PartitionLabel:   c.clock().Format("2006-01"),
		StartedAt:        c.clock(),
		EndedAt:          c.clock(),
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
