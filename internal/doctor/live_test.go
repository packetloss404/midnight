package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/preflight"
)

func TestNewLiveHostSourceWiresBoundedReadProbesAndPreservesOverrides(t *testing.T) {
	defaults := NewLiveHostSource(preflight.HostSource{})
	if _, ok := defaults.Reader.(preflight.OSFileReader); !ok {
		t.Fatal("live filesystem reader missing")
	}
	if _, ok := defaults.Runner.(preflight.OSCommandRunner); !ok {
		t.Fatal("live command runner missing")
	}
	if defaults.Paths.PackageEnv != "/etc/midnight/midnight.env" || defaults.Paths.DockerDaemonJSON != "/etc/docker/daemon.json" || defaults.Paths.DatabaseDSN != "/etc/midnight/db.dsn" {
		t.Fatalf("live installed paths=%+v", defaults.Paths)
	}
	if defaults.Hints.FirewallMode != "" || defaults.Hints.TimeSyncStatus != "" || defaults.Hints.WorkerRuntimeBackend != "" {
		t.Fatal("live source invented healthy hints")
	}
	var calls int
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := NewLiveHostSource(preflight.HostSource{
		Reader: stubReader{}, Paths: preflight.HostPaths{PackageEnv: "/fixture/midnight.env"},
		Runner: preflight.CommandRunnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 3*time.Second {
				t.Error("live command has no bounded context")
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Error("caller cancellation was discarded")
			}
			return nil, ctx.Err()
		}),
	})
	if source.Paths.PackageEnv != "/fixture/midnight.env" {
		t.Fatal("live constructor replaced explicit input")
	}
	results := RunLiveChecks(ctx, LiveOptions{Source: source, Live: true, FilterIDs: []string{"OR-CHK-DOC-001", "OR-CHK-DOC-003"}})
	if calls != 2 || len(results) != 2 {
		t.Fatalf("live read probes were skipped: calls=%d results=%+v", calls, results)
	}
	for _, result := range results {
		if result.Status != StatusConfigError {
			t.Fatalf("canceled probe claimed healthy: %+v", result)
		}
	}
}

func TestFixturePathsOnlyNeverReadsPackageDerivedPaths(t *testing.T) {
	var reads []string
	source := preflight.HostSource{
		FixturePathsOnly: true,
		Paths:            preflight.HostPaths{PackageEnv: "captured.env"},
		Reader: preflight.FileReaderFunc(func(path string) ([]byte, error) {
			reads = append(reads, path)
			if path != "captured.env" {
				t.Errorf("fixture read an uncaptured path %q", path)
				return nil, os.ErrNotExist
			}
			return []byte("MIDNIGHT_DB_DSN_FILE=/outside/db.dsn\nMIDNIGHT_WORKER_ROUTE_CONFIG_DIR=/outside/routes\nMIDNIGHT_BUILDKITD_BIN=/outside/buildkitd\nMIDNIGHT_BUILDCTL=/outside/buildctl\nMIDNIGHT_REGISTRY_BIN=/outside/registry\n"), nil
		}),
	}
	if result := runCheckDatabaseDSN(context.Background(), source); result.Status != StatusSkip {
		t.Fatalf("doctor followed package DSN: %+v", result)
	}
	if result := runCheckRouteConfigDirWritable(context.Background(), source); result.Status != StatusFail || !strings.Contains(result.Message, "not configured") {
		t.Fatalf("doctor followed package routes: %+v", result)
	}
	for _, check := range []func(preflight.HostSource) preflight.Result{
		preflight.CheckDatabaseDSNConfigured, preflight.CheckRouteConfigDirWritable, preflight.CheckRuntimeBinariesPresent,
	} {
		if result := check(source); result.Status != preflight.StatusSkip {
			t.Fatalf("preflight followed package path: %+v", result)
		}
	}
	if len(reads) == 0 {
		t.Fatal("test did not consume captured package configuration")
	}
}

type stubReader map[string][]byte

