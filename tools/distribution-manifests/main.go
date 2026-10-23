// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Command distribution-manifests turns verified GoReleaser archives into a
// publication kit for npm, Homebrew, and Scoop. It never publishes anything;
// external namespace ownership and repository credentials remain release
// operator responsibilities.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxBinaryBytes = 256 << 20

var (
	versionRE    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	repositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	npmPackageRE = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
)

type config struct {
	dist       string
	out        string
	license    string
	version    string
	repository string
	npmPackage string
}

type platform struct {
	archiveSuffix string
	npmOS         string
	npmCPU        string
	packageSuffix string
	binary        string
}

var platforms = []platform{
	{archiveSuffix: "darwin_amd64.tar.gz", npmOS: "darwin", npmCPU: "x64", packageSuffix: "darwin-x64", binary: "midnight"},
	{archiveSuffix: "darwin_arm64.tar.gz", npmOS: "darwin", npmCPU: "arm64", packageSuffix: "darwin-arm64", binary: "midnight"},
	{archiveSuffix: "linux_amd64.tar.gz", npmOS: "linux", npmCPU: "x64", packageSuffix: "linux-x64", binary: "midnight"},
	{archiveSuffix: "linux_arm64.tar.gz", npmOS: "linux", npmCPU: "arm64", packageSuffix: "linux-arm64", binary: "midnight"},
	{archiveSuffix: "windows_amd64.zip", npmOS: "win32", npmCPU: "x64", packageSuffix: "win32-x64", binary: "midnight.exe"},
}

type sourceArchive struct {
	platform platform
	name     string
	hash     string
	path     string
}

type npmPackageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Description          string            `json:"description"`
	License              string            `json:"license"`
	Repository           npmRepository     `json:"repository"`
	Homepage             string            `json:"homepage"`
	Engines              map[string]string `json:"engines,omitempty"`
	OS                   []string          `json:"os,omitempty"`
	CPU                  []string          `json:"cpu,omitempty"`
	Bin                  map[string]string `json:"bin,omitempty"`
	Files                []string          `json:"files"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
	PublishConfig        npmPublishConfig  `json:"publishConfig"`
}

type npmRepository struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type npmPublishConfig struct {
	Access string `json:"access"`
}

type kitManifest struct {
	Version      string            `json:"version"`
	Repository   string            `json:"repository"`
	NPMPackage   string            `json:"npm_package"`
	SourceSHA256 map[string]string `json:"source_sha256"`
	NPMPackages  []string          `json:"npm_packages"`
	HomebrewFile string            `json:"homebrew_file"`
	ScoopFile    string            `json:"scoop_file"`
}

func main() {
	var cfg config
	flag.StringVar(&cfg.dist, "dist", "dist", "GoReleaser output directory")
	flag.StringVar(&cfg.out, "out", "artifacts/release/distribution", "publication kit output directory")
	flag.StringVar(&cfg.license, "license", "LICENSE", "Apache-2.0 license file to embed")
	flag.StringVar(&cfg.version, "version", "", "release version without a leading v")
	flag.StringVar(&cfg.repository, "repository", "", "GitHub source repository as owner/name")
	flag.StringVar(&cfg.npmPackage, "npm-package", "", "approved npm package name")
	flag.Parse()
	if err := generate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "distribution-manifests:", err)
		os.Exit(1)
	}
}

func generate(cfg config) error {
	if strings.TrimSpace(cfg.license) == "" {
		cfg.license = "LICENSE"
	}
	cfg.dist = filepath.Clean(strings.TrimSpace(cfg.dist))
	cfg.out = filepath.Clean(strings.TrimSpace(cfg.out))
	cfg.license = filepath.Clean(strings.TrimSpace(cfg.license))
	cfg.version = strings.TrimPrefix(strings.TrimSpace(cfg.version), "v")
	cfg.repository = strings.TrimSpace(cfg.repository)
	cfg.npmPackage = strings.TrimSpace(cfg.npmPackage)
	if !versionRE.MatchString(cfg.version) {
		return fmt.Errorf("invalid release version %q", cfg.version)
	}
	if !repositoryRE.MatchString(cfg.repository) {
		return fmt.Errorf("invalid GitHub repository %q", cfg.repository)
	}
	if !npmPackageRE.MatchString(cfg.npmPackage) {
		return fmt.Errorf("invalid or missing npm package name %q", cfg.npmPackage)
	}
	if cfg.out == "." || filepath.IsAbs(cfg.out) && filepath.Clean(cfg.out) == string(filepath.Separator) {
		return errors.New("output directory must not be the working directory or filesystem root")
	}

	archives, err := loadArchives(cfg)
	if err != nil {
		return err
	}
	for _, directory := range []string{
		filepath.Join(cfg.out, "homebrew", "Formula"),
		filepath.Join(cfg.out, "scoop"),
		filepath.Join(cfg.out, "npm"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}

	if err := writeFile(filepath.Join(cfg.out, "homebrew", "Formula", "midnight.rb"), []byte(renderHomebrew(cfg, archives)), 0o644); err != nil {
		return err
	}
	scoop, err := renderScoop(cfg, archives)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(cfg.out, "scoop", "midnight.json"), scoop); err != nil {
		return err
	}
	npmPackages, err := writeNPMPackages(cfg, archives)
	if err != nil {
		return err
	}

	hashes := make(map[string]string, len(archives))
	for _, archive := range archives {
		hashes[archive.name] = archive.hash
	}
	manifest := kitManifest{
		Version: cfg.version, Repository: cfg.repository, NPMPackage: cfg.npmPackage,
		SourceSHA256: hashes, NPMPackages: npmPackages,
		HomebrewFile: "homebrew/Formula/midnight.rb", ScoopFile: "scoop/midnight.json",
	}
	return writeJSON(filepath.Join(cfg.out, "manifest.json"), manifest)
}

func loadArchives(cfg config) ([]sourceArchive, error) {
	manifestName := "midnight_" + cfg.version + "_SHA256SUMS"
	raw, err := os.ReadFile(filepath.Join(cfg.dist, manifestName))
	if err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	manifestHashes := map[string]string{}
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid checksum manifest line %d", lineNumber+1)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("invalid checksum on manifest line %d", lineNumber+1)
		}
		if filepath.Base(fields[1]) != fields[1] {
			return nil, fmt.Errorf("invalid archive name on manifest line %d", lineNumber+1)
		}
		if _, exists := manifestHashes[fields[1]]; exists {
			return nil, fmt.Errorf("duplicate archive name on manifest line %d", lineNumber+1)
		}
		manifestHashes[fields[1]] = strings.ToLower(fields[0])
	}

	archives := make([]sourceArchive, 0, len(platforms))
	for _, target := range platforms {
		name := "midnight_" + cfg.version + "_" + target.archiveSuffix
		expected, ok := manifestHashes[name]
		if !ok {
			return nil, fmt.Errorf("checksum manifest is missing %s", name)
		}
		path := filepath.Join(cfg.dist, name)
		actual, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, fmt.Errorf("checksum mismatch for %s", name)
		}
		archives = append(archives, sourceArchive{platform: target, name: name, hash: expected, path: path})
	}
	if len(manifestHashes) != len(archives) {
		return nil, fmt.Errorf("checksum manifest contains %d entries; expected %d", len(manifestHashes), len(archives))
	}
	return archives, nil
}

func renderHomebrew(cfg config, archives []sourceArchive) string {
	bySuffix := archiveMap(archives)
	asset := func(suffix string) sourceArchive { return bySuffix[suffix] }
	url := func(value sourceArchive) string {
		return fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", cfg.repository, cfg.version, value.name)
	}
	return fmt.Sprintf(`class Midnight < Formula
  desc "Self-hosted application deployment platform CLI"
  homepage "https://deployatmidnight.com"
  version %q
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.arm?
      url %q
      sha256 %q
    else
      url %q
      sha256 %q
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url %q
      sha256 %q
    else
      url %q
      sha256 %q
    end
  end

  def install
    bin.install "midnight"
    bash_completion.install "completions/midnight.bash" => "midnight"
    zsh_completion.install "completions/_midnight" => "_midnight"
    fish_completion.install "completions/midnight.fish"
  end

  test do
    assert_equal version.to_s, shell_output("#{bin}/midnight version").strip
  end
