package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTraefikDynamicPublicationDirectoryRefusesRelativeRoot(t *testing.T) {
	if _, err := NewTraefikDynamicPublicationDirectory("relative/path"); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestNewTraefikDynamicPublicationDirectoryRefusesMissingDir(t *testing.T) {
	tmp := t.TempDir()
	if _, err := NewTraefikDynamicPublicationDirectory(filepath.Join(tmp, "not-here")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestNewTraefikDynamicPublicationDirectoryRefusesFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTraefikDynamicPublicationDirectory(path); err == nil {
		t.Fatal("file root accepted")
	}
}

func TestPublishRouteGroupAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		ServiceURL: "http://10.0.0.1:8080",
	}}
	evidence, err := pubdir.PublishRouteGroup("envA", routes, "")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if evidence.Status != RouteArtifactPublicationApplyStatusReady {
		t.Fatalf("status = %s, want ready", evidence.Status)
	}
	if evidence.Writes != 1 {
		t.Fatalf("writes = %d, want 1", evidence.Writes)
	}
	// Each route is now written to its own file within the group directory so
	// sibling services in the same group cannot clobber one another.
	stem, err := SanitizeRouteFilename("demo")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename: %v", err)
	}
	expected := filepath.Join(dir, "enva."+stem+".traefik.dynamic.yaml")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("written artifact missing: %v", err)
	}
	if !strings.Contains(string(data), "demo.example.test") {
		t.Fatalf("artifact missing host: %s", data)
	}
	// ACME challenge router must always be present at maximum priority.
	if !strings.Contains(string(data), "acme-http01-challenge") {
		t.Fatalf("artifact missing reserved ACME router: %s", data)
	}
}

func TestPublishRouteGroupSkipsWhenHashMatches(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		ServiceURL: "http://10.0.0.1:8080",
	}}
	rendered, _ := RenderTraefikDynamicYAML(routes)
	hash := RenderedConfigHash(rendered)

	evidence, err := pubdir.PublishRouteGroup("envA", routes, hash)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if evidence.Skips != 1 || evidence.Writes != 0 {
		t.Fatalf("evidence = %+v, want one skip", evidence)
	}
}

// TestPublishRouteGroupDoesNotClobberSiblingServiceRoute proves the core fix:
// two distinct services that publish into the SAME route group (the default
// group key is shared across services) each get their own artifact file, so
// service B's publish does not delete or overwrite service A's route file.
func TestPublishRouteGroupDoesNotClobberSiblingServiceRoute(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}

	const group = "project-1-environment-1"

	if _, err := pubdir.PublishRouteGroup(group, []RouteInput{{
		Name:       "service-a",
		Host:       "a.example.test",
		ServiceURL: "http://10.0.0.1:8080",
	}}, ""); err != nil {
		t.Fatalf("publish service A: %v", err)
	}
	if _, err := pubdir.PublishRouteGroup(group, []RouteInput{{
		Name:       "service-b",
		Host:       "b.example.test",
		ServiceURL: "http://10.0.0.2:8080",
	}}, ""); err != nil {
		t.Fatalf("publish service B: %v", err)
	}

	stemA, err := SanitizeRouteFilename("service-a")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename(service-a): %v", err)
	}
	stemB, err := SanitizeRouteFilename("service-b")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename(service-b): %v", err)
	}

	fileA := filepath.Join(dir, "project-1-environment-1."+stemA+".traefik.dynamic.yaml")
	fileB := filepath.Join(dir, "project-1-environment-1."+stemB+".traefik.dynamic.yaml")

	dataA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("service A route file clobbered by service B publish: %v", err)
	}
	if !strings.Contains(string(dataA), "a.example.test") {
		t.Fatalf("service A file lost its host: %s", dataA)
	}
	dataB, err := os.ReadFile(fileB)
	if err != nil {
		t.Fatalf("service B route file missing: %v", err)
	}
	if !strings.Contains(string(dataB), "b.example.test") {
		t.Fatalf("service B file missing its host: %s", dataB)
	}

	// Both routes publish as flat files directly in the watched root: Traefik
	// reads only that directory and never descends into subdirectories.
	allEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read publication root: %v", err)
	}
	entries := make([]os.DirEntry, 0, len(allEntries))
	for _, e := range allEntries {
		if strings.HasSuffix(e.Name(), ".traefik.dynamic.yaml") {
			entries = append(entries, e)
		}
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("group dir has %d files %v, want exactly 2 (one per service)", len(entries), names)
	}
}

func TestPublishRouteGroupRefusesReservedACMEPath(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		PathPrefix: "/.well-known/acme-challenge/foo",
		ServiceURL: "http://10.0.0.1:8080",
	}}
	if _, err := pubdir.PublishRouteGroup("envA", routes, ""); err == nil {
		t.Fatal("reserved ACME path accepted")
	}
}

func TestPublishRouteGroupRejectsUnsafeGroupBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		ServiceURL: "http://10.0.0.1:8080",
	}}

	for _, group := range []string{"../apps", "apps/newline\ninjected", "apps:\n  evil"} {
		if _, err := pubdir.PublishRouteGroup(group, routes, ""); err == nil {
			t.Fatalf("PublishRouteGroup(%q) error = nil, want rejection", group)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("publication root entries = %#v, want no writes", entries)
	}
}

func TestPublishRouteSetReobservesArtifactAndRepairsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:        "api",
		Host:        "api.example.test",
		PathPrefix:  "/",
		ServiceURLs: []string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"},
	}}
	first, hash, err := pubdir.PublishRouteSet("durable-env", routes)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first.Writes != 1 || hash == "" {
		t.Fatalf("first evidence = %#v hash=%q", first, hash)
	}
	path := filepath.Join(dir, "durable-env."+TraefikDynamicArtifactExtension)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range routes[0].ServiceURLs {
		if !strings.Contains(string(data), backend) {
			t.Fatalf("artifact missing backend %q: %s", backend, data)
		}
	}

	// A fresh publisher instance sees the on-disk artifact and skips.
	restarted, err := NewTraefikDynamicPublicationDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := restarted.PublishRouteSet("durable-env", routes)
	if err != nil {
		t.Fatalf("restart publish: %v", err)
	}
	if second.Skips != 1 || second.Writes != 0 || secondHash != hash {
		t.Fatalf("restart evidence = %#v hash=%q", second, secondHash)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	repaired, repairedHash, err := restarted.PublishRouteSet("durable-env", routes)
	if err != nil {
		t.Fatalf("repair publish: %v", err)
	}
	if repaired.Writes != 1 || repairedHash != hash {
		t.Fatalf("repair evidence = %#v hash=%q", repaired, repairedHash)
	}
}
