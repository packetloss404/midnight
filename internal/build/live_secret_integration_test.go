// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveRailpackBuildKitSecretMount is opt-in because it requires the real
// pinned buildctl binary, a live BuildKit daemon, a prepared Railpack plan,
// and a reachable registry. The parity toolchain runner supplies those inputs.
func TestLiveRailpackBuildKitSecretMount(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_BUILDKIT_ADDR"))    // not-a-failure-code
	contextDir := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_BUILD_CONTEXT")) // not-a-failure-code
	planPath := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_RAILPACK_PLAN"))   // not-a-failure-code
	outputDir := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_BUILD_OUTPUT"))   // not-a-failure-code
	image := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_BUILD_IMAGE"))        // not-a-failure-code
	registry := strings.TrimSpace(os.Getenv("MIDNIGHT_LIVE_REGISTRY"))        // not-a-failure-code
	secret := os.Getenv("MIDNIGHT_LIVE_BUILD_SECRET")                         // not-a-failure-code
	if address == "" || contextDir == "" || planPath == "" || outputDir == "" || image == "" || registry == "" || secret == "" {
		t.Skip("live Railpack/BuildKit secret acceptance is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := (BuildctlRunner{
		BuildctlPath: envOrDefault("MIDNIGHT_LIVE_BUILDCTL", "buildctl"), // not-a-failure-code
		Address:      address,
	}).Build(ctx, BuildctlInvocation{
		ServiceID:      "live-secret-proof",
		ContextDir:     contextDir,
		Frontend:       "gateway.v0",
		FrontendSource: RequiredRailpackFrontend,
		PlanPath:       planPath,
		Image:          image,
		Registry:       registry,
		Insecure:       true,
		OutputDir:      outputDir,
		BuildArgs: map[string]string{
			"cache-key":                 "live-secret-proof",
			RailpackSecretsHashBuildArg: BuildSecretsHash([]BuildSecret{{ID: "PRIVATE_TOKEN", Value: []byte(secret)}}), // not-a-failure-code
		},
		Secrets: []BuildSecret{{ID: "PRIVATE_TOKEN", Value: []byte(secret)}}, // not-a-failure-code
	})
	if err != nil {
		t.Fatalf("live secret build: %v", err)
	}
	if !strings.HasPrefix(result.Digest, "sha256:") {
		t.Fatalf("live secret build digest = %q", result.Digest)
	}
	if err := filepath.Walk(outputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(content, []byte(secret)) {
			t.Fatalf("live build artifact %s leaked the secret", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
