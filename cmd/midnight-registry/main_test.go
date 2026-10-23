package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestVerifyRegistryVersionAcceptsPin(t *testing.T) {
	oldExec := execCommandOutput
	defer func() { execCommandOutput = oldExec }()
	execCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/opt/registry" {
			t.Fatalf("name = %q, want /opt/registry", name)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("args = %#v, want --version", args)
		}
		return []byte("registry github.com/distribution/distribution/v3 v3.0.0 deadbeef\n"), nil
	}

	if err := verifyRegistryVersion(context.Background(), "/opt/registry"); err != nil {
		t.Fatalf("verifyRegistryVersion() error = %v", err)
	}
}

func TestVerifyRegistryVersionRejectsMismatch(t *testing.T) {
	oldExec := execCommandOutput
	defer func() { execCommandOutput = oldExec }()
	execCommandOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("registry github.com/distribution/distribution/v3 v9.9.9 deadbeef\n"), nil
	}

	if err := verifyRegistryVersion(context.Background(), "/opt/registry"); err == nil {
		t.Fatal("verifyRegistryVersion() error = nil, want mismatch")
	}
}

func TestRunServeReturnsWhenRegistryChildExits(t *testing.T) {
	oldExec := execCommandOutput
	oldNew := newRegistryProcess
	t.Cleanup(func() {
		execCommandOutput = oldExec
		newRegistryProcess = oldNew
	})
	execCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("registry github.com/distribution/distribution/v3 v3.0.0 deadbeef\n"), nil
	}
	sentinel := errors.New("child-exit-sentinel")
	process := &registryProcessTestDouble{done: make(chan struct{}), waitErr: sentinel}
	close(process.done)
	newRegistryProcess = func(string, string) registryProcess { return process }

	err := runServe(context.Background(), []string{"--registry", "/opt/registry", "--config", "/etc/midnight/registry.yml"})
	if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("runServe() error = %v, want child exit", err)
	}
	if process.started != 1 || process.stopped != 0 || process.waited != 1 {
		t.Fatalf("process calls start/stop/wait = %d/%d/%d", process.started, process.stopped, process.waited)
	}
}

type registryProcessTestDouble struct {
	done    chan struct{}
	waitErr error
	started int
	stopped int
	waited  int
}

func (p *registryProcessTestDouble) Start(context.Context) error {
	p.started++
	return nil
}

func (p *registryProcessTestDouble) Stop() error {
	p.stopped++
	return nil
}

func (p *registryProcessTestDouble) Done() <-chan struct{} { return p.done }

func (p *registryProcessTestDouble) Wait() error {
	p.waited++
	return p.waitErr
}
