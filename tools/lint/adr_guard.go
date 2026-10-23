package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// adrDir holds the architecture decision records.
const adrDir = "docs/adr"

// adrIndexFile is the human-readable index re-derived from the per-ADR
// front-matter and verified for drift on every lint run.
const adrIndexFile = "docs/adr/README.md"

// adrFileNamePattern matches the canonical four-digit-numbered ADR file name
// "0001-some-slug.md". The leading number drives the sort order.
var adrFileNamePattern = regexp.MustCompile(`^([0-9]{4})-[a-z0-9-]+\.md$`)

// adrEntry is a single decoded ADR row used to render the deterministic index.
type adrEntry struct {
	Number string
	File   string
	Title  string
	Status string
}

// checkADRIndexDrift re-derives the ADR index from each ADR's front-matter and
// fails when docs/adr/README.md has drifted. The docs/adr/0000-template.md
// file is the canonical template and is excluded from the index.
func checkADRIndexDrift(root string) []error {
	dirAbs := filepath.Join(root, filepath.FromSlash(adrDir))
	indexAbs := filepath.Join(root, filepath.FromSlash(adrIndexFile))
	if _, err := os.Stat(dirAbs); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", adrDir, err)}
	}
	if _, err := os.Stat(indexAbs); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", adrIndexFile, err)}
	}
	entries, err := loadADREntries(dirAbs)
	if err != nil {
		return []error{err}
	}
	if len(entries) == 0 {
		return nil
	}
	want := renderADRIndex(entries)
	got, err := readNormalizedFile(indexAbs)
	if err != nil {
		return []error{err}
	}
	if string(got) != want {
		return []error{fmt.Errorf("%s drifted from front-matter; rerun the index regeneration to fix", adrIndexFile)}
	}
	return nil
}

// loadADREntries reads ADR markdown files in dirAbs and returns deterministic
// front-matter projections. ADRs that lack a Status line or H1 title are
// reported as errors so authors fix the source rather than the index.
func loadADREntries(dirAbs string) ([]adrEntry, error) {
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", adrDir, err)
	}
	var out []adrEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "0000-template.md" || name == "README.md" {
			continue
		}
		m := adrFileNamePattern.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		full := filepath.Join(dirAbs, name)
		data, err := readNormalizedFile(full)
		if err != nil {
			return nil, err
		}
		title, status, err := parseADRFrontMatter(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", adrDir, name, err)
		}
		out = append(out, adrEntry{Number: m[1], File: name, Title: title, Status: status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// parseADRFrontMatter extracts the H1 title and "- Status: <value>" line from
// the body of an ADR. The H1 form is "# ADR-NNNN: <title>" so the numeric
// prefix is removed for the index column.
func parseADRFrontMatter(body string) (string, string, error) {
	var title, status string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if title == "" && strings.HasPrefix(trimmed, "# ADR-") {
			_, t, ok := strings.Cut(trimmed, ":")
			if ok {
				title = strings.TrimSpace(t)
			}
		}
		if status == "" && strings.HasPrefix(trimmed, "- Status:") {
			status = strings.TrimSpace(strings.TrimPrefix(trimmed, "- Status:"))
		}
		if title != "" && status != "" {
			break
		}
	}
	if title == "" {
		return "", "", fmt.Errorf("missing \"# ADR-NNNN:\" title heading")
	}
	if status == "" {
		return "", "", fmt.Errorf("missing \"- Status:\" line")
	}
	return title, status, nil
}

// renderADRIndex returns the deterministic markdown body for the ADR index.
// The format is intentionally compact: a fixed header followed by one row per
// ADR ordered by number.
func renderADRIndex(entries []adrEntry) string {
	var b strings.Builder
	b.WriteString("# Architecture Decision Records\n")
	b.WriteString("\n")
	b.WriteString("| ADR | Status | Title |\n")
	b.WriteString("|---|---|---|\n")
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("| [%s](%s) | %s | %s |\n", e.Number, e.File, e.Status, e.Title))
	}
	return b.String()
}

// writeADRIndex regenerates docs/adr/README.md from the per-ADR front-matter
// and writes it to disk if it differs from the on-disk content. The first
// return is true when the file was written, false when it already matched.
// The function returns nil when the ADR directory is absent or empty so the
// `--adr-index-write` mode is safe to run on a fresh clone.
func writeADRIndex(root string) (bool, error) {
	dirAbs := filepath.Join(root, filepath.FromSlash(adrDir))
	indexAbs := filepath.Join(root, filepath.FromSlash(adrIndexFile))
	if _, err := os.Stat(dirAbs); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", adrDir, err)
	}
	entries, err := loadADREntries(dirAbs)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	want := renderADRIndex(entries)
	current, err := readNormalizedFile(indexAbs)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", adrIndexFile, err)
	}
	if err == nil && string(current) == want {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(indexAbs), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(indexAbs), err)
	}
	if err := os.WriteFile(indexAbs, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", adrIndexFile, err)
	}
	return true, nil
}
