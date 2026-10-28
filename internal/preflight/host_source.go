package preflight

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Command describes an explicit command probe supplied by caller wiring.
type Command struct {
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name"`
	Args         []string      `json:"args"`
	Timeout      time.Duration `json:"-"`
	RequiresRoot bool          `json:"requires_root"`
}

// CommandRunner runs caller-selected commands. Tests inject fakes; no default
// host-inspecting runner is used by preflight package entry points.
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// CommandRunnerFunc adapts a function to CommandRunner.
type CommandRunnerFunc func(context.Context, string, ...string) ([]byte, error)

// Run implements CommandRunner.
func (f CommandRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// HostCommands names explicit commands used by live adapters.
type HostCommands struct {
	CgroupFSType     Command
	DockerAPIVersion Command
}

// FileReader reads caller-selected host or fixture files.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// FileReaderFunc adapts a function to FileReader.
type FileReaderFunc func(path string) ([]byte, error)

// ReadFile implements FileReader.
func (f FileReaderFunc) ReadFile(path string) ([]byte, error) {
	return f(path)
}

// OSFileReader reads explicit paths from the local filesystem.
type OSFileReader struct{}

// ReadFile implements FileReader.
func (OSFileReader) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// HostPaths names explicit inputs used by live adapters.
type HostPaths struct {
	MountInfo        string
	DockerAPIVersion string
	DockerDaemonJSON string
	PackageEnv       string
	DatabaseDSN      string
	CursorKey        string
	RegistryConfig   string
	RouteConfigDir   string
	SystemdUnitDir   string
	// SSLNT is the path to a captured `ss -ltn` listener output
	// fixture used by the live doctor's port-bind probe. It stays
	// fixture-driven so default `go test` does not need to shell out.
	SSLNT string
}

// HostHints carries small, opt-in operator-supplied facts the live
// doctor consumes when no probe is wired (firewall mode, time-sync
// status, distro package list, hostname/FQDN). The package and live
// adapters can promote these hints to direct probes as backend wiring
// becomes available.
type HostHints struct {
	FirewallMode string
	// IngressPortsReservedBy names the operator's own ingress service when it
	// legitimately owns ports 80/443. The install path requires an
	// operator-installed Traefik holding those ports, so an unqualified
	// "something is listening" cannot be treated as a conflict.
	IngressPortsReservedBy  string
	MandatoryAccessControl  string
	TimeSyncStatus          string
	HostnameFQDN            string
	InstalledDockerPackages []string
	RuntimeBinaries         []string
	DatabaseDSNConfigured   bool
	WorkerID                string
	WorkerRuntimeBackend    string
	WorkerDockerHost        string
	WorkerAgentID           string
	WorkerHostID            string
	WorkerRouteGroup        string
	WorkerRoutePublicHost   string
	WorkerRoutePublicPath   string
	WorkerRoutePublicTLS    bool
	WorkerRouteCertResolver string
	WorkerRouteTargetPort   int
	BackupDestination       BackupDestinationHint
}

// HostSource reads host facts from injected readers and explicit paths only.
type HostSource struct {
	Reader   FileReader
	Runner   CommandRunner
	Paths    HostPaths
	Commands HostCommands
	Hints    HostHints
	// FixturePathsOnly prevents captured package configuration from supplying
	// additional filesystem paths. Explicit Paths and RuntimeBinaries hints
	// remain usable; missing fixture inputs never borrow installed files.
	FixturePathsOnly bool
}

// ReadMountInfo reads the configured mountinfo source.
func (s HostSource) ReadMountInfo() (string, error) {
	return s.readText("mountinfo", s.Paths.MountInfo)
}

// ReadDockerAPIVersionText reads the configured Docker API version text source.
func (s HostSource) ReadDockerAPIVersionText() (string, error) {
	return s.ReadDockerAPIVersionTextContext(context.Background())
}

// ReadDockerAPIVersionTextContext reads or runs the configured Docker API version source.
func (s HostSource) ReadDockerAPIVersionTextContext(ctx context.Context) (string, error) {
	if s.Paths.DockerAPIVersion == "" && s.Runner != nil {
		data, err := s.runCommandContext(ctx, "Docker API version", s.dockerAPIVersionCommand())
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return s.readText("Docker API version text", s.Paths.DockerAPIVersion)
}

// ReadCgroupFSType runs the configured cgroup filesystem type probe.
func (s HostSource) ReadCgroupFSType(ctx context.Context) (string, error) {
	data, err := s.runCommandContext(ctx, "cgroup filesystem type", s.cgroupFSTypeCommand())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadDockerDaemonJSON reads the configured daemon.json source.
func (s HostSource) ReadDockerDaemonJSON() ([]byte, error) {
	return s.readBytes("daemon.json", s.Paths.DockerDaemonJSON)
}

// ReadPackageEnv reads the configured Midnight package EnvironmentFile source.
func (s HostSource) ReadPackageEnv() (string, error) {
	return s.readText("package env", s.Paths.PackageEnv)
}

// ReadDatabaseDSN reads the configured Postgres DSN file source.
func (s HostSource) ReadDatabaseDSN() (string, error) {
	return s.readText("database DSN", s.Paths.DatabaseDSN)
}

// ReadCursorKey reads the configured cursor-key source.
func (s HostSource) ReadCursorKey() ([]byte, error) {
	return s.readBytes("cursor key", s.Paths.CursorKey)
}

// ReadRegistryConfig reads the configured local registry config source.
func (s HostSource) ReadRegistryConfig() ([]byte, error) {
	return s.readBytes("registry config", s.Paths.RegistryConfig)
}

func (s HostSource) readText(name, path string) (string, error) {
	data, err := s.readBytes(name, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s HostSource) readBytes(name, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s path is required", name)
	}
	if s.Reader == nil {
		return nil, fmt.Errorf("file reader is required")
	}
	return s.Reader.ReadFile(path)
}

func (s HostSource) runCommand(name string, command Command) ([]byte, error) {
	return s.runCommandContext(context.Background(), name, command)
}

func (s HostSource) runCommandContext(ctx context.Context, name string, command Command) ([]byte, error) {
	if command.Name == "" {
		return nil, fmt.Errorf("%s command is required", name)
	}
	if s.Runner == nil {
		return nil, fmt.Errorf("command runner is required")
	}
	allowed, err := allowedCommandSpecForCommand(command)
	if err != nil {
		return nil, err
	}
	command = allowed.Command
	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}
	return s.Runner.Run(ctx, command.Name, command.Args...)
}

func (s HostSource) cgroupFSTypeCommand() Command {
	if s.Commands.CgroupFSType.Name != "" {
		return s.Commands.CgroupFSType
	}
	return CgroupFSTypeCommandSpec().Command
}

func (s HostSource) dockerAPIVersionCommand() Command {
	if s.Commands.DockerAPIVersion.Name != "" {
		return s.Commands.DockerAPIVersion
	}
	return DockerAPICommandSpec().Command
}
