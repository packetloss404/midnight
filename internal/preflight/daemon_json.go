package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// DockerDaemonBaselineJSON is the Phase 0 Docker daemon baseline owned by Midnight.
// It intentionally does not include daemon-level init:true; S04 owns per-container HostConfig.Init.
const DockerDaemonBaselineJSON = `{
  "features": {
    "buildkit": true
  },
  "live-restore": true,
  "log-driver": "json-file",
  "log-opts": {
    "max-file": "5",
    "max-size": "10m"
  },
  "no-new-privileges": true,
  "storage-driver": "overlay2"
}`

// DockerDaemonMergeResult describes a daemon.json merge without performing disk I/O.
type DockerDaemonMergeResult struct {
	Merged  []byte
	Changed bool
}

// PathProvider supplies an explicit file path without coupling parser code to host defaults.
type PathProvider interface {
	Path() string
}

// PathProviderFunc adapts a function to PathProvider.
type PathProviderFunc func() string

// Path implements PathProvider.
func (f PathProviderFunc) Path() string {
	return f()
}

// DockerDaemonConflictError reports operator values that conflict with the Midnight baseline.
type DockerDaemonConflictError struct {
	Paths []string
}

func (e *DockerDaemonConflictError) Error() string {
	if len(e.Paths) == 0 {
		return "docker daemon.json conflicts with Midnight baseline"
	}
	return "docker daemon.json conflicts with Midnight baseline at " + strings.Join(e.Paths, ", ")
}

// MergeDockerDaemonBaseline merges existing daemon.json content with the Phase 0 baseline.
func MergeDockerDaemonBaseline(existing []byte) (DockerDaemonMergeResult, error) {
	return MergeDockerDaemonJSON(existing, []byte(DockerDaemonBaselineJSON))
}

// MergeDockerDaemonBaselineFromPath reads a caller-supplied daemon.json fixture and merges the baseline.
func MergeDockerDaemonBaselineFromPath(path string) (DockerDaemonMergeResult, error) {
	return MergeDockerDaemonBaselineFromSource(HostSource{Reader: OSFileReader{}, Paths: HostPaths{DockerDaemonJSON: path}})
}

// MergeDockerDaemonBaselineFromSource reads injected daemon.json content and merges the baseline.
func MergeDockerDaemonBaselineFromSource(source HostSource) (DockerDaemonMergeResult, error) {
	data, err := source.ReadDockerDaemonJSON()
	if err != nil {
		return DockerDaemonMergeResult{}, err
	}
	return MergeDockerDaemonBaseline(data)
}

// DockerDaemonJSONFromPathProvider reads daemon.json bytes from an injected path provider.
func DockerDaemonJSONFromPathProvider(reader FileReader, provider PathProvider) ([]byte, error) {
	if provider == nil {
		return nil, fmt.Errorf("daemon.json path provider is required")
	}
	return HostSource{Reader: reader, Paths: HostPaths{DockerDaemonJSON: provider.Path()}}.ReadDockerDaemonJSON()
}

// MergeDockerDaemonBaselineFromPathProvider reads daemon.json from an injected path provider and merges the baseline.
func MergeDockerDaemonBaselineFromPathProvider(reader FileReader, provider PathProvider) (DockerDaemonMergeResult, error) {
	data, err := DockerDaemonJSONFromPathProvider(reader, provider)
	if err != nil {
		return DockerDaemonMergeResult{}, err
	}
	return MergeDockerDaemonBaseline(data)
}

// MergeDockerDaemonJSON recursively adds missing baseline keys and refuses conflicting values.
// Empty existing content is treated like an absent daemon.json file.
func MergeDockerDaemonJSON(existing, baseline []byte) (DockerDaemonMergeResult, error) {
	current, err := decodeDockerDaemonObject(existing, true)
	if err != nil {
		return DockerDaemonMergeResult{}, fmt.Errorf("parse existing daemon.json: %w", err)
	}

	base, err := decodeDockerDaemonObject(baseline, false)
	if err != nil {
		return DockerDaemonMergeResult{}, fmt.Errorf("parse baseline daemon.json: %w", err)
	}

	merged, conflicts, changed := mergeDaemonObject(current, base, "")
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return DockerDaemonMergeResult{}, &DockerDaemonConflictError{Paths: conflicts}
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return DockerDaemonMergeResult{}, fmt.Errorf("encode merged daemon.json: %w", err)
	}
	out = append(out, '\n')

	if !changed {
		changed = !jsonObjectsEqual(existing, out)
	}

	return DockerDaemonMergeResult{Merged: out, Changed: changed}, nil
}

func decodeDockerDaemonObject(data []byte, allowEmpty bool) (map[string]any, error) {
	if allowEmpty && len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("multiple JSON values")
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("daemon.json must be a JSON object")
	}
	return object, nil
}

func mergeDaemonObject(current, baseline map[string]any, prefix string) (map[string]any, []string, bool) {
	merged := cloneDaemonObject(current)
	var conflicts []string
	changed := false

	for key, baselineValue := range baseline {
		path := joinDaemonPath(prefix, key)
		currentValue, exists := current[key]
		if !exists {
			merged[key] = cloneDaemonValue(baselineValue)
			changed = true
			continue
		}

		currentObject, currentIsObject := currentValue.(map[string]any)
		baselineObject, baselineIsObject := baselineValue.(map[string]any)
		if currentIsObject && baselineIsObject {
			nested, nestedConflicts, nestedChanged := mergeDaemonObject(currentObject, baselineObject, path)
			merged[key] = nested
			conflicts = append(conflicts, nestedConflicts...)
			changed = changed || nestedChanged
			continue
		}

		if !reflect.DeepEqual(currentValue, baselineValue) {
			conflicts = append(conflicts, path)
		}
	}

	return merged, conflicts, changed
}

func cloneDaemonObject(object map[string]any) map[string]any {
	clone := make(map[string]any, len(object))
	for key, value := range object {
		clone[key] = cloneDaemonValue(value)
	}
	return clone
}

func cloneDaemonValue(value any) any {
	if object, ok := value.(map[string]any); ok {
		return cloneDaemonObject(object)
	}
	return value
}

func joinDaemonPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func jsonObjectsEqual(left, right []byte) bool {
	leftObject, leftErr := decodeDockerDaemonObject(left, true)
	rightObject, rightErr := decodeDockerDaemonObject(right, true)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftObject, rightObject)
}
