// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import "context"

type Snapshot struct {
	Rows        []Row
	Checkpoints []Checkpoint
}

type Snapshotter interface {
	Snapshot(context.Context) (Snapshot, error)
}

func ReadSnapshot(ctx context.Context, chain AuditChain) (Snapshot, error) {
	if chain == nil {
		return Snapshot{}, nil
	}
	if source, ok := chain.(Snapshotter); ok {
		return source.Snapshot(ctx)
	}
	rows, err := chain.Read(ctx, Query{})
	if err != nil {
		return Snapshot{}, err
	}
	checkpoints, err := chain.ListCheckpoints(ctx)
	return Snapshot{Rows: rows, Checkpoints: checkpoints}, err
}

func (c *MemoryChain) Snapshot(ctx context.Context) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Rows: cloneRows(c.rows), Checkpoints: cloneCheckpoints(c.checkpoints)}, nil
}

func (s Snapshot) Source() Source { return snapshotSource{s} }

type snapshotSource struct{ snapshot Snapshot }

func (s snapshotSource) Rows(context.Context) ([]Row, error) { return cloneRows(s.snapshot.Rows), nil }
func (s snapshotSource) Checkpoints(context.Context) ([]Checkpoint, error) {
	return cloneCheckpoints(s.snapshot.Checkpoints), nil
}
