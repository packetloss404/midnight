package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicWritesAndReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("new"), 0o640); err != nil {
		t.Fatalf("WriteFileAtomic returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("file content = %q, want new", got)
	}
}
