// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/midnight/midnight/internal/volume"
)

const DefaultVolumeArchiveHelperImage = "alpine@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1"

var dockerVolumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// VolumeAttachmentStager atomically records a replacement-volume attachment
// intent. Its implementation owns the control-plane CAS; it must not remove
// the source attachment or source volume.
type VolumeAttachmentStager interface {
	StageReplacement(ctx context.Context, sourceVolumeName string, replacement ReplacementVolume) error
}

// DockerCLIVolumeArchiveDriver performs real tar I/O through short-lived,
// network-disabled helper containers. Commands are passed as argv without a
// host shell, and HelperImage must be digest-pinned.
type DockerCLIVolumeArchiveDriver struct {
	Binary      string
	Host        string
	HelperImage string
	Stager      VolumeAttachmentStager
}

func (d DockerCLIVolumeArchiveDriver) binary() string {
	if strings.TrimSpace(d.Binary) == "" {
		return "docker"
	}
	return d.Binary
}

func (d DockerCLIVolumeArchiveDriver) helperImage() (string, error) {
	image := strings.TrimSpace(d.HelperImage)
	if image == "" {
		image = DefaultVolumeArchiveHelperImage
	}
	if !strings.Contains(image, "@sha256:") {
		return "", errors.New("volume archive helper image must be digest-pinned")
	}
	return image, nil
}

func (d DockerCLIVolumeArchiveDriver) command(ctx context.Context, args ...string) *exec.Cmd {
	if host := strings.TrimSpace(d.Host); host != "" {
		args = append([]string{"--host", host}, args...)
	}
	return exec.CommandContext(ctx, d.binary(), args...)
}

