package docker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// CGroupMode reports the detected cgroup hierarchy on the host. ADR-005
// hard-requires the unified hierarchy ("cgroup2fs") and refuses to operate
// on hybrid or v1 hosts.
type CGroupMode string

const (
	// CGroupModeUnknown is the default zero value before detection runs or
	// when the host's filesystem cannot be probed (non-Linux).
	CGroupModeUnknown CGroupMode = "unknown"
	// CGroupModeV1 indicates the legacy split hierarchy.
	CGroupModeV1 CGroupMode = "v1"
	// CGroupModeV2 indicates the unified hierarchy required by Midnight.
	CGroupModeV2 CGroupMode = "v2"
	// CGroupModeHybrid indicates v2 is mounted with v1 controllers attached;
	// ADR-005 treats this as v1 for refusal purposes.
	CGroupModeHybrid CGroupMode = "hybrid"
)

// CGroupV2RemediationMessage is the panel-locked operator-facing remediation
// string (ADR-005, line 88). The exact wording is contractual; do not
// paraphrase without ADR amendment.
const CGroupV2RemediationMessage = "add 'systemd.unified_cgroup_hierarchy=1' to the kernel command line and reboot"

// DetectCGroupMode probes the host filesystem to identify the active cgroup
// hierarchy. The probe is cheap (reads /sys/fs/cgroup metadata + a controller
// list file) and is intended for agent boot, not the hot path.
//
// On non-Linux hosts the probe returns CGroupModeUnknown without error so
// developer machines (Windows/macOS) can still run unit tests. The Docker
// adapter Init() refuses to start when DetectCGroupMode returns anything
// other than CGroupModeV2.
func DetectCGroupMode() (CGroupMode, error) {
	return detectCGroupMode("/sys/fs/cgroup")
}

// detectCGroupMode is the test seam. Real callers use DetectCGroupMode,
// which pins root="/sys/fs/cgroup".
func detectCGroupMode(root string) (CGroupMode, error) {
	if root == "" {
		return CGroupModeUnknown, fmt.Errorf("cgroup root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CGroupModeUnknown, nil
		}
		return CGroupModeUnknown, fmt.Errorf("stat cgroup root: %w", err)
	}
	if !info.IsDir() {
		return CGroupModeUnknown, fmt.Errorf("cgroup root %q is not a directory", root)
	}

	// cgroup v2 unified hierarchy presents a `cgroup.controllers` file at
	// the root of /sys/fs/cgroup. v1 splits controllers into per-controller
	// subtrees and never has a top-level `cgroup.controllers` file.
	if _, err := os.Stat(joinPath(root, "cgroup.controllers")); err == nil {
		// Hybrid mode: v2 is mounted but the legacy v1 freezer/cpu/memory
		// controllers are still attached at /sys/fs/cgroup/<controller>.
		if hasV1Controllers(root) {
			return CGroupModeHybrid, nil
		}
		return CGroupModeV2, nil
	}
	// No top-level controllers file means we are on v1 or have no cgroup
	// support at all.
	if hasV1Controllers(root) {
		return CGroupModeV1, nil
	}
	return CGroupModeUnknown, nil
}

// EnsureCGroupV2 returns nil when the host's unified hierarchy is active and
// a typed *Error with code CGROUP_V2_REQUIRED otherwise. This is the agent's
// hard-stop on Init().
func EnsureCGroupV2(mode CGroupMode) error {
	switch mode {
	case CGroupModeV2:
		return nil
	case CGroupModeV1, CGroupModeHybrid:
		detail := fmt.Sprintf("detected cgroup mode %q", mode)
		return newError(failurecodes.CGroupV2Required, CGroupV2RemediationMessage, detail)
	case CGroupModeUnknown, "":
		return newError(failurecodes.CGroupV2Required, CGroupV2RemediationMessage, "could not detect cgroup hierarchy on this host")
	default:
		return newError(failurecodes.CGroupV2Required, CGroupV2RemediationMessage, fmt.Sprintf("unrecognized cgroup mode %q", mode))
	}
}

func hasV1Controllers(root string) bool {
	for _, controller := range []string{"memory", "cpu", "freezer", "pids"} {
		path := joinPath(root, controller)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// joinPath joins two filesystem path segments using forward slashes. The
// adapter only ever talks to Linux paths under /sys/fs/cgroup, so we stay
// off filepath.Join (which would emit Windows separators on developer
// machines and break the test fixtures).
func joinPath(root, name string) string {
	if strings.HasSuffix(root, "/") {
		return root + name
	}
	return root + "/" + name
}
