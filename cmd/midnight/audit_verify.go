package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/midnight/midnight/internal/audit"
)

// Audit verify exit codes are panel-locked by ADR-011 (see
// "Compliance / Verification" + "Decision" item 6 in
// agile/engineering/adr/ADR-011-audit-log-hash-chain-mvp.md):
//
//   - 0 healthy
//   - 1 warnings only (e.g. non-contiguous prev_checkpoint_id at a
//     pruned partition boundary)
//   - 2 chain mismatch / tamper detected
//   - 3 signature failure or signing key missing
//
// Usage / I/O errors that fall outside the panel envelope use the sysexits
// convention exit 64 (EX_USAGE) so they are unambiguously distinguishable from
// the four panel-locked outcomes.
const (
	auditVerifyExitOK             = 0
	auditVerifyExitWarn           = 1
	auditVerifyExitChainBreak     = 2
	auditVerifyExitSignatureBreak = 3
	auditVerifyExitUsage          = 64
	auditVerifyExitMissingInput   = 66 // EX_NOINPUT
)

// runAuditVerify is the subcommand entry point. It returns an exit code
// and never calls os.Exit itself; main.go is responsible for calling
// os.Exit with the returned code.
type auditVerifyFlags struct {
	fromJSONL    *string
	fromDB       *string
	publicKey    *string
	publicKeyDir *string
	asJSON       *bool
}

func defineAuditVerifyFlags(fs *flag.FlagSet) auditVerifyFlags {
	return auditVerifyFlags{
		fromJSONL:    fs.String("from-jsonl", "", "path to JSONL export to verify (mutually exclusive with --from-db)"),
		fromDB:       fs.String("from-db", "", "URL of a live audit chain to verify (not yet wired to Postgres)"),
		publicKey:    fs.String("public-key", "", "PEM-encoded MIDNIGHT AUDIT PUBLIC KEY file"),
		publicKeyDir: fs.String("public-key-dir", "", "directory of MIDNIGHT AUDIT PUBLIC KEY .pem files (rotation overlap)"),
		asJSON:       fs.Bool("json", false, "alias for --output json"),
	}
}

func runAuditVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineAuditVerifyFlags(fs)
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already wrote a usage message to stderr.
		return auditVerifyExitUsage
	}

	fromJSONL, fromDB := *f.fromJSONL, *f.fromDB
	keyFile, keyDir, jsonOut := *f.publicKey, *f.publicKeyDir, *f.asJSON

	if fromJSONL == "" && fromDB == "" {
		fmt.Fprintln(stderr, "audit verify: one of --from-jsonl or --from-db is required")
		fs.Usage()
		return auditVerifyExitUsage
	}
	if fromJSONL != "" && fromDB != "" {
		fmt.Fprintln(stderr, "audit verify: --from-jsonl and --from-db are mutually exclusive")
		return auditVerifyExitUsage
	}

	keys, err := loadVerifyingKeys(keyFile, keyDir)
	if err != nil {
		fmt.Fprintf(stderr, "audit verify: %v\n", err)
		if errors.Is(err, os.ErrNotExist) {
			return auditVerifyExitMissingInput
		}
		return auditVerifyExitSignatureBreak
	}

	ctx := context.Background()
	var src audit.Source
	if fromJSONL != "" {
		jsonl, err := audit.NewJSONLSourceFromFile(fromJSONL)
		if err != nil {
			fmt.Fprintf(stderr, "audit verify: read jsonl %s: %v\n", fromJSONL, err)
			if errors.Is(err, os.ErrNotExist) {
				return auditVerifyExitMissingInput
			}
			return auditVerifyExitUsage
		}
		src = jsonl
	} else {
		fmt.Fprintln(stderr, "audit verify: --from-db is not yet wired to a live Postgres source; use --from-jsonl for the current verification path.")
		return auditVerifyExitMissingInput
	}

	report, err := audit.Verify(ctx, src, keys)
	if err != nil {
		fmt.Fprintf(stderr, "audit verify: %v\n", err)
		return auditVerifyExitChainBreak
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "audit verify: render report: %v\n", err)
		return auditVerifyExitUsage
	}
	if _, done := renderCLIRawDocument(stdout, stderr, jsonOut, encoded); !done {
		writeHumanReport(stdout, report)
	}

	switch report.Outcome {
	case audit.OutcomeOK:
		return auditVerifyExitOK
	case audit.OutcomeWarn:
		return auditVerifyExitWarn
	case audit.OutcomeChainBreak:
		return auditVerifyExitChainBreak
	case audit.OutcomeSignatureBreak:
		return auditVerifyExitSignatureBreak
	default:
		return auditVerifyExitUsage
	}
}

func loadVerifyingKeys(keyFile, keyDir string) ([]audit.VerifyingKey, error) {
	var keys []audit.VerifyingKey
	if keyFile != "" {
		key, err := audit.LoadVerifyingKeyFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("load --public-key %s: %w", keyFile, err)
		}
		keys = append(keys, key)
	}
	if keyDir != "" {
		entries, err := os.ReadDir(keyDir)
		if err != nil {
			return nil, fmt.Errorf("read --public-key-dir %s: %w", keyDir, err)
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(ent.Name()), ".pem") {
				continue
			}
			path := filepath.Join(keyDir, ent.Name())
			key, err := audit.LoadVerifyingKeyFile(path)
			if err != nil {
				return nil, fmt.Errorf("load %s: %w", path, err)
			}
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func writeHumanReport(w io.Writer, r audit.VerifyReport) {
	fmt.Fprintf(w, "outcome: %s\n", r.Outcome)
	fmt.Fprintf(w, "signature_status: %s (signed_through_sequence=%d unsigned_rows=%d)\n", r.SignatureStatus, r.SignedThroughSequence, r.UnsignedRows)
	fmt.Fprintf(w, "rows checked: %d\n", r.RowsChecked)
	fmt.Fprintf(w, "checkpoints checked: %d\n", r.CheckpointsChecked)
	fmt.Fprintf(w, "genesis: %s\n", r.GenesisHashHex)
	if len(r.Breaks) > 0 {
		fmt.Fprintln(w, "breaks:")
		for _, b := range r.Breaks {
			fmt.Fprintf(w, "  - kind=%s sequence=%d checkpoint=%d detail=%s\n", b.Kind, b.Sequence, b.CheckpointID, b.Detail)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w, "warnings:")
		for _, b := range r.Warnings {
			fmt.Fprintf(w, "  - kind=%s sequence=%d checkpoint=%d detail=%s\n", b.Kind, b.Sequence, b.CheckpointID, b.Detail)
		}
	}
}
