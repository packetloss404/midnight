package version

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCurrentReturnsBuildMetadata(t *testing.T) {
	got := Current()
	if got.Version != Version || got.Commit != Commit || got.Date != Date {
		t.Fatalf("Current() = %#v, want package build variables", got)
	}
	if !got.IsDevelopment() {
		t.Fatalf("default metadata %#v must be marked as development", got)
	}
}

func TestMetadataIsDevelopment(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "", want: true},
		{version: "(devel)", want: true},
		{version: "0.0.0-dev", want: true},
		{version: "1.2.3-dev.4", want: true},
		{version: "1.2.3", want: false},
		{version: "1.2.3-alpha.1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := (Metadata{Version: tt.version}).IsDevelopment(); got != tt.want {
				t.Fatalf("Metadata{Version: %q}.IsDevelopment() = %t, want %t", tt.version, got, tt.want)
			}
		})
	}
}

func TestLinkerInjectsCompleteBuildMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping nested build in short mode")
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	binary := filepath.Join(t.TempDir(), "version-probe")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	const (
		wantVersion = "1.2.3-rc.1"
		wantCommit  = "0123456789ab"
		wantDate    = "2026-08-12T12:34:56Z"
	)
	ldflags := "-X github.com/midnight/midnight/internal/version.Version=" + wantVersion +
		" -X github.com/midnight/midnight/internal/version.Commit=" + wantCommit +
		" -X github.com/midnight/midnight/internal/version.Date=" + wantDate
	build := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", binary, "./internal/version/testdata/buildinfo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linker probe: %v\n%s", err, output)
	}

	output, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("run linker probe: %v\n%s", err, output)
	}
	var got Metadata
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode linker probe output %q: %v", output, err)
	}
	if got.Version != wantVersion || got.Commit != wantCommit || got.Date != wantDate {
		t.Fatalf("linker-injected metadata = %#v", got)
	}
	if got.IsDevelopment() {
		t.Fatalf("release metadata %#v must not be marked as development", got)
	}
}

func TestCompatIncludesVersionAndSchema(t *testing.T) {
	got := Compat()
	if got.ServerVersion != Version || got.APIVersion != APIVersion || got.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected compat payload: %#v", got)
	}
	if got.BuildCommit != Commit || got.BuildDate != Date {
		t.Fatalf("compat build metadata = (%q, %q), want (%q, %q)", got.BuildCommit, got.BuildDate, Commit, Date)
	}
	if len(got.Components) == 0 {
		t.Fatal("expected component compatibility rows")
	}
	want := map[string]bool{"server": true, "agent": true, "worker": true, "scheduler": true}
	for _, component := range got.Components {
		delete(want, component.Component)
		if component.Minimum == "" {
			t.Fatalf("component %q missing minimum version", component.Component)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing compatibility rows: %#v", want)
	}
}

func TestCompatIncludesPhaseThreeV15Matrix(t *testing.T) {
	got := Compat()
	want := map[string]string{
		"stateless-multihost-scheduling": "scheduler",
		"agent-canary-rollout":           "agent",
	}
	for _, row := range got.Matrix {
		component, ok := want[row.Capability]
		if !ok {
			continue
		}
		if row.Component != component || row.Minimum == "" || row.Status != "planning-only" {
			t.Fatalf("matrix row %q = %#v", row.Capability, row)
		}
		delete(want, row.Capability)
	}
	if len(want) != 0 {
		t.Fatalf("missing compatibility matrix rows: %#v", want)
	}
}

func TestSchemaDTO(t *testing.T) {
	got := Schema()
	if got.SchemaVersion != SchemaVersion || got.APIVersion != APIVersion {
		t.Fatalf("Schema() = %#v", got)
	}
}
