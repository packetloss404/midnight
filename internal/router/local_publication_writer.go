package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LocalRouteArtifactPublicationWriter writes route publication artifacts under a
// single explicit filesystem root. Artifact names are always interpreted as
// relative to Root and are rejected if they are absolute or traverse upward.
type LocalRouteArtifactPublicationWriter struct {
	Root string
}

func NewLocalRouteArtifactPublicationWriter(root string) (*LocalRouteArtifactPublicationWriter, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("route publication root is required")
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("route publication root must be absolute")
	}
	return &LocalRouteArtifactPublicationWriter{Root: filepath.Clean(root)}, nil
}

func (writer *LocalRouteArtifactPublicationWriter) WriteRouteArtifact(write RouteArtifactPublicationWrite) (int, error) {
	if writer == nil || strings.TrimSpace(writer.Root) == "" {
		return 0, errors.New("route publication root is required")
	}

	artifactName, err := localRouteArtifactName(write)
	if err != nil {
		return 0, err
	}
	path, err := rootScopedRouteArtifactPath(writer.Root, artifactName)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, errors.New("route publication artifact directory create failed")
	}
	if err := WriteFileAtomic(path, write.Content, routeGroupArtifactPerm); err != nil {
		return 0, errors.New("route publication artifact write failed")
	}
	// A legacy artifact left in the group subdirectory by an older release is
	// still loaded when Traefik next starts, which would resurrect a stale
	// router alongside the current one and trip the publication verifier's
	// conflicting-revision check. Best-effort removal keeps a restart honest.
	writer.removeLegacyGroupArtifact(write)
	return len(write.Content), nil
}

// removeLegacyGroupArtifact deletes the pre-flat `<group>/<file>` artifact for
// this write, if one survives from an older release. Failures are ignored: the
// current artifact is already published, and a leftover file only matters at
// Traefik's next startup.
func (writer *LocalRouteArtifactPublicationWriter) removeLegacyGroupArtifact(write RouteArtifactPublicationWrite) {
	routeGroup, err := SanitizeRouteGroupFilename(write.RouteGroup)
	if err != nil {
		return
	}
	format := strings.TrimSpace(write.Format)
	if format == "" {
		format = "traefik.dynamic.yaml"
	}
	fileName := format
	if routeName := strings.TrimSpace(write.RouteName); routeName != "" {
		stem, stemErr := SanitizeRouteFilename(routeName)
		if stemErr != nil {
			return
		}
		fileName = stem + "." + format
	}
	legacy := filepath.Join(routeGroup, filepath.FromSlash(fileName))
	if err := validateRelativeRouteArtifactName(legacy); err != nil {
		return
	}
	path, err := rootScopedRouteArtifactPath(writer.Root, legacy)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(filepath.Dir(path))
}

// RemoveRouteArtifact deletes a single route's artifact file within its group
// directory. It is idempotent: a missing file is not an error. Because each
// route owns its own file (see localRouteArtifactName), removing one route
// never disturbs sibling routes published into the same group. Callers derive
// the target from the same RouteGroup/RouteName/Format used to publish it.
func (writer *LocalRouteArtifactPublicationWriter) RemoveRouteArtifact(write RouteArtifactPublicationWrite) error {
	if writer == nil || strings.TrimSpace(writer.Root) == "" {
		return errors.New("route publication root is required")
	}

	artifactName, err := localRouteArtifactName(write)
	if err != nil {
		return err
	}
	path, err := rootScopedRouteArtifactPath(writer.Root, artifactName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("route publication artifact remove failed")
	}
	return nil
}

func localRouteArtifactName(write RouteArtifactPublicationWrite) (string, error) {
	routeGroup, err := SanitizeRouteGroupFilename(write.RouteGroup)
	if err != nil {
		return "", errors.New("route publication route group invalid")
	}
	format := strings.TrimSpace(write.Format)
	if format == "" {
		format = "traefik.dynamic.yaml"
	}
	if err := validateRelativeRouteArtifactName(format); err != nil {
		return "", err
	}

	// Every artifact is written as a flat file directly in the publication
	// root, with the route group as the filename prefix.
	//
	// Traefik's file provider loads files from subdirectories of
	// `providers.file.directory` at startup, but only *watches* the directory
	// itself. A `<group>/<file>` artifact is therefore read once when Traefik
	// starts and never again: subsequent route changes are written correctly,
	// hash-verified, and never observed, so publication fails its
	// acknowledgement check and the promoter rolls the route group back. This
	// was measured on a running host — a new top-level file is picked up
	// within seconds while a new subdirectory file is never seen until Traefik
	// restarts. Keeping the group in the filename preserves per-group and
	// per-route separation, so distinct services publishing into the same
	// group still get their own file.
	fileName := routeGroup + "." + format
	if routeName := strings.TrimSpace(write.RouteName); routeName != "" {
		stem, err := SanitizeRouteFilename(routeName)
		if err != nil {
			return "", errors.New("route publication route name invalid")
		}
		fileName = routeGroup + "." + stem + "." + format
	}
	if err := validateRelativeRouteArtifactName(fileName); err != nil {
		return "", err
	}

	return filepath.FromSlash(fileName), nil
}

func rootScopedRouteArtifactPath(root, artifactName string) (string, error) {
	root = filepath.Clean(root)
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return "", errors.New("route publication root must be absolute")
	}
	if err := validateRelativeRouteArtifactName(artifactName); err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.FromSlash(artifactName))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", errors.New("route publication artifact path escapes root")
	}
	return path, nil
}

func validateRelativeRouteArtifactName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("route publication artifact name is required")
	}
	if filepath.IsAbs(name) || filepath.IsAbs(filepath.FromSlash(name)) {
		return errors.New("route publication artifact name must be relative")
	}
	for _, segment := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return errors.New("route publication artifact name must not traverse root")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("route publication artifact name must not traverse root")
	}
	return nil
}
