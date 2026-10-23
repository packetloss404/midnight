package registry

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

func TestEmbeddedRegistryPublishesUnexpectedChildExit(t *testing.T) {
	config := writeRegistryTestConfig(t)
	process := newRegistryTestProcess()
	embedded := &EmbeddedRegistry{
		Binary:     "/usr/local/bin/registry",
		ConfigPath: config,
		Runner:     registryTestRunner{process: process},
	}
	if err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	sentinel := errors.New("registry child exited with status 17")
	process.exit(sentinel)
	select {
	case <-embedded.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not close after child exit")
	}
	if err := embedded.Wait(); !errors.Is(err, sentinel) {
		t.Fatalf("Wait() error = %v, want sentinel", err)
	}
	if err := embedded.Stop(); !errors.Is(err, sentinel) {
		t.Fatalf("Stop() error = %v, want sentinel", err)
	}
}

func TestEmbeddedRegistryStopSignalsAndReapsOnce(t *testing.T) {
	config := writeRegistryTestConfig(t)
	process := newRegistryTestProcess()
	process.exitOnSignal = true
	embedded := &EmbeddedRegistry{
		ConfigPath: config,
		Runner:     registryTestRunner{process: process},
	}
	if err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := embedded.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := process.signalCount(); got != 1 {
		t.Fatalf("Signal() calls = %d, want 1", got)
	}
	if err := embedded.Stop(); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if got := process.waitCount(); got != 1 {
		t.Fatalf("Wait() calls = %d, want 1", got)
	}
	if err := embedded.Wait(); err != nil {
		t.Fatalf("Wait() after Stop error = %v", err)
	}
}

func TestEmbeddedRegistryConcurrentStopSharesSignalAndResult(t *testing.T) {
	config := writeRegistryTestConfig(t)
	process := newRegistryTestProcess()
	process.exitOnSignal = true
	sentinel := errors.New("registry shutdown failed")
	process.signalExitErr = sentinel
	embedded := &EmbeddedRegistry{
		ConfigPath: config,
		Runner:     registryTestRunner{process: process},
	}
	if err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	const callers = 32
	results := make(chan error, callers)
	start := make(chan struct{})
	var calls sync.WaitGroup
	for range callers {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			results <- embedded.Stop()
		}()
	}
	close(start)
	calls.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, sentinel) {
			t.Fatalf("Stop() error = %v, want shared sentinel", err)
		}
	}
	if got := process.signalCount(); got != 1 {
		t.Fatalf("Signal() calls = %d, want 1", got)
	}
	if got := process.waitCount(); got != 1 {
		t.Fatalf("Wait() calls = %d, want 1", got)
	}
	if err := embedded.Stop(); !errors.Is(err, sentinel) {
		t.Fatalf("repeated Stop() error = %v, want stored sentinel", err)
	}
	if err := embedded.Wait(); !errors.Is(err, sentinel) {
		t.Fatalf("Wait() after Stop error = %v, want raw sentinel", err)
	}
}

func writeRegistryTestConfig(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/registry.yml"
	if err := os.WriteFile(path, []byte("version: 0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type registryTestRunner struct {
	process *registryTestProcess
}

func (r registryTestRunner) Start(context.Context, string, []string, io.Writer, io.Writer) (Process, error) {
	return r.process, nil
}

type registryTestProcess struct {
	mu            sync.Mutex
	waitCh        chan error
	waits         int
	signals       int
	exitOnSignal  bool
	signalExitErr error
	exitOnce      sync.Once
}

func newRegistryTestProcess() *registryTestProcess {
	return &registryTestProcess{waitCh: make(chan error, 1)}
}

func (p *registryTestProcess) Wait() error {
	p.mu.Lock()
	p.waits++
	p.mu.Unlock()
	return <-p.waitCh
}

func (p *registryTestProcess) Signal(os.Signal) error {
	p.mu.Lock()
	p.signals++
	exitOnSignal := p.exitOnSignal
	p.mu.Unlock()
	if exitOnSignal {
		p.exit(p.signalExitErr)
	}
	return nil
}

func (p *registryTestProcess) exit(err error) {
	p.exitOnce.Do(func() { p.waitCh <- err })
}

func (p *registryTestProcess) signalCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.signals
}

func (p *registryTestProcess) waitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waits
}
