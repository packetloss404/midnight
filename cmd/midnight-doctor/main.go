// Command midnight-doctor is the live host-diagnostic CLI for the
// `midnight doctor` surface. It runs the canonical `OR-CHK-DOC-NNN`
// checks and threads them through the operator exit-code table:
//
//	0 — everything healthy
//	1 — at least one warn
//	2 — at least one fail
//	3 — runner could not decide (config_error)
//
// The default invocation runs against fixture/hint sources so it is
// safe to exercise inside containers, CI, and unit-test harnesses.
// Live host probing is gated behind `MIDNIGHT_DOCTOR_LIVE=1` (or
// `--live`).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/midnight/midnight/internal/doctor"
	"github.com/midnight/midnight/internal/preflight"
	"github.com/midnight/midnight/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if exitErr, ok := err.(*exitError); ok {
			os.Exit(exitErr.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

type exitError struct {
	code int
}

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintln(stdout, version.Version)
		return nil
	}
	if len(args) > 0 && args[0] == "wait" {
		return runWait(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("midnight-doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		jsonOutput     bool
		live           bool
		mvpReadiness   bool
		installReady   bool
		check          string
		fix            string
		hostSource     string
		mountInfoPath  string
		daemonJSONPath string
		dockerVerPath  string
		packageEnvPath string
		dbDSNPath      string
		cursorKeyPath  string
		registryConfig string
		routeConfigDir string
		systemdUnitDir string
		ssLNTPath      string
		firewallMode   string
		ingressOwner   string
		macMode        string
		timeSyncMode   string
		hostnameFQDN   string
		dockerPkgsCSV  string
		runtimeBinsCSV string
		dbDSNEnv       bool
		workerID       string
		workerBackend  string
		workerDocker   string
		workerAgentID  string
		workerHostID   string
		routeGroup     string
		routeHost      string
		routePath      string
		routeTLS       bool
		routeResolver  string
		routePort      int
		backupDestURI  string
		backupDestHint preflight.BackupDestinationHint
	)
	fs.BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON output")
	fs.BoolVar(&live, "live", false, "enable live host probes (also enabled by MIDNIGHT_DOCTOR_LIVE=1)")
	fs.BoolVar(&mvpReadiness, "mvp-readiness", false, "run only the MVP operator readiness checks (no deploy is executed)")
	fs.BoolVar(&installReady, "install-readiness", false, "run only the package install-path readiness checks")
	fs.StringVar(&check, "check", "", "comma-separated list of OR-CHK-DOC ids to filter on")
	fs.StringVar(&fix, "fix", "", "reserved safe-fix mode: safe|all (currently returns an explicit unsupported error)")
	fs.StringVar(&hostSource, "hostsource", "", "host-fact source override: `live` (default behavior) or `fixture:<dir>` to load the canonical fixture set from <dir> (mountinfo, daemon.json, docker-version.txt, midnight.env, db.dsn, systemd/, ss-ltn.txt, hints.json). Individual --*-fixture flags still take precedence and live host probing is unchanged. Used by the disposable-VM CI harness.")
	fs.StringVar(&mountInfoPath, "mountinfo-fixture", "", "path to a mountinfo fixture for OR-CHK-DOC-001 (cgroup v2)")
	fs.StringVar(&daemonJSONPath, "daemon-json-fixture", "", "path to a daemon.json fixture for OR-CHK-DOC-002")
	fs.StringVar(&dockerVerPath, "docker-version-fixture", "", "path to `docker version` text for OR-CHK-DOC-003")
	fs.StringVar(&packageEnvPath, "package-env-fixture", "", "path to a Midnight package env fixture for OR-CHK-DOC-018")
	fs.StringVar(&dbDSNPath, "db-dsn-fixture", "", "path to a Postgres DSN fixture for OR-CHK-DOC-010")
	fs.StringVar(&cursorKeyPath, "cursor-key-fixture", "", "path to a cursor key fixture for OR-CHK-DOC-011")
	fs.StringVar(&registryConfig, "registry-config-fixture", "", "path to a registry config fixture for OR-CHK-DOC-012")
	fs.StringVar(&routeConfigDir, "route-config-dir", "", "Traefik dynamic route config directory for MVP readiness checks")
	fs.StringVar(&systemdUnitDir, "systemd-unit-dir", "", "directory containing Midnight systemd unit fixtures for OR-CHK-DOC-019")
	fs.StringVar(&ssLNTPath, "ss-ltn-fixture", "", "path to `ss -ltn` output for OR-CHK-DOC-004")
	fs.StringVar(&ingressOwner, "ingress-reserved-by", "", "name of the operator ingress that legitimately owns :80/:443 (for example traefik); records the reservation instead of failing OR-CHK-DOC-004")
	fs.StringVar(&firewallMode, "firewall-mode", "", "firewall hint for OR-CHK-DOC-005 (ufw|firewalld|nftables|iptables|none)")
	fs.StringVar(&macMode, "mac-mode", "", "MAC hint for OR-CHK-DOC-006 (apparmor-enforce|apparmor-complain|selinux-enforcing|selinux-permissive|disabled)")
	fs.StringVar(&timeSyncMode, "time-sync", "", "time-sync hint for OR-CHK-DOC-007 (synchronized|drift|unsynchronized)")
	fs.StringVar(&hostnameFQDN, "hostname-fqdn", "", "hostname FQDN hint for OR-CHK-DOC-008 (overrides os.Hostname())")
	fs.StringVar(&dockerPkgsCSV, "docker-packages", "", "comma-separated installed docker packages hint for OR-CHK-DOC-009")
	fs.StringVar(&runtimeBinsCSV, "runtime-binaries", "", "comma-separated runtime binaries for OR-CHK-DOC-013 (buildkitd,buildctl,registry)")
	fs.BoolVar(&dbDSNEnv, "db-dsn-configured", false, "hint that MIDNIGHT_DB_DSN is configured without exposing the DSN value")
	fs.StringVar(&workerID, "worker-id", "", "worker id hint for MVP readiness checks")
	fs.StringVar(&workerBackend, "worker-runtime-backend", "", "worker runtime backend hint for MVP readiness checks (docker required)")
	fs.StringVar(&workerDocker, "worker-docker-host", "", "Docker host hint for the worker; empty means Docker SDK default")
	fs.StringVar(&workerAgentID, "worker-agent-id", "", "worker agent id hint for runtime labels")
	fs.StringVar(&workerHostID, "worker-host-id", "", "worker host id hint for runtime labels")
	fs.StringVar(&routeGroup, "route-group", "", "route publication group hint for MVP readiness checks")
	fs.StringVar(&routeHost, "route-public-host", "", "public host hint for promoted routes")
	fs.StringVar(&routePath, "route-public-path", "", "public path prefix hint for promoted routes")
	fs.BoolVar(&routeTLS, "route-public-tls", false, "TLS hint for promoted routes")
	fs.StringVar(&routeResolver, "route-cert-resolver", "", "Traefik certificate resolver hint for promoted TLS routes")
	fs.IntVar(&routePort, "route-target-port", 0, "container target port hint for promoted routes")
	fs.StringVar(&backupDestURI, "backup-destination-uri", "", "off-host backup destination URI hint for MVP readiness checks")
	if err := fs.Parse(normalizeDoctorArgs(args)); err != nil {
		return &exitError{code: 3}
	}
	explicitFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })
	if !live {
		live = strings.EqualFold(strings.TrimSpace(os.Getenv("MIDNIGHT_DOCTOR_LIVE")), "1")
	}

	bundle, err := resolveHostSource(hostSource)
	if err != nil {
		fmt.Fprintf(stderr, "midnight-doctor: %s\n", err.Error())
		return &exitError{code: 3}
	}
	if bundle != nil {
		if mountInfoPath == "" {
			mountInfoPath = bundle.mountInfo
		}
		if daemonJSONPath == "" {
			daemonJSONPath = bundle.daemonJSON
		}
		if dockerVerPath == "" {
			dockerVerPath = bundle.dockerVersion
		}
		if packageEnvPath == "" {
			packageEnvPath = bundle.packageEnv
		}
		if dbDSNPath == "" {
			dbDSNPath = bundle.dbDSN
		}
		if cursorKeyPath == "" {
			cursorKeyPath = bundle.cursorKey
		}
		if registryConfig == "" {
			registryConfig = bundle.registryConfig
		}
		if routeConfigDir == "" {
			routeConfigDir = bundle.routeConfigDir
		}
		if systemdUnitDir == "" {
			systemdUnitDir = bundle.systemdUnitDir
		}
		if ssLNTPath == "" {
			ssLNTPath = bundle.ssLNT
		}
		if ingressOwner == "" {
			ingressOwner = bundle.hints.IngressPortsReservedBy
		}
		if firewallMode == "" {
			firewallMode = bundle.hints.FirewallMode
		}
		if macMode == "" {
			macMode = bundle.hints.MandatoryAccessControl
		}
		if timeSyncMode == "" {
			timeSyncMode = bundle.hints.TimeSyncStatus
		}
		if hostnameFQDN == "" {
			hostnameFQDN = bundle.hints.HostnameFQDN
		}
		if dockerPkgsCSV == "" && len(bundle.hints.InstalledDockerPackages) > 0 {
			dockerPkgsCSV = strings.Join(bundle.hints.InstalledDockerPackages, ",")
		}
		if runtimeBinsCSV == "" && len(bundle.hints.RuntimeBinaries) > 0 {
			runtimeBinsCSV = strings.Join(bundle.hints.RuntimeBinaries, ",")
		}
		dbDSNEnv = dbDSNEnv || bundle.hints.DatabaseDSNConfigured
		if workerID == "" {
			workerID = bundle.hints.WorkerID
		}
		if workerBackend == "" {
			workerBackend = bundle.hints.WorkerRuntimeBackend
		}
		if workerDocker == "" {
			workerDocker = bundle.hints.WorkerDockerHost
		}
		if workerAgentID == "" {
			workerAgentID = bundle.hints.WorkerAgentID
		}
		if workerHostID == "" {
			workerHostID = bundle.hints.WorkerHostID
		}
		if routeGroup == "" {
			routeGroup = bundle.hints.WorkerRouteGroup
		}
		if routeHost == "" {
			routeHost = bundle.hints.WorkerRoutePublicHost
		}
		if routePath == "" {
			routePath = bundle.hints.WorkerRoutePublicPath
		}
		routeTLS = routeTLS || bundle.hints.WorkerRoutePublicTLS
		if routeResolver == "" {
			routeResolver = bundle.hints.WorkerRouteCertResolver
		}
		if routePort == 0 {
			routePort = bundle.hints.WorkerRouteTargetPort
		}
		if !explicitFlags["backup-destination-uri"] && bundle.hints.BackupDestination.Configured {
			backupDestHint = bundle.hints.BackupDestination
		}
	}
	if fix != "" {
		switch strings.ToLower(fix) {
		case "safe", "all":
			fmt.Fprintf(stderr, "midnight-doctor: --fix=%s is not supported by the current check registry; run without --fix to review readiness output.\n", fix)
			return &exitError{code: 3}
		default:
			fmt.Fprintf(stderr, "midnight-doctor: --fix=%q is invalid; expected `safe` or `all`\n", fix)
			return &exitError{code: 3}
		}
	}
	if live {
		if packageEnvPath == "" {
			packageEnvPath = envFirst("MIDNIGHT_ENV_FILE", "/etc/midnight/midnight.env")
		}
	}

	packageValues := map[string]string{}
	packageDSNConfigured := false
	packageBackupHint := preflight.BackupDestinationHint{}
	if packageEnvPath != "" {
		data, readErr := os.ReadFile(packageEnvPath)
		switch {
		case readErr == nil:
			packageValues, packageDSNConfigured, packageBackupHint, err = preflight.ParsePackageReadinessEnv(string(data))
			if err != nil {
				fmt.Fprintf(stderr, "midnight-doctor: package environment %s could not be parsed: %s\n", packageEnvPath, err.Error())
				return &exitError{code: 3}
			}
		case errors.Is(readErr, os.ErrNotExist):
			// The registered package-environment check reports a missing file.
		default:
			fmt.Fprintf(stderr, "midnight-doctor: package environment %s is not readable\n", packageEnvPath)
			return &exitError{code: 3}
		}
	}

	if !explicitFlags["db-dsn-configured"] && !dbDSNEnv {
		dbDSNEnv = processEnvPresent("MIDNIGHT_DB_DSN", "MIDNIGHT_DATABASE_URL") || packageDSNConfigured
	}
	workerID = resolveDoctorString(workerID, explicitFlags["worker-id"], []string{"MIDNIGHT_WORKER_ID"}, packageValues, []string{"MIDNIGHT_WORKER_ID"}, "midnight-worker")
	workerBackend = resolveDoctorString(workerBackend, explicitFlags["worker-runtime-backend"], []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, packageValues, []string{"MIDNIGHT_WORKER_RUNTIME_BACKEND"}, "")
	workerDocker = resolveDoctorString(workerDocker, explicitFlags["worker-docker-host"], []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, packageValues, []string{"MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"}, "")
	workerAgentID = resolveDoctorString(workerAgentID, explicitFlags["worker-agent-id"], []string{"MIDNIGHT_WORKER_AGENT_ID", "MIDNIGHT_AGENT_ID"}, packageValues, []string{"MIDNIGHT_WORKER_AGENT_ID", "MIDNIGHT_AGENT_ID"}, "")
	workerHostID = resolveDoctorString(workerHostID, explicitFlags["worker-host-id"], []string{"MIDNIGHT_WORKER_HOST_ID", "MIDNIGHT_HOST_ID"}, packageValues, []string{"MIDNIGHT_WORKER_HOST_ID", "MIDNIGHT_HOST_ID"}, "")
	routeConfigDir = resolveDoctorString(routeConfigDir, explicitFlags["route-config-dir"], []string{"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"}, packageValues, []string{"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"}, liveDefault(live, "/etc/midnight/traefik/dynamic"))
	routeGroup = resolveDoctorString(routeGroup, explicitFlags["route-group"], []string{"MIDNIGHT_WORKER_ROUTE_GROUP"}, packageValues, []string{"MIDNIGHT_WORKER_ROUTE_GROUP"}, "default")
	routeHost = resolveDoctorString(routeHost, explicitFlags["route-public-host"], []string{"MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST"}, packageValues, []string{"MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST"}, "")
	routePath = resolveDoctorString(routePath, explicitFlags["route-public-path"], []string{"MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH"}, packageValues, []string{"MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH"}, "/")
	routeResolver = resolveDoctorString(routeResolver, explicitFlags["route-cert-resolver"], []string{"MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER"}, packageValues, []string{"MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER"}, "")
	if explicitFlags["backup-destination-uri"] {
		backupDestHint = preflight.SanitizeBackupDestination(backupDestURI)
	} else if !backupDestHint.Configured {
		if processValue, ok := os.LookupEnv("MIDNIGHT_BACKUP_DESTINATION_URI"); ok && strings.TrimSpace(processValue) != "" {
			backupDestHint = preflight.SanitizeBackupDestination(processValue)
		} else {
			backupDestHint = packageBackupHint
		}
	}
	// Retain no credential-bearing destination after resolving readiness.
	backupDestURI = ""
	dbDSNPath = resolveDoctorString(dbDSNPath, explicitFlags["db-dsn-fixture"], []string{"MIDNIGHT_DB_DSN_FILE"}, packageValues, []string{"MIDNIGHT_DB_DSN_FILE"}, liveDefault(live, "/etc/midnight/db.dsn"))
	cursorKeyPath = resolveDoctorString(cursorKeyPath, explicitFlags["cursor-key-fixture"], []string{"MIDNIGHT_CURSOR_KEY_PATH"}, packageValues, []string{"MIDNIGHT_CURSOR_KEY_PATH"}, liveDefault(live, "/etc/midnight/cursor.key"))
	registryConfig = resolveDoctorString(registryConfig, explicitFlags["registry-config-fixture"], []string{"MIDNIGHT_REGISTRY_CONFIG"}, packageValues, []string{"MIDNIGHT_REGISTRY_CONFIG"}, liveDefault(live, "/etc/midnight/registry.yml"))
	systemdUnitDir = resolveDoctorString(systemdUnitDir, explicitFlags["systemd-unit-dir"], []string{"MIDNIGHT_SYSTEMD_UNIT_DIR"}, packageValues, nil, liveDefault(live, "/lib/systemd/system"))

	if !explicitFlags["route-public-tls"] && !routeTLS {
		routeTLS, err = resolveDoctorBool("MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS", packageValues, false)
		if err != nil {
			fmt.Fprintf(stderr, "midnight-doctor: %s\n", err.Error())
			return &exitError{code: 3}
		}
	}
	if !explicitFlags["route-target-port"] && routePort == 0 {
		routePort, err = resolveDoctorInt("MIDNIGHT_WORKER_ROUTE_TARGET_PORT", packageValues, 0)
		if err != nil {
			fmt.Fprintf(stderr, "midnight-doctor: %s\n", err.Error())
			return &exitError{code: 3}
		}
	}
	if runtimeBinsCSV == "" && !explicitFlags["runtime-binaries"] {
		runtimeBinsCSV = strings.Join([]string{
			resolveDoctorString("", false, []string{"MIDNIGHT_BUILDKITD_BIN"}, packageValues, []string{"MIDNIGHT_BUILDKITD_BIN"}, liveDefault(live, "buildkitd")),
			resolveDoctorString("", false, []string{"MIDNIGHT_BUILDCTL"}, packageValues, []string{"MIDNIGHT_BUILDCTL"}, liveDefault(live, "buildctl")),
			resolveDoctorString("", false, []string{"MIDNIGHT_REGISTRY_BIN"}, packageValues, []string{"MIDNIGHT_REGISTRY_BIN"}, liveDefault(live, "registry")),
		}, ",")
	}

	source := preflight.HostSource{
		Reader: preflight.OSFileReader{},
		Paths: preflight.HostPaths{
			MountInfo:        mountInfoPath,
			DockerDaemonJSON: daemonJSONPath,
			DockerAPIVersion: dockerVerPath,
			PackageEnv:       packageEnvPath,
			DatabaseDSN:      dbDSNPath,
			CursorKey:        cursorKeyPath,
			RegistryConfig:   registryConfig,
			RouteConfigDir:   routeConfigDir,
			SystemdUnitDir:   systemdUnitDir,
			SSLNT:            ssLNTPath,
		},
		Hints: preflight.HostHints{
			IngressPortsReservedBy:  ingressOwner,
			FirewallMode:            firewallMode,
			MandatoryAccessControl:  macMode,
			TimeSyncStatus:          timeSyncMode,
			HostnameFQDN:            hostnameFQDN,
			InstalledDockerPackages: splitCSV(dockerPkgsCSV),
			RuntimeBinaries:         splitCSV(runtimeBinsCSV),
			DatabaseDSNConfigured:   dbDSNEnv,
			WorkerID:                workerID,
			WorkerRuntimeBackend:    workerBackend,
			WorkerDockerHost:        workerDocker,
			WorkerAgentID:           workerAgentID,
			WorkerHostID:            workerHostID,
			WorkerRouteGroup:        routeGroup,
			WorkerRoutePublicHost:   routeHost,
			WorkerRoutePublicPath:   routePath,
			WorkerRoutePublicTLS:    routeTLS,
			WorkerRouteCertResolver: routeResolver,
			WorkerRouteTargetPort:   routePort,
			BackupDestination:       backupDestHint,
		},
	}

	var filter []string
	profile := "all"
	if explicitFlags["check"] {
		profile = "explicit-checks"
		known := make(map[string]struct{}, len(doctor.LiveCheckRegistry()))
		for _, registered := range doctor.LiveCheckRegistry() {
			known[registered.ID] = struct{}{}
		}
		seen := make(map[string]struct{})
		for _, part := range strings.Split(check, ",") {
			trimmed := strings.TrimSpace(part)
			if _, ok := known[trimmed]; !ok {
				fmt.Fprintf(stderr, "midnight-doctor: --check contains unknown check id %q\n", trimmed)
				return &exitError{code: 3}
			}
			if _, duplicate := seen[trimmed]; duplicate {
				fmt.Fprintf(stderr, "midnight-doctor: --check contains duplicate check id %q\n", trimmed)
				return &exitError{code: 3}
			}
			seen[trimmed] = struct{}{}
			filter = append(filter, trimmed)
		}
	}
	profileFlags := 0
	if explicitFlags["check"] {
		profileFlags++
	}
	if mvpReadiness {
		profileFlags++
	}
	if installReady {
		profileFlags++
	}
	if profileFlags > 1 {
		fmt.Fprintln(stderr, "midnight-doctor: --check, --mvp-readiness, and --install-readiness are mutually exclusive")
		return &exitError{code: 3}
	}
	if mvpReadiness && len(filter) == 0 {
		profile = "mvp-readiness"
		filter = doctor.MVPReadinessCheckIDs()
	}
	if installReady && len(filter) == 0 {
		profile = "install-readiness"
		filter = doctor.InstallReadinessCheckIDs()
	}

	results := doctor.RunLiveChecks(context.Background(), doctor.LiveOptions{
		Source:    source,
		FilterIDs: filter,
		Live:      live,
	})

	if jsonOutput {
		out := struct {
			SchemaVersion int          `json:"schema_version"`
			Live          bool         `json:"live"`
			Profile       string       `json:"profile"`
			ExitCode      int          `json:"exit_code"`
			Results       []wireResult `json:"results"`
		}{SchemaVersion: 1, Live: live, Profile: profile, ExitCode: doctor.ExitCodeForResults(results), Results: toWireResults(results)}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		writeText(stdout, results, live, profile)
	}

	return &exitError{code: doctor.ExitCodeForResults(results)}
}

