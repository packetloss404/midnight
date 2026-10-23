package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/preflight"
	"github.com/midnight/midnight/internal/router"
)

// CanonicalCheckPrefix is the panel-locked id namespace for the live
// `midnight doctor` CLI surface. Adding a new check id is a MINOR-version
// bump; renaming an id is a BREAKING change.
const CanonicalCheckPrefix = "OR-CHK-DOC"

// LiveCheck binds a canonical OR-CHK-DOC id to a live diagnostic
// runner. The runner consumes a HostSource so unit tests can supply
// fixtures for every probe; live execution wires the OS command
// runner + filesystem reader.
type LiveCheck struct {
	ID           string
	Description  string
	Severity     string
	DocsURL      string
	FailureCode  failurecodes.Code
	Run          func(ctx context.Context, src preflight.HostSource) Result
	SafeFix      SafeFixMetadata
	RequiresLive bool
	Remediation  string
}

// LiveCheckRegistry returns the canonical OR-CHK-DOC catalogue in
// stable execution order. The order matters: cgroup v2 runs first
// because every later check depends on a working container runtime
// boot path.
func LiveCheckRegistry() []LiveCheck {
	return []LiveCheck{
		{
			ID:          "OR-CHK-DOC-001",
			Description: "host runs the cgroup v2 unified hierarchy (ADR-005 hard requirement)",
			Severity:    "fatal",
			DocsURL:     "docs/error-codes.md#cgroup_v2_required",
			FailureCode: failurecodes.CGroupV2Required,
			Remediation: "add `systemd.unified_cgroup_hierarchy=1` to the kernel command line and reboot",
			Run:         runCheckCgroupV2,
		},
		{
			ID:          "OR-CHK-DOC-002",
			Description: "/etc/docker/daemon.json is mergeable with the Midnight baseline",
			Severity:    "fatal",
			DocsURL:     "docs/error-codes.md#daemon_json_conflict",
			FailureCode: failurecodes.DaemonJSONConflict,
			Remediation: "reconcile /etc/docker/daemon.json keys with the Midnight baseline (see deploy/debian/daemon-json-merge.md) before retrying",
			Run:         runCheckDaemonJSON,
		},
		{
			ID:          "OR-CHK-DOC-003",
			Description: "Docker Engine API is at v1.43 or newer (ADR-004)",
			Severity:    "fatal",
			DocsURL:     "docs/error-codes.md#docker_engine_unsupported",
			FailureCode: failurecodes.DockerEngineUnsupported,
			Remediation: "upgrade Docker Engine to >= 24.0 (Engine API v1.43)",
			Run:         runCheckDockerAPI,
		},
		{
			ID:          "OR-CHK-DOC-004",
			Description: "ingress ports 80/443 are free (or reserved by Midnight)",
			Severity:    "fatal",
			DocsURL:     "docs/error-codes.md#ingress_port_bound",
			FailureCode: failurecodes.IngressPortBound,
			Remediation: "stop the conflicting listener on :80/:443 (Apache, nginx, etc.), or pass --ingress-reserved-by=<service> when it is the operator's own ingress",
			Run:         runCheckIngressPorts,
		},
		{
			ID:          "OR-CHK-DOC-005",
			Description: "host firewall mode is one of UFW, firewalld, nftables, iptables, or none",
			Severity:    "warn",
			DocsURL:     "docs/runbooks/firewall.md",
			FailureCode: "",
			Remediation: "open ports 80 + 443 inbound (or document an explicit reverse proxy in front)",
			Run:         runCheckFirewall,
		},
		{
			ID:          "OR-CHK-DOC-006",
			Description: "AppArmor or SELinux is enforcing or complain for default container hardening",
			Severity:    "warn",
			DocsURL:     "deploy/apparmor/README.md",
			FailureCode: "",
			Remediation: "load the midnight-default AppArmor profile or set SELinux to enforcing/permissive",
			Run:         runCheckMAC,
		},
		{
			ID:          "OR-CHK-DOC-007",
			Description: "host clock is synchronized (NTP/chrony) and within 30s of UTC",
			Severity:    "warn",
			DocsURL:     "docs/error-codes.md#time_sync_drift",
			FailureCode: failurecodes.TimeSyncDrift,
			Remediation: "enable systemd-timesyncd or chrony and resync NTP",
			Run:         runCheckTimeSync,
		},
		{
			ID:          "OR-CHK-DOC-008",
			Description: "hostname resolves and FQDN is set",
			Severity:    "warn",
			DocsURL:     "docs/runbooks/hostname.md",
			FailureCode: "",
			Remediation: "set a fully-qualified hostname via `hostnamectl set-hostname` and ensure /etc/hosts resolves it",
			Run:         runCheckHostname,
		},
		{
			ID:          "OR-CHK-DOC-009",
			Description: "the distro `docker.io` package is not installed (Midnight requires upstream docker-ce or compatible)",
			Severity:    "fatal",
			DocsURL:     "deploy/debian/README.Debian",
			FailureCode: failurecodes.DockerEngineUnsupported,
			Remediation: "uninstall `docker.io` and install `docker-ce` from the Docker upstream APT repo",
			Run:         runCheckDistroDockerIORejection,
		},
		{
			ID:          "OR-CHK-DOC-010",
			Description: "durable Postgres queue/storage DSN is configured",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-010",
			FailureCode: "",
			Remediation: "set MIDNIGHT_DB_DSN or write a Postgres DSN to /etc/midnight/db.dsn before starting Midnight services",
			Run:         runCheckDatabaseDSN,
		},
		{
			ID:          "OR-CHK-DOC-011",
			Description: "cursor signing key exists and meets the package length budget",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-011",
			FailureCode: "",
			Remediation: "generate /etc/midnight/cursor.key with at least 32 random bytes and mode 0600",
			Run:         runCheckCursorKey,
		},
		{
			ID:          "OR-CHK-DOC-012",
			Description: "local registry config is present for the registry service wrapper",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-012",
			FailureCode: "",
			Remediation: "install /etc/midnight/registry.yml from /usr/share/midnight/registry.yml.default",
			Run:         runCheckRegistryConfig,
		},
		{
			ID:          "OR-CHK-DOC-013",
			Description: "external runtime binaries required by wrappers are resolvable",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-013",
			FailureCode: "",
			Remediation: "install the pinned buildkitd, buildctl, and registry binaries or configure MIDNIGHT_*_BIN paths",
			Run:         runCheckRuntimeBinaries,
		},
		{
			ID:          "OR-CHK-DOC-014",
			Description: "worker runtime backend is configured for Docker deploy execution",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-014",
			FailureCode: "",
			Remediation: "set MIDNIGHT_WORKER_RUNTIME_BACKEND=docker before enabling deploy workers",
			Run:         runCheckWorkerDockerBackend,
		},
		{
			ID:          "OR-CHK-DOC-015",
			Description: "worker route config directory is configured and writable",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-015",
			FailureCode: "",
			Remediation: "set MIDNIGHT_WORKER_ROUTE_CONFIG_DIR to the Traefik dynamic directory and ensure the midnight user can create child directories there",
			Run:         runCheckRouteConfigDirWritable,
		},
		{
			ID:          "OR-CHK-DOC-016",
			Description: "Traefik dynamic directory holds no unwatched route artifacts",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-016",
			FailureCode: "",
			Remediation: "remove route artifacts left in <route-config-dir>/<route-group>/ subdirectories; the publisher writes flat <route-config-dir>/<route-group>[.<route>].traefik.dynamic.yaml files",
			Run:         runCheckTraefikDynamicDirectoryShape,
		},
		{
			ID:          "OR-CHK-DOC-017",
			Description: "worker route environment is coherent for deploy route publication",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-017",
			FailureCode: "",
			Remediation: "set the MIDNIGHT_WORKER_ROUTE_* and MIDNIGHT_WORKER_* identifiers consistently before enabling deploy workers",
			Run:         runCheckWorkerRouteEnvironment,
		},
		{
			ID:          "OR-CHK-DOC-018",
			Description: "package environment file is readable for systemd units",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-018",
			FailureCode: "",
			Remediation: "install /etc/midnight/midnight.env from /usr/share/midnight/midnight.env.default and ensure it is readable by systemd",
			Run:         runCheckPackageEnvReadable,
		},
		{
			ID:          "OR-CHK-DOC-019",
			Description: "required systemd units are present and loadable",
			Severity:    "fatal",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-019",
			FailureCode: "",
			Remediation: "install the midnight systemd unit files and run `systemctl daemon-reload` before enabling services",
			Run:         runCheckSystemdUnitsLoadable,
		},
		{
			ID:          "OR-CHK-DOC-020",
			Description: "backup destination status is valid when an off-host destination is configured",
			Severity:    "warn",
			DocsURL:     "docs/doctor/checks.md#or-chk-doc-020",
			FailureCode: "",
			Remediation: "set MIDNIGHT_BACKUP_DESTINATION_URI to a supported off-host URI such as s3://, gcs://, sftp://, rsync://, or ssh://",
			Run:         runCheckBackupDestinationStatus,
		},
	}
}

