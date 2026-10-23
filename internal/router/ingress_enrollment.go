package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"

	midnightnetwork "github.com/midnight/midnight/internal/network"
)

type IngressEnrollmentState string

const (
	IngressEnrollmentPending  IngressEnrollmentState = "pending"
	IngressEnrollmentAttached IngressEnrollmentState = "attached"
	IngressEnrollmentVerified IngressEnrollmentState = "verified"
	IngressEnrollmentFailed   IngressEnrollmentState = "failed"
)

type IngressEnrollment struct {
	ProjectID, EnvironmentID string
	IngressID, NetworkName   string
	State                    IngressEnrollmentState
	LastError                string
	ObservedAt               *time.Time
}

type IngressEnrollmentStore interface {
	RecordIngressEnrollment(context.Context, IngressEnrollment) error
}

type TrustedIngressDockerClient interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	NetworkInspect(context.Context, string) (dockernetwork.Inspect, error)
	NetworkConnect(context.Context, string, string) error
}

// DockerTrustedIngressClient is deliberately narrower than the runtime
// adapter. It can inspect and attach one operator-selected ingress container;
// it has no create/remove/default-network operations.
type DockerTrustedIngressClient struct {
	client *dockerclient.Client
}

func NewDockerTrustedIngressClient(host string) (*DockerTrustedIngressClient, error) {
	opts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
	if strings.TrimSpace(host) != "" {
		opts = append(opts, dockerclient.WithHost(strings.TrimSpace(host)))
	}
	client, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &DockerTrustedIngressClient{client: client}, nil
}

func (c *DockerTrustedIngressClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *DockerTrustedIngressClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	result, err := c.client.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	return result.Container, err
}

func (c *DockerTrustedIngressClient) NetworkInspect(ctx context.Context, id string) (dockernetwork.Inspect, error) {
	result, err := c.client.NetworkInspect(ctx, id, dockerclient.NetworkInspectOptions{})
	return result.Network, err
}

func (c *DockerTrustedIngressClient) NetworkConnect(ctx context.Context, networkID, containerID string) error {
	_, err := c.client.NetworkConnect(ctx, networkID, dockerclient.NetworkConnectOptions{Container: containerID})
	return err
}

// DockerTrustedIngressEnroller attaches only the explicitly configured
// ingress container to the requested environment bridge. It validates the
// bridge's ownership labels and rejects any non-ingress container whose scope
// does not match, preserving workload isolation. It never uses Docker's
// default bridge.
type DockerTrustedIngressEnroller struct {
	Client           TrustedIngressDockerClient
	Store            IngressEnrollmentStore
	IngressContainer string
	Now              func() time.Time
}

func (e *DockerTrustedIngressEnroller) EnsureIngressEnrollment(ctx context.Context, group DurableRouteGroup) error {
	if e == nil || e.Client == nil {
		return errors.New("trusted ingress Docker client is required")
	}
	ingressID := strings.TrimSpace(e.IngressContainer)
	if ingressID == "" {
		return errors.New("explicit trusted ingress container is required")
	}
	projectNetwork := midnightnetwork.ProjectNetwork{ProjectID: group.ProjectID, EnvironmentID: group.EnvironmentID}
	networkName := projectNetwork.Name()
	enrollment := IngressEnrollment{
		ProjectID: group.ProjectID, EnvironmentID: group.EnvironmentID,
		IngressID: ingressID, NetworkName: networkName, State: IngressEnrollmentPending,
	}
	fail := func(err error) error {
		enrollment.State = IngressEnrollmentFailed
		enrollment.LastError = err.Error()
		e.record(ctx, enrollment)
		return err
	}
	observedNetwork, err := e.Client.NetworkInspect(ctx, networkName)
	if err != nil {
		return fail(fmt.Errorf("inspect environment ingress network %q: %w", networkName, err))
	}
	if observedNetwork.Labels[midnightnetwork.LabelManagedBy] != midnightnetwork.LabelManagedByV ||
		observedNetwork.Labels[midnightnetwork.LabelProjectID] != group.ProjectID ||
		observedNetwork.Labels[midnightnetwork.LabelEnvironmentID] != group.EnvironmentID {
		return fail(errors.New("environment ingress network ownership labels do not match route scope"))
	}
	ingress, err := e.Client.ContainerInspect(ctx, ingressID)
	if err != nil {
		return fail(fmt.Errorf("inspect trusted ingress container: %w", err))
	}
	canonicalIngressID := strings.TrimSpace(ingress.ID)
	if canonicalIngressID == "" {
		canonicalIngressID = ingressID
	}
	attached := ingress.NetworkSettings != nil && ingress.NetworkSettings.Networks[networkName] != nil
	if !attached {
		if err := e.Client.NetworkConnect(ctx, observedNetwork.ID, canonicalIngressID); err != nil {
			return fail(fmt.Errorf("attach trusted ingress to environment bridge: %w", err))
		}
		enrollment.State = IngressEnrollmentAttached
		e.record(ctx, enrollment)
		observedNetwork, err = e.Client.NetworkInspect(ctx, networkName)
		if err != nil {
			return fail(fmt.Errorf("verify trusted ingress environment attachment: %w", err))
		}
	}
	if _, ok := observedNetwork.Containers[canonicalIngressID]; !ok {
		// Docker may abbreviate container IDs in the map. Resolve by name/ID.
		found := false
		for containerID := range observedNetwork.Containers {
			if strings.HasPrefix(canonicalIngressID, containerID) || strings.HasPrefix(containerID, canonicalIngressID) {
				found = true
				break
			}
		}
		if !found {
			return fail(errors.New("trusted ingress attachment was not observed on the environment bridge"))
		}
	}
	for containerID := range observedNetwork.Containers {
		if strings.HasPrefix(canonicalIngressID, containerID) || strings.HasPrefix(containerID, canonicalIngressID) {
			continue
		}
		workload, err := e.Client.ContainerInspect(ctx, containerID)
		if err != nil {
			return fail(fmt.Errorf("inspect environment bridge member: %w", err))
		}
		labels := map[string]string{}
		if workload.Config != nil {
			labels = workload.Config.Labels
		}
		if labels[midnightnetwork.LabelProjectID] != group.ProjectID ||
			labels[midnightnetwork.LabelEnvironmentID] != group.EnvironmentID {
			return fail(fmt.Errorf("environment bridge contains cross-scope container %s", containerID))
		}
	}
	now := e.now()
	enrollment.State = IngressEnrollmentVerified
	enrollment.ObservedAt = &now
	enrollment.LastError = ""
	if err := e.record(ctx, enrollment); err != nil {
		return err
	}
	return nil
}

func (e *DockerTrustedIngressEnroller) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *DockerTrustedIngressEnroller) record(ctx context.Context, enrollment IngressEnrollment) error {
	if e.Store == nil {
		return nil
	}
	return e.Store.RecordIngressEnrollment(ctx, enrollment)
}
