package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallReadinessChecksPassWithFixtureSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbDSN := filepath.Join(dir, "db.dsn")
	routeDir := filepath.Join(dir, "routes")
	binDir := filepath.Join(dir, "bin")
	unitDir := filepath.Join(dir, "systemd")
	if err := os.MkdirAll(routeDir, 0o755); err != nil {
		t.Fatalf("mkdir route dir: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	for _, name := range []string{"buildkitd", "buildctl", "registry"} {
		writeFixture(t, filepath.Join(binDir, name), "fixture")
	}
	writeFixture(t, dbDSN, "postgres://midnight@db/midnight\n")
	env := strings.Join([]string{
		"MIDNIGHT_DB_DSN_FILE=" + dbDSN,
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=" + routeDir,
		"MIDNIGHT_BUILDKITD_BIN=" + filepath.Join(binDir, "buildkitd"),
		"MIDNIGHT_BUILDCTL=" + filepath.Join(binDir, "buildctl"),
		"MIDNIGHT_REGISTRY_BIN=" + filepath.Join(binDir, "registry"),
		"",
	}, "\n")
	writeFixture(t, filepath.Join(dir, "midnight.env"), env)
	for _, unit := range RequiredSystemdUnitNames {
		writeFixture(t, filepath.Join(unitDir, unit), minimalSystemdUnit())
	}

	report := NewReport(RunChecks(context.Background(), InstallReadinessChecks(HostSource{
		Reader: OSFileReader{},
		Paths: HostPaths{
			PackageEnv:     filepath.Join(dir, "midnight.env"),
			SystemdUnitDir: unitDir,
		},
	})))
	if report.Status != StatusPass {
		t.Fatalf("Status = %q, want pass; report = %+v", report.Status, report)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("len(Checks) = %d, want 5", len(report.Checks))
	}
}

func TestInstallReadinessRejectsRelativeRouteDir(t *testing.T) {
	t.Parallel()

	result := CheckRouteConfigDirWritable(HostSource{Paths: HostPaths{RouteConfigDir: "relative/routes"}})
	if result.Status != StatusFail {
		t.Fatalf("Status = %q, want fail", result.Status)
	}
	if !strings.Contains(result.Message, "absolute") {
		t.Fatalf("Message = %q, want absolute-path failure", result.Message)
	}
}

func TestInstallReadinessSystemdUnitsReportMissingUnit(t *testing.T) {
	t.Parallel()

	unitDir := t.TempDir()
	for _, unit := range RequiredSystemdUnitNames[:len(RequiredSystemdUnitNames)-1] {
		writeFixture(t, filepath.Join(unitDir, unit), minimalSystemdUnit())
	}
	result := CheckSystemdUnitsLoadable(HostSource{
		Reader: OSFileReader{},
		Paths:  HostPaths{SystemdUnitDir: unitDir},
	})
	if result.Status != StatusFail {
		t.Fatalf("Status = %q, want fail", result.Status)
	}
	if !strings.Contains(result.Message, "midnight-registry.service missing") {
		t.Fatalf("Message = %q, want missing registry unit", result.Message)
	}
}

func TestInstallReadinessRuntimeBinariesRequireAllEnvKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "buildkitd")
	writeFixture(t, bin, "fixture")
	result := CheckRuntimeBinariesPresent(HostSource{
		Reader: fakeFileReader{"midnight.env": []byte("MIDNIGHT_BUILDKITD_BIN=" + bin + "\n")},
		Paths:  HostPaths{PackageEnv: "midnight.env"},
	})
	if result.Status != StatusFail {
		t.Fatalf("Status = %q, want fail", result.Status)
	}
	if !strings.Contains(result.Message, "buildctl") || !strings.Contains(result.Message, "registry") {
		t.Fatalf("Message = %q, want missing buildctl and registry", result.Message)
	}
}

func minimalSystemdUnit() string {
	return "[Unit]\nDescription=fixture\n[Service]\nExecStart=/usr/bin/midnight fixture\n[Install]\nWantedBy=multi-user.target\n"
}
