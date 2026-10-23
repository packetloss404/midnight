package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultTraefikDynamicDirectory is the panel-locked publication root for
// generated Traefik file-provider artifacts. Operators install Traefik with
// `providers.file.directory: /etc/midnight/traefik/dynamic`; the control
// plane writes route-group artifacts under this root via the atomic-rename
// publication writer.
//
// Lane 3 chose this directory contract so that:
//
//   - The directory is owned by the midnight UID, separately from
//     `/etc/traefik/traefik.yaml` (operator-owned static config). A
//     compromised tenant cannot influence ingress routing because tenants
//     never have write access to /etc/midnight/.
//   - The publication root is OS-portable. On Windows the path is
//     respected verbatim; on Linux it is the canonical location ADR-006
//     names.
const DefaultTraefikDynamicDirectory = "/etc/midnight/traefik/dynamic"

// TraefikDynamicArtifactExtension is the file extension Lane 3 writes for
// every route-group artifact. Traefik's file provider treats the extension
// as a YAML hint; using `.yaml` rather than `.yml` matches the Traefik docs
// and the existing local-publication writer default.
const TraefikDynamicArtifactExtension = "traefik.dynamic.yaml"

// TraefikDynamicPublicationDirectory wraps a publication writer that scopes
// every write under a single absolute root directory. It is the live
// implementation of the file-provider directory contract documented in
// deploy/traefik/README.md.
type TraefikDynamicPublicationDirectory struct {
	root string
}

// NewTraefikDynamicPublicationDirectory validates the root directory and
// returns a writer scoped to it. The root must exist; Lane 3 refuses to
// auto-create it because the installer owns the create+chown step. If
// `root` is empty, DefaultTraefikDynamicDirectory is used; on Windows the
// caller MUST pass an explicit root because the default Linux path will
// not exist.
func NewTraefikDynamicPublicationDirectory(root string) (*TraefikDynamicPublicationDirectory, error) {
	if strings.TrimSpace(root) == "" {
		root = DefaultTraefikDynamicDirectory
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("traefik dynamic publication root must be absolute")
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("traefik dynamic publication root must be a directory")
	}
	return &TraefikDynamicPublicationDirectory{root: root}, nil
}

// Root returns the absolute publication root.
func (d *TraefikDynamicPublicationDirectory) Root() string {
	if d == nil {
		return ""
	}
	return d.root
}

// Writer returns a route-artifact publication writer scoped to this
// directory. Callers wire the returned writer into ApplyRouteArtifactPublication.
func (d *TraefikDynamicPublicationDirectory) Writer() (*LocalRouteArtifactPublicationWriter, error) {
	if d == nil {
		return nil, errors.New("traefik dynamic publication directory is nil")
	}
	return NewLocalRouteArtifactPublicationWriter(d.root)
}

// WithRouteGroupPublicationLock serializes one environment's complete
// load -> publish -> acknowledge -> probe -> record window across every
// Midnight process that writes the same Traefik publication directory.
//
// The lock is an OS advisory lock rather than an in-memory mutex so multiple
// worker processes cannot interleave stale and current revisions. The lock
// file is intentionally persistent: the kernel releases the lock on process
// exit, while retaining the inode avoids an unlink/recreate race between
// waiters.
func (d *TraefikDynamicPublicationDirectory) WithRouteGroupPublicationLock(
	ctx context.Context,
	projectID string,
	environmentID string,
	fn func() error,
) error {
	if d == nil {
		return errors.New("traefik dynamic publication directory is nil")
	}
	if fn == nil {
		return errors.New("route-group publication lock callback is required")
	}
	projectID = strings.TrimSpace(projectID)
	environmentID = strings.TrimSpace(environmentID)
	if projectID == "" || environmentID == "" {
		return errors.New("route-group publication lock requires project and environment")
	}
	lockStem, err := SanitizeRouteGroupFilename(
		durableRouteGroupName(projectID, environmentID),
	)
	if err != nil {
		return fmt.Errorf("route-group publication lock name: %w", err)
	}
	lockPath := filepath.Join(
		d.root,
		"."+lockStem+".midnight-publication.lock",
	)
	return withExclusiveRoutePublicationFileLock(ctx, lockPath, fn)
}

