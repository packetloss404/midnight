package logs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func writeFixtureFile(t *testing.T, dir, name string, entries []Entry) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	if err := EncodeFixtureNDJSON(file, entries); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return path
}

func TestFileStorePaginatesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "app.log.1", []Entry{
		{At: base, Level: "info", Component: "app", Message: "boot one"},
		{At: base.Add(time.Second), Level: "info", Component: "app", Message: "ready"},
	})
	writeFixtureFile(t, dir, "app.log.2", []Entry{
		{At: base.Add(2 * time.Second), Level: "warn", Component: "app", Message: "degraded"},
		{At: base.Add(3 * time.Second), Level: "error", Component: "app", Message: "panic in handler"},
	})

	store, err := NewFileStore(dir, "app.log.*")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	page, err := store.Query(Filter{}, Cursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Entries) != 2 || !page.Truncated || page.NextCursor.File == "" {
		t.Fatalf("first page = %#v", page)
	}
	if page.Entries[0].Message != "boot one" || page.Entries[1].Message != "ready" {
		t.Fatalf("first page entries unexpected: %#v", page.Entries)
	}

	page2, err := store.Query(Filter{}, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page2.Entries) != 2 {
		t.Fatalf("second page count = %d, want 2", len(page2.Entries))
	}
	if page2.Entries[0].Message != "degraded" || page2.Entries[1].Message != "panic in handler" {
		t.Fatalf("second page entries unexpected: %#v", page2.Entries)
	}
	if page2.Truncated {
		t.Fatal("second page should not be truncated")
	}
}

func TestFileStoreFiltersByLevelComponentAndDeployment(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "mixed.log", []Entry{
		{At: base, Level: "info", Component: "app", Message: "ok", Fields: map[string]any{"deployment_id": "dep-1", "operation_id": "op-1"}},
		{At: base.Add(time.Second), Level: "error", Component: "app", Message: "boom", Fields: map[string]any{"deployment_id": "dep-1", "operation_id": "op-1"}},
		{At: base.Add(2 * time.Second), Level: "info", Component: "router", Message: "ok", Fields: map[string]any{"deployment_id": "dep-2", "operation_id": "op-2"}},
	})
	store, err := NewFileStore(dir, "*.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	page, err := store.Query(Filter{Levels: []string{"error"}}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("level filter: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "boom" {
		t.Fatalf("level filter = %#v", page.Entries)
	}

	page, err = store.Query(Filter{Components: []string{"router"}}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("component filter: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Component != "router" {
		t.Fatalf("component filter = %#v", page.Entries)
	}

	page, err = store.Query(Filter{Deployments: []string{"dep-2"}}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("deployment filter: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Component != "router" {
		t.Fatalf("deployment filter = %#v", page.Entries)
	}

	page, err = store.Query(Filter{Operations: []string{"op-2"}}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("operation filter: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Component != "router" {
		t.Fatalf("operation filter = %#v", page.Entries)
	}
}

func TestFileStoreFiltersByTimeRangeAndFreeText(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "x.log", []Entry{
		{At: base, Level: "info", Component: "app", Message: "before window"},
		{At: base.Add(time.Hour), Level: "info", Component: "app", Message: "interesting needle"},
		{At: base.Add(2 * time.Hour), Level: "info", Component: "app", Message: "after window"},
	})
	store, err := NewFileStore(dir, "*.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	page, err := store.Query(Filter{
		Since: base.Add(30 * time.Minute),
		Until: base.Add(90 * time.Minute),
	}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("time range: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "interesting needle" {
		t.Fatalf("time range filter = %#v", page.Entries)
	}

	page, err = store.Query(Filter{FreeText: "NEEDLE"}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("free text: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("free text = %#v", page.Entries)
	}
}

func TestFileStoreReturnsLogQueryInvalidOnBadFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "*.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	_, err = store.Query(Filter{Tail: -1}, Cursor{}, 10)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	code, hint, ok := failurecodes.ReasonOf(err)
	if !ok || code != failurecodes.LogQueryInvalid {
		t.Fatalf("reason = (%q, %v); want LOG_QUERY_INVALID", code, ok)
	}
	if hint == "" {
		t.Fatal("expected non-empty recovery hint")
	}
	var failure *failurecodes.FailureError
	if !errors.As(err, &failure) {
		t.Fatal("expected *FailureError")
	}
}

func TestFileStoreSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.log")
	content := strings.Join([]string{
		`{"at":"2026-04-25T12:00:00Z","level":"info","component":"app","message":"first"}`,
		`not json`,
		`{"at":"2026-04-25T12:00:01Z","level":"info","component":"app","message":"second"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := NewFileStore(dir, "*.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	page, err := store.Query(Filter{}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Message != "first" || page.Entries[1].Message != "second" {
		t.Fatalf("malformed-line skip failed: %#v", page.Entries)
	}
}

func TestFileStoreResumesAtCursorOffsetInsideFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureFile(t, dir, "single.log", []Entry{
		{At: time.Unix(1, 0).UTC(), Level: "info", Component: "app", Message: "1"},
		{At: time.Unix(2, 0).UTC(), Level: "info", Component: "app", Message: "2"},
		{At: time.Unix(3, 0).UTC(), Level: "info", Component: "app", Message: "3"},
		{At: time.Unix(4, 0).UTC(), Level: "info", Component: "app", Message: "4"},
	})
	store, err := NewFileStore(dir, "single.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	first, err := store.Query(Filter{}, Cursor{}, 2)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.NextCursor.File != path || first.NextCursor.Offset != 2 {
		t.Fatalf("first cursor = %#v, want File=%s Offset=2", first.NextCursor, path)
	}

	second, err := store.Query(Filter{}, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second.Entries) != 2 || second.Entries[0].Message != "3" || second.Entries[1].Message != "4" {
		t.Fatalf("second page = %#v", second)
	}
}
