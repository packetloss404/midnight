package supportbundle

import (
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/redaction"
)

func TestRedactBundleScrubsTextFilesAndCountsMatches(t *testing.T) {
	input := BundleInput{
		BundleID: "bundle-001",
		Files: []BundleInputFile{
			{Path: "logs/api.log", Kind: "log", Content: []byte("operator=ops@midnight.example sent midn_TOKENVALUE12345 from 10.0.0.5"), IsText: true},
			{Path: "logs/clean.log", Kind: "log", Content: []byte("deploy succeeded id=run-001"), IsText: true},
			{Path: "binary/blob.bin", Kind: "binary", Content: []byte{0x00, 0x01, 0x02}, IsText: false},
		},
	}
	out, report, err := RedactBundle(input)
	if err != nil {
		t.Fatalf("RedactBundle() error = %v", err)
	}
	if out.BundleID != "bundle-001" {
		t.Fatalf("BundleOutput.BundleID = %q, want bundle-001", out.BundleID)
	}
	if len(out.Files) != 3 || len(report.Entries) != 3 {
		t.Fatalf("RedactBundle() lengths out=%d, report=%d, want 3 each", len(out.Files), len(report.Entries))
	}

	// Path sorting: binary/blob.bin, logs/api.log, logs/clean.log.
	wantOrder := []string{"binary/blob.bin", "logs/api.log", "logs/clean.log"}
	for i, want := range wantOrder {
		if out.Files[i].Path != want {
			t.Fatalf("RedactBundle() out[%d].Path = %q, want %q", i, out.Files[i].Path, want)
		}
		if report.Entries[i].Path != want {
			t.Fatalf("RedactBundle() report[%d].Path = %q, want %q", i, report.Entries[i].Path, want)
		}
	}

	apiContent := string(out.Files[1].Content)
	for _, leaked := range []string{"ops@midnight.example", "midn_TOKENVALUE12345", "10.0.0.5"} {
		if strings.Contains(apiContent, leaked) {
			t.Fatalf("RedactBundle() leaked %q in api.log content %q", leaked, apiContent)
		}
	}
	if !strings.Contains(apiContent, redaction.Placeholder) {
		t.Fatalf("RedactBundle() api.log content = %q, want to contain redactor placeholder", apiContent)
	}
	if report.Entries[1].MatchCount < 3 {
		t.Fatalf("RedactBundle() api.log MatchCount = %d, want at least 3", report.Entries[1].MatchCount)
	}

	// Clean log: zero matches.
	if report.Entries[2].MatchCount != 0 {
		t.Fatalf("RedactBundle() clean.log MatchCount = %d, want 0", report.Entries[2].MatchCount)
	}

	// Binary file: pass-through, zero matches.
	if report.Entries[0].MatchCount != 0 {
		t.Fatalf("RedactBundle() binary MatchCount = %d, want 0", report.Entries[0].MatchCount)
	}
	if string(out.Files[0].Content) != string([]byte{0x00, 0x01, 0x02}) {
		t.Fatalf("RedactBundle() binary content = %v, want pass-through", out.Files[0].Content)
	}

	if report.TotalMatches < 3 {
		t.Fatalf("RedactBundle() report.TotalMatches = %d, want >= 3", report.TotalMatches)
	}
}

