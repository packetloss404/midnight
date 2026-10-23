package preflight

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var RequiredSystemdUnitNames = []string{
	"midnight-server.service",
	"midnight-worker.service",
	"midnight-agent.service",
	"midnight-buildkit.service",
	"midnight-buildkit-worker.service",
	"midnight-github-status-worker.service",
	"midnight-registry.service",
}

// InstallReadinessChecks builds package-install checks that are safe for
// fixture bundles and installer wiring. Empty paths skip rather than probing
// live host state.
func InstallReadinessChecks(source HostSource) []RunnableCheck {
	return []RunnableCheck{
		{
			ID:          "PRE-INSTALL-ENV-001",
			Description: "Midnight package environment file is readable",
			Severity:    "fatal",
			Run: func(context.Context) Result {
				return CheckPackageEnvReadable(source)
			},
		},
		{
			ID:          "PRE-INSTALL-DB-001",
			Description: "Postgres DSN is configured via package env or DSN file",
			Severity:    "fatal",
			Run: func(context.Context) Result {
				return CheckDatabaseDSNConfigured(source)
			},
		},
		{
			ID:          "PRE-INSTALL-ROUTES-001",
			Description: "route config directory is absolute and writable",
			Severity:    "fatal",
			Run: func(context.Context) Result {
				return CheckRouteConfigDirWritable(source)
			},
		},
		{
			ID:          "PRE-INSTALL-RUNTIME-001",
			Description: "required runtime binaries are configured and present",
			Severity:    "fatal",
			Run: func(context.Context) Result {
				return CheckRuntimeBinariesPresent(source)
			},
		},
		{
			ID:          "PRE-INSTALL-SYSTEMD-001",
			Description: "required systemd units are present and loadable",
			Severity:    "fatal",
			Run: func(context.Context) Result {
				return CheckSystemdUnitsLoadable(source)
			},
		},
	}
}

func CheckPackageEnvReadable(source HostSource) Result {
	if strings.TrimSpace(source.Paths.PackageEnv) == "" {
		return Result{Status: StatusSkip, Message: "package env path not provided"}
	}
	values, err := PackageEnvValues(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Status: StatusFail, Message: "package env file is missing"}
		}
		return Result{Status: StatusConfigError, Err: err, Message: "could not read package env file"}
	}
	if len(values) == 0 {
		return Result{Status: StatusFail, Message: "package env file is empty or has no KEY=value entries"}
	}
	return Result{Status: StatusPass, Message: "package env file is readable"}
}

func CheckDatabaseDSNConfigured(source HostSource) Result {
	values, _ := PackageEnvValues(source)
	if hasEnvValue(values, "MIDNIGHT_DB_DSN", "MIDNIGHT_DATABASE_URL") || source.Hints.DatabaseDSNConfigured {
		return Result{Status: StatusPass, Message: "Postgres DSN is configured via environment"}
	}
	path := strings.TrimSpace(source.Paths.DatabaseDSN)
	if path == "" {
		path = strings.TrimSpace(values["MIDNIGHT_DB_DSN_FILE"])
	}
	if path == "" {
		return Result{Status: StatusSkip, Message: "database DSN file path or env value not provided"}
	}
	dsn, err := source.withDatabaseDSNPath(path).ReadDatabaseDSN()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Status: StatusFail, Message: "database DSN file is missing"}
		}
		return Result{Status: StatusConfigError, Err: err, Message: "could not read database DSN"}
	}
	if strings.TrimSpace(dsn) == "" {
		return Result{Status: StatusFail, Message: "database DSN file is empty"}
	}
	return Result{Status: StatusPass, Message: "database DSN file is present"}
}

func CheckRouteConfigDirWritable(source HostSource) Result {
	values, _ := PackageEnvValues(source)
	root := strings.TrimSpace(source.Paths.RouteConfigDir)
	if root == "" {
		root = strings.TrimSpace(values["MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"])
	}
	if root == "" {
		return Result{Status: StatusSkip, Message: "route config directory path not provided"}
	}
	if !filepath.IsAbs(root) {
		return Result{Status: StatusFail, Message: "route config directory must be absolute"}
	}
	info, err := os.Stat(root)
	if err != nil {
		return Result{Status: StatusFail, Err: err, Message: "route config directory is not accessible"}
	}
	if !info.IsDir() {
		return Result{Status: StatusFail, Message: "route config path is not a directory"}
	}
	probe, err := os.MkdirTemp(root, ".midnight-preflight-route-")
	if err != nil {
		return Result{Status: StatusFail, Err: err, Message: "route config directory is not writable by this process"}
	}
	if err := os.RemoveAll(probe); err != nil {
		return Result{Status: StatusWarn, Err: err, Message: "route config directory is writable, but cleanup of the preflight probe failed"}
	}
	return Result{Status: StatusPass, Message: "route config directory is absolute and writable"}
}

