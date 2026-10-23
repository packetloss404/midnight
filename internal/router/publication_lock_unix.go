//go:build !aix && !windows && !plan9 && !js && !wasip1

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

	for {
		err = unix.Flock(
			int(file.Fd()),
			unix.LOCK_EX|unix.LOCK_NB,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) &&
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
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}()
	return fn()
}
