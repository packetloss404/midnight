//go:build cosign_sdk

package docker

import (
	"context"
	"os"
	"testing"
)

// TestCosignParitySDKSide drives the canonical parity table against
// CosignSDKVerifier. The build-tagged lane must fail if it is still
// wired to the skeleton, unless an explicit local override asks for
// skeleton acceptance while the SDK implementation is being developed.
//
// When the v1 PR fills in CosignSDKVerifier.Verify (per
// agile/handoff/governance/COSIGN_SDK_BOUNDARY.md "v1 follow-up
// plan"), this test stops skipping and asserts byte-for-byte parity
// with CosignExecVerifier on the same canonicalParityCases() table.
func TestCosignParitySDKSide(t *testing.T) {
	for _, tc := range canonicalParityCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			v := CosignSDKVerifier{}
			err := v.Verify(context.Background(), tc.imageRef)
			if IsCosignSDKNotImplemented(err) {
				if os.Getenv("MIDNIGHT_ALLOW_COSIGN_SDK_SKELETON") == "1" {
					t.Skipf("SDK side is explicitly allowed to remain skeleton by MIDNIGHT_ALLOW_COSIGN_SDK_SKELETON=1")
					return
				}
				t.Fatalf("CosignSDKVerifier is still the skeleton; set MIDNIGHT_ALLOW_COSIGN_SDK_SKELETON=1 only for an explicit local skeleton lane")
			}
			// When the v1 PR lands, the assertions below take over.
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("Verify(%s) = %v, want nil", tc.imageRef, err)
				}
				return
			}
			dockerErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if dockerErr.Code != tc.wantCode {
				t.Fatalf("Code = %s, want %s", dockerErr.Code, tc.wantCode)
			}
		})
	}
}
