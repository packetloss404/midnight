//go:build aix

package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
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

	lock := unix.Flock_t{
		Type:   unix.F_WRLCK,
		Whence: 0,
		Start:  0,
		Len:    1,
	}
	for {
		err = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EACCES) &&
			!errors.Is(err, unix.EAGAIN) {
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
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	}()
	return fn()
}
