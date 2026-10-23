package audit

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func newTestRow(seq int) Row {
	return Row{
		ActorType:    "system",
		ResourceKind: "deployment",
		Action:       "create",
		RequestID:    "req-" + time.Now().Format("150405.000000000"),
		Attrs:        map[string]any{"seq": int64(seq)},
	}
}

func TestMemoryChainAppendChainsCorrectly(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	prev := GenesisHash()
	for i := 0; i < 5; i++ {
		r, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if !bytes.Equal(r.PrevHash, prev) {
			t.Fatalf("prev_hash mismatch at %d", i)
		}
		prev = r.RowHash
	}
}

func TestVerifyOK(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 16; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	report, err := Verify(ctx, SourceFromChain{Chain: c}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeOK {
		t.Fatalf("outcome = %s want ok; breaks=%+v", report.Outcome, report.Breaks)
	}
	if report.RowsChecked != 16 {
		t.Fatalf("rows checked = %d", report.RowsChecked)
	}
}

func TestVerifyDetectsRowEdit(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 5; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := c.Tamper(3, func(r *Row) { r.Action = "edited-by-attacker" }); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	report, err := Verify(ctx, SourceFromChain{Chain: c}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeChainBreak {
		t.Fatalf("outcome = %s want chain_break", report.Outcome)
	}
	foundOffending := false
	for _, b := range report.Breaks {
		if b.Sequence == 3 && b.Kind == "row_hash" {
			foundOffending = true
		}
	}
	if !foundOffending {
		t.Fatalf("expected row_hash break at row 3; got %+v", report.Breaks)
	}
}

func TestVerifyDetectsRowDelete(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 5; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := c.DeleteSequence(3); err != nil {
		t.Fatalf("delete: %v", err)
	}
	report, err := Verify(ctx, SourceFromChain{Chain: c}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeChainBreak {
		t.Fatalf("outcome = %s want chain_break", report.Outcome)
	}
	// The first surviving row after the deleted one (Sequence == 4)
	// should fail with prev_hash mismatch.
	found := false
	for _, b := range report.Breaks {
		if b.Sequence == 4 && b.Kind == "prev_hash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected prev_hash break at row 4; got %+v", report.Breaks)
	}
}

func TestEd25519CheckpointRoundtrip(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 4; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	cp, err := NewCheckpointer(c, key).Sign(ctx)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if cp.Signature == nil {
		t.Fatalf("checkpoint missing signature")
	}
	if cp.SigningKeyFingerprint != key.Fingerprint {
		t.Fatalf("fingerprint mismatch")
	}
	report, err := Verify(ctx, SourceFromChain{Chain: c}, []VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeOK {
		t.Fatalf("verify with key = %s; breaks=%+v", report.Outcome, report.Breaks)
	}
}

func TestVerifyDetectsCheckpointSignatureBreak(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 3; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	key, _ := GenerateSigningKey()
	cp, err := NewCheckpointer(c, key).Sign(ctx)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Mutate the persisted checkpoint signature in place.
	c.mu.Lock()
	for i := range c.checkpoints {
		if c.checkpoints[i].ID == cp.ID {
			if len(c.checkpoints[i].Signature) > 0 {
				c.checkpoints[i].Signature[0] ^= 0xff
			}
		}
	}
	c.mu.Unlock()
	report, err := Verify(ctx, SourceFromChain{Chain: c}, []VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeSignatureBreak {
		t.Fatalf("outcome = %s want signature_break", report.Outcome)
	}
}

func TestVerifyMissingKey(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	_, err := c.Append(ctx, newTestRow(0))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	key, _ := GenerateSigningKey()
	if _, err := NewCheckpointer(c, key).Sign(ctx); err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Verify without supplying any key.
	report, err := Verify(ctx, SourceFromChain{Chain: c}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != OutcomeSignatureBreak {
		t.Fatalf("outcome = %s want signature_break", report.Outcome)
	}
	hit := false
	for _, b := range report.Breaks {
		if b.Kind == "missing_key" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("expected missing_key break, got %+v", report.Breaks)
	}
}

func TestVerifyMarksReportInvalidWhenRowsReadFails(t *testing.T) {
	report, err := Verify(context.Background(), failingVerifySource{rowsErr: errors.New("rows unavailable")}, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want source read error")
	}
	if report.Valid || report.Status != "failed" || report.Outcome != OutcomeSourceError {
		t.Fatalf("report = %+v, want invalid source_error report", report)
	}
}

func TestVerifyMarksReportInvalidWhenCheckpointsReadFails(t *testing.T) {
	report, err := Verify(context.Background(), failingVerifySource{checkpointErr: errors.New("checkpoints unavailable")}, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want checkpoint read error")
	}
	if report.Valid || report.Status != "failed" || report.Outcome != OutcomeSourceError {
		t.Fatalf("report = %+v, want invalid source_error report", report)
	}
}

func TestExportAndJSONLRoundtrip(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryChain()
	for i := 0; i < 6; i++ {
		_, err := c.Append(ctx, newTestRow(i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	key, _ := GenerateSigningKey()
	if _, err := NewCheckpointer(c, key).Sign(ctx); err != nil {
		t.Fatalf("sign: %v", err)
	}
	dst := NewMemoryDestination("test")
	manifest, err := NewExporter(c).Export(ctx, dst)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != 6 || manifest.CheckpointCount != 1 {
		t.Fatalf("manifest counts = %+v", manifest)
	}
	src, err := NewJSONLSourceFromReader(bytes.NewReader(dst.Bytes()))
	if err != nil {
		t.Fatalf("parse jsonl: %v", err)
	}
	report, err := Verify(ctx, src, []VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil {
		t.Fatalf("verify jsonl: %v", err)
	}
	if report.Outcome != OutcomeOK {
		t.Fatalf("jsonl roundtrip outcome = %s breaks=%+v", report.Outcome, report.Breaks)
	}
	if src.Manifest().StreamSHA256 == "" {
		t.Fatalf("manifest sha256 missing")
	}
}

func TestParseDestination(t *testing.T) {
	cases := map[string]bool{
		"file:///tmp/audit.jsonl":  true,
		"/abs/path/audit.jsonl":    true,
		"memory://demo":            true,
		"s3://bucket/audit.jsonl":  false,
		"gcs://bucket/audit.jsonl": false,
		"sftp://host/audit.jsonl":  false,
		"weird://path":             false,
	}
	for u, supported := range cases {
		dst, err := ParseDestination(u)
		if supported {
			if err != nil {
				t.Fatalf("%s should parse: %v", u, err)
			}
			if dst.URL() == "" {
				t.Fatalf("%s url empty", u)
			}
		} else {
			if err == nil {
				t.Fatalf("%s should not be supported", u)
			}
			if u != "weird://path" && !errors.Is(err, ErrDestinationNotSupported) {
				t.Fatalf("%s expected ErrDestinationNotSupported, got %v", u, err)
			}
		}
	}
}

func TestValidateDrainPlan(t *testing.T) {
	ok := DrainPlan{ID: "p1", Destination: "file:///tmp/audit.jsonl", PartitionLabel: "2026-04"}
	if err := ValidateDrainPlan(ok); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	bad := []DrainPlan{
		{},
		{ID: "p"},
		{ID: "p", Destination: "weird://x"},
		{ID: "p", Destination: "file:///x", PartitionLabel: "bad"},
		{ID: "p", Destination: "file:///x", SigningKeyFingerprint: "tooShort"},
	}
	for i, plan := range bad {
		if err := ValidateDrainPlan(plan); !errors.Is(err, ErrDrainPlanInvalid) {
			t.Fatalf("plan[%d] expected invalid, got %v", i, err)
		}
	}
}

type failingVerifySource struct {
	rowsErr       error
	checkpointErr error
}

func (s failingVerifySource) Rows(context.Context) ([]Row, error) {
	if s.rowsErr != nil {
		return nil, s.rowsErr
	}
	return nil, nil
}

func (s failingVerifySource) Checkpoints(context.Context) ([]Checkpoint, error) {
	if s.checkpointErr != nil {
		return nil, s.checkpointErr
	}
	return nil, nil
}
