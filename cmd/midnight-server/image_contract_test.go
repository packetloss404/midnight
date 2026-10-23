// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestServerImageSeedsWritableSharedUploadMountpoint(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "oci", "server", "Dockerfile"))
	if err != nil {
		t.Fatalf("read server Dockerfile: %v", err)
	}
	text := string(body)
	for _, required := range []string{
		"install -d -m 0750 /out/uploads",
		"COPY --from=build --chown=65532:65532 /out/uploads /var/lib/midnight/uploads",
		"USER nonroot:nonroot",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server Dockerfile missing %q", required)
		}
	}
}

func TestLocalComposeUsesOneSourceArtifactIdentity(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.local.yml"))
	if err != nil {
		t.Fatalf("read local compose: %v", err)
	}
	var document struct {
		Services map[string]struct {
			User      string         `yaml:"user"`
			DependsOn map[string]any `yaml:"depends_on"`
			Volumes   []any          `yaml:"volumes"`
			Command   any            `yaml:"command"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode local compose: %v", err)
	}
	apiService, ok := document.Services["api"]
	if !ok {
		t.Fatal("local compose has no api service")
	}
	if apiService.User != "65532:65532" {
		t.Fatalf("local API identity=%q, want shared source-artifact UID/GID", apiService.User)
	}
	for _, serviceName := range []string{"api", "build-worker"} {
		service, ok := document.Services[serviceName]
		if !ok {
			t.Fatalf("local compose has no %s service", serviceName)
		}
		if _, ok := service.DependsOn["local-source-init"]; !ok {
			t.Fatalf("%s does not wait for source-volume ownership initialization", serviceName)
		}
		if !composeMountsSourceArtifactVolume(service.Volumes) {
			t.Fatalf("%s does not mount the shared source-artifact volume", serviceName)
		}
	}
	initializer, ok := document.Services["local-source-init"]
	if !ok {
		t.Fatal("local compose has no source-volume initializer")
	}
	encodedCommand, err := yaml.Marshal(initializer.Command)
	if err != nil {
		t.Fatal(err)
	}
	command := string(encodedCommand)
	for _, required := range []string{
		"chown -R 65532:65532",
		"/var/lib/midnight/uploads",
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("source-volume initializer missing %q", required)
		}
	}
}

func composeMountsSourceArtifactVolume(volumes []any) bool {
	for _, raw := range volumes {
		if value, ok := raw.(string); ok &&
			value == "midnight-up-uploads:/var/lib/midnight/uploads" {
			return true
		}
	}
	return false
}
