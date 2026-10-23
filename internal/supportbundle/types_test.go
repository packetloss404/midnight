package supportbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestValidateAcceptsValidManifest(t *testing.T) {
	manifest := Manifest{
		ID:        "bundle-001",
		CreatedAt: time.Now(),
		Selector:  Selector{Components: []string{"api"}, Logs: true},
		Files:     []ManifestFile{{Path: "logs/api.log", Kind: "log", Bytes: 12}},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestManifestValidateRejectsUnsafePath(t *testing.T) {
	manifest := Manifest{
		ID:        "bundle-001",
		CreatedAt: time.Now(),
		Selector:  Selector{Logs: true},
		Files:     []ManifestFile{{Path: "../secret", Kind: "log"}},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsafe path error")
	}
}

func TestManifestFileValidateChecksSHA256WhenPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	data := []byte("explicit evidence bytes")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	file := ManifestFile{Path: "logs/api.log", Kind: "log", Bytes: int64(len(data)), SHA256: sha256ForExplicitEvidenceFile(t, path)}
	if err := file.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	file.SHA256 = strings.ToUpper(file.SHA256)
	if err := file.Validate(); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("Validate() error = %v, want sha256 format error", err)
	}
}

func TestManifestFileValidateAllowsOmittedSHA256(t *testing.T) {
	file := ManifestFile{Path: "logs/api.log", Kind: "log", Bytes: 12}
	if err := file.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestSelectorValidateRequiresInputKind(t *testing.T) {
	if err := (Selector{Components: []string{"api"}}).Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing input kind error")
	}
}

func TestBuildPreviewManifestValidatesSelector(t *testing.T) {
	_, err := BuildPreviewManifest("preview-001", time.Now(), Selector{}, []PreviewFile{{Path: "logs/api.log", Kind: "log"}})
	if err == nil {
		t.Fatal("BuildPreviewManifest() error = nil, want selector validation error")
	}
}

func TestBuildPreviewManifestRejectsHardBlockedPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want BlockedPathClass
	}{
		{name: "secret", path: "logs/app-secret.txt", want: BlockedPathSecret},
		{name: "token", path: "debug/session-token.log", want: BlockedPathToken},
		{name: "raw env dump", path: "debug/raw-env-dump.txt", want: BlockedPathSecret},
		{name: "dot env", path: "debug/.env", want: BlockedPathSecret},
		{name: "authorization header", path: "headers/authorization.txt", want: BlockedPathToken},
		{name: "cookie header", path: "headers/cookie.txt", want: BlockedPathToken},
		{name: "acme", path: "certs/acme-account.key", want: BlockedPathACME},
		{name: "acme directory key", path: "certs/acme/account.key", want: BlockedPathACME},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyHardBlockedPath(tt.path); got != tt.want {
				t.Fatalf("ClassifyHardBlockedPath() = %q, want %q", got, tt.want)
			}
			_, err := BuildPreviewManifest("preview-001", time.Now(), Selector{Logs: true}, []PreviewFile{{Path: tt.path, Kind: "log"}})
			if err == nil {
				t.Fatal("BuildPreviewManifest() error = nil, want hard-blocked path error")
			}
		})
	}
}

