// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Digest resolution errors. These are typed so callers (and tests) can
// distinguish a missing digest from an ambiguous one — the latter is the
// dangerous case where buildctl output mentioned two DISTINCT image digests
// (e.g. a base/cache layer plus the pushed image) and silently picking the
// first would record the wrong artifact.
var (
	// ErrMetadataDigestMissing means the buildctl --metadata-file JSON did not
	// carry a containerimage.digest (or descriptor) entry.
	ErrMetadataDigestMissing = errors.New("build metadata image digest is missing")
	// ErrInvalidImageDigest means a value was present but did not match the
	// canonical sha256:<64 hex> form.
	ErrInvalidImageDigest = errors.New("invalid image digest")
	// ErrDigestMissing means no sha256 digest could be located in the fallback
	// stdout scan.
	ErrDigestMissing = errors.New("image digest is missing")
	// ErrDigestAmbiguous means the fallback stdout scan found two or more
	// DISTINCT sha256 digests and cannot safely choose one.
	ErrDigestAmbiguous = errors.New("image digest is ambiguous")
)

var (
	// sha256DigestPattern validates a fully-qualified image digest.
	sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// sha256DigestToken finds candidate digest tokens in free-form log output.
	// It greedily captures the maximal hex run so that a longer-than-64 hex
	// string is rejected by the strict validator rather than matched as a
	// truncated 64-char prefix.
	sha256DigestToken = regexp.MustCompile(`sha256:[a-f0-9]{64,}`)
)

// ParseMetadataDigest extracts the pushed image digest from the JSON written by
// buildctl's --metadata-file flag. The canonical key is
// "containerimage.digest"; some exporter configurations only populate
// "containerimage.descriptor" (an OCI descriptor object whose "digest" field
// carries the same value), which is accepted as a fallback. The returned digest
// is validated against sha256DigestPattern.
func ParseMetadataDigest(metadata []byte) (string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &document); err != nil {
		return "", fmt.Errorf("parse build metadata: %w", err)
	}

	if raw, ok := document["containerimage.digest"]; ok {
		var digest string
		if err := json.Unmarshal(raw, &digest); err != nil {
			return "", fmt.Errorf("parse containerimage.digest: %w", err)
		}
		return validateImageDigest(digest)
	}

	if raw, ok := document["containerimage.descriptor"]; ok {
		var descriptor struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(raw, &descriptor); err != nil {
			return "", fmt.Errorf("parse containerimage.descriptor: %w", err)
		}
		if descriptor.Digest != "" {
			return validateImageDigest(descriptor.Digest)
		}
	}

	return "", ErrMetadataDigestMissing
}

// ParseImageDigest scans free-form buildctl stdout/stderr output for image
// digests. Unlike a first-match scan, it COLLECTS every distinct
// sha256:<64 hex> token, deduplicates, and:
//   - returns ErrDigestMissing when none are present,
//   - returns the single digest when exactly one distinct value appears,
//   - returns ErrDigestAmbiguous when two or more DISTINCT digests appear so a
//     base-image/cache digest is never silently mistaken for the pushed image.
func ParseImageDigest(output string) (string, error) {
	unique := make(map[string]struct{})
	order := make([]string, 0, 2)
	for _, token := range sha256DigestToken.FindAllString(output, -1) {
		if !sha256DigestPattern.MatchString(token) {
			// Longer-than-64 hex run: not a clean digest, ignore it.
			continue
		}
		if _, seen := unique[token]; seen {
			continue
		}
		unique[token] = struct{}{}
		order = append(order, token)
	}

	switch len(order) {
	case 0:
		return "", ErrDigestMissing
	case 1:
		return order[0], nil
	default:
		return "", fmt.Errorf("%w: %v", ErrDigestAmbiguous, order)
	}
}

func validateImageDigest(digest string) (string, error) {
	if !sha256DigestPattern.MatchString(digest) {
		return "", fmt.Errorf("%w: %q", ErrInvalidImageDigest, digest)
	}
	return digest, nil
}
