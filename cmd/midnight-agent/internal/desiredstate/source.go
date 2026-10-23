// Package desiredstate defines how the agent receives desired runtime state.
package desiredstate

import "context"

// State is the opaque desired-state document returned by the control plane.
// A later slice will replace this with concrete deployment fields once the
// control-plane contract is finalized.
type State struct {
	ETag string
	Raw  []byte
}

// Source fetches the latest desired state for an agent.
type Source interface {
	Fetch(context.Context, string) (State, error)
}

// RESTPollingSource is a placeholder for the future REST polling implementation.
// It intentionally performs no network calls in Phase 0; Fetch returns ErrNotImplemented
// until the control-plane route, authentication, and backoff policy are defined.
type RESTPollingSource struct {
	BaseURL string
}

// ErrNotImplemented reports that REST polling is not wired yet.
type ErrNotImplemented struct{}

func (ErrNotImplemented) Error() string {
	return "desired-state REST polling is not implemented"
}

// Fetch satisfies Source without making a network request.
func (s RESTPollingSource) Fetch(context.Context, string) (State, error) {
	return State{}, ErrNotImplemented{}
}
