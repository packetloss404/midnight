package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const runtimeProbeTimeout = 20 * time.Second
const runtimeOutputLimit = 64 << 10
const runtimeEnginePrefix = "MIDNIGHT_DOCKER_ENGINE "

type runtimeScope struct{ ProjectID, ServiceID, EnvironmentID, DeploymentID string }
type runtimeExec func(context.Context, string, []string) ([]byte, error)

type runtimeSSHConfig struct {
	target, port, keyFile, knownHostsFile string
	sudo                                  bool
	timeout                               time.Duration
	exec                                  runtimeExec
}

// Raw identifiers stay in memory, outside receipts and serialized journals.
type runtimeObservation struct {
	engineSHA256 string
	containerIDs []string
	running      int
	scope        runtimeScope
	positive     bool
}

func (runtimeObservation) String() string   { return "runtime observation (identifiers redacted)" }
func (runtimeObservation) GoString() string { return "runtime observation (identifiers redacted)" }
func (runtimeSSHConfig) String() string {
	return "runtime SSH configuration (private details redacted)"
}
func (runtimeSSHConfig) GoString() string {
	return "runtime SSH configuration (private details redacted)"
}

var runtimeUser = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
var runtimeHostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var runtimeContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var runtimeEngineID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{7,255}$`)

func runtimeSSHConfigFrom(c config) (runtimeSSHConfig, bool, error) {
	keys := []string{"QA_RUNTIME_SSH_TARGET", "QA_RUNTIME_SSH_PORT", "QA_RUNTIME_SSH_KEY_FILE", "QA_RUNTIME_SSH_KNOWN_HOSTS_FILE", "QA_RUNTIME_SSH_SUDO"}
	present := false
	for _, key := range keys {
		if c.values[key] != "" {
			present = true
		}
	}
	if !present {
		return runtimeSSHConfig{}, false, nil
	}
	cfg := runtimeSSHConfig{target: c.values[keys[0]], port: c.values[keys[1]], timeout: runtimeProbeTimeout}
	if cfg.port == "" {
		cfg.port = "22"
	}
	if c.values[keys[2]] == "" || c.values[keys[3]] == "" {
		return runtimeSSHConfig{}, true, blocked("RUNTIME_SSH_PRIVATE_FILES_REQUIRED")
	}
	cfg.keyFile = filepath.Clean(c.path(keys[2]))
	cfg.knownHostsFile = filepath.Clean(c.path(keys[3]))
	if raw := c.values[keys[4]]; raw != "" {
		if raw != "true" && raw != "false" {
			return runtimeSSHConfig{}, true, blocked("RUNTIME_SSH_SUDO_INVALID")
		}
		cfg.sudo = raw == "true"
	}
	if err := cfg.validate(); err != nil {
		return runtimeSSHConfig{}, true, err
	}
	if err := cfg.privateFiles(); err != nil {
		return runtimeSSHConfig{}, true, err
	}
	return cfg, true, nil
}

func (c runtimeSSHConfig) validate() error {
	parts := strings.Split(c.target, "@")
	if len(parts) != 2 || !runtimeUser.MatchString(parts[0]) || !validRuntimeHost(parts[1]) {
		return blocked("RUNTIME_SSH_TARGET_INVALID")
	}
	port, err := strconv.Atoi(c.port)
	if err != nil || port < 1 || port > 65535 || strings.Trim(c.port, "0123456789") != "" {
		return blocked("RUNTIME_SSH_PORT_INVALID")
	}
	for _, path := range []string{c.keyFile, c.knownHostsFile} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n\t\"'`$%\\;&|<>") {
			return blocked("RUNTIME_SSH_PRIVATE_PATH_INVALID")
		}
	}
	return nil
}

func validRuntimeHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00%/\\'\"`$;&|<>") {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return net.ParseIP(host[1:len(host)-1]) != nil
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if !runtimeHostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func (c runtimeSSHConfig) privateFiles() error {
	for _, path := range []string{c.keyFile, c.knownHostsFile} {
		data, err := readPrivate(path, 1<<20)
		if err != nil {
			return blocked("RUNTIME_SSH_PRIVATE_FILES_INVALID")
		}
		empty := len(bytes.TrimSpace(data)) == 0
		clear(data)
		if empty {
			return blocked("RUNTIME_SSH_PRIVATE_FILES_INVALID")
		}
	}
	return nil
}

func probePreviewRuntime(ctx context.Context, cfg runtimeSSHConfig, scope runtimeScope) (runtimeObservation, error) {
	observation, err := inspectPreviewRuntime(ctx, cfg, scope)
	if err != nil {
		return runtimeObservation{}, err
	}
	if observation.running == 0 {
		return runtimeObservation{}, failed("PREVIEW_RUNTIME_POSITIVE_CONTROL_MISSING")
	}
	observation.positive = true
	return observation, nil
}

func confirmPreviewRuntimeRemoved(ctx context.Context, cfg runtimeSSHConfig, scope runtimeScope, baseline runtimeObservation) error {
	if !baseline.positive || baseline.scope != scope || baseline.running < 1 || len(baseline.containerIDs) == 0 || !runtimeContainerID.MatchString(baseline.engineSHA256) {
		return failed("PREVIEW_RUNTIME_BASELINE_REQUIRED")
	}
	current, err := inspectPreviewRuntime(ctx, cfg, scope)
	if err != nil {
		return err
	}
	if current.engineSHA256 != baseline.engineSHA256 {
		return failed("PREVIEW_RUNTIME_ENGINE_CHANGED")
	}
	if len(current.containerIDs) != 0 {
		return failed("PREVIEW_RUNTIME_CONTAINERS_REMAIN")
	}
	return nil
}