func TestBuildPreviewManifestReturnsSafePreview(t *testing.T) {
	manifest, err := BuildPreviewManifest("preview-001", time.Now(), Selector{Components: []string{"api"}, Logs: true}, []PreviewFile{{Path: "logs/api.log", Kind: "log", Bytes: 42}})
	if err != nil {
		t.Fatalf("BuildPreviewManifest() error = %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "logs/api.log" || manifest.Files[0].Bytes != 42 {
		t.Fatalf("BuildPreviewManifest() files = %+v, want safe preview entry", manifest.Files)
	}
}

func TestBuildPreviewJSONRendersManifest(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	data, err := BuildPreviewJSON("preview-001", createdAt, Selector{Logs: true}, []PreviewFile{{Path: "logs/api.log", Kind: "log", Bytes: 42}})
	if err != nil {
		t.Fatalf("BuildPreviewJSON() error = %v", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("BuildPreviewJSON() returned invalid JSON: %v", err)
	}
	if manifest.ID != "preview-001" || len(manifest.Files) != 1 || manifest.Files[0].Path != "logs/api.log" {
		t.Fatalf("BuildPreviewJSON() manifest = %+v, want preview manifest", manifest)
	}
}

func TestBuildArchivePlanAppliesPerCategoryTruncation(t *testing.T) {
	plan, err := BuildArchivePlan(Selector{Logs: true}, []PreviewFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 80},
		{Path: "logs/worker.log", Kind: "log", Bytes: 80},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 40},
	}, SizeCap{MaxBytes: 100})
	if err != nil {
		t.Fatalf("BuildArchivePlan() error = %v", err)
	}

	if plan.CategoryBudgets["log"] != 50 || plan.CategoryBudgets["metric"] != 50 {
		t.Fatalf("BuildArchivePlan() budgets = %+v, want equal per-category budgets", plan.CategoryBudgets)
	}
	if plan.OriginalBytes != 200 || plan.IncludedBytes != 90 || len(plan.TruncationMarkers) != 2 {
		t.Fatalf("BuildArchivePlan() = %+v, want truncated log category and full metric category", plan)
	}
	if plan.Files[0].IncludedBytes != 50 || plan.Files[1].IncludedBytes != 0 || plan.Files[2].IncludedBytes != 40 {
		t.Fatalf("BuildArchivePlan() files = %+v, want per-file included bytes", plan.Files)
	}
}

func TestBuildArchivePlanUsesDefaultCap(t *testing.T) {
	plan, err := BuildArchivePlan(Selector{Logs: true}, []PreviewFile{
		{Path: "logs/api.log", Kind: "log", Bytes: DefaultSizeCapBytes + 1},
	}, SizeCap{})
	if err != nil {
		t.Fatalf("BuildArchivePlan() error = %v", err)
	}

	if plan.MaxBytes != DefaultSizeCapBytes || plan.IncludedBytes != DefaultSizeCapBytes {
		t.Fatalf("BuildArchivePlan() = %+v, want default 200MB cap enforced", plan)
	}
	if len(plan.TruncationMarkers) != 1 || plan.TruncationMarkers[0].IncludedBytes != DefaultSizeCapBytes {
		t.Fatalf("BuildArchivePlan() truncation = %+v, want default cap marker", plan.TruncationMarkers)
	}
}

func TestBuildArchivePlanRejectsBlockedPath(t *testing.T) {
	_, err := BuildArchivePlan(Selector{Logs: true}, []PreviewFile{{Path: "logs/session-token.log", Kind: "log", Bytes: 80}}, SizeCap{MaxBytes: 100})
	if err == nil {
		t.Fatal("BuildArchivePlan() error = nil, want blocked path error")
	}
}