// InstallReadinessCheckIDs returns the package install readiness slice.
func InstallReadinessCheckIDs() []string {
	return []string{
		"OR-CHK-DOC-010",
		"OR-CHK-DOC-013",
		"OR-CHK-DOC-015",
		"OR-CHK-DOC-018",
		"OR-CHK-DOC-019",
	}
}

// MVPReadinessCheckIDs returns the neutral operator readiness slice for the
// single-server MVP deploy path. It reuses the durable Postgres DSN check and
// adds the worker/runtime, route provider, and backup-destination checks that
// do not require executing a deployment.
func MVPReadinessCheckIDs() []string {
	return []string{
		"OR-CHK-DOC-010",
		"OR-CHK-DOC-014",
		"OR-CHK-DOC-015",
		"OR-CHK-DOC-016",
		"OR-CHK-DOC-017",
		"OR-CHK-DOC-020",
	}
}

// LiveDeployProofCheckIDs is a compatibility alias for the neutral MVP
// readiness check slice.
func LiveDeployProofCheckIDs() []string {
	return MVPReadinessCheckIDs()
}

// LiveOptions parameterizes the live doctor run. Unset fields fall
// back to fixture-friendly defaults so default `go test` runs do not
// require a live host.
type LiveOptions struct {
	Source preflight.HostSource

	// FilterIDs, when non-empty, limits the run to checks whose ID
	// is present in the slice (--check=ID flag).
	FilterIDs []string

	// Live indicates the caller has set MIDNIGHT_DOCTOR_LIVE=1 (or
	// equivalent) so probes that require shell-out / network may
	// run. When false, RequiresLive checks return StatusSkip with a
	// "live mode disabled" message.
	Live bool

	// HostnameFixture is the deterministic hostname to use when
	// Source.Reader does not expose /etc/hostname. Tests use this to
	// pin the fixture path.
	HostnameFixture string
}

