package build

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// RequiredBuildKitVersion pins the BuildKit binary version Midnight expects.
// Operators must install a `buildctl` binary whose `--version` output matches
// this string; the validator rejects mismatched binaries before any solve
// starts so deployment snapshots can record an exact builder version per
// ADR-008.
const RequiredBuildKitVersion = "v0.28.1"

// RequiredRailpackVersion pins the source-analysis CLI used to generate build
// plans. The matching frontend below MUST move in lockstep with this value.
const RequiredRailpackVersion = "v0.23.0"

// RequiredRailpackFrontend is the immutable multi-platform manifest consumed
// by BuildKit's gateway.v0 frontend. Keeping the human-readable version tag
// alongside the digest makes incident output understandable without allowing
// the registry tag to drift.
const RequiredRailpackFrontend = "ghcr.io/railwayapp/railpack-frontend:v0.23.0@sha256:ba4c430961d9ee3215c64807727a4b11e2198daac31250e9db9eaf9cee4624d6"

// RequiredDistributionVersion pins the OCI registry binary
// (github.com/distribution/distribution) Midnight launches from
// cmd/midnight-registry.
const RequiredDistributionVersion = "v3.0.0"

// RequiredTraefikVersion pins the Traefik OSS binary operators must install.
// The Traefik file-provider directory shape and middleware vocabulary are
// version-sensitive; the gate refuses to publish ingress config to a
// mismatched Traefik to keep the ABI honest.
const RequiredTraefikVersion = "v3.1.5"

// RequiredLegoVersion pins the Lego ACME library. It is also enforced by
// `go mod tidy` because Lego is a Go dependency, but pinning here keeps the
// integration-test runner honest if Lego is installed as a side-binary.
const RequiredLegoVersion = "v4.17.4"

// RequiredCosignVersion pins the `cosign` binary version Midnight expects
// the release workflow to install (via sigstore/cosign-installer) and the
// agent-side CosignExecVerifier to fork-exec when signature enforcement is
// enabled. The Go SDK import is binding-deferred per
// `agile/handoff/governance/COSIGN_SDK_BOUNDARY.md`. Operators that mirror
// the binary must re-verify against RequiredCosignLinuxAMD64Digest /
// RequiredCosignLinuxARM64Digest below.
const RequiredCosignVersion = "v2.6.3"

// RequiredCosignLinuxAMD64Digest is the SHA-256 digest of the
// `cosign-linux-amd64` asset published with the v2.6.3 release. Operators
// that download the binary out-of-band MUST verify this digest before
// trusting the result of any `cosign verify` invocation.
const RequiredCosignLinuxAMD64Digest = "sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4"

// RequiredCosignLinuxARM64Digest is the SHA-256 digest of the
// `cosign-linux-arm64` asset published with the v2.6.3 release.
const RequiredCosignLinuxARM64Digest = "sha256:b7c23659a50a59fd8eec44b87188e9062157d0c87796cac7b38727e5390c4917"

// Versions contains the Phase 1 build/registry/ingress compatibility pins
// plus the cosign binary pin and per-architecture SHA-256 digests consumed
// by the release pipeline and the agent-side fork-exec verifier.
type Versions struct {
	BuildKit               string
	Railpack               string
	RailpackFrontend       string
	Distribution           string
	Traefik                string
	Lego                   string
	Cosign                 string
	CosignLinuxAMD64Digest string
	CosignLinuxARM64Digest string
}

// Registry is a backwards-compatible accessor that returns the pinned
// distribution version. Existing Phase 0 callers and tests read from
// Versions.Registry; Lane 3 introduced the Distribution field but kept the
// Registry alias to avoid churn.
func (v Versions) Registry() string {
	return v.Distribution
}

// BuildKitVersionMismatchError is returned when the installed BuildKit version
// does not match the Phase 1 compatibility pin.
type BuildKitVersionMismatchError struct {
	Required string
	Actual   string
}

