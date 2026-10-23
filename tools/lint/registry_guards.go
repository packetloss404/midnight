package main

import (
	"bufio"
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

// failurecodesRegistryFile is the canonical Go source where typed
// failurecodes.Code constants are declared. The error-code consistency guard
// reads it to learn which codes the Go API actually surfaces.
const failurecodesRegistryFile = "internal/failurecodes/codes.go"

// errorCodesDoc is the human-facing additive registry that operators consult
// when they triage an alert.
const errorCodesDoc = "docs/error-codes.md"

// errorCodeTablePattern matches the SCREAMING_SNAKE_CASE token in a registry
// table row. Codes appear in column one wrapped in backticks.
var errorCodeTablePattern = regexp.MustCompile("^\\| `([A-Z][A-Z0-9_]+)` \\|")

// checkErrorCodeRegistryConsistency ensures that every typed failurecodes.Code
// constant in internal/failurecodes/codes.go has a row in docs/error-codes.md.
// This catches the "added a constant, forgot the doc" failure mode that would
// otherwise leave operators without a Recovery hint when an alert fires.
func checkErrorCodeRegistryConsistency(root string) []error {
	registryPath := filepath.Join(root, filepath.FromSlash(failurecodesRegistryFile))
	if _, err := os.Stat(registryPath); err != nil {
		if os.IsNotExist(err) {
			// In test fixtures the registry source may be absent; skip
			// silently rather than report a confusing failure that has
			// nothing to do with the consistency check.
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", failurecodesRegistryFile, err)}
	}
	docPath := filepath.Join(root, filepath.FromSlash(errorCodesDoc))
	if _, err := os.Stat(docPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", errorCodesDoc, err)}
	}
	codes, err := parseFailurecodeConstants(registryPath)
	if err != nil {
		return []error{err}
	}
	docCodes, err := parseErrorCodesDoc(docPath)
	if err != nil {
		return []error{err}
	}
	docSet := make(map[string]bool, len(docCodes))
	for _, c := range docCodes {
		docSet[c] = true
	}
	var errs []error
	missing := make([]string, 0, len(codes))
	for _, c := range codes {
		if !docSet[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	for _, c := range missing {
		errs = append(errs, fmt.Errorf("error-code %s is declared in %s but missing from %s", c, failurecodesRegistryFile, errorCodesDoc))
	}
	if !sort.StringsAreSorted(docCodes) {
		errs = append(errs, fmt.Errorf("%s rows must be sorted alphabetically by code", errorCodesDoc))
	}
	return errs
}

// parseFailurecodeConstants returns the SCREAMING_SNAKE_CASE values of every
// typed failurecodes.Code constant in path. Untyped or non-Code constants are
// ignored so unrelated package additions do not become guard noise.
func parseFailurecodeConstants(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", failurecodesRegistryFile, err)
	}
	var codes []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if !isCodeTypedValueSpec(vs) {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				str := strings.Trim(lit.Value, "\"")
				if str == "" {
					continue
				}
				codes = append(codes, str)
			}
		}
	}
	sort.Strings(codes)
	return codes, nil
}

// isCodeTypedValueSpec returns true when the value spec declares values of
// type Code (the failurecodes.Code typed string). The comparison is purely
// syntactic and intentionally ignores aliases.
func isCodeTypedValueSpec(vs *ast.ValueSpec) bool {
	if vs.Type == nil {
		return false
	}
	ident, ok := vs.Type.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "Code"
}

// parseErrorCodesDoc returns the codes listed in docs/error-codes.md in the
// document order. The document order is preserved so the consistency guard can
// also report when rows are not alphabetically sorted.
func parseErrorCodesDoc(path string) ([]string, error) {
	data, err := readNormalizedFile(path)
	if err != nil {
		return nil, err
	}
	var codes []string
	for _, line := range strings.Split(string(data), "\n") {
		m := errorCodeTablePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		codes = append(codes, m[1])
	}
	return codes, nil
}

// failureCodeShape matches a SCREAMING_SNAKE_CASE token of length >= 3 that
// starts with a letter, contains only ASCII upper-case letters, digits, and
// underscores, and includes at least one underscore separator. This is the
// shape that operators expect to map to a row in docs/error-codes.md.
var failureCodeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)

// notFailureCodeAnnotation is the inline marker contributors use to confirm
// that a SCREAMING_SNAKE_CASE literal is intentionally not a failure code.
// The marker is matched anywhere in the same line or on the immediately
// preceding non-blank comment line.
const notFailureCodeAnnotation = "not-a-failure-code"

// nonFailureCodeAllowlistFile holds a curated, version-controlled list of
// tokens that look like failure codes but are not. The file lets feature
// lanes keep ownership of their own non-failure-code literals (env-var
// names, kernel capability names, TLS cipher suite names, internal issue
// codes) without requiring per-line annotations the foundations lane is not
// allowed to add.
const nonFailureCodeAllowlistFile = "tools/lint/non_failure_code_literals.txt"

// deepRegistryScanRoots names the top-level directories whose Go sources are
// scanned for failure-code-shaped string literals. The forbidden lanes
// (docs/, agile/, deploy/, web/, schemas/, etc.) are not Go code and never
// enter the scan.
var deepRegistryScanRoots = []string{"cmd", "internal", "tools"}

