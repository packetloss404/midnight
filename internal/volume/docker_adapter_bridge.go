// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"

	dockervolume "github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

// SDKVolumeClient is a small wrapper around the Docker SDK volume surface
// that satisfies DockerVolumeClient. It exists so callers can wire the
// production Docker daemon without re-implementing the conversion between
// the volume package's DockerInfo and the SDK's volume.Volume value.
type SDKVolumeClient struct {
	// Inner is the narrower volume API the Docker SDK exposes; in
	// production callers wrap dockerclient.Client which already
	// satisfies dockerclient.VolumeAPIClient.
	Inner SDKVolumeAPIClient
}

// SDKVolumeAPIClient matches the methods Docker's
// dockerclient.VolumeAPIClient ships, narrowed to what the bridge needs.
// Tests pass a fake; production passes a real *dockerclient.Client.
type SDKVolumeAPIClient interface {
	VolumeCreate(ctx context.Context, options dockerclient.VolumeCreateOptions) (dockerclient.VolumeCreateResult, error)
	VolumeInspect(ctx context.Context, name string, options dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error)
	VolumeRemove(ctx context.Context, name string, options dockerclient.VolumeRemoveOptions) (dockerclient.VolumeRemoveResult, error)
}

// NewSDKVolumeClient returns a DockerVolumeClient backed by the supplied
// SDK API client. nil is rejected so the configuration error surfaces at
// boot rather than first call.
func NewSDKVolumeClient(inner SDKVolumeAPIClient) *SDKVolumeClient {
	return &SDKVolumeClient{Inner: inner}
}

// VolumeCreate implements DockerVolumeClient.
func (c *SDKVolumeClient) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (DockerInfo, error) {
	if c == nil || c.Inner == nil {
		return DockerInfo{}, ErrVolumeMissing
	}
	result, err := c.Inner.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name:       name,
		Driver:     driver,
		Labels:     labels,
		DriverOpts: opts,
	})
	if err != nil {
		return DockerInfo{}, err
	}
	return dockerInfoFromSDK(result.Volume), nil
}

// VolumeInspect implements DockerVolumeClient.
func (c *SDKVolumeClient) VolumeInspect(ctx context.Context, name string) (DockerInfo, error) {
	if c == nil || c.Inner == nil {
		return DockerInfo{}, ErrVolumeMissing
	}
	result, err := c.Inner.VolumeInspect(ctx, name, dockerclient.VolumeInspectOptions{})
	if err != nil {
		return DockerInfo{}, err
	}
	return dockerInfoFromSDK(result.Volume), nil
}

// VolumeRemove implements DockerVolumeClient.
func (c *SDKVolumeClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	if c == nil || c.Inner == nil {
		return ErrVolumeMissing
	}
	_, err := c.Inner.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{Force: force})
	return err
}

func dockerInfoFromSDK(v dockervolume.Volume) DockerInfo {
	return DockerInfo{
		Name:       v.Name,
		Driver:     v.Driver,
		Labels:     v.Labels,
		CreatedAt:  v.CreatedAt,
		Mountpoint: v.Mountpoint,
	}
}
