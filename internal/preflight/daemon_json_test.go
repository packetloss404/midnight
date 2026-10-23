package preflight

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeDockerDaemonBaseline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		existing      string
		wantChanged   bool
		wantConflict  bool
		wantErr       bool
		wantValues    map[string]any
		wantExtraPath string
	}{
		{
			name:        "empty",
			existing:    "",
			wantChanged: true,
			wantValues: map[string]any{
				"features.buildkit": true,
				"live-restore":      true,
				"log-driver":        "json-file",
				"log-opts.max-file": "5",
				"log-opts.max-size": "10m",
				"no-new-privileges": true,
				"storage-driver":    "overlay2",
			},
		},
		{
			name: "partial",
			existing: `{
				"features": {"buildkit": true},
				"log-opts": {"max-size": "10m"}
			}`,
			wantChanged: true,
			wantValues: map[string]any{
				"features.buildkit": true,
				"live-restore":      true,
				"log-driver":        "json-file",
				"log-opts.max-file": "5",
				"log-opts.max-size": "10m",
				"no-new-privileges": true,
				"storage-driver":    "overlay2",
			},
		},
		{
			name:         "conflicting",
			existing:     `{"features":{"buildkit":false}}`,
			wantConflict: true,
			wantErr:      true,
		},
		{
			name:     "invalid JSON",
			existing: `{"features":`,
			wantErr:  true,
		},
		{
			name: "non-conflicting operator keys",
			existing: `{
				"features": {"buildkit": true, "containerd-snapshotter": true},
				"live-restore": true,
				"log-driver": "json-file",
				"log-opts": {"max-file": "5", "max-size": "10m", "labels": "com.example.owner"},
				"no-new-privileges": true,
				"registry-mirrors": ["https://mirror.example.test"]
				,"storage-driver": "overlay2"
			}`,
			wantChanged: false,
			wantValues: map[string]any{
				"features.buildkit":               true,
				"features.containerd-snapshotter": true,
				"log-driver":                      "json-file",
				"log-opts.labels":                 "com.example.owner",
			},
			wantExtraPath: "registry-mirrors",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := MergeDockerDaemonBaseline([]byte(tt.existing))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("MergeDockerDaemonBaseline() error = nil, want error")
				}
				var conflict *DockerDaemonConflictError
				gotConflict := errors.As(err, &conflict)
				if gotConflict != tt.wantConflict {
					t.Fatalf("conflict error = %v, want %v; err = %v", gotConflict, tt.wantConflict, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("MergeDockerDaemonBaseline() error = %v", err)
			}
			if result.Changed != tt.wantChanged {
				t.Fatalf("Changed = %v, want %v", result.Changed, tt.wantChanged)
			}

			var merged map[string]any
			if err := json.Unmarshal(result.Merged, &merged); err != nil {
				t.Fatalf("merged JSON is invalid: %v", err)
			}
			for path, want := range tt.wantValues {
				if got := valueAtPath(t, merged, path); got != want {
					t.Fatalf("merged[%s] = %#v, want %#v", path, got, want)
				}
			}
			if tt.wantExtraPath != "" && valueAtPath(t, merged, tt.wantExtraPath) == nil {
				t.Fatalf("merged[%s] was not preserved", tt.wantExtraPath)
			}
		})
	}
}

func TestMergeDockerDaemonJSONRejectsInvalidBaseline(t *testing.T) {
	t.Parallel()

	_, err := MergeDockerDaemonJSON([]byte(`{}`), []byte(`[]`))
	if err == nil {
		t.Fatal("MergeDockerDaemonJSON() error = nil, want error")
	}
}

func TestMergeDockerDaemonBaselineFromPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(DockerDaemonBaselineJSON), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result, err := MergeDockerDaemonBaselineFromPath(path)
	if err != nil {
		t.Fatalf("MergeDockerDaemonBaselineFromPath() error = %v", err)
	}
	if result.Changed {
		t.Fatal("Changed = true, want false")
	}
}

func TestMergeDockerDaemonBaselineFromSourceUsesInjectedReader(t *testing.T) {
	t.Parallel()

	result, err := MergeDockerDaemonBaselineFromSource(HostSource{
		Reader: FileReaderFunc(func(path string) ([]byte, error) {
			if path != "daemon.json" {
				t.Fatalf("path = %q, want daemon.json", path)
			}
			return []byte(`{"features":{"buildkit":true},"live-restore":true,"log-driver":"json-file","log-opts":{"max-file":"5","max-size":"10m"},"no-new-privileges":true,"storage-driver":"overlay2"}`), nil
		}),
		Paths: HostPaths{DockerDaemonJSON: "daemon.json"},
	})
	if err != nil {
		t.Fatalf("MergeDockerDaemonBaselineFromSource() error = %v", err)
	}
	if result.Changed {
		t.Fatal("Changed = true, want false")
	}
}

func TestMergeDockerDaemonBaselineFromSourceRequiresExplicitPath(t *testing.T) {
	t.Parallel()

	_, err := MergeDockerDaemonBaselineFromSource(HostSource{Reader: FileReaderFunc(func(string) ([]byte, error) {
		t.Fatal("reader must not be called without an explicit path")
		return nil, nil
	})})
	if err == nil {
		t.Fatal("MergeDockerDaemonBaselineFromSource() error = nil, want error")
	}
}

func TestMergeDockerDaemonBaselineFromPathProvider(t *testing.T) {
	t.Parallel()

	result, err := MergeDockerDaemonBaselineFromPathProvider(
		fakeFileReader{"daemon.json": []byte(DockerDaemonBaselineJSON)},
		PathProviderFunc(func() string { return "daemon.json" }),
	)
	if err != nil {
		t.Fatalf("MergeDockerDaemonBaselineFromPathProvider() error = %v", err)
	}
	if result.Changed {
		t.Fatal("Changed = true, want false")
	}
}

func TestDockerDaemonJSONFromPathProviderRequiresProvider(t *testing.T) {
	t.Parallel()

	_, err := DockerDaemonJSONFromPathProvider(fakeFileReader{}, nil)
	if err == nil {
		t.Fatal("DockerDaemonJSONFromPathProvider() error = nil, want error")
	}
}

func valueAtPath(t *testing.T, object map[string]any, path string) any {
	t.Helper()

	current := any(object)
	start := 0
	for i := 0; i <= len(path); i++ {
		if i != len(path) && path[i] != '.' {
			continue
		}
		key := path[start:i]
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s traversed through non-object %#v", path, current)
		}
		current = object[key]
		start = i + 1
	}
	return current
}
