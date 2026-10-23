package supportbundle

import (
	"fmt"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/redaction"
)

// BundleInputFile is one in-memory bundle file plus a kind hint. Text-typed files
// (Kind starting with "log", "config", "event", "metric", "doctor", or "text")
// are routed through the default redactor; non-text files are pass-through.
type BundleInputFile struct {
	Path    string
	Kind    string
	Content []byte
	IsText  bool
}

// BundleInput is the input to RedactBundle: a set of files plus an optional bundle id.
type BundleInput struct {
	BundleID string
	Files    []BundleInputFile
}

// BundleOutputFile is the post-redaction view of one bundle file.
type BundleOutputFile struct {
	Path    string
	Kind    string
	Content []byte
	IsText  bool
}

// BundleOutput is the post-redaction set of files mirroring BundleInput.
type BundleOutput struct {
	BundleID string
	Files    []BundleOutputFile
}

// RedactionReportEntry is the per-file accounting of redactor matches.
type RedactionReportEntry struct {
	Path       string
	IsText     bool
	MatchCount int
}

// RedactionReport is the per-file match-count report produced alongside RedactBundle output.
type RedactionReport struct {
	BundleID string
	Entries  []RedactionReportEntry
	// TotalMatches sums all per-file match counts for quick top-line reporting.
	TotalMatches int
}

// RedactBundle runs the package-default redactor (internal/redaction.DefaultRedactor)
// over every text-typed file in input and returns the redacted bundle alongside a
// per-file match-count report. Non-text files pass through with zero match counts.
//
// The function is pure: it does no I/O and does not mutate input. The returned
// BundleOutput carries the same bundle id, the same path/kind/IsText shape, and
// only the file content is modified for text files. Files in stable (path-sorted)
// order are processed deterministically so the report is reproducible.
func RedactBundle(input BundleInput) (BundleOutput, RedactionReport, error) {
	if strings.TrimSpace(input.BundleID) == "" {
		return BundleOutput{}, RedactionReport{}, failurecodes.New(failurecodes.SupportBundleRedactionFailed, "bundle input id is required")
	}
	redactor := redaction.DefaultRedactor()

	files := append([]BundleInputFile(nil), input.Files...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	output := BundleOutput{BundleID: input.BundleID, Files: make([]BundleOutputFile, 0, len(files))}
	report := RedactionReport{BundleID: input.BundleID, Entries: make([]RedactionReportEntry, 0, len(files))}

	seen := make(map[string]struct{}, len(files))
	for i, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			return BundleOutput{}, RedactionReport{}, failurecodes.New(failurecodes.SupportBundleRedactionFailed, fmt.Sprintf("files[%d]: path is required", i))
		}
		if _, dup := seen[file.Path]; dup {
			return BundleOutput{}, RedactionReport{}, failurecodes.New(failurecodes.SupportBundleRedactionFailed, fmt.Sprintf("files[%d]: duplicate path %q", i, file.Path))
		}
		seen[file.Path] = struct{}{}

		out := BundleOutputFile{Path: file.Path, Kind: file.Kind, IsText: file.IsText}
		entry := RedactionReportEntry{Path: file.Path, IsText: file.IsText}
		if file.IsText {
			before := string(file.Content)
			after := redactor.RedactString(before)
			entry.MatchCount = countRedactionMatches(before, after)
			out.Content = []byte(after)
		} else {
			out.Content = append([]byte(nil), file.Content...)
		}
		report.TotalMatches += entry.MatchCount
		output.Files = append(output.Files, out)
		report.Entries = append(report.Entries, entry)
	}
	return output, report, nil
}

// countRedactionMatches counts how many redactor placeholder substitutions were applied
// when transforming before to after. The default redactor emits the constant prefix
// "[REDACTED]" for every match; counting placeholder occurrences in the after-string
// minus those that already appeared in the before-string gives a stable per-file count.
func countRedactionMatches(before, after string) int {
	if before == after {
		return 0
	}
	want := redaction.Placeholder
	return strings.Count(after, want) - strings.Count(before, want)
}