func CheckRuntimeBinariesPresent(source HostSource) Result {
	values, _ := PackageEnvValues(source)
	binaries := runtimeBinaryCandidates(source, values)
	if len(binaries) == 0 {
		return Result{Status: StatusSkip, Message: "runtime binary paths not provided"}
	}
	var missing []string
	for _, candidate := range binaries {
		role := candidate.role
		binary := candidate.path
		binary = strings.TrimSpace(binary)
		if binary == "" {
			missing = append(missing, role)
			continue
		}
		if err := RuntimeBinaryExists(binary); err != nil {
			missing = append(missing, fmt.Sprintf("%s=%s", role, binary))
		}
	}
	if len(missing) > 0 {
		return Result{Status: StatusFail, Message: fmt.Sprintf("runtime binaries not found: %s", strings.Join(missing, ", "))}
	}
	return Result{Status: StatusPass, Message: "required runtime binaries are present"}
}

func CheckSystemdUnitsLoadable(source HostSource) Result {
	dir := strings.TrimSpace(source.Paths.SystemdUnitDir)
	if dir == "" {
		return Result{Status: StatusSkip, Message: "systemd unit directory not provided"}
	}
	if source.Reader == nil {
		return Result{Status: StatusConfigError, Message: "file reader is required"}
	}
	var problems []string
	for _, name := range RequiredSystemdUnitNames {
		data, err := source.Reader.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("%s missing", name))
				continue
			}
			return Result{Status: StatusConfigError, Err: err, Message: fmt.Sprintf("could not read systemd unit %s", name)}
		}
		if err := validateSystemdUnitText(name, string(data)); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return Result{Status: StatusFail, Message: strings.Join(problems, "; ")}
	}
	return Result{Status: StatusPass, Message: "required systemd units are present and parseable"}
}

func PackageEnvValues(source HostSource) (map[string]string, error) {
	if strings.TrimSpace(source.Paths.PackageEnv) == "" {
		return map[string]string{}, nil
	}
	text, err := source.ReadPackageEnv()
	if err != nil {
		return nil, err
	}
	return ParsePackageEnvStrict(text)
}

func RuntimeBinaryExists(binary string) error {
	if filepath.IsAbs(binary) || strings.ContainsAny(binary, `/\`) {
		info, err := os.Stat(binary)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", binary)
		}
		return nil
	}
	_, err := LookPath(binary)
	return err
}

var LookPath = func(file string) (string, error) {
	return exec.LookPath(file)
}

type runtimeBinaryCandidate struct {
	role string
	path string
}

func runtimeBinaryCandidates(source HostSource, values map[string]string) []runtimeBinaryCandidate {
	if len(source.Hints.RuntimeBinaries) > 0 {
		out := make([]runtimeBinaryCandidate, 0, len(source.Hints.RuntimeBinaries))
		for i, binary := range source.Hints.RuntimeBinaries {
			out = append(out, runtimeBinaryCandidate{role: fmt.Sprintf("runtime[%d]", i), path: binary})
		}
		return out
	}
	out := []runtimeBinaryCandidate{}
	seenEnv := false
	for _, item := range []runtimeBinaryCandidate{
		{role: "buildkitd", path: "MIDNIGHT_BUILDKITD_BIN"},
		{role: "buildctl", path: "MIDNIGHT_BUILDCTL"},
		{role: "registry", path: "MIDNIGHT_REGISTRY_BIN"},
	} {
		key := item.path
		if _, ok := values[key]; ok {
			seenEnv = true
		}
		out = append(out, runtimeBinaryCandidate{role: item.role, path: values[key]})
	}
	if !seenEnv {
		return nil
	}
	return out
}

func hasEnvValue(values map[string]string, names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(values[name]) != "" {
			return true
		}
	}
	return false
}

func (s HostSource) withDatabaseDSNPath(path string) HostSource {
	s.Paths.DatabaseDSN = path
	return s
}

func validateSystemdUnitText(name, text string) error {
	sections := map[string]bool{}
	hasExecStart := false
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if section != "" {
				sections[section] = true
			}
			continue
		}
		if strings.HasPrefix(line, "ExecStart=") && strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")) != "" {
			hasExecStart = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for _, section := range []string{"Unit", "Service", "Install"} {
		if !sections[section] {
			return fmt.Errorf("%s missing [%s] section", name, section)
		}
	}
	if !hasExecStart {
		return fmt.Errorf("%s missing ExecStart", name)
	}
	return nil
}
