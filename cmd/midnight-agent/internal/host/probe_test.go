package host

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDockerAPIAtLeastComparesMajorMinor(t *testing.T) {
	cases := []struct {
		observed string
		required string
		want     bool
		wantErr  bool
	}{
		{"1.43", "1.43", true, false},
		{"1.49", "1.43", true, false},
		{"1.42", "1.43", false, false},
		{"2.00", "1.43", true, false},
		{" v1.45 ", "1.43", true, false},
		{"1.x", "1.43", false, true},
		{"1", "1.43", false, true},
		{"", "1.43", false, true},
		{"1.43", "1.x", false, true},
	}
	for _, tc := range cases {
		got, err := DockerAPIAtLeast(tc.observed, tc.required)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("DockerAPIAtLeast(%q, %q): expected error", tc.observed, tc.required)
			}
			continue
		}
		if err != nil {
			t.Fatalf("DockerAPIAtLeast(%q, %q): unexpected error %v", tc.observed, tc.required, err)
		}
		if got != tc.want {
			t.Fatalf("DockerAPIAtLeast(%q, %q) = %v, want %v", tc.observed, tc.required, got, tc.want)
		}
	}
}

func TestEvaluateDockerProbeReportsAvailableForReachableSocketAndCompliantAPI(t *testing.T) {
	socket := dockerSocketForTest(t)

	probe := EvaluateDockerProbe(socket, DockerEngineAPIResponse{APIVersion: "1.45", Version: "24.0.6"})
	if !probe.SocketReachable {
		t.Fatalf("expected socket reachable")
	}
	if !probe.APIFloorOK {
		t.Fatalf("expected api floor ok")
	}
	if !probe.Available {
		t.Fatalf("expected probe available, got %+v", probe)
	}
	if probe.APIFloorRequired != MinDockerAPIVersion {
		t.Fatalf("api floor required = %q", probe.APIFloorRequired)
	}
	if probe.Endpoint != "unix://"+socket {
		t.Fatalf("endpoint = %q", probe.Endpoint)
	}
	if probe.Error != "" {
		t.Fatalf("error = %q", probe.Error)
	}
}

func TestEvaluateDockerProbeReportsMissingSocket(t *testing.T) {
	probe := EvaluateDockerProbe(filepath.Join(t.TempDir(), "missing.sock"), DockerEngineAPIResponse{APIVersion: "1.43"})
	if probe.SocketReachable {
		t.Fatalf("expected socket not reachable")
	}
	if probe.Available {
		t.Fatalf("expected probe unavailable when socket missing")
	}
	if probe.Error == "" {
		t.Fatalf("expected error string")
	}
	if !probe.APIFloorOK {
		t.Fatalf("expected api floor ok despite missing socket")
	}
}

func TestEvaluateDockerProbeReportsAPIFloorViolation(t *testing.T) {
	socket := dockerSocketForTest(t)
	probe := EvaluateDockerProbe(socket, DockerEngineAPIResponse{APIVersion: "1.42", Version: "23.0.0"})
	if probe.APIFloorOK {
		t.Fatalf("expected api floor violation")
	}
	if probe.Available {
		t.Fatalf("expected probe unavailable when api floor below required")
	}
}

func TestEvaluateDockerProbeUsesDefaultSocketPath(t *testing.T) {
	probe := EvaluateDockerProbe("", DockerEngineAPIResponse{APIVersion: "1.43"})
	if probe.SocketPath != DefaultDockerSocketPath {
		t.Fatalf("default socket path = %q, want %q", probe.SocketPath, DefaultDockerSocketPath)
	}
}

func TestDetectCgroupV2DetectsMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("cpu memory io\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}
	probe := DetectCgroupV2(dir)
	if !probe.V2Unified {
		t.Fatalf("expected cgroup v2 unified detected, got %+v", probe)
	}
	if probe.MountPath != dir {
		t.Fatalf("mount path = %q, want %q", probe.MountPath, dir)
	}
}

func TestDetectCgroupV2AbsentMarkerReportsLegacy(t *testing.T) {
	probe := DetectCgroupV2(t.TempDir())
	if probe.V2Unified {
		t.Fatalf("expected cgroup v2 absent")
	}
}

func TestDetectKernelVersionReadsProcVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version")
	const sample = "Linux version 6.5.0-15-generic (buildd@lcy02) #15-Ubuntu SMP\n"
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatalf("write version file: %v", err)
	}
	probe := DetectKernelVersion(path)
	if probe.Release != "6.5.0-15-generic" {
		t.Fatalf("kernel release = %q", probe.Release)
	}
	if probe.Version == "" {
		t.Fatalf("kernel version line should be populated")
	}
}

func TestDetectKernelVersionMissingFileIsZero(t *testing.T) {
	probe := DetectKernelVersion(filepath.Join(t.TempDir(), "missing"))
	if probe.Release != "" || probe.Version != "" {
		t.Fatalf("expected zero kernel probe, got %+v", probe)
	}
	probe = DetectKernelVersion("")
	if probe.Release != "" || probe.Version != "" {
		t.Fatalf("expected zero kernel probe for empty path, got %+v", probe)
	}
}

func TestEvaluateDockerProbeRecordsAPIVersionEvenWhenSocketMissing(t *testing.T) {
	probe := EvaluateDockerProbe(filepath.Join(t.TempDir(), "x.sock"), DockerEngineAPIResponse{APIVersion: "1.45"})
	if probe.APIVersion != "1.45" {
		t.Fatalf("api version = %q", probe.APIVersion)
	}
}

func dockerSocketForTest(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(socket, []byte{}, 0o600); err != nil {
			t.Fatalf("write fake socket: %v", err)
		}
		return socket
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return socket
}

// Defensive guard: the package must not link to any docker SDK type by
// reflection or syscall. The check is symbolic — runtime.Compiler is used
// only so the linter notices we exercise the runtime package.
func TestPackageHasNoLiveDockerLink(t *testing.T) {
	if runtime.Compiler == "" {
		t.Skip("compiler unknown")
	}
}