func (s stubReader) ReadFile(path string) ([]byte, error) {
	if data, ok := s[path]; ok {
		return data, nil
	}
	return nil, &fileNotFoundError{path: path}
}

type fileNotFoundError struct{ path string }

func (e *fileNotFoundError) Error() string { return "file not found: " + e.path }
func (e *fileNotFoundError) Is(target error) bool {
	// ensure the merge path's os.IsNotExist check for daemon.json
	// works when we want to assert "no daemon.json present" branch.
	return false
}

func TestLiveCheckRegistryHasUniqueOrCheckIDs(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	for _, c := range LiveCheckRegistry() {
		if c.ID == "" {
			t.Fatalf("LiveCheckRegistry has empty id")
		}
		if c.Description == "" {
			t.Fatalf("check %s has no description", c.ID)
		}
		if _, dup := seen[c.ID]; dup {
			t.Fatalf("duplicate check id %s", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	if len(seen) < 19 {
		t.Fatalf("expected at least 19 checks, got %d", len(seen))
	}
}

func TestRunLiveChecksCgroupV2Pass(t *testing.T) {
	t.Parallel()

	mountInfo := "26 25 0:23 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate,memory_recursiveprot\n"
	src := preflight.HostSource{
		Reader: stubReader{"/proc/self/mountinfo": []byte(mountInfo)},
		Paths:  preflight.HostPaths{MountInfo: "/proc/self/mountinfo"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-001"},
	})
	if len(results) != 1 || results[0].ID != "OR-CHK-DOC-001" {
		t.Fatalf("expected single OR-CHK-DOC-001 result, got %#v", results)
	}
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksCgroupV1Fails(t *testing.T) {
	t.Parallel()

	mountInfo := "26 25 0:23 / /sys/fs/cgroup/memory rw - cgroup memory rw,memory\n"
	src := preflight.HostSource{
		Reader: stubReader{"/proc/self/mountinfo": []byte(mountInfo)},
		Paths:  preflight.HostPaths{MountInfo: "/proc/self/mountinfo"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-001"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail on cgroup v1, got %s", results[0].Status)
	}
	if ExitCodeForResults(results) != 2 {
		t.Fatalf("expected exit 2 (fail), got %d", ExitCodeForResults(results))
	}
}

func TestRunLiveChecksDaemonJSONConflict(t *testing.T) {
	t.Parallel()

	// daemon.json that conflicts with the baseline log-driver.
	conflict := []byte(`{"log-driver": "syslog"}`)
	src := preflight.HostSource{
		Reader: stubReader{"/etc/docker/daemon.json": conflict},
		Paths:  preflight.HostPaths{DockerDaemonJSON: "/etc/docker/daemon.json"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-002"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail on daemon.json conflict, got %s (%s)", results[0].Status, results[0].Message)
	}
	if results[0].Message == "" {
		t.Fatalf("expected non-empty conflict message")
	}
}

func TestRunLiveChecksDockerAPISufficient(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Reader: stubReader{"/tmp/docker-version.txt": []byte("1.45")},
		Paths:  preflight.HostPaths{DockerAPIVersion: "/tmp/docker-version.txt"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-003"},
	})
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass on Docker API 1.45, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksDockerAPIBelowFloor(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Reader: stubReader{"/tmp/docker-version.txt": []byte("1.41")},
		Paths:  preflight.HostPaths{DockerAPIVersion: "/tmp/docker-version.txt"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-003"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail on Docker API < 1.43, got %s", results[0].Status)
	}
}

func TestRunLiveChecksIngressPortsFree(t *testing.T) {
	t.Parallel()

	output := "State    Recv-Q  Send-Q  Local Address:Port  Peer Address:Port\nLISTEN   0       4096    127.0.0.1:5432      0.0.0.0:*\n"
	src := preflight.HostSource{
		Reader: stubReader{"/tmp/ss-ltn.txt": []byte(output)},
		Paths:  preflight.HostPaths{SSLNT: "/tmp/ss-ltn.txt"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-004"},
	})
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass on free ingress ports, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksIngressPortsBound(t *testing.T) {
	t.Parallel()

	output := "State    Recv-Q  Send-Q  Local Address:Port  Peer Address:Port\nLISTEN   0       4096    0.0.0.0:80          0.0.0.0:*\n"
	src := preflight.HostSource{
		Reader: stubReader{"/tmp/ss-ltn.txt": []byte(output)},
		Paths:  preflight.HostPaths{SSLNT: "/tmp/ss-ltn.txt"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-004"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail on bound port 80, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksHints(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Hints: preflight.HostHints{
			FirewallMode:            "ufw",
			MandatoryAccessControl:  "apparmor-enforce",
			TimeSyncStatus:          "synchronized",
			HostnameFQDN:            "demo.midnight.local",
			InstalledDockerPackages: []string{"docker-ce", "docker-ce-cli"},
		},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: src,
		FilterIDs: []string{
			"OR-CHK-DOC-005",
			"OR-CHK-DOC-006",
			"OR-CHK-DOC-007",
			"OR-CHK-DOC-008",
			"OR-CHK-DOC-009",
		},
	})
	for _, r := range results {
		if r.Status != StatusPass {
			t.Fatalf("expected pass for %s, got %s (%s)", r.ID, r.Status, r.Message)
		}
	}
	if ExitCodeForResults(results) != 0 {
		t.Fatalf("expected exit 0, got %d", ExitCodeForResults(results))
	}
}

func TestRunLiveChecksDistroDockerIORejected(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Hints: preflight.HostHints{InstalledDockerPackages: []string{"docker.io"}},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-009"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail on docker.io, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksPackageConfigPass(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	buildkitd := filepath.Join(binDir, "buildkitd")
	buildctl := filepath.Join(binDir, "buildctl")
	registry := filepath.Join(binDir, "registry")
	for _, path := range []string{buildkitd, buildctl, registry} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	src := preflight.HostSource{
		Reader: stubReader{
			"/etc/midnight/db.dsn":       []byte("postgres://midnight@db/midnight\n"),
			"/etc/midnight/cursor.key":   []byte("12345678901234567890123456789012\n"),
			"/etc/midnight/registry.yml": []byte("version: 0.1\n"),
		},
		Paths: preflight.HostPaths{
			DatabaseDSN:    "/etc/midnight/db.dsn",
			CursorKey:      "/etc/midnight/cursor.key",
			RegistryConfig: "/etc/midnight/registry.yml",
		},
		Hints: preflight.HostHints{RuntimeBinaries: []string{buildkitd, buildctl, registry}},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: src,
		FilterIDs: []string{
			"OR-CHK-DOC-010",
			"OR-CHK-DOC-011",
			"OR-CHK-DOC-012",
			"OR-CHK-DOC-013",
		},
	})
	for _, r := range results {
		if r.Status != StatusPass {
			t.Fatalf("expected pass for %s, got %s (%s)", r.ID, r.Status, r.Message)
		}
	}
}

func TestRunLiveChecksPackageConfigFailsOnMissingRuntimeBinary(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Hints: preflight.HostHints{RuntimeBinaries: []string{filepath.Join(t.TempDir(), "missing-buildkitd")}},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-013"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for missing runtime binary, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksB1LiveDeployProofPasses(t *testing.T) {
	t.Parallel()

	routeDir := t.TempDir()
	src := preflight.HostSource{
		Reader: stubReader{"/etc/midnight/db.dsn": []byte("postgres://midnight@db/midnight\n")},
		Paths: preflight.HostPaths{
			DatabaseDSN:    "/etc/midnight/db.dsn",
			RouteConfigDir: routeDir,
		},
		Hints: preflight.HostHints{
			WorkerID:                "worker-a",
			WorkerRuntimeBackend:    "docker",
			WorkerDockerHost:        "unix:///run/docker.sock",
			WorkerAgentID:           "agent-a",
			WorkerHostID:            "host-a",
			WorkerRouteGroup:        "apps",
			WorkerRoutePublicHost:   "app.example.test",
			WorkerRoutePublicPath:   "/",
			WorkerRoutePublicTLS:    true,
			WorkerRouteCertResolver: "letsencrypt-staging",
			WorkerRouteTargetPort:   8080,
			BackupDestination:       preflight.SanitizeBackupDestination("s3://midnight-backups/platform"),
		},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: LiveDeployProofCheckIDs(),
	})
	if len(results) != len(LiveDeployProofCheckIDs()) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(LiveDeployProofCheckIDs()))
	}
	for _, r := range results {
		if r.Status != StatusPass {
			t.Fatalf("expected pass for %s, got %s (%s)", r.ID, r.Status, r.Message)
		}
	}
}

