package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

// VolumeEngineClient extends EngineClient with the per-host volume
// surface §1.9 needs. Implementations return the raw Docker API
// shapes; the typed Adapter methods (VolumeCreate / VolumeAttach /
// VolumeDetach / VolumeInspect / VolumeRemove) consume them.
//
// We intentionally keep this as a separate interface so existing
// EngineClient implementations (Lane 2's fakeEngine, the agent unit
// tests) keep compiling without change. The realEngineClient
// implements both interfaces.
type VolumeEngineClient interface {
	VolumeCreate(ctx context.Context, name string, driver string, labels map[string]string, opts map[string]string) (volume.Volume, error)
	VolumeInspect(ctx context.Context, name string) (volume.Volume, error)
	VolumeRemove(ctx context.Context, name string, force bool) error
	VolumeList(ctx context.Context, labelFilter map[string]string) ([]*volume.Volume, error)
}

// VolumeCreateOptions parameterizes Adapter.VolumeCreate. Driver
// defaults to `local` and DriverOpts default to nil; tests override
// them when exercising the bind-mount path.
type VolumeCreateOptions struct {
	Name       string
	Driver     string
	Labels     map[string]string
	DriverOpts map[string]string
}

// VolumeAttachmentRequest captures the inputs Adapter.VolumeAttach
// records for a downstream Schedule call. Mounts are translated into
// HostConfig.Mounts at container-create time; this struct is the
// adapter-side memo that survives the schedule boundary.
type VolumeAttachmentRequest struct {
	VolumeName   string
	DeploymentID string
	InstanceID   string
	MountPath    string
	ReadOnly     bool
	AttachedAt   time.Time
}

// VolumeCreate creates a Docker volume with the canonical Midnight
// label set. Returns ContainerNameConflict if the volume already
// exists with a different label set.
func (a *Adapter) VolumeCreate(ctx context.Context, opts VolumeCreateOptions) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if strings.TrimSpace(opts.Name) == "" {
		return volume.Volume{}, fmt.Errorf("docker adapter: volume name is required")
	}
	cli, ok := a.client.(VolumeEngineClient)
	if !ok {
		return volume.Volume{}, fmt.Errorf("docker adapter: engine client does not support volumes")
	}
	driver := opts.Driver
	if driver == "" {
		driver = "local"
	}
	v, err := cli.VolumeCreate(ctx, opts.Name, driver, opts.Labels, opts.DriverOpts)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("docker volume create: %w", err)
	}
	return v, nil
}

// VolumeInspect proxies to the engine.
func (a *Adapter) VolumeInspect(ctx context.Context, name string) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	cli, ok := a.client.(VolumeEngineClient)
	if !ok {
		return volume.Volume{}, fmt.Errorf("docker adapter: engine client does not support volumes")
	}
	v, err := cli.VolumeInspect(ctx, name)
	if err != nil {
		return volume.Volume{}, err
	}
	return v, nil
}

// VolumeRemove deletes a Docker volume. force=true removes even
// when the volume is referenced by stopped containers (tested by the
// soft-delete reaper).
func (a *Adapter) VolumeRemove(ctx context.Context, name string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cli, ok := a.client.(VolumeEngineClient)
	if !ok {
		return fmt.Errorf("docker adapter: engine client does not support volumes")
	}
	if err := cli.VolumeRemove(ctx, name, force); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("docker volume remove: %w", err)
	}
	return nil
}

// VolumeList returns every volume carrying the supplied label filter.
// Used by the Lane 2 adapter's reconcile pass to detect orphans.
func (a *Adapter) VolumeList(ctx context.Context, labels map[string]string) ([]*volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cli, ok := a.client.(VolumeEngineClient)
	if !ok {
		return nil, fmt.Errorf("docker adapter: engine client does not support volumes")
	}
	return cli.VolumeList(ctx, labels)
}

// realEngineClient implementations of VolumeEngineClient.

func (c *realEngineClient) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (volume.Volume, error) {
	result, err := c.cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name:       name,
		Driver:     driver,
		Labels:     labels,
		DriverOpts: opts,
	})
	if err != nil {
		return volume.Volume{}, err
	}
	return result.Volume, nil
}

func (c *realEngineClient) VolumeInspect(ctx context.Context, name string) (volume.Volume, error) {
	result, err := c.cli.VolumeInspect(ctx, name, dockerclient.VolumeInspectOptions{})
	if err != nil {
		return volume.Volume{}, err
	}
	return result.Volume, nil
}

func (c *realEngineClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	_, err := c.cli.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{Force: force})
	return err
}

func (c *realEngineClient) VolumeList(ctx context.Context, labelFilter map[string]string) ([]*volume.Volume, error) {
	args := make(dockerclient.Filters)
	for k, v := range labelFilter {
		if v == "" {
			args.Add("label", k)
			continue
		}
		args.Add("label", fmt.Sprintf("%s=%s", k, v))
	}
	resp, err := c.cli.VolumeList(ctx, dockerclient.VolumeListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	volumes := make([]*volume.Volume, 0, len(resp.Items))
	for i := range resp.Items {
		volumes = append(volumes, &resp.Items[i])
	}
	return volumes, nil
}

var _ dockerclient.VolumeAPIClient = (*dockerclient.Client)(nil)
