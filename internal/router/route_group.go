package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const routeGroupArtifactPerm = 0o640

// RouteGroupDynamicConfigPath returns the deterministic Phase 0 JSON artifact path
// for a route group. This is an interim test artifact, not final Traefik YAML.
func RouteGroupDynamicConfigPath(dir, routeGroup string) (string, error) {
	name, err := SanitizeRouteGroupFilename(routeGroup)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// WriteRouteGroupDynamicConfig renders routes and writes the route group JSON
// artifact atomically.
func WriteRouteGroupDynamicConfig(dir, routeGroup string, routes []RouteInput) (string, error) {
	path, err := RouteGroupDynamicConfigPath(dir, routeGroup)
	if err != nil {
		return "", err
	}

	file, err := RenderTraefikFile(routes)
	if err != nil {
		return "", err
	}

	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		return "", fmt.Errorf("marshal route group dynamic config: %w", err)
	}

	if err := WriteFileAtomic(path, data.Bytes(), routeGroupArtifactPerm); err != nil {
		return "", err
	}

	return path, nil
}
