package main

import (
	"strings"
	"testing"
)

// scanRel is the path every scanner unit test attributes its source to. Using
// a .tsx path keeps JSX parsing on for the fixtures that need it.
const scanRel = "web-next/src/components/Fixture.tsx"

// findingWords returns the banned words the scanner reported, so assertions can
// talk about vocabulary rather than message formatting.
func findingWords(findings []copyFinding) []string {
	words := make([]string, 0, len(findings))
	for _, finding := range findings {
		words = append(words, finding.word)
	}
	return words
}

func containsWord(findings []copyFinding, word string) bool {
	for _, finding := range findings {
		if finding.word == word {
			return true
		}
	}
	return false
}

func TestScanProductCopyFlagsJSXText(t *testing.T) {
	src := `export function Panel() {
  return <p className="text-xs">No durable occurrences have been recorded yet.</p>;
}
`
	findings := scanProductCopy(scanRel, src)
	if !containsWord(findings, "durable") {
		t.Fatalf("findings = %v, want a durable finding", findingWords(findings))
	}
	if findings[0].line != 2 {
		t.Fatalf("line = %d, want 2", findings[0].line)
	}
	if findings[0].snippet != "No durable occurrences have been recorded yet." {
		t.Fatalf("snippet = %q", findings[0].snippet)
	}
}

func TestScanProductCopyFlagsVisibleAttributes(t *testing.T) {
	src := `export const tile = <Stat label="Operations" value={count} />;
`
	findings := scanProductCopy(scanRel, src)
	if !containsWord(findings, "operations") {
		t.Fatalf("findings = %v, want an operations finding", findingWords(findings))
	}
}

func TestScanProductCopyFlagsObjectKeysAndErrors(t *testing.T) {
	src := `const tile = { detail: "Logical volumes admitted by the control plane." };
function guard() {
  throw new Error("A valid domain idempotency key is required.");
}
`
	findings := scanProductCopy(scanRel, src)
	if !containsWord(findings, "admitted") {
		t.Fatalf("findings = %v, want an admitted finding", findingWords(findings))
	}
	if !containsWord(findings, "idempotency") {
		t.Fatalf("findings = %v, want an idempotency finding", findingWords(findings))
	}
}

func TestScanProductCopyIgnoresIdentifiersHeadersAndPaths(t *testing.T) {
	src := `import { useOperation } from "@/api/hooks";
const headers = { "Idempotency-Key": key };
const url = "/environments/" + id + "/changeset";
const queryKey = ["environment-changeset", environmentId];
const truthSource = "canonical-github-link";
export function useOperationLogs(operationId: string) {
  return operationId;
}
`
	if findings := scanProductCopy(scanRel, src); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findingWords(findings))
	}
}

func TestScanProductCopyIgnoresCommentsAndDiagnostics(t *testing.T) {
	src := `// ServiceSettings — durable, environment-scoped desired deployment config.
/* Operation evidence is recorded elsewhere. */
function guard() {
  console.error("midnight: refusing the write without a valid idempotency key");
  throw new Error("Something went wrong preparing that request. Please try again.", {
    cause: "invalid domain idempotency key",
  });
}
`
	if findings := scanProductCopy(scanRel, src); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findingWords(findings))
	}
}

func TestScanProductCopyIgnoresTypeArgumentsAndComparisons(t *testing.T) {
	src := `const [state, setState] = useState<OperationRecord | null>(null);
const list: Array<Changeset> = [];
// The user hasn't scrolled up; the durable revision is irrelevant here.
if (count < total && total > 0) {
  report(state);
}
`
	if findings := scanProductCopy(scanRel, src); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findingWords(findings))
	}
}

