// Package host contains host capability probe data shapes.
package host

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// MinDockerAPIVersion is the panel-locked Docker Engine API floor that the
// agent refuses to operate below. It corresponds to Docker Engine >= 24.0.
const MinDockerAPIVersion = "1.43"

// DefaultDockerSocketPath is the conventional Linux Docker socket location
// used by the agent for offline reachability checks.
const DefaultDockerSocketPath = "/var/run/docker.sock"

// Probe describes local host properties collected by future runtime probes.
type Probe struct {
	Hostname string
	OS       string
	Arch     string
	Kernel   KernelProbe
	Cgroups  CgroupProbe
	Runtime  RuntimeProbe
	Docker   DockerProbe
}

// KernelProbe describes kernel identity for host capability reports.
type KernelProbe struct {
	Version string
	Release string
}

// CgroupProbe describes cgroup hierarchy facts. The agent rejects
// cgroup v1 hosts with a deterministic remediation string.
type CgroupProbe struct {
	V2Unified bool
	MountPath string
}

// RuntimeProbe describes agent runtime paths and permissions.
type RuntimeProbe struct {
	RootPath string
	Writable bool
}

// DockerProbe describes Docker Engine availability without binding to Docker packages.
// The probe never opens a network connection; SocketReachable only reports
// whether the socket path is present and is a unix-domain-socket file.
type DockerProbe struct {
	Available        bool
	APIVersion       string
	Endpoint         string
	SocketPath       string
	SocketReachable  bool
	APIFloorOK       bool
	APIFloorRequired string
	Error            string
}

// DockerEngineAPIResponse is the read-only subset of Engine `/version` body
// fields the probe consumes. Callers inject this DTO so the probe stays
// offline (no live Docker network call).
type DockerEngineAPIResponse struct {
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Version       string `json:"Version"`
}

// EvaluateDockerProbe builds a DockerProbe from offline inputs. The caller
// supplies the (already-decoded) Engine API /version response and the
// socket path. No network or Docker daemon I/O happens here.
func EvaluateDockerProbe(socketPath string, engineResp DockerEngineAPIResponse) DockerProbe {
	probe := DockerProbe{
		SocketPath:       strings.TrimSpace(socketPath),
		APIVersion:       strings.TrimSpace(engineResp.APIVersion),
		APIFloorRequired: MinDockerAPIVersion,
	}
	if probe.SocketPath == "" {
		probe.SocketPath = DefaultDockerSocketPath
	}
	probe.SocketReachable = dockerSocketExists(probe.SocketPath)
	if !probe.SocketReachable {
		probe.Error = fmt.Sprintf("docker socket %q not found", probe.SocketPath)
	}
	probe.Endpoint = "unix://" + probe.SocketPath
	if probe.APIVersion != "" {
		ok, err := DockerAPIAtLeast(probe.APIVersion, MinDockerAPIVersion)
		if err != nil {
			if probe.Error == "" {
				probe.Error = err.Error()
			}
		}
		probe.APIFloorOK = ok
	}
	probe.Available = probe.SocketReachable && probe.APIFloorOK
	return probe
}

// DockerAPIAtLeast reports whether observed (e.g. "1.45") is greater than
// or equal to required (e.g. "1.43"). Both inputs MUST be major.minor
// numeric strings; malformed inputs return an error and false.
func DockerAPIAtLeast(observed, required string) (bool, error) {
	oMajor, oMinor, err := parseDockerAPIVersion(observed)
	if err != nil {
		return false, fmt.Errorf("parse observed docker api version: %w", err)
	}
	rMajor, rMinor, err := parseDockerAPIVersion(required)
	if err != nil {
		return false, fmt.Errorf("parse required docker api version: %w", err)
	}
	if oMajor != rMajor {
		return oMajor > rMajor, nil
	}
	return oMinor >= rMinor, nil
}

// DetectCgroupV2 reports whether the supplied filesystem root mounts the
// cgroup v2 unified hierarchy. The check is offline: it stats the
// canonical /sys/fs/cgroup/cgroup.controllers marker file, which is only
// present on cgroup v2.
func DetectCgroupV2(rootPath string) CgroupProbe {
	mount := strings.TrimRight(strings.TrimSpace(rootPath), "/")
	if mount == "" {
		mount = "/sys/fs/cgroup"
	}
	probe := CgroupProbe{MountPath: mount}
	marker := filepath.Join(mount, "cgroup.controllers")
	if info, err := os.Stat(marker); err == nil && !info.IsDir() {
		probe.V2Unified = true
	}
	return probe
}

// DetectKernelVersion reads the supplied kernel-version source file and
// returns a normalized KernelProbe. A missing or unreadable file yields a
// zero-valued probe, never an error; callers may surface the missing
// kernel info via doctor.
func DetectKernelVersion(path string) KernelProbe {
	path = strings.TrimSpace(path)
	if path == "" {
		return KernelProbe{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return KernelProbe{}
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return KernelProbe{}
	}
	fields := strings.Fields(line)
	probe := KernelProbe{Version: line}
	if len(fields) >= 3 {
		// /proc/version typical format:
		// "Linux version <release> (<builder>) (<gcc>) #N SMP <date>"
		probe.Release = fields[2]
	}
	return probe
}

// dockerSocketExists checks whether the Docker socket path is a real
// unix-domain socket. It does NOT attempt to connect or perform any RPC. A
// regular file or device node is rejected so a misconfigured host doesn't
// report a fake "reachable" state. On Windows (where named-pipe paths don't
// register as ModeSocket) we fall back to the existence check.
func dockerSocketExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	// Unix socket files report ModeSocket. On Windows the Docker named pipe
	// at `//./pipe/docker_engine` does not produce a Stat result with that
	// bit set, so we accept any non-directory existence as a reachability
	// signal there.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&os.ModeSocket != 0
}

// parseDockerAPIVersion splits a Docker Engine API version string of the
// form "1.43" into its major and minor components. Whitespace is trimmed
// and a leading "v" prefix is tolerated.
func parseDockerAPIVersion(version string) (int, int, error) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		return 0, 0, fmt.Errorf("docker api version is empty")
	}
	parts := strings.Split(version, ".")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("docker api version %q must be major.minor", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("docker api major %q: %w", parts[0], err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("docker api minor %q: %w", parts[1], err)
	}
	if major < 0 || minor < 0 {
		return 0, 0, fmt.Errorf("docker api version %q is negative", version)
	}
	return major, minor, nil
}