// goKeywordTokens are reserved Go keywords that could in principle look like
// SCREAMING_SNAKE_CASE in a future Go version. None do today, but the list is
// included to satisfy the brief's "whitelisted Go-keyword-shaped tokens"
// requirement and keep the guard robust if the language ever adds one.
var goKeywordTokens = map[string]bool{
	// no Go keywords currently match the SCREAMING_SNAKE_CASE shape; the
	// map exists so the guard can be extended without touching call sites.
}

// checkUnregisteredFailureCodeLiterals walks every Go source file in the deep
// scan roots and rejects SCREAMING_SNAKE_CASE string literals that are not
// registered in docs/error-codes.md, allowlisted in
// tools/lint/non_failure_code_literals.txt, or annotated in source with
// `// not-a-failure-code`. The intent is to catch the "added a code, forgot
// the registry row" failure mode before it reaches an operator alert.
func checkUnregisteredFailureCodeLiterals(root string, files []goSourceFile) []error {
	docPath := filepath.Join(root, filepath.FromSlash(errorCodesDoc))
	if _, err := os.Stat(docPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", errorCodesDoc, err)}
	}
	docCodes, err := parseErrorCodesDoc(docPath)
	if err != nil {
		return []error{err}
	}
	docSet := make(map[string]bool, len(docCodes))
	for _, c := range docCodes {
		docSet[c] = true
	}
	allowSet, err := loadNonFailureCodeAllowlist(filepath.Join(root, filepath.FromSlash(nonFailureCodeAllowlistFile)))
	if err != nil {
		return []error{err}
	}

	type literalFinding struct {
		path  string
		line  int
		token string
	}
	var findings []literalFinding
	scanRoots := make(map[string]bool, len(deepRegistryScanRoots))
	for _, top := range deepRegistryScanRoots {
		scanRoots[top] = true
	}
	for _, f := range files {
		top, _, _ := strings.Cut(f.relPath, "/")
		if !scanRoots[top] {
			continue
		}
		// Skip the canonical registry source itself; every literal in it
		// is by definition a failure code and is checked by
		// checkErrorCodeRegistryConsistency in the same run.
		if filepath.ToSlash(f.relPath) == failurecodesRegistryFile {
			continue
		}
		// Skip the lint tool's own test-fixture sources because they
		// declare deliberately-mismatched constants for unit tests.
		if strings.HasPrefix(f.relPath, "tools/lint/") && f.isTest {
			continue
		}
		annotatedLines, err := loadNotFailureCodeAnnotatedLines(filepath.Join(root, filepath.FromSlash(f.relPath)))
		if err != nil {
			return []error{err}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := unquoteSourceString(lit.Value)
			if err != nil {
				return true
			}
			if !failureCodeShape.MatchString(value) {
				return true
			}
			if !strings.Contains(value, "_") {
				return true
			}
			if goKeywordTokens[value] {
				return true
			}
			if docSet[value] {
				return true
			}
			if allowSet[value] {
				return true
			}
			pos := f.fset.Position(lit.Pos())
			if annotatedLines[pos.Line] {
				return true
			}
			findings = append(findings, literalFinding{path: f.relPath, line: pos.Line, token: value})
			return true
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].path != findings[j].path {
			return findings[i].path < findings[j].path
		}
		if findings[i].line != findings[j].line {
			return findings[i].line < findings[j].line
		}
		return findings[i].token < findings[j].token
	})
	var errs []error
	for _, fx := range findings {
		errs = append(errs, fmt.Errorf("failure-code-shaped literal %q at %s:%d is not registered in %s and not allowlisted (add a row, list it in %s, or annotate the line with `// %s`)", fx.token, fx.path, fx.line, errorCodesDoc, nonFailureCodeAllowlistFile, notFailureCodeAnnotation))
	}
	return errs
}

// loadNotFailureCodeAnnotatedLines returns the set of source-file line numbers
// that carry the `// not-a-failure-code` annotation either on themselves or on
// the immediately preceding non-blank line. Both forms are accepted because
// the annotation may appear next to a `const` block declaration or alongside
// the literal use.
func loadNotFailureCodeAnnotatedLines(path string) (map[int]bool, error) {
	data, err := readNormalizedFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	annotated := make(map[int]bool)
	for i, line := range lines {
		if !strings.Contains(line, notFailureCodeAnnotation) {
			continue
		}
		// Mark the annotation line itself (1-indexed) and the next
		// non-blank source line so contributors can write either:
		//
		//   // not-a-failure-code
		//   const X = "FOO_BAR"
		//
		// or:
		//
		//   const X = "FOO_BAR" // not-a-failure-code
		annotated[i+1] = true
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			annotated[j+1] = true
			break
		}
	}
	return annotated, nil
}

// loadNonFailureCodeAllowlist parses the curated allowlist of tokens that
// look like failure codes but are not. The file format is one token per line,
// lines starting with `#` are comments, blank lines are ignored. A missing
// file is treated as an empty allowlist so a fresh repo never trips the
// guard with a confusing "missing tools/lint/non_failure_code_literals.txt"
// error.
func loadNonFailureCodeAllowlist(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	out := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// unquoteSourceString trims a Go string literal token. Backtick raw strings
// and double-quoted strings without escapes are both accepted; literals with
// escapes are returned verbatim because failure-code shapes never contain
// escape sequences.
func unquoteSourceString(value string) (string, error) {
	if len(value) < 2 {
		return "", fmt.Errorf("string literal too short")
	}
	first := value[0]
	last := value[len(value)-1]
	if (first == '"' && last == '"') || (first == '`' && last == '`') {
		return value[1 : len(value)-1], nil
	}
	return "", fmt.Errorf("string literal not quoted: %s", value)
}