func inspectPreviewRuntime(ctx context.Context, cfg runtimeSSHConfig, scope runtimeScope) (runtimeObservation, error) {
	if ctx.Err() != nil {
		return runtimeObservation{}, failed("RUNTIME_SSH_CANCELLED_OR_TIMED_OUT")
	}
	for _, id := range []string{scope.ProjectID, scope.ServiceID, scope.EnvironmentID, scope.DeploymentID} {
		if !identifier.MatchString(id) {
			return runtimeObservation{}, failed("PREVIEW_RUNTIME_SCOPE_INVALID")
		}
	}
	if err := cfg.validate(); err != nil {
		return runtimeObservation{}, err
	}
	if err := cfg.privateFiles(); err != nil {
		return runtimeObservation{}, err
	}
	timeout := cfg.timeout
	if timeout <= 0 || timeout > runtimeProbeTimeout {
		timeout = runtimeProbeTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	execute := cfg.exec
	if execute == nil {
		execute = executeRuntimeSSH
	}
	output, err := execute(bounded, "ssh", runtimeSSHArguments(cfg, scope))
	defer clear(output)
	if bounded.Err() != nil {
		return runtimeObservation{}, failed("RUNTIME_SSH_CANCELLED_OR_TIMED_OUT")
	}
	if err != nil {
		return runtimeObservation{}, failed("RUNTIME_SSH_PROBE_FAILED")
	}
	return parseRuntimeObservation(output, scope)
}

func runtimeSSHArguments(c runtimeSSHConfig, scope runtimeScope) []string {
	args := []string{"-F", "none", "-T", "-n", "-i", c.keyFile, "-p", c.port}
	for _, option := range []string{
		"BatchMode=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "StrictHostKeyChecking=yes",
		`UserKnownHostsFile="` + c.knownHostsFile + `"`, "GlobalKnownHostsFile=/dev/null", "KnownHostsCommand=none", "UpdateHostKeys=no", "VerifyHostKeyDNS=no",
		"PreferredAuthentications=publickey", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "GSSAPIAuthentication=no", "HostbasedAuthentication=no",
		"ProxyCommand=none", "ProxyJump=none", "CanonicalizeHostname=no", "PermitLocalCommand=no", "ClearAllForwardings=yes", "ForwardAgent=no", "ForwardX11=no",
		"ControlMaster=no", "ControlPath=none", "ConnectTimeout=10", "ConnectionAttempts=1", "ServerAliveInterval=5", "ServerAliveCountMax=1",
	} {
		args = append(args, "-o", option)
	}
	return append(args, "--", c.target, runtimeRemoteCommand(c.sudo, scope))
}

func runtimeRemoteCommand(sudo bool, scope runtimeScope) string {
	docker := "/usr/bin/docker --host=unix:///var/run/docker.sock"
	if sudo {
		docker = "sudo -n -- " + docker
	}
	command := docker + ` info --format 'MIDNIGHT_DOCKER_ENGINE {{.ID}}' && ` + docker + ` ps -a --no-trunc --format '{{.ID}} {{.State}}'`
	for _, filter := range []string{"midnight.project_id=" + scope.ProjectID, "midnight.service_id=" + scope.ServiceID, "midnight.environment_id=" + scope.EnvironmentID, "midnight.deployment_id=" + scope.DeploymentID} {
		command += " --filter 'label=" + filter + "'"
	}
	return command
}

func parseRuntimeObservation(output []byte, scope runtimeScope) (runtimeObservation, error) {
	if len(output) == 0 || len(output) > runtimeOutputLimit {
		return runtimeObservation{}, failed("RUNTIME_SSH_OUTPUT_INVALID")
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], runtimeEnginePrefix) {
		return runtimeObservation{}, failed("RUNTIME_SSH_OUTPUT_INVALID")
	}
	engine := strings.TrimPrefix(lines[0], runtimeEnginePrefix)
	if !runtimeEngineID.MatchString(engine) {
		return runtimeObservation{}, failed("RUNTIME_SSH_OUTPUT_INVALID")
	}
	hash := sha256.Sum256([]byte("midnight-runtime-engine/v1\x00" + engine))
	result := runtimeObservation{engineSHA256: hex.EncodeToString(hash[:]), scope: scope}
	seen := map[string]bool{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 || !runtimeContainerID.MatchString(fields[0]) || seen[fields[0]] {
			return runtimeObservation{}, failed("RUNTIME_SSH_OUTPUT_INVALID")
		}
		switch fields[1] {
		case "running":
			result.running++
		case "created", "paused", "restarting", "removing", "exited", "dead":
		default:
			return runtimeObservation{}, failed("RUNTIME_SSH_OUTPUT_INVALID")
		}
		seen[fields[0]] = true
		result.containerIDs = append(result.containerIDs, fields[0])
	}
	return result, nil
}

type boundedRuntimeOutput struct{ data bytes.Buffer }

func (b *boundedRuntimeOutput) Write(data []byte) (int, error) {
	if b.data.Len()+len(data) > runtimeOutputLimit {
		return 0, errors.New("runtime output limit")
	}
	return b.data.Write(data)
}
func (b *boundedRuntimeOutput) Bytes() []byte { return b.data.Bytes() }

func executeRuntimeSSH(ctx context.Context, name string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output boundedRuntimeOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	err := command.Run()
	return output.Bytes(), err
}
