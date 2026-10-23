package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type evidenceRefsFlag []evidenceRef

func (refs *evidenceRefsFlag) String() string {
	if refs == nil || len(*refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(*refs))
	for _, ref := range *refs {
		parts = append(parts, ref.Name+"="+ref.Path)
	}
	return strings.Join(parts, ",")
}

func (refs *evidenceRefsFlag) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok {
		return fmt.Errorf("evidence ref must be name=path")
	}
	ref := evidenceRef{Name: strings.TrimSpace(name), Path: strings.TrimSpace(path)}
	if ref.Name == "" || ref.Path == "" {
		return fmt.Errorf("evidence ref must include non-empty name and path")
	}
	*refs = append(*refs, ref)
	return nil
}

// gateInputs is the parsed CLI surface for the phase0-gate binary. It is
// constructed from the process flag set by parseFlags and consumed by run.
type gateInputs struct {
	Format                     string
	Out                        string
	LastGreen                  string
	DrillAdapter               string
	ExecuteDrills              bool
	RequireExecutedDrills      bool
	ReleaseEvidence            string
	ReleaseSummary             string
	ReleaseEvidenceIndex       string
	VerifyReleaseEvidenceIndex string
	VerifyEvidenceFiles        bool
	EvidenceRefs               []evidenceRef
	JUnitOut                   string
	PrintCoverage              bool
	Quiet                      bool
	ValidateReferences         bool
	References                 string
	DiffEvidenceLeft           string
	DiffEvidenceRight          string
	DiffOut                    string
}

func main() {
	inputs, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "phase0 gate failed: %v\n", err)
		os.Exit(ExitInputError.Int())
	}
	exit := run(inputs, os.Stdout, os.Stderr)
	os.Exit(exit.Int())
}

