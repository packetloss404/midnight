// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

type cliOutputWriter struct {
	io.Writer
	execution *CLIExecutionContext
}

func (writer *cliOutputWriter) Write(value []byte) (int, error) {
	if writer == nil || writer.Writer == nil {
		return len(value), nil
	}
	if writer.execution == nil || !writer.execution.Options.NoColor {
		return writer.Writer.Write(value)
	}
	clean := stripCLIANSI(string(value))
	_, err := io.WriteString(writer.Writer, clean)
	if err != nil {
		return 0, err
	}
	return len(value), nil
}

func stripCLIANSI(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	for index := 0; index < len(value); {
		current, size := utf8.DecodeRuneInString(value[index:])
		if current != '\x1b' && current != '\u009b' {
			output.WriteString(value[index : index+size])
			index += size
			continue
		}
		index += size
		if current == '\u009b' {
			index = consumeTerminalCSI(value, index)
			continue
		}
		if index >= len(value) {
			continue
		}
		next, nextSize := utf8.DecodeRuneInString(value[index:])
		index += nextSize
		switch next {
		case '[':
			index = consumeTerminalCSI(value, index)
		case ']', 'P', 'X', '^', '_':
			index = consumeTerminalControlString(value, index)
		}
	}
	return output.String()
}

type cliOutputSelection struct {
	format  string
	quiet   bool
	noColor bool
}

func cliOutputSelectionFor(output io.Writer, legacyJSON bool) cliOutputSelection {
	selection := cliOutputSelection{format: "table"}
	if wrapped, ok := output.(*cliOutputWriter); ok && wrapped.execution != nil {
		selection.format = strings.TrimSpace(wrapped.execution.Options.Output)
		selection.quiet = wrapped.execution.Options.Quiet
		selection.noColor = wrapped.execution.Options.NoColor
	}
	if selection.format == "" {
		selection.format = "table"
	}
	if legacyJSON && selection.format == "table" {
		selection.format = "json"
	}
	if selection.quiet {
		selection.format = "name"
	}
	return selection
}

// renderCLIList centralizes list output while keeping legacy --json behavior.
// Machine formats never include headings, progress, warnings, or table text.
func renderCLIList[T any](
	stdout, stderr io.Writer,
	legacyJSON bool,
	raw []byte,
	values []T,
	primary func(T) string,
	table func(io.Writer, []T),
) int {
	selection := cliOutputSelectionFor(stdout, legacyJSON)
	sorted := append([]T(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return primary(sorted[i]) < primary(sorted[j])
	})
	switch selection.format {
	case "json":
		return writeSortedRawJSON(stdout, stderr, raw)
	case "jsonl":
		return writeRawJSONLines(stdout, stderr, raw, primaryJSONKey)
	case "name":
		for _, value := range sorted {
			if name := strings.TrimSpace(primary(value)); name != "" {
				fmt.Fprintln(stdout, name)
			}
		}
		return 0
	default:
		table(stdout, sorted)
		return 0
	}
}

func cliOutputContext(output io.Writer) (context.Context, context.CancelFunc) {
	if wrapped, ok := output.(*cliOutputWriter); ok && wrapped.execution != nil && wrapped.execution.Options.Timeout > 0 {
		return context.WithTimeout(context.Background(), wrapped.execution.Options.Timeout)
	}
	return context.WithCancel(context.Background())
}

func writeSortedRawJSON(stdout, stderr io.Writer, raw []byte) int {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err == nil {
		sortRawJSONValues(values)
		return writeJSONValue(stdout, stderr, values)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(raw, &page); err != nil {
		return writeRawJSON(stdout, stderr, raw)
	}
	if data, ok := page["data"]; ok {
		if err := json.Unmarshal(data, &values); err == nil {
			sortRawJSONValues(values)
			page["data"], _ = json.Marshal(values)
		}
	}
	return writeJSONValue(stdout, stderr, page)
}

func sortRawJSONValues(values []json.RawMessage) {
	sort.SliceStable(values, func(i, j int) bool {
		left, right := primaryJSONKey(values[i]), primaryJSONKey(values[j])
		if left == right {
			return bytes.Compare(values[i], values[j]) < 0
		}
		return left < right
	})
}

func writeJSONValue(stdout, stderr io.Writer, value any) int {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "midnight: format JSON response: %s\n", err)
		return 1
	}
	encoded = append(encoded, '\n')
	_, _ = stdout.Write(encoded)
	return 0
}

// renderCLIRawDocument emits one server document for every machine output
// format and reports whether it handled the invocation. A table invocation
// returns false so the caller falls through to its human rendering. This is
// the single decision point that makes --output json, --output jsonl,
// --output name, and the legacy --json alias behave the same everywhere.
// cliDeletedDocument and cliRevokedDocument synthesize the document a
// no-content mutation would otherwise have no way to report, so --output json
// and --output name behave the same for a delete as for a read.
func cliDeletedDocument(idField, id string) []byte {
	return cliMutationDocument(idField, id, "deleted")
}

