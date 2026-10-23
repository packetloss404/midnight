// Command vm-harness is the disposable-VM CI assertion harness for
// 0.1-EC1 (Ubuntu 24.04) and 0.1-EC2 (Debian 12). It invokes
// `midnight doctor --json` on the host, parses the canonical
// schema_version=1 output emitted by `cmd/midnight-doctor`, and
// asserts the panel-locked exit-criteria contract:
//
//   - the run completed in < --max-duration (default 10m)
//   - the doctor process exited with code 0
//   - every OR-CHK-DOC-001..009 entry reports status="pass"
//
// The harness writes:
//
//	--json-out=<path>     — verbatim doctor JSON for CI artifacts
//	--junit-out=<path>    — JUnit XML so downstream rollups can
//	                        aggregate EC1 / EC2 across distros
//
// The harness itself never imports `internal/doctor` directly; it
// shells out to `midnight doctor` (or `midnight-doctor`, configurable
// via --doctor-binary) so it tests the same surface operators run.
//
// Default `go test ./...` only exercises the parser via a small unit
// test (parser_test.go); the binary is exercised solely by the
// .github/workflows/doctor-vm-{ec1,ec2}.yml workflows.
//
// Optional `--package-canary --server-url=<url>` mode adds the installed
// package/control-plane boot probe: expected package files exist, and the
// booted server reports v1alpha compatibility plus fully applied migrations.
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// expectedCheckIDs is the panel-locked OR-CHK-DOC registry that the
// harness asserts pass on a fresh disposable VM. The list is mirrored
// from `internal/doctor/live.go::LiveCheckRegistry`. Any drift is
// caught by `tools/lint` (registry guard) — adding an id is a MINOR
// bump per P0-D005.
var expectedCheckIDs = []string{
	"OR-CHK-DOC-001",
	"OR-CHK-DOC-002",
	"OR-CHK-DOC-003",
	"OR-CHK-DOC-004",
	"OR-CHK-DOC-005",
	"OR-CHK-DOC-006",
	"OR-CHK-DOC-007",
	"OR-CHK-DOC-008",
	"OR-CHK-DOC-009",
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exitErr *harnessFailure
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.Error())
			os.Exit(exitErr.code)
		}
		fmt.Fprintf(os.Stderr, "vm-harness: %s\n", err.Error())
		os.Exit(2)
	}
}

type harnessFailure struct {
	code int
	msg  string
}

