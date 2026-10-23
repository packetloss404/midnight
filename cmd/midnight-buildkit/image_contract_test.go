package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/build"
)

func TestBuildWorkerImagePinsRequiredToolchain(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "oci", "build-worker", "Dockerfile"))
	if err != nil {
		t.Fatalf("read build-worker Dockerfile: %v", err)
	}
	text := string(body)
	required := []string{
		"ARG BUILDKIT_VERSION=" + build.RequiredBuildKitVersion,
		"ARG RAILPACK_VERSION=" + build.RequiredRailpackVersion,
		"2e84057b941488c54575aee57015681f08c71042bd20c739b23879dfad3f2449",
		"40caf32b10cb0766f7440a53fea9cfe15c8116d64f9365c53ff417958a136332",
		"e8bffc181c13e68c1c78ec618f1418e60b1602ec548f3ec1054996394ce0f06a",
		"d4505d0f5f9f48c6350ad349111e10a1f5ba16c118834c85c6d0ecd630b25c34",
		"sha256sum -c -",
		"ca-certificates git",
		"COPY --from=tools /out-buildctl /usr/local/bin/buildctl",
		"COPY --from=tools /out-railpack /usr/local/bin/railpack",
		"USER midnight:midnight",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("build-worker Dockerfile missing %q", want)
		}
	}
}