func cliRevokedDocument(idField, id string) []byte {
	return cliMutationDocument(idField, id, "revoked")
}

func cliMutationDocument(idField, id, outcome string) []byte {
	document := map[string]any{outcome: true}
	if strings.TrimSpace(idField) != "" {
		document[idField] = strings.TrimSpace(id)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return []byte(`{"` + outcome + `":true}`)
	}
	return encoded
}

func renderCLIRawDocument(stdout, stderr io.Writer, legacyJSON bool, raw []byte) (int, bool) {
	switch cliOutputSelectionFor(stdout, legacyJSON).format {
	case "json":
		return writeRawJSON(stdout, stderr, raw), true
	case "jsonl":
		return writeCompactJSONLines(stdout, stderr, raw), true
	case "name":
		return writePrimaryJSONNames(stdout, raw), true
	default:
		return 0, false
	}
}

// writeCompactJSONLines writes one compact JSON value per line: each element
// of an array or page, or the document itself.
func writeCompactJSONLines(stdout, stderr io.Writer, raw []byte) int {
	if values, ok := jsonDocumentElements(raw); ok {
		for _, value := range values {
			var compact bytes.Buffer
			if err := json.Compact(&compact, value); err != nil {
				fmt.Fprintf(stderr, "midnight: format JSONL response: %s\n", err)
				return 1
			}
			compact.WriteByte('\n')
			_, _ = stdout.Write(compact.Bytes())
		}
		return 0
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		fmt.Fprintf(stderr, "midnight: format JSONL response: %s\n", err)
		return 1
	}
	compact.WriteByte('\n')
	_, _ = stdout.Write(compact.Bytes())
	return 0
}

// writePrimaryJSONNames prints the primary identifier of each element, or of
// the document itself, one per line.
func writePrimaryJSONNames(stdout io.Writer, raw []byte) int {
	if values, ok := jsonDocumentElements(raw); ok {
		for _, value := range values {
			if name := strings.TrimSpace(primaryJSONKey(value)); name != "" {
				fmt.Fprintln(stdout, name)
			}
		}
		return 0
	}
	if name := strings.TrimSpace(primaryJSONKey(raw)); name != "" {
		fmt.Fprintln(stdout, name)
	}
	return 0
}

// jsonDocumentElements returns the elements of a bare array or of the "data"
// or "items" collection a paged response wraps them in.
func jsonDocumentElements(raw []byte) ([]json.RawMessage, bool) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err == nil {
		return values, true
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, false
	}
	for _, key := range []string{"data", "items"} {
		collection, ok := page[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(collection, &values); err == nil {
			return values, true
		}
	}
	return nil, false
}

func renderCLISingle[T any](
	stdout, stderr io.Writer,
	legacyJSON bool,
	raw []byte,
	value T,
	primary func(T) string,
	table func(io.Writer, T),
) int {
	selection := cliOutputSelectionFor(stdout, legacyJSON)
	switch selection.format {
	case "json":
		return writeRawJSON(stdout, stderr, raw)
	case "jsonl":
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			fmt.Fprintf(stderr, "midnight: format JSONL response: %s\n", err)
			return 1
		}
		compact.WriteByte('\n')
		_, _ = stdout.Write(compact.Bytes())
		return 0
	case "name":
		if name := strings.TrimSpace(primary(value)); name != "" {
			fmt.Fprintln(stdout, name)
		}
		return 0
	default:
		table(stdout, value)
		return 0
	}
}

func writeRawJSONLines(stdout, stderr io.Writer, raw []byte, key func(json.RawMessage) string) int {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		var page struct {
			Data []json.RawMessage `json:"data"`
		}
		if pageErr := json.Unmarshal(raw, &page); pageErr != nil || page.Data == nil {
			fmt.Fprintf(stderr, "midnight: format JSONL response: %s\n", err)
			return 1
		}
		values = page.Data
	}
	sort.SliceStable(values, func(i, j int) bool {
		left, right := key(values[i]), key(values[j])
		if left == right {
			return bytes.Compare(values[i], values[j]) < 0
		}
		return left < right
	})
	for _, value := range values {
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			fmt.Fprintf(stderr, "midnight: format JSONL response: %s\n", err)
			return 1
		}
		compact.WriteByte('\n')
		_, _ = stdout.Write(compact.Bytes())
	}
	return 0
}

// primaryJSONKey names a document: the identifier a caller would pipe into the
// next command. The scoped fallbacks matter for documents that describe a
// resource without carrying a bare "id", such as a service configuration.
func primaryJSONKey(value json.RawMessage) string {
	var identity struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		OperationID   string `json:"operation_id"`
		DeploymentID  string `json:"deployment_id"`
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ProjectID     string `json:"project_id"`
	}
	if json.Unmarshal(value, &identity) != nil {
		return string(value)
	}
	for _, candidate := range []string{
		identity.ID,
		identity.Name,
		identity.Slug,
		identity.OperationID,
		identity.DeploymentID,
		identity.ServiceID,
		identity.EnvironmentID,
		identity.ProjectID,
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}
