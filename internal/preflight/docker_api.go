package preflight

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const minimumDockerAPIVersion = "1.43"

var dockerAPIVersionPattern = regexp.MustCompile(`\bv?\d+\.\d+\b`)

// CompareDockerAPIVersion compares Docker API versions like "1.43".
func CompareDockerAPIVersion(left, right string) (int, error) {
	leftMajor, leftMinor, err := parseDockerAPIVersion(left)
	if err != nil {
		return 0, fmt.Errorf("parse left Docker API version: %w", err)
	}
	rightMajor, rightMinor, err := parseDockerAPIVersion(right)
	if err != nil {
		return 0, fmt.Errorf("parse right Docker API version: %w", err)
	}

	if leftMajor != rightMajor {
		if leftMajor < rightMajor {
			return -1, nil
		}
		return 1, nil
	}
	if leftMinor < rightMinor {
		return -1, nil
	}
	if leftMinor > rightMinor {
		return 1, nil
	}
	return 0, nil
}

// DockerAPISupportsPhase0 reports whether version is at least Docker API 1.43.
func DockerAPISupportsPhase0(version string) (bool, error) {
	comparison, err := CompareDockerAPIVersion(version, minimumDockerAPIVersion)
	if err != nil {
		return false, err
	}
	return comparison >= 0, nil
}

// DockerAPIVersionFromText extracts a Docker API version from caller-supplied command output.
// It accepts either a bare version such as "1.43" or docker version text containing API version lines.
func DockerAPIVersionFromText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("Docker version text is empty")
	}
	if dockerAPIVersionPattern.MatchString(text) && dockerAPIVersionPattern.FindString(text) == text {
		return strings.TrimPrefix(text, "v"), nil
	}

	var fallback string
	inServerSection := false
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		trimmed := strings.TrimSpace(lower)
		if strings.HasPrefix(trimmed, "server:") {
			inServerSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "client:") {
			inServerSection = false
			continue
		}
		if !strings.Contains(lower, "api version") {
			continue
		}
		match := dockerAPIVersionPattern.FindString(line)
		if match == "" {
			continue
		}
		version := strings.TrimPrefix(match, "v")
		if strings.Contains(lower, "server") || inServerSection {
			return version, nil
		}
		if fallback == "" {
			fallback = version
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("Docker API version not found")
}

// DockerAPIVersionFromPath reads caller-supplied fixture or probe output and extracts the API version.
func DockerAPIVersionFromPath(path string) (string, error) {
	return DockerAPIVersionFromSource(HostSource{Reader: OSFileReader{}, Paths: HostPaths{DockerAPIVersion: path}})
}

// DockerAPIVersionFromSource reads injected Docker version output and extracts the API version.
func DockerAPIVersionFromSource(source HostSource) (string, error) {
	return DockerAPIVersionFromSourceContext(context.Background(), source)
}

// DockerAPIVersionFromSourceContext reads or runs injected Docker version output and extracts the API version.
func DockerAPIVersionFromSourceContext(ctx context.Context, source HostSource) (string, error) {
	text, err := source.ReadDockerAPIVersionTextContext(ctx)
	if err != nil {
		return "", err
	}
	return DockerAPIVersionFromText(text)
}

// DockerAPISupportsPhase0FromPath checks a caller-supplied Docker version fixture or probe output.
func DockerAPISupportsPhase0FromPath(path string) (bool, error) {
	version, err := DockerAPIVersionFromPath(path)
	if err != nil {
		return false, err
	}
	return DockerAPISupportsPhase0(version)
}

// DockerAPISupportsPhase0FromSource checks injected Docker version output.
func DockerAPISupportsPhase0FromSource(source HostSource) (bool, error) {
	version, err := DockerAPIVersionFromSource(source)
	if err != nil {
		return false, err
	}
	return DockerAPISupportsPhase0(version)
}

func parseDockerAPIVersion(version string) (int, int, error) {
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	parts := strings.Split(version, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("%q must have major.minor format", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, fmt.Errorf("invalid major version %q", parts[0])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, fmt.Errorf("invalid minor version %q", parts[1])
	}
	return major, minor, nil
}