func TestRunLiveChecksBackupDestinationSkipsWhenUnavailable(t *testing.T) {
	t.Parallel()

	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    preflight.HostSource{},
		FilterIDs: []string{"OR-CHK-DOC-020"},
	})
	if results[0].Status != StatusSkip {
		t.Fatalf("expected skip for missing backup destination, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksBackupDestinationRejectsLocalURI(t *testing.T) {
	t.Parallel()

	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: preflight.HostSource{
			Hints: preflight.HostHints{BackupDestination: preflight.SanitizeBackupDestination("file:///var/lib/midnight/backups")},
		},
		FilterIDs: []string{"OR-CHK-DOC-020"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for local backup destination, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksBackupDestinationAcceptsOffhostURIFromPackageEnv(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Reader: stubReader{"/etc/midnight/midnight.env": []byte("MIDNIGHT_BACKUP_DESTINATION_URI=sftp://backup.example/midnight\n")},
		Paths:  preflight.HostPaths{PackageEnv: "/etc/midnight/midnight.env"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-020"},
	})
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass for off-host backup destination, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksBackupDestinationDoesNotRetainOrDiscloseCredentials(t *testing.T) {
	t.Parallel()
	const secret = "BACKUP-SECRET-SENTINEL"
	src := preflight.HostSource{
		Reader: stubReader{"/etc/midnight/midnight.env": []byte("MIDNIGHT_BACKUP_DESTINATION_URI=sftp://user:" + secret + "@backup.example/midnight\n")},
		Paths:  preflight.HostPaths{PackageEnv: "/etc/midnight/midnight.env"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: src, FilterIDs: []string{"OR-CHK-DOC-020"},
	})
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass, got %s (%s)", results[0].Status, results[0].Message)
	}
	if got := fmt.Sprintf("%#v", results[0]); strings.Contains(got, secret) || strings.Contains(got, "backup.example") {
		t.Fatalf("readiness result disclosed backup URI: %s", got)
	}
}

func TestRunLiveChecksB1LiveDeployProofRejectsLocalBackend(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Hints: preflight.HostHints{WorkerRuntimeBackend: "local"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-014"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for local backend, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksB1RouteConfigDirMustBeWritable(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "route-file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write route file: %v", err)
	}
	src := preflight.HostSource{Paths: preflight.HostPaths{RouteConfigDir: filePath}}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-015"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for file route dir, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksB1TraefikShapeAcceptsFlatArtifact(t *testing.T) {
	t.Parallel()

	routeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(routeDir, "apps.traefik.dynamic.yaml"), []byte("http: {}\n"), 0o600); err != nil {
		t.Fatalf("write flat artifact: %v", err)
	}
	src := preflight.HostSource{
		Paths: preflight.HostPaths{RouteConfigDir: routeDir},
		Hints: preflight.HostHints{WorkerRouteGroup: "apps"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-016"},
	})
	if results[0].Status != StatusPass {
		t.Fatalf("expected pass for flat artifact, got %s (%s)", results[0].Status, results[0].Message)
	}
	if !strings.Contains(results[0].Message, "apps.traefik.dynamic.yaml") {
		t.Fatalf("expected flat expected-path message, got %q", results[0].Message)
	}
}

func TestRunLiveChecksB1TraefikShapeRejectsUnwatchedSubdirectoryArtifact(t *testing.T) {
	t.Parallel()

	routeDir := t.TempDir()
	legacyDir := filepath.Join(routeDir, "apps")
	if err := os.MkdirAll(legacyDir, 0o750); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "traefik.dynamic.yaml"), []byte("http: {}\n"), 0o600); err != nil {
		t.Fatalf("write legacy artifact: %v", err)
	}
	src := preflight.HostSource{
		Paths: preflight.HostPaths{RouteConfigDir: routeDir},
		Hints: preflight.HostHints{WorkerRouteGroup: "apps"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-016"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for unwatched subdirectory artifact, got %s (%s)", results[0].Status, results[0].Message)
	}
	if !strings.Contains(results[0].Message, filepath.Join("apps", "traefik.dynamic.yaml")) {
		t.Fatalf("expected stale artifact named in message, got %q", results[0].Message)
	}
}

