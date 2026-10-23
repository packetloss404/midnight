// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMetadataDigest(t *testing.T) {
	realDigest := "sha256:" + strings.Repeat("a", 64)

	cases := []struct {
		name       string
		metadata   string
		wantDigest string
		wantErr    error
	}{
		{
			name: "containerimage.digest",
			metadata: `{
  "containerimage.config.digest": "sha256:` + strings.Repeat("b", 64) + `",
  "containerimage.digest": "` + realDigest + `",
  "image.name": "registry.midnight.local:5000/midnight/app:rev_abc123"
}`,
			wantDigest: realDigest,
		},
		{
			name: "containerimage.descriptor fallback",
			metadata: `{
  "containerimage.descriptor": {
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "digest": "` + realDigest + `",
    "size": 1234
  }
}`,
			wantDigest: realDigest,
		},
		{
			name:     "missing digest",
			metadata: `{"image.name":"registry/app:tag"}`,
			wantErr:  ErrMetadataDigestMissing,
		},
		{
			name:     "invalid digest",
			metadata: `{"containerimage.digest":"sha256:not-a-real-digest"}`,
			wantErr:  ErrInvalidImageDigest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMetadataDigest([]byte(tc.metadata))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseMetadataDigest() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMetadataDigest() unexpected err = %v", err)
			}
			if got != tc.wantDigest {
				t.Fatalf("ParseMetadataDigest() = %q, want %q", got, tc.wantDigest)
			}
		})
	}
}

func TestParseImageDigest(t *testing.T) {
	base := "sha256:" + strings.Repeat("b", 64)
	pushed := "sha256:" + strings.Repeat("c", 64)

	cases := []struct {
		name       string
		output     string
		wantDigest string
		wantErr    error
	}{
		{
			name:       "single digest",
			output:     "exporting to image\npushed digest " + pushed + "\n",
			wantDigest: pushed,
		},
		{
			name:       "repeated same digest is not ambiguous",
			output:     "resolve " + pushed + "\npushed " + pushed + "\n",
			wantDigest: pushed,
		},
		{
			// Decoy base-image digest appears BEFORE the real pushed digest.
			// A first-match scan would wrongly return the base image; the
			// collector must reject the ambiguity instead.
			name:    "decoy base image before real digest is ambiguous",
			output:  "FROM alpine@" + base + "\nexporting to image\npushed digest " + pushed + "\n",
			wantErr: ErrDigestAmbiguous,
		},
		{
			name:    "no digest",
			output:  "loading dockerfile\nexporting\n",
			wantErr: ErrDigestMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseImageDigest(tc.output)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseImageDigest() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImageDigest() unexpected err = %v", err)
			}
			if got != tc.wantDigest {
				t.Fatalf("ParseImageDigest() = %q, want %q", got, tc.wantDigest)
			}
		})
	}
}

func TestResolveImageDigestPrefersMetadataOverAmbiguousStdout(t *testing.T) {
	dir := t.TempDir()
	metadataPath := filepath.Join(dir, "buildctl.metadata.json")
	realDigest := "sha256:" + strings.Repeat("d", 64)
	base := "sha256:" + strings.Repeat("e", 64)
	if err := os.WriteFile(metadataPath, []byte(`{"containerimage.digest":"`+realDigest+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// stdout is intentionally ambiguous (two distinct digests). The metadata
	// file must win, proving the metadata path takes priority.
	stdout := strings.NewReader("FROM base@" + base + "\npushed digest " + realDigest + "\ndecoy " + base + "\n")
	got, err := resolveImageDigest(metadataPath, stdout)
	if err != nil {
		t.Fatalf("resolveImageDigest() err = %v", err)
	}
	if got != realDigest {
		t.Fatalf("resolveImageDigest() = %q, want metadata digest %q", got, realDigest)
	}
}

func TestResolveImageDigestFallsBackAndRejectsAmbiguity(t *testing.T) {
	dir := t.TempDir()
	// Metadata file absent -> must fall back to the stdout scan, which is
	// ambiguous and therefore rejected rather than silently guessing.
	missing := filepath.Join(dir, "does-not-exist.json")
	base := "sha256:" + strings.Repeat("1", 64)
	pushed := "sha256:" + strings.Repeat("2", 64)
	stdout := strings.NewReader("FROM base@" + base + "\npushed " + pushed + "\n")
	if _, err := resolveImageDigest(missing, stdout); !errors.Is(err, ErrDigestAmbiguous) {
		t.Fatalf("resolveImageDigest() err = %v, want ErrDigestAmbiguous", err)
	}
}
