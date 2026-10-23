package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAllowedCommandSpecs(t *testing.T) {
	t.Parallel()

	specs := AllowedCommandSpecs()
	if len(specs) != 2 {
		t.Fatalf("len(AllowedCommandSpecs()) = %d, want 2", len(specs))
	}

	tests := []struct {
		name string
		spec CommandSpec
		want string
		args []string
	}{
		{name: "cgroup", spec: CgroupFSTypeCommandSpec(), want: "stat", args: []string{"-fc", "%T", "/sys/fs/cgroup"}},
		{name: "docker", spec: DockerAPICommandSpec(), want: "docker", args: []string{"version", "--format", "{{.Server.APIVersion}}"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.spec.Command.Name != tt.want {
				t.Fatalf("Command.Name = %q, want %q", tt.spec.Command.Name, tt.want)
			}
			if !equalStrings(tt.spec.Command.Args, tt.args) {
				t.Fatalf("Command.Args = %v, want %v", tt.spec.Command.Args, tt.args)
			}
			if tt.spec.Command.Timeout <= 0 {
				t.Fatalf("Command.Timeout = %s, want positive", tt.spec.Command.Timeout)
			}
			if tt.spec.Command.RequiresRoot {
				t.Fatal("Command.RequiresRoot = true, want false")
			}
			if err := ValidateAllowedCommand(tt.spec.Command); err != nil {
				t.Fatalf("ValidateAllowedCommand() error = %v", err)
			}
		})
	}
}

func TestCommandExecutionPlanIsJSONSafe(t *testing.T) {
	t.Parallel()

	plan := CommandExecutionPlan()
	if len(plan) != 2 {
		t.Fatalf("len(CommandExecutionPlan()) = %d, want 2", len(plan))
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan) error = %v", err)
	}
	var decoded []CommandPlanEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(plan) error = %v", err)
	}
	if decoded[0].Executable != "stat" || decoded[0].Timeout == "" || decoded[0].RequiresRoot {
		t.Fatalf("decoded[0] = %+v, want stat with timeout and requires_root=false", decoded[0])
	}
}

func TestValidateAllowedCommandRejectsChangedExecutableOrArgs(t *testing.T) {
	t.Parallel()

	tests := []Command{
		{Name: "sudo", Args: []string{"docker", "version", "--format", "{{.Server.APIVersion}}"}},
		{Name: "docker", Args: []string{"version"}},
		{Name: "stat", Args: []string{"-fc", "%T", "/"}},
		{ID: CommandSpecDockerAPI, Name: "stat", Args: []string{"-fc", "%T", "/sys/fs/cgroup"}},
	}
	for _, command := range tests {
		command := command
		t.Run(command.Name, func(t *testing.T) {
			t.Parallel()

			if err := ValidateAllowedCommand(command); err == nil {
				t.Fatalf("ValidateAllowedCommand(%+v) error = nil, want error", command)
			}
		})
	}
}

func TestRunCommandForEvidenceUsesFakeRunner(t *testing.T) {
	t.Parallel()

	evidence, err := RunCommandForEvidence(context.Background(), DockerAPICommandSpec(), CommandRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" || !equalStrings(args, []string{"version", "--format", "{{.Server.APIVersion}}"}) {
			t.Fatalf("runner got %q %v", name, args)
		}
		return []byte("1.44\n"), nil
	}))
	if err != nil {
		t.Fatalf("RunCommandForEvidence() error = %v", err)
	}
	if evidence.Schema != CommandEvidenceSchemaV1 || evidence.Stdout != "1.44\n" || evidence.Error != "" {
		t.Fatalf("evidence = %+v, want schema/stdout without error", evidence)
	}
	data, err := MarshalCommandEvidenceJSON(evidence)
	if err != nil {
		t.Fatalf("MarshalCommandEvidenceJSON() error = %v", err)
	}
	var decoded CommandEvidence
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(evidence) error = %v", err)
	}
	if decoded.Executable != "docker" || decoded.Timeout == "" || decoded.RequiresRoot {
		t.Fatalf("decoded evidence = %+v, want executable, timeout, requires_root=false", decoded)
	}
}