func (e *harnessFailure) Error() string { return fmt.Sprintf("vm-harness: %s", e.msg) }

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vm-harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		doctorBinary  string
		distro        string
		jsonOut       string
		junitOut      string
		maxDuration   time.Duration
		extraArgsCSV  string
		expectExitStr string
		expectFail    string
		negativePath  bool
		hostSource    string
		packageCanary bool
		packageRoot   string
		serverURL     string
	)
	fs.StringVar(&doctorBinary, "doctor-binary", "midnight-doctor", "path to the midnight doctor binary (or `midnight doctor` shim).")
	fs.StringVar(&distro, "distro", "", "distro label (e.g. ubuntu-24.04 or debian-12); written to the JUnit XML.")
	fs.StringVar(&jsonOut, "json-out", "", "path to write the verbatim doctor JSON output.")
	fs.StringVar(&junitOut, "junit-out", "", "path to write a JUnit XML rollup.")
	fs.DurationVar(&maxDuration, "max-duration", 10*time.Minute, "max wall-clock duration for the doctor run (panel-locked default 10m for 0.1-EC1/EC2).")
	fs.StringVar(&extraArgsCSV, "doctor-args", "", "comma-separated extra arguments to pass to midnight-doctor (after --json).")
	fs.StringVar(&expectExitStr, "expect-exit", "0", "expected doctor exit code (default 0). Negative-path jobs set this to 2 or 3.")
	fs.StringVar(&expectFail, "expect-fail-id", "", "negative-path: assert this OR-CHK-DOC id reports status=fail. Empty = positive-path (all pass).")
	fs.BoolVar(&negativePath, "negative-path", false, "negative-path mode: skip the all-pass assertion and require --expect-fail-id to be present.")
	fs.StringVar(&hostSource, "hostsource", "", "passed through to midnight-doctor as --hostsource=<value>; empty = leave unset.")
	fs.BoolVar(&packageCanary, "package-canary", false, "also verify installed package files and the booted control-plane API. Requires --server-url.")
	fs.StringVar(&packageRoot, "package-root", "/", "root filesystem to inspect for --package-canary.")
	fs.StringVar(&serverURL, "server-url", "", "base URL for the booted midnight-server, used by --package-canary (for example http://127.0.0.1:8080).")
	if err := fs.Parse(args); err != nil {
		return err
	}

	expectExit, err := parseExitCode(expectExitStr)
	if err != nil {
		return fmt.Errorf("--expect-exit: %w", err)
	}
	if negativePath && expectFail == "" {
		return errors.New("--negative-path requires --expect-fail-id to be set")
	}
	if !negativePath && expectFail != "" {
		return errors.New("--expect-fail-id is only valid with --negative-path")
	}

	doctorArgs := []string{"--json"}
	if hostSource != "" {
		doctorArgs = append(doctorArgs, "--hostsource="+hostSource)
	}
	if extraArgsCSV != "" {
		for _, a := range strings.Split(extraArgsCSV, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				doctorArgs = append(doctorArgs, a)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxDuration+30*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, doctorBinary, doctorArgs...)
	output, runErr := cmd.Output()
	elapsed := time.Since(start)

	exitCode := 0
	if exit, ok := runErr.(*exec.ExitError); ok {
		exitCode = exit.ExitCode()
	} else if runErr != nil {
		// Could not even start the binary — config_error.
		return &harnessFailure{code: 3, msg: fmt.Sprintf("could not run %s %s: %v", doctorBinary, strings.Join(doctorArgs, " "), runErr)}
	}

	if jsonOut != "" {
		if err := writeFile(jsonOut, output); err != nil {
			return fmt.Errorf("write --json-out: %w", err)
		}
	}

	report, parseErr := ParseDoctorReport(output)
	if parseErr != nil {
		// Even on parse failure we want to emit a JUnit shell so
		// the workflow can attach it as an artifact.
		if junitOut != "" {
			_ = writeJUnit(junitOut, distro, elapsed, []junitCase{{
				Name:    "vm-harness.parse",
				Failure: parseErr.Error(),
			}})
		}
		return &harnessFailure{code: 3, msg: fmt.Sprintf("parse doctor JSON: %v", parseErr)}
	}

	cases := buildJUnitCases(report, expectedCheckIDs)
	cases = append(cases, junitCase{
		Name:    "vm-harness.duration",
		Time:    elapsed.Seconds(),
		Failure: durationFailureMessage(elapsed, maxDuration),
	})
	cases = append(cases, junitCase{
		Name:    "vm-harness.exit-code",
		Failure: exitCodeFailureMessage(exitCode, expectExit),
	})
	var packageFailures []string
	if packageCanary {
		result := runPackageCanary(ctx, packageCanaryConfig{
			Root:      packageRoot,
			ServerURL: serverURL,
			Client:    http.DefaultClient,
		})
		cases = append(cases, result.Cases...)
		packageFailures = result.Failures
	}
	if junitOut != "" {
		if err := writeJUnit(junitOut, distro, elapsed, cases); err != nil {
			return fmt.Errorf("write --junit-out: %w", err)
		}
	}

	// Assertions, in stable order so CI logs read deterministically.
	var failures []string
	if elapsed > maxDuration {
		failures = append(failures, fmt.Sprintf("doctor run took %s, exceeds --max-duration=%s", elapsed.Round(time.Millisecond), maxDuration))
	}
	if exitCode != expectExit {
		failures = append(failures, fmt.Sprintf("doctor exited with %d, expected %d (--expect-exit)", exitCode, expectExit))
	}
	failures = append(failures, packageFailures...)
	if !negativePath {
		if missing := MissingExpectedChecks(report, expectedCheckIDs); len(missing) > 0 {
			failures = append(failures, "doctor JSON missing expected check ids: "+strings.Join(missing, ","))
		}
		if failed := NonPassingChecks(report, expectedCheckIDs); len(failed) > 0 {
			failures = append(failures, "doctor reports non-pass for: "+strings.Join(failed, ","))
		}
	} else {
		if !ChecksContainsFail(report, expectFail) {
			failures = append(failures, fmt.Sprintf("doctor did not report status=fail for %s (negative-path)", expectFail))
		}
	}

	if len(failures) > 0 {
		_, _ = stdout.Write(output)
		fmt.Fprintln(stderr, strings.Repeat("-", 60))
		for _, msg := range failures {
			fmt.Fprintf(stderr, "FAIL %s\n", msg)
		}
		return &harnessFailure{code: 1, msg: fmt.Sprintf("%d assertion(s) failed; see %s", len(failures), jsonOutOrStdout(jsonOut))}
	}

	fmt.Fprintf(stdout, "vm-harness: PASS distro=%s elapsed=%s exit=%d checks=%d\n", labelOrUnknown(distro), elapsed.Round(time.Millisecond), exitCode, len(report.Results))
	return nil
}