// hostSourceBundle is the parsed result of resolving the --hostsource
// flag. fixture:<dir> populates the canonical fixture filenames; live
// (or empty) returns nil so the caller's individual --*-fixture flags
// remain the only source of truth.
type hostSourceBundle struct {
	mountInfo      string
	daemonJSON     string
	dockerVersion  string
	packageEnv     string
	dbDSN          string
	cursorKey      string
	registryConfig string
	routeConfigDir string
	systemdUnitDir string
	ssLNT          string
	hints          preflight.HostHints
}

// hostSourceHintsFile is the JSON shape stored at <dir>/hints.json
// inside a `--hostsource=fixture:<dir>` bundle. The field set mirrors
// preflight.HostHints; it is intentionally additive to the rest of the
// CLI surface so the fixture can be passed by file rather than by a
// dozen separate flags. The disposable-VM CI harness writes this file
// at runtime; humans editing fixtures are expected to keep it small.
type hostSourceHintsFile struct {
	IngressPortsReservedBy  string   `json:"ingress_ports_reserved_by,omitempty"`
	FirewallMode            string   `json:"firewall_mode,omitempty"`
	MandatoryAccessControl  string   `json:"mandatory_access_control,omitempty"`
	TimeSyncStatus          string   `json:"time_sync_status,omitempty"`
	HostnameFQDN            string   `json:"hostname_fqdn,omitempty"`
	InstalledDockerPackages []string `json:"installed_docker_packages,omitempty"`
	RuntimeBinaries         []string `json:"runtime_binaries,omitempty"`
	DatabaseDSNConfigured   bool     `json:"database_dsn_configured,omitempty"`
	WorkerID                string   `json:"worker_id,omitempty"`
	WorkerRuntimeBackend    string   `json:"worker_runtime_backend,omitempty"`
	WorkerDockerHost        string   `json:"worker_docker_host,omitempty"`
	WorkerAgentID           string   `json:"worker_agent_id,omitempty"`
	WorkerHostID            string   `json:"worker_host_id,omitempty"`
	WorkerRouteGroup        string   `json:"worker_route_group,omitempty"`
	WorkerRoutePublicHost   string   `json:"worker_route_public_host,omitempty"`
	WorkerRoutePublicPath   string   `json:"worker_route_public_path,omitempty"`
	WorkerRoutePublicTLS    bool     `json:"worker_route_public_tls,omitempty"`
	WorkerRouteCertResolver string   `json:"worker_route_cert_resolver,omitempty"`
	WorkerRouteTargetPort   int      `json:"worker_route_target_port,omitempty"`
	BackupDestinationURI    string   `json:"backup_destination_uri,omitempty"`
}

