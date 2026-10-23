//go:build windows

package router

// syncDirectory is a no-op on Windows: MoveFileExW with MOVEFILE_WRITE_THROUGH
// (used by replaceFile) already flushes the rename to disk, and directory
// handles cannot be fsynced the way POSIX allows.
func syncDirectory(string) error {
	return nil
}
