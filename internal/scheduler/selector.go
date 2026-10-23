package scheduler

import (
	"errors"
	"strings"
)

// SelectorEnv is the environment variable name the server and worker
// command binaries read to pick the deployment-execution scheduler.
//   - "local" (default) wires LocalDockerScheduler — drives the local
//     Docker daemon, identical to today's behavior.
//   - "multihost" wires MultiHostScheduler — selects a host from the
//     hosts table and dispatches via the existing agent endpoints.
const SelectorEnv = "MIDNIGHT_SCHEDULER"

// Selector identifies which Scheduler implementation to wire.
type Selector string

const (
	// SelectorLocal is the default. Wires internal/scheduler/local.
	SelectorLocal Selector = "local"
	// SelectorMultiHost wires internal/scheduler/multihost. Stub today;
	// fillable in without re-plumbing per ADR-0018.
	SelectorMultiHost Selector = "multihost"
)

// ErrUnknownSelector is returned by ParseSelector for unrecognized values.
var ErrUnknownSelector = errors.New("scheduler: unknown selector")

// ParseSelector normalizes the env var input. An empty input defaults to
// SelectorLocal; an unknown value returns ErrUnknownSelector so callers can
// fail-fast at startup instead of silently downgrading to local.
func ParseSelector(value string) (Selector, error) {
	s := strings.ToLower(strings.TrimSpace(value))
	switch s {
	case "", string(SelectorLocal):
		return SelectorLocal, nil
	case string(SelectorMultiHost):
		return SelectorMultiHost, nil
	default:
		return "", ErrUnknownSelector
	}
}
