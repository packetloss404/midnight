package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveHostSourceLiveAndEmpty verifies the legacy default: an
// empty value or `live` returns no override bundle so individual
// --*-fixture flags remain the only source of truth.
func TestResolveHostSourceLiveAndEmpty(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "live", "LIVE", " live "} {
		bundle, err := resolveHostSource(value)
		if err != nil {
			t.Fatalf("value %q: unexpected error %v", value, err)
		}
		if bundle != nil {
			t.Fatalf("value %q: expected nil bundle, got %#v", value, bundle)
		}
	}
}

// TestResolveHostSourceFixtureLoadsCanonicalNames verifies that
// `fixture:<dir>` populates the four canonical filenames plus
// hints.json. Files that are absent leave the corresponding bundle
// field empty so the caller's own --*-fixture / --*-mode flags win.
func TestResolveHostSourceFixtureLoadsCanonicalNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"mountinfo":          "26 25 0:23 / /sys/fs/cgroup rw,relatime - cgroup2 cgroup2 rw\n",
		"daemon.json":        `{"log-driver":"json-file"}` + "\n",
		"docker-version.txt": "1.45\n",
		"midnight.env":       "MIDNIGHT_DB_DSN_FILE=/etc/midnight/db.dsn\n",
		"db.dsn":             "postgres://midnight@db/midnight\n",
		"cursor.key":         "12345678901234567890123456789012\n",
		"registry.yml":       "version: 0.1\n",
		"ss-ltn.txt":         "State Recv-Q Send-Q Local Address:Port Peer Address:Port\n",
		"hints.json":         mustEncodeHints(t),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "traefik", "dynamic"), 0o755); err != nil {
		t.Fatalf("mkdir traefik dynamic dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "systemd"), 0o755); err != nil {
		t.Fatalf("mkdir systemd dir: %v", err)
	}

	bundle, err := resolveHostSource("fixture:" + dir)
	if err != nil {
		t.Fatalf("resolveHostSource: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle for fixture:<dir>")
	}
	if got, want := bundle.mountInfo, filepath.Join(dir, "mountinfo"); got != want {
		t.Errorf("mountInfo = %q, want %q", got, want)
	}
	if got, want := bundle.daemonJSON, filepath.Join(dir, "daemon.json"); got != want {
		t.Errorf("daemonJSON = %q, want %q", got, want)
	}
	if got, want := bundle.dockerVersion, filepath.Join(dir, "docker-version.txt"); got != want {
		t.Errorf("dockerVersion = %q, want %q", got, want)
	}
	if got, want := bundle.packageEnv, filepath.Join(dir, "midnight.env"); got != want {
		t.Errorf("packageEnv = %q, want %q", got, want)
	}
	if got, want := bundle.dbDSN, filepath.Join(dir, "db.dsn"); got != want {
		t.Errorf("dbDSN = %q, want %q", got, want)
	}
	if got, want := bundle.cursorKey, filepath.Join(dir, "cursor.key"); got != want {
		t.Errorf("cursorKey = %q, want %q", got, want)
	}
	if got, want := bundle.registryConfig, filepath.Join(dir, "registry.yml"); got != want {
		t.Errorf("registryConfig = %q, want %q", got, want)
	}
	if got, want := bundle.routeConfigDir, filepath.Join(dir, "traefik", "dynamic"); got != want {
		t.Errorf("routeConfigDir = %q, want %q", got, want)
	}
	if got, want := bundle.systemdUnitDir, filepath.Join(dir, "systemd"); got != want {
		t.Errorf("systemdUnitDir = %q, want %q", got, want)
	}
	if got, want := bundle.ssLNT, filepath.Join(dir, "ss-ltn.txt"); got != want {
		t.Errorf("ssLNT = %q, want %q", got, want)
	}
	if bundle.hints.FirewallMode != "ufw" {
		t.Errorf("hints.FirewallMode = %q, want ufw", bundle.hints.FirewallMode)
	}
	if bundle.hints.HostnameFQDN != "harness.midnight.local" {
		t.Errorf("hints.HostnameFQDN = %q, want harness.midnight.local", bundle.hints.HostnameFQDN)
	}
	if got := strings.Join(bundle.hints.InstalledDockerPackages, ","); got != "docker-ce,docker-ce-cli" {
		t.Errorf("hints.InstalledDockerPackages = %q, want docker-ce,docker-ce-cli", got)
	}
	if got := strings.Join(bundle.hints.RuntimeBinaries, ","); got != "buildkitd,buildctl,registry" {
		t.Errorf("hints.RuntimeBinaries = %q, want buildkitd,buildctl,registry", got)
	}
	if !bundle.hints.DatabaseDSNConfigured {
		t.Errorf("hints.DatabaseDSNConfigured = false, want true")
	}
	if bundle.hints.WorkerRuntimeBackend != "docker" {
		t.Errorf("hints.WorkerRuntimeBackend = %q, want docker", bundle.hints.WorkerRuntimeBackend)
	}
	if bundle.hints.WorkerRoutePublicHost != "app.example.test" {
		t.Errorf("hints.WorkerRoutePublicHost = %q, want app.example.test", bundle.hints.WorkerRoutePublicHost)
	}
	if got := bundle.hints.BackupDestination; !got.Configured || !got.URIValid || got.Scheme != "s3" {
		t.Errorf("hints.BackupDestination = %#v, want sanitized s3", got)
	}
}

