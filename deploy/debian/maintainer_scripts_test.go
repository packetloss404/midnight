package debian_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostinstRunsScopedLiveDoctorAndPreservesWarningPolicy(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("postinst")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "run_postinstall_doctor() {")
	if start < 0 {
		t.Fatal("could not locate post-install doctor function")
	}
	endMarker := "\n}\n\ncase \"$1\" in"
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		t.Fatal("could not locate post-install doctor function")
	}
	functionText := text[start : start+end+2]
	fakeDoctor := filepath.Join(dir, "midnight-doctor")
	argsPath := filepath.Join(dir, "doctor.args")
	reportPath := filepath.Join(dir, "doctor-postinst.json")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$FAKE_DOCTOR_ARGS\"\nprintf '{\"schema_version\":1,\"live\":true,\"profile\":\"install-readiness\",\"exit_code\":%s,\"results\":[]}\\n' \"$FAKE_DOCTOR_EXIT\"\nexit \"$FAKE_DOCTOR_EXIT\"\n"
	if err := os.WriteFile(fakeDoctor, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	functionText = strings.ReplaceAll(functionText, "/usr/bin/midnight-doctor", fakeDoctor)
	functionText = strings.ReplaceAll(functionText, "/var/lib/midnight/doctor-postinst.json", reportPath)
	harness := filepath.Join(dir, "postinst-doctor-harness")
	if err := os.WriteFile(harness, []byte("#!/bin/sh\nset -e\n"+functionText+"\nrun_postinstall_doctor\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		doctor   string
		wantExit int
		warning  bool
	}{
		{name: "pass", doctor: "0", wantExit: 0},
		{name: "warning remains non-failing", doctor: "1", wantExit: 0, warning: true},
		{name: "fatal propagates", doctor: "2", wantExit: 2},
		{name: "configuration error propagates", doctor: "3", wantExit: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command("sh", harness)
			cmd.Env = append(os.Environ(), "FAKE_DOCTOR_ARGS="+argsPath, "FAKE_DOCTOR_EXIT="+test.doctor)
			output, runErr := cmd.CombinedOutput()
			gotExit := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) {
					t.Fatalf("run harness: %v", runErr)
				}
				gotExit = exitErr.ExitCode()
			}
			if gotExit != test.wantExit {
				t.Fatalf("exit=%d want=%d output=%s", gotExit, test.wantExit, output)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(args)) != "--json --install-readiness --live" {
				t.Fatalf("doctor args = %q", args)
			}
			if test.warning != strings.Contains(string(output), "package configuration continues") {
				t.Fatalf("warning output = %q", output)
			}
		})
	}
}

