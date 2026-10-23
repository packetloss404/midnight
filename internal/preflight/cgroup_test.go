package preflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHasCgroupV2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		want    bool
		wantErr bool
	}{
		{
			name: "v2 mount present",
			text: "27 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
			want: true,
		},
		{
			name: "v1 only",
			text: "28 23 0:27 / /sys/fs/cgroup/cpu rw,relatime - cgroup cgroup rw,cpu\n",
		},
		{
			name:    "malformed",
			text:    "not mountinfo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := HasCgroupV2(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("HasCgroupV2() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("HasCgroupV2() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasCgroupV2FSType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		want    bool
		wantErr bool
	}{
		{name: "v2", text: "cgroup2fs\n", want: true},
		{name: "tmpfs", text: "tmpfs\n"},
		{name: "empty", text: "\n", wantErr: true},
		{name: "multiple tokens", text: "cgroup2fs extra\n", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := HasCgroupV2FSType(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("HasCgroupV2FSType() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("HasCgroupV2FSType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCgroupFSTypeFromSourceUsesInjectedRunner(t *testing.T) {
	t.Parallel()

	got, err := CgroupFSTypeFromSource(context.Background(), HostSource{
		Runner: CommandRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "stat" {
				t.Fatalf("name = %q, want stat", name)
			}
			wantArgs := []string{"-fc", "%T", "/sys/fs/cgroup"}
			if len(args) != len(wantArgs) {
				t.Fatalf("args = %v, want %v", args, wantArgs)
			}
			for i := range args {
				if args[i] != wantArgs[i] {
					t.Fatalf("args = %v, want %v", args, wantArgs)
				}
			}
			return []byte("cgroup2fs\n"), nil
		}),
	})
	if err != nil {
		t.Fatalf("CgroupFSTypeFromSource() error = %v", err)
	}
	if got != "cgroup2fs" {
		t.Fatalf("CgroupFSTypeFromSource() = %q, want cgroup2fs", got)
	}
}

func TestHasCgroupV2FromPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte("27 23 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := HasCgroupV2FromPath(path)
	if err != nil {
		t.Fatalf("HasCgroupV2FromPath() error = %v", err)
	}
	if !got {
		t.Fatal("HasCgroupV2FromPath() = false, want true")
	}
}

func TestHasCgroupV2FromSourceUsesInjectedReader(t *testing.T) {
	t.Parallel()

	source := HostSource{
		Reader: FileReaderFunc(func(path string) ([]byte, error) {
			if path != "fixture/mountinfo" {
				t.Fatalf("path = %q, want fixture/mountinfo", path)
			}
			return []byte("27 23 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n"), nil
		}),
		Paths: HostPaths{MountInfo: "fixture/mountinfo"},
	}

	got, err := HasCgroupV2FromSource(source)
	if err != nil {
		t.Fatalf("HasCgroupV2FromSource() error = %v", err)
	}
	if !got {
		t.Fatal("HasCgroupV2FromSource() = false, want true")
	}
}

func TestHostSourceRequiresExplicitMountInfoPath(t *testing.T) {
	t.Parallel()

	_, err := HasCgroupV2FromSource(HostSource{Reader: FileReaderFunc(func(string) ([]byte, error) {
		t.Fatal("reader must not be called without an explicit path")
		return nil, nil
	})})
	if err == nil {
		t.Fatal("HasCgroupV2FromSource() error = nil, want error")
	}
}
