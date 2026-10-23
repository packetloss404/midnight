package registry

import "testing"

func TestParseImageReferenceValid(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref, err := ParseImageReference("registry.local:5000/team/app:v1.2.3@" + digest)
	if err != nil {
		t.Fatalf("ParseImageReference() error = %v", err)
	}

	if ref.Registry != "registry.local:5000" || ref.Repository != "team/app" || ref.Tag != "v1.2.3" || ref.Digest != digest {
		t.Fatalf("ParseImageReference() = %#v", ref)
	}
}

func TestParseImageReferenceRejectsMutableImplicitLatest(t *testing.T) {
	if _, err := ParseImageReference("team/app"); err == nil {
		t.Fatal("ParseImageReference() error = nil, want missing tag or digest error")
	}
}

func TestParseImageReferenceRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		"https://registry.local/team/app:v1",
		"Team/App:v1",
		"registry.local/team/app:bad tag",
		"registry.local/team/app@sha256:nothex",
		"registry.local:/team/app:v1",
	} {
		if _, err := ParseImageReference(value); err == nil {
			t.Fatalf("ParseImageReference(%q) error = nil, want error", value)
		}
	}
}

func TestCheckImmutableReleaseTagRefusesMovedReleaseTag(t *testing.T) {
	err := CheckImmutableReleaseTag(
		[]ImageTag{{Repository: "app", Name: "v1.2.3", Digest: "sha256:old"}},
		ImageTag{Repository: "app", Name: "v1.2.3", Digest: "sha256:new"},
	)
	if err == nil {
		t.Fatal("CheckImmutableReleaseTag() error = nil, want immutable tag error")
	}
}

func TestCheckImmutableReleaseTagAllowsSameDigestAndMutableTag(t *testing.T) {
	existing := []ImageTag{{Repository: "app", Name: "v1.2.3", Digest: "sha256:same"}}
	if err := CheckImmutableReleaseTag(existing, ImageTag{Repository: "app", Name: "v1.2.3", Digest: "sha256:same"}); err != nil {
		t.Fatalf("CheckImmutableReleaseTag(same digest) error = %v", err)
	}
	if err := CheckImmutableReleaseTag(existing, ImageTag{Repository: "app", Name: "dev", Digest: "sha256:new"}); err != nil {
		t.Fatalf("CheckImmutableReleaseTag(mutable tag) error = %v", err)
	}
}
