// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignedExportRequiresIndependentTrustAndValidManifest(t *testing.T) {
	chain := NewMemoryChain()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	key, _ := GenerateSigningKey()
	if _, err := NewCheckpointer(chain, key).Sign(context.Background()); err != nil {
		t.Fatal(err)
	}
	dst := NewMemoryDestination("signed-export")
	if _, err := NewExporter(chain).Export(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	src, err := NewJSONLSourceFromReader(bytes.NewReader(dst.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]VerifyingKey{nil, {{Public: key.Public, Fingerprint: key.Fingerprint}}} {
		report, err := Verify(context.Background(), src, keys)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			if report.Valid || report.Outcome != OutcomeSignatureBreak {
				t.Fatalf("export self-trusted=%#v", report)
			}
		} else if !report.Valid || report.CheckpointsChecked != 1 {
			t.Fatalf("trusted export=%#v", report)
		}
	}
	if _, err := NewJSONLSourceFromReader(strings.NewReader(strings.Replace(string(dst.Bytes()), `"action":"create"`, `"action":"changed"`, 1))); err == nil {
		t.Fatal("modified export bypassed stream manifest")
	}
	lines := bytes.Split(bytes.TrimSpace(dst.Bytes()), []byte("\n"))
	if _, err := NewJSONLSourceFromReader(bytes.NewReader(bytes.Join(lines[:len(lines)-1], []byte("\n")))); err == nil {
		t.Fatal("truncated manifest accepted")
	}
	var last JSONLRecord
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	last.Manifest.CheckpointCount++
	lines[len(lines)-1], _ = json.Marshal(last)
	if _, err := NewJSONLSourceFromReader(bytes.NewReader(bytes.Join(lines, []byte("\n")))); err == nil {
		t.Fatal("incorrect manifest counts accepted")
	}
}

func TestTrustedVerifierRefusesUnsignedNonemptyHistory(t *testing.T) {
	chain := NewMemoryChain()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	key, _ := GenerateSigningKey()
	report, err := Verify(context.Background(), SourceFromChain{Chain: chain}, []VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil || report.Valid || report.Outcome != OutcomeSignatureBreak {
		t.Fatalf("unsigned trusted verification=%#v err=%v", report, err)
	}
}
