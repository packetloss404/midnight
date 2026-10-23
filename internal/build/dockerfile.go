package build

import "strings"

// DockerfileFromDigest records an image referenced by a FROM line. Digest is
// populated only when the image is pinned with a sha256 digest.
type DockerfileFromDigest struct {
	Line   int
	Image  string
	Digest string
	Stage  string
}

// ParseDockerfileFroms extracts every FROM reference using a deliberately small
// line parser. It does not attempt full Dockerfile parsing.
func ParseDockerfileFroms(contents string) []DockerfileFromDigest {
	lines := strings.Split(contents, "\n")
	records := make([]DockerfileFromDigest, 0)
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}

		imageIndex := 1
		for imageIndex < len(fields) && strings.HasPrefix(fields[imageIndex], "--") {
			imageIndex++
		}
		if imageIndex >= len(fields) {
			continue
		}

		image := fields[imageIndex]
		record := DockerfileFromDigest{Line: index + 1, Image: image}
		_, digest, ok := strings.Cut(image, "@")
		if ok && strings.HasPrefix(digest, "sha256:") {
			record.Digest = digest
		}
		if imageIndex+2 < len(fields) && strings.EqualFold(fields[imageIndex+1], "AS") {
			record.Stage = fields[imageIndex+2]
		}
		records = append(records, record)
	}

	return records
}

// ParseDockerfileFromDigests extracts digest-pinned FROM references using a
// deliberately small line parser. It does not attempt full Dockerfile parsing.
func ParseDockerfileFromDigests(contents string) []DockerfileFromDigest {
	froms := ParseDockerfileFroms(contents)
	records := make([]DockerfileFromDigest, 0)
	for _, from := range froms {
		if from.Digest != "" {
			records = append(records, from)
		}
	}

	return records
}
