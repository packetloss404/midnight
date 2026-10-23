package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samplePassReport = `{
  "schema_version": 1,
  "live": true,
  "exit_code": 0,
  "results": [
    {"id":"OR-CHK-DOC-001","description":"cgroup v2","status":"pass","message":"cgroup v2 unified hierarchy detected"},
    {"id":"OR-CHK-DOC-002","description":"daemon.json","status":"pass","message":"daemon.json already matches the Midnight baseline"},
    {"id":"OR-CHK-DOC-003","description":"docker api","status":"pass"},
    {"id":"OR-CHK-DOC-004","description":"ports","status":"pass"},
    {"id":"OR-CHK-DOC-005","description":"firewall","status":"pass"},
    {"id":"OR-CHK-DOC-006","description":"mac","status":"pass"},
    {"id":"OR-CHK-DOC-007","description":"time-sync","status":"pass"},
    {"id":"OR-CHK-DOC-008","description":"hostname","status":"pass"},
    {"id":"OR-CHK-DOC-009","description":"distro","status":"pass"}
  ]
}`

const sampleNegativeCgroupV1 = `{
  "schema_version": 1,
  "live": true,
  "exit_code": 2,
  "results": [
    {"id":"OR-CHK-DOC-001","description":"cgroup v2","status":"fail","message":"host runs cgroup v1; ADR-005 hard-requires cgroup v2"},
    {"id":"OR-CHK-DOC-002","description":"daemon.json","status":"pass"}
  ]
}`

func TestParseDoctorReportRejectsBadSchemaVersion(t *testing.T) {
	t.Parallel()
	_, err := ParseDoctorReport([]byte(`{"schema_version": 2}`))
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("expected schema_version error, got %v", err)
	}
}

func TestParseDoctorReportEmptyInputErrors(t *testing.T) {
	t.Parallel()
	if _, err := ParseDoctorReport(nil); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestParseDoctorReportSucceeds(t *testing.T) {
	t.Parallel()
	report, err := ParseDoctorReport([]byte(samplePassReport))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if report.SchemaVersion != 1 || report.ExitCode != 0 {
		t.Errorf("unexpected report header: %+v", report)
	}
	if len(report.Results) != 9 {
		t.Fatalf("expected 9 results, got %d", len(report.Results))
	}
}

func TestMissingExpectedChecks(t *testing.T) {
	t.Parallel()
	report, err := ParseDoctorReport([]byte(sampleNegativeCgroupV1))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	missing := MissingExpectedChecks(report, expectedCheckIDs)
	if len(missing) != 7 {
		t.Errorf("expected 7 missing ids in negative-path fixture, got %v", missing)
	}
}

func TestNonPassingChecksReportsCgroupV1(t *testing.T) {
	t.Parallel()
	report, err := ParseDoctorReport([]byte(sampleNegativeCgroupV1))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bad := NonPassingChecks(report, []string{"OR-CHK-DOC-001", "OR-CHK-DOC-002"})
	if len(bad) != 1 || !strings.HasPrefix(bad[0], "OR-CHK-DOC-001=") {
		t.Errorf("expected OR-CHK-DOC-001 non-pass, got %v", bad)
	}
}

func TestChecksContainsFailMatchesId(t *testing.T) {
	t.Parallel()
	report, err := ParseDoctorReport([]byte(sampleNegativeCgroupV1))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ChecksContainsFail(report, "OR-CHK-DOC-001") {
		t.Error("expected ChecksContainsFail(OR-CHK-DOC-001) = true")
	}
	if ChecksContainsFail(report, "OR-CHK-DOC-002") {
		t.Error("expected ChecksContainsFail(OR-CHK-DOC-002) = false")
	}
}

func TestExpectedCheckIDsCoverPanelLockedRegistry(t *testing.T) {
	t.Parallel()
	if len(expectedCheckIDs) != 9 {
		t.Errorf("expectedCheckIDs must hold 9 ids (panel-locked OR-CHK-DOC-001..009); got %d", len(expectedCheckIDs))
	}
	for i, id := range expectedCheckIDs {
		want := "OR-CHK-DOC-00" + string(rune('1'+i))
		if id != want {
			t.Errorf("expectedCheckIDs[%d] = %q, want %q", i, id, want)
		}
	}
}

func TestPackageCanaryPassesWithInstalledFilesAndBootedControlPlane(t *testing.T) {
	root := writePackageCanaryRoot(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/compat":
			_, _ = w.Write([]byte(`{"server_version":"0.1.0-alpha","api_version":"v1alpha","schema_version":1}`))
		case "/api/v1alpha/migrations":
			_, _ = w.Write([]byte(`{"latest_version":8,"applied_count":8,"pending_count":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := runPackageCanary(context.Background(), packageCanaryConfig{
		Root:      root,
		ServerURL: server.URL,
		Client:    server.Client(),
	})
	if len(result.Failures) != 0 {
		t.Fatalf("package canary failures = %v", result.Failures)
	}
	if len(result.Cases) != len(expectedPackagePaths)+2 {
		t.Fatalf("junit cases = %d, want %d", len(result.Cases), len(expectedPackagePaths)+2)
	}
}

func TestPackageCanaryRequiresBootedControlPlaneURL(t *testing.T) {
	root := writePackageCanaryRoot(t)
	result := runPackageCanary(context.Background(), packageCanaryConfig{Root: root})
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "--server-url") {
		t.Fatalf("failures = %v, want --server-url failure", result.Failures)
	}
}

func TestPackageCanaryFailsPendingMigrations(t *testing.T) {
	root := writePackageCanaryRoot(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/compat":
			_, _ = w.Write([]byte(`{"server_version":"0.1.0-alpha","api_version":"v1alpha","schema_version":1}`))
		case "/api/v1alpha/migrations":
			_, _ = w.Write([]byte(`{"latest_version":8,"applied_count":7,"pending_count":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := runPackageCanary(context.Background(), packageCanaryConfig{
		Root:      root,
		ServerURL: server.URL,
		Client:    server.Client(),
	})
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "pending_count=1") {
		t.Fatalf("failures = %v, want pending_count failure", result.Failures)
	}
}

func writePackageCanaryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, expected := range expectedPackagePaths {
		path := packagePath(root, expected.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		mode := os.FileMode(0o644)
		if expected.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte("ok\n"), mode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}
