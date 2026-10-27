//go:build windows

package main

import "os"

// The command blocks Windows; Unix file mode bits cannot establish this trust
// boundary on an NTFS ACL. Do not silently weaken credential checks.
func trustedFileOwner(os.FileInfo) bool { return false }
