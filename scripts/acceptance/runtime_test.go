package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func runtimeFixture(t *testing.T) runtimeSSHConfig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SSH acceptance requires Unix private-file ownership/modes")
	}
	dir := privateTempDir(t)
	key := filepath.Join(dir, "private key")
	hosts := filepath.Join(dir, "known hosts")
	if err := os.WriteFile(key, []byte("fixture-private-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("fixture-known-hosts-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, present, err := runtimeSSHConfigFrom(config{directory: dir, values: map[string]string{
		"QA_RUNTIME_SSH_TARGET": "qa@runtime.example.invalid", "QA_RUNTIME_SSH_PORT": "2222", "QA_RUNTIME_SSH_KEY_FILE": key, "QA_RUNTIME_SSH_KNOWN_HOSTS_FILE": hosts,
	}})
	if err != nil || !present {
		t.Fatalf("private SSH configuration failed: %v", err)
	}
	return cfg
}

func runtimeFixtureScope() runtimeScope {
	return runtimeScope{ProjectID: "prj_fixture", ServiceID: "svc_fixture", EnvironmentID: "env_preview", DeploymentID: "dep_preview"}
}
func runtimeFixtureOutput(engine, state string) []byte {
	output := runtimeEnginePrefix + engine + "\n"
	if state != "" {
		output += strings.Repeat("a", 64) + " " + state + "\n"
	}
	return []byte(output)
}
func runtimeErrorCode(err error) string {
	var result *outcomeError
	if errors.As(err, &result) {
		return result.code
	}
	return "unexpected-error-type"
}

func TestRuntimeSSHCommandUsesExplicitPrivateIdentityAndExactReadOnlyScope(t *testing.T) {
	cfg := runtimeFixture(t)
	scope := runtimeFixtureScope()
	cfg.sudo = true
	cfg.timeout = time.Hour // A caller cannot extend the observer's fixed budget.
	calls := 0
	cfg.exec = func(ctx context.Context, name string, args []string) ([]byte, error) {
		calls++
		if name != "ssh" {
			t.Fatalf("inspector invoked %q instead of direct ssh", name)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > runtimeProbeTimeout || time.Until(deadline) <= 0 {
			t.Fatal("SSH observer did not receive bounded context")
		}
		joined := strings.Join(args, "\n")
		for _, secret := range []string{"fixture-private-key-material", "fixture-known-hosts-material"} {
			if strings.Contains(joined, secret) {
				t.Fatal("SSH argv contains private file content")
			}
		}
		if len(args) < 10 || args[0] != "-F" || args[1] != "none" || args[4] != "-i" || args[5] != cfg.keyFile || args[6] != "-p" || args[7] != "2222" {
			t.Fatalf("SSH identity/port/config arguments incorrect: %v", args[:8])
		}
		for _, option := range []string{"BatchMode=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "StrictHostKeyChecking=yes", `UserKnownHostsFile="` + cfg.knownHostsFile + `"`, "GlobalKnownHostsFile=/dev/null", "KnownHostsCommand=none", "ProxyCommand=none", "ProxyJump=none", "ControlMaster=no", "ControlPath=none", "ForwardAgent=no", "ClearAllForwardings=yes", "UpdateHostKeys=no", "PasswordAuthentication=no"} {
			if !strings.Contains(joined, "\n"+option+"\n") {
				t.Fatalf("missing SSH isolation option %q", option)
			}
		}
		if args[len(args)-3] != "--" || args[len(args)-2] != cfg.target {
			t.Fatal("SSH target is not an explicit destination argument")
		}
		remote := args[len(args)-1]
		if strings.Count(remote, "sudo -n -- /usr/bin/docker --host=unix:///var/run/docker.sock") != 2 || !strings.Contains(remote, " info --format ") || !strings.Contains(remote, " ps -a --no-trunc --format ") {
			t.Fatalf("remote probe is not the bounded static Docker read contract: %s", remote)
		}
		for _, filter := range []string{"midnight.project_id=" + scope.ProjectID, "midnight.service_id=" + scope.ServiceID, "midnight.environment_id=" + scope.EnvironmentID, "midnight.deployment_id=" + scope.DeploymentID} {
			if !strings.Contains(remote, "--filter 'label="+filter+"'") {
				t.Fatalf("missing exact Docker label scope %q", filter)
			}
		}
		return runtimeFixtureOutput("fixture-engine-one", "running"), nil
	}
	observation, err := probePreviewRuntime(context.Background(), cfg, scope)
	if err != nil || calls != 1 || len(observation.containerIDs) != 1 || observation.running != 1 || !observation.positive {
		t.Fatalf("positive observation failed: %v", err)
	}
	serialized, _ := json.Marshal(observation)
	for _, text := range []string{string(serialized), fmt.Sprintf("%+v", observation), fmt.Sprintf("%#v", observation), fmt.Sprintf("%+v", cfg)} {
		for _, private := range []string{"fixture-engine-one", strings.Repeat("a", 64), cfg.target, cfg.keyFile, cfg.knownHostsFile} {
			if strings.Contains(text, private) {
				t.Fatal("private runtime identifiers escaped observation redaction")
			}
		}
	}
}

func TestRuntimeSSHRejectsScopeTargetPortAndPathInjectionBeforeExecution(t *testing.T) {
	cfg := runtimeFixture(t)
	scope := runtimeFixtureScope()
	calls := 0
	cfg.exec = func(context.Context, string, []string) ([]byte, error) { calls++; return nil, nil }
	for _, value := range []string{"dep'; touch /tmp/unsafe; '", "dep_qa\nfalse", "dep_$(false)", "dep/../../other"} {
		bad := scope
		bad.DeploymentID = value
		if _, err := probePreviewRuntime(context.Background(), cfg, bad); runtimeErrorCode(err) != "PREVIEW_RUNTIME_SCOPE_INVALID" {
			t.Fatalf("scope injection accepted: %q", value)
		}
	}
	for _, target := range []string{"qa@host;false", "-oProxyCommand=evil@host", "qa@host\nfalse", "qa@host/path", "qa@host%proxy", "qa@host@other", "qa@$(false)", "qa@host -oStrictHostKeyChecking=no"} {
		bad := cfg
		bad.target = target
		if _, err := probePreviewRuntime(context.Background(), bad, scope); err == nil {
			t.Fatal("SSH target injection accepted")
		}
	}
	for _, port := range []string{"0", "65536", "22 -oProxyCommand=evil", "+22", "22\n"} {
		bad := cfg
		bad.port = port
		if _, err := probePreviewRuntime(context.Background(), bad, scope); runtimeErrorCode(err) != "RUNTIME_SSH_PORT_INVALID" {
			t.Fatalf("invalid SSH port accepted: %q", port)
		}
	}
	for _, path := range []string{"relative-key", "/private/%h/key", "/private/${HOME}/key", "/private/key\" ProxyCommand=evil", "/private/$(false)"} {
		bad := cfg
		bad.knownHostsFile = path
		if _, err := probePreviewRuntime(context.Background(), bad, scope); runtimeErrorCode(err) != "RUNTIME_SSH_PRIVATE_PATH_INVALID" {
			t.Fatal("SSH private path expansion/injection accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid SSH/scope input reached execution")
	}
}

func TestRuntimeSSHPhysicalRemovalRequiresPositiveControlSameEngineAndNoStoppedResidue(t *testing.T) {
	cfg := runtimeFixture(t)
	scope := runtimeFixtureScope()
	cfg.exec = func(context.Context, string, []string) ([]byte, error) {
		return runtimeFixtureOutput("fixture-engine-one", "running"), nil
	}
	baseline, err := probePreviewRuntime(context.Background(), cfg, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, engine, state, code string }{
		{"same-engine-empty", "fixture-engine-one", "", ""},
		{"different-engine-empty", "fixture-engine-two", "", "PREVIEW_RUNTIME_ENGINE_CHANGED"},
		{"stopped-container-remains", "fixture-engine-one", "exited", "PREVIEW_RUNTIME_CONTAINERS_REMAIN"},
		{"running-container-remains", "fixture-engine-one", "running", "PREVIEW_RUNTIME_CONTAINERS_REMAIN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg.exec = func(context.Context, string, []string) ([]byte, error) {
				return runtimeFixtureOutput(test.engine, test.state), nil
			}
			err := confirmPreviewRuntimeRemoved(context.Background(), cfg, scope, baseline)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if runtimeErrorCode(err) != test.code {
				t.Fatalf("physical teardown result=%v", err)
			}
		})
	}
	calls := 0
	cfg.exec = func(context.Context, string, []string) ([]byte, error) {
		calls++
		return runtimeFixtureOutput("fixture-engine-one", ""), nil
	}
	if _, err := probePreviewRuntime(context.Background(), cfg, scope); runtimeErrorCode(err) != "PREVIEW_RUNTIME_POSITIVE_CONTROL_MISSING" {
		t.Fatal("wrong-host empty positive control passed")
	}
	calls = 0
	if err := confirmPreviewRuntimeRemoved(context.Background(), cfg, scope, runtimeObservation{}); runtimeErrorCode(err) != "PREVIEW_RUNTIME_BASELINE_REQUIRED" {
		t.Fatal("cleanup without positive baseline passed")
	}
	other := scope
	other.DeploymentID = "dep_other"
	if err := confirmPreviewRuntimeRemoved(context.Background(), cfg, other, baseline); runtimeErrorCode(err) != "PREVIEW_RUNTIME_BASELINE_REQUIRED" {
		t.Fatal("baseline for another deployment authorized teardown proof")
	}
	if calls != 0 {
		t.Fatal("invalid baseline reached SSH")
	}
	for _, state := range []string{"exited", "created", "paused", "restarting", "dead", "removing"} {
		cfg.exec = func(context.Context, string, []string) ([]byte, error) {
			return runtimeFixtureOutput("fixture-engine-one", state), nil
		}
		if _, err := probePreviewRuntime(context.Background(), cfg, scope); err == nil {
			t.Fatal("non-running container counted as active positive control")
		}
	}
}

func TestRuntimeSSHOutputParsingErrorsAndCancellationCannotLeakOrPass(t *testing.T) {
	cfg := runtimeFixture(t)
	scope := runtimeFixtureScope()
	for _, output := range []string{"", "untrusted host banner\n", runtimeEnginePrefix + "fixture-engine-one\nshort running\n", runtimeEnginePrefix + "fixture-engine-one\n" + strings.Repeat("a", 64) + " strange-state\n", runtimeEnginePrefix + "fixture-engine-one\n" + strings.Repeat("a", 64) + " running\n" + strings.Repeat("a", 64) + " running\n", strings.Repeat("x", runtimeOutputLimit+1)} {
		cfg.exec = func(context.Context, string, []string) ([]byte, error) { return []byte(output), nil }
		if _, err := probePreviewRuntime(context.Background(), cfg, scope); runtimeErrorCode(err) != "RUNTIME_SSH_OUTPUT_INVALID" {
			t.Fatal("malformed or oversized runtime output passed")
		}
	}
	cfg.exec = func(context.Context, string, []string) ([]byte, error) {
		return []byte("private-hostname private-container private-key"), errors.New("sensitive remote stderr must never escape")
	}
	_, err := probePreviewRuntime(context.Background(), cfg, scope)
	if runtimeErrorCode(err) != "RUNTIME_SSH_PROBE_FAILED" || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("runtime error exposed private SSH output")
	}
	cfg.timeout = 20 * time.Millisecond
	cfg.exec = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return runtimeFixtureOutput("fixture-engine-one", "running"), ctx.Err()
	}
	start := time.Now()
	_, err = probePreviewRuntime(context.Background(), cfg, scope)
	if runtimeErrorCode(err) != "RUNTIME_SSH_CANCELLED_OR_TIMED_OUT" || time.Since(start) > time.Second {
		t.Fatalf("runtime cancellation was unbounded or passed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg.exec = func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("cancelled observer started execution")
		return nil, nil
	}
	if _, err := probePreviewRuntime(ctx, cfg, scope); runtimeErrorCode(err) != "RUNTIME_SSH_CANCELLED_OR_TIMED_OUT" {
		t.Fatal("cancelled observer passed")
	}
}

func TestRuntimeSSHOptionalConfigurationAndPrivateFileEnforcement(t *testing.T) {
	if _, present, err := runtimeSSHConfigFrom(config{}); err != nil || present {
		t.Fatal("unconfigured optional runtime observer appeared enabled")
	}
	cfg := runtimeFixture(t)
	if err := os.Chmod(cfg.knownHostsFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := probePreviewRuntime(context.Background(), cfg, runtimeFixtureScope()); runtimeErrorCode(err) != "RUNTIME_SSH_PRIVATE_FILES_INVALID" {
		t.Fatal("publicly readable known_hosts file accepted")
	}
}

func TestRuntimeSSHOutputWriterCannotBypassBoundThroughReaderFrom(t *testing.T) {
	var output boundedRuntimeOutput
	if _, ok := any(&output).(io.ReaderFrom); ok {
		t.Fatal("output writer accidentally exposes an unbounded ReaderFrom path")
	}
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", runtimeOutputLimit+1))); err == nil {
		t.Fatal("oversized command output was not bounded")
	}
}