func resolveHostSource(value string) (*hostSourceBundle, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "live") {
		return nil, nil
	}
	const fixturePrefix = "fixture:"
	if !strings.HasPrefix(strings.ToLower(value), fixturePrefix) {
		return nil, fmt.Errorf("--hostsource=%q is invalid; expected `live` or `fixture:<dir>`", value)
	}
	dir := strings.TrimSpace(value[len(fixturePrefix):])
	if dir == "" {
		return nil, errors.New("--hostsource=fixture: requires a directory path after the colon")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("--hostsource fixture dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--hostsource fixture dir %q is not a directory", dir)
	}

	bundle := &hostSourceBundle{}
	bundle.mountInfo = optionalFixturePath(dir, "mountinfo")
	bundle.daemonJSON = optionalFixturePath(dir, "daemon.json")
	bundle.dockerVersion = optionalFixturePath(dir, "docker-version.txt")
	bundle.packageEnv = optionalFixturePath(dir, "midnight.env")
	bundle.dbDSN = optionalFixturePath(dir, "db.dsn")
	bundle.cursorKey = optionalFixturePath(dir, "cursor.key")
	bundle.registryConfig = optionalFixturePath(dir, "registry.yml")
	if routeDir := filepath.Join(dir, "traefik", "dynamic"); isDir(routeDir) {
		bundle.routeConfigDir = routeDir
	} else if routeDir := filepath.Join(dir, "dynamic"); isDir(routeDir) {
		bundle.routeConfigDir = routeDir
	}
	if systemdDir := filepath.Join(dir, "systemd"); isDir(systemdDir) {
		bundle.systemdUnitDir = systemdDir
	} else if systemdDir := filepath.Join(dir, "units"); isDir(systemdDir) {
		bundle.systemdUnitDir = systemdDir
	}
	bundle.ssLNT = optionalFixturePath(dir, "ss-ltn.txt")

	hintsPath := filepath.Join(dir, "hints.json")
	if data, err := os.ReadFile(hintsPath); err == nil {
		var h hostSourceHintsFile
		if err := json.Unmarshal(data, &h); err != nil {
			return nil, fmt.Errorf("--hostsource fixture %s: %w", hintsPath, err)
		}
		bundle.hints = preflight.HostHints{
			IngressPortsReservedBy:  h.IngressPortsReservedBy,
			FirewallMode:            h.FirewallMode,
			MandatoryAccessControl:  h.MandatoryAccessControl,
			TimeSyncStatus:          h.TimeSyncStatus,
			HostnameFQDN:            h.HostnameFQDN,
			InstalledDockerPackages: append([]string(nil), h.InstalledDockerPackages...),
			RuntimeBinaries:         append([]string(nil), h.RuntimeBinaries...),
			DatabaseDSNConfigured:   h.DatabaseDSNConfigured,
			WorkerID:                h.WorkerID,
			WorkerRuntimeBackend:    h.WorkerRuntimeBackend,
			WorkerDockerHost:        h.WorkerDockerHost,
			WorkerAgentID:           h.WorkerAgentID,
			WorkerHostID:            h.WorkerHostID,
			WorkerRouteGroup:        h.WorkerRouteGroup,
			WorkerRoutePublicHost:   h.WorkerRoutePublicHost,
			WorkerRoutePublicPath:   h.WorkerRoutePublicPath,
			WorkerRoutePublicTLS:    h.WorkerRoutePublicTLS,
			WorkerRouteCertResolver: h.WorkerRouteCertResolver,
			WorkerRouteTargetPort:   h.WorkerRouteTargetPort,
			BackupDestination:       preflight.SanitizeBackupDestination(h.BackupDestinationURI),
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("--hostsource fixture %s: %w", hintsPath, err)
	}

	return bundle, nil
}