func TestRunLiveChecksB1WorkerRouteEnvironmentAllowsGeneratedHost(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Paths: preflight.HostPaths{RouteConfigDir: t.TempDir()},
		Hints: preflight.HostHints{
			WorkerID:              "worker-a",
			WorkerRouteGroup:      "apps",
			WorkerRoutePublicPath: "/",
			WorkerRouteTargetPort: 8080,
		},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-017"},
	})
	if results[0].Status != StatusWarn {
		t.Fatalf("expected usable generated-host config warning, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksInstallReadinessPassesFromPackageEnv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	routeDir := filepath.Join(root, "routes")
	unitDir := filepath.Join(root, "systemd")
	for _, dir := range []string{binDir, routeDir, unitDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	binaries := map[string]string{
		"MIDNIGHT_BUILDKITD_BIN": filepath.Join(binDir, "buildkitd"),
		"MIDNIGHT_BUILDCTL":      filepath.Join(binDir, "buildctl"),
		"MIDNIGHT_REGISTRY_BIN":  filepath.Join(binDir, "registry"),
	}
	for _, path := range binaries {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatalf("write binary %s: %v", path, err)
		}
	}
	dbDSN := filepath.Join(root, "db.dsn")
	if err := os.WriteFile(dbDSN, []byte("postgres://midnight@db/midnight\n"), 0o600); err != nil {
		t.Fatalf("write db dsn: %v", err)
	}
	envPath := filepath.Join(root, "midnight.env")
	env := "MIDNIGHT_DB_DSN_FILE=" + dbDSN + "\n" +
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=" + routeDir + "\n" +
		"MIDNIGHT_BUILDKITD_BIN=" + binaries["MIDNIGHT_BUILDKITD_BIN"] + "\n" +
		"MIDNIGHT_BUILDCTL=" + binaries["MIDNIGHT_BUILDCTL"] + "\n" +
		"MIDNIGHT_REGISTRY_BIN=" + binaries["MIDNIGHT_REGISTRY_BIN"] + "\n"
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	for _, unit := range preflight.RequiredSystemdUnitNames {
		if err := os.WriteFile(filepath.Join(unitDir, unit), []byte(minimalDoctorSystemdUnit()), 0o644); err != nil {
			t.Fatalf("write unit %s: %v", unit, err)
		}
	}

	src := preflight.HostSource{
		Reader: preflight.OSFileReader{},
		Paths: preflight.HostPaths{
			PackageEnv:     envPath,
			SystemdUnitDir: unitDir,
		},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: InstallReadinessCheckIDs(),
	})
	if len(results) != len(InstallReadinessCheckIDs()) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(InstallReadinessCheckIDs()))
	}
	for _, r := range results {
		if r.Status != StatusPass {
			t.Fatalf("expected pass for %s, got %s (%s)", r.ID, r.Status, r.Message)
		}
	}
}

