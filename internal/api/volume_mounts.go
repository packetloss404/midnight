// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"sync"
)

// MemoryVolumeMountStore is the in-memory implementation of VolumeMountStore
// for unit tests and developer-mode boots. The Phase 1 wire-up records the
// mount path the create request supplied so subsequent GETs echo it back
// even before a deployment has materialized the attachment.
type MemoryVolumeMountStore struct {
	mu   sync.Mutex
	data map[string]string
}

// NewMemoryVolumeMountStore returns an empty store.
func NewMemoryVolumeMountStore() *MemoryVolumeMountStore {
	return &MemoryVolumeMountStore{data: make(map[string]string)}
}

// SetMountPath records the (volume_id, mount_path) pair.
func (s *MemoryVolumeMountStore) SetMountPath(ctx context.Context, volumeID, mountPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[volumeID] = mountPath
	return nil
}

// GetMountPath returns the recorded path or the empty string when missing.
func (s *MemoryVolumeMountStore) GetMountPath(ctx context.Context, volumeID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[volumeID], nil
}