func parseFlags(args []string) (gateInputs, error) {
	fs := flag.NewFlagSet("phase0-gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inputs := gateInputs{}
	fs.StringVar(&inputs.Format, "format", "text", "report format: text, markdown, or json")
	fs.StringVar(&inputs.Out, "out", "", "write report to path instead of stdout")
	fs.StringVar(&inputs.LastGreen, "last-green", "unknown", "last green release SHA or unknown")
	fs.StringVar(&inputs.DrillAdapter, "drill-adapter", "local", "drill execution adapter: local or live (live is rejected until implemented)")
	fs.BoolVar(&inputs.ExecuteDrills, "execute-drills", false, "execute drill adapter and include non-blocking drill execution summary")
	fs.BoolVar(&inputs.RequireExecutedDrills, "require-executed-drills", false, "require executed drills to run and pass without skipped, blocked, or failed drills")
	fs.StringVar(&inputs.ReleaseEvidence, "release-evidence", "", "write deterministic release evidence JSON to path")
	fs.StringVar(&inputs.ReleaseSummary, "release-summary", "", "write concise deterministic release summary JSON to path")
	fs.StringVar(&inputs.ReleaseEvidenceIndex, "release-evidence-index", "", "write deterministic release evidence archive index JSON to path")
	fs.StringVar(&inputs.VerifyReleaseEvidenceIndex, "verify-release-evidence-index", "", "read and verify release evidence archive index JSON from path")
	fs.BoolVar(&inputs.VerifyEvidenceFiles, "verify-evidence-files", false, "when verifying a release evidence index, require referenced relative files to exist from the current working directory")
	fs.StringVar(&inputs.JUnitOut, "junit-out", "", "write JUnit XML test report to path")
	fs.BoolVar(&inputs.PrintCoverage, "print-coverage", false, "print drill coverage report (drill rows, reason codes, runbooks, missing references) and exit")
	fs.BoolVar(&inputs.Quiet, "quiet", false, "suppress success output; only print failures")
	fs.BoolVar(&inputs.ValidateReferences, "validate-references", false, "validate drill manifest references against the on-disk error-codes registry, runbooks, and event allowlist")
	fs.StringVar(&inputs.References, "references", "", "reference-catalog mode; set to 'strict' to require every drill row's runbook field to resolve to an existing file under docs/runbooks/")
	fs.StringVar(&inputs.DiffEvidenceLeft, "diff-evidence-left", "", "left release-evidence JSON path for diff mode")
	fs.StringVar(&inputs.DiffEvidenceRight, "diff-evidence-right", "", "right release-evidence JSON path for diff mode")
	fs.StringVar(&inputs.DiffOut, "diff-out", "", "write release-evidence diff JSON to path; defaults to stdout when diff mode is active")
	var evidenceRefs evidenceRefsFlag
	fs.Var(&evidenceRefs, "evidence-ref", "additional release evidence reference as name=relative/path, repeatable")
	if err := fs.Parse(args); err != nil {
		return gateInputs{}, err
	}
	inputs.EvidenceRefs = evidenceRefs
	return inputs, nil
}

// run executes one phase0-gate invocation given pre-parsed inputs and writers.
// The returned ExitCode is the deterministic exit code surface documented in
// gate.ExitCode.
func run(inputs gateInputs, stdout, stderr io.Writer) ExitCode {
	if inputs.Format != "text" && inputs.Format != "markdown" && inputs.Format != "json" {
		fmt.Fprintf(stderr, "phase0 gate failed: unsupported format %q\n", inputs.Format)
		return ExitInputError
	}
	if err := validateCLIOptions(inputs.ExecuteDrills, inputs.RequireExecutedDrills, inputs.DrillAdapter, inputs.ReleaseEvidence, inputs.ReleaseSummary, inputs.ReleaseEvidenceIndex, inputs.VerifyReleaseEvidenceIndex, inputs.VerifyEvidenceFiles, inputs.EvidenceRefs); err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitInputError
	}
	if inputs.DiffEvidenceLeft != "" || inputs.DiffEvidenceRight != "" {
		return runDiffMode(inputs, stdout, stderr)
	}

	alerts, err := os.ReadFile("deploy/prometheus/alerts.yaml")
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitInputError
	}
	manifest, err := os.ReadFile("test/drills/manifest.yaml")
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitInputError
	}
	if inputs.ValidateReferences {
		if err := validateDrillManifestWithReferences(manifest, "."); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitManifestInvalid
		}
	}
	if mode := strings.ToLower(strings.TrimSpace(inputs.References)); mode != "" {
		if mode != "strict" {
			fmt.Fprintf(stderr, "phase0 gate failed: unsupported --references mode %q (only \"strict\" is supported)\n", inputs.References)
			return ExitInputError
		}
		parsed, err := parseDrillManifest(manifest)
		if err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitManifestInvalid
		}
		if err := validateParsedDrillManifest(parsed); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitManifestInvalid
		}
		if err := ValidateRunbookReferencesStrict(parsed, "."); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitManifestInvalid
		}
	}
	if inputs.PrintCoverage {
		coverage, err := buildCoverageReport(manifest, ".")
		if err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitManifestInvalid
		}
		fmt.Fprint(stdout, coverage.Text())
		return ExitOK
	}

	var adapter drillExecutionAdapter
	if inputs.ExecuteDrills {
		adapter = localDrillExecutionAdapter{manifestData: manifest, alertData: alerts}
	}
	report, err := buildGateReportWithDrillExecution(manifest, alerts, inputs.LastGreen, adapter, inputs.RequireExecutedDrills)
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitManifestInvalid
	}
	modeFlags := cliModeFlags{
		Format:                     inputs.Format,
		Out:                        inputs.Out,
		LastGreen:                  inputs.LastGreen,
		DrillAdapter:               normalizeDrillAdapter(inputs.DrillAdapter),
		ExecuteDrills:              inputs.ExecuteDrills,
		RequireExecutedDrills:      inputs.RequireExecutedDrills,
		ReleaseEvidence:            inputs.ReleaseEvidence,
		ReleaseSummary:             inputs.ReleaseSummary,
		ReleaseEvidenceIndex:       inputs.ReleaseEvidenceIndex,
		VerifyReleaseEvidenceIndex: inputs.VerifyReleaseEvidenceIndex,
		VerifyEvidenceFiles:        inputs.VerifyEvidenceFiles,
		EvidenceRefs:               inputs.EvidenceRefs,
	}
	if inputs.ReleaseEvidenceIndex != "" {
		index, err := buildReleaseEvidenceArchiveIndex(inputs.ReleaseEvidence, inputs.EvidenceRefs)
		if err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
		if err := writeReleaseEvidenceArchiveIndex(inputs.ReleaseEvidenceIndex, index); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	}
	if inputs.ReleaseEvidence != "" && inputs.VerifyEvidenceFiles {
		if err := writeReleaseEvidence(inputs.ReleaseEvidence, buildReleaseEvidence(report, modeFlags, inputs.RequireExecutedDrills)); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	}
	if inputs.VerifyReleaseEvidenceIndex != "" {
		verification := verifyReleaseEvidenceArchiveIndexFile(inputs.VerifyReleaseEvidenceIndex, inputs.VerifyEvidenceFiles)
		report.ReleaseEvidenceIndexVerification = &verification
		report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: "release-evidence-index", Status: verification.Status, Path: inputs.VerifyReleaseEvidenceIndex})
	}
	data, err := report.Render(inputs.Format)
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitInputError
	}
	if inputs.ReleaseEvidence != "" {
		evidence := buildReleaseEvidence(report, modeFlags, inputs.RequireExecutedDrills)
		if err := writeReleaseEvidence(inputs.ReleaseEvidence, evidence); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	}
	if inputs.ReleaseSummary != "" {
		summary := buildReleaseSummary(report)
		summary = attachCanonicalProofPacketToSummary(summary, report, modeFlags)
		summary = enrichReleaseSummary(summary, manifest)
		if err := writeReleaseSummary(inputs.ReleaseSummary, summary); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	}
	if inputs.JUnitOut != "" {
		if err := writeJUnitReportFromReport(inputs.JUnitOut, report); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	}
	failed := shouldFailForRequiredChecks(report, inputs.RequireExecutedDrills)
	if inputs.Out != "" {
		if err := os.WriteFile(inputs.Out, data, 0o644); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	} else if !inputs.Quiet || failed {
		fmt.Fprint(stdout, string(data))
	}
	if failed {
		if shouldFailForDrillExecution(report, inputs.RequireExecutedDrills) {
			return ExitDrillFailure
		}
		return ExitReleaseEvidenceFailure
	}
	return ExitOK
}