func TestRunLiveChecksPackageEnvMissingFails(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Reader: preflight.OSFileReader{},
		Paths:  preflight.HostPaths{PackageEnv: filepath.Join(t.TempDir(), "missing.env")},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-018"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for missing package env, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksSystemdUnitsRejectMalformedUnit(t *testing.T) {
	t.Parallel()

	unitDir := t.TempDir()
	for _, unit := range preflight.RequiredSystemdUnitNames {
		content := minimalDoctorSystemdUnit()
		if unit == "midnight-worker.service" {
			content = "[Unit]\nDescription=broken\n"
		}
		if err := os.WriteFile(filepath.Join(unitDir, unit), []byte(content), 0o644); err != nil {
			t.Fatalf("write unit %s: %v", unit, err)
		}
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: preflight.HostSource{
			Reader: preflight.OSFileReader{},
			Paths:  preflight.HostPaths{SystemdUnitDir: unitDir},
		},
		FilterIDs: []string{"OR-CHK-DOC-019"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for malformed systemd unit, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func TestRunLiveChecksCursorKeyRejectsShortKey(t *testing.T) {
	t.Parallel()

	src := preflight.HostSource{
		Reader: stubReader{"/etc/midnight/cursor.key": []byte("too-short\n")},
		Paths:  preflight.HostPaths{CursorKey: "/etc/midnight/cursor.key"},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source:    src,
		FilterIDs: []string{"OR-CHK-DOC-011"},
	})
	if results[0].Status != StatusFail {
		t.Fatalf("expected fail for short cursor key, got %s (%s)", results[0].Status, results[0].Message)
	}
}

func minimalDoctorSystemdUnit() string {
	return "[Unit]\nDescription=fixture\n[Service]\nExecStart=/usr/bin/midnight fixture\n[Install]\nWantedBy=multi-user.target\n"
}

func TestExitCodePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []Result
		want  int
	}{
		{name: "all pass", input: []Result{{Status: StatusPass}, {Status: StatusPass}}, want: 0},
		{name: "warn only", input: []Result{{Status: StatusPass}, {Status: StatusWarn}}, want: 1},
		{name: "fail beats warn", input: []Result{{Status: StatusFail}, {Status: StatusWarn}}, want: 2},
		{name: "config_error wins", input: []Result{{Status: StatusFail}, {Status: StatusConfigError}}, want: 3},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitCodeForResults(tt.input); got != tt.want {
				t.Fatalf("ExitCodeForResults() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunLiveChecksIngressPortsReservedByOperatorIngress(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ssPath := filepath.Join(dir, "ss-ltn.txt")
	listeners := "State Recv-Q Send-Q Local Address:Port Peer Address:Port\nLISTEN 0 4096 *:80 *:*\nLISTEN 0 4096 *:443 *:*\n"
	if err := os.WriteFile(ssPath, []byte(listeners), 0o600); err != nil {
		t.Fatalf("write ss fixture: %v", err)
	}
	src := preflight.HostSource{
		Paths:  preflight.HostPaths{SSLNT: ssPath},
		Reader: preflight.OSFileReader{},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{Source: src, FilterIDs: []string{"OR-CHK-DOC-004"}})
	if results[0].Status != StatusFail {
		t.Fatalf("expected undeclared bound ports to fail, got %s (%s)", results[0].Status, results[0].Message)
	}

	src.Hints.IngressPortsReservedBy = "traefik"
	results = RunLiveChecks(context.Background(), LiveOptions{Source: src, FilterIDs: []string{"OR-CHK-DOC-004"}})
	if results[0].Status != StatusPass {
		t.Fatalf("expected declared reservation to pass, got %s (%s)", results[0].Status, results[0].Message)
	}
	if !strings.Contains(results[0].Message, "traefik") {
		t.Fatalf("expected the reserving service named in the message, got %q", results[0].Message)
	}
}
