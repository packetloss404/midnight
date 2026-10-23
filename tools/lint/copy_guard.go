package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// productCopyScanRoot is the console source tree whose user-visible strings the
// product-copy guard reads. Only this tree is scanned: Go sources, marketing
// pages, and generated assets have their own review paths.
const productCopyScanRoot = "web-next/src"

// productCopyAllowlistFile is the explicit register of user-visible strings
// that are allowed to keep implementation vocabulary. Every entry names the
// file and the exact snippet, so a reworded string retires its own exemption
// instead of silently widening the hole.
const productCopyAllowlistFile = "tools/lint/product_copy_allowlist.txt"

// productCopyExcludedPrefixes lists repo-relative path prefixes that are
// exempt by design. The in-app documentation teaches the control-plane model,
// so `operation`, `changeset`, and `idempotency` are the subject matter there
// rather than leaked jargon.
var productCopyExcludedPrefixes = []string{
	"web-next/src/pages/docs/",
}

// productCopyBannedWords is the implementation vocabulary that must not reach
// primary UI copy. The list mirrors the "Avoid in primary UI" column of
// docs/railway-ux-9-of-10-target-2026-09-01.md.
var productCopyBannedWords = []string{
	"admitted",
	"canonical",
	"changeset",
	"changesets",
	"durable",
	"durably",
	"evidence",
	"idempotency",
	"idempotent",
	"intake",
	"operation",
	"operations",
}

// productCopyReplacements is the author-facing translation table printed with
// each violation so the fix does not require opening the design document.
var productCopyReplacements = map[string]string{
	"admitted":    "connected, allowed, or available",
	"canonical":   "current, or drop the qualifier",
	"changeset":   "draft or changes",
	"changesets":  "drafts or changes",
	"durable":     "saved, stored, or drop the qualifier",
	"durably":     "drop the qualifier",
	"evidence":    "status, or what was actually reported",
	"idempotency": "reword the message for a reader (keep the detail in console.error or an error cause)",
	"idempotent":  "reword the message for a reader",
	"intake":      "accepted, or say what happened",
	"operation":   "activity, rollout, build, deploy, or restore",
	"operations":  "activity, rollouts, builds, or deployments",
}

// productCopyVisibleKeys are the prop and object-literal keys whose string
// value is rendered to a person. A single-word literal in one of these
// positions is user-visible even though it does not read as prose.
var productCopyVisibleKeys = []string{
	"alt",
	"aria-label",
	"caption",
	"description",
	"detail",
	"empty",
	"heading",
	"hint",
	"label",
	"message",
	"placeholder",
	"reason",
	"subtitle",
	"summary",
	"text",
	"title",
	"tooltip",
}

// productCopyDiagnosticPrefixes are call and key contexts whose string
// argument is developer-facing by construction. Debug detail is deliberately
// preserved there when a user-facing message is reworded, so the guard must
// not push authors to delete it.
var productCopyDiagnosticPrefixes = []string{
	"console.log(",
	"console.info(",
	"console.warn(",
	"console.error(",
	"console.debug(",
	"console.trace(",
	"cause:",
}

// copyFinding is one user-visible string that still carries implementation
// vocabulary, located precisely enough for an author to fix it.
type copyFinding struct {
	relPath string
	line    int
	word    string
	snippet string
}

// allowlistEntry is one parsed exemption. `used` records whether the scan
// actually matched it so stale entries can be reported and removed.
type allowlistEntry struct {
	relPath string
	snippet string
	line    int
	used    bool
}