func TestRunCommandForEvidenceCapturesRunnerError(t *testing.T) {
	t.Parallel()

	runErr := errors.New("docker unavailable")
	evidence, err := RunCommandForEvidence(context.Background(), DockerAPICommandSpec(), CommandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(""), runErr
	}))
	if err != nil {
		t.Fatalf("RunCommandForEvidence() error = %v", err)
	}
	if evidence.Error != runErr.Error() {
		t.Fatalf("Evidence.Error = %q, want %q", evidence.Error, runErr.Error())
	}
}

func TestRunCommandForEvidenceCapturesResultRunnerFailure(t *testing.T) {
	t.Parallel()

	runErr := errors.New("docker failed token=abc123")
	evidence, err := RunCommandForEvidence(context.Background(), DockerAPICommandSpec(), &fakeEvidenceRunner{
		result: CommandResult{
			Stdout:   []byte("token=stdout-secret\n1.44\n"),
			Stderr:   []byte("Authorization: Bearer stderr-secret\n"),
			ExitCode: 17,
		},
		err: runErr,
	})
	if err != nil {
		t.Fatalf("RunCommandForEvidence() error = %v", err)
	}
	if evidence.ExitCode != 17 {
		t.Fatalf("ExitCode = %d, want 17", evidence.ExitCode)
	}
	if evidence.Stdout != "token=<redacted>\n1.44\n" {
		t.Fatalf("Stdout = %q, want redacted token", evidence.Stdout)
	}
	if evidence.Stderr != "Authorization: Bearer <redacted>\n" {
		t.Fatalf("Stderr = %q, want redacted bearer token", evidence.Stderr)
	}
	if evidence.Error != "docker failed token=<redacted>" {
		t.Fatalf("Error = %q, want redacted error", evidence.Error)
	}
}

func TestReviewCommandEvidenceRedactionClean(t *testing.T) {
	t.Parallel()

	review := ReviewCommandEvidenceRedaction([]CommandEvidence{{SpecID: CommandSpecDockerAPI, Stdout: "1.44\n"}})
	if review.Status != CommandEvidenceRedactionClean || len(review.Findings) != 0 {
		t.Fatalf("review = %+v, want clean with no findings", review)
	}
}

func TestReviewCommandEvidenceRedactionNeedsReviewForRedactedFields(t *testing.T) {
	t.Parallel()

	review := ReviewCommandEvidenceRedaction([]CommandEvidence{{SpecID: CommandSpecDockerAPI, Stderr: "Authorization: Bearer <redacted>\n", Error: "token=<redacted>"}})
	if review.Status != CommandEvidenceRedactionNeedsReview {
		t.Fatalf("Status = %q, want needs-review", review.Status)
	}
	if len(review.Findings) != 2 {
		t.Fatalf("Findings = %+v, want stderr and error findings", review.Findings)
	}
	for _, finding := range review.Findings {
		if finding.Status != CommandEvidenceRedactionNeedsReview {
			t.Fatalf("finding = %+v, want needs-review", finding)
		}
	}
}

func TestReviewCommandEvidenceRedactionBlocksObviousSecrets(t *testing.T) {
	t.Parallel()

	review := ReviewCommandEvidenceRedaction([]CommandEvidence{{
		SpecID: CommandSpecDockerAPI,
		Stdout: "api_key=abc123\n",
		Stderr: "Authorization: Bearer still-secret\n",
		Error:  "-----BEGIN PRIVATE KEY-----",
	}})
	if review.Status != CommandEvidenceRedactionBlocked {
		t.Fatalf("Status = %q, want blocked", review.Status)
	}
	if len(review.Findings) != 3 {
		t.Fatalf("Findings = %+v, want one blocked finding per populated field", review.Findings)
	}
	for _, finding := range review.Findings {
		if finding.Status != CommandEvidenceRedactionBlocked {
			t.Fatalf("finding = %+v, want blocked", finding)
		}
	}
}

func TestRunCommandForEvidenceCapturesTimeoutFromContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()

	evidence, err := RunCommandForEvidence(ctx, CgroupFSTypeCommandSpec(), &fakeEvidenceRunner{
		waitForContext: true,
		result:         CommandResult{Stderr: []byte("timed out waiting for stat\n")},
	})
	if err != nil {
		t.Fatalf("RunCommandForEvidence() error = %v", err)
	}
	if !evidence.TimedOut || evidence.ExitCode != -1 || evidence.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("evidence timeout fields = %+v, want timed_out=true exit_code=-1 deadline error", evidence)
	}
}

