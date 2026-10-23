//go:build windows

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package cliconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func withConfigLock(path string, fn func() error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cliconfig: create config lock directory: %v", redactCredentialError(err))
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cliconfig: open config lock: %v", redactCredentialError(err))
	}
	defer lock.Close()
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		return fmt.Errorf("cliconfig: acquire config lock: %w", err)
	}
	defer windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, &overlapped) //nolint:errcheck
	return fn()
}
