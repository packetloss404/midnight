package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// phase0GateDocPath holds the operator-facing reference for the Phase 0
// release gate. The file is hand-maintained and must stay aligned with the
// flag surface declared in tools/phase0-gate/main.go.
const phase0GateDocPath = "docs/governance/phase0-gate.md"

// phase0GateMainPath is the source file from which the canonical flag set is
// re-derived.
const phase0GateMainPath = "tools/phase0-gate/main.go"

// gateDocFlagPattern matches a documented flag in a markdown table cell. The
// gate doc renders flags as `--name=<value>` or `--name`. We extract the
// leading `<name>` token from the first backticked field of each table row.
var gateDocFlagPattern = regexp.MustCompile("^\\| `--([a-z][a-z0-9-]*)")

// checkPhase0GateDocAccuracy parses the documented flag list from
// phase0-gate.md and compares it with the actual flag definitions in
// phase0-gate/main.go. The guard fails when the doc advertises a flag that
// does not exist in main.go (the operator-facing direction); undocumented
// flags are allowed because adding internal-only flags should not require a
// doc revision.
func checkPhase0GateDocAccuracy(root string) []error {
	docPath := filepath.Join(root, filepath.FromSlash(phase0GateDocPath))
	mainPath := filepath.Join(root, filepath.FromSlash(phase0GateMainPath))
	if _, err := os.Stat(docPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", phase0GateDocPath, err)}
	}
	if _, err := os.Stat(mainPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", phase0GateMainPath, err)}
	}
	docFlags, err := parsePhase0GateDocFlags(docPath)
	if err != nil {
		return []error{err}
	}
	codeFlags, err := parsePhase0GateMainFlags(mainPath)
	if err != nil {
		return []error{err}
	}
	codeSet := make(map[string]bool, len(codeFlags))
	for _, f := range codeFlags {
		codeSet[f] = true
	}
	var errs []error
	missing := make([]string, 0, len(docFlags))
	for _, name := range docFlags {
		if !codeSet[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		errs = append(errs, fmt.Errorf("%s documents flag --%s but %s does not register it", phase0GateDocPath, name, phase0GateMainPath))
	}
	return errs
}

// parsePhase0GateDocFlags returns the set of `--<flag>` names appearing as
// table-row keys in the operator-facing reference. The doc convention is one
// flag per row with the flag name as the leading backticked field.
func parsePhase0GateDocFlags(path string) ([]string, error) {
	data, err := readNormalizedFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", phase0GateDocPath, err)
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		m := gateDocFlagPattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// parsePhase0GateMainFlags returns the set of flag names registered against
// the `flag.FlagSet` in tools/phase0-gate/main.go. The walk recognises the
// idiomatic `fs.<Type>Var(&inputs.X, "name", ...)` and `fs.Var(..., "name", ...)`
// forms that the gate uses today.
func parsePhase0GateMainFlags(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", phase0GateMainPath, err)
	}
	seen := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "StringVar", "BoolVar", "IntVar", "Int64Var", "Float64Var", "DurationVar", "Uint64Var", "UintVar":
			if name, ok := stringLiteralArg(call.Args, 1); ok {
				seen[name] = true
			}
		case "Var":
			if name, ok := stringLiteralArg(call.Args, 1); ok {
				seen[name] = true
			}
		case "String", "Bool", "Int", "Int64", "Float64", "Duration", "Uint64", "Uint":
			if name, ok := stringLiteralArg(call.Args, 0); ok {
				seen[name] = true
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// stringLiteralArg returns the unquoted string-literal argument at the given
// index, or false if it is missing or not a basic string literal.
func stringLiteralArg(args []ast.Expr, idx int) (string, bool) {
	if idx >= len(args) {
		return "", false
	}
	lit, ok := args[idx].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := unquoteGoString(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// unquoteGoString unwraps a Go string literal token. We re-implement the
// trim-quote dance rather than depend on strconv.Unquote so that escape
// handling is obvious: the gate flag names never use escapes.
func unquoteGoString(value string) (string, error) {
	if len(value) < 2 {
		return "", fmt.Errorf("string literal too short")
	}
	if value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1], nil
	}
	if value[0] == '`' && value[len(value)-1] == '`' {
		return value[1 : len(value)-1], nil
	}
	return "", fmt.Errorf("string literal not quoted: %s", value)
}
