//go:build windows

package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const routePublicationLockRetryInterval = 10 * time.Millisecond

func withExclusiveRoutePublicationFileLock(
	ctx context.Context,
	path string,
	fn func() error,
) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open route publication lock: %w", err)
	}
	defer file.Close()

	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return fmt.Errorf("acquire route publication lock: %w", err)
		}
		timer := time.NewTimer(routePublicationLockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf(
				"acquire route publication lock: %w",
				ctx.Err(),
			)
		case <-timer.C:
		}
	}
	defer func() {
		_ = windows.UnlockFileEx(
			windows.Handle(file.Fd()),
			0,
			1,
			0,
			&overlapped,
		)
	}()
	return fn()
}