func TestProductCopyScannableExcludesTestsAndDocsPages(t *testing.T) {
	cases := map[string]bool{
		"web-next/src/components/panels/DomainList.tsx":      true,
		"web-next/src/components/panels/domains/api.ts":      true,
		"web-next/src/components/panels/DomainList.test.tsx": false,
		"web-next/src/api/client.spec.ts":                    false,
		"web-next/src/pages/docs/APIReferenceDocs.tsx":       false,
		"web-next/src/vite-env.d.ts":                         false,
		"web-next/src/styles/tokens.css":                     false,
	}
	for rel, want := range cases {
		if got := productCopyScannable(rel); got != want {
			t.Errorf("productCopyScannable(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestCheckProductCopyVocabularyReportsAndAllowsExactSnippets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "web-next/src/components/Bad.tsx",
		`export const bad = <Stat label="Operations" value={n} />;`+"\n")
	writeFile(t, root, "web-next/src/pages/docs/Teach.tsx",
		`export const doc = <p>An operation proves progress for a durable changeset.</p>;`+"\n")

	failures := checkProductCopyVocabulary(root)
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	msg := failures[0].Error()
	if !strings.Contains(msg, "web-next/src/components/Bad.tsx:1") {
		t.Fatalf("message = %q, want the Bad.tsx location", msg)
	}
	if !strings.Contains(msg, "activity, rollouts, builds, or deployments") {
		t.Fatalf("message = %q, want the replacement guidance", msg)
	}
	if !strings.Contains(msg, productCopyAllowlistFile) {
		t.Fatalf("message = %q, want the allowlist path", msg)
	}

	writeFile(t, root, productCopyAllowlistFile,
		"# deliberate\nweb-next/src/components/Bad.tsx | Operations\n")
	if failures := checkProductCopyVocabulary(root); len(failures) != 0 {
		t.Fatalf("failures = %v, want none once allowlisted", failures)
	}
}

func TestCheckProductCopyVocabularyRejectsStaleAllowlistEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "web-next/src/components/Good.tsx",
		`export const good = <Stat label="Activity" value={n} />;`+"\n")
	writeFile(t, root, productCopyAllowlistFile,
		"web-next/src/components/Good.tsx | Operations\n")

	failures := checkProductCopyVocabulary(root)
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one stale-entry failure", failures)
	}
	if !strings.Contains(failures[0].Error(), "no longer matches") {
		t.Fatalf("message = %q, want the stale-entry diagnostic", failures[0].Error())
	}
}

func TestCheckProductCopyVocabularyRejectsMalformedAllowlist(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "web-next/src/components/Good.tsx", "export const good = 1;\n")
	writeFile(t, root, productCopyAllowlistFile, "web-next/src/components/Good.tsx Operations\n")

	failures := checkProductCopyVocabulary(root)
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one parse failure", failures)
	}
	if !strings.Contains(failures[0].Error(), "expected `<path> | <snippet>`") {
		t.Fatalf("message = %q, want the format diagnostic", failures[0].Error())
	}
}

func TestCheckProductCopyVocabularySkipsMissingConsoleTree(t *testing.T) {
	root := t.TempDir()
	if failures := checkProductCopyVocabulary(root); len(failures) != 0 {
		t.Fatalf("failures = %v, want none for a Go-only checkout", failures)
	}
}

func TestRunRejectsProductCopyVocabulary(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	// The scaffold's CODEOWNERS has no rule for the console tree this test
	// creates, and an unowned top-level directory is a scaffold failure that
	// would win the reason code.
	writeFile(t, root, "CODEOWNERS", "* @midnight/maintainers\n/cmd/ @midnight/platform-core\n/internal/ @midnight/platform-core\n/api/ @midnight/platform-core\n/deploy/ @midnight/install-ops-quality\n/test/ @midnight/install-ops-quality\n/tools/ @midnight/dx\n/docs/ @midnight/docs\n/web-next/ @midnight/console\n")
	writeFile(t, root, "web-next/src/components/Bad.tsx",
		`export const bad = <p>The durable changeset is applied.</p>;`+"\n")

	var stdout, stderr strings.Builder
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	got := stderr.String()
	if !strings.Contains(got, "product-copy guard:") {
		t.Fatalf("stderr = %q, want a product-copy guard diagnostic", got)
	}
	if !strings.Contains(got, "lint reason: LINT_PRODUCT_COPY_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_PRODUCT_COPY_GUARD_FAILED reason", got)
	}
}
