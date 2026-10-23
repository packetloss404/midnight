package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouteGroupDynamicConfigPathUsesDeterministicJSONName(t *testing.T) {
	path, err := RouteGroupDynamicConfigPath("configs", "Tenant A / Primary")
	if err != nil {
		t.Fatalf("RouteGroupDynamicConfigPath returned error: %v", err)
	}

	want := filepath.Join("configs", "tenant-a-primary.json")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestWriteRouteGroupDynamicConfigRendersAndWritesJSON(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteRouteGroupDynamicConfig(dir, "Tenant A / Primary", []RouteInput{{
		Name:       "app",
		Host:       "app.example.test",
		ServiceURL: "http://127.0.0.1:8080",
		TLS:        true,
	}})
	if err != nil {
		t.Fatalf("WriteRouteGroupDynamicConfig returned error: %v", err)
	}

	wantPath := filepath.Join(dir, "tenant-a-primary.json")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"routers"`,
		`"app"`,
		`"rule": "Host(` + "`" + `app.example.test` + "`" + `) && PathPrefix(` + "`" + `/` + "`" + `)"`,
		`"app-service"`,
		`"url": "http://127.0.0.1:8080"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("artifact missing %q: %s", want, text)
		}
	}
}
