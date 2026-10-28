package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

// ErrEmptyChain is returned when a Verify is asked to walk a chain that
// has zero rows. This is not a failure on its own — the caller decides
// whether the empty chain is expected.
var ErrEmptyChain = errors.New("audit: chain is empty")

// ComputeRowHash returns row_hash := SHA-256(prev_hash || canonical_row)
// per ADR-011 §Decision item 2.
func ComputeRowHash(prev []byte, canonical []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(prev)
	_, _ = h.Write(canonical)
	sum := h.Sum(nil)
	out := make([]byte, len(sum))
	copy(out, sum[:])
	return out
}

// FillChain populates r.PrevHash and r.RowHash given the previous row's
// hash. Callers MUST already have set every other field on r.
func FillChain(r *Row, prev []byte) error {
	r.CanonicalFormVersion = CanonicalFormVersion
	canonical, err := CanonicalRow(*r)
	if err != nil {
		return err
	}
	r.PrevHash = append(r.PrevHash[:0], prev...)
	r.RowHash = ComputeRowHash(prev, canonical)
	return nil
}

// AuditChain is the persistence boundary Lane 1 will implement against
// Postgres. Lane 4 ships an in-memory implementation in this package.
//
// Panel decision pinning (line 121 / ADR-011): Append acquires an
// advisory lock on the chain head, computes row_hash from the previous
// row, and inserts atomically. Concurrent appends serialize. UPDATE and
// DELETE are not exposed — the contract is INSERT-only.
type AuditChain interface {
	// Append serializes r through the chain head, computes the panel-
	// locked row_hash + prev_hash, and persists. The returned Row has
	// Sequence + PrevHash + RowHash populated.
	Append(ctx context.Context, r Row) (Row, error)

	// Read returns rows in id-ascending order.
	Read(ctx context.Context, q Query) ([]Row, error)

	// LatestCheckpoint returns the most recent persisted checkpoint or
	// (Checkpoint{}, ErrNoCheckpoints) when none have been written.
	LatestCheckpoint(ctx context.Context) (Checkpoint, error)

	// InsertCheckpoint persists a Checkpoint produced by Sign.
	InsertCheckpoint(ctx context.Context, cp Checkpoint) (Checkpoint, error)

	// ListCheckpoints returns checkpoints in id-ascending order.
	ListCheckpoints(ctx context.Context) ([]Checkpoint, error)
}

// ErrNoCheckpoints signals that the chain has no signed checkpoints yet.
var ErrNoCheckpoints = errors.New("audit: no checkpoints recorded")

var ErrCheckpointConflict = errors.New("audit: checkpoint predecessor or coverage changed")

func validateCheckpointSuccessor(cp, previous Checkpoint) error {
	if cp.PrevCheckpointID != previous.ID || cp.FirstRowID != previous.LastRowID+1 || cp.LastRowID < cp.FirstRowID || len(cp.LastRowHash) != sha256.Size || cp.EndedAt.Before(cp.StartedAt) {
		return ErrCheckpointConflict
	}
	return nil
}

// Query bounds a chain read.
type Query struct {
	// SinceID returns rows with Sequence > SinceID.
	SinceID int64
	// Limit caps the number of rows returned. 0 == no limit.
	Limit int
}

// ProjectQuery bounds a latest-first, tenant-scoped audit read. BeforeID is
// exclusive; zero starts at the current chain head.
type ProjectQuery struct {
	ProjectID string
	BeforeID  int64
	Limit     int
}

// ProjectReader is the optional efficient read seam used by the product audit
// browser. AuditChain remains unchanged so external chain implementations do
// not gain a new mandatory method.
type ProjectReader interface {
	ReadProject(ctx context.Context, q ProjectQuery) ([]Row, error)
}

// Validate rejects queries that could widen a tenant-scoped read.
func (q ProjectQuery) Validate() error {
	if strings.TrimSpace(q.ProjectID) == "" || q.BeforeID < 0 || q.Limit <= 0 {
		return errors.New("audit: invalid project query")
	}
	return nil
}

// AppendWithDecorator is a small helper that runs the supplied decorator
// (typically a clock + request-id stamper) before delegating to the
// chain's Append. It exists so callers don't have to wrap every emit
// site themselves.
func AppendWithDecorator(ctx context.Context, chain AuditChain, decorator func(*Row), r Row) (Row, error) {
	if decorator != nil {
		decorator(&r)
	}
	if r.RecordedAt.IsZero() {
		return Row{}, fmt.Errorf("audit: RecordedAt is required")
	}
	return chain.Append(ctx, r)
}