// checkProductCopyVocabulary scans the console source tree for implementation
// vocabulary in strings a person reads, and reports both new violations and
// allowlist entries that no longer match anything. It deliberately uses a
// hand-rolled scanner rather than a TypeScript parser: the guard runs on every
// `make lint` and must stay dependency-free.
func checkProductCopyVocabulary(root string) []error {
	scanRoot := filepath.Join(root, filepath.FromSlash(productCopyScanRoot))
	info, err := os.Stat(scanRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// Go-only checkouts and lint test fixtures have no console
			// tree; that is not a copy failure.
			return nil
		}
		return []error{fmt.Errorf("stat %s: %w", productCopyScanRoot, err)}
	}
	if !info.IsDir() {
		return nil
	}

	allowlist, err := loadProductCopyAllowlist(root)
	if err != nil {
		return []error{err}
	}

	var findings []copyFinding
	walkErr := filepath.Walk(scanRoot, func(path string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "node_modules" || name == "dist" || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !productCopyScannable(rel) {
			return nil
		}
		data, readErr := readNormalizedFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", rel, readErr)
		}
		findings = append(findings, scanProductCopy(rel, string(data))...)
		return nil
	})
	if walkErr != nil {
		return []error{fmt.Errorf("scan %s: %w", productCopyScanRoot, walkErr)}
	}

	var failures []error
	for _, finding := range findings {
		if matchAllowlist(allowlist, finding) {
			continue
		}
		guidance, ok := productCopyReplacements[finding.word]
		if !ok {
			guidance = "use the reader's word for it"
		}
		failures = append(failures, fmt.Errorf(
			"%s:%d: user-visible copy says %q; write %s instead. Reword it, or if this string genuinely names an API operation the reader is looking at, add the exact snippet to %s: %s",
			finding.relPath, finding.line, finding.word, guidance, productCopyAllowlistFile, finding.snippet,
		))
	}
	for _, entry := range allowlist {
		if entry.used {
			continue
		}
		failures = append(failures, fmt.Errorf(
			"%s:%d: allowlist entry no longer matches any user-visible string in %s; delete the line",
			productCopyAllowlistFile, entry.line, entry.relPath,
		))
	}
	sort.Slice(failures, func(i, j int) bool {
		return failures[i].Error() < failures[j].Error()
	})
	return failures
}

// productCopyScannable reports whether a repo-relative path holds shipped
// console copy. Tests, type declarations, and the in-app docs pages are out of
// scope.
func productCopyScannable(rel string) bool {
	if !strings.HasSuffix(rel, ".ts") && !strings.HasSuffix(rel, ".tsx") {
		return false
	}
	if strings.HasSuffix(rel, ".d.ts") {
		return false
	}
	base := filepath.Base(rel)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return false
	}
	if strings.Contains(rel, "/__tests__/") {
		return false
	}
	for _, prefix := range productCopyExcludedPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return false
		}
	}
	return true
}

// loadProductCopyAllowlist parses the exemption register. Each non-comment
// line is `<repo-relative path> | <exact snippet>`.
func loadProductCopyAllowlist(root string) ([]*allowlistEntry, error) {
	path := filepath.Join(root, filepath.FromSlash(productCopyAllowlistFile))
	data, err := readNormalizedFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", productCopyAllowlistFile, err)
	}
	var entries []*allowlistEntry
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf(
				"%s:%d: expected `<path> | <snippet>`, got %q",
				productCopyAllowlistFile, i+1, line,
			)
		}
		entries = append(entries, &allowlistEntry{
			relPath: strings.TrimSpace(parts[0]),
			snippet: normalizeCopyText(parts[1]),
			line:    i + 1,
		})
	}
	return entries, nil
}

// matchAllowlist marks and reports the first exemption covering a finding.
func matchAllowlist(entries []*allowlistEntry, finding copyFinding) bool {
	matched := false
	for _, entry := range entries {
		if entry.relPath != finding.relPath {
			continue
		}
		if entry.snippet != finding.snippet {
			continue
		}
		entry.used = true
		matched = true
	}
	return matched
}

// scanProductCopy walks one source file and returns every user-visible string
// that carries implementation vocabulary. The scanner tracks comments, the
// three JavaScript string forms, and JSX elements so it can tell copy from
// code without building a syntax tree. It is deliberately conservative: a
// construct it cannot classify is treated as code, because a false alarm costs
// an author more than a missed string costs the guard.
func scanProductCopy(relPath, src string) []copyFinding {
	sc := &copyScanner{relPath: relPath, runes: []rune(src), line: 1}
	sc.run()
	return sc.findings
}

// copyScanner holds the cursor for one file. Methods advance `pos` and keep
// `line` accurate so every finding can be reported at a real location.
type copyScanner struct {
	relPath  string
	runes    []rune
	pos      int
	line     int
	findings []copyFinding
}

// run drives the top-level scan: comments and code are skipped, string
// literals are classified by the context that precedes them, and a JSX tag
// hands off to the element scanner.
func (s *copyScanner) run() {
	for s.pos < len(s.runes) {
		c := s.runes[s.pos]
		switch {
		case c == '\n':
			s.line++
			s.pos++
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case c == '"' || c == '\'' || c == '`':
			s.scanStringLiteral()
		case c == '<' && s.opensJSXTag():
			s.scanJSXElement()
		default:
			s.pos++
		}
	}
}

// peek returns the rune `offset` positions ahead, or 0 past the end.
func (s *copyScanner) peek(offset int) rune {
	if s.pos+offset >= len(s.runes) {
		return 0
	}
	return s.runes[s.pos+offset]
}