// RunLiveChecks executes the OR-CHK-DOC registry against the supplied
// HostSource. The returned slice is in registry order; failures do
// NOT short-circuit so operators see every diagnosis at once.
func RunLiveChecks(ctx context.Context, opts LiveOptions) []Result {
	registry := LiveCheckRegistry()
	filter := buildFilter(opts.FilterIDs)
	results := make([]Result, 0, len(registry))
	for _, check := range registry {
		if filter != nil {
			if _, ok := filter[check.ID]; !ok {
				continue
			}
		}
		base := Result{
			ID:          check.ID,
			Description: check.Description,
			Severity:    check.Severity,
			DocsURL:     check.DocsURL,
		}
		if check.Run == nil {
			base.Status = StatusConfigError
			base.Message = "check has no runner"
			results = append(results, base)
			continue
		}
		if check.RequiresLive && !opts.Live {
			base.Status = StatusSkip
			base.Message = "skipped: set MIDNIGHT_DOCTOR_LIVE=1 to run live host probes"
			results = append(results, base)
			continue
		}
		out := check.Run(ctx, opts.Source)
		if out.ID == "" {
			out.ID = check.ID
		}
		if out.Description == "" {
			out.Description = check.Description
		}
		if out.Severity == "" {
			out.Severity = check.Severity
		}
		if out.DocsURL == "" {
			out.DocsURL = check.DocsURL
		}
		if out.Status == "" {
			out.Status = StatusConfigError
			if strings.TrimSpace(out.Message) == "" {
				out.Message = "check returned empty status"
			} else {
				out.Message = strings.TrimSpace(out.Message) + "; check returned empty status"
			}
		}
		results = append(results, out)
	}
	return results
}