func TestRunMVPReadinessFiltersChecks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbDSN := filepath.Join(dir, "db.dsn")
	if err := os.WriteFile(dbDSN, []byte("postgres://midnight@db/midnight\n"), 0o600); err != nil {
		t.Fatalf("write db dsn: %v", err)
	}
	routeDir := filepath.Join(dir, "traefik", "dynamic")
	if err := os.MkdirAll(routeDir, 0o755); err != nil {
		t.Fatalf("mkdir route dir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--json",
		"--mvp-readiness",
		"--db-dsn-fixture", dbDSN,
		"--worker-runtime-backend", "docker",
		"--worker-docker-host", "unix:///run/docker.sock",
		"--worker-id", "worker-a",
		"--worker-agent-id", "agent-a",
		"--worker-host-id", "host-a",
		"--route-config-dir", routeDir,
		"--route-group", "apps",
		"--route-public-host", "app.example.test",
		"--route-public-path", "/",
		"--route-public-tls",
		"--route-cert-resolver", "letsencrypt-staging",
		"--route-target-port", "8080",
		"--backup-destination-uri", "s3://midnight-backups/platform",
	}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 0 {
		t.Fatalf("run() error = %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var out struct {
		Results []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, stdout.String())
	}
	gotIDs := make([]string, 0, len(out.Results))
	for _, result := range out.Results {
		gotIDs = append(gotIDs, result.ID)
		if result.Status != "pass" {
			t.Fatalf("result %s status = %s, want pass", result.ID, result.Status)
		}
	}
	wantIDs := []string{"OR-CHK-DOC-010", "OR-CHK-DOC-014", "OR-CHK-DOC-015", "OR-CHK-DOC-016", "OR-CHK-DOC-017", "OR-CHK-DOC-020"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("ids = %v, want %v", gotIDs, wantIDs)
	}
}