// runDiffMode executes the release-evidence diff helper and writes the result
// to the requested destination. Both --diff-evidence-left and
// --diff-evidence-right must be set.
func runDiffMode(inputs gateInputs, stdout, stderr io.Writer) ExitCode {
	if inputs.DiffEvidenceLeft == "" || inputs.DiffEvidenceRight == "" {
		fmt.Fprintln(stderr, "phase0 gate failed: --diff-evidence-left and --diff-evidence-right are both required for diff mode")
		return ExitInputError
	}
	diff, err := diffReleaseEvidenceFiles(inputs.DiffEvidenceLeft, inputs.DiffEvidenceRight)
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitReleaseEvidenceFailure
	}
	data, err := diff.JSON()
	if err != nil {
		fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
		return ExitReleaseEvidenceFailure
	}
	data = append(data, '\n')
	if inputs.DiffOut != "" {
		if err := os.WriteFile(inputs.DiffOut, data, 0o644); err != nil {
			fmt.Fprintf(stderr, "phase0 gate failed: %v\n", err)
			return ExitReleaseEvidenceFailure
		}
	} else if !inputs.Quiet {
		fmt.Fprint(stdout, string(data))
	}
	return ExitOK
}

func validateCLIOptions(executeDrills, requireExecutedDrills bool, drillAdapter string, releaseEvidence string, releaseSummary string, releaseEvidenceIndex string, verifyReleaseEvidenceIndex string, verifyEvidenceFiles bool, evidenceRefs []evidenceRef) error {
	adapter := normalizeDrillAdapter(drillAdapter)
	if adapter != "local" && adapter != "live" {
		return fmt.Errorf("unsupported --drill-adapter %q (supported: local, live)", drillAdapter)
	}
	if adapter == "live" {
		return fmt.Errorf("--drill-adapter=live is not implemented; refusing unsafe live drill execution")
	}
	if !executeDrills && adapter != "local" {
		return fmt.Errorf("--drill-adapter requires --execute-drills")
	}
	if requireExecutedDrills && !executeDrills {
		return fmt.Errorf("--require-executed-drills requires --execute-drills")
	}
	if releaseEvidence != "" {
		if err := validateReleaseEvidencePath(releaseEvidence); err != nil {
			return err
		}
	}
	if releaseSummary != "" {
		if err := validateReleaseEvidencePath(releaseSummary); err != nil {
			return err
		}
	}
	if releaseEvidenceIndex != "" {
		if err := validateReleaseEvidencePath(releaseEvidenceIndex); err != nil {
			return err
		}
		_, err := buildReleaseEvidenceArchiveIndex(releaseEvidence, evidenceRefs)
		return err
	}
	if len(evidenceRefs) > 0 {
		return fmt.Errorf("--evidence-ref requires --release-evidence-index")
	}
	if verifyReleaseEvidenceIndex != "" {
		if err := validateReleaseEvidencePath(verifyReleaseEvidenceIndex); err != nil {
			return err
		}
	}
	if verifyEvidenceFiles && verifyReleaseEvidenceIndex == "" {
		return fmt.Errorf("--verify-evidence-files requires --verify-release-evidence-index")
	}
	return nil
}

func normalizeDrillAdapter(adapter string) string {
	adapter = strings.ToLower(strings.TrimSpace(adapter))
	if adapter == "" {
		return "local"
	}
	return adapter
}

func shouldFailForRequiredChecks(report gateReport, requireExecutedDrills bool) bool {
	if shouldFailForDrillExecution(report, requireExecutedDrills) {
		return true
	}
	return requireExecutedDrills && report.ReleaseEvidenceIndexVerification != nil && report.ReleaseEvidenceIndexVerification.Status != "pass"
}

func shouldFailForDrillExecution(report gateReport, requireExecutedDrills bool) bool {
	if report.DrillExecution == nil {
		return false
	}
	if requireExecutedDrills {
		return !executedDrillsPassed(*report.DrillExecution)
	}
	return report.DrillExecution.Failed > 0
}
