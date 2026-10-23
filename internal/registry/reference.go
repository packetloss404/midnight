package registry

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	repositoryComponentPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	tagPattern                 = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	sha256DigestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	releaseTagPattern          = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9_.-]+)?$`)
)

// ImageReference is the validated subset of OCI image references Midnight needs
// for local build and registry planning. It intentionally avoids registry calls.
type ImageReference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

// ParseImageReference validates an OCI-ish image reference in the form
// [registry/]repository[:tag][@sha256:digest]. A tag or digest is required so
// callers do not accidentally depend on mutable implicit "latest" semantics.
func ParseImageReference(value string) (ImageReference, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ImageReference{}, fmt.Errorf("image reference is empty")
	}
	if strings.ContainsAny(value, " \t\n\r") || strings.Contains(value, "://") {
		return ImageReference{}, fmt.Errorf("image reference %q is invalid", value)
	}

	namePart, digest, hasDigest := strings.Cut(value, "@")
	if hasDigest {
		if strings.Contains(digest, "@") || !sha256DigestPattern.MatchString(digest) {
			return ImageReference{}, fmt.Errorf("image reference %q has invalid sha256 digest", value)
		}
	}

	tag := ""
	lastSlash := strings.LastIndex(namePart, "/")
	lastColon := strings.LastIndex(namePart, ":")
	if lastColon > lastSlash {
		tag = namePart[lastColon+1:]
		namePart = namePart[:lastColon]
		if !tagPattern.MatchString(tag) {
			return ImageReference{}, fmt.Errorf("image reference %q has invalid tag", value)
		}
	}
	if tag == "" && !hasDigest {
		return ImageReference{}, fmt.Errorf("image reference %q must include a tag or digest", value)
	}

	registry, repository := splitRegistry(namePart)
	if repository == "" || !validRepository(repository) {
		return ImageReference{}, fmt.Errorf("image reference %q has invalid repository", value)
	}
	if registry != "" && !validRegistry(registry) {
		return ImageReference{}, fmt.Errorf("image reference %q has invalid registry", value)
	}

	return ImageReference{Registry: registry, Repository: repository, Tag: tag, Digest: digest}, nil
}

// CheckImmutableReleaseTag enforces that release tags cannot move once present.
// Non-release tags are deliberately ignored by this policy checker.
func CheckImmutableReleaseTag(existing []ImageTag, candidate ImageTag) error {
	if !IsReleaseTag(candidate.Name) {
		return nil
	}
	if candidate.Repository == "" || candidate.Name == "" || candidate.Digest == "" {
		return fmt.Errorf("release tag candidate must include repository, tag, and digest")
	}

	for _, tag := range existing {
		if tag.Repository != candidate.Repository || tag.Name != candidate.Name {
			continue
		}
		if tag.Digest != candidate.Digest {
			return fmt.Errorf("release tag %s/%s is immutable: existing digest %s, candidate digest %s", candidate.Repository, candidate.Name, tag.Digest, candidate.Digest)
		}
		return nil
	}

	return nil
}

func IsReleaseTag(tag string) bool {
	return releaseTagPattern.MatchString(tag)
}

func splitRegistry(name string) (string, string) {
	first, rest, ok := strings.Cut(name, "/")
	if !ok {
		return "", name
	}
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first, rest
	}
	return "", name
}

func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	for _, part := range parts {
		if !repositoryComponentPattern.MatchString(part) {
			return false
		}
	}
	return true
}

func validRegistry(registry string) bool {
	if strings.HasPrefix(registry, ".") || strings.HasSuffix(registry, ".") || strings.Contains(registry, "..") {
		return false
	}
	host, port, hasPort := strings.Cut(registry, ":")
	if host == "" {
		return false
	}
	if hasPort {
		if port == "" {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
