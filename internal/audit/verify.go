package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
)

// VerifyOutcome is the high-level result of a Verify run.
type VerifyOutcome string

const (
	// OutcomeOK means every row hash matches and every checkpoint
	// signature verified.
	OutcomeOK VerifyOutcome = "ok"
	// OutcomeWarn means the chain verified but the verifier observed
	// recoverable conditions (e.g. partition gaps with final
	// checkpoints, non-contiguous prev_checkpoint_id).
	OutcomeWarn VerifyOutcome = "warn"
	// OutcomeChainBreak is returned when any row's hash chain does not
	// match. Maps to CLI exit code 2.
	OutcomeChainBreak VerifyOutcome = "chain_break"
	// OutcomeSignatureBreak is returned when any checkpoint signature
	// is missing/bad/wrong key. Maps to CLI exit code 3.
	OutcomeSignatureBreak VerifyOutcome = "signature_break"
	// OutcomeSourceError is returned with a non-nil error when rows or
	// checkpoints cannot be read. Structured reports must not claim validity
	// when the verifier never inspected the complete source.
	OutcomeSourceError VerifyOutcome = "source_error"
)

// Break describes a single integrity violation.
type Break struct {
	Kind         string `json:"kind"` // "row_hash" | "prev_hash" | "checkpoint_sig" | "checkpoint_row" | "missing_key"
	Sequence     int64  `json:"sequence,omitempty"`
	CheckpointID int64  `json:"checkpoint_id,omitempty"`
	Detail       string `json:"detail"`
}

// VerifyReport is the structured output of Verify.
type VerifyReport struct {
	Outcome            VerifyOutcome `json:"outcome"`
	Status             string        `json:"status"`
	Valid              bool          `json:"valid"`
	RowsChecked        int           `json:"rows_checked"`
	CheckpointsChecked int           `json:"checkpoints_checked"`
	Breaks             []Break       `json:"breaks,omitempty"`
	Warnings           []Break       `json:"warnings,omitempty"`
	GenesisHashHex     string        `json:"genesis_hash"`
}

// Source is anything the verifier can read rows + checkpoints from. The
// AuditChain implementations satisfy this directly; the JSONL reader
// also satisfies it.
type Source interface {
	Rows(ctx context.Context) ([]Row, error)
	Checkpoints(ctx context.Context) ([]Checkpoint, error)
}

