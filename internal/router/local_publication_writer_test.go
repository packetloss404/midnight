package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRouteArtifactPublicationWriterWritesUnderRoot(t *testing.T) {
	root := t.TempDir()
	writer, err := NewLocalRouteArtifactPublicationWriter(root)
	if err != nil {
		t.Fatalf("NewLocalRouteArtifactPublicationWriter returned error: %v", err)
	}

	written, err := writer.WriteRouteArtifact(RouteArtifactPublicationWrite{
		RouteGroup: "Group A",
		Format:     "traefik.dynamic.yaml",
		Content:    []byte("http:\n  routers: {}\n"),
	})
	if err != nil {
		t.Fatalf("WriteRouteArtifact returned error: %v", err)
	}
	if written != len("http:\n  routers: {}\n") {
		t.Fatalf("written = %d, want content length", written)
	}

	got, err := os.ReadFile(filepath.Join(root, "group-a.traefik.dynamic.yaml"))
	if err != nil {
		t.Fatalf("read written artifact: %v", err)
	}
	if string(got) != "http:\n  routers: {}\n" {
		t.Fatalf("written content = %q", string(got))
	}
}

func TestLocalRouteArtifactPublicationWriterWritesPerRouteFile(t *testing.T) {
	root := t.TempDir()
	writer, err := NewLocalRouteArtifactPublicationWriter(root)
	if err != nil {
		t.Fatalf("NewLocalRouteArtifactPublicationWriter returned error: %v", err)
	}

	if _, err := writer.WriteRouteArtifact(RouteArtifactPublicationWrite{
		RouteGroup: "Group A",
		RouteName:  "svc-a",
		Format:     "traefik.dynamic.yaml",
		Content:    []byte("a"),
	}); err != nil {
		t.Fatalf("write route svc-a: %v", err)
	}
	if _, err := writer.WriteRouteArtifact(RouteArtifactPublicationWrite{
		RouteGroup: "Group A",
		RouteName:  "svc-b",
		Format:     "traefik.dynamic.yaml",
		Content:    []byte("b"),
	}); err != nil {
		t.Fatalf("write route svc-b: %v", err)
	}

	stemA, err := SanitizeRouteFilename("svc-a")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename: %v", err)
	}
	stemB, err := SanitizeRouteFilename("svc-b")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename: %v", err)
	}

	gotA, err := os.ReadFile(filepath.Join(root, "group-a."+stemA+".traefik.dynamic.yaml"))
	if err != nil {
		t.Fatalf("read svc-a artifact: %v", err)
	}
	if string(gotA) != "a" {
		t.Fatalf("svc-a content = %q", gotA)
	}
	gotB, err := os.ReadFile(filepath.Join(root, "group-a."+stemB+".traefik.dynamic.yaml"))
	if err != nil {
		t.Fatalf("read svc-b artifact: %v", err)
	}
	if string(gotB) != "b" {
		t.Fatalf("svc-b content = %q", gotB)
	}
}

func TestLocalRouteArtifactPublicationWriterRemoveIsIsolatedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	writer, err := NewLocalRouteArtifactPublicationWriter(root)
	if err != nil {
		t.Fatalf("NewLocalRouteArtifactPublicationWriter returned error: %v", err)
	}

	keep := RouteArtifactPublicationWrite{RouteGroup: "group-a", RouteName: "keep", Format: "traefik.dynamic.yaml", Content: []byte("keep")}
	drop := RouteArtifactPublicationWrite{RouteGroup: "group-a", RouteName: "drop", Format: "traefik.dynamic.yaml", Content: []byte("drop")}
	for _, w := range []RouteArtifactPublicationWrite{keep, drop} {
		if _, err := writer.WriteRouteArtifact(w); err != nil {
			t.Fatalf("write %s: %v", w.RouteName, err)
		}
	}

	if err := writer.RemoveRouteArtifact(drop); err != nil {
		t.Fatalf("remove drop: %v", err)
	}
	// Idempotent: removing an already-absent route is not an error.
	if err := writer.RemoveRouteArtifact(drop); err != nil {
		t.Fatalf("remove drop (second): %v", err)
	}

	stemKeep, _ := SanitizeRouteFilename("keep")
	stemDrop, _ := SanitizeRouteFilename("drop")
	if _, err := os.Stat(filepath.Join(root, "group-a."+stemKeep+".traefik.dynamic.yaml")); err != nil {
		t.Fatalf("removing drop deleted sibling keep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "group-a."+stemDrop+".traefik.dynamic.yaml")); !os.IsNotExist(err) {
		t.Fatalf("drop route file still present after remove: err=%v", err)
	}
}

func TestNewLocalRouteArtifactPublicationWriterRejectsRelativeRoot(t *testing.T) {
	if _, err := NewLocalRouteArtifactPublicationWriter("relative-root"); err == nil {
		t.Fatal("NewLocalRouteArtifactPublicationWriter returned nil error for relative root")
	}
}

