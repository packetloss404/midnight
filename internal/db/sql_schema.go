// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package db

import (
	"strings"
)

// TableColumn captures a single column definition extracted by the offline
// SQL subset parser. It is intentionally lossy — the goal is evidence
// (counts and shapes), not full DDL fidelity.
type TableColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// TableEvidence is a richer description of a CREATE TABLE statement than
// the flat "table" entry in DetectedObjects.
type TableEvidence struct {
	Name        string        `json:"name"`
	Columns     []TableColumn `json:"columns"`
	PrimaryKeys []string      `json:"primary_keys,omitempty"`
	Partitioned bool          `json:"partitioned,omitempty"`
	PartitionBy string        `json:"partition_by,omitempty"`
}

// IndexEvidence describes a CREATE INDEX statement.
type IndexEvidence struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Columns []string `json:"columns,omitempty"`
	Unique  bool     `json:"unique,omitempty"`
}

// PartitionEvidence captures a CREATE TABLE ... PARTITION OF clause.
type PartitionEvidence struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

// ParseSQLSubset extracts tables, indexes, and partitions from the provided
// SQL text. It supports a small but representative subset of Postgres DDL
// — enough to surface migration evidence without depending on a real
// database. Statements outside the supported subset are reported via the
// returned skipped count.
func ParseSQLSubset(sql string) (tables []TableEvidence, indexes []IndexEvidence, partitions []PartitionEvidence, unsupported int, invalid int) {
	for _, raw := range splitStatements(sql) {
		statement := strings.TrimSpace(stripLineComments(raw))
		if statement == "" {
			continue
		}
		fields := strings.Fields(statement)
		if len(fields) < 2 {
			invalid++
			continue
		}
		if !strings.EqualFold(fields[0], "create") {
			unsupported++
			continue
		}
		// Detect CREATE UNIQUE INDEX / CREATE INDEX
		if strings.EqualFold(fields[1], "unique") && len(fields) > 2 && strings.EqualFold(fields[2], "index") {
			if index, ok := parseIndexStatement(fields[2:], statement, true); ok {
				indexes = append(indexes, index)
			} else {
				invalid++
			}
			continue
		}
		switch strings.ToLower(fields[1]) {
		case "table":
			table, partition, ok, kind := parseTableStatement(fields[1:], statement)
			switch kind {
			case "table":
				if ok {
					tables = append(tables, table)
				} else {
					invalid++
				}
			case "partition":
				if ok {
					partitions = append(partitions, partition)
				} else {
					invalid++
				}
			default:
				invalid++
			}
		case "index":
			if index, ok := parseIndexStatement(fields[1:], statement, false); ok {
				indexes = append(indexes, index)
			} else {
				invalid++
			}
		default:
			unsupported++
		}
	}
	return tables, indexes, partitions, unsupported, invalid
}

func splitStatements(sql string) []string {
	stripped := stripLineComments(sql)
	parts := strings.Split(stripped, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseTableStatement returns either a TableEvidence (kind="table") or a
// PartitionEvidence (kind="partition") depending on whether the statement
// contains a PARTITION OF clause.
func parseTableStatement(fields []string, raw string) (TableEvidence, PartitionEvidence, bool, string) {
	// fields[0] == "table" already; the next token may be "if" "not" "exists" "<name>".
	cursor := 1
	if cursor+2 < len(fields) && strings.EqualFold(fields[cursor], "if") && strings.EqualFold(fields[cursor+1], "not") && strings.EqualFold(fields[cursor+2], "exists") {
		cursor += 3
	}
	if cursor >= len(fields) {
		return TableEvidence{}, PartitionEvidence{}, false, "table"
	}
	name := cleanSQLIdentifier(fields[cursor])
	if name == "" {
		return TableEvidence{}, PartitionEvidence{}, false, "table"
	}
	rest := strings.Join(fields[cursor+1:], " ")
	rest = strings.TrimSpace(rest)
	upperRest := strings.ToUpper(rest)
	if strings.Contains(upperRest, "PARTITION OF") {
		// Treat as partition child. Pull the parent name following PARTITION OF.
		if parent := parsePartitionParent(rest); parent != "" {
			return TableEvidence{}, PartitionEvidence{Name: name, Parent: parent}, true, "partition"
		}
		return TableEvidence{}, PartitionEvidence{}, false, "partition"
	}

	table := TableEvidence{Name: name}
	if open := strings.Index(rest, "("); open >= 0 {
		body, partition, ok := splitColumnsAndPartition(rest[open+1:])
		if !ok {
			return TableEvidence{}, PartitionEvidence{}, false, "table"
		}
		table.Columns, table.PrimaryKeys = parseColumnDefinitions(body)
		if partition != "" {
			table.Partitioned = true
			table.PartitionBy = strings.TrimSpace(partition)
		}
	}
	_ = raw
	return table, PartitionEvidence{}, true, "table"
}

// splitColumnsAndPartition takes the body after the leading `(` and returns
// (column-body, partition-by, ok). The matching close `)` is found via a
// simple paren counter.
func splitColumnsAndPartition(body string) (string, string, bool) {
	depth := 1
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closing := body[:i]
				rest := body[i+1:]
				upperRest := strings.ToUpper(rest)
				if idx := strings.Index(upperRest, "PARTITION BY"); idx >= 0 {
					return closing, strings.TrimSpace(rest[idx+len("PARTITION BY"):]), true
				}
				return closing, "", true
			}
		}
	}
	return "", "", false
}

