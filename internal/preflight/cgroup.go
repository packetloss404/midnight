package preflight

import (
	"context"
	"fmt"
	"strings"
)

// CgroupMount describes one cgroup mount parsed from Linux mountinfo text.
type CgroupMount struct {
	MountPoint string
	FSType     string
}

// ParseCgroupMountInfo parses /proc/*/mountinfo text supplied by tests or callers.
func ParseCgroupMountInfo(text string) ([]CgroupMount, error) {
	var mounts []CgroupMount
	for lineNumber, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			return nil, fmt.Errorf("mountinfo line %d missing separator", lineNumber+1)
		}
		fields := strings.Fields(parts[0])
		fsFields := strings.Fields(parts[1])
		if len(fields) < 5 || len(fsFields) < 1 {
			return nil, fmt.Errorf("mountinfo line %d is incomplete", lineNumber+1)
		}

		fsType := fsFields[0]
		if fsType != "cgroup" && fsType != "cgroup2" {
			continue
		}
		mounts = append(mounts, CgroupMount{
			MountPoint: unescapeMountInfo(fields[4]),
			FSType:     fsType,
		})
	}
	return mounts, nil
}

// HasCgroupV2 reports whether supplied mountinfo text contains a cgroup2 mount.
func HasCgroupV2(text string) (bool, error) {
	mounts, err := ParseCgroupMountInfo(text)
	if err != nil {
		return false, err
	}
	for _, mount := range mounts {
		if mount.FSType == "cgroup2" {
			return true, nil
		}
	}
	return false, nil
}

// HasCgroupV2FromPath reads caller-supplied fixture or probe data and checks it for cgroup v2.
func HasCgroupV2FromPath(path string) (bool, error) {
	return HasCgroupV2FromSource(HostSource{Reader: OSFileReader{}, Paths: HostPaths{MountInfo: path}})
}

// HasCgroupV2FromSource reads injected mountinfo text and checks it for cgroup v2.
func HasCgroupV2FromSource(source HostSource) (bool, error) {
	data, err := source.ReadMountInfo()
	if err != nil {
		return false, err
	}
	return HasCgroupV2(data)
}

// CgroupFSTypeFromText parses stat -fc %T /sys/fs/cgroup output.
func CgroupFSTypeFromText(text string) (string, error) {
	fsType := strings.TrimSpace(text)
	if fsType == "" {
		return "", fmt.Errorf("cgroup filesystem type text is empty")
	}
	if strings.ContainsAny(fsType, " \t\n\r") {
		return "", fmt.Errorf("cgroup filesystem type %q must be a single token", fsType)
	}
	return fsType, nil
}

// CgroupFSTypeFromSource runs an injected stat probe and parses its output.
func CgroupFSTypeFromSource(ctx context.Context, source HostSource) (string, error) {
	text, err := source.ReadCgroupFSType(ctx)
	if err != nil {
		return "", err
	}
	return CgroupFSTypeFromText(text)
}

// HasCgroupV2FSType reports whether stat -fc %T output identifies cgroup v2.
func HasCgroupV2FSType(text string) (bool, error) {
	fsType, err := CgroupFSTypeFromText(text)
	if err != nil {
		return false, err
	}
	return fsType == "cgroup2fs", nil
}

// HasCgroupV2FSTypeFromSource runs an injected stat probe and checks for cgroup v2.
func HasCgroupV2FSTypeFromSource(ctx context.Context, source HostSource) (bool, error) {
	fsType, err := CgroupFSTypeFromSource(ctx, source)
	if err != nil {
		return false, err
	}
	return fsType == "cgroup2fs", nil
}

func unescapeMountInfo(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}
