package preflight

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// OSCommandRunner is an opt-in bridge from allow-listed command specs to the
// local operating system. Package defaults do not construct or use it.
type OSCommandRunner struct{}

// Run implements CommandRunner for callers that already validated a command
// through RunCommandForEvidence. Direct calls still fail closed on the allow-list.
func (OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	result, err := OSCommandRunner{}.RunCommandResult(ctx, name, args...)
	return result.Stdout, err
}

// RunCommandResult implements CommandResultRunner and refuses commands that do
// not exactly match an allowed preflight command before invoking exec.
func (OSCommandRunner) RunCommandResult(ctx context.Context, name string, args ...string) (CommandResult, error) {
	allowed, err := allowedCommandSpecForCommand(Command{Name: name, Args: append([]string(nil), args...)})
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	return runOSCommandResult(ctx, allowed.Command)
}

// RunOSCommandForEvidence executes an allowed command spec through the opt-in OS
// runner and converts stdout, stderr, exit status, timeout, and error data to
// CommandEvidence.
func RunOSCommandForEvidence(ctx context.Context, spec CommandSpec) (CommandEvidence, error) {
	return RunCommandForEvidence(ctx, spec, OSCommandRunner{})
}

func runOSCommandResult(ctx context.Context, command Command) (CommandResult, error) {
	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCodeForError(err),
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if result.TimedOut && err == nil {
		err = context.DeadlineExceeded
	}
	if err != nil && result.ExitCode == 0 {
		result.ExitCode = -1
	}
	return result, err
}
