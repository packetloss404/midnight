// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"strings"
	"testing"
)

func TestSafeTemplateFailureReasonIsBoundedAndRedactsCredentialShapes(t *testing.T) {
	if got := safeTemplateFailureReason("dial failed password=super-secret"); strings.Contains(got, "super-secret") {
		t.Fatalf("credential-shaped reason leaked: %q", got)
	}
	got := safeTemplateFailureReason(strings.Repeat("x", 700))
	if len(got) != 512 {
		t.Fatalf("bounded reason length=%d, want 512", len(got))
	}
}
