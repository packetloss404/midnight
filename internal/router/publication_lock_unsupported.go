//go:build plan9 || js || wasip1

package router

import (
	"context"
	"errors"
)

func withExclusiveRoutePublicationFileLock(
	_ context.Context,
	_ string,
	_ func() error,
) error {
	return errors.New(
		"cross-process route publication locking is unsupported on this platform",
	)
}