func parseColumnDefinitions(body string) ([]TableColumn, []string) {
	parts := splitTopLevel(body, ',')
	columns := make([]TableColumn, 0, len(parts))
	primaryKeys := []string{}
	for _, raw := range parts {
		def := strings.TrimSpace(raw)
		if def == "" {
			continue
		}
		upper := strings.ToUpper(def)
		switch {
		case strings.HasPrefix(upper, "PRIMARY KEY"):
			primaryKeys = append(primaryKeys, extractParenIdentifiers(def)...)
			continue
		case strings.HasPrefix(upper, "UNIQUE"), strings.HasPrefix(upper, "FOREIGN KEY"), strings.HasPrefix(upper, "CHECK"), strings.HasPrefix(upper, "CONSTRAINT"):
			continue
		}
		fields := strings.Fields(def)
		if len(fields) < 2 {
			continue
		}
		name := cleanSQLIdentifier(fields[0])
		if name == "" {
			continue
		}
		columnType := strings.ToUpper(fields[1])
		// Allow types with size like VARCHAR(255). Append parens if present in next tokens.
		columns = append(columns, TableColumn{Name: name, Type: columnType})
		if strings.Contains(strings.ToUpper(def), "PRIMARY KEY") {
			primaryKeys = append(primaryKeys, name)
		}
	}
	return columns, dedupeStrings(primaryKeys)
}

func parsePartitionParent(rest string) string {
	upper := strings.ToUpper(rest)
	idx := strings.Index(upper, "PARTITION OF")
	if idx < 0 {
		return ""
	}
	tail := strings.TrimSpace(rest[idx+len("PARTITION OF"):])
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return ""
	}
	return cleanSQLIdentifier(fields[0])
}

// parseIndexStatement extracts the index, table, and column list from a
// CREATE [UNIQUE] INDEX statement. fields starts at the "index" keyword.
func parseIndexStatement(fields []string, raw string, unique bool) (IndexEvidence, bool) {
	if len(fields) < 4 || !strings.EqualFold(fields[0], "index") {
		return IndexEvidence{}, false
	}
	cursor := 1
	if cursor+2 < len(fields) && strings.EqualFold(fields[cursor], "if") && strings.EqualFold(fields[cursor+1], "not") && strings.EqualFold(fields[cursor+2], "exists") {
		cursor += 3
	}
	if cursor >= len(fields) {
		return IndexEvidence{}, false
	}
	name := cleanSQLIdentifier(fields[cursor])
	if name == "" {
		return IndexEvidence{}, false
	}
	cursor++
	if cursor >= len(fields) || !strings.EqualFold(fields[cursor], "on") {
		return IndexEvidence{}, false
	}
	cursor++
	if cursor >= len(fields) {
		return IndexEvidence{}, false
	}
	// The next token may be "table_name(col1,col2)" with no whitespace before
	// the open paren, so trim everything from the first "(".
	tableToken := fields[cursor]
	if open := strings.Index(tableToken, "("); open >= 0 {
		tableToken = tableToken[:open]
	}
	target := cleanSQLIdentifier(tableToken)
	if target == "" {
		return IndexEvidence{}, false
	}
	columns := extractParenIdentifiers(raw)
	return IndexEvidence{Name: name, Table: target, Columns: columns, Unique: unique}, true
}

// extractParenIdentifiers pulls comma-separated identifiers out of the
// first parenthesized group in the statement, ignoring nested parens.
func extractParenIdentifiers(statement string) []string {
	open := strings.Index(statement, "(")
	if open < 0 {
		return nil
	}
	depth := 0
	end := -1
	for i := open; i < len(statement); i++ {
		switch statement[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	body := statement[open+1 : end]
	parts := splitTopLevel(body, ',')
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		first := strings.TrimSpace(part)
		if first == "" {
			continue
		}
		fields := strings.Fields(first)
		identifier := cleanSQLIdentifier(fields[0])
		if identifier == "" {
			continue
		}
		out = append(out, identifier)
	}
	return out
}

// splitTopLevel splits the body on the supplied separator while respecting
// parenthesis depth.
func splitTopLevel(body string, sep rune) []string {
	depth := 0
	var (
		out   []string
		start int
	)
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	if start < len(body) {
		out = append(out, body[start:])
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