func (s *copyScanner) skipLineComment() {
	for s.pos < len(s.runes) && s.runes[s.pos] != '\n' {
		s.pos++
	}
}

func (s *copyScanner) skipBlockComment() {
	s.pos += 2
	for s.pos+1 < len(s.runes) && !(s.runes[s.pos] == '*' && s.runes[s.pos+1] == '/') {
		if s.runes[s.pos] == '\n' {
			s.line++
		}
		s.pos++
	}
	s.pos += 2
}

// opensJSXTag reports whether the `<` at the cursor starts a JSX element
// rather than a comparison or a type parameter list. A type argument list
// always follows an identifier (`useState<string>`, `Array<Item>`), while a
// JSX tag never does, so the preceding character settles it.
func (s *copyScanner) opensJSXTag() bool {
	next := s.peek(1)
	if next != '/' && next != '>' && !unicode.IsLetter(next) {
		return false
	}
	if s.pos == 0 {
		return true
	}
	prev := s.runes[s.pos-1]
	if unicode.IsLetter(prev) || unicode.IsDigit(prev) {
		return false
	}
	switch prev {
	case '_', '$', ')', ']', '>':
		return false
	}
	return true
}

// scanStringLiteral consumes one literal and records findings when the literal
// is copy: either it sits in a prop or object key that renders to a person, or
// it reads like a sentence. Console arguments and error causes are skipped so
// authors can keep developer detail alongside a reworded user message.
func (s *copyScanner) scanStringLiteral() {
	startLine := s.line
	prefix := precedingContext(s.runes, s.pos)
	body := s.consumeStringLiteral()
	if isDiagnosticContext(prefix) {
		return
	}
	if isVisibleKeyContext(prefix) || isProse(body) {
		s.findings = append(s.findings, copyFindingsFor(s.relPath, startLine, body)...)
	}
}

// consumeStringLiteral advances past the literal at the cursor and returns its
// text. Template substitutions collapse to a space so a `${expr}` never becomes
// part of the copy.
func (s *copyScanner) consumeStringLiteral() string {
	quote := s.runes[s.pos]
	var body strings.Builder
	s.pos++
	for s.pos < len(s.runes) {
		c := s.runes[s.pos]
		switch {
		case c == '\\':
			s.pos += 2
		case c == quote:
			s.pos++
			return body.String()
		case c == '\n':
			s.line++
			body.WriteRune(' ')
			s.pos++
		case quote == '`' && c == '$' && s.peek(1) == '{':
			s.skipBracedExpression()
			body.WriteRune(' ')
		default:
			body.WriteRune(c)
			s.pos++
		}
	}
	return body.String()
}

// skipBracedExpression advances past a balanced `{...}` run, counting lines and
// respecting nested strings so a brace inside a literal does not unbalance it.
func (s *copyScanner) skipBracedExpression() {
	for s.pos < len(s.runes) && s.runes[s.pos] != '{' {
		s.pos++
	}
	depth := 0
	for s.pos < len(s.runes) {
		c := s.runes[s.pos]
		switch {
		case c == '\n':
			s.line++
			s.pos++
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case c == '"' || c == '\'' || c == '`':
			s.scanStringLiteral()
		case c == '{':
			depth++
			s.pos++
		case c == '}':
			depth--
			s.pos++
			if depth == 0 {
				return
			}
		default:
			s.pos++
		}
	}
}

// scanJSXElement consumes one tag, scanning its attribute literals, and then
// reads the text that follows it up to the next tag.
func (s *copyScanner) scanJSXElement() {
	s.pos++ // past '<'
	for s.pos < len(s.runes) {
		c := s.runes[s.pos]
		switch {
		case c == '\n':
			s.line++
			s.pos++
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case c == '"' || c == '\'' || c == '`':
			s.scanStringLiteral()
		case c == '{':
			s.skipBracedExpression()
		case c == '>':
			s.pos++
			s.scanJSXText()
			return
		default:
			s.pos++
		}
	}
}

// scanJSXText reads the run of characters between a closing `>` and the next
// `<`, dropping `{...}` interpolations, and records findings when what remains
// reads as prose.
func (s *copyScanner) scanJSXText() {
	startLine := s.line
	var body strings.Builder
	for s.pos < len(s.runes) {
		c := s.runes[s.pos]
		switch {
		case c == '<':
			s.emitJSXText(startLine, body.String())
			return
		case c == '\n':
			s.line++
			body.WriteRune(' ')
			s.pos++
		case c == '{':
			s.skipBracedExpression()
			body.WriteRune(' ')
		default:
			body.WriteRune(c)
			s.pos++
		}
	}
	s.emitJSXText(startLine, body.String())
}

