package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ModeMetadata records an expected filesystem mode for an installed path.
type ModeMetadata struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// PackageContentEntry is a manifest package-content entry with optional SHA-256.
type PackageContentEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// Manifest records the stable install-time facts needed by ops and uninstall.
type Manifest struct {
	SchemaVersion         int                   `json:"schema_version"`
	PackageName           string                `json:"package_name"`
	PackageVersion        string                `json:"package_version"`
	InstalledAt           string                `json:"installed_at"`
	InstallRoot           string                `json:"install_root"`
	ConfigDir             string                `json:"config_dir"`
	StateDir              string                `json:"state_dir"`
	LogDir                string                `json:"log_dir"`
	User                  string                `json:"user"`
	Group                 string                `json:"group"`
	Services              []string              `json:"services"`
	Modes                 []ModeMetadata        `json:"modes,omitempty"`
	PackageContent        []string              `json:"package_content,omitempty"`
	PackageContentDigests []PackageContentEntry `json:"package_content_digests,omitempty"`
}

// PackageContentVerification reports manifest package paths absent from an
// explicit path list and any digest mismatches the verifier observed.
type PackageContentVerification struct {
	Missing        []string                       `json:"missing,omitempty"`
	DigestMismatch []PackageContentDigestMismatch `json:"digest_mismatch,omitempty"`
}

// PackageContentDigestMismatch describes a manifest entry whose actual digest did not match the expected value.
type PackageContentDigestMismatch struct {
	Path string `json:"path"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// ReadManifestFile reads and validates a manifest from an explicit path.
func ReadManifestFile(path string) (Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return Manifest{}, fmt.Errorf("manifest path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// WriteManifestFile validates and writes a manifest to an explicit path using mode.
func WriteManifestFile(path string, manifest Manifest, mode os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("manifest path is required")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}

// Validate rejects incomplete manifests and duplicate service names.
func (m Manifest) Validate() error {
	if m.SchemaVersion <= 0 {
		return fmt.Errorf("schema_version must be positive")
	}
	required := map[string]string{
		"package_name":    m.PackageName,
		"package_version": m.PackageVersion,
		"installed_at":    m.InstalledAt,
		"install_root":    m.InstallRoot,
		"config_dir":      m.ConfigDir,
		"state_dir":       m.StateDir,
		"log_dir":         m.LogDir,
		"user":            m.User,
		"group":           m.Group,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(m.Services) == 0 {
		return fmt.Errorf("services is required")
	}
	seen := make(map[string]struct{}, len(m.Services))
	for index, service := range m.Services {
		service = strings.TrimSpace(service)
		if service == "" {
			return fmt.Errorf("services[%d] is empty", index)
		}
		if _, ok := seen[service]; ok {
			return fmt.Errorf("duplicate service %q", service)
		}
		seen[service] = struct{}{}
	}
	for index, mode := range m.Modes {
		if strings.TrimSpace(mode.Path) == "" {
			return fmt.Errorf("modes[%d].path is required", index)
		}
		if strings.TrimSpace(mode.Mode) == "" {
			return fmt.Errorf("modes[%d].mode is required", index)
		}
	}
	seenContent := make(map[string]struct{}, len(m.PackageContent))
	for index, path := range m.PackageContent {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("package_content[%d] is empty", index)
		}
		if _, ok := seenContent[path]; ok {
			return fmt.Errorf("duplicate package content path %q", path)
		}
		seenContent[path] = struct{}{}
	}
	seenDigests := make(map[string]struct{}, len(m.PackageContentDigests))
	for index, entry := range m.PackageContentDigests {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			return fmt.Errorf("package_content_digests[%d].path is empty", index)
		}
		if _, ok := seenDigests[path]; ok {
			return fmt.Errorf("duplicate package content digest path %q", path)
		}
		seenDigests[path] = struct{}{}
		if err := validateSHA256Hex(entry.SHA256); err != nil {
			return fmt.Errorf("package_content_digests[%d]: %w", index, err)
		}
	}
	return nil
}

// VerifyPackageContent checks manifest package paths against an explicit path list.
func VerifyPackageContent(manifest Manifest, paths []string) (PackageContentVerification, error) {
	if err := manifest.Validate(); err != nil {
		return PackageContentVerification{}, err
	}
	present := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			present[path] = struct{}{}
		}
	}

	verification := PackageContentVerification{}
	for _, path := range manifest.PackageContent {
		if _, ok := present[path]; !ok {
			verification.Missing = append(verification.Missing, path)
		}
	}
	return verification, nil
}

// VerifyPackageContentFiles validates the manifest, checks each digested entry
// exists under baseDir, and verifies its SHA-256 matches the manifest digest.
// Entries without a digest are still checked for existence. baseDir must be an
// explicit path; the helper does not inspect the live install root by default.
func VerifyPackageContentFiles(manifest Manifest, baseDir string) (PackageContentVerification, error) {
	if err := manifest.Validate(); err != nil {
		return PackageContentVerification{}, err
	}
	if strings.TrimSpace(baseDir) == "" {
		return PackageContentVerification{}, errors.New("baseDir is required")
	}
	cleanBase, err := absoluteCleanPath(baseDir)
	if err != nil {
		return PackageContentVerification{}, err
	}
	info, err := os.Stat(cleanBase)
	if err != nil {
		return PackageContentVerification{}, fmt.Errorf("baseDir %q: %w", baseDir, err)
	}
	if !info.IsDir() {
		return PackageContentVerification{}, fmt.Errorf("baseDir %q must be a directory", baseDir)
	}

	verification := PackageContentVerification{}
	for _, entry := range manifest.PackageContentDigests {
		// Allow the entry path to be either an absolute manifest path (joined
		// using its base name under baseDir) or a relative path under baseDir.
		filePath := resolvePackagePath(cleanBase, entry.Path)
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			verification.Missing = append(verification.Missing, entry.Path)
			continue
		}
		if !fileInfo.Mode().IsRegular() {
			verification.Missing = append(verification.Missing, entry.Path)
			continue
		}
		if entry.SHA256 == "" {
			continue
		}
		actual, err := ComputeFileSHA256(filePath)
		if err != nil {
			return PackageContentVerification{}, err
		}
		if actual != strings.ToLower(entry.SHA256) {
			verification.DigestMismatch = append(verification.DigestMismatch, PackageContentDigestMismatch{
				Path: entry.Path,
				Want: strings.ToLower(entry.SHA256),
				Got:  actual,
			})
		}
	}
	return verification, nil
}

// ComputeFileSHA256 reads file at path and returns its lowercase hex SHA-256.
func ComputeFileSHA256(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q must be a regular file", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateSHA256Hex(value string) error {
	if value == "" {
		return nil
	}
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("sha256 %q must be 64 hex characters", value)
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("sha256 %q must be lowercase hex", value)
		}
	}
	return nil
}

func absoluteCleanPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func resolvePackagePath(baseDir, manifestPath string) string {
	if filepath.IsAbs(manifestPath) {
		// Map an absolute manifest path to baseDir + base name.
		return filepath.Join(baseDir, filepath.Base(manifestPath))
	}
	return filepath.Join(baseDir, filepath.FromSlash(manifestPath))
}