func (d DockerCLIVolumeArchiveDriver) OpenVolumeArchive(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if err := validateDockerVolumeName(volumeName); err != nil {
		return nil, err
	}
	image, err := d.helperImage()
	if err != nil {
		return nil, err
	}
	cmd := d.command(ctx,
		"run", "--rm", "--network", "none", "--read-only",
		"--mount", "type=volume,src="+volumeName+",dst=/source,readonly",
		image, "tar", "-C", "/source", "-cf", "-", ".",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &boundedWriter{writer: &stderr, remaining: 16 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Docker volume export: %w", err)
	}
	return &commandReadCloser{
		ReadCloser: stdout,
		wait: func() error {
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("Docker volume export: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
			return nil
		},
	}, nil
}

func (d DockerCLIVolumeArchiveDriver) CreateReplacementVolume(ctx context.Context, sourceVolumeName, restoreID string, replacement ReplacementVolume) (io.WriteCloser, error) {
	if err := validateDockerVolumeName(sourceVolumeName); err != nil {
		return nil, err
	}
	restoreID = strings.TrimSpace(restoreID)
	if restoreID == "" {
		return nil, errors.New("volume restore id is required")
	}
	if err := validateReplacementVolume(replacement); err != nil {
		return nil, err
	}
	image, err := d.helperImage()
	if err != nil {
		return nil, err
	}
	replacementName := replacement.RuntimeName
	exists := d.command(ctx, "volume", "inspect", replacementName)
	if err := exists.Run(); err == nil {
		return nil, fmt.Errorf("replacement volume %q already exists", replacementName)
	}
	createArgs := []string{
		"volume", "create",
	}
	labels := volume.EnsureLabels(
		replacement.ID,
		replacement.ServiceID,
		replacement.EnvironmentID,
		replacement.ProjectID,
		replacement.DisplayName,
		map[string]string{
			"midnight.restore_id":     restoreID,
			"midnight.restore_source": sourceVolumeName,
		},
	)
	labelKeys := make([]string, 0, len(labels))
	for key := range labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		createArgs = append(createArgs, "--label", key+"="+labels[key])
	}
	createArgs = append(createArgs, replacementName)
	create := d.command(ctx, createArgs...)
	var createStderr bytes.Buffer
	create.Stderr = &boundedWriter{writer: &createStderr, remaining: 16 << 10}
	if output, err := create.Output(); err != nil {
		return nil, fmt.Errorf("create Docker replacement volume: %w: %s", err, strings.TrimSpace(createStderr.String()))
	} else if strings.TrimSpace(string(output)) != replacementName {
		return nil, errors.New("Docker returned an unexpected replacement volume name")
	}

	cmd := d.command(ctx,
		"run", "--rm", "--network", "none", "-i",
		"--mount", "type=volume,src="+replacementName+",dst=/target",
		image, "tar", "-C", "/target", "-xf", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &boundedWriter{writer: &stderr, remaining: 16 << 10}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = d.DiscardReplacementVolume(context.WithoutCancel(ctx), replacementName)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = d.DiscardReplacementVolume(context.WithoutCancel(ctx), replacementName)
		return nil, fmt.Errorf("start Docker volume restore: %w", err)
	}
	return &commandWriteCloser{
		WriteCloser: stdin,
		wait: func() error {
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("Docker volume restore: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
			return nil
		},
	}, nil
}

func (d DockerCLIVolumeArchiveDriver) VerifyReplacementVolume(ctx context.Context, replacementVolumeName string) error {
	if err := validateDockerVolumeName(replacementVolumeName); err != nil {
		return err
	}
	image, err := d.helperImage()
	if err != nil {
		return err
	}
	cmd := d.command(ctx,
		"run", "--rm", "--network", "none", "--read-only",
		"--mount", "type=volume,src="+replacementVolumeName+",dst=/target,readonly",
		image, "tar", "-C", "/target", "-cf", "-", ".",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &boundedWriter{writer: &stderr, remaining: 16 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Docker replacement verification: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, stdout)
	waitErr := cmd.Wait()
	if err := errors.Join(copyErr, waitErr); err != nil {
		return fmt.Errorf("Docker replacement verification: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if written == 0 || len(hash.Sum(nil)) != sha256.Size {
		return errors.New("Docker replacement verification produced no tar evidence")
	}
	return nil
}

func (d DockerCLIVolumeArchiveDriver) StageAttachmentSwap(ctx context.Context, sourceVolumeName string, replacement ReplacementVolume) error {
	if d.Stager == nil {
		return errors.New("volume replacement attachment stager is required")
	}
	if err := validateDockerVolumeName(sourceVolumeName); err != nil {
		return err
	}
	if err := validateReplacementVolume(replacement); err != nil {
		return err
	}
	if sourceVolumeName == replacement.RuntimeName {
		return errors.New("source and replacement volume must differ")
	}
	return d.Stager.StageReplacement(ctx, sourceVolumeName, replacement)
}

func (d DockerCLIVolumeArchiveDriver) DiscardReplacementVolume(ctx context.Context, replacementVolumeName string) error {
	if err := validateDockerVolumeName(replacementVolumeName); err != nil {
		return err
	}
	cmd := d.command(ctx, "volume", "rm", replacementVolumeName)
	var stderr bytes.Buffer
	cmd.Stderr = &boundedWriter{writer: &stderr, remaining: 16 << 10}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remove Docker replacement volume: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func validateDockerVolumeName(name string) error {
	if !dockerVolumeNamePattern.MatchString(strings.TrimSpace(name)) {
		return errors.New("Docker volume name is invalid")
	}
	return nil
}

func dockerRestoreVolumeID(sourceVolumeName, restoreID string) string {
	sum := sha256.Sum256([]byte(sourceVolumeName + "\x00" + restoreID))
	return "vol_restore_" + hex.EncodeToString(sum[:12])
}

func dockerRestoreVolumeName(sourceVolumeName, restoreID string) string {
	return volume.DockerVolumeName(dockerRestoreVolumeID(sourceVolumeName, restoreID))
}

func validateReplacementVolume(replacement ReplacementVolume) error {
	if strings.TrimSpace(replacement.ID) == "" ||
		strings.TrimSpace(replacement.ProjectID) == "" ||
		strings.TrimSpace(replacement.EnvironmentID) == "" ||
		strings.TrimSpace(replacement.ServiceID) == "" ||
		strings.TrimSpace(replacement.DisplayName) == "" ||
		strings.TrimSpace(replacement.MountPath) == "" {
		return errors.New("replacement volume identity, scope, name, and mount path are required")
	}
	if replacement.SizeBytes < 0 {
		return errors.New("replacement volume size must be non-negative")
	}
	if replacement.RuntimeName != volume.DockerVolumeName(replacement.ID) {
		return errors.New("replacement runtime name does not match the durable volume id")
	}
	return validateDockerVolumeName(replacement.RuntimeName)
}

type commandReadCloser struct {
	io.ReadCloser
	wait     func() error
	waitOnce sync.Once
	waitErr  error
}

func (r *commandReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		r.waitOnce.Do(func() { r.waitErr = r.wait() })
		if r.waitErr != nil {
			return n, r.waitErr
		}
	}
	return n, err
}

func (r *commandReadCloser) Close() error {
	closeErr := r.ReadCloser.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	r.waitOnce.Do(func() { r.waitErr = r.wait() })
	return errors.Join(closeErr, r.waitErr)
}

type commandWriteCloser struct {
	io.WriteCloser
	wait     func() error
	waitOnce sync.Once
	waitErr  error
}

func (w *commandWriteCloser) Close() error {
	closeErr := w.WriteCloser.Close()
	w.waitOnce.Do(func() { w.waitErr = w.wait() })
	return errors.Join(closeErr, w.waitErr)
}

type boundedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.remaining <= 0 {
		return original, nil
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	_, err := w.writer.Write(p)
	w.remaining -= len(p)
	return original, err
}
