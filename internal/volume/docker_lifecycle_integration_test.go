//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"

	"github.com/midnight/midnight/internal/volume"
)

func TestManagerPersistsBytesAcrossRealDockerContainerReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker client unavailable: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		t.Skipf("Docker daemon unavailable: %v", err)
	}

	image := "busybox:1.36"
	pull, err := cli.ImagePull(ctx, image, imagePullOptions())
	if err != nil {
		t.Skipf("pull %s: %v", image, err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()

	store := volume.NewMemoryStore()
	created, err := store.Create(ctx, volume.VolumeCreateInput{
		ProjectID: "prj_it", EnvironmentID: "env_it", ServiceID: "svc_it",
		Name: "data", MountPath: "/data",
	})
	if err != nil {
		t.Fatalf("Create volume intent: %v", err)
	}
	manager := volume.NewManager(store, volume.NewDockerDriver(volume.NewSDKVolumeClient(cli)))
	prepared, err := manager.PrepareServiceDeployment(ctx, volume.DeploymentAttachments{
		DeploymentID: "dep_writer", InstanceID: "inst_writer", ProjectID: "prj_it",
		EnvironmentID: "env_it", ServiceID: "svc_it", Replicas: 1,
	})
	if err != nil {
		t.Fatalf("prepare writer: %v", err)
	}
	if err := manager.ConfirmForSchedule(ctx, prepared); err != nil {
		t.Fatalf("confirm writer: %v", err)
	}
	dockerName := volume.DockerVolumeName(created.ID)
	t.Cleanup(func() {
		_, _ = cli.VolumeRemove(context.Background(), dockerName, dockerclient.VolumeRemoveOptions{Force: true})
	})

	suffix := time.Now().UTC().UnixNano()
	writerName := fmt.Sprintf("midnight-volume-writer-%d", suffix)
	readerName := fmt.Sprintf("midnight-volume-reader-%d", suffix)
	writerID := runVolumeContainer(t, ctx, cli, writerName, image, dockerName, "printf durable-midnight > /data/sentinel")
	if err := manager.ActivatePrepared(ctx, prepared); err != nil {
		t.Fatalf("activate writer: %v", err)
	}
	if _, err := cli.ContainerRemove(ctx, writerID, dockerclient.ContainerRemoveOptions{}); err != nil {
		t.Fatalf("remove writer: %v", err)
	}
	attachments, _ := store.ListAttachments(ctx, created.ID)
	if err := store.MarkAttachmentDetached(ctx, attachments[0].ID, time.Now().UTC()); err != nil {
		t.Fatalf("detach writer: %v", err)
	}

	prepared, err = manager.PrepareServiceDeployment(ctx, volume.DeploymentAttachments{
		DeploymentID: "dep_reader", InstanceID: "inst_reader", ProjectID: "prj_it",
		EnvironmentID: "env_it", ServiceID: "svc_it", Replicas: 1,
	})
	if err != nil {
		t.Fatalf("prepare reader: %v", err)
	}
	if err := manager.ConfirmForSchedule(ctx, prepared); err != nil {
		t.Fatalf("confirm reader: %v", err)
	}
	readerID := runVolumeContainer(t, ctx, cli, readerName, image, dockerName, `test "$(cat /data/sentinel)" = durable-midnight`)
	if err := manager.ActivatePrepared(ctx, prepared); err != nil {
		t.Fatalf("activate reader: %v", err)
	}
	if _, err := cli.ContainerRemove(ctx, readerID, dockerclient.ContainerRemoveOptions{}); err != nil {
		t.Fatalf("remove reader: %v", err)
	}
}

func runVolumeContainer(t *testing.T, ctx context.Context, cli *dockerclient.Client, name, image, source, command string) string {
	t.Helper()
	response, err := cli.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config: &container.Config{Image: image, Cmd: []string{"sh", "-c", command}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{
			Type: mount.TypeVolume, Source: source, Target: "/data",
		}}},
		Name: name,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), response.ID, dockerclient.ContainerRemoveOptions{Force: true})
	})
	// Register the wait before starting so a fast container cannot exit before
	// the client begins listening. NextExit is required here: NotRunning may
	// return immediately for the newly-created, not-yet-started container.
	waitResult := cli.ContainerWait(ctx, response.ID, dockerclient.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, response.ID, dockerclient.ContainerStartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	select {
	case err := <-waitResult.Error:
		if err != nil {
			t.Fatalf("wait %s: %v", name, err)
		}
	case result := <-waitResult.Result:
		if result.StatusCode != 0 {
			t.Fatalf("%s exit code = %d", name, result.StatusCode)
		}
	case <-ctx.Done():
		t.Fatalf("wait %s: %v", name, ctx.Err())
	}
	return response.ID
}

func imagePullOptions() dockerclient.ImagePullOptions {
	return dockerclient.ImagePullOptions{}
}