// ExitCodeForResults maps the panel-locked exit-code table:
//
//	0 — every applicable check passed (or skipped intentionally).
//	1 — at least one warn-severity check produced status="warn".
//	2 — at least one check produced status="fail".
//	3 — at least one check produced status="config_error" (the
//	    runner could not even decide).
//
// Higher-precedence statuses dominate. Skipped checks never trigger
// a non-zero exit on their own.
func ExitCodeForResults(results []Result) int {
	exit := 0
	for _, r := range results {
		switch r.Status {
		case StatusFail:
			if exit < 2 {
				exit = 2
			}
		case StatusConfigError:
			exit = 3
		case StatusWarn:
			if exit < 1 {
				exit = 1
			}
		}
	}
	return exit
}

func buildFilter(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ----- Individual check runners ---------------------------------------------

func runCheckCgroupV2(ctx context.Context, src preflight.HostSource) Result {
	if src.Reader == nil && src.Runner == nil {
		return Result{Status: StatusSkip, Message: "no host source: provide --mountinfo-fixture or set MIDNIGHT_DOCTOR_LIVE=1"}
	}
	if src.Paths.MountInfo != "" {
		ok, err := preflight.HasCgroupV2FromSource(src)
		if err != nil {
			return Result{Status: StatusConfigError, Err: err, Message: "could not read mountinfo"}
		}
		if !ok {
			return Result{Status: StatusFail, Message: "host runs cgroup v1; ADR-005 hard-requires cgroup v2"}
		}
		return Result{Status: StatusPass, Message: "cgroup v2 unified hierarchy detected"}
	}
	if src.Runner == nil {
		return Result{Status: StatusSkip, Message: "no command runner; cannot probe /sys/fs/cgroup"}
	}
	ok, err := preflight.HasCgroupV2FSTypeFromSource(ctx, src)
	if err != nil {
		return Result{Status: StatusConfigError, Err: err}
	}
	if !ok {
		return Result{Status: StatusFail, Message: "stat -fc %T /sys/fs/cgroup did not return cgroup2fs"}
	}
	return Result{Status: StatusPass, Message: "cgroup v2 detected via stat probe"}
}

func runCheckDaemonJSON(_ context.Context, src preflight.HostSource) Result {
	if src.Paths.DockerDaemonJSON == "" || src.Reader == nil {
		return Result{Status: StatusSkip, Message: "daemon.json fixture not configured"}
	}
	merge, err := preflight.MergeDockerDaemonBaselineFromSource(src)
	if err != nil {
		var conflict *preflight.DockerDaemonConflictError
		if errors.As(err, &conflict) {
			return Result{Status: StatusFail, Message: conflict.Error()}
		}
		// Missing file is a fixable warn — installer will write the
		// baseline.
		if errors.Is(err, os.ErrNotExist) {
			return Result{Status: StatusWarn, Message: "no /etc/docker/daemon.json present; installer will write the Midnight baseline"}
		}
		return Result{Status: StatusConfigError, Err: err}
	}
	if merge.Changed {
		return Result{Status: StatusWarn, Message: "daemon.json baseline merge would rewrite the file (no conflicts)"}
	}
	return Result{Status: StatusPass, Message: "daemon.json already matches the Midnight baseline"}
}

func runCheckDockerAPI(ctx context.Context, src preflight.HostSource) Result {
	if src.Paths.DockerAPIVersion == "" && src.Runner == nil {
		return Result{Status: StatusSkip, Message: "Docker API probe not configured"}
	}
	ok, err := preflight.DockerAPISupportsPhase0FromSource(src)
	if err != nil {
		return Result{Status: StatusConfigError, Err: err}
	}
	if !ok {
		return Result{Status: StatusFail, Message: "Docker Engine API is below v1.43 (ADR-004 floor)"}
	}
	return Result{Status: StatusPass, Message: "Docker Engine API >= v1.43"}
}

func runCheckIngressPorts(ctx context.Context, src preflight.HostSource) Result {
	if src.Paths.SSLNT == "" || src.Reader == nil {
		return Result{Status: StatusSkip, Message: "ss -ltn fixture not configured"}
	}
	res := preflight.CheckIngressPortsFromPath(ctx, src, src.Paths.SSLNT)
	switch res.Status {
	case preflight.StatusPass:
		return Result{Status: StatusPass, Message: res.Message}
	case preflight.StatusFail:
		// The install path requires an operator-installed ingress that owns
		// 80/443, so a bound port is only a conflict when the operator has not
		// claimed it. An operator who declares the owner gets a recorded
		// reservation instead of a fatal that no correct host can clear.
		if owner := strings.TrimSpace(src.Hints.IngressPortsReservedBy); owner != "" {
			return Result{Status: StatusPass, Message: fmt.Sprintf(
				"ingress ports 80/443 are reserved by %s: %s", owner, res.Message,
			)}
		}
		return Result{Status: StatusFail, Message: res.Message}
	default:
		return Result{Status: StatusSkip, Message: res.Message}
	}
}

func runCheckFirewall(_ context.Context, src preflight.HostSource) Result {
	mode := strings.TrimSpace(src.Hints.FirewallMode)
	if mode == "" {
		return Result{Status: StatusSkip, Message: "no firewall hint provided"}
	}
	switch strings.ToLower(mode) {
	case "ufw", "firewalld", "nftables", "iptables", "none":
		return Result{Status: StatusPass, Message: fmt.Sprintf("firewall mode reported: %s", mode)}
	default:
		return Result{Status: StatusWarn, Message: fmt.Sprintf("unrecognized firewall mode %q", mode)}
	}
}

func runCheckMAC(_ context.Context, src preflight.HostSource) Result {
	mode := strings.TrimSpace(src.Hints.MandatoryAccessControl)
	if mode == "" {
		return Result{Status: StatusSkip, Message: "no AppArmor / SELinux hint provided"}
	}
	switch strings.ToLower(mode) {
	case "apparmor-enforce", "apparmor-complain", "selinux-enforcing", "selinux-permissive":
		return Result{Status: StatusPass, Message: fmt.Sprintf("MAC mode: %s", mode)}
	case "disabled":
		return Result{Status: StatusWarn, Message: "AppArmor/SELinux disabled; consider loading midnight-default profile"}
	default:
		return Result{Status: StatusWarn, Message: fmt.Sprintf("unrecognized MAC mode %q", mode)}
	}
}

func runCheckTimeSync(_ context.Context, src preflight.HostSource) Result {
	if src.Hints.TimeSyncStatus == "" {
		return Result{Status: StatusSkip, Message: "no time-sync hint provided"}
	}
	switch strings.ToLower(src.Hints.TimeSyncStatus) {
	case "synchronized", "ok", "ntp", "chrony":
		return Result{Status: StatusPass, Message: "host clock reported synchronized"}
	case "drift":
		return Result{Status: StatusWarn, Message: "host clock reports drift > 30s"}
	case "unsynchronized", "offline":
		return Result{Status: StatusFail, Message: "host clock is unsynchronized; ACME ops will be refused"}
	default:
		return Result{Status: StatusWarn, Message: fmt.Sprintf("unrecognized time-sync hint %q", src.Hints.TimeSyncStatus)}
	}
}

func runCheckHostname(_ context.Context, src preflight.HostSource) Result {
	hostname := strings.TrimSpace(src.Hints.HostnameFQDN)
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = strings.TrimSpace(h)
		}
	}
	if hostname == "" {
		return Result{Status: StatusSkip, Message: "no hostname hint and os.Hostname() returned empty"}
	}
	if !strings.Contains(hostname, ".") {
		return Result{Status: StatusWarn, Message: fmt.Sprintf("hostname %q is not fully qualified (no dot)", hostname)}
	}
	return Result{Status: StatusPass, Message: fmt.Sprintf("FQDN: %s", hostname)}
}

