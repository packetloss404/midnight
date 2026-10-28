package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func installDoctorProbeCommands(t *testing.T, dockerBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixed-argv OS runner contract uses POSIX executable fixtures")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDNIGHT_TEST_DOCTOR_LOG", dir) // not-a-failure-code
	t.Setenv("MIDNIGHT_DOCTOR_LIVE", "")
	for name, body := range map[string]string{"stat": "printf 'cgroup2fs\\n'\n", "docker": dockerBody} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MIDNIGHT_TEST_DOCTOR_LOG/" + name + ".args\"\n" + body
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func doctorProbeReport(t *testing.T, args ...string) (int, []wireResult) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(append([]string{"--json"}, args...), &stdout, &stderr)
	code := 0
	if exit, ok := err.(*exitError); ok {
		code = exit.code
	} else if err != nil {
		t.Fatalf("doctor failed: %v stderr=%s", err, stderr.String())
	}
	var report struct {
		Results []wireResult `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor JSON=%s stderr=%s err=%v", stdout.String(), stderr.String(), err)
	}
	return code, report.Results
}

func TestRunLiveReadProbesInvokeFixedOSCommands(t *testing.T) {
	dir := installDoctorProbeCommands(t, "printf '1.55\\n'\n")
	env := filepath.Join(dir, "midnight.env")
	if err := os.WriteFile(env, []byte("MIDNIGHT_WORKER_RUNTIME_BACKEND=docker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, results := doctorProbeReport(t, "--live", "--check", "OR-CHK-DOC-001,OR-CHK-DOC-003", "--package-env-fixture", env)
	if code != 0 || len(results) != 2 {
		t.Fatalf("live read probes: code=%d results=%+v", code, results)
	}
	for _, result := range results {
		if result.Status != "pass" {
			t.Fatalf("live read probe skipped/failed: %+v", result)
		}
	}
	for name, want := range map[string]string{"stat": "-fc\n%T\n/sys/fs/cgroup\n", "docker": "version\n--format\n{{.Server.APIVersion}}\n"} {
		actual, err := os.ReadFile(filepath.Join(dir, name+".args"))
		if err != nil || string(actual) != want {
			t.Fatalf("%s argv=%q err=%v, want %q", name, actual, err, want)
		}
	}
}

func TestRunFixtureModeNeverInvokesHostCommands(t *testing.T) {
	dir := installDoctorProbeCommands(t, "exit 97\n")
	_, results := doctorProbeReport(t, "--check", "OR-CHK-DOC-001,OR-CHK-DOC-003")
	for _, result := range results {
		if result.Status != "skip" {
			t.Fatalf("non-live command was not skipped: %+v", result)
		}
	}
	fixture := t.TempDir()
	for name, content := range map[string]string{
		"mountinfo":          "26 25 0:23 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
		"docker-version.txt": "1.55\n",
	} {
		if err := os.WriteFile(filepath.Join(fixture, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, results := doctorProbeReport(t, "--live", "--hostsource", "fixture:"+fixture, "--check", "OR-CHK-DOC-001,OR-CHK-DOC-003")
	if code != 0 || len(results) != 2 {
		t.Fatalf("fixture checks: code=%d results=%+v", code, results)
	}
	for _, result := range results {
		if result.Status != "pass" {
			t.Fatalf("fixture check failed: %+v", result)
		}
	}
	for _, name := range []string{"stat", "docker"} {
		if _, err := os.Stat(filepath.Join(dir, name+".args")); !os.IsNotExist(err) {
			t.Fatalf("fixture mode ran host %s: %v", name, err)
		}
	}
}

func TestRunLiveDockerProbeErrorCannotBecomeSkipOrHealthy(t *testing.T) {
	dir := installDoctorProbeCommands(t, "exit 7\n")
	env := filepath.Join(dir, "midnight.env")
	if err := os.WriteFile(env, []byte("MIDNIGHT_WORKER_RUNTIME_BACKEND=docker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, results := doctorProbeReport(t, "--live", "--check", "OR-CHK-DOC-003", "--package-env-fixture", env)
	if code != 3 || len(results) != 1 || results[0].Status != "config_error" {
		t.Fatalf("failed Docker command reported healthy/skipped: code=%d results=%+v", code, results)
	}
	if !strings.Contains(results[0].Error, "exit status 7") {
		t.Fatalf("actual command error was lost: %+v", results[0])
	}
}

func TestRunFixtureBundleDoesNotFollowAmbientOrPackagePaths(t *testing.T) {
	commands := installDoctorProbeCommands(t, "exit 97\n")
	outside := t.TempDir()
	routes := filepath.Join(outside, "routes")
	if err := os.Mkdir(routes, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR": routes,
		"MIDNIGHT_DB_DSN_FILE":             filepath.Join(outside, "db.dsn"),
		"MIDNIGHT_CURSOR_KEY_PATH":         filepath.Join(outside, "cursor.key"),
		"MIDNIGHT_REGISTRY_CONFIG":         filepath.Join(outside, "registry.yml"),
		"MIDNIGHT_SYSTEMD_UNIT_DIR":        outside,
		"MIDNIGHT_BUILDKITD_BIN":           filepath.Join(commands, "docker"),
		"MIDNIGHT_BUILDCTL":                filepath.Join(commands, "docker"),
		"MIDNIGHT_REGISTRY_BIN":            filepath.Join(commands, "docker"),
	}
	for name, content := range map[string]string{
		"db.dsn":     "postgres://fixture@database.invalid/fixture\n",
		"cursor.key": strings.Repeat("k", 32), "registry.yml": "version: 0.1\n",
	} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MIDNIGHT_DB_DSN", "")
	t.Setenv("MIDNIGHT_DATABASE_URL", "")
	for _, mode := range []string{"ambient", "captured-package"} {
		t.Run(mode, func(t *testing.T) {
			fixture := t.TempDir()
			var lines []string
			for key, path := range paths {
				value := ""
				if mode == "ambient" {
					value = path
				}
				t.Setenv(key, value)
				lines = append(lines, key+"="+path)
			}
			if mode == "captured-package" {
				if err := os.WriteFile(filepath.Join(fixture, "midnight.env"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// A create/remove probe changes the parent directory's modification
			// time even when it removes its temporary artifact successfully.
			old := time.Unix(946684800, 0)
			if err := os.Chtimes(routes, old, old); err != nil {
				t.Fatal(err)
			}
			code, results := doctorProbeReport(t, "--live", "--hostsource", "fixture:"+fixture, "--check",
				"OR-CHK-DOC-001,OR-CHK-DOC-003,OR-CHK-DOC-010,OR-CHK-DOC-011,OR-CHK-DOC-012,OR-CHK-DOC-013,OR-CHK-DOC-015,OR-CHK-DOC-019")
			if code != 2 || len(results) != 8 {
				t.Fatalf("incomplete fixture: code=%d results=%+v", code, results)
			}
			for _, result := range results {
				want := "skip"
				if result.ID == "OR-CHK-DOC-015" {
					want = "fail"
				}
				if result.Status != want {
					t.Fatalf("fixture borrowed outside path: %+v, want %s", result, want)
				}
			}
			info, err := os.Stat(routes)
			if err != nil || !info.ModTime().Equal(old) {
				t.Fatalf("fixture wrote outside route directory: info=%v err=%v", info, err)
			}
			// Explicit CLI overrides remain usable and are the only paths
			// authorized for this test-owned writable directory probe.
			explicitRoutes := t.TempDir()
			code, results = doctorProbeReport(t, "--hostsource", "fixture:"+fixture, "--check", "OR-CHK-DOC-010,OR-CHK-DOC-015",
				"--db-dsn-fixture", paths["MIDNIGHT_DB_DSN_FILE"], "--route-config-dir", explicitRoutes)
			if code != 0 || len(results) != 2 || results[0].Status != "pass" || results[1].Status != "pass" {
				t.Fatalf("explicit fixture overrides failed: code=%d results=%+v", code, results)
			}
		})
	}
	for _, name := range []string{"stat", "docker"} {
		if _, err := os.Stat(filepath.Join(commands, name+".args")); !os.IsNotExist(err) {
			t.Fatalf("fixture mode ran host %s: %v", name, err)
		}
	}
}
