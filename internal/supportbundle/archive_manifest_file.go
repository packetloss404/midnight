package supportbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteArchiveManifestJSONFile validates and writes deterministic archive manifest JSON.
func WriteArchiveManifestJSONFile(path string, manifest Manifest) error {
	if err := validateArchiveManifestJSONPath(path); err != nil {
		return err
	}
	data, err := MarshalManifestJSON(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadArchiveManifestJSONFile reads, strictly decodes, and validates an archive manifest JSON file.
func ReadArchiveManifestJSONFile(path string) (Manifest, error) {
	if err := validateArchiveManifestJSONPath(path); err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("support bundle archive manifest must contain a single JSON document")
		}
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("support bundle archive manifest: %w", err)
	}
	return manifest, nil
}

func validateArchiveManifestJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("support bundle archive manifest path is required")
	}
	if filepath.Clean(path) != path {
		return errors.New("support bundle archive manifest path must be clean")
	}
	if filepath.Base(path) == ".json" || filepath.Ext(path) != ".json" {
		return errors.New("support bundle archive manifest path must end with .json")
	}
	return nil
}
