// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package runtime

import (
	"errors"
	"testing"
)

func TestValidateSingleReplicaFailsClosed(t *testing.T) {
	if err := ValidateSingleReplica(0); err != nil {
		t.Fatalf("zero/default replicas: %v", err)
	}
	if err := ValidateSingleReplica(1); err != nil {
		t.Fatalf("one replica: %v", err)
	}
	if err := ValidateSingleReplica(2); !errors.Is(err, ErrReplicasUnsupported) {
		t.Fatalf("two replicas error = %v, want ErrReplicasUnsupported", err)
	}
}