func TestRunCommandForEvidenceRejectsSpecMismatchBeforeRunner(t *testing.T) {
	t.Parallel()

	runner := &fakeEvidenceRunner{result: CommandResult{Stdout: []byte("1.44\n")}}
	spec := DockerAPICommandSpec()
	spec.Command.Args = []string{"version"}
	evidence, err := RunCommandForEvidence(context.Background(), spec, runner)
	if err == nil {
		t.Fatalf("RunCommandForEvidence() error = nil, want allow-list error with evidence %+v", evidence)
	}
	if runner.called {
		t.Fatal("runner was called for rejected command spec")
	}
}

func TestRunOSCommandForEvidenceRejectsDisallowedSpecBeforeExec(t *testing.T) {
	t.Parallel()

	spec := DockerAPICommandSpec()
	spec.Command.Name = "midnight-command-that-must-not-execute"
	spec.Command.Args = nil

	evidence, err := RunOSCommandForEvidence(context.Background(), spec)
	if err == nil {
		t.Fatalf("RunOSCommandForEvidence() error = nil, want allow-list error with evidence %+v", evidence)
	}
	if evidence.Schema != "" {
		t.Fatalf("RunOSCommandForEvidence() evidence = %+v, want empty evidence", evidence)
	}
}

func TestOSCommandRunnerRejectsDisallowedCommandBeforeExec(t *testing.T) {
	t.Parallel()

	result, err := OSCommandRunner{}.RunCommandResult(context.Background(), "midnight-command-that-must-not-execute")
	if err == nil {
		t.Fatalf("OSCommandRunner.RunCommandResult() error = nil, want allow-list error with result %+v", result)
	}
	if result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("OSCommandRunner.RunCommandResult() result = %+v, want empty failed result", result)
	}
}

func TestMarshalCommandEvidenceJSONIsDeterministic(t *testing.T) {
	t.Parallel()

	evidence, err := RunCommandForEvidence(context.Background(), CgroupFSTypeCommandSpec(), &fakeEvidenceRunner{result: CommandResult{Stdout: []byte("cgroup2fs\n")}})
	if err != nil {
		t.Fatalf("RunCommandForEvidence() error = %v", err)
	}
	data, err := MarshalCommandEvidenceJSON(evidence)
	if err != nil {
		t.Fatalf("MarshalCommandEvidenceJSON() error = %v", err)
	}
	const want = "{\n  \"schema\": \"midnight.preflight.command_evidence.v1\",\n  \"spec_id\": \"preflight.cgroup_fs_type\",\n  \"check_id\": \"PRE-CGROUP-001\",\n  \"executable\": \"stat\",\n  \"args\": [\n    \"-fc\",\n    \"%T\",\n    \"/sys/fs/cgroup\"\n  ],\n  \"timeout\": \"3s\",\n  \"requires_root\": false,\n  \"exit_code\": 0,\n  \"stdout\": \"cgroup2fs\\n\"\n}"
	if string(data) != want {
		t.Fatalf("MarshalCommandEvidenceJSON() = %s, want %s", data, want)
	}
}

func TestCommandBackedCheckRejectsUnexpectedCommandBeforeRunner(t *testing.T) {
	t.Parallel()

	called := false
	result := CheckDockerAPI(context.Background(), HostSource{
		Runner: CommandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return []byte("1.44\n"), nil
		}),
		Commands: HostCommands{DockerAPIVersion: Command{Name: "docker", Args: []string{"version"}}},
	})
	if called {
		t.Fatal("runner was called for rejected command")
	}
	if result.Status != StatusFail || result.Err == nil {
		t.Fatalf("result = %+v, want fail with validation error", result)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type fakeEvidenceRunner struct {
	result         CommandResult
	err            error
	waitForContext bool
	called         bool
}

func (r *fakeEvidenceRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	result, err := r.RunCommandResult(ctx, name, args...)
	return result.Stdout, err
}

func (r *fakeEvidenceRunner) RunCommandResult(ctx context.Context, _ string, _ ...string) (CommandResult, error) {
	r.called = true
	if r.waitForContext {
		<-ctx.Done()
		result := r.result
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		return result, ctx.Err()
	}
	return r.result, r.err
}