// Verify walks every row in the source, recomputes the hash chain, and
// verifies every checkpoint signature against the supplied verifying
// keys (multiple keys are accepted to support rotation overlap).
func Verify(ctx context.Context, src Source, keys []VerifyingKey) (report VerifyReport, err error) {
	report = VerifyReport{
		Outcome:        OutcomeOK,
		Status:         "verified",
		Valid:          true,
		GenesisHashHex: hexBytes(GenesisHash()),
	}
	defer func() {
		reportStatus(&report)
	}()
	rows, err := src.Rows(ctx)
	if err != nil {
		report.Outcome = OutcomeSourceError
		report.Breaks = append(report.Breaks, Break{Kind: "source_read", Detail: err.Error()})
		return report, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Sequence < rows[j].Sequence })

	expectedPrev := GenesisHash()
	for i := range rows {
		r := rows[i]
		report.RowsChecked++
		canonical, err := CanonicalRow(r)
		if err != nil {
			report.Outcome = OutcomeChainBreak
			report.Breaks = append(report.Breaks, Break{Kind: "row_hash", Sequence: r.Sequence, Detail: fmt.Sprintf("canonicalize: %v", err)})
			continue
		}
		actual := ComputeRowHash(r.PrevHash, canonical)
		if !bytes.Equal(actual, r.RowHash) {
			report.Outcome = OutcomeChainBreak
			report.Breaks = append(report.Breaks, Break{Kind: "row_hash", Sequence: r.Sequence, Detail: "row_hash recomputation mismatch — row was edited"})
		}
		if !bytes.Equal(r.PrevHash, expectedPrev) {
			report.Outcome = OutcomeChainBreak
			report.Breaks = append(report.Breaks, Break{Kind: "prev_hash", Sequence: r.Sequence, Detail: "prev_hash does not match previous row's row_hash — row was deleted, reordered, or inserted"})
		}
		// The "expected previous hash" for the next iteration is this
		// row's row_hash AS RECORDED. If the recorded value was
		// tampered, the next row will match (or not) against the
		// tampered value. We use the recorded value so a single
		// tamper produces exactly one prev_hash break at the
		// successor row, matching panel-locked semantics.
		expectedPrev = append(expectedPrev[:0], r.RowHash...)
	}

	checkpoints, err := src.Checkpoints(ctx)
	if err != nil {
		report.Outcome = OutcomeSourceError
		report.Breaks = append(report.Breaks, Break{Kind: "source_read", Detail: err.Error()})
		return report, err
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].ID < checkpoints[j].ID })

	rowsByID := make(map[int64]Row, len(rows))
	for _, r := range rows {
		rowsByID[r.Sequence] = r
	}

	var prevCheckpointID int64
	for i := range checkpoints {
		cp := checkpoints[i]
		report.CheckpointsChecked++

		key, ok := matchingKey(cp.SigningKeyFingerprint, keys)
		if !ok {
			report.Outcome = elevateOutcome(report.Outcome, OutcomeSignatureBreak)
			report.Breaks = append(report.Breaks, Break{Kind: "missing_key", CheckpointID: cp.ID, Detail: "verifying key not in keyring (fingerprint=" + cp.SigningKeyFingerprint + ")"})
			continue
		}
		if err := VerifyCheckpoint(cp, key); err != nil {
			report.Outcome = elevateOutcome(report.Outcome, OutcomeSignatureBreak)
			report.Breaks = append(report.Breaks, Break{Kind: "checkpoint_sig", CheckpointID: cp.ID, Detail: err.Error()})
			continue
		}
		// Confirm the checkpoint's last_row_hash matches the row at
		// last_row_id.
		row, ok := rowsByID[cp.LastRowID]
		if !ok {
			report.Outcome = elevateOutcome(report.Outcome, OutcomeChainBreak)
			report.Breaks = append(report.Breaks, Break{Kind: "checkpoint_row", CheckpointID: cp.ID, Sequence: cp.LastRowID, Detail: "last_row_id missing from chain"})
			continue
		}
		if !bytes.Equal(row.RowHash, cp.LastRowHash) {
			report.Outcome = elevateOutcome(report.Outcome, OutcomeChainBreak)
			report.Breaks = append(report.Breaks, Break{Kind: "checkpoint_row", CheckpointID: cp.ID, Sequence: cp.LastRowID, Detail: "checkpoint last_row_hash does not match row_hash at last_row_id"})
			continue
		}
		// Non-contiguous prev_checkpoint_id is a warning (acceptable on
		// partition pruning per FEATURE_AUDIT_HASH_CHAIN.md §"Verify
		// Algorithm" step 5).
		if prevCheckpointID != 0 && cp.PrevCheckpointID != prevCheckpointID {
			report.Warnings = append(report.Warnings, Break{Kind: "checkpoint_link", CheckpointID: cp.ID, Detail: fmt.Sprintf("prev_checkpoint_id=%d expected=%d (acceptable on pruned partition)", cp.PrevCheckpointID, prevCheckpointID)})
		}
		prevCheckpointID = cp.ID
	}

	if report.Outcome == OutcomeOK && len(report.Warnings) > 0 {
		report.Outcome = OutcomeWarn
	}
	return report, nil
}

func reportStatus(report *VerifyReport) {
	switch report.Outcome {
	case OutcomeOK, OutcomeWarn:
		report.Status = "verified"
		report.Valid = true
	default:
		report.Status = "failed"
		report.Valid = false
	}
}

func matchingKey(fp string, keys []VerifyingKey) (VerifyingKey, bool) {
	for _, k := range keys {
		if k.Fingerprint == fp {
			return k, true
		}
	}
	return VerifyingKey{}, false
}

// elevateOutcome returns the higher-severity of two outcomes. Severity
// order (lowest to highest): OK < Warn < SignatureBreak < ChainBreak < SourceError.
// Note: chain_break (exit 2) is treated as more severe than
// signature_break (exit 3) because it indicates a successful tamper of
// the data store; signature_break indicates the verifier could not
// authenticate a checkpoint, which by itself does not prove tamper.
func elevateOutcome(current, candidate VerifyOutcome) VerifyOutcome {
	rank := map[VerifyOutcome]int{
		OutcomeOK:             0,
		OutcomeWarn:           1,
		OutcomeSignatureBreak: 2,
		OutcomeChainBreak:     3,
		OutcomeSourceError:    4,
	}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

// SourceFromChain adapts an AuditChain to a Source.
type SourceFromChain struct{ Chain AuditChain }

// Rows implements Source.
func (s SourceFromChain) Rows(ctx context.Context) ([]Row, error) {
	return s.Chain.Read(ctx, Query{})
}

// Checkpoints implements Source.
func (s SourceFromChain) Checkpoints(ctx context.Context) ([]Checkpoint, error) {
	return s.Chain.ListCheckpoints(ctx)
}

// hexBytes mirrors encoding/hex.EncodeToString without the import burden.
func hexBytes(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = hex[c>>4]
		out[2*i+1] = hex[c&0x0f]
	}
	return string(out)
}

// Sanity check the ed25519 imports are kept. Some platforms otherwise
// elide the import via dead-code reduction for purely-test files; we
// reference the package here to make the linkage explicit.
var _ = ed25519.PublicKeySize

// ErrInvalidSource is returned when a Source returns malformed input.
var ErrInvalidSource = errors.New("audit: source returned invalid input")
