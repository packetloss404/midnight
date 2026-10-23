// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package runtime

import (
	"errors"
	"fmt"
)

// ErrReplicasUnsupported is returned when a caller asks a currently-shipping
// runtime adapter to create more than one replica from one ScheduleRequest.
// Docker and the local adapter each materialize exactly one named instance;
// accepting a larger count would report a scale-out that never happened.
var ErrReplicasUnsupported = errors.New("runtime replicas are not implemented by this adapter")

// ValidateSingleReplica enforces the v1 runtime boundary. A zero value is the
// legacy/default spelling for one replica; values above one fail before any
// container or planning resource is created.
func ValidateSingleReplica(replicas int) error {
	if replicas <= 1 {
		return nil
	}
	return fmt.Errorf("%w: requested=%d supported=1", ErrReplicasUnsupported, replicas)
}