func TestBuildArchiveManifestSortsAndAppliesCategoryBudgets(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildArchiveManifest("bundle-001", createdAt, Selector{Logs: true, Metrics: true}, []PlannedFile{
		{Path: "logs/z.log", Kind: "log", Bytes: 40, SHA256: strings.Repeat("1", 64)},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 10, SHA256: strings.Repeat("2", 64)},
		{Path: "logs/a.log", Kind: "log", Bytes: 30, SHA256: strings.Repeat("3", 64)},
		{Path: "events/a.json", Kind: "event", Bytes: 1, SHA256: strings.Repeat("4", 64)},
		{Path: "logs/session-token.log", Kind: "log", Bytes: 1, SHA256: strings.Repeat("5", 64)},
	}, map[string]int64{"log": 50, "metric": 10})
	if err != nil {
		t.Fatalf("BuildArchiveManifest() error = %v", err)
	}

	wantFiles := []ManifestFile{
		{Path: "logs/a.log", Kind: "log", SHA256: strings.Repeat("3", 64), Bytes: 30},
		{Path: "metrics/api.prom", Kind: "metric", SHA256: strings.Repeat("2", 64), Bytes: 10},
	}
	if len(manifest.Files) != len(wantFiles) {
		t.Fatalf("BuildArchiveManifest() files = %+v, want %+v", manifest.Files, wantFiles)
	}
	for i := range wantFiles {
		if manifest.Files[i] != wantFiles[i] {
			t.Fatalf("BuildArchiveManifest() files[%d] = %+v, want %+v", i, manifest.Files[i], wantFiles[i])
		}
	}

	wantExcluded := []ManifestExclusion{
		{Path: "events/a.json", Kind: "event", Bytes: 1, Reason: "missing-category-budget"},
		{Path: "logs/session-token.log", Kind: "log", Bytes: 1, Reason: "hard-blocked-token"},
		{Path: "logs/z.log", Kind: "log", Bytes: 40, Reason: "category-budget-exceeded"},
	}
	if len(manifest.Excluded) != len(wantExcluded) {
		t.Fatalf("BuildArchiveManifest() excluded = %+v, want %+v", manifest.Excluded, wantExcluded)
	}
	for i := range wantExcluded {
		if manifest.Excluded[i] != wantExcluded[i] {
			t.Fatalf("BuildArchiveManifest() excluded[%d] = %+v, want %+v", i, manifest.Excluded[i], wantExcluded[i])
		}
	}
}

func TestWriteManifestJSONWritesStableManifest(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildArchiveManifest("bundle-001", createdAt, Selector{Logs: true}, []PlannedFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 42, SHA256: strings.Repeat("a", 64)},
	}, map[string]int64{"log": 100})
	if err != nil {
		t.Fatalf("BuildArchiveManifest() error = %v", err)
	}

	var buf bytes.Buffer
	if err := WriteManifestJSON(&buf, manifest); err != nil {
		t.Fatalf("WriteManifestJSON() error = %v", err)
	}

	var got Manifest
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("WriteManifestJSON() returned invalid JSON: %v", err)
	}
	if got.ID != "bundle-001" || len(got.Files) != 1 || got.Files[0].Path != "logs/api.log" || got.Files[0].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("WriteManifestJSON() manifest = %+v, want written archive manifest", got)
	}
}