func runCheckDistroDockerIORejection(_ context.Context, src preflight.HostSource) Result {
	pkgs := src.Hints.InstalledDockerPackages
	if len(pkgs) == 0 {
		return Result{Status: StatusSkip, Message: "no installed-package hint provided"}
	}
	for _, name := range pkgs {
		if strings.EqualFold(strings.TrimSpace(name), "docker.io") {
			return Result{Status: StatusFail, Message: "distro `docker.io` package detected; uninstall and install `docker-ce` from the Docker upstream APT repo"}
		}
	}
	return Result{Status: StatusPass, Message: "no distro `docker.io` package detected"}
}

func runCheckDatabaseDSN(_ context.Context, src preflight.HostSource) Result {
	env, _ := preflight.PackageEnvValues(src)
	if src.Hints.DatabaseDSNConfigured {
		return Result{Status: StatusPass, Message: "Postgres DSN is configured via environment"}
	}
	if strings.TrimSpace(env["MIDNIGHT_DB_DSN"]) != "" || strings.TrimSpace(env["MIDNIGHT_DATABASE_URL"]) != "" {
		return Result{Status: StatusPass, Message: "Postgres DSN is configured via package environment file"}
	}
	dsnPath := strings.TrimSpace(src.Paths.DatabaseDSN)
	if dsnPath == "" {
		dsnPath = strings.TrimSpace(env["MIDNIGHT_DB_DSN_FILE"])
	}
	if dsnPath == "" || src.Reader == nil {
		return Result{Status: StatusSkip, Message: "database DSN source not configured"}
	}
	dsn, err := srcWithDatabaseDSNPath(src, dsnPath).ReadDatabaseDSN()
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

func runCheckCursorKey(_ context.Context, src preflight.HostSource) Result {
	if src.Paths.CursorKey == "" || src.Reader == nil {
		return Result{Status: StatusSkip, Message: "cursor key source not configured"}
	}
	data, err := src.ReadCursorKey()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Status: StatusFail, Message: "cursor key file is missing"}
		}
		return Result{Status: StatusConfigError, Err: err, Message: "could not read cursor key"}
	}
	key := strings.TrimRight(string(data), "\r\n")
	if len(key) < 32 {
		return Result{Status: StatusFail, Message: "cursor key must contain at least 32 bytes after trimming line endings"}
	}
	if len(data) > 4096 {
		return Result{Status: StatusFail, Message: "cursor key file exceeds the 4 KiB package budget"}
	}
	return Result{Status: StatusPass, Message: "cursor key length is within the package budget"}
}