// emitJSXText records findings for a JSX text run.
func (s *copyScanner) emitJSXText(line int, text string) {
	if isJSXProse(text) {
		s.findings = append(s.findings, copyFindingsFor(s.relPath, line, text)...)
	}
}

// precedingContext returns the compacted code immediately before a literal,
// which is what tells a `label=` position apart from an ordinary expression.
func precedingContext(runes []rune, start int) string {
	from := start - 64
	if from < 0 {
		from = 0
	}
	return strings.TrimSpace(strings.Join(strings.Fields(string(runes[from:start])), " "))
}

// isDiagnosticContext reports whether a literal is a console argument or an
// error `cause`. Those are read by developers, never rendered.
func isDiagnosticContext(prefix string) bool {
	for _, marker := range productCopyDiagnosticPrefixes {
		if strings.HasSuffix(prefix, marker) {
			return true
		}
	}
	return false
}

// isVisibleKeyContext reports whether a literal sits directly after a prop or
// object key whose value is shown to a person.
func isVisibleKeyContext(prefix string) bool {
	trimmed := strings.TrimRight(prefix, " ")
	if !strings.HasSuffix(trimmed, "=") && !strings.HasSuffix(trimmed, ":") {
		return false
	}
	trimmed = strings.TrimSpace(strings.TrimRight(strings.TrimRight(trimmed, "=:"), " "))
	if trimmed == "" {
		return false
	}
	fields := strings.Fields(trimmed)
	key := strings.ToLower(fields[len(fields)-1])
	key = strings.TrimLeft(key, "{[(,")
	for _, candidate := range productCopyVisibleKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

// isProse reports whether a string literal reads like a sentence rather than
// an identifier, header name, URL, or query key. Two words and a lowercase run
// is enough signal, and it keeps `"Idempotency-Key"` and
// `"environment-changeset"` out of the scan.
func isProse(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.Contains(trimmed, " ") {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "http") {
		return false
	}
	words := 0
	for _, field := range strings.Fields(trimmed) {
		if hasLowerRun(field, 3) {
			words++
		}
	}
	return words >= 2
}

// isJSXProse applies the prose test to a JSX text run and first rejects
// anything carrying code punctuation. A `<` that opened a type parameter list
// rather than an element can leave real code in this position, so the guard
// would rather skip a sentence than accuse an author of bad copy in a
// generic.
func isJSXProse(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, bad := range []string{"=", "&", "|", ";", "$", "`", ":", "?", "{", "}", "[", "]", "//", "/*"} {
		if strings.Contains(trimmed, bad) {
			return false
		}
	}
	if hasCallSyntax(trimmed) {
		return false
	}
	return isProse(trimmed)
}

// hasCallSyntax reports whether the text contains an identifier immediately
// followed by `(`, which is a function call rather than a prose parenthetical.
func hasCallSyntax(text string) bool {
	runes := []rune(text)
	for i := 1; i < len(runes); i++ {
		if runes[i] != '(' {
			continue
		}
		prev := runes[i-1]
		if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' {
			return true
		}
	}
	return false
}

// hasLowerRun reports whether a token contains a run of at least n consecutive
// lowercase letters, which distinguishes an English word from `svc_01H`.
func hasLowerRun(token string, n int) bool {
	run := 0
	for _, r := range token {
		if unicode.IsLower(r) {
			run++
			if run >= n {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// copyFindingsFor reports one finding per distinct banned word in a string.
func copyFindingsFor(relPath string, line int, text string) []copyFinding {
	normalized := normalizeCopyText(text)
	if normalized == "" {
		return nil
	}
	seen := map[string]bool{}
	var findings []copyFinding
	for _, word := range wordsOf(normalized) {
		if !isBannedCopyWord(word) || seen[word] {
			continue
		}
		seen[word] = true
		findings = append(findings, copyFinding{
			relPath: relPath,
			line:    line,
			word:    word,
			snippet: normalized,
		})
	}
	return findings
}

// isBannedCopyWord reports whether a lowercased word is implementation
// vocabulary.
func isBannedCopyWord(word string) bool {
	for _, banned := range productCopyBannedWords {
		if word == banned {
			return true
		}
	}
	return false
}

// wordsOf splits normalized copy into lowercase alphabetic words. Hyphens and
// punctuation are separators, so `operation-scoped` still yields `operation`.
func wordsOf(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	return fields
}

// normalizeCopyText collapses whitespace so a snippet reported by the scanner
// and a snippet pasted into the allowlist compare equal regardless of how the
// source wrapped it.
func normalizeCopyText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