end
`, cfg.version,
		url(asset("darwin_arm64.tar.gz")), asset("darwin_arm64.tar.gz").hash,
		url(asset("darwin_amd64.tar.gz")), asset("darwin_amd64.tar.gz").hash,
		url(asset("linux_arm64.tar.gz")), asset("linux_arm64.tar.gz").hash,
		url(asset("linux_amd64.tar.gz")), asset("linux_amd64.tar.gz").hash,
	)
}

func renderScoop(cfg config, archives []sourceArchive) (map[string]any, error) {
	archive, ok := archiveMap(archives)["windows_amd64.zip"]
	if !ok {
		return nil, errors.New("Windows amd64 archive is unavailable")
	}
	return map[string]any{
		"version":     cfg.version,
		"description": "Self-hosted application deployment platform CLI",
		"homepage":    "https://deployatmidnight.com",
		"license":     "Apache-2.0",
		"architecture": map[string]any{
			"64bit": map[string]any{
				"url":  fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", cfg.repository, cfg.version, archive.name),
				"hash": archive.hash,
			},
		},
		"bin": "midnight.exe",
	}, nil
}

func writeNPMPackages(cfg config, archives []sourceArchive) ([]string, error) {
	npmRoot := filepath.Join(cfg.out, "npm")
	common := npmPackageJSON{
		Version: cfg.version, Description: "Self-hosted application deployment platform CLI",
		License:       "Apache-2.0",
		Repository:    npmRepository{Type: "git", URL: "git+https://github.com/" + cfg.repository + ".git"},
		Homepage:      "https://deployatmidnight.com",
		PublishConfig: npmPublishConfig{Access: "public"},
	}
	optional := map[string]string{}
	packageNames := make([]string, 0, len(archives)+1)
	for _, archive := range archives {
		packageName := platformPackageName(cfg.npmPackage, archive.platform.packageSuffix)
		packageNames = append(packageNames, packageName)
		optional[packageName] = cfg.version
		directory := filepath.Join(npmRoot, archive.platform.packageSuffix)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
		binary, err := readArchiveBinary(archive)
		if err != nil {
			return nil, err
		}
		mode := os.FileMode(0o755)
		if archive.platform.npmOS == "win32" {
			mode = 0o644
		}
		if err := writeFile(filepath.Join(directory, archive.platform.binary), binary, mode); err != nil {
			return nil, err
		}
		metadata := common
		metadata.Name = packageName
		metadata.OS = []string{archive.platform.npmOS}
		metadata.CPU = []string{archive.platform.npmCPU}
		metadata.Files = []string{archive.platform.binary, "LICENSE", "README.md"}
		if err := writeJSON(filepath.Join(directory, "package.json"), metadata); err != nil {
			return nil, err
		}
		if err := writePackageSupportFiles(directory, cfg, packageName, true); err != nil {
			return nil, err
		}
	}

	mainDirectory := filepath.Join(npmRoot, "cli")
	if err := os.MkdirAll(filepath.Join(mainDirectory, "bin"), 0o755); err != nil {
		return nil, err
	}
	metadata := common
	metadata.Name = cfg.npmPackage
	metadata.Engines = map[string]string{"node": ">=18"}
	metadata.Bin = map[string]string{"midnight": "bin/midnight.js"}
	metadata.Files = []string{"bin/midnight.js", "LICENSE", "README.md"}
	metadata.OptionalDependencies = optional
	if err := writeJSON(filepath.Join(mainDirectory, "package.json"), metadata); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(mainDirectory, "bin", "midnight.js"), []byte(renderNPMDispatcher(cfg.npmPackage)), 0o755); err != nil {
		return nil, err
	}
	if err := writePackageSupportFiles(mainDirectory, cfg, cfg.npmPackage, false); err != nil {
		return nil, err
	}
	packageNames = append(packageNames, cfg.npmPackage)
	return packageNames, nil
}

func renderNPMDispatcher(packageName string) string {
	entries := make([]string, 0, len(platforms))
	for _, target := range platforms {
		name := platformPackageName(packageName, target.packageSuffix)
		entries = append(entries, fmt.Sprintf("  %q: [%q, %q]", target.npmOS+"-"+target.npmCPU, name, target.binary))
	}
	sort.Strings(entries)
	return fmt.Sprintf(`#!/usr/bin/env node