func TestUpgradeSnapshotsActiveUnitsBeforeStoppingThem(t *testing.T) {
	dir := t.TempDir()
	activePath := filepath.Join(dir, "active")
	logPath := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(activePath, []byte("midnight-server.service\nmidnight-agent.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBin := writeLifecycleFakes(t, dir)

	prermData, err := os.ReadFile("prerm")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	prermText := strings.Replace(string(prermData), "UPGRADE_STATE_DIR=/run/midnight-package-upgrade", "UPGRADE_STATE_DIR="+stateDir, 1)
	prermText = strings.Replace(prermText, `install -d -m 0700 -o root -g root "$UPGRADE_STATE_DIR"`, `mkdir -p "$UPGRADE_STATE_DIR"`, 1)
	prermText = strings.Replace(prermText, `chown root:root "$active_tmp"`, `:`, 1)
	prermPath := filepath.Join(dir, "prerm")
	if err := os.WriteFile(prermPath, []byte(prermText), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", prermPath, "upgrade", "0.2.0")
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "FAKE_ACTIVE="+activePath, "FAKE_LOG="+logPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prerm upgrade: %v\n%s", err, output)
	}
	snapshot, err := os.ReadFile(filepath.Join(stateDir, "active-units"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(snapshot), "midnight-server.service\nmidnight-agent.service\n"; got != want {
		t.Fatalf("active snapshot = %q, want %q", got, want)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantStop := "stop midnight-server.service midnight-worker.service midnight-agent.service midnight-buildkit-worker.service midnight-buildkit.service midnight-registry.service midnight-github-status-worker.service"
	if got := strings.TrimSpace(string(logData)); got != wantStop {
		t.Fatalf("systemctl stop transaction = %q, want %q", got, wantStop)
	}

	// A retry sees the services stopped, but must preserve the original active
	// set captured by the first transaction.
	if err := os.WriteFile(activePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("sh", prermPath, "upgrade", "0.2.0")
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "FAKE_ACTIVE="+activePath, "FAKE_LOG="+logPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prerm upgrade retry: %v\n%s", err, output)
	}
	snapshot, err = os.ReadFile(filepath.Join(stateDir, "active-units"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(snapshot), "midnight-server.service\nmidnight-agent.service\n"; got != want {
		t.Fatalf("retry active snapshot = %q, want preserved %q", got, want)
	}
}

func TestPostinstRejectsUnknownSnapshotBeforeStoppingUnits(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "active-units"), []byte("caller-chosen.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBin := writeLifecycleFakes(t, dir)
	logPath := filepath.Join(dir, "systemctl.log")
	harness := writeUpgradeLifecycleHarness(t, dir, stateDir)
	cmd := exec.Command("sh", harness)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "FAKE_LOG="+logPath)
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "unknown unit") {
		t.Fatalf("unsafe postinst snapshot result err=%v output=%s", err, output)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("unsafe snapshot reached systemctl before validation: %v", err)
	}
}

func TestUpgradeRestartUsesSnapshotAndRetainsItOnFailure(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "active-units")
	writeSnapshot := func() {
		t.Helper()
		if err := os.WriteFile(statePath, []byte("midnight-server.service\nmidnight-agent.service\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSnapshot()
	fakeBin := writeLifecycleFakes(t, dir)
	logPath := filepath.Join(dir, "systemctl.log")
	harness := writeRestartHarness(t, dir, stateDir)

	cmd := exec.Command("sh", harness)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "FAKE_LOG="+logPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart harness: %v\n%s", err, output)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("snapshot was not removed after successful restart: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logData)), "start midnight-server.service midnight-agent.service"; got != want {
		t.Fatalf("restart transaction = %q, want %q", got, want)
	}

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSnapshot()
	cmd = exec.Command("sh", harness)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "FAKE_LOG="+logPath, "FAKE_START_FAIL=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("restart harness succeeded despite injected start failure:\n%s", output)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("snapshot not retained after failed restart: %v", err)
	}
}

func TestAlpha2BridgeCapturesActiveUnitsAndNewPostinstRecoversFailure(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	activePath := filepath.Join(dir, "active")
	logPath := filepath.Join(dir, "lifecycle.log")
	packagePath := filepath.Join(dir, "midnight-alpha3.deb")
	if err := os.WriteFile(activePath, []byte("midnight-server.service\nmidnight-agent.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packagePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	postinstHarness := writeUpgradeLifecycleHarness(t, dir, stateDir)
	oldPrerm := filepath.Join(dir, "alpha2-prerm")
	if err := os.WriteFile(oldPrerm, []byte("#!/bin/sh\nprintf 'old-alpha2-prerm %s\\n' \"$1\" >>\"$FAKE_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeBin := writeAlpha2BridgeFakes(t, dir)
	bridge := writeAlpha2BridgeHarness(t, dir, stateDir, fakeBin)
	baseEnv := append(os.Environ(),
		"FAKE_ACTIVE="+activePath,
		"FAKE_LOG="+logPath,
		"FAKE_STATE_DIR="+stateDir,
		"FAKE_OLD_PRERM="+oldPrerm,
		"FAKE_NEW_POSTINST="+postinstHarness,
	)

	// A failed daemon-reload must leave every service stopped and retain the
	// original alpha-2 active set for an idempotent configuration retry.
	cmd := exec.Command("sh", bridge, packagePath)
	cmd.Env = append(baseEnv, "FAKE_RELOAD_FAIL=1")
	firstOutput, firstErr := cmd.CombinedOutput()
	if firstErr == nil {
		t.Fatalf("alpha-2 bridge succeeded despite reload failure:\n%s", firstOutput)
	}
	snapshotPath := filepath.Join(stateDir, "active-units")
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		logData, _ := os.ReadFile(logPath)
		t.Fatalf("read retained snapshot: %v\nbridge output=%s\nlifecycle log=%s", err, firstOutput, logData)
	}
	if got, want := string(snapshot), "midnight-server.service\nmidnight-agent.service\n"; got != want {
		t.Fatalf("bridge snapshot = %q, want %q", got, want)
	}
	firstLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(firstLog), "start ") {
		t.Fatalf("reload failure restarted a unit:\n%s", firstLog)
	}
	if !strings.Contains(string(firstLog), "stop midnight-server.service midnight-worker.service midnight-agent.service midnight-buildkit-worker.service midnight-buildkit.service midnight-registry.service midnight-github-status-worker.service\n") {
		t.Fatalf("new postinst did not quiesce fixed allowlist in one transaction:\n%s", firstLog)
	}

	// Simulate the stopped state seen by a retry. The bridge must preserve the
	// first snapshot rather than infer activity from enablement or overwrite it.
	if err := os.WriteFile(activePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("sh", bridge, packagePath)
	cmd.Env = baseEnv
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("alpha-2 bridge recovery retry: %v\n%s", err, output)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("successful recovery did not consume snapshot: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(logData); strings.Contains(got, "is-enabled") || !strings.Contains(got, "start midnight-server.service midnight-agent.service\n") {
		t.Fatalf("bridge inferred enablement or failed to restart original active set:\n%s", got)
	}
}

func TestAlpha2BridgeRequiresRootBeforeDpkg(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	fakeBin := writeAlpha2BridgeFakes(t, dir)
	bridge := writeAlpha2BridgeHarness(t, dir, stateDir, fakeBin)
	packagePath := filepath.Join(dir, "midnight-alpha3.deb")
	if err := os.WriteFile(packagePath, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "lifecycle.log")
	cmd := exec.Command("sh", bridge, packagePath)
	cmd.Env = append(os.Environ(), "FAKE_UID=1000", "FAKE_LOG="+logPath, "FAKE_STATE_DIR="+stateDir)
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "must run as root") {
		t.Fatalf("non-root bridge result err=%v output=%s", err, output)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("non-root bridge reached dpkg/systemctl: %v", err)
	}
}

func writeRestartHarness(t *testing.T, dir, stateDir string) string {
	t.Helper()
	data, err := os.ReadFile("postinst")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "validate_upgrade_snapshot() {")
	end := strings.Index(text[start:], "\ncase \"$1\" in")
	if start < 0 || end < 0 {
		t.Fatal("could not locate upgrade snapshot functions in postinst")
	}
	functionText := text[start : start+end]
	harness := strings.Join([]string{
		"#!/bin/sh",
		"set -e",
		"UPGRADE_STATE_DIR=" + stateDir,
		`UPGRADE_ACTIVE_UNITS="$UPGRADE_STATE_DIR/active-units"`,
		`MIDNIGHT_UNIT_FILES="midnight-server.service midnight-worker.service midnight-agent.service midnight-buildkit-worker.service midnight-buildkit.service midnight-registry.service midnight-github-status-worker.service"`,
		functionText,
		"restart_previously_active_units",
	}, "\n")
	path := filepath.Join(dir, "restart-harness")
	if err := os.WriteFile(path, []byte(harness), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeUpgradeLifecycleHarness(t *testing.T, dir, stateDir string) string {
	t.Helper()
	data, err := os.ReadFile("postinst")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "stop_midnight_units() {")
	end := strings.Index(text[start:], "\ncase \"$1\" in")
	if start < 0 || end < 0 {
		t.Fatal("could not locate lifecycle functions in postinst")
	}
	functionText := text[start : start+end]
	harness := strings.Join([]string{
		"#!/bin/sh",
		"set -e",
		"UPGRADE_STATE_DIR=" + stateDir,
		`UPGRADE_ACTIVE_UNITS="$UPGRADE_STATE_DIR/active-units"`,
		`MIDNIGHT_UNIT_FILES="midnight-server.service midnight-worker.service midnight-agent.service midnight-buildkit-worker.service midnight-buildkit.service midnight-registry.service midnight-github-status-worker.service"`,
		functionText,
		"quiesce_upgrade_units",
		"reload_systemd_units",
		"restart_previously_active_units",
	}, "\n")
	path := filepath.Join(dir, "upgrade-lifecycle-harness")
	if err := os.WriteFile(path, []byte(harness), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAlpha2BridgeHarness(t *testing.T, dir, stateDir, fakeBin string) string {
	t.Helper()
	data, err := os.ReadFile("upgrade-alpha2-bridge")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "PATH=/usr/sbin:/usr/bin:/sbin:/bin", "PATH="+fakeBin+":/usr/bin:/bin", 1)
	text = strings.Replace(text, "UPGRADE_STATE_DIR=/run/midnight-package-upgrade", "UPGRADE_STATE_DIR="+stateDir, 1)
	path := filepath.Join(dir, "upgrade-alpha2-bridge")
	if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAlpha2BridgeFakes(t *testing.T, dir string) string {
	t.Helper()
	fakeBin := filepath.Join(dir, "bridge-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{
		"id":         "#!/bin/sh\nprintf '%s\\n' \"${FAKE_UID:-0}\"\n",
		"realpath":   "#!/bin/sh\nfor value in \"$@\"; do path=\"$value\"; done\nprintf '%s\\n' \"$path\"\n",
		"flock":      "#!/bin/sh\nexit 0\n",
		"dpkg-query": "#!/bin/sh\nprintf '0.1.0~alpha-2'\n",
		"dpkg-deb": `#!/bin/sh
case "$3" in
Package) printf 'midnight\n' ;;
Version) printf '0.1.0~alpha-3\n' ;;
*) exit 1 ;;
esac
`,
		"dpkg": `#!/bin/sh
if [ "$1" = "--compare-versions" ]; then
    exit 0
fi
printf 'dpkg %s\n' "$*" >>"$FAKE_LOG"
[ "${FAKE_DPKG_FAIL:-0}" -eq 0 ] || exit 42
sh "$FAKE_OLD_PRERM" upgrade 0.1.0~alpha-3 || exit $?
sh "$FAKE_NEW_POSTINST" configure 0.1.0~alpha-2
`,
		"install": `#!/bin/sh
target=""
for value in "$@"; do target="$value"; done
mkdir -p "$target"
chmod 0700 "$target"
`,
		"stat": `#!/bin/sh
format="$2"
target="$4"
case "$format" in
%u) printf '0\n' ;;
%a)
    if [ "$target" = "$FAKE_STATE_DIR" ]; then printf '700\n'; else printf '600\n'; fi
    ;;
*) exit 1 ;;
esac
`,
		"chown": "#!/bin/sh\nexit 0\n",
		"systemctl": `#!/bin/sh
case "$1" in
is-active)
    grep -Fqx -- "$3" "$FAKE_ACTIVE"
    ;;
is-enabled)
    printf 'is-enabled %s\n' "$*" >>"$FAKE_LOG"
    exit 1
    ;;
stop)
    shift
    printf 'stop %s\n' "$*" >>"$FAKE_LOG"
    ;;
daemon-reload)
    printf 'daemon-reload\n' >>"$FAKE_LOG"
    [ "${FAKE_RELOAD_FAIL:-0}" -eq 0 ]
    ;;
start)
    shift
    printf 'start %s\n' "$*" >>"$FAKE_LOG"
    [ "${FAKE_START_FAIL:-0}" -eq 0 ]
    ;;
*) exit 0 ;;
esac
`,
	}
	for name, contents := range commands {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fakeBin
}

func writeLifecycleFakes(t *testing.T, dir string) string {
	t.Helper()
	fakeBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	systemctl := `#!/bin/sh
case "$1" in
is-active)
    grep -Fqx -- "$3" "$FAKE_ACTIVE"
    ;;
stop)
    shift
    printf 'stop %s\n' "$*" >>"$FAKE_LOG"
    ;;
daemon-reload)
    printf 'daemon-reload\n' >>"$FAKE_LOG"
    [ "${FAKE_RELOAD_FAIL:-0}" -eq 0 ]
    ;;
disable)
    shift
    printf 'disable %s\n' "$*" >>"$FAKE_LOG"
    ;;
start)
    shift
    printf 'start %s\n' "$*" >>"$FAKE_LOG"
    [ "${FAKE_START_FAIL:-0}" -eq 0 ]
    ;;
*) exit 0 ;;
esac
`
	stat := `#!/bin/sh
case "$2" in
%u) printf '0\n' ;;
%a) printf '600\n' ;;
*) exit 1 ;;
esac
`
	for name, contents := range map[string]string{"systemctl": systemctl, "stat": stat} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fakeBin
}
