package main

import (
	"fmt"
	"sort"
	"strings"
)

// CoverageReport summarizes drill metadata coverage: every drill row, every
// reason code referenced, every runbook referenced, and any references that
// are not present in the on-disk catalog. It is rendered as plain text by the
// `--print-coverage` flag and is useful for release-managers reviewing what
// the drill manifest currently asserts.
type CoverageReport struct {
	Drills            []CoverageDrillRow
	ReasonCodes       []string
	Runbooks          []string
	Events            []string
	MissingReferences []string
}

// CoverageDrillRow captures the per-drill summary used by CoverageReport.
type CoverageDrillRow struct {
	ID         string
	Title      string
	Status     string
	ReasonCode string
	Event      string
	Runbook    string
	Diagnostic string
}

// buildCoverageReport reads the drill manifest and reference catalog and
// returns a deterministic CoverageReport sorted by drill ID. Missing
// references are populated by validating the manifest against the catalog.
func buildCoverageReport(manifestData []byte, root string) (CoverageReport, error) {
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		return CoverageReport{}, err
	}
	if err := validateParsedDrillManifest(manifest); err != nil {
		return CoverageReport{}, err
	}
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		return CoverageReport{}, err
	}
	report := CoverageReport{}
	reasonSet := map[string]struct{}{}
	runbookSet := map[string]struct{}{}
	eventSet := map[string]struct{}{}
	for _, d := range manifest.Drills {
		report.Drills = append(report.Drills, CoverageDrillRow{
			ID: d.ID, Title: d.Title, Status: d.Status,
			ReasonCode: d.ReasonCode, Event: d.Event, Runbook: d.Runbook, Diagnostic: d.Diagnostic,
		})
		if v := strings.TrimSpace(d.ReasonCode); v != "" {
			reasonSet[v] = struct{}{}
		}
		if v := strings.TrimSpace(d.Runbook); v != "" {
			runbookSet[v] = struct{}{}
		}
		if v := strings.TrimSpace(d.Event); v != "" {
			eventSet[v] = struct{}{}
		}
	}
	report.ReasonCodes = sortedKeys(reasonSet)
	report.Runbooks = sortedKeys(runbookSet)
	report.Events = sortedKeys(eventSet)
	if err := validateDrillManifestReferences(manifest, catalog); err != nil {
		// validateDrillManifestReferences returns one error containing a
		// semicolon-separated set of missing references; expose them as a
		// list for the coverage view.
		message := err.Error()
		const prefix = "drill manifest references invalid: "
		if strings.HasPrefix(message, prefix) {
			message = strings.TrimPrefix(message, prefix)
		}
		for _, part := range strings.Split(message, "; ") {
			if part = strings.TrimSpace(part); part != "" {
				report.MissingReferences = append(report.MissingReferences, part)
			}
		}
		sort.Strings(report.MissingReferences)
	}
	sort.Slice(report.Drills, func(i, j int) bool { return report.Drills[i].ID < report.Drills[j].ID })
	return report, nil
}

// Text renders the coverage report as a deterministic plain-text report. The
// output is stable across runs given the same manifest and catalog input.
func (r CoverageReport) Text() string {
	var b strings.Builder
	b.WriteString("# Phase 0 Gate Coverage\n\n")
	b.WriteString(fmt.Sprintf("Drill rows: %d\n", len(r.Drills)))
	b.WriteString(fmt.Sprintf("Reason codes referenced: %d\n", len(r.ReasonCodes)))
	b.WriteString(fmt.Sprintf("Runbooks referenced: %d\n", len(r.Runbooks)))
	b.WriteString(fmt.Sprintf("Events referenced: %d\n", len(r.Events)))
	b.WriteString(fmt.Sprintf("Missing references: %d\n\n", len(r.MissingReferences)))

	b.WriteString("## Drills\n")
	for _, d := range r.Drills {
		b.WriteString(fmt.Sprintf("- %s [%s] %s\n", d.ID, d.Status, d.Title))
		b.WriteString(fmt.Sprintf("    reason-code: %s\n", d.ReasonCode))
		b.WriteString(fmt.Sprintf("    event:       %s\n", d.Event))
		b.WriteString(fmt.Sprintf("    runbook:     %s\n", d.Runbook))
	}

	b.WriteString("\n## Reason Codes\n")
	for _, code := range r.ReasonCodes {
		b.WriteString("- " + code + "\n")
	}
	b.WriteString("\n## Runbooks\n")
	for _, rb := range r.Runbooks {
		b.WriteString("- " + rb + "\n")
	}
	b.WriteString("\n## Events\n")
	for _, ev := range r.Events {
		b.WriteString("- " + ev + "\n")
	}
	if len(r.MissingReferences) > 0 {
		b.WriteString("\n## Missing References\n")
		for _, m := range r.MissingReferences {
			b.WriteString("- " + m + "\n")
		}
	}
	return b.String()
}

// Markdown renders the coverage report as a deterministic Markdown document
// that mirrors the content of Text() but uses Markdown headings, tables, and
// bullet lists. The output is stable across runs given the same manifest and
// catalog input. Markdown rendering is the format that downstream evidence
// archives and PR review tooling consume; Text() remains the terminal-friendly
// view.
func (r CoverageReport) Markdown() string {
	var b strings.Builder
	b.WriteString("# Phase 0 Gate Coverage\n\n")
	b.WriteString("## Summary\n\n")
	b.WriteString("| Metric | Count |\n")
	b.WriteString("| --- | ---: |\n")
	b.WriteString(fmt.Sprintf("| Drill rows | %d |\n", len(r.Drills)))
	b.WriteString(fmt.Sprintf("| Reason codes referenced | %d |\n", len(r.ReasonCodes)))
	b.WriteString(fmt.Sprintf("| Runbooks referenced | %d |\n", len(r.Runbooks)))
	b.WriteString(fmt.Sprintf("| Events referenced | %d |\n", len(r.Events)))
	b.WriteString(fmt.Sprintf("| Missing references | %d |\n", len(r.MissingReferences)))

	b.WriteString("\n## Drills\n\n")
	b.WriteString("| ID | Status | Title | Reason Code | Event | Runbook |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, d := range r.Drills {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			d.ID, d.Status, escapeMarkdownPipe(d.Title),
			d.ReasonCode, d.Event, d.Runbook))
	}

	b.WriteString("\n## Reason Codes\n\n")
	if len(r.ReasonCodes) == 0 {
		b.WriteString("_None._\n")
	} else {
		for _, code := range r.ReasonCodes {
			b.WriteString("- `" + code + "`\n")
		}
	}
	b.WriteString("\n## Runbooks\n\n")
	if len(r.Runbooks) == 0 {
		b.WriteString("_None._\n")
	} else {
		for _, rb := range r.Runbooks {
			b.WriteString("- `" + rb + "`\n")
		}
	}
	b.WriteString("\n## Events\n\n")
	if len(r.Events) == 0 {
		b.WriteString("_None._\n")
	} else {
		for _, ev := range r.Events {
			b.WriteString("- `" + ev + "`\n")
		}
	}
	if len(r.MissingReferences) > 0 {
		b.WriteString("\n## Missing References\n\n")
		for _, m := range r.MissingReferences {
			b.WriteString("- " + m + "\n")
		}
	}
	return b.String()
}

// escapeMarkdownPipe escapes the pipe character in Markdown table cells so
// drill titles containing literal "|" do not break the row layout.
func escapeMarkdownPipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
