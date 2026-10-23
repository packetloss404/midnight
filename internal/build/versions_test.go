package build

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadVersionsReadsBuildKitPin(t *testing.T) {
	versions, err := LoadVersions(filepath.Join("..", "..", "versions.yaml"))
	if err != nil {
		t.Fatalf("LoadVersions() error = %v", err)
	}

	if versions.BuildKit != RequiredBuildKitVersion {
		t.Fatalf("BuildKit = %q, want %q", versions.BuildKit, RequiredBuildKitVersion)
	}
	if versions.Railpack != RequiredRailpackVersion {
		t.Fatalf("Railpack = %q, want %q", versions.Railpack, RequiredRailpackVersion)
	}
	if versions.RailpackFrontend != RequiredRailpackFrontend {
		t.Fatalf("RailpackFrontend = %q, want %q", versions.RailpackFrontend, RequiredRailpackFrontend)
	}
	if versions.Distribution != RequiredDistributionVersion {
		t.Fatalf("Distribution = %q, want %q", versions.Distribution, RequiredDistributionVersion)
	}
	if versions.Registry() != RequiredDistributionVersion {
		t.Fatalf("Registry alias = %q, want %q", versions.Registry(), RequiredDistributionVersion)
	}
	if versions.Traefik != RequiredTraefikVersion {
		t.Fatalf("Traefik = %q, want %q", versions.Traefik, RequiredTraefikVersion)
	}
	if versions.Lego != RequiredLegoVersion {
		t.Fatalf("Lego = %q, want %q", versions.Lego, RequiredLegoVersion)
	}
	if versions.Cosign != RequiredCosignVersion {
		t.Fatalf("Cosign = %q, want %q", versions.Cosign, RequiredCosignVersion)
	}
	if versions.CosignLinuxAMD64Digest != RequiredCosignLinuxAMD64Digest {
		t.Fatalf("CosignLinuxAMD64Digest = %q, want %q", versions.CosignLinuxAMD64Digest, RequiredCosignLinuxAMD64Digest)
	}
	if versions.CosignLinuxARM64Digest != RequiredCosignLinuxARM64Digest {
		t.Fatalf("CosignLinuxARM64Digest = %q, want %q", versions.CosignLinuxARM64Digest, RequiredCosignLinuxARM64Digest)
	}
}

func TestValidateBuildKitVersionMismatch(t *testing.T) {
	err := ValidateBuildKitVersion("v0.0.0")
	if err == nil {
		t.Fatal("ValidateBuildKitVersion() error = nil, want mismatch")
	}

	var mismatch BuildKitVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error type = %T, want BuildKitVersionMismatchError", err)
	}
}

func TestValidateDistributionVersionAcceptsPin(t *testing.T) {
	if err := ValidateDistributionVersion(RequiredDistributionVersion); err != nil {
		t.Fatalf("ValidateDistributionVersion(%q) error = %v, want nil", RequiredDistributionVersion, err)
	}
}

func TestValidateDistributionVersionMismatch(t *testing.T) {
	err := ValidateDistributionVersion("v0.0.0")
	if err == nil {
		t.Fatal("ValidateDistributionVersion() error = nil, want mismatch")
	}

	var mismatch DistributionVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error type = %T, want DistributionVersionMismatchError", err)
	}
}

func TestParsePinnedBinaryVersion(t *testing.T) {
	tests := map[string]string{
		"buildctl github.com/moby/buildkit v0.28.1 deadbeef":              "v0.28.1",
		"registry github.com/distribution/distribution/v3 3.0.0 deadbeef": "v3.0.0",
		"not a version": "",
	}
	for input, want := range tests {
		if got := ParsePinnedBinaryVersion(input); got != want {
			t.Fatalf("ParsePinnedBinaryVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateCosignVersionAcceptsPin(t *testing.T) {
	if err := ValidateCosignVersion(RequiredCosignVersion); err != nil {
		t.Fatalf("ValidateCosignVersion(%q) error = %v, want nil", RequiredCosignVersion, err)
	}
}

func TestValidateCosignVersionMismatch(t *testing.T) {
	err := ValidateCosignVersion("v0.0.0")
	if err == nil {
		t.Fatal("ValidateCosignVersion() error = nil, want mismatch")
	}
	var mismatch CosignVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error type = %T, want CosignVersionMismatchError", err)
	}
	if mismatch.Required != RequiredCosignVersion {
		t.Fatalf("mismatch.Required = %q, want %q", mismatch.Required, RequiredCosignVersion)
	}
	if mismatch.Actual != "v0.0.0" {
		t.Fatalf("mismatch.Actual = %q, want %q", mismatch.Actual, "v0.0.0")
	}
}