'use strict';

const path = require('node:path');
const { spawnSync } = require('node:child_process');

const targets = {
%s
};
const key = process.platform + '-' + process.arch;
const target = targets[key];
if (!target) {
  console.error('midnight: unsupported platform ' + key);
  process.exit(1);
}

let packagePath;
try {
  packagePath = require.resolve(target[0] + '/package.json');
} catch (error) {
  console.error('midnight: native package ' + target[0] + ' is unavailable for ' + key);
  process.exit(1);
}
const binary = path.join(path.dirname(packagePath), target[1]);
const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error('midnight: ' + result.error.message);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status === null ? 1 : result.status);
`, strings.Join(entries, ",\n"))
}

func writePackageSupportFiles(directory string, cfg config, packageName string, native bool) error {
	license, err := os.ReadFile(cfg.license)
	if err != nil {
		return fmt.Errorf("read LICENSE: %w", err)
	}
	if err := writeFile(filepath.Join(directory, "LICENSE"), license, 0o644); err != nil {
		return err
	}
	detail := fmt.Sprintf("# %s\n\nMidnight CLI version %s. Source and verification instructions: https://github.com/%s/releases/tag/v%s\n", packageName, cfg.version, cfg.repository, cfg.version)
	if native {
		detail += "\nThis platform package is installed automatically by the main Midnight CLI npm package.\n"
	}
	return writeFile(filepath.Join(directory, "README.md"), []byte(detail), 0o644)
}

func readArchiveBinary(archive sourceArchive) ([]byte, error) {
	if strings.HasSuffix(archive.name, ".zip") {
		reader, err := zip.OpenReader(archive.path)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		for _, file := range reader.File {
			if file.Name != archive.platform.binary {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer opened.Close()
			return readBoundedBinary(opened, archive.name)
		}
		return nil, fmt.Errorf("%s is missing %s", archive.name, archive.platform.binary)
	}
	file, err := os.Open(archive.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name == archive.platform.binary && header.Typeflag == tar.TypeReg {
			if header.Size > maxBinaryBytes {
				return nil, fmt.Errorf("%s binary exceeds size limit", archive.name)
			}
			return readBoundedBinary(tarReader, archive.name)
		}
	}
	return nil, fmt.Errorf("%s is missing %s", archive.name, archive.platform.binary)
}

func readBoundedBinary(reader io.Reader, archiveName string) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maxBinaryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxBinaryBytes {
		return nil, fmt.Errorf("%s binary exceeds size limit", archiveName)
	}
	return contents, nil
}

func platformPackageName(base, suffix string) string {
	if strings.HasPrefix(base, "@") {
		scope, name, _ := strings.Cut(base, "/")
		return scope + "/" + name + "-" + suffix
	}
	return base + "-" + suffix
}

func archiveMap(archives []sourceArchive) map[string]sourceArchive {
	result := make(map[string]sourceArchive, len(archives))
	for _, archive := range archives {
		result[archive.platform.archiveSuffix] = archive
	}
	return result
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'), 0o644)
}

func writeFile(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
