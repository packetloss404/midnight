// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// VolumeArchiveDriver is the runtime-specific boundary for destructive
// recovery work. CreateReplacement must return a brand-new volume; it must
// never open the currently attached volume for writing.
type VolumeArchiveDriver interface {
	OpenVolumeArchive(ctx context.Context, volumeName string) (io.ReadCloser, error)
	CreateReplacementVolume(ctx context.Context, sourceVolumeName, restoreID string, replacement ReplacementVolume) (archive io.WriteCloser, err error)
	VerifyReplacementVolume(ctx context.Context, replacementVolumeName string) error
	StageAttachmentSwap(ctx context.Context, sourceVolumeName string, replacement ReplacementVolume) error
	DiscardReplacementVolume(ctx context.Context, replacementVolumeName string) error
}

// ReplacementVolume is allocated durably before restore I/O. RuntimeName must
// use the same canonical name the ordinary volume manager will later ensure,
// so applying a staged restore never creates a second backing volume.
type ReplacementVolume struct {
	ID            string `json:"id"`
	RuntimeName   string `json:"runtime_name"`
	DisplayName   string `json:"display_name"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServiceID     string `json:"service_id"`
	MountPath     string `json:"mount_path"`
	SizeBytes     int64  `json:"size_bytes"`
}

type VolumeBackupRequest struct {
	VolumeName string
	ObjectKey  string
	Passphrase []byte
}

type VolumeBackupResult struct {
	VolumeName string             `json:"volume_name"`
	Archive    StreamBackupResult `json:"archive"`
}

type VolumeRestoreRequest struct {
	RestoreID            string
	SourceVolumeName     string
	ObjectKey            string
	ExpectedObjectSHA256 string
	ExpectedSourceSHA256 string
	Passphrase           []byte
	Replacement          ReplacementVolume
}

// VolumeRestoreResult proves that a replacement volume was populated,
// byte-verified, smoke-tested, and staged for attachment. SourceRetained is
// always true: deletion of the old volume is an explicit later operation.
type VolumeRestoreResult struct {
	RestoreID            string              `json:"restore_id"`
	SourceVolumeName     string              `json:"source_volume_name"`
	ReplacementVolume    string              `json:"replacement_volume_name"`
	SourceRetained       bool                `json:"source_retained"`
	AttachmentSwapStaged bool                `json:"attachment_swap_staged"`
	Archive              StreamRestoreResult `json:"archive"`
}

type VolumeRecovery struct {
	Destination Destination
	Driver      VolumeArchiveDriver
}

func (r VolumeRecovery) Backup(ctx context.Context, request VolumeBackupRequest) (VolumeBackupResult, error) {
	if r.Destination == nil || r.Driver == nil {
		return VolumeBackupResult{}, errors.New("volume backup destination and driver are required")
	}
	request.VolumeName = strings.TrimSpace(request.VolumeName)
	if request.VolumeName == "" {
		return VolumeBackupResult{}, errors.New("volume backup name is required")
	}
	source, err := r.Driver.OpenVolumeArchive(ctx, request.VolumeName)
	if err != nil {
		return VolumeBackupResult{}, fmt.Errorf("volume backup export: %w", err)
	}
	defer func() { _ = source.Close() }()
	result, err := PutEncryptedArchive(ctx, r.Destination, request.ObjectKey, source, request.Passphrase)
	if err != nil {
		return VolumeBackupResult{}, err
	}
	return VolumeBackupResult{VolumeName: request.VolumeName, Archive: result}, nil
}

func (r VolumeRecovery) Restore(ctx context.Context, request VolumeRestoreRequest) (result VolumeRestoreResult, err error) {
	if r.Destination == nil || r.Driver == nil {
		return VolumeRestoreResult{}, errors.New("volume restore destination and driver are required")
	}
	request.RestoreID = strings.TrimSpace(request.RestoreID)
	request.SourceVolumeName = strings.TrimSpace(request.SourceVolumeName)
	request.ExpectedSourceSHA256 = strings.ToLower(strings.TrimSpace(request.ExpectedSourceSHA256))
	if request.RestoreID == "" || request.SourceVolumeName == "" || request.ExpectedSourceSHA256 == "" {
		return VolumeRestoreResult{}, errors.New("volume restore id, source volume, and source checksum are required")
	}
	replacement := request.Replacement.RuntimeName
	target, err := r.Driver.CreateReplacementVolume(ctx, request.SourceVolumeName, request.RestoreID, request.Replacement)
	if err != nil {
		return VolumeRestoreResult{}, fmt.Errorf("volume restore replacement: %w", err)
	}
	replacement = strings.TrimSpace(replacement)
	if replacement == "" || replacement == request.SourceVolumeName || target == nil {
		if target != nil {
			_ = target.Close()
		}
		return VolumeRestoreResult{}, errors.New("volume restore driver did not create a distinct replacement")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if discardErr := r.Driver.DiscardReplacementVolume(context.WithoutCancel(ctx), replacement); discardErr != nil {
			err = errors.Join(err, fmt.Errorf("discard failed replacement volume: %w", discardErr))
		}
	}()

	archive, restoreErr := GetEncryptedArchive(
		ctx,
		r.Destination,
		request.ObjectKey,
		target,
		request.Passphrase,
		request.ExpectedObjectSHA256,
	)
	closeErr := target.Close()
	if restoreErr != nil || closeErr != nil {
		return VolumeRestoreResult{}, errors.Join(restoreErr, closeErr)
	}
	if archive.RestoredSHA256 != request.ExpectedSourceSHA256 {
		return VolumeRestoreResult{}, fmt.Errorf(
			"volume restore source checksum mismatch: got %s",
			archive.RestoredSHA256,
		)
	}
	if err := r.Driver.VerifyReplacementVolume(ctx, replacement); err != nil {
		return VolumeRestoreResult{}, fmt.Errorf("volume restore smoke verification: %w", err)
	}
	if err := r.Driver.StageAttachmentSwap(ctx, request.SourceVolumeName, request.Replacement); err != nil {
		return VolumeRestoreResult{}, fmt.Errorf("volume restore stage attachment swap: %w", err)
	}
	committed = true
	return VolumeRestoreResult{
		RestoreID:            request.RestoreID,
		SourceVolumeName:     request.SourceVolumeName,
		ReplacementVolume:    replacement,
		SourceRetained:       true,
		AttachmentSwapStaged: true,
		Archive:              archive,
	}, nil
}
