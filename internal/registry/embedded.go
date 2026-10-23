package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// EmbeddedRegistry runs github.com/distribution/distribution in fork-exec
// mode. We do not import the distribution Go packages; the import graph
// drags in the entire OCI image-spec/azure/gcs/swift storage drivers, and
// Phase 1 only needs the filesystem driver.
//
// The wrapper holds the filesystem driver root, the listen address, the
// optional read-only mode flag (used during GC apply to ensure no writes
// race the sweep), and the subprocess handle. The alpha package installs the
// Midnight wrapper; operators provide the pinned upstream `registry` binary
// path through package env.
type EmbeddedRegistry struct {
	// Binary is the path to the pinned `registry` binary. Empty means
	// "registry on PATH".
	Binary string
	// ConfigPath is the absolute path to a YAML config file the registry
	// reads on start. Lane 3 ships a sample config under deploy/registry/.
	ConfigPath string
	// Logger is an optional writer that mirrors stdout/stderr from the
	// subprocess. nil silences the subprocess.
	Logger io.Writer
	// Runner is the seam tests use to substitute fake subprocesses. nil =
	// real os/exec.
	Runner SubprocessRunner

	mu       sync.Mutex
	process  Process
	done     chan struct{}
	waitErr  error
	stopDone chan struct{}
	stopErr  error
}

// SubprocessRunner is the seam tests use to substitute fake registry
// processes. Production code uses execSubprocessRunner.
type SubprocessRunner interface {
	Start(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (Process, error)
}

// Process is the subset of *exec.Cmd we use after Start.
type Process interface {
	Wait() error
	Signal(os.Signal) error
}

type execSubprocessRunner struct{}

func (execSubprocessRunner) Start(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The wrapper owns graceful cancellation and signals the child from Stop.
	// CommandContext would race that path by killing the child as soon as the
	// wrapper's signal context is canceled.
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd}, nil
}

type execProcess struct{ cmd *exec.Cmd }

func (p execProcess) Wait() error                { return p.cmd.Wait() }
func (p execProcess) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// Validate checks that the supplied config path exists and is absolute. It
// does not parse the YAML; that is the registry binary's responsibility.
func (e *EmbeddedRegistry) Validate() error {
	if e == nil {
		return errors.New("embedded registry is nil")
	}
	if strings.TrimSpace(e.ConfigPath) == "" {
		return errors.New("embedded registry config path is required")
	}
	if !filepath.IsAbs(e.ConfigPath) {
		return errors.New("embedded registry config path must be absolute")
	}
	if _, err := os.Stat(e.ConfigPath); err != nil {
		return fmt.Errorf("embedded registry config: %w", err)
	}
	return nil
}

// Start spawns the registry subprocess and starts a single waiter. Returns
// immediately once the subprocess has started; callers select on Done and use
// Wait to obtain the child's exit status.
func (e *EmbeddedRegistry) Start(ctx context.Context) error {
	if err := e.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done != nil {
		return errors.New("embedded registry already started")
	}
	runner := e.runner()
	logger := e.Logger
	if logger == nil {
		logger = io.Discard
	}
	proc, err := runner.Start(ctx, e.binary(), []string{"serve", e.ConfigPath}, logger, logger)
	if err != nil {
		return fmt.Errorf("embedded registry start: %w", err)
	}
	e.process = proc
	e.done = make(chan struct{})
	e.waitErr = nil
	e.stopDone = nil
	e.stopErr = nil
	done := e.done
	go func() {
		err := proc.Wait()
		e.mu.Lock()
		e.waitErr = err
		e.process = nil
		close(done)
		e.mu.Unlock()
	}()
	return nil
}

// Done closes when the registry child exits. It is nil before the first
// successful Start, matching the usual select semantics for an unavailable
// event source.
func (e *EmbeddedRegistry) Done() <-chan struct{} {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done
}

// Wait waits for the started registry child and returns its raw exit status. It
// is safe to call more than once after Start, including after Stop. Stop treats
// the interrupt exit it initiated as success; Wait deliberately continues to
// report that raw signal status so child-exit observers retain complete truth.
func (e *EmbeddedRegistry) Wait() error {
	if e == nil {
		return errors.New("embedded registry is nil")
	}
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()
	if done == nil {
		return errors.New("embedded registry not started")
	}
	<-done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waitErr
}

// Stop sends the platform interrupt signal to the registry subprocess and
// waits for exit. Concurrent and repeated calls share one signal attempt and
// return the same stored result.
func (e *EmbeddedRegistry) Stop() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	done := e.done
	stopDone := e.stopDone
	initiator := false
	var proc Process
	if done != nil && stopDone == nil {
		stopDone = make(chan struct{})
		e.stopDone = stopDone
		proc = e.process
		initiator = true
	}
	e.mu.Unlock()
	if done == nil {
		return nil
	}
	if !initiator {
		<-stopDone
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.stopErr
	}

	var result error
	signaled := false
	if proc != nil {
		if err := proc.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			result = fmt.Errorf("signal embedded registry: %w", err)
		} else if err == nil {
			signaled = true
		}
	}
	if result == nil {
		<-done
		e.mu.Lock()
		result = e.waitErr
		e.mu.Unlock()
		if signaled && isSignalExit(result) {
			result = nil
		}
	}

	e.mu.Lock()
	e.stopErr = result
	close(stopDone)
	e.mu.Unlock()
	return result
}

func (e *EmbeddedRegistry) binary() string {
	if strings.TrimSpace(e.Binary) != "" {
		return e.Binary
	}
	return "registry"
}

func (e *EmbeddedRegistry) runner() SubprocessRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return execSubprocessRunner{}
}

func isSignalExit(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Process killed by signal returns ExitCode -1 on most platforms.
		if exitErr.ExitCode() == -1 {
			return true
		}
	}
	return false
}