func runCheckRegistryConfig(_ context.Context, src preflight.HostSource) Result {
	if src.Paths.RegistryConfig == "" || src.Reader == nil {
		return Result{Status: StatusSkip, Message: "registry config source not configured"}
	}
	data, err := src.ReadRegistryConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Status: StatusFail, Message: "registry config file is missing"}
		}
		return Result{Status: StatusConfigError, Err: err, Message: "could not read registry config"}
	}
	if strings.TrimSpace(string(data)) == "" {
		return Result{Status: StatusFail, Message: "registry config file is empty"}
	}
	return Result{Status: StatusPass, Message: "registry config file is present"}
}

func runCheckRuntimeBinaries(_ context.Context, src preflight.HostSource) Result {
	result := preflight.CheckRuntimeBinariesPresent(src)
	if result.Message == "runtime binary paths not provided" {
		result.Message = "runtime binary hints not configured"
	}
	return fromPreflightResult(result)
}

func runCheckWorkerDockerBackend(_ context.Context, src preflight.HostSource) Result {
	backend := strings.ToLower(strings.TrimSpace(src.Hints.WorkerRuntimeBackend))
	if backend == "" {
		return Result{Status: StatusFail, Message: "worker runtime backend is not configured; set MIDNIGHT_WORKER_RUNTIME_BACKEND=docker"}
	}
	switch backend {
	case "docker":
		host := strings.TrimSpace(src.Hints.WorkerDockerHost)
		if host == "" {
			return Result{Status: StatusPass, Message: "worker runtime backend is docker; Docker SDK will use DOCKER_HOST/default local socket"}
		}
		return Result{Status: StatusPass, Message: "worker runtime backend is docker with an explicit Docker host"}
	case "local":
		return Result{Status: StatusFail, Message: "worker runtime backend is local; MVP deploy readiness requires docker"}
	default:
		return Result{Status: StatusConfigError, Message: fmt.Sprintf("unsupported worker runtime backend %q", backend)}
	}
}

