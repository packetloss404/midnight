package timeline

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// MemoryStore keeps deployment timeline events in process for Phase 0 control-plane tests.
type MemoryStore struct {
	mu      sync.Mutex
	events  map[string][]Event
	lastSeq map[string]int64
}

// NewMemoryStore returns an empty in-memory deployment timeline store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:  make(map[string][]Event),
		lastSeq: make(map[string]int64),
	}
}

// Append records event under its DeploymentID. The event sequence must be
// strictly greater than the previous event's sequence for the same deployment.
func (store *MemoryStore) Append(event Event) error {
	if store == nil {
		return errors.New("timeline store is nil")
	}
	if event.DeploymentID == "" {
		return errors.New("deployment ID is required")
	}
	if event.EventSeq <= 0 {
		return errors.New("event_seq must be greater than zero")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if last, ok := store.lastSeq[event.DeploymentID]; ok && event.EventSeq <= last {
		return fmt.Errorf("event_seq %d must be greater than last event_seq %d for deployment %q", event.EventSeq, last, event.DeploymentID)
	}
	store.events[event.DeploymentID] = append(store.events[event.DeploymentID], event)
	store.lastSeq[event.DeploymentID] = event.EventSeq
	return nil
}

// List returns the in-order events recorded for the given deployment ID. The
// returned slice is a defensive copy.
func (store *MemoryStore) List(deploymentID string) []Event {
	if store == nil || deploymentID == "" {
		return nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	events := store.events[deploymentID]
	return append([]Event(nil), events...)
}

// DeploymentIDs returns the recorded deployment IDs sorted ascending.
func (store *MemoryStore) DeploymentIDs() []string {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]string, 0, len(store.events))
	for id := range store.events {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Query returns one page of timeline events that satisfy filter. Pagination
// is cursor-based across deployments and event types: when cursor is
// non-zero, scanning resumes at the named DeploymentID, skipping every
// event with EventSeq <= AfterSeq, and continues across subsequent
// deployments in lexical order. pageSize caps the number of returned
// events; when pageSize <= 0 it defaults to 100.
func (store *MemoryStore) Query(filter Filter, cursor QueryCursor, pageSize int) (QueryResult, error) {
	if store == nil {
		return QueryResult{}, errors.New("timeline store is nil")
	}
	if err := filter.Validate(); err != nil {
		return QueryResult{}, err
	}
	if pageSize <= 0 {
		pageSize = 100
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	scope := filter.effectiveDeployments()
	deploymentIDs := make([]string, 0, len(store.events))
	if len(scope) > 0 {
		// Honor explicit deployment scoping; only include deployments that
		// actually have events recorded.
		seen := make(map[string]struct{}, len(scope))
		for _, id := range scope {
			if _, dup := seen[id]; dup {
				continue
			}
			if _, ok := store.events[id]; ok {
				seen[id] = struct{}{}
				deploymentIDs = append(deploymentIDs, id)
			}
		}
	} else {
		for id := range store.events {
			deploymentIDs = append(deploymentIDs, id)
		}
	}
	sort.Strings(deploymentIDs)

	result := QueryResult{Events: make([]Event, 0, pageSize)}
	for _, id := range deploymentIDs {
		events := store.events[id]
		afterSeq := int64(0)
		if cursor.DeploymentID == id {
			afterSeq = cursor.AfterSeq
		} else if cursor.DeploymentID != "" && id < cursor.DeploymentID {
			// When cursor names a specific deployment, skip everything before it.
			continue
		}
		for _, event := range events {
			if event.EventSeq <= afterSeq {
				continue
			}
			if !filter.Match(event) {
				continue
			}
			result.Events = append(result.Events, event)
			if len(result.Events) >= pageSize {
				result.NextCursor = QueryCursor{DeploymentID: id, AfterSeq: event.EventSeq}
				result.Truncated = true
				return result, nil
			}
		}
	}
	return result, nil
}