func TestArchiveManifestJSONFileRoundTripsDeterministically(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildArchiveManifest("bundle-001", createdAt, Selector{Logs: true}, []PlannedFile{
		{Path: "logs/api.log", Kind: "log", Bytes: 42, SHA256: strings.Repeat("a", 64)},
	}, map[string]int64{"log": 100})
	if err != nil {
		t.Fatalf("BuildArchiveManifest() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")

	if err := WriteArchiveManifestJSONFile(path, manifest); err != nil {
		t.Fatalf("WriteArchiveManifestJSONFile() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalManifestJSON(manifest)
	if err != nil {
		t.Fatalf("MarshalManifestJSON() error = %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("WriteArchiveManifestJSONFile() wrote %q, want deterministic manifest JSON %q", written, want)
	}

	got, err := ReadArchiveManifestJSONFile(path)
	if err != nil {
		t.Fatalf("ReadArchiveManifestJSONFile() error = %v", err)
	}
	if got.ID != manifest.ID || len(got.Files) != 1 || got.Files[0] != manifest.Files[0] {
		t.Fatalf("ReadArchiveManifestJSONFile() = %+v, want archive manifest %+v", got, manifest)
	}
}

func TestArchiveManifestJSONFileRejectsNonJSONPath(t *testing.T) {
	manifest := Manifest{ID: "bundle-001", CreatedAt: time.Now(), Selector: Selector{Logs: true}, Files: []ManifestFile{{Path: "logs/api.log", Kind: "log", Bytes: 1}}}
	path := filepath.Join(t.TempDir(), "manifest.txt")

	if err := WriteArchiveManifestJSONFile(path, manifest); err == nil {
		t.Fatal("WriteArchiveManifestJSONFile() error = nil, want .json path validation error")
	}
	if _, err := ReadArchiveManifestJSONFile(path); err == nil {
		t.Fatal("ReadArchiveManifestJSONFile() error = nil, want .json path validation error")
	}
}

func TestReadArchiveManifestJSONFileRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	data := []byte(`{
  "id": "bundle-001",
  "createdAt": "2026-04-25T12:00:00Z",
  "selector": {"logs": true},
  "files": [{"path": "logs/api.log", "kind": "log", "bytes": 1, "surprise": true}]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ReadArchiveManifestJSONFile(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ReadArchiveManifestJSONFile() error = %v, want unknown field error", err)
	}
}

func TestReadArchiveManifestJSONFileValidatesDecodedManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	data := []byte(`{
  "id": "bundle-001",
  "createdAt": "2026-04-25T12:00:00Z",
  "selector": {"logs": true},
  "files": [{"path": "../secret", "kind": "log", "bytes": 1}]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadArchiveManifestJSONFile(path); err == nil {
		t.Fatal("ReadArchiveManifestJSONFile() error = nil, want decoded manifest validation error")
	}
}

func TestBuildArchiveManifestRejectsInvalidBudget(t *testing.T) {
	_, err := BuildArchiveManifest("bundle-001", time.Now(), Selector{Logs: true}, []PlannedFile{{Path: "logs/api.log", Kind: "log", Bytes: 1}}, map[string]int64{"log": -1})
	if err == nil {
		t.Fatal("BuildArchiveManifest() error = nil, want invalid budget error")
	}
}

func TestBuildArchiveReviewSummarySummarizesManifest(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildArchiveManifest("bundle-001", createdAt, Selector{Logs: true, Metrics: true}, []PlannedFile{
		{Path: "logs/a.log", Kind: "log", Bytes: 30, SHA256: strings.Repeat("a", 64)},
		{Path: "logs/z.log", Kind: "log", Bytes: 40, SHA256: strings.Repeat("b", 64)},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: 10, SHA256: strings.Repeat("c", 64)},
	}, map[string]int64{"log": 50, "metric": 10})
	if err != nil {
		t.Fatalf("BuildArchiveManifest() error = %v", err)
	}
	redaction := RedactionSummary{Findings: []RedactionFinding{{Path: "logs/a.log", Rule: "token", Count: 2}}, Total: 2}

	summary, err := BuildArchiveReviewSummary(manifest, map[string]int64{"log": 50, "metric": 10}, redaction, ReviewDecisionNeedsReview)
	if err != nil {
		t.Fatalf("BuildArchiveReviewSummary() error = %v", err)
	}

	if summary.ManifestID != "bundle-001" || summary.EntryCount != 2 || summary.ExcludedCount != 1 {
		t.Fatalf("BuildArchiveReviewSummary() counts = %+v, want manifest id and entry/excluded counts", summary)
	}
	if summary.BytesByCategory["log"] != 30 || summary.BytesByCategory["metric"] != 10 {
		t.Fatalf("BuildArchiveReviewSummary() bytesByCategory = %+v, want included bytes by category", summary.BytesByCategory)
	}
	if len(summary.OverBudgetCategories) != 1 {
		t.Fatalf("BuildArchiveReviewSummary() overBudgetCategories = %+v, want one log category", summary.OverBudgetCategories)
	}
	wantOverBudget := OverBudgetCategory{Category: "log", BudgetBytes: 50, IncludedBytes: 30, ExcludedBytes: 40, AttemptedBytes: 70}
	if summary.OverBudgetCategories[0] != wantOverBudget {
		t.Fatalf("BuildArchiveReviewSummary() overBudgetCategories[0] = %+v, want %+v", summary.OverBudgetCategories[0], wantOverBudget)
	}
	if summary.RedactionSummary.Total != 2 || summary.Decision != ReviewDecisionNeedsReview {
		t.Fatalf("BuildArchiveReviewSummary() redaction/decision = %+v/%q, want passthrough and needs-review", summary.RedactionSummary, summary.Decision)
	}
}

func TestBuildArchiveReviewSummaryValidatesDecision(t *testing.T) {
	manifest := Manifest{ID: "bundle-001", CreatedAt: time.Now(), Selector: Selector{Logs: true}, Files: []ManifestFile{{Path: "logs/api.log", Kind: "log", Bytes: 1}}}
	_, err := BuildArchiveReviewSummary(manifest, map[string]int64{"log": 10}, RedactionSummary{}, ReviewDecision("pending"))
	if err == nil {
		t.Fatal("BuildArchiveReviewSummary() error = nil, want invalid decision error")
	}
}

func TestMarshalArchiveReviewSummaryJSONRendersSummary(t *testing.T) {
	summary := ArchiveReviewSummary{
		ManifestID:      "bundle-001",
		EntryCount:      1,
		BytesByCategory: map[string]int64{"log": 42},
		RedactionSummary: RedactionSummary{
			Findings: []RedactionFinding{{Path: "logs/api.log", Rule: "email", Count: 1}},
			Total:    1,
		},
		Decision: ReviewDecisionApproved,
	}

	data, err := MarshalArchiveReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalArchiveReviewSummaryJSON() error = %v", err)
	}

	var got ArchiveReviewSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalArchiveReviewSummaryJSON() returned invalid JSON: %v", err)
	}
	if got.ManifestID != "bundle-001" || got.EntryCount != 1 || got.BytesByCategory["log"] != 42 || got.Decision != ReviewDecisionApproved {
		t.Fatalf("MarshalArchiveReviewSummaryJSON() summary = %+v, want rendered review summary", got)
	}
}

func TestSummarizeRedactionFindings(t *testing.T) {
	summary := SummarizeRedactionFindings([]RedactionFinding{
		{Path: "logs/api.log", Rule: "token", Count: 2},
		{Path: "logs/api.log", Rule: "token", Count: 3},
		{Path: "logs/empty.log", Rule: "password", Count: 0},
		{Path: "logs/invalid.log", Rule: "", Count: 1},
		{Path: "events/api.json", Rule: "email", Count: 1},
	})

	if summary.Total != 6 || len(summary.Findings) != 2 {
		t.Fatalf("SummarizeRedactionFindings() = %+v, want total 6 and two aggregated findings", summary)
	}
	if summary.Findings[0] != (RedactionFinding{Path: "events/api.json", Rule: "email", Count: 1}) {
		t.Fatalf("SummarizeRedactionFindings() first finding = %+v, want deterministic sort", summary.Findings[0])
	}
	if summary.Findings[1] != (RedactionFinding{Path: "logs/api.log", Rule: "token", Count: 5}) {
		t.Fatalf("SummarizeRedactionFindings() second finding = %+v, want aggregated token finding", summary.Findings[1])
	}
}

func TestSizeCapValidate(t *testing.T) {
	if err := (SizeCap{MaxBytes: 1024}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := (SizeCap{}).Validate(); err == nil {
		t.Fatal("Validate() error = nil, want positive cap error")
	}
}

func TestTruncationMarkerValidate(t *testing.T) {
	marker := TruncationMarker{Path: "logs/api.log", OriginalBytes: 4096, IncludedBytes: 1024, Reason: "size-cap"}
	if err := marker.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	marker.IncludedBytes = 8192
	if err := marker.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want included bytes validation error")
	}
}

func TestTruncationMarkerValidateRejectsUnsafePath(t *testing.T) {
	marker := TruncationMarker{Path: "../secret", OriginalBytes: 4096, IncludedBytes: 1024, Reason: "size-cap"}
	if err := marker.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsafe path error")
	}
}

func sha256ForExplicitEvidenceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