func (e BuildKitVersionMismatchError) Error() string {
	return fmt.Sprintf("buildkit version mismatch: required %s, actual %s", e.Required, e.Actual)
}

// DistributionVersionMismatchError is returned when the installed registry
// binary does not match the Phase 1 compatibility pin.
type DistributionVersionMismatchError struct {
	Required string
	Actual   string
}

func (e DistributionVersionMismatchError) Error() string {
	return fmt.Sprintf("distribution version mismatch: required %s, actual %s", e.Required, e.Actual)
}

// ValidateBuildKitVersion compares an actual buildctl version (typically
// extracted from `buildctl --version` output) against the pinned version.
func ValidateBuildKitVersion(actual string) error {
	if actual != RequiredBuildKitVersion {
		return BuildKitVersionMismatchError{Required: RequiredBuildKitVersion, Actual: actual}
	}

	return nil
}

// ValidateDistributionVersion compares an actual registry version against the
// pinned distribution version.
func ValidateDistributionVersion(actual string) error {
	if actual != RequiredDistributionVersion {
		return DistributionVersionMismatchError{Required: RequiredDistributionVersion, Actual: actual}
	}

	return nil
}

// ParsePinnedBinaryVersion extracts the first version-looking token from a
// typical fork-exec `--version` response. It accepts both `v1.2.3` and
// `1.2.3`, normalizing the latter with a leading `v`.
func ParsePinnedBinaryVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			candidate := strings.Trim(strings.TrimSuffix(field, ","), "\"'")
			if candidate == "" {
				continue
			}
			trimmed := strings.TrimPrefix(candidate, "v")
			if looksLikeSemver(trimmed) {
				return "v" + trimmed
			}
		}
	}
	return ""
}

func looksLikeSemver(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// LoadVersions reads the repository versions.yaml contract without taking a
// YAML dependency. The file uses single-line `key: value` pairs only.
func LoadVersions(path string) (Versions, error) {
	file, err := os.Open(path)
	if err != nil {
		return Versions{}, err
	}
	defer func() { _ = file.Close() }()

	versions := Versions{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		v := strings.Trim(strings.TrimSpace(value), "\"")
		switch strings.TrimSpace(key) {
		case "buildkit":
			versions.BuildKit = v
		case "railpack":
			versions.Railpack = v
		case "railpack_frontend":
			versions.RailpackFrontend = v
		case "distribution", "registry":
			versions.Distribution = v
		case "traefik":
			versions.Traefik = v
		case "lego":
			versions.Lego = v
		case "cosign":
			versions.Cosign = v
		case "cosign_linux_amd64_digest":
			versions.CosignLinuxAMD64Digest = v
		case "cosign_linux_arm64_digest":
			versions.CosignLinuxARM64Digest = v
		}
	}
	if err := scanner.Err(); err != nil {
		return Versions{}, err
	}
	if versions.BuildKit == "" {
		return Versions{}, fmt.Errorf("versions.yaml missing buildkit version")
	}
	if versions.Distribution == "" {
		return Versions{}, fmt.Errorf("versions.yaml missing distribution version")
	}
	return versions, nil
}

// CosignVersionMismatchError is returned when the installed cosign binary
// version does not match the pin recorded in versions.yaml. The release
// workflow + the agent's reproducible-cosign-version assertion both surface
// this typed error so operators see one canonical failure shape.
type CosignVersionMismatchError struct {
	Required string
	Actual   string
}

func (e CosignVersionMismatchError) Error() string {
	return fmt.Sprintf("cosign version mismatch: required %s, actual %s", e.Required, e.Actual)
}

// ValidateCosignVersion compares an actual cosign --version (or `cosign
// version --json`'s `GitVersion` field) against the pinned version. The
// comparison is exact-string equality so a v2.6.3 vs v2.6.3+dirty drift
// surfaces as a hard mismatch.
func ValidateCosignVersion(actual string) error {
	if actual != RequiredCosignVersion {
		return CosignVersionMismatchError{Required: RequiredCosignVersion, Actual: actual}
	}
	return nil
}