func envFirst(names ...string) string {
	for i, name := range names {
		if i == len(names)-1 {
			return name
		}
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func processEnvPresent(names ...string) bool {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func resolveDoctorString(current string, explicit bool, envNames []string, packageValues map[string]string, packageNames []string, fallback string) string {
	if explicit || current != "" {
		return current
	}
	for _, name := range envNames {
		if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, name := range packageNames {
		if value, ok := packageValues[name]; ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return fallback
}

func resolveDoctorBool(name string, packageValues map[string]string, fallback bool) (bool, error) {
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return parseDoctorBool(name, value)
	}
	if value, ok := packageValues[name]; ok && strings.TrimSpace(value) != "" {
		return parseDoctorBool(name, value)
	}
	return fallback, nil
}

func parseDoctorBool(name, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", name)
	}
}

func resolveDoctorInt(name string, packageValues map[string]string, fallback int) (int, error) {
	value := ""
	if processValue, ok := os.LookupEnv(name); ok && strings.TrimSpace(processValue) != "" {
		value = processValue
	} else if packageValue, ok := packageValues[name]; ok && strings.TrimSpace(packageValue) != "" {
		value = packageValue
	}
	if value == "" {
		return fallback, nil
	}
	var parsed int
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil || fmt.Sprintf("%d", parsed) != strings.TrimSpace(value) {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func liveDefault(live bool, value string) string {
	if live {
		return value
	}
	return ""
}

func normalizeDoctorArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, arg := range out {
		switch {
		case arg == "--b1-live-deploy-proof" || arg == "--live-deploy-readiness":
			out[i] = "--mvp-readiness"
		case strings.HasPrefix(arg, "--b1-live-deploy-proof="):
			out[i] = "--mvp-readiness=" + strings.TrimPrefix(arg, "--b1-live-deploy-proof=")
		case strings.HasPrefix(arg, "--live-deploy-readiness="):
			out[i] = "--mvp-readiness=" + strings.TrimPrefix(arg, "--live-deploy-readiness=")
		}
	}
	return out
}

func optionalFixturePath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	info, err := os.Stat(candidate)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return ""
	}
	return candidate
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// wireResult is the JSON-friendly shape emitted by --json. It
// stringifies doctor.Result.Err so JSON output stays well-formed.
type wireResult struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Severity    string `json:"severity,omitempty"`
	DocsURL     string `json:"docs_url,omitempty"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
}

func toWireResults(results []doctor.Result) []wireResult {
	out := make([]wireResult, 0, len(results))
	for _, r := range results {
		w := wireResult{
			ID:          r.ID,
			Description: r.Description,
			Severity:    r.Severity,
			DocsURL:     r.DocsURL,
			Status:      string(r.Status),
			Message:     r.Message,
		}
		if r.Err != nil {
			w.Error = r.Err.Error()
		}
		out = append(out, w)
	}
	return out
}

func writeText(w io.Writer, results []doctor.Result, live bool, profile string) {
	if live {
		fmt.Fprintf(w, "midnight doctor (profile=%s; live host probes enabled)\n", profile)
	} else {
		fmt.Fprintf(w, "midnight doctor (profile=%s; fixture/hint mode; pass --live for live probes)\n", profile)
	}
	for _, r := range results {
		line := fmt.Sprintf("[%s] %s — %s", strings.ToUpper(string(r.Status)), r.ID, r.Description)
		fmt.Fprintln(w, line)
		if r.Message != "" {
			fmt.Fprintf(w, "    %s\n", r.Message)
		}
		if r.Err != nil {
			fmt.Fprintf(w, "    error: %s\n", r.Err.Error())
		}
		if r.DocsURL != "" {
			fmt.Fprintf(w, "    docs: %s\n", r.DocsURL)
		}
	}
}
