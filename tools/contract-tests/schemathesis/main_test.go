package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestStatefulAPIContract(t *testing.T) {
	var output bytes.Buffer
	if err := Run(&output, 24, 32); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "stateful API contract: ok") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSeedClassesCoverEveryTerminalLifecycle(t *testing.T) {
	want := []orailtypes.OperationState{
		orailtypes.OperationSucceeded,
		orailtypes.OperationFailed,
		orailtypes.OperationCanceled,
	}
	for seed, state := range want {
		machine := newMachine(int64(seed))
		if err := machine.run(1); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if machine.op.State != state {
			t.Fatalf("seed %d final state = %s, want %s", seed, machine.op.State, state)
		}
	}
}

func TestStatefulAPIContractRejectsEmptyBounds(t *testing.T) {
	if err := Run(&bytes.Buffer{}, 0, 1); err == nil {
		t.Fatal("Run accepted zero seeds")
	}
}
