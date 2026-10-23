package desiredstate

import (
	"path/filepath"
	"testing"
)

func TestCacheFileSaveAndLoad(t *testing.T) {
	cache := CacheFile{Path: filepath.Join(t.TempDir(), "nested", "desired.json")}
	want := State{ETag: `"etag-a"`, Raw: []byte(`{"deployments":[]}`)}

	if err := cache.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := cache.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.ETag != want.ETag || string(got.Raw) != string(want.Raw) {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
}

func TestCacheFileLoadMissingReturnsEmptyState(t *testing.T) {
	cache := CacheFile{Path: filepath.Join(t.TempDir(), "desired.json")}

	got, err := cache.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.ETag != "" || got.Raw != nil {
		t.Fatalf("state = %+v", got)
	}
}

func TestCacheFileRequiresPath(t *testing.T) {
	if _, err := (CacheFile{}).Load(); err == nil {
		t.Fatal("expected Load error")
	}
	if err := (CacheFile{}).Save(State{}); err == nil {
		t.Fatal("expected Save error")
	}
}