func runCheckRouteConfigDirWritable(_ context.Context, src preflight.HostSource) Result {
	env, _ := preflight.PackageEnvValues(src)
	root := strings.TrimSpace(src.Paths.RouteConfigDir)
	if root == "" {
		root = strings.TrimSpace(env["MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"])
	}
	if root == "" {
		return Result{Status: StatusFail, Message: "worker route config directory is not configured; set MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"}
	}
	if _, err := router.NewTraefikDynamicPublicationDirectory(root); err != nil {
		return Result{Status: StatusFail, Err: err, Message: "route config directory is not a usable Traefik dynamic publication root"}
	}
	probe, err := os.MkdirTemp(root, ".midnight-doctor-route-")
	if err != nil {
		return Result{Status: StatusFail, Err: err, Message: "route config directory is not writable by this process"}
	}
	if err := os.RemoveAll(probe); err != nil {
		return Result{Status: StatusWarn, Err: err, Message: "route config directory is writable, but cleanup of the doctor probe failed"}
	}
	return Result{Status: StatusPass, Message: fmt.Sprintf("route config directory is writable: %s", filepath.Clean(root))}
}

func runCheckTraefikDynamicDirectoryShape(_ context.Context, src preflight.HostSource) Result {
	root := strings.TrimSpace(src.Paths.RouteConfigDir)
	if root == "" {
		return Result{Status: StatusFail, Message: "route config directory is not configured"}
	}
	pubdir, err := router.NewTraefikDynamicPublicationDirectory(root)
	if err != nil {
		return Result{Status: StatusFail, Err: err, Message: "route config directory is not a Traefik dynamic publication root"}
	}
	routeGroup := strings.TrimSpace(src.Hints.WorkerRouteGroup)
	if routeGroup == "" {
		routeGroup = "default"
	}
	routeGroupDir, err := router.SanitizeRouteGroupFilename(routeGroup)
	if err != nil {
		return Result{Status: StatusFail, Err: err, Message: "worker route group cannot be mapped to a safe artifact directory"}
	}
	// Traefik's file provider loads subdirectory files when it starts but only
	// watches the configured directory itself, so an artifact left in a
	// `<group>/` subdirectory by a pre-flat release is read once at startup and
	// never refreshed. It has to be flagged: it resurrects a stale router that
	// conflicts with the live flat artifact.
	stale, err := staleRouteArtifactSubdirectoryFiles(pubdir.Root())
	if err != nil {
		return Result{Status: StatusConfigError, Err: err, Message: "could not inspect Traefik dynamic directory"}
	}
	if len(stale) > 0 {
		return Result{Status: StatusFail, Message: fmt.Sprintf(
			"found unwatched route artifacts in subdirectories: %s",
			strings.Join(stale, ", "),
		)}
	}
	expected := filepath.Join(pubdir.Root(), routeGroupDir+"."+router.TraefikDynamicArtifactExtension)
	return Result{Status: StatusPass, Message: fmt.Sprintf("Traefik dynamic artifact path shape is %s", expected)}
}

// staleRouteArtifactSubdirectoryFiles returns route artifacts that live one
// level below the publication root. Names are returned relative to the root so
// the finding stays support-safe.
func staleRouteArtifactSubdirectoryFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var stale []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, child := range children {
			if child.IsDir() || !strings.HasSuffix(child.Name(), router.TraefikDynamicArtifactExtension) {
				continue
			}
			stale = append(stale, filepath.Join(entry.Name(), child.Name()))
		}
	}
	sort.Strings(stale)
	return stale, nil
}

