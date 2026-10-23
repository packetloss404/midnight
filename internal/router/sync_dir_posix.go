//go:build !windows

package router

import "os"

// syncDirectory fsyncs a directory so that a preceding atomic rename is durable
// across power loss on filesystems (ext4/xfs) that do not otherwise persist the
// directory entry until the parent directory is itself synced.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
