// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"sort"
	"strings"
	"testing"
)

func TestOfficialImageReferencesCoverEveryShippedTemplate(t *testing.T) {
	references := OfficialImageReferences()
	if len(references) == 0 {
		t.Fatal("no official image references were exported")
	}
	if !sort.StringsAreSorted(references) {
		t.Fatalf("references must be sorted, got %v", references)
	}
	index := make(map[string]bool, len(references))
	for _, reference := range references {
		if index[reference] {
			t.Fatalf("reference %q is duplicated", reference)
		}
		index[reference] = true
	}

	for _, template := range OfficialDatabaseTemplates() {
		if !index[template.Image] {
			t.Fatalf("database template %s image %q is not exported as an official image", template.ID, template.Image)
		}
	}
	for _, template := range Builtins() {
		for _, service := range template.Services {
			if !index[service.Image] {
				t.Fatalf("builtin template %s service %s image %q is not exported as an official image", template.Slug, service.Slug, service.Image)
			}
		}
	}
}

func TestOfficialImagesArePinnedAndAttributed(t *testing.T) {
	for _, image := range OfficialImages() {
		if !strings.Contains(image.Reference, "@"+image.Digest) {
			t.Fatalf("image %+v: digest is not part of the reference", image)
		}
		if !digestPattern.MatchString(image.Digest) {
			t.Fatalf("image %q digest %q is not a sha256 pin", image.Reference, image.Digest)
		}
		if len(image.Sources) == 0 {
			t.Fatalf("image %q has no template attribution; an operator could not tell why it is trusted", image.Reference)
		}
		if !sort.StringsAreSorted(image.Sources) {
			t.Fatalf("image %q sources must be sorted, got %v", image.Reference, image.Sources)
		}
	}
}

func TestOfficialImagesDeduplicateSharedDigests(t *testing.T) {
	// redis-7-cache and redis-7-durable ship one image; it must appear once,
	// attributed to both templates.
	var redis *OfficialImage
	for _, image := range OfficialImages() {
		if strings.HasPrefix(image.Reference, "redis:") {
			candidate := image
			redis = &candidate
		}
	}
	if redis == nil {
		t.Fatal("the official redis image is missing")
	}
	if len(redis.Sources) < 2 {
		t.Fatalf("redis image sources = %v, want both the cache and durable templates", redis.Sources)
	}
}
