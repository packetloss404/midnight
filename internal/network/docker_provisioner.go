package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

// DockerNetworkClient is the narrow Docker SDK surface DockerProvisioner
// uses. The runtime adapter's EngineClient already satisfies this surface
// once it gains the network methods; tests inject an in-memory fake.
//
// Keeping this interface separate from internal/runtime/docker.EngineClient
// avoids an import cycle: internal/runtime/docker uses this package, and
// this package can't reach back into internal/runtime/docker.
type DockerNetworkClient interface {
	// NetworkCreate creates a user-defined bridge with the given options.
	// Implementations MUST return the engine-assigned network id; the name
	// is set by the caller via CreateOptions.
	NetworkCreate(ctx context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error)
	// NetworkRemove removes a network by id or name.
	NetworkRemove(ctx context.Context, networkID string) error
	// NetworkList returns the Midnight-managed networks matching the
	// given label filter. The boot reconciliation pass uses this to
	// rediscover networks without consulting the control plane.
	NetworkList(ctx context.Context, labelFilter map[string]string) ([]network.Summary, error)
}

// DockerProvisioner is the production Provisioner backed by the Docker
// daemon. It is safe for concurrent use; the internal cache keys are
// (project_id, environment_id) pairs so per-tenant convergence is
// serialized without blocking other tenants.
type DockerProvisioner struct {
	client DockerNetworkClient

	mu    sync.Mutex
	cache map[string]NetworkID
}

// NewDockerProvisioner constructs a DockerProvisioner. Returns an error if
// client is nil so misconfigured callers fail at construction rather than
// on first EnsureNetwork.
func NewDockerProvisioner(client DockerNetworkClient) (*DockerProvisioner, error) {
	if client == nil {
		return nil, errors.New("network: docker provisioner requires a client")
	}
	return &DockerProvisioner{
		client: client,
		cache:  make(map[string]NetworkID),
	}, nil
}

// EnsureNetwork creates the per-(project, environment) Docker bridge if
// it does not exist and returns the network id. The call is idempotent:
// repeated invocations return the cached id without contacting the daemon.
//
// The implementation tolerates concurrent first-creates because Docker's
// NetworkCreate returns an "already exists" error that we re-inspect via
// NetworkList. The list/inspect path is the same one the boot reconciler
// uses, keeping the convergence logic in one place.
func (p *DockerProvisioner) EnsureNetwork(ctx context.Context, projectID, environmentID string) (NetworkID, error) {
	if err := validateScope(projectID, environmentID); err != nil {
		return "", err
	}
	pn := ProjectNetwork{ProjectID: projectID, EnvironmentID: environmentID}
	key := scopeKey(projectID, environmentID)

	p.mu.Lock()
	if id, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return id, nil
	}
	p.mu.Unlock()

	// Check the daemon for an existing network matching this scope's
	// labels. This catches the post-restart case where the cache was lost
	// but the network still exists on the host.
	existing, err := p.client.NetworkList(ctx, pn.Labels())
	if err != nil {
		return "", fmt.Errorf("network: list managed networks: %w", err)
	}
	for _, n := range existing {
		if matchesScope(n, projectID, environmentID) {
			id := NetworkID(n.ID)
			p.mu.Lock()
			p.cache[key] = id
			p.mu.Unlock()
			return id, nil
		}
	}

	id, err := p.client.NetworkCreate(ctx, pn.Name(), dockerclient.NetworkCreateOptions{
		Driver: "bridge",
		Scope:  "local",
		Labels: pn.Labels(),
		// Attachable lets the daemon connect containers via NetworkConnect
		// even when they were created before the network existed; the
		// runtime adapter wires the network at create time today, but the
		// option is cheap and keeps the door open for future hot-attach.
		Attachable: true,
	})
	if err != nil {
		// Race: another process may have created the network between our
		// List() and Create(). Re-list and pick up the id.
		if isAlreadyExists(err) {
			existing, listErr := p.client.NetworkList(ctx, pn.Labels())
			if listErr == nil {
				for _, n := range existing {
					if matchesScope(n, projectID, environmentID) {
						nid := NetworkID(n.ID)
						p.mu.Lock()
						p.cache[key] = nid
						p.mu.Unlock()
						return nid, nil
					}
				}
			}
		}
		return "", fmt.Errorf("network: create %s: %w", pn.Name(), err)
	}
	nid := NetworkID(id)
	p.mu.Lock()
	p.cache[key] = nid
	p.mu.Unlock()
	return nid, nil
}

// RemoveNetwork deletes the per-(project, environment) network if it
// exists. The current product surface never calls RemoveNetwork — the
// hook exists for project/environment deletion which is not wired
// through the API yet. The implementation is best-effort: a NotFound
// from the daemon is collapsed to nil so the hook is idempotent.
func (p *DockerProvisioner) RemoveNetwork(ctx context.Context, projectID, environmentID string) error {
	if err := validateScope(projectID, environmentID); err != nil {
		return err
	}
	pn := ProjectNetwork{ProjectID: projectID, EnvironmentID: environmentID}
	existing, err := p.client.NetworkList(ctx, pn.Labels())
	if err != nil {
		return fmt.Errorf("network: list managed networks: %w", err)
	}
	for _, n := range existing {
		if !matchesScope(n, projectID, environmentID) {
			continue
		}
		if err := p.client.NetworkRemove(ctx, n.ID); err != nil && !isNotFound(err) {
			return fmt.Errorf("network: remove %s: %w", n.ID, err)
		}
	}
	p.mu.Lock()
	delete(p.cache, scopeKey(projectID, environmentID))
	p.mu.Unlock()
	return nil
}

// ListManagedNetworks returns every Midnight-managed network on the host.
// The worker boot pass calls this to seed the in-memory cache so the
// first EnsureNetwork after restart does not re-create existing networks.
func (p *DockerProvisioner) ListManagedNetworks(ctx context.Context) ([]ProjectNetwork, error) {
	summaries, err := p.client.NetworkList(ctx, map[string]string{LabelManagedBy: LabelManagedByV})
	if err != nil {
		return nil, fmt.Errorf("network: list managed networks: %w", err)
	}
	out := make([]ProjectNetwork, 0, len(summaries))
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range summaries {
		projectID := n.Labels[LabelProjectID]
		environmentID := n.Labels[LabelEnvironmentID]
		if projectID == "" || environmentID == "" {
			continue
		}
		pn := ProjectNetwork{ProjectID: projectID, EnvironmentID: environmentID}
		out = append(out, pn)
		p.cache[scopeKey(projectID, environmentID)] = NetworkID(n.ID)
	}
	return out, nil
}

// scopeKey returns the cache key for a (project_id, environment_id) pair.
// The pipe separator cannot appear in either id (they are ULIDs in the
// production schema), so the key is collision-free.
func scopeKey(projectID, environmentID string) string {
	return projectID + "|" + environmentID
}

func matchesScope(n network.Summary, projectID, environmentID string) bool {
	if n.Labels[LabelManagedBy] != LabelManagedByV {
		return false
	}
	return n.Labels[LabelProjectID] == projectID && n.Labels[LabelEnvironmentID] == environmentID
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "is already in use") ||
		strings.Contains(msg, "name already in use")
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such network") ||
		strings.Contains(msg, "not found")
}