// PublishRouteGroup is the high-level convenience the control-plane
// reconciler calls. It renders the supplied routes (the planner already
// computes them via PlanRouteReconciliation), runs the validator, and
// writes one route-group artifact through the directory-scoped writer
// using temp-file + atomic rename.
//
// existingHash is the previous artifact's hash (or "" if the artifact is
// new). When existingHash equals the rendered artifact's hash the apply
// is a no-op (no rewrite, no temp file), which keeps inotify watchers
// quiet and avoids burning ACME budget on unchanged routes.
//
// Each route is rendered and written to its OWN flat file in the publication
// root (`<root>/<group>.<routeName>.traefik.dynamic.yaml`). Because Traefik's
// file provider unions every file in `providers.file.directory`, publishing one
// service's route into a shared group no longer clobbers a sibling service's
// route file. The file is flat rather than nested under `<group>/` because
// Traefik loads subdirectory files only at startup while watching just the
// configured directory; see localRouteArtifactName. The current caller (internal/deploy's route
// promoter) passes exactly one route per call, so this is a single route ->
// single file.
//
// existingHash is applied to every route input. It is meaningful only when the
// caller passes a single route (the production path); for a multi-route publish
// a single shared existingHash cannot correctly represent per-route state, so
// callers that batch multiple routes should either publish per route or pass an
// empty existingHash to force a write.
func (d *TraefikDynamicPublicationDirectory) PublishRouteGroup(routeGroup string, routes []RouteInput, existingHash string) (RouteArtifactPublicationApplyEvidence, error) {
	if err := ValidateRouteGroup(routeGroup); err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	writer, err := d.Writer()
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}

	inputs := make([]RouteArtifactPrewriteInput, 0, len(routes))
	for _, route := range routes {
		single := []RouteInput{route}
		file, err := RenderTraefikFile(single)
		if err != nil {
			return RouteArtifactPublicationApplyEvidence{}, err
		}
		rendered, err := RenderTraefikDynamicYAML(single)
		if err != nil {
			return RouteArtifactPublicationApplyEvidence{}, err
		}
		inputs = append(inputs, RouteArtifactPrewriteInput{
			RouteGroup:   routeGroup,
			RouteName:    route.Name,
			Format:       TraefikDynamicArtifactExtension,
			File:         file,
			Rendered:     rendered,
			ExistingHash: existingHash,
		})
	}

	prewrite := PlanRouteArtifactPrewrite(inputs)
	return ApplyRouteArtifactPublication(prewrite, writer)
}

// PublishRouteSet atomically replaces the complete durable route set for a
// group in one file. It observes the current artifact on disk instead of
// trusting a caller-supplied hash, so a controller restart repairs a missing
// artifact and a crash after rename can be recognized as an idempotent skip.
//
// The returned hash proves only that a validated file was written (or already
// existed). It is not Traefik acknowledgement or HTTP reachability evidence.
func (d *TraefikDynamicPublicationDirectory) PublishRouteSet(routeGroup string, routes []RouteInput) (RouteArtifactPublicationApplyEvidence, string, error) {
	if err := ValidateRouteGroup(routeGroup); err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	writer, err := d.Writer()
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	file, err := RenderTraefikFile(routes)
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	rendered, err := RenderTraefikDynamicYAML(routes)
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	hash := RenderedConfigHash(rendered)
	artifactName, err := localRouteArtifactName(RouteArtifactPublicationWrite{
		RouteGroup: routeGroup,
		Format:     TraefikDynamicArtifactExtension,
	})
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	path, err := rootScopedRouteArtifactPath(d.root, artifactName)
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, "", err
	}
	existingHash := ""
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		existingHash = RenderedConfigHash(string(existing))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return RouteArtifactPublicationApplyEvidence{
			Status:      RouteArtifactPublicationApplyStatusError,
			Errors:      1,
			Diagnostics: []string{"code=route_publication_observe_failed"},
		}, "", errors.New("route publication artifact observation failed")
	}
	prewrite := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{{
		RouteGroup:   routeGroup,
		Format:       TraefikDynamicArtifactExtension,
		File:         file,
		Rendered:     rendered,
		ExpectedHash: hash,
		ExistingHash: existingHash,
	}})
	evidence, err := ApplyRouteArtifactPublication(prewrite, writer)
	return evidence, hash, err
}
