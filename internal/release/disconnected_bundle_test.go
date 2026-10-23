package release

import "testing"

func validDisconnectedBundlePlanInput() DisconnectedBundlePlan {
	return DisconnectedBundlePlan{
		Release:       "v4.0.0",
		GeneratedAt:   "2026-04-26T00:00:00Z",
		APT:           APTMirror{RepositoryURL: "file:///bundle/apt", Suites: []string{"noble", "bookworm"}, KeyFingerprint: "0123456789ABCDEF"},
		OCI:           OCIMirror{Registry: "registry.local/midnight", ImageRefs: []string{"registry.local/midnight/server@sha256:abc"}, DigestOnly: true},
		Docs:          DocsBundle{Included: true, Paths: []string{"docs/install/disconnected-bundle.md", "docs/runbooks"}},
		Vulnerability: VulnerabilityDB{Included: true, UpdatedAt: "2026-04-26T00:00:00Z"},
		Signatures:    SignatureSet{ManifestSHA256: "abc123", CosignBundle: "bundle.cosign", GPGDetachedSig: "manifest.json.asc"},
	}
}

func TestNewDisconnectedBundlePlanReady(t *testing.T) {
	plan := NewDisconnectedBundlePlan(validDisconnectedBundlePlanInput())
	if plan.SchemaVersion != DisconnectedBundlePlanSchemaVersion {
		t.Fatalf("SchemaVersion = %q", plan.SchemaVersion)
	}
	if plan.Status != BundleStatusReady || len(plan.Issues) != 0 {
		t.Fatalf("Status = %q issues = %#v, want ready/no issues", plan.Status, plan.Issues)
	}
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		t.Fatalf("ValidateDisconnectedBundlePlan() error = %v", err)
	}
}

func TestNewDisconnectedBundlePlanBlocksMissingDigestPinning(t *testing.T) {
	input := validDisconnectedBundlePlanInput()
	input.OCI.DigestOnly = false
	plan := NewDisconnectedBundlePlan(input)
	if plan.Status != BundleStatusBlocked {
		t.Fatalf("Status = %q, want blocked", plan.Status)
	}
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		t.Fatalf("ValidateDisconnectedBundlePlan() error = %v", err)
	}
	found := false
	for _, issue := range plan.Issues {
		if issue.Field == "oci.digest_only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %#v, want oci.digest_only", plan.Issues)
	}
}

func TestNewDisconnectedBundlePlanBlocksTagBasedImageRefs(t *testing.T) {
	input := validDisconnectedBundlePlanInput()
	input.OCI.ImageRefs = []string{"registry.local/midnight/server:v4.0.0", " "}
	plan := NewDisconnectedBundlePlan(input)
	if plan.Status != BundleStatusBlocked {
		t.Fatalf("Status = %q, want blocked", plan.Status)
	}
	foundTagged := false
	foundEmpty := false
	for _, issue := range plan.Issues {
		switch {
		case issue.Field == "oci.image_refs[0]" && issue.Message == "OCI image reference must not be empty":
			foundEmpty = true
		case issue.Field == "oci.image_refs[1]" && issue.Message == "OCI image reference must include a sha256 digest":
			foundTagged = true
		}
	}
	if !foundTagged || !foundEmpty {
		t.Fatalf("issues = %#v, want per-image-ref digest and empty ref issues", plan.Issues)
	}
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		t.Fatalf("ValidateDisconnectedBundlePlan() error = %v", err)
	}
}

func TestDisconnectedBundlePlanAllowsNeedsReviewForMissingVulnDB(t *testing.T) {
	input := validDisconnectedBundlePlanInput()
	input.Vulnerability = VulnerabilityDB{}
	plan := NewDisconnectedBundlePlan(input)
	if plan.Status != BundleStatusNeedsReview {
		t.Fatalf("Status = %q, want needs-review", plan.Status)
	}
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		t.Fatalf("ValidateDisconnectedBundlePlan() error = %v", err)
	}
}

func TestDisconnectedBundlePlanStrictJSONRoundTrip(t *testing.T) {
	plan := NewDisconnectedBundlePlan(validDisconnectedBundlePlanInput())
	data, err := MarshalDisconnectedBundlePlanJSON(plan)
	if err != nil {
		t.Fatalf("MarshalDisconnectedBundlePlanJSON() error = %v", err)
	}
	got, err := UnmarshalDisconnectedBundlePlanJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalDisconnectedBundlePlanJSON() error = %v", err)
	}
	if got.SchemaVersion != DisconnectedBundlePlanSchemaVersion || got.Release != plan.Release {
		t.Fatalf("round trip = %#v", got)
	}
	bad := append(data[:len(data)-2], []byte(",\n  \"unknown\": true\n}")...)
	if _, err := UnmarshalDisconnectedBundlePlanJSON(bad); err == nil {
		t.Fatalf("UnmarshalDisconnectedBundlePlanJSON() error = nil, want unknown field error")
	}
}