func TestRedactBundleNoLeakGoldenVectors(t *testing.T) {
	// Golden vectors: every input must scrub to text that no longer contains the original secret.
	vectors := []struct {
		name string
		in   string
	}{
		{name: "midn-token", in: "auth=midn_ABCDEF1234567890"},
		{name: "email", in: "from=ops@midnight.example"},
		{name: "ipv4", in: "remote=10.0.0.42"},
		{name: "ipv6", in: "remote=2001:0db8:85a3:0000:0000:8a2e:0370:7334"},
		{name: "aws-access-key", in: "id=AKIAIOSFODNN7EXAMPLE"},
	}
	for _, tt := range vectors {
		t.Run(tt.name, func(t *testing.T) {
			input := BundleInput{
				BundleID: "bundle-leak-" + tt.name,
				Files: []BundleInputFile{
					{Path: "logs/leak.log", Kind: "log", Content: []byte(tt.in), IsText: true},
				},
			}
			out, report, err := RedactBundle(input)
			if err != nil {
				t.Fatalf("RedactBundle() error = %v", err)
			}
			content := string(out.Files[0].Content)
			// The redactor strips known patterns; assert the secret is gone.
			if strings.Contains(content, strings.TrimPrefix(tt.in, strings.SplitN(tt.in, "=", 2)[0]+"=")) {
				t.Fatalf("RedactBundle() leaked %q, got %q", tt.in, content)
			}
			if report.Entries[0].MatchCount < 1 {
				t.Fatalf("RedactBundle() match count = %d, want >= 1", report.Entries[0].MatchCount)
			}
		})
	}
}

func TestRedactBundleRejectsEmptyBundleID(t *testing.T) {
	_, _, err := RedactBundle(BundleInput{Files: []BundleInputFile{{Path: "logs/x.log", Kind: "log", IsText: true}}})
	if err == nil {
		t.Fatal("RedactBundle() error = nil, want missing bundle id error")
	}
}

func TestRedactBundleRejectsDuplicatePaths(t *testing.T) {
	_, _, err := RedactBundle(BundleInput{
		BundleID: "bundle-dupe",
		Files: []BundleInputFile{
			{Path: "logs/x.log", Kind: "log", IsText: true},
			{Path: "logs/x.log", Kind: "log", IsText: true},
		},
	})
	if err == nil {
		t.Fatal("RedactBundle() error = nil, want duplicate path error")
	}
}

func TestRedactBundleSkipsNonTextFiles(t *testing.T) {
	// A non-text file containing a secret-shaped value must NOT be redacted by RedactBundle:
	// callers tag binary files as IsText=false, and the redactor skip is intentional. The
	// caller is responsible for running a separate binary-shape check. This guarantees we
	// never silently corrupt binary content with placeholder text.
	leaky := []byte("operator=ops@midnight.example")
	input := BundleInput{
		BundleID: "bundle-bin",
		Files: []BundleInputFile{
			{Path: "binary/x.bin", Kind: "binary", Content: leaky, IsText: false},
		},
	}
	out, report, err := RedactBundle(input)
	if err != nil {
		t.Fatalf("RedactBundle() error = %v", err)
	}
	if string(out.Files[0].Content) != string(leaky) {
		t.Fatalf("RedactBundle() binary content = %q, want pass-through", out.Files[0].Content)
	}
	if report.Entries[0].MatchCount != 0 {
		t.Fatalf("RedactBundle() binary MatchCount = %d, want 0", report.Entries[0].MatchCount)
	}
}

func TestRedactBundleIsDeterministic(t *testing.T) {
	input := BundleInput{
		BundleID: "bundle-det",
		Files: []BundleInputFile{
			{Path: "logs/zzz.log", Kind: "log", Content: []byte("ops@midnight.example"), IsText: true},
			{Path: "logs/aaa.log", Kind: "log", Content: []byte("midn_ABCDEFGH12345678"), IsText: true},
		},
	}
	out1, rep1, err := RedactBundle(input)
	if err != nil {
		t.Fatalf("RedactBundle() error = %v", err)
	}
	out2, rep2, err := RedactBundle(input)
	if err != nil {
		t.Fatalf("RedactBundle() error = %v", err)
	}
	if len(out1.Files) != len(out2.Files) {
		t.Fatalf("RedactBundle() length divergence: %d vs %d", len(out1.Files), len(out2.Files))
	}
	for i := range out1.Files {
		if out1.Files[i].Path != out2.Files[i].Path || string(out1.Files[i].Content) != string(out2.Files[i].Content) {
			t.Fatalf("RedactBundle() determinism broken at %d", i)
		}
	}
	if rep1.TotalMatches != rep2.TotalMatches {
		t.Fatalf("RedactBundle() determinism broken in report: %d vs %d", rep1.TotalMatches, rep2.TotalMatches)
	}
}