func TestLocalRouteArtifactPublicationWriterRejectsUnsafeArtifactNames(t *testing.T) {
	root := t.TempDir()
	writer, err := NewLocalRouteArtifactPublicationWriter(root)
	if err != nil {
		t.Fatalf("NewLocalRouteArtifactPublicationWriter returned error: %v", err)
	}

	for _, format := range []string{
		filepath.Join(root, "absolute.yaml"),
		"../escape.yaml",
		"nested/../escape.yaml",
		`nested\..\escape.yaml`,
	} {
		if _, err := writer.WriteRouteArtifact(RouteArtifactPublicationWrite{RouteGroup: "group-a", Format: format, Content: []byte("secret")}); err == nil {
			t.Fatalf("WriteRouteArtifact(%q) returned nil error, want rejection", format)
		}
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.yaml")); err == nil {
		t.Fatal("unsafe artifact write created an escape file outside the root")
	}
}

func TestLocalRouteArtifactPublicationWriterErrorsRemainSupportSafeThroughApply(t *testing.T) {
	root := t.TempDir()
	writer, err := NewLocalRouteArtifactPublicationWriter(root)
	if err != nil {
		t.Fatalf("NewLocalRouteArtifactPublicationWriter returned error: %v", err)
	}
	plan := RouteArtifactPrewritePlan{
		Writes: 1,
		Items: []RouteArtifactPrewriteItem{{
			RouteGroup: "secret-route-group",
			Format:     "../secret-host.yaml",
			Hash:       "hash",
			Action:     RouteArtifactPrewriteActionWrite,
			rendered:   "secret rendered config",
		}},
	}

	evidence, err := ApplyRouteArtifactPublication(plan, writer)
	if err == nil {
		t.Fatal("ApplyRouteArtifactPublication returned nil error, want writer failure")
	}
	if evidence.Status != RouteArtifactPublicationApplyStatusError || evidence.Writes != 0 || evidence.BytesWritten != 0 {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	joined := strings.Join(append(evidence.Diagnostics, err.Error()), "\n")
	for _, leaked := range []string{root, "secret-route-group", "secret-host", "secret rendered config"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("support-safe evidence leaked %q: %s", leaked, joined)
		}
	}
}

// Traefik's file provider reads only the files directly inside
// providers.file.directory and does not descend into subdirectories. Publishing
// into a per-group subdirectory produced correct configuration that Traefik
// never read: routes were staged, verified as written, and silently never
// served. Every artifact must therefore land flat in the publication root.
func TestLocalRouteArtifactNamesAreFlatSoTraefikReadsThem(t *testing.T) {
	cases := []RouteArtifactPublicationWrite{
		{RouteGroup: "durable-env", Format: "traefik.dynamic.yaml"},
		{RouteGroup: "durable-env", RouteName: "service-a", Format: "traefik.dynamic.yaml"},
	}
	seen := make(map[string]struct{}, len(cases))
	for _, write := range cases {
		name, err := localRouteArtifactName(write)
		if err != nil {
			t.Fatalf("localRouteArtifactName(%+v): %v", write, err)
		}
		if strings.ContainsAny(name, `/\`) {
			t.Fatalf("artifact %q must be a flat filename; Traefik does not read subdirectories", name)
		}
		if !strings.HasPrefix(name, "durable-env.") {
			t.Fatalf("artifact %q must keep its route group as a filename prefix", name)
		}
		if !strings.HasSuffix(name, ".traefik.dynamic.yaml") {
			t.Fatalf("artifact %q must keep the provider's expected extension", name)
		}
		seen[name] = struct{}{}
	}
	// Distinct routes in one group must still not collide.
	if len(seen) != len(cases) {
		t.Fatalf("expected one distinct filename per write, got %v", seen)
	}
}

// Traefik loads a subdirectory artifact at its next startup even though it
// never watches it, so a file left behind by an older release would resurrect
// a stale router beside the current one and trip the verifier's conflicting-
// revision check. Publishing removes it.
func TestWriteRouteArtifactRemovesLegacyGroupDirectoryArtifact(t *testing.T) {
	root := t.TempDir()
	writer := &LocalRouteArtifactPublicationWriter{Root: root}
	write := RouteArtifactPublicationWrite{
		RouteGroup: "group-a", Format: "traefik.dynamic.yaml", Content: []byte("current"),
	}

	legacyDir := filepath.Join(root, "group-a")
	if err := os.MkdirAll(legacyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyDir, "traefik.dynamic.yaml")
	if err := os.WriteFile(legacyFile, []byte("stale"), 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := writer.WriteRouteArtifact(write); err != nil {
		t.Fatalf("WriteRouteArtifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "group-a.traefik.dynamic.yaml")); err != nil {
		t.Fatalf("current artifact missing: %v", err)
	}
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Fatalf("legacy subdirectory artifact survived: %v", err)
	}
}