func TestRunMVPReadinessLiveLoadsPackageEnvironmentWithoutDisclosingSecrets(t *testing.T) {
	dir := t.TempDir()
	dbDSN := filepath.Join(dir, "db.dsn")
	if err := os.WriteFile(dbDSN, []byte("postgres://midnight@db/midnight\n"), 0o600); err != nil {
		t.Fatalf("write db dsn: %v", err)
	}
	routeDir := filepath.Join(dir, "traefik", "dynamic")
	if err := os.MkdirAll(routeDir, 0o755); err != nil {
		t.Fatalf("mkdir route dir: %v", err)
	}

	const secret = "SECRET-SENTINEL-DO-NOT-PRINT"
	packageEnv := filepath.Join(dir, "midnight.env")
	contents := strings.Join([]string{
		"MIDNIGHT_DB_DSN_FILE=" + dbDSN,
		"MIDNIGHT_WORKER_ID=worker-package",
		"MIDNIGHT_WORKER_RUNTIME_BACKEND=docker",
		"MIDNIGHT_WORKER_DOCKER_HOST=unix:///run/docker.sock",
		"MIDNIGHT_WORKER_AGENT_ID=agent-package",
		"MIDNIGHT_WORKER_HOST_ID=host-package",
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=" + routeDir,
		"MIDNIGHT_WORKER_ROUTE_GROUP=apps",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST=app.example.test",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH=/",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=true",
		"MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER=letsencrypt",
		"MIDNIGHT_WORKER_ROUTE_TARGET_PORT=8080",
		"MIDNIGHT_BACKUP_DESTINATION_URI=sftp://user:" + secret + "@backup.example/midnight",
		`MIDNIGHT_API_BEARER_TOKEN='` + secret + ` $(touch /tmp/midnight-doctor-must-not-run)'`,
	}, "\n")
	if err := os.WriteFile(packageEnv, []byte(contents), 0o600); err != nil {
		t.Fatalf("write package env: %v", err)
	}
	t.Setenv("MIDNIGHT_ENV_FILE", packageEnv)
	t.Setenv("MIDNIGHT_BACKUP_DESTINATION_URI", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{"--json", "--mvp-readiness", "--live"}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 0 {
		t.Fatalf("run() error = %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("doctor output disclosed package secret: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	var out struct {
		Results []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, stdout.String())
	}
	for _, result := range out.Results {
		if result.Status != "pass" {
			t.Fatalf("result %s status = %s, want pass", result.ID, result.Status)
		}
	}
}

func TestDoctorConfigurationPrecedence(t *testing.T) {
	packageValues := map[string]string{"MIDNIGHT_WORKER_RUNTIME_BACKEND": "package"}
	t.Setenv("MIDNIGHT_WORKER_RUNTIME_BACKEND", "process")
	if got := resolveDoctorString("cli", true, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, packageValues, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, "default"); got != "cli" {
		t.Fatalf("explicit CLI value = %q, want cli", got)
	}
	if got := resolveDoctorString("", false, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, packageValues, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, "default"); got != "process" {
		t.Fatalf("process environment value = %q, want process", got)
	}
	t.Setenv("MIDNIGHT_WORKER_RUNTIME_BACKEND", "")
	if got := resolveDoctorString("", false, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, packageValues, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, "default"); got != "package" {
		t.Fatalf("package environment value = %q, want package", got)
	}
	delete(packageValues, "MIDNIGHT_WORKER_RUNTIME_BACKEND")
	if got := resolveDoctorString("", false, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, packageValues, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, "default"); got != "default" {
		t.Fatalf("default value = %q, want default", got)
	}
}

func TestDoctorDockerHostPrecedenceIncludesPackageDockerHost(t *testing.T) {
	packageValues := map[string]string{"DOCKER_HOST": "unix:///package/docker.sock"}
	t.Setenv("MIDNIGHT_WORKER_DOCKER_HOST", "")
	t.Setenv("DOCKER_HOST", "unix:///process/docker.sock")
	if got := resolveDoctorString("", false, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, packageValues, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, ""); got != "unix:///process/docker.sock" {
		t.Fatalf("process Docker host = %q, want process value", got)
	}
	t.Setenv("DOCKER_HOST", "")
	if got := resolveDoctorString("", false, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, packageValues, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, ""); got != "unix:///package/docker.sock" {
		t.Fatalf("package Docker host = %q, want package DOCKER_HOST", got)
	}
	if got := resolveDoctorString("unix:///cli/docker.sock", true, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, packageValues, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, ""); got != "unix:///cli/docker.sock" {
		t.Fatalf("CLI Docker host = %q, want CLI value", got)
	}
}

func TestRunRejectsMalformedPackageValueWithoutDisclosingIt(t *testing.T) {
	dir := t.TempDir()
	const secret = "SECRET-SENTINEL-DO-NOT-PRINT"
	packageEnv := filepath.Join(dir, "midnight.env")
	if err := os.WriteFile(packageEnv, []byte("MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS="+secret+"\n"), 0o600); err != nil {
		t.Fatalf("write package env: %v", err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"--json", "--mvp-readiness", "--live", "--package-env-fixture", packageEnv}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 3 {
		t.Fatalf("run() error = %v, want exit 3", err)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr disclosed invalid package value: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS") {
		t.Fatalf("stderr = %s, want safe variable name", stderr.String())
	}
}

func TestRunMVPReadinessRejectsInvalidBackupDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbDSN := filepath.Join(dir, "db.dsn")
	if err := os.WriteFile(dbDSN, []byte("postgres://midnight@db/midnight\n"), 0o600); err != nil {
		t.Fatalf("write db dsn: %v", err)
	}
	routeDir := filepath.Join(dir, "traefik", "dynamic")
	if err := os.MkdirAll(routeDir, 0o755); err != nil {
		t.Fatalf("mkdir route dir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--json",
		"--mvp-readiness",
		"--db-dsn-fixture", dbDSN,
		"--worker-runtime-backend", "docker",
		"--worker-id", "worker-a",
		"--worker-agent-id", "agent-a",
		"--worker-host-id", "host-a",
		"--route-config-dir", routeDir,
		"--route-group", "apps",
		"--route-public-host", "app.example.test",
		"--route-public-path", "/",
		"--route-target-port", "8080",
		"--backup-destination-uri", "file:///var/lib/midnight/backups",
	}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 2 {
		t.Fatalf("run() error = %v, want exit 2 stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"id": "OR-CHK-DOC-020"`) || !strings.Contains(stdout.String(), `"status": "fail"`) {
		t.Fatalf("stdout = %s, want failing backup destination check", stdout.String())
	}
}

func TestHelpUsesNeutralReadinessName(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run([]string{"--help"}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 3 {
		t.Fatalf("run() error = %v, want help exit 3", err)
	}
	help := stderr.String()
	if !strings.Contains(help, "-mvp-readiness") {
		t.Fatalf("help = %s, want --mvp-readiness", help)
	}
	for _, old := range []string{"-b1-live-deploy-proof", "-live-deploy-readiness"} {
		if strings.Contains(help, old) {
			t.Fatalf("help = %s, should not show old alias %s", help, old)
		}
	}
}

func TestLegacyReadinessAliasNormalizesToMVPReadiness(t *testing.T) {
	t.Parallel()

	got := strings.Join(normalizeDoctorArgs([]string{"--live-deploy-readiness=true"}), " ")
	if got != "--mvp-readiness=true" {
		t.Fatalf("normalized args = %q, want --mvp-readiness=true", got)
	}
}

func TestRunInstallReadinessFiltersChecks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	routeDir := filepath.Join(dir, "routes")
	unitDir := filepath.Join(dir, "systemd")
	for _, path := range []string{binDir, routeDir, unitDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	for _, name := range []string{"buildkitd", "buildctl", "registry"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("fixture"), 0o755); err != nil {
			t.Fatalf("write binary %s: %v", name, err)
		}
	}
	dbDSN := filepath.Join(dir, "db.dsn")
	if err := os.WriteFile(dbDSN, []byte("postgres://midnight@db/midnight\n"), 0o600); err != nil {
		t.Fatalf("write db dsn: %v", err)
	}
	envPath := filepath.Join(dir, "midnight.env")
	env := strings.Join([]string{
		"MIDNIGHT_DB_DSN_FILE=" + dbDSN,
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=" + routeDir,
		"MIDNIGHT_BUILDKITD_BIN=" + filepath.Join(binDir, "buildkitd"),
		"MIDNIGHT_BUILDCTL=" + filepath.Join(binDir, "buildctl"),
		"MIDNIGHT_REGISTRY_BIN=" + filepath.Join(binDir, "registry"),
		"",
	}, "\n")
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	for _, unit := range []string{
		"midnight-server.service",
		"midnight-worker.service",
		"midnight-agent.service",
		"midnight-buildkit.service",
		"midnight-buildkit-worker.service",
		"midnight-github-status-worker.service",
		"midnight-registry.service",
	} {
		if err := os.WriteFile(filepath.Join(unitDir, unit), []byte(testSystemdUnit()), 0o644); err != nil {
			t.Fatalf("write unit %s: %v", unit, err)
		}
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--json",
		"--install-readiness",
		"--live",
		"--package-env-fixture", envPath,
		"--systemd-unit-dir", unitDir,
	}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 0 {
		t.Fatalf("run() error = %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var out struct {
		Live    bool   `json:"live"`
		Profile string `json:"profile"`
		Results []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, stdout.String())
	}
	if !out.Live || out.Profile != "install-readiness" {
		t.Fatalf("doctor report live=%t profile=%q, want live install-readiness", out.Live, out.Profile)
	}
	gotIDs := make([]string, 0, len(out.Results))
	for _, result := range out.Results {
		gotIDs = append(gotIDs, result.ID)
		if result.Status != "pass" {
			t.Fatalf("result %s status = %s, want pass", result.ID, result.Status)
		}
	}
	wantIDs := []string{"OR-CHK-DOC-010", "OR-CHK-DOC-013", "OR-CHK-DOC-015", "OR-CHK-DOC-018", "OR-CHK-DOC-019"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("ids = %v, want %v", gotIDs, wantIDs)
	}
}

func TestReadinessProfilesAreMutuallyExclusive(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run([]string{"--json", "--install-readiness", "--mvp-readiness"}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 3 {
		t.Fatalf("run() error = %v, want config exit 3", err)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestExplicitCheckProfileRejectsUnknownEmptyAndDuplicateIDs(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"unknown":   "OR-CHK-DOC-999",
		"empty":     "",
		"trailing":  "OR-CHK-DOC-001,",
		"duplicate": "OR-CHK-DOC-001,OR-CHK-DOC-001",
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{"--json", "--check=" + value}, &stdout, &stderr)
			if exit, ok := err.(*exitError); !ok || exit.code != 3 {
				t.Fatalf("run() error = %v, want config exit 3", err)
			}
			if !strings.Contains(stderr.String(), "--check") || stdout.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestResolveHostSourceFixtureMissingDir(t *testing.T) {
	t.Parallel()
	if _, err := resolveHostSource("fixture:" + filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing fixture dir")
	}
}

func testSystemdUnit() string {
	return "[Unit]\nDescription=fixture\n[Service]\nExecStart=/usr/bin/midnight fixture\n[Install]\nWantedBy=multi-user.target\n"
}

func TestResolveHostSourceFixtureMissingPrefix(t *testing.T) {
	t.Parallel()
	if _, err := resolveHostSource("fictional"); err == nil {
		t.Fatal("expected error on unsupported --hostsource value")
	}
}

func TestResolveHostSourceFixtureCanonicalCgroupV1(t *testing.T) {
	t.Parallel()
	// Root-relative fixture lives under internal/doctor/testdata/cgroup-v1
	// and is consumed by the disposable-VM CI harness's negative-path job.
	// The test asserts the fixture is well-formed JSON and the mountinfo
	// describes a v1 hierarchy.
	root := filepath.Join("..", "..", "internal", "doctor", "testdata", "cgroup-v1")
	bundle, err := resolveHostSource("fixture:" + root)
	if err != nil {
		t.Fatalf("resolveHostSource: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	mountInfo, err := os.ReadFile(bundle.mountInfo)
	if err != nil {
		t.Fatalf("read mountinfo: %v", err)
	}
	if !bytes.Contains(mountInfo, []byte("- cgroup cgroup ")) {
		t.Errorf("expected v1 cgroup mount line in fixture; got %s", mountInfo)
	}
	if bytes.Contains(mountInfo, []byte("cgroup2")) {
		t.Errorf("cgroup-v1 fixture must not include a cgroup2 mount line")
	}
}

func mustEncodeHints(t *testing.T) string {
	t.Helper()
	hints := hostSourceHintsFile{
		FirewallMode:            "ufw",
		MandatoryAccessControl:  "apparmor-enforce",
		TimeSyncStatus:          "synchronized",
		HostnameFQDN:            "harness.midnight.local",
		InstalledDockerPackages: []string{"docker-ce", "docker-ce-cli"},
		RuntimeBinaries:         []string{"buildkitd", "buildctl", "registry"},
		DatabaseDSNConfigured:   true,
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
		BackupDestinationURI:    "s3://midnight-backups/platform",
	}
	data, err := json.Marshal(hints)
	if err != nil {
		t.Fatalf("encode hints: %v", err)
	}
	return string(data) + "\n"
}
