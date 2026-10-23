// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateBuildsScriptFreeDistributionKit(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	licensePath := filepath.Join(root, "LICENSE")
	if err := os.WriteFile(licensePath, []byte("Apache License 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const version = "1.2.3-alpha.1"
	checksums := make([]string, 0, len(platforms))
	for _, target := range platforms {
		name := "midnight_" + version + "_" + target.archiveSuffix
		path := filepath.Join(dist, name)
		if strings.HasSuffix(name, ".zip") {
			writeTestZip(t, path, target.binary)
		} else {
			writeTestTar(t, path, target.binary)
		}
		hash, err := fileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
		checksums = append(checksums, hash+"  "+name)
	}
	if err := os.WriteFile(filepath.Join(dist, "midnight_"+version+"_SHA256SUMS"), []byte(strings.Join(checksums, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(config{dist: dist, out: out, license: licensePath, version: version, repository: "packetloss404/midnight", npmPackage: "@deployatmidnight/cli"})
	if err != nil {
		t.Fatal(err)
	}
	formula := readTestFile(t, filepath.Join(out, "homebrew", "Formula", "midnight.rb"))
	for _, want := range []string{"class Midnight < Formula", `version "1.2.3-alpha.1"`, "darwin_arm64.tar.gz", "linux_amd64.tar.gz", "bash_completion.install"} {
		if !strings.Contains(formula, want) {
			t.Fatalf("formula missing %q\n%s", want, formula)
		}
	}
	var scoop map[string]any
	readTestJSON(t, filepath.Join(out, "scoop", "midnight.json"), &scoop)
	if scoop["bin"] != "midnight.exe" || scoop["version"] != version {
		t.Fatalf("scoop = %#v", scoop)
	}

	var meta npmPackageJSON
	readTestJSON(t, filepath.Join(out, "npm", "cli", "package.json"), &meta)
	if meta.Name != "@deployatmidnight/cli" || len(meta.OptionalDependencies) != len(platforms) {
		t.Fatalf("npm meta = %#v", meta)
	}
	rawMeta := readTestFile(t, filepath.Join(out, "npm", "cli", "package.json"))
	if strings.Contains(rawMeta, `"scripts"`) {
		t.Fatalf("npm package must not use lifecycle scripts: %s", rawMeta)
	}
	if strings.Contains(rawMeta, `"provenance"`) {
		t.Fatalf("npm package must let trusted publishing negotiate supported provenance: %s", rawMeta)
	}
	dispatcher := readTestFile(t, filepath.Join(out, "npm", "cli", "bin", "midnight.js"))
	if !strings.Contains(dispatcher, "@deployatmidnight/cli-linux-x64") || !strings.Contains(dispatcher, "spawnSync") {
		t.Fatalf("dispatcher = %s", dispatcher)
	}
	nativeBinary := readTestFile(t, filepath.Join(out, "npm", "linux-arm64", "midnight"))
	if nativeBinary != "binary:midnight" {
		t.Fatalf("native binary = %q", nativeBinary)
	}
	var manifest kitManifest
	readTestJSON(t, filepath.Join(out, "manifest.json"), &manifest)
	if len(manifest.SourceSHA256) != len(platforms) || len(manifest.NPMPackages) != len(platforms)+1 {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestGenerateRejectsChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	version := "1.2.3"
	if err := os.WriteFile(filepath.Join(root, "midnight_"+version+"_SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  midnight_1.2.3_darwin_amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "midnight_1.2.3_darwin_amd64.tar.gz"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(config{dist: root, out: filepath.Join(root, "out"), version: version, repository: "packetloss404/midnight", npmPackage: "midnight-test"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch", err)
	}
}

func TestGenerateRejectsDuplicateManifestEntry(t *testing.T) {
	root := t.TempDir()
	version := "1.2.3"
	line := strings.Repeat("0", 64) + "  midnight_1.2.3_darwin_amd64.tar.gz\n"
	if err := os.WriteFile(filepath.Join(root, "midnight_"+version+"_SHA256SUMS"), []byte(line+line), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(config{dist: root, out: filepath.Join(root, "out"), version: version, repository: "packetloss404/midnight", npmPackage: "midnight-test"})
	if err == nil || !strings.Contains(err.Error(), "duplicate archive name") {
		t.Fatalf("error = %v, want duplicate archive name", err)
	}
}

func writeTestTar(t *testing.T, path, binary string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	contents := []byte("binary:" + binary)
	if err := tarWriter.WriteHeader(&tar.Header{Name: binary, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestZip(t *testing.T, path, binary string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create(binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "binary:"+binary); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readTestJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw := []byte(readTestFile(t, path))
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("midnight"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := sha256.Sum256([]byte("midnight"))
	want := hex.EncodeToString(wantRaw[:])
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}
