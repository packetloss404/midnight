// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"sort"
	"strings"
)

// OfficialImage is one container image that ships inside this Midnight
// release, pinned by exact sha256 digest at build time.
//
// The digest is compiled into the binary from the template definitions in this
// package, so it *is* the integrity guarantee: an operator cannot make it
// stronger by re-declaring it in a runtime allowlist, and an attacker who
// could change it already controls the release artifact. Sources names the
// templates that ship it, so `midnight doctor`-style surfaces can tell an
// operator why a digest is trusted rather than just that it is.
type OfficialImage struct {
	// Reference is the full `repo:tag@sha256:<hex>` string the runtime pulls.
	Reference string
	// Digest is the `sha256:<hex>` half of Reference.
	Digest string
	// Sources are the official template identifiers that ship this image,
	// sorted and de-duplicated.
	Sources []string
}

// OfficialImageReferences returns every digest-pinned image reference shipped
// by this release, sorted and de-duplicated.
//
// Deploy workers use it to trust the product's own digests by construction:
// the four official database templates are unusable on a default install if
// the operator has to copy these digests into
// MIDNIGHT_WORKER_TRUSTED_IMAGES by hand, and requiring that adds no security
// because the same binary already decides which digest to deploy.
func OfficialImageReferences() []string {
	images := OfficialImages()
	out := make([]string, 0, len(images))
	for _, image := range images {
		out = append(out, image.Reference)
	}
	return out
}

// OfficialImages returns the digest-pinned images shipped by this release
// together with the templates that ship them.
func OfficialImages() []OfficialImage {
	sources := map[string]map[string]struct{}{}
	record := func(reference, source string) {
		reference = strings.TrimSpace(reference)
		source = strings.TrimSpace(source)
		if reference == "" || !strings.Contains(reference, "@sha256:") {
			// Only immutable references are trusted by construction; a
			// template that somehow lost its pin must not become an
			// unpinned trust entry.
			return
		}
		if _, ok := sources[reference]; !ok {
			sources[reference] = map[string]struct{}{}
		}
		if source != "" {
			sources[reference][source] = struct{}{}
		}
	}

	for _, template := range OfficialDatabaseTemplates() {
		record(template.Image, template.ID)
	}
	for _, template := range Builtins() {
		for _, service := range template.Services {
			record(service.Image, template.Slug)
		}
	}

	out := make([]OfficialImage, 0, len(sources))
	for reference, names := range sources {
		_, digest, _ := strings.Cut(reference, "@")
		image := OfficialImage{
			Reference: reference,
			Digest:    digest,
			Sources:   make([]string, 0, len(names)),
		}
		for name := range names {
			image.Sources = append(image.Sources, name)
		}
		sort.Strings(image.Sources)
		out = append(out, image)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reference < out[j].Reference })
	return out
}