func parseExitCode(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func durationFailureMessage(actual, max time.Duration) string {
	if actual <= max {
		return ""
	}
	return fmt.Sprintf("doctor run took %s, exceeds budget %s", actual.Round(time.Millisecond), max)
}

func exitCodeFailureMessage(actual, expect int) string {
	if actual == expect {
		return ""
	}
	return fmt.Sprintf("doctor exit %d, expected %d", actual, expect)
}

func jsonOutOrStdout(jsonOut string) string {
	if jsonOut != "" {
		return jsonOut
	}
	return "stdout (pass --json-out=<path> to persist)"
}

func labelOrUnknown(label string) string {
	if label == "" {
		return "unknown"
	}
	return label
}

// ----------------- Installed package canary ---------------------------------

type packageCanaryConfig struct {
	Root      string
	ServerURL string
	Client    *http.Client
}

type packageCanaryResult struct {
	Cases    []junitCase
	Failures []string
}

type packageExpectedPath struct {
	Name       string
	Path       string
	Executable bool
}

var expectedPackagePaths = []packageExpectedPath{
	{Name: "bin.midnight", Path: "/usr/bin/midnight", Executable: true},
	{Name: "bin.midnight-doctor", Path: "/usr/bin/midnight-doctor", Executable: true},
	{Name: "bin.midnight-server", Path: "/usr/bin/midnight-server", Executable: true},
	{Name: "bin.midnight-worker", Path: "/usr/bin/midnight-worker", Executable: true},
	{Name: "bin.midnight-agent", Path: "/usr/bin/midnight-agent", Executable: true},
	{Name: "bin.midnight-buildkit", Path: "/usr/bin/midnight-buildkit", Executable: true},
	{Name: "bin.midnight-registry", Path: "/usr/bin/midnight-registry", Executable: true},
	{Name: "bin.midnight-github-status-worker", Path: "/usr/bin/midnight-github-status-worker", Executable: true},
	{Name: "unit.midnight-server", Path: "/lib/systemd/system/midnight-server.service"},
	{Name: "unit.midnight-worker", Path: "/lib/systemd/system/midnight-worker.service"},
	{Name: "unit.midnight-agent", Path: "/lib/systemd/system/midnight-agent.service"},
	{Name: "unit.midnight-buildkit", Path: "/lib/systemd/system/midnight-buildkit.service"},
	{Name: "unit.midnight-registry", Path: "/lib/systemd/system/midnight-registry.service"},
	{Name: "unit.midnight-github-status-worker", Path: "/lib/systemd/system/midnight-github-status-worker.service"},
	{Name: "default-env", Path: "/usr/share/midnight/midnight.env.default"},
	{Name: "daemon-json-baseline", Path: "/usr/share/midnight/daemon-json/docker-daemon-baseline.json"},
	{Name: "installed-env", Path: "/etc/midnight/midnight.env"},
	{Name: "cursor-key", Path: "/etc/midnight/cursor.key"},
	{Name: "bootstrap-token", Path: "/var/lib/midnight/bootstrap-token"},
}

func runPackageCanary(ctx context.Context, cfg packageCanaryConfig) packageCanaryResult {
	result := packageCanaryResult{
		Cases: make([]junitCase, 0, len(expectedPackagePaths)+2),
	}
	for _, expected := range expectedPackagePaths {
		name := "midnight.package." + expected.Name
		if failure := installedPathFailure(cfg.Root, expected); failure != "" {
			result.Cases = append(result.Cases, junitCase{Name: name, Failure: failure})
			result.Failures = append(result.Failures, failure)
			continue
		}
		result.Cases = append(result.Cases, junitCase{Name: name})
	}

	if strings.TrimSpace(cfg.ServerURL) == "" {
		failure := "--package-canary requires --server-url to prove control-plane boot readiness"
		result.Cases = append(result.Cases, junitCase{Name: "midnight.package.control-plane-url", Failure: failure})
		result.Failures = append(result.Failures, failure)
		return result
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if failure := compatProbeFailure(ctx, cfg); failure != "" {
		result.Cases = append(result.Cases, junitCase{Name: "midnight.package.control-plane.compat", Failure: failure})
		result.Failures = append(result.Failures, failure)
	} else {
		result.Cases = append(result.Cases, junitCase{Name: "midnight.package.control-plane.compat"})
	}
	if failure := migrationsProbeFailure(ctx, cfg); failure != "" {
		result.Cases = append(result.Cases, junitCase{Name: "midnight.package.control-plane.migrations", Failure: failure})
		result.Failures = append(result.Failures, failure)
	} else {
		result.Cases = append(result.Cases, junitCase{Name: "midnight.package.control-plane.migrations"})
	}
	return result
}

func installedPathFailure(root string, expected packageExpectedPath) string {
	path := packagePath(root, expected.Path)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("%s missing at %s: %v", expected.Path, path, err)
	}
	if info.IsDir() {
		return fmt.Sprintf("%s is a directory at %s", expected.Path, path)
	}
	if expected.Executable && runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return fmt.Sprintf("%s is not executable at %s", expected.Path, path)
	}
	return ""
}

