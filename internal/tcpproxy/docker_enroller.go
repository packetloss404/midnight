package tcpproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"

	midnightnetwork "github.com/midnight/midnight/internal/network"
)

type DockerNetworkClient interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	NetworkInspect(context.Context, string) (dockernetwork.Inspect, error)
	NetworkConnect(context.Context, string, string) error
	NetworkDisconnect(context.Context, string, string) error
}

type DockerNetworkEnroller struct {
	Client    DockerNetworkClient
	Container string
}

type dockerNetworkClient struct {
	client *dockerclient.Client
}

func NewDockerNetworkClient(host string) (*dockerNetworkClient, error) {
	opts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
	if strings.TrimSpace(host) != "" {
		opts = append(opts, dockerclient.WithHost(strings.TrimSpace(host)))
	}
	client, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &dockerNetworkClient{client: client}, nil
}

func (c *dockerNetworkClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *dockerNetworkClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	result, err := c.client.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	return result.Container, err
}

func (c *dockerNetworkClient) NetworkInspect(ctx context.Context, id string) (dockernetwork.Inspect, error) {
	result, err := c.client.NetworkInspect(ctx, id, dockerclient.NetworkInspectOptions{})
	return result.Network, err
}

func (c *dockerNetworkClient) NetworkConnect(ctx context.Context, networkID, containerID string) error {
	_, err := c.client.NetworkConnect(ctx, networkID, dockerclient.NetworkConnectOptions{Container: containerID})
	return err
}

func (c *dockerNetworkClient) NetworkDisconnect(ctx context.Context, networkID, containerID string) error {
	_, err := c.client.NetworkDisconnect(ctx, networkID, dockerclient.NetworkDisconnectOptions{Container: containerID})
	return err
}

func (e DockerNetworkEnroller) ReconcileNetworks(ctx context.Context, scopes []Scope) error {
	if e.Client == nil {
		return errors.New("tcp proxy Docker network client is required")
	}
	containerName := strings.TrimSpace(e.Container)
	if containerName == "" {
		return errors.New("explicit TCP proxy container is required")
	}
	proxy, err := e.Client.ContainerInspect(ctx, containerName)
	if err != nil {
		return fmt.Errorf("inspect TCP proxy container: %w", err)
	}
	containerID := strings.TrimSpace(proxy.ID)
	if containerID == "" {
		return errors.New("TCP proxy container inspection returned no id")
	}
	desired := make(map[string]Scope)
	for _, scope := range scopes {
		if err := scope.Validate(); err != nil {
			return err
		}
		name := (midnightnetwork.ProjectNetwork{
			ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID,
		}).Name()
		desired[name] = scope
	}
	for networkName, scope := range desired {
		networkValue, err := e.Client.NetworkInspect(ctx, networkName)
		if err != nil {
			return fmt.Errorf("inspect TCP proxy environment network: %w", err)
		}
		if networkValue.Labels[midnightnetwork.LabelManagedBy] != midnightnetwork.LabelManagedByV ||
			networkValue.Labels[midnightnetwork.LabelProjectID] != scope.ProjectID ||
			networkValue.Labels[midnightnetwork.LabelEnvironmentID] != scope.EnvironmentID {
			return errors.New("TCP proxy environment network ownership labels do not match endpoint scope")
		}
		attached := proxy.NetworkSettings != nil && proxy.NetworkSettings.Networks[networkName] != nil
		if !attached {
			if err := e.Client.NetworkConnect(ctx, networkValue.ID, containerID); err != nil {
				return fmt.Errorf("attach TCP proxy to environment network: %w", err)
			}
		}
	}
	proxy, err = e.Client.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("verify TCP proxy environment network enrollment: %w", err)
	}
	for networkName := range desired {
		if proxy.NetworkSettings == nil || proxy.NetworkSettings.Networks[networkName] == nil {
			return errors.New("TCP proxy environment network enrollment was not observed")
		}
	}
	if proxy.NetworkSettings == nil {
		return nil
	}
	for networkName := range proxy.NetworkSettings.Networks {
		if _, keep := desired[networkName]; keep {
			continue
		}
		networkValue, err := e.Client.NetworkInspect(ctx, networkName)
		if err != nil {
			continue
		}
		if networkValue.Labels[midnightnetwork.LabelManagedBy] != midnightnetwork.LabelManagedByV {
			continue
		}
		if err := e.Client.NetworkDisconnect(ctx, networkValue.ID, containerID); err != nil {
			return fmt.Errorf("detach TCP proxy from stale environment network: %w", err)
		}
	}
	return nil
}
