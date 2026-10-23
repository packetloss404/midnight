package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/audit"
)

func writeJSONLExport(t *testing.T, dir string, mutator func(*audit.MemoryChain)) (jsonlPath, pubKeyPath string, key audit.SigningKey) {
	t.Helper()
	chain := audit.NewMemoryChain()
	for i := 0; i < 5; i++ {
		_, err := chain.Append(context.Background(), audit.Row{
			ActorType:    "system",
			ResourceKind: "deployment",
			Action:       "transition",
			RequestID:    "req",
			Attrs:        map[string]any{"i": int64(i)},
		})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	var err error
	key, err = audit.GenerateSigningKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if _, err := audit.NewCheckpointer(chain, key).Sign(context.Background()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if mutator != nil {
		mutator(chain)
	}
	jsonlPath = filepath.Join(dir, "audit.jsonl")
	dst := audit.FileDestination{Path: jsonlPath}
	if _, err := audit.NewExporter(chain).Export(context.Background(), dst); err != nil {
		t.Fatalf("export: %v", err)
	}
	pubKeyPath = filepath.Join(dir, "audit-pub.pem")
	if err := os.WriteFile(pubKeyPath, audit.EncodeVerifyingKeyPEM(key), 0o600); err != nil {
		t.Fatalf("write pubkey: %v", err)
	}
	return jsonlPath, pubKeyPath, key
}

func TestRunAuditVerifyOK(t *testing.T) {
	dir := t.TempDir()
	jsonlPath, pubKeyPath, _ := writeJSONLExport(t, dir, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAuditVerify([]string{"--from-jsonl", jsonlPath, "--public-key", pubKeyPath}, stdout, stderr)
	if code != auditVerifyExitOK {
		t.Fatalf("ok run exit = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "outcome: ok") {
		t.Fatalf("expected ok outcome in stdout: %s", stdout.String())
	}
}

func TestRunAuditVerifyChainBreakExit2(t *testing.T) {
	dir := t.TempDir()
	jsonlPath, pubKeyPath, _ := writeJSONLExport(t, dir, func(c *audit.MemoryChain) {
		// Tamper after the checkpoint signs to simulate post-export
		// chain edit. The exported file already has the checkpoint
		// signature over the unmodified chain, so verification should
		// detect both row_hash mismatch and (depending on order) a
		// checkpoint last_row_hash mismatch.
		_ = c.Tamper(2, func(r *audit.Row) { r.Action = "tampered-by-attacker" })
	})
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAuditVerify([]string{"--from-jsonl", jsonlPath, "--public-key", pubKeyPath}, stdout, stderr)
	if code != auditVerifyExitChainBreak {
		t.Fatalf("tamper exit = %d (want %d) stderr=%s stdout=%s", code, auditVerifyExitChainBreak, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "sequence=2") {
		t.Fatalf("expected offending row 2 in output: %s", out)
	}
	if !strings.Contains(out, "row_hash") {
		t.Fatalf("expected row_hash break in output: %s", out)
	}
}

func TestRunAuditVerifyMissingKeyExit3(t *testing.T) {
	dir := t.TempDir()
	jsonlPath, _, _ := writeJSONLExport(t, dir, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAuditVerify([]string{"--from-jsonl", jsonlPath}, stdout, stderr)
	if code != auditVerifyExitSignatureBreak {
		t.Fatalf("missing key exit = %d (want %d) stderr=%s stdout=%s", code, auditVerifyExitSignatureBreak, stderr.String(), stdout.String())
	}
}

func TestRunAuditVerifyMissingInputFile(t *testing.T) {
	dir := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAuditVerify([]string{"--from-jsonl", filepath.Join(dir, "nope.jsonl")}, stdout, stderr)
	if code != auditVerifyExitMissingInput {
		t.Fatalf("missing input exit = %d (want %d) stderr=%s", code, auditVerifyExitMissingInput, stderr.String())
	}
}

func TestRunAuditVerifyUsageExit(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if got := runAuditVerify(nil, stdout, stderr); got != auditVerifyExitUsage {
		t.Fatalf("no args exit = %d (want %d)", got, auditVerifyExitUsage)
	}
	if got := runAuditVerify([]string{"--from-jsonl", "a", "--from-db", "b"}, stdout, stderr); got != auditVerifyExitUsage {
		t.Fatalf("conflicting args exit = %d (want %d)", got, auditVerifyExitUsage)
	}
}

func TestRunAuditVerifyJSONFlag(t *testing.T) {
	dir := t.TempDir()
	jsonlPath, pubKeyPath, _ := writeJSONLExport(t, dir, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAuditVerify([]string{"--from-jsonl", jsonlPath, "--public-key", pubKeyPath, "--json"}, stdout, stderr)
	if code != auditVerifyExitOK {
		t.Fatalf("json exit = %d", code)
	}
	if !strings.Contains(stdout.String(), `"outcome": "ok"`) {
		t.Fatalf("expected json outcome: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status": "verified"`) || !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("expected acceptance status fields: %s", stdout.String())
	}
}