func packagePath(root, unixPath string) string {
	if strings.TrimSpace(root) == "" {
		root = "/"
	}
	relative := strings.TrimPrefix(strings.TrimSpace(unixPath), "/")
	return filepath.Join(root, filepath.FromSlash(relative))
}

type packageCompatResponse struct {
	ServerVersion string `json:"server_version"`
	APIVersion    string `json:"api_version"`
	SchemaVersion int    `json:"schema_version"`
}

func compatProbeFailure(ctx context.Context, cfg packageCanaryConfig) string {
	var compat packageCompatResponse
	if err := getJSON(ctx, cfg.Client, cfg.ServerURL, "/api/v1alpha/compat", &compat); err != nil {
		return err.Error()
	}
	if compat.APIVersion != "v1alpha" {
		return fmt.Sprintf("compat api_version=%q, want v1alpha", compat.APIVersion)
	}
	if compat.SchemaVersion != 1 {
		return fmt.Sprintf("compat schema_version=%d, want 1", compat.SchemaVersion)
	}
	if compat.ServerVersion == "" {
		return "compat server_version is empty"
	}
	return ""
}

type packageMigrationsResponse struct {
	LatestVersion int `json:"latest_version"`
	AppliedCount  int `json:"applied_count"`
	PendingCount  int `json:"pending_count"`
}

func migrationsProbeFailure(ctx context.Context, cfg packageCanaryConfig) string {
	var migrations packageMigrationsResponse
	if err := getJSON(ctx, cfg.Client, cfg.ServerURL, "/api/v1alpha/migrations", &migrations); err != nil {
		return err.Error()
	}
	if migrations.LatestVersion <= 0 {
		return fmt.Sprintf("migrations latest_version=%d, want >0", migrations.LatestVersion)
	}
	if migrations.PendingCount != 0 {
		return fmt.Sprintf("migrations pending_count=%d, want 0", migrations.PendingCount)
	}
	if migrations.AppliedCount != migrations.LatestVersion {
		return fmt.Sprintf("migrations applied_count=%d, want latest_version=%d", migrations.AppliedCount, migrations.LatestVersion)
	}
	return ""
}