func runCheckWorkerRouteEnvironment(_ context.Context, src preflight.HostSource) Result {
	var problems []string
	workerID := strings.TrimSpace(src.Hints.WorkerID)
	if workerID == "" {
		problems = append(problems, "MIDNIGHT_WORKER_ID is empty")
	}
	if strings.TrimSpace(src.Paths.RouteConfigDir) == "" {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_CONFIG_DIR is empty")
	}
	publicPath := strings.TrimSpace(src.Hints.WorkerRoutePublicPath)
	if publicPath == "" {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH is empty")
	} else if !strings.HasPrefix(publicPath, "/") {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH must start with /")
	}
	if strings.TrimSpace(src.Hints.WorkerRouteCertResolver) != "" && !src.Hints.WorkerRoutePublicTLS {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER requires MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=true")
	}
	if src.Hints.WorkerRouteTargetPort < 0 {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_TARGET_PORT must be positive")
	}
	routeGroup := strings.TrimSpace(src.Hints.WorkerRouteGroup)
	if routeGroup == "" {
		routeGroup = "default"
	}
	if _, err := router.SanitizeRouteGroupFilename(routeGroup); err != nil {
		problems = append(problems, "MIDNIGHT_WORKER_ROUTE_GROUP must map to a safe path segment")
	}
	if len(problems) > 0 {
		return Result{Status: StatusFail, Message: strings.Join(problems, "; ")}
	}

	agentID := strings.TrimSpace(src.Hints.WorkerAgentID)
	hostID := strings.TrimSpace(src.Hints.WorkerHostID)
	if agentID == "" || hostID == "" {
		return Result{Status: StatusWarn, Message: "worker route env is usable; agent/host IDs will be derived from MIDNIGHT_WORKER_ID unless explicitly set"}
	}
	return Result{Status: StatusPass, Message: "worker route environment is coherent for route publication"}
}

func runCheckPackageEnvReadable(_ context.Context, src preflight.HostSource) Result {
	return fromPreflightResult(preflight.CheckPackageEnvReadable(src))
}

func runCheckSystemdUnitsLoadable(_ context.Context, src preflight.HostSource) Result {
	return fromPreflightResult(preflight.CheckSystemdUnitsLoadable(src))
}

func runCheckBackupDestinationStatus(_ context.Context, src preflight.HostSource) Result {
	hint := src.Hints.BackupDestination
	if !hint.Configured && strings.TrimSpace(src.Paths.PackageEnv) != "" {
		if text, err := src.ReadPackageEnv(); err == nil {
			_, _, packageHint, parseErr := preflight.ParsePackageReadinessEnv(text)
			if parseErr == nil {
				hint = packageHint
			}
		}
	}
	if !hint.Configured {
		hint = preflight.SanitizeBackupDestination(os.Getenv("MIDNIGHT_BACKUP_DESTINATION_URI"))
	}
	if !hint.Configured {
		return Result{Status: StatusSkip, Message: "backup destination is not configured; no status probe available"}
	}
	if !hint.URIValid {
		return Result{Status: StatusFail, Message: "off-host backup destination URI is invalid"}
	}
	switch hint.Scheme {
	case "s3", "gcs", "sftp", "rsync", "ssh":
		return Result{Status: StatusPass, Message: fmt.Sprintf("off-host backup destination is configured using %s://; restore execution availability is not asserted by doctor", hint.Scheme)}
	case "file", "local", "":
		return Result{Status: StatusFail, Message: "no off-host backup destination is configured"}
	default:
		return Result{Status: StatusFail, Message: "backup destination URI is not a supported off-host destination"}
	}
}

func fromPreflightResult(result preflight.Result) Result {
	return Result{
		Status:  Status(result.Status),
		Message: result.Message,
		Err:     result.Err,
	}
}

func srcWithDatabaseDSNPath(src preflight.HostSource, path string) preflight.HostSource {
	src.Paths.DatabaseDSN = path
	return src
}
