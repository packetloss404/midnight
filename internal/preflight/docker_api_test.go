package preflight

import (
	"context"
	"os"
	"testing"
)

func TestDockerAPISupportsPhase0(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    bool
		wantErr bool
	}{
		{version: "1.42"},
		{version: "1.43", want: true},
		{version: "1.44", want: true},
		{version: "2.0", want: true},
		{version: "v1.43", want: true},
		{version: "1", wantErr: true},
		{version: "1.x", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			got, err := DockerAPISupportsPhase0(tt.version)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DockerAPISupportsPhase0() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("DockerAPISupportsPhase0() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDockerAPIVersionFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		want    string
		wantErr bool
	}{
		{name: "bare", text: "1.43\n", want: "1.43"},
		{name: "docker format server api", text: "1.44\n", want: "1.44"},
		{name: "server preferred", text: "Client:\n API version: 1.42\nServer:\n API version: 1.44\n", want: "1.44"},
		{name: "server same line", text: "Server API version: v1.45\n", want: "1.45"},
		{name: "missing", text: "Docker version 24.0.0\n", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := DockerAPIVersionFromText(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DockerAPIVersionFromText() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("DockerAPIVersionFromText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDockerAPIVersionFromSourceUsesInjectedRunner(t *testing.T) {
	t.Parallel()

	got, err := DockerAPIVersionFromSourceContext(context.Background(), HostSource{
		Runner: CommandRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "docker" {
				t.Fatalf("name = %q, want docker", name)
			}
			wantArgs := []string{"version", "--format", "{{.Server.APIVersion}}"}
			if len(args) != len(wantArgs) {
				t.Fatalf("args = %v, want %v", args, wantArgs)
			}
			for i := range args {
				if args[i] != wantArgs[i] {
					t.Fatalf("args = %v, want %v", args, wantArgs)
				}
			}
			return []byte("1.44\n"), nil
		}),
	})
	if err != nil {
		t.Fatalf("DockerAPIVersionFromSourceContext() error = %v", err)
	}
	if got != "1.44" {
		t.Fatalf("DockerAPIVersionFromSourceContext() = %q, want 1.44", got)
	}
}

func TestDockerAPIVersionFromSourceRunnerError(t *testing.T) {
	t.Parallel()

	_, err := DockerAPIVersionFromSourceContext(context.Background(), HostSource{
		Runner: CommandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		}),
	})
	if err == nil {
		t.Fatal("DockerAPIVersionFromSourceContext() error = nil, want error")
	}
}

func TestDockerAPIVersionFromSourceUsesInjectedReader(t *testing.T) {
	t.Parallel()

	got, err := DockerAPIVersionFromSource(HostSource{
		Reader: FileReaderFunc(func(path string) ([]byte, error) {
			if path != "docker-version.txt" {
				t.Fatalf("path = %q, want docker-version.txt", path)
			}
			return []byte("Server:\n API version: 1.44\n"), nil
		}),
		Paths: HostPaths{DockerAPIVersion: "docker-version.txt"},
	})
	if err != nil {
		t.Fatalf("DockerAPIVersionFromSource() error = %v", err)
	}
	if got != "1.44" {
		t.Fatalf("DockerAPIVersionFromSource() = %q, want 1.44", got)
	}
}

func TestDockerAPIVersionFromSourceRequiresExplicitPath(t *testing.T) {
	t.Parallel()

	_, err := DockerAPIVersionFromSource(HostSource{Reader: FileReaderFunc(func(string) ([]byte, error) {
		t.Fatal("reader must not be called without an explicit path")
		return nil, nil
	})})
	if err == nil {
		t.Fatal("DockerAPIVersionFromSource() error = nil, want error")
	}
}