func getJSON(ctx context.Context, client *http.Client, baseURL, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

// ----------------- Doctor JSON parser ---------------------------------------

// DoctorReport mirrors the schema_version=1 envelope emitted by
// cmd/midnight-doctor when --json is passed. Field names match the
// JSON shape exactly so round-trip parsing is stable.
type DoctorReport struct {
	SchemaVersion int            `json:"schema_version"`
	Live          bool           `json:"live"`
	ExitCode      int            `json:"exit_code"`
	Results       []DoctorResult `json:"results"`
}

// DoctorResult is one OR-CHK-DOC entry inside the report.
type DoctorResult struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Severity    string `json:"severity,omitempty"`
	DocsURL     string `json:"docs_url,omitempty"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ParseDoctorReport unmarshals the doctor --json output. It rejects
// schema versions != 1 so a future schema bump is loud rather than
// silent.
func ParseDoctorReport(data []byte) (DoctorReport, error) {
	var report DoctorReport
	if len(data) == 0 {
		return report, errors.New("empty doctor output")
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return report, err
	}
	if report.SchemaVersion != 1 {
		return report, fmt.Errorf("unsupported doctor schema_version %d (want 1)", report.SchemaVersion)
	}
	return report, nil
}

// MissingExpectedChecks returns the subset of expected ids that are
// not present in the report. Stable order matches expected.
func MissingExpectedChecks(report DoctorReport, expected []string) []string {
	have := make(map[string]struct{}, len(report.Results))
	for _, r := range report.Results {
		have[r.ID] = struct{}{}
	}
	var missing []string
	for _, id := range expected {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// NonPassingChecks returns ids in expected whose status is not "pass".
// Stable lexicographic order.
func NonPassingChecks(report DoctorReport, expected []string) []string {
	want := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		want[id] = struct{}{}
	}
	var bad []string
	for _, r := range report.Results {
		if _, ok := want[r.ID]; !ok {
			continue
		}
		if r.Status != "pass" {
			bad = append(bad, r.ID+"="+r.Status)
		}
	}
	sort.Strings(bad)
	return bad
}

// ChecksContainsFail reports whether the named id has status="fail".
// Used by the negative-path jobs (cgroup v1, daemon.json conflict).
func ChecksContainsFail(report DoctorReport, id string) bool {
	for _, r := range report.Results {
		if r.ID == id && r.Status == "fail" {
			return true
		}
	}
	return false
}

// ----------------- JUnit XML emitter ----------------------------------------
//
// Junit shape is the de-facto Maven Surefire flavor. Downstream
// rollups (see release-evidence/) only consume <testsuite> and
// <testcase> with optional <failure>; we keep it minimal so the
// emitter stays stdlib-only.

type junitTestSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Time     float64     `xml:"time,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	XMLName xml.Name `xml:"testcase"`
	Name    string   `xml:"name,attr"`
	Time    float64  `xml:"time,attr,omitempty"`
	Failure string   `xml:"failure,omitempty"`
}

func buildJUnitCases(report DoctorReport, expected []string) []junitCase {
	cases := make([]junitCase, 0, len(report.Results))
	have := make(map[string]DoctorResult, len(report.Results))
	for _, r := range report.Results {
		have[r.ID] = r
	}
	for _, id := range expected {
		r, ok := have[id]
		if !ok {
			cases = append(cases, junitCase{
				Name:    id,
				Failure: "doctor JSON did not include this check id",
			})
			continue
		}
		c := junitCase{Name: id}
		if r.Status != "pass" {
			c.Failure = fmt.Sprintf("status=%s message=%q", r.Status, r.Message)
		}
		cases = append(cases, c)
	}
	return cases
}

func writeJUnit(path, distro string, elapsed time.Duration, cases []junitCase) error {
	failures := 0
	for _, c := range cases {
		if c.Failure != "" {
			failures++
		}
	}
	suite := junitTestSuite{
		Name:     "midnight.doctor.disposable-vm." + labelOrUnknown(distro),
		Time:     elapsed.Seconds(),
		Tests:    len(cases),
		Failures: failures,
		Cases:    cases,
	}
	data, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	data = append([]byte(xml.Header), data...)
	data = append(data, '\n')
	return writeFile(path, data)
}
